//go:build e2e
// +build e2e

/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/charg/technitium-operator/test/utils"
)

// clusteredClusterName is the multi-replica TechnitiumCluster this file
// provisions and drives through real cluster init/join, distinct from
// zone_test.go's single-replica clusterName.
const clusteredClusterName = "e2e-dns-cluster"

// deployClusteredTechnitiumCluster applies a multi-replica TechnitiumCluster
// and waits for the operator to provision every pod, bootstrap each one, and
// then drive real cluster init/join (POST /api/admin/cluster/init on the
// primary, /api/admin/cluster/initJoin on every secondary) until the
// resource reports Ready with every node connected. A real Technitium
// cluster runs here, so a Ready status with status.members == "N/N" proves
// the operator drove actual server-to-server clustering, not just N
// independent standalone instances. dnsServerDomain is set because one shared
// server domain once gave every node the same Technitium node name, so every
// join after the first was refused.
func deployClusteredTechnitiumCluster(name string, replicas int) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: TechnitiumCluster
metadata:
  name: %s
spec:
  image: technitium/dns-server:15.4.0
  replicas: %d
  dnsServerDomain: dns.e2e.test
  storage:
    size: 1Gi
`, name, replicas)
	applyManifest("cluster-"+name, manifest)

	// Clustering is the slowest step in the suite: it only starts once every
	// pod is individually Ready and bootstrapped, then still needs one init
	// call plus (replicas-1) initJoin calls to actually converge
	// server-side, on top of the image pull and first-boot budget a
	// single-replica cluster already spends.
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "technitiumcluster", name,
			"-o", "jsonpath={.status.phase}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("Ready"), "TechnitiumCluster phase not Ready yet")
	}, 10*time.Minute, 5*time.Second).Should(Succeed())

	wantMembers := fmt.Sprintf("%d/%d", replicas, replicas)
	cmd := exec.Command("kubectl", "get", "technitiumcluster", name,
		"-o", "jsonpath={.status.members}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(wantMembers), "not every node reports having joined the cluster")

	// Every node's own membership state should be Self (the primary) or
	// Connected (a joined secondary): Ready gates on exactly this, but
	// asserting it directly catches a regression that reaches Ready without
	// the primary's clusterNodes actually reflecting a live join.
	cmd = exec.Command("kubectl", "get", "technitiumcluster", name,
		"-o", "jsonpath={.status.nodes[*].state}")
	output, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	for _, state := range strings.Fields(output) {
		Expect(state).To(Or(Equal("Self"), Equal("Connected")),
			"node state %q is not a joined cluster state", state)
	}
}

// podIP returns a pod's current IP in the operator namespace.
func podIP(pod string) string {
	cmd := exec.Command("kubectl", "get", "pod", pod, "-n", namespace,
		"-o", "jsonpath={.status.podIP}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(output)
}

// podUID returns a pod's UID, which changes whenever the pod is recreated.
func podUID(pod string) string {
	cmd := exec.Command("kubectl", "get", "pod", pod, "-n", namespace,
		"-o", "jsonpath={.metadata.uid}")
	output, err := utils.Run(cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

// restartPodAndAwaitReconnect deletes a StatefulSet pod, waits for its
// replacement to come back Ready, then asserts the cluster converges again
// without manual intervention: the cluster phase returns to Ready and every
// Secondary is Connected in the Primary's clusterNodes (surfaced as
// status.nodes, which is rendered straight from the Primary's
// /api/admin/cluster/state). The replacement may get a new pod IP, which is
// the case this guards: the cluster pinned each node's IP at init/join time.
func restartPodAndAwaitReconnect(clusterName, pod string, replicas int) {
	oldUID, oldIP := podUID(pod), podIP(pod)
	Expect(oldUID).NotTo(BeEmpty())

	cmd := exec.Command("kubectl", "delete", "pod", pod, "-n", namespace, "--wait=true", "--timeout=120s")
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "failed to delete pod %s", pod)

	By(fmt.Sprintf("waiting for %s to be recreated and Ready (was %s)", pod, oldIP))
	Eventually(func(g Gomega) {
		g.Expect(podUID(pod)).NotTo(Or(BeEmpty(), Equal(oldUID)), "pod not recreated yet")
		cmd := exec.Command("kubectl", "get", "pod", pod, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("True"))
	}, 5*time.Minute, 3*time.Second).Should(Succeed())
	newIP := podIP(pod)
	_, _ = fmt.Fprintf(GinkgoWriter, "pod %s IP: %s -> %s\n", pod, oldIP, newIP)

	By("waiting for every node to be Self/Connected in the Primary's clusterNodes and the cluster Ready")
	// status.nodes can briefly still show the pre-restart view, so require
	// the converged state on several consecutive polls rather than one.
	stable := 0
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "technitiumcluster", clusterName,
			"-o", "jsonpath={.status.phase}|{.status.members}|{range .status.nodes[*]}{.name}={.state} {end}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		_, _ = fmt.Fprintf(GinkgoWriter, "cluster after restart of %s: %s\n", pod, output)
		parts := strings.SplitN(output, "|", 3)
		ok := len(parts) == 3 && parts[0] == "Ready" && parts[1] == fmt.Sprintf("%d/%d", replicas, replicas)
		if ok {
			for _, f := range strings.Fields(parts[2]) {
				state := f[strings.Index(f, "=")+1:]
				if state != "Self" && state != "Connected" {
					ok = false
				}
			}
		}
		if ok {
			stable++
		} else {
			stable = 0
		}
		g.Expect(stable).To(BeNumerically(">=", 3), "cluster not converged: %s", output)
	}, 8*time.Minute, 10*time.Second).Should(Succeed())
}

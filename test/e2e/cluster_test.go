//go:build e2e
// +build e2e

/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/charg/technitium-operator/test/testimage"
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
	deployClusteredTechnitiumClusterWithImage(name, replicas, testimage.TechnitiumImage)
}

// deployClusteredTechnitiumClusterWithImage is deployClusteredTechnitiumCluster
// for a specific Technitium image, which upgrade specs start on an older one.
func deployClusteredTechnitiumClusterWithImage(name string, replicas int, image string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: TechnitiumCluster
metadata:
  name: %s
spec:
  image: %s
  replicas: %d
  dnsServerDomain: dns.e2e.test
  storage:
    size: 1Gi
`, name, image, replicas)
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

	// The pods gate readiness on Technitium's health check (an exec curl),
	// not a bare TCP check, and a pod passing that probe reports Ready.
	pod := name + "-0"
	cmd = exec.Command("kubectl", "get", "pod", pod,
		"-o", "jsonpath={.spec.containers[0].readinessProbe.exec.command[2]}")
	output, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(ContainSubstring("/api/dnsClient/healthCheck"))
	cmd = exec.Command("kubectl", "get", "pod", pod,
		"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
	output, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal("True"), "pod %s is not Ready under the health check probe", pod)

	expectReportedVersionOf(name, image)
}

// expectReportedVersion asserts every node of the named TechnitiumCluster
// reports the version of the deployed image tag, and that the cluster-wide
// status.version summary (the Version print column) equals it. The nodes'
// versions come from the live Technitium servers, so a match proves the
// operator read them rather than echoing the spec.
func expectReportedVersion(name string) {
	expectReportedVersionOf(name, testimage.TechnitiumImage)
}

// expectReportedVersionOf is expectReportedVersion for a specific image.
func expectReportedVersionOf(name, image string) {
	wantVersion := strings.TrimPrefix(image, "technitium/dns-server:")

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "technitiumcluster", name,
			"-o", "jsonpath={.status.nodes[*].version}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		versions := strings.Fields(output)
		g.Expect(versions).NotTo(BeEmpty(), "no node reports a version yet")
		for _, v := range versions {
			g.Expect(v).To(Equal(wantVersion), "node version does not match the image tag")
		}

		cmd = exec.Command("kubectl", "get", "technitiumcluster", name,
			"-o", "jsonpath={.status.version}")
		output, err = utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal(wantVersion), "status.version does not equal the image tag")
	}, 2*time.Minute, 5*time.Second).Should(Succeed())
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

	// The Primary's view alone is not enough: after a Primary restart the
	// Primary can see every Secondary Connected (it dials them) while every
	// Secondary still holds the Primary's old IP and sees it Unreachable, so
	// config sync is silently broken. Assert every node's own view too: each
	// member is Self/Connected and recorded at its pod's current IP.
	By("verifying every node's own view lists every member as Connected at its current pod IP")
	Eventually(func(g Gomega) {
		for i := 0; i < replicas; i++ {
			observer := fmt.Sprintf("%s-%d", clusterName, i)
			view := nodeOwnClusterView(clusterName, observer)
			g.Expect(view).To(HaveLen(replicas), "%s sees %d members", observer, len(view))
			for j := 0; j < replicas; j++ {
				member := fmt.Sprintf("%s-%d", clusterName, j)
				var found bool
				for _, n := range view {
					if !strings.HasPrefix(n.Name, member+".") {
						continue
					}
					found = true
					g.Expect(n.State).To(Or(Equal("Self"), Equal("Connected")),
						"%s sees %s as %s", observer, member, n.State)
					g.Expect(n.IPAddresses).To(ConsistOf(podIP(member)),
						"%s has a stale IP recorded for %s", observer, member)
				}
				g.Expect(found).To(BeTrue(), "%s does not list %s", observer, member)
			}
		}
	}, 5*time.Minute, 10*time.Second).Should(Succeed())
}

// clusterViewNode is one member of /api/admin/cluster/state as a single node
// reports it.
type clusterViewNode struct {
	Name        string   `json:"name"`
	State       string   `json:"state"`
	IPAddresses []string `json:"ipAddresses"`
}

// nodeOwnClusterView asks a single Technitium pod for its own
// /api/admin/cluster/state by exec'ing into it (the image ships bash, so the
// call is made over /dev/tcp) and returns that node's view of the members.
func nodeOwnClusterView(clusterName, pod string) []clusterViewNode {
	secret := clusterName + "-admin"
	get := func(key string) string {
		cmd := exec.Command("kubectl", "get", "secret", secret, "-n", namespace,
			"-o", "jsonpath={.data."+key+"}")
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
		Expect(err).NotTo(HaveOccurred())
		return string(decoded)
	}
	script := `
http_get() {
  exec 3<>/dev/tcp/127.0.0.1/5380
  printf 'GET %s HTTP/1.0\r\nHost: localhost\r\n\r\n' "$1" >&3
  cat <&3
  exec 3<&-
}
token=$(http_get "/api/user/login?user=$TUSER&pass=$TPASS" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)
http_get "/api/admin/cluster/state?token=$token" | tail -n 1
`
	cmd := exec.Command("kubectl", "exec", pod, "-n", namespace, "--",
		"env", "TUSER="+get("username"), "TPASS="+get("password"), "bash", "-c", script)
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "failed to read cluster state from %s", pod)

	var body struct {
		Response struct {
			ClusterNodes []clusterViewNode `json:"clusterNodes"`
		} `json:"response"`
	}
	Expect(json.Unmarshal([]byte(strings.TrimSpace(out)), &body)).To(Succeed(), "bad state body from %s: %q", pod, out)
	_, _ = fmt.Fprintf(GinkgoWriter, "%s own view: %+v\n", pod, body.Response.ClusterNodes)
	return body.Response.ClusterNodes
}

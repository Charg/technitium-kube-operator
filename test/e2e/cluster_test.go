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
// independent standalone instances.
func deployClusteredTechnitiumCluster(name string, replicas int) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: TechnitiumCluster
metadata:
  name: %s
spec:
  image: technitium/dns-server:15.4.0
  replicas: %d
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

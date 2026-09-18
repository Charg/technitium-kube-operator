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
	"time"

	. "github.com/onsi/gomega"

	"github.com/charg/technitium-operator/test/utils"
)

// applyDHCPScope creates a DHCPScope CR, in namespace, targeting the given
// TechnitiumCluster. It uses a private range (192.168.222.0/24) chosen not to
// collide with the Kind/pod network, plus one static reservation.
//
// enabled is left false: enabling a DHCP scope whose subnet matches no server
// network interface can fail on some Technitium builds, and the Kind node
// running the e2e TechnitiumCluster has no interface on 192.168.222.0/24.
// Leaving the scope disabled still exercises create, reservation converge,
// and delete without depending on an enable call the test environment cannot
// guarantee will succeed.
func applyDHCPScope(name, namespace, serverRef string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: DHCPScope
metadata:
  name: %s
  namespace: %s
spec:
  serverRef:
    name: %s
  scopeName: e2e-scope
  startingAddress: 192.168.222.100
  endingAddress: 192.168.222.200
  subnetMask: 255.255.255.0
  routerAddress: 192.168.222.1
  enabled: false
  reservations:
  - hardwareAddress: "00:11:22:33:44:55"
    ipAddress: 192.168.222.150
`, name, namespace, serverRef)

	// The admission webhook can still be settling, so retry until the apply is
	// accepted rather than failing on a transient dial error.
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "apply", "-f", writeManifest("dhcpscope-"+name, manifest))
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, time.Minute, 3*time.Second).Should(Succeed())
}

// verifyDHCPScopeReady polls until the named DHCPScope reports Ready, or fails
// the spec if it never does. Ready is reachable with enabled:false: the
// controller treats a disabled-by-spec scope that exists on the server as
// Ready.
func verifyDHCPScopeReady(name, namespace string) {
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "dhcpscope", name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("True"), "DHCPScope not Ready yet")
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
}

// deleteDHCPScopeAndVerifyGone deletes the named DHCPScope and waits for the
// finalizer-driven teardown to complete. With the default Delete policy the
// scope and its reservation are removed from the server before the resource
// itself is cleared; this confirms the resource is gone.
func deleteDHCPScopeAndVerifyGone(name, namespace string) {
	cmd := exec.Command("kubectl", "delete", "dhcpscope", name, "-n", namespace, "--wait=true", "--timeout=90s")
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "DHCPScope deletion did not complete")

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "dhcpscope", name, "-n", namespace)
		_, err := utils.Run(cmd)
		g.Expect(err).To(HaveOccurred(), "DHCPScope should no longer exist")
	}, time.Minute, 2*time.Second).Should(Succeed())
}

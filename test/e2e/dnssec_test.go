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

// applyDNSSEC creates a DNSSEC CR in namespace, targeting the given
// TechnitiumCluster and zone. It signs with ECDSA/P256 over NSEC3, values that
// are cheap for a fresh instance to generate and easy to read back off the
// server.
func applyDNSSEC(name, namespace, serverRef, zone string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: DNSSEC
metadata:
  name: %s
  namespace: %s
spec:
  serverRef:
    name: %s
  zone: %s
  algorithm: ECDSA
  curve: P256
  nsecType: NSEC3
`, name, namespace, serverRef, zone)

	// The admission webhook can still be settling, so retry until the apply is
	// accepted rather than failing on a transient dial error.
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "apply", "-f", writeManifest("dnssec-"+name, manifest))
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, time.Minute, 3*time.Second).Should(Succeed())
}

// verifyDNSSECReady polls until the named DNSSEC reports Ready and
// status.signed, or fails the spec if it never does.
func verifyDNSSECReady(name, namespace string) {
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "dnssec", name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("True"), "DNSSEC not Ready yet")
	}, 2*time.Minute, 2*time.Second).Should(Succeed())

	cmd := exec.Command("kubectl", "get", "dnssec", name, "-n", namespace, "-o", "jsonpath={.status.signed}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal("true"), "DNSSEC status.signed should be true")
}

// deleteDNSSECAndVerifyGone deletes the named DNSSEC and waits for the
// finalizer-driven teardown to complete. With the default Delete policy the
// zone is unsigned before the resource itself is cleared; this confirms the
// resource is gone.
func deleteDNSSECAndVerifyGone(name, namespace string) {
	cmd := exec.Command("kubectl", "delete", "dnssec", name, "-n", namespace, "--wait=true", "--timeout=90s")
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "DNSSEC deletion did not complete")

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "dnssec", name, "-n", namespace)
		_, err := utils.Run(cmd)
		g.Expect(err).To(HaveOccurred(), "DNSSEC should no longer exist")
	}, time.Minute, 2*time.Second).Should(Succeed())
}

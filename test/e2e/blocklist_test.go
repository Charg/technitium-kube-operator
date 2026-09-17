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

// applyBlocklist creates a Blocklist CR, in namespace, targeting the given
// TechnitiumCluster. It sets one block list URL plus a manual allowed and
// blocked domain, which are safe to apply to a fresh instance and easy to read
// back off the server. Blocklist is namespaced, so the caller supplies where it
// lands.
func applyBlocklist(name, namespace, serverRef, blockListURL, allowedDomain, blockedDomain string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: Blocklist
metadata:
  name: %s
  namespace: %s
spec:
  serverRef:
    name: %s
  enabled: true
  blockingType: NxDomain
  blockListUrls:
  - %s
  allowedDomains:
  - %s
  blockedDomains:
  - %s
`, name, namespace, serverRef, blockListURL, allowedDomain, blockedDomain)

	// The admission webhook can still be settling, so retry until the apply is
	// accepted rather than failing on a transient dial error.
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "apply", "-f", writeManifest("blocklist-"+name, manifest))
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, time.Minute, 3*time.Second).Should(Succeed())
}

// verifyBlocklistReady polls until the named Blocklist reports Ready, or fails
// the spec if it never does.
func verifyBlocklistReady(name, namespace string) {
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "blocklist", name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("True"), "Blocklist not Ready yet")
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
}

// deleteBlocklistAndVerifyGone deletes the named Blocklist and waits for the
// finalizer-driven teardown to complete. With the default Delete policy the
// managed URLs and domain overrides are removed from the server before the
// resource itself is cleared; this confirms the resource is gone.
func deleteBlocklistAndVerifyGone(name, namespace string) {
	cmd := exec.Command("kubectl", "delete", "blocklist", name, "-n", namespace, "--wait=true", "--timeout=90s")
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Blocklist deletion did not complete")

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "blocklist", name, "-n", namespace)
		_, err := utils.Run(cmd)
		g.Expect(err).To(HaveOccurred(), "Blocklist should no longer exist")
	}, time.Minute, 2*time.Second).Should(Succeed())
}

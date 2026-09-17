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

// applyServerSettings creates a ServerSettings CR, in namespace, targeting the
// given TechnitiumCluster. It sets forwarders, since those are safe to apply to
// a fresh instance and easy to read back off the server. ServerSettings is
// namespaced, so the caller supplies where it lands.
func applyServerSettings(name, namespace, serverRef, forwarder string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: ServerSettings
metadata:
  name: %s
  namespace: %s
spec:
  serverRef:
    name: %s
  forwarders:
    addresses:
    - %s
    protocol: Udp
`, name, namespace, serverRef, forwarder)

	// The admission webhook can still be settling, so retry until the apply is
	// accepted rather than failing on a transient dial error.
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "apply", "-f", writeManifest("serversettings-"+name, manifest))
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, time.Minute, 3*time.Second).Should(Succeed())
}

// verifyServerSettingsReady polls until the named ServerSettings reports Ready,
// or fails the spec if it never does.
func verifyServerSettingsReady(name, namespace string) {
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "serversettings", name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("True"), "ServerSettings not Ready yet")
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
}

// deleteServerSettingsAndVerifyGone deletes the named ServerSettings and waits
// for the finalizer-driven teardown to remove it. The applied settings are
// retained on the server (the only DeletionPolicy is Retain); this only
// confirms the resource itself is cleared.
func deleteServerSettingsAndVerifyGone(name, namespace string) {
	cmd := exec.Command("kubectl", "delete", "serversettings", name, "-n", namespace, "--wait=true", "--timeout=90s")
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "ServerSettings deletion did not complete")

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "serversettings", name, "-n", namespace)
		_, err := utils.Run(cmd)
		g.Expect(err).To(HaveOccurred(), "ServerSettings should no longer exist")
	}, time.Minute, 2*time.Second).Should(Succeed())
}

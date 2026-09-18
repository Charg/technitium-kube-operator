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

// applyDNSApp creates a DNSApp CR in namespace, targeting the given
// TechnitiumCluster. appName and downloadURL name a real store app: the spec
// installs it fresh on every run, so the app must tolerate a repeated
// install/uninstall cycle across e2e runs. DNSApp is namespaced, so the
// caller supplies where it lands.
func applyDNSApp(name, namespace, serverRef, appName, downloadURL string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: DNSApp
metadata:
  name: %s
  namespace: %s
spec:
  serverRef:
    name: %s
  appName: %q
  url: %s
`, name, namespace, serverRef, appName, downloadURL)

	// The admission webhook can still be settling, so retry until the apply is
	// accepted rather than failing on a transient dial error.
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "apply", "-f", writeManifest("dnsapp-"+name, manifest))
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, time.Minute, 3*time.Second).Should(Succeed())
}

// verifyDNSAppReady polls until the named DNSApp reports Ready, or fails the
// spec if it never does. Downloading and installing a real app package can
// take longer than the config-only CRDs, so this allows more time.
func verifyDNSAppReady(name, namespace string) {
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "dnsapp", name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("True"), "DNSApp not Ready yet")
	}, 3*time.Minute, 3*time.Second).Should(Succeed())
}

// deleteDNSAppAndVerifyGone deletes the named DNSApp and waits for the
// finalizer-driven teardown to complete. With the default Delete policy the
// app is uninstalled from the server before the resource itself is cleared;
// this confirms the resource is gone.
func deleteDNSAppAndVerifyGone(name, namespace string) {
	cmd := exec.Command("kubectl", "delete", "dnsapp", name, "-n", namespace, "--wait=true", "--timeout=90s")
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "DNSApp deletion did not complete")

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "dnsapp", name, "-n", namespace)
		_, err := utils.Run(cmd)
		g.Expect(err).To(HaveOccurred(), "DNSApp should no longer exist")
	}, time.Minute, 2*time.Second).Should(Succeed())
}

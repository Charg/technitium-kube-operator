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

// applyRecord creates an A Record CR, in namespace, targeting the given
// TechnitiumCluster and zone. Record is namespaced (unlike Zone and
// TechnitiumCluster), so the caller supplies where it lands.
func applyRecord(name, namespace, zoneName, domain, serverRef, ipAddress string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: Record
metadata:
  name: %s
  namespace: %s
spec:
  serverRef:
    name: %s
  zone: %s
  name: %s
  type: A
  data: %s
`, name, namespace, serverRef, zoneName, domain, ipAddress)

	// The admission webhook can still be settling, so retry until the apply is
	// accepted rather than failing on a transient dial error.
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "apply", "-f", writeManifest("record-"+name, manifest))
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, time.Minute, 3*time.Second).Should(Succeed())
}

// verifyRecordReady polls until the named Record reports Ready, or fails the
// spec if it never does.
func verifyRecordReady(name, namespace string) {
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "record", name, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("True"), "Record not Ready yet")
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
}

// deleteRecordAndVerifyGone deletes the named Record and waits for the
// finalizer-driven cleanup to remove it.
func deleteRecordAndVerifyGone(name, namespace string) {
	cmd := exec.Command("kubectl", "delete", "record", name, "-n", namespace, "--wait=true", "--timeout=90s")
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Record deletion did not complete")

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "record", name, "-n", namespace)
		_, err := utils.Run(cmd)
		g.Expect(err).To(HaveOccurred(), "Record should no longer exist")
	}, time.Minute, 2*time.Second).Should(Succeed())
}

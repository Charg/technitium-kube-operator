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
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/gomega"

	"github.com/charg/technitium-operator/test/utils"
)

// managerDeployment is the controller-manager Deployment created by `just deploy`.
const managerDeployment = "technitium-operator-controller-manager"

// clusterName is the TechnitiumCluster the operator provisions and reconciles Zones against.
const clusterName = "e2e-dns"

// deployTechnitiumCluster applies a TechnitiumCluster and waits for the operator to
// provision the StatefulSet, boot the server, mint an API token, and report Ready.
// The workload lands in the operator's namespace (POD_NAMESPACE). A real Technitium
// server runs here so a Ready Zone proves the operator drove the actual server API.
func deployTechnitiumCluster(name string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: TechnitiumCluster
metadata:
  name: %s
spec:
  image: technitium/dns-server:15.4.0
  storage:
    size: 1Gi
`, name)
	applyManifest("cluster-"+name, manifest)

	// Ready gates on image pull, first boot, and token creation, so it is the
	// slowest step in the suite. Docker Hub pulls dominate the budget in CI.
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "technitiumcluster", name,
			"-o", "jsonpath={.status.phase}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("Ready"), "TechnitiumCluster phase not Ready yet")
	}, 6*time.Minute, 5*time.Second).Should(Succeed())

	// A token in the admin Secret confirms the operator bootstrapped against the
	// real server, not just that the workload rolled out.
	cmd := exec.Command("kubectl", "get", "secret", name+"-admin", "-n", namespace,
		"-o", "jsonpath={.data.token}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "admin Secret not found")
	Expect(output).NotTo(BeEmpty(), "admin Secret carries no API token")
}

// waitForWebhookEndpointReady blocks until the webhook Service has a ready
// endpoint backing it.
func waitForWebhookEndpointReady() {
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "endpointslices.discovery.k8s.io", "-n", namespace,
			"-l", "kubernetes.io/service-name=technitium-operator-webhook-service",
			"-o", "jsonpath={range .items[*]}{range .endpoints[*]}{.conditions.ready}{'\\n'}{end}{end}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(ContainSubstring("true"), "webhook endpoint not ready")
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
}

// applyZone creates a Primary Zone CR targeting the given TechnitiumCluster.
func applyZone(name, zoneName, serverRef string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: Zone
metadata:
  name: %s
spec:
  serverRef:
    name: %s
  zoneName: %s
  type: Primary
`, name, serverRef, zoneName)
	path := writeManifest("zone-"+name, manifest)

	// The admission webhook can still be settling, so retry until the apply is
	// accepted rather than failing on a transient dial error.
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "apply", "-f", path)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, time.Minute, 3*time.Second).Should(Succeed())
}

// applyForwarderZone creates a Forwarder Zone CR with DNSSEC validation enabled,
// targeting the given TechnitiumCluster over the given transport protocol. The
// proxy fields are left out: the e2e cluster has no proxy server to exercise
// them against, so forwarderProxy is covered by unit and envtest coverage only.
func applyForwarderZone(name, zoneName, serverRef, forwarder, protocol string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: Zone
metadata:
  name: %s
spec:
  serverRef:
    name: %s
  zoneName: %s
  type: Forwarder
  forwarder: %s
  forwarderProtocol: %s
  dnssecValidation: true
`, name, serverRef, zoneName, forwarder, protocol)
	path := writeManifest("zone-"+name, manifest)

	// The admission webhook can still be settling, so retry until the apply is
	// accepted rather than failing on a transient dial error.
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "apply", "-f", path)
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
	}, time.Minute, 3*time.Second).Should(Succeed())
}

// applyManifest writes a manifest to a temp file and applies it once.
func applyManifest(name, manifest string) {
	cmd := exec.Command("kubectl", "apply", "-f", writeManifest(name, manifest))
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to apply manifest %s", name)
}

// writeManifest writes a manifest to a temp file and returns its path.
func writeManifest(name, manifest string) string {
	path := filepath.Join(os.TempDir(), name+".yaml")
	Expect(os.WriteFile(path, []byte(manifest), 0o644)).To(Succeed())
	return path
}

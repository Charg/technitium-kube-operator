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

// stubName is the in-cluster Technitium API stub the operator reconciles against.
const stubName = "technitium-stub"

// technitiumStubManifest deploys a tiny HTTP server that answers every Technitium
// API call with an ok envelope. That is all the reconciler needs to create a zone
// (an ok GetZoneOptions is treated as "already present"), keep it Ready, and
// delete it. Running a full Technitium server is unnecessary and slower for an
// end to end check of the operator's own behavior. The pod satisfies the
// namespace's restricted Pod Security Standard.
var technitiumStubManifest = fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    app: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[1]s
  template:
    metadata:
      labels:
        app: %[1]s
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: stub
        image: hashicorp/http-echo:1.0.0
        args:
        - -listen=:5380
        - '-text={"status":"ok"}'
        ports:
        - containerPort: 5380
        securityContext:
          readOnlyRootFilesystem: true
          allowPrivilegeEscalation: false
          capabilities:
            drop:
            - ALL
---
apiVersion: v1
kind: Service
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  selector:
    app: %[1]s
  ports:
  - port: 5380
    targetPort: 5380
`, stubName, namespace)

// deployTechnitiumStub applies the stub and waits for it to become available.
func deployTechnitiumStub() {
	applyManifest("technitium-stub", technitiumStubManifest)

	cmd := exec.Command("kubectl", "rollout", "status", "deployment/"+stubName,
		"-n", namespace, "--timeout=120s")
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Technitium stub did not become ready")
}

// pointOperatorAtStub rewrites the manager container args to reach the in-cluster
// stub, then waits for the new pod to roll out. The manager reads the credentials
// Secret and server URL at startup, so the change takes effect on restart.
func pointOperatorAtStub() {
	stubURL := fmt.Sprintf("http://%s.%s.svc:5380", stubName, namespace)
	args := []string{
		"--leader-elect",
		"--health-probe-bind-address=:8081",
		"--webhook-cert-path=/tmp/k8s-webhook-server/serving-certs",
		"--technitium-url=" + stubURL,
		"--technitium-credentials-secret=technitium-operator-credentials",
	}
	patch := fmt.Sprintf(
		`[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":[%s]}]`,
		quoteJSONList(args))

	cmd := exec.Command("kubectl", "patch", "deployment", managerDeployment,
		"-n", namespace, "--type=json", "-p", patch)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to repoint the operator at the stub")

	cmd = exec.Command("kubectl", "rollout", "status", "deployment/"+managerDeployment,
		"-n", namespace, "--timeout=120s")
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Operator did not roll out after repointing")

	// The webhook server in the new pod comes up shortly after the pod is Ready.
	// Wait for its Service to have a ready endpoint so an apply is not rejected by
	// a failurePolicy=Fail webhook that is momentarily unreachable.
	waitForWebhookEndpointReady()
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

// applyZone creates a Primary Zone CR with the given resource and zone names.
func applyZone(name, zoneName string) {
	manifest := fmt.Sprintf(`
apiVersion: dns.packet.fail/v1alpha1
kind: Zone
metadata:
  name: %s
spec:
  zoneName: %s
  type: Primary
`, name, zoneName)
	path := writeManifest("zone-"+name, manifest)

	// The admission webhook can still be settling right after a rollout, so retry
	// until the apply is accepted rather than failing on a transient dial error.
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

// quoteJSONList renders a string slice as a comma-separated list of JSON string
// literals for embedding in a JSON patch.
func quoteJSONList(items []string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%q", item)
	}
	return out
}

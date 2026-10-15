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

const (
	// externalDNSImage is the ExternalDNS release the spec runs against.
	externalDNSImage = "registry.k8s.io/external-dns/external-dns:v0.23.0"
	// externalDNSName names the Deployment, ServiceAccount, ClusterRole and
	// the namespaced Role and RoleBinding of the ExternalDNS install.
	externalDNSName = "e2e-external-dns"
	// externalDNSLabelSelector selects the Records the webhook owns.
	externalDNSLabelSelector = "app.kubernetes.io/managed-by=external-dns"
	// dnsClientPod is the pod dig runs in. It lives in the default namespace
	// because the operator namespace enforces the restricted pod security
	// level, which the dnsutils image does not satisfy.
	dnsClientPod = "e2e-dig"
	// dnsClientImage ships dig.
	dnsClientImage = "registry.k8s.io/e2e-test-images/jessie-dnsutils:1.7"
)

// deployExternalDNS installs ExternalDNS with the operator image running as the
// webhook provider sidecar, and grants its ServiceAccount access to Records in
// the operator namespace (the same Role the chart renders for
// externalDNS.rbac.create). Plain manifests are used so CI needs no helm.
func deployExternalDNS(serverRef, zoneName string) {
	manifest := fmt.Sprintf(`
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %[1]s
  namespace: %[2]s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %[1]s
rules:
  - apiGroups: [""]
    resources: ["services", "endpoints", "pods", "nodes", "namespaces"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: %[1]s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: %[1]s
subjects:
  - kind: ServiceAccount
    name: %[1]s
    namespace: %[2]s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %[1]s-webhook
  namespace: %[2]s
rules:
  - apiGroups: ["dns.packet.fail"]
    resources: ["records"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %[1]s-webhook
  namespace: %[2]s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %[1]s-webhook
subjects:
  - kind: ServiceAccount
    name: %[1]s
    namespace: %[2]s
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[2]s
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
      serviceAccountName: %[1]s
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: external-dns
          image: %[3]s
          args:
            - --source=service
            - --provider=webhook
            - --webhook-provider-url=http://localhost:8888
            - --domain-filter=%[5]s
            - --policy=sync
            - --registry=txt
            - --txt-owner-id=e2e
            - --interval=5s
            - --log-level=debug
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
        - name: webhook
          image: %[6]s
          imagePullPolicy: IfNotPresent
          args:
            - external-dns-webhook
            - --server-ref=%[4]s
            - --zone=%[5]s
            - --namespace=%[2]s
          readinessProbe:
            httpGet:
              path: /healthz
              port: 8080
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
`, externalDNSName, namespace, externalDNSImage, serverRef, zoneName, managerImage)
	applyManifest("external-dns", manifest)

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "rollout", "status", "deployment/"+externalDNSName,
			"-n", namespace, "--timeout=10s")
		_, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred(), "ExternalDNS Deployment not rolled out yet")
	}, 3*time.Minute, 5*time.Second).Should(Succeed())
}

// externalDNSLogs returns the logs of every container of the ExternalDNS pod,
// for failure diagnostics.
func externalDNSLogs() string {
	cmd := exec.Command("kubectl", "logs", "deployment/"+externalDNSName, "-n", namespace,
		"--all-containers", "--tail=200")
	out, err := utils.Run(cmd)
	if err != nil {
		return err.Error()
	}
	return out
}

// removeExternalDNS deletes everything deployExternalDNS created.
func removeExternalDNS() {
	for _, args := range [][]string{
		{"delete", "deployment", externalDNSName, "-n", namespace, "--ignore-not-found"},
		{"delete", "rolebinding,role", externalDNSName + "-webhook", "-n", namespace, "--ignore-not-found"},
		{"delete", "serviceaccount", externalDNSName, "-n", namespace, "--ignore-not-found"},
		{"delete", "clusterrolebinding,clusterrole", externalDNSName, "--ignore-not-found"},
	} {
		_, _ = utils.Run(exec.Command("kubectl", args...))
	}
}

// applyExternalDNSService creates a ClusterIP Service in the default namespace
// that ExternalDNS publishes as host at ip. Kind has no LoadBalancer, so the
// target annotation supplies the address.
func applyExternalDNSService(name, host, ip string) {
	manifest := fmt.Sprintf(`
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: default
  annotations:
    external-dns.kubernetes.io/hostname: %s
    external-dns.kubernetes.io/target: %s
spec:
  type: ClusterIP
  ports:
    - port: 80
`, name, host, ip)
	applyManifest("edns-svc-"+name, manifest)
}

// externalDNSRecords lists the managed Records as "type name ready" lines.
func externalDNSRecords() ([]string, error) {
	cmd := exec.Command("kubectl", "get", "records", "-n", namespace, "-l", externalDNSLabelSelector,
		"-o", `jsonpath={range .items[*]}{.spec.type} {.spec.name} `+
			`{.status.conditions[?(@.type=='Ready')].status}{"\n"}{end}`)
	out, err := utils.Run(cmd)
	if err != nil {
		return nil, err
	}
	return utils.GetNonEmptyLines(out), nil
}

// deployDNSClient starts a long-lived pod to run dig from.
func deployDNSClient() {
	cmd := exec.Command("kubectl", "run", dnsClientPod, "-n", "default", "--restart=Never",
		"--image="+dnsClientImage, "--command", "--", "sleep", "3600")
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to start the dig pod")
	cmd = exec.Command("kubectl", "wait", "pod/"+dnsClientPod, "-n", "default",
		"--for=condition=Ready", "--timeout=3m")
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "dig pod never became Ready")
}

// dig queries the Technitium client Service for name/rrType and returns the
// trimmed short answer.
func dig(serverRef, name, rrType string) (string, error) {
	cmd := exec.Command("kubectl", "exec", dnsClientPod, "-n", "default", "--", "dig", "+short", "+time=3", "+tries=1",
		fmt.Sprintf("@%s.%s.svc", serverRef, namespace), name, rrType)
	out, err := utils.Run(cmd)
	return strings.TrimSpace(out), err
}

/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

// clusterPollInterval is how often a not-yet-ready workload is re-checked.
// It is short because readiness only shows up once kubelet reports the pod
// ready, and we want that observed quickly rather than on the drift cadence.
const clusterPollInterval = 15 * time.Second

// clusterDriftInterval is how long to wait before re-reconciling a workload
// that is already up. Hand edits to the Deployment or Services (kubectl edit,
// another controller) are only corrected on this cadence.
const clusterDriftInterval = 5 * time.Minute

// adminSecretUsernameKey and adminSecretPasswordKey are the keys the operator
// writes into the generated admin Secret. internal/config/credentials.go reads
// the same keys off whatever Secret the Technitium client is configured with.
const (
	adminSecretUsernameKey = "username"
	adminSecretPasswordKey = "password"
	// adminSecretTokenKey is where ensureToken writes the minted API token
	// once the instance is bootstrapped. Its presence is also the idempotency
	// check: a non-empty token here means bootstrap already ran.
	adminSecretTokenKey = "token"
)

// operatorTokenName is the tokenName the operator registers with Technitium
// when minting its own API token, so it is identifiable (and revocable) in
// the server's token list as distinct from any human-created token.
const operatorTokenName = "technitium-operator"

// bootstrapAPI is the slice of the Technitium client the bootstrap flow
// needs. It exists so tests can substitute a fake client without spinning up
// a real Technitium server.
type bootstrapAPI interface {
	CreateToken(ctx context.Context, tokenName string) (string, error)
}

// nodeAPI is the slice of the Technitium client the per-node status read
// needs. It exists so tests can substitute a fake client without spinning up
// a real Technitium server, and so the controller does not depend on
// technitium.Client directly for a call this narrow.
type nodeAPI interface {
	GetClusterState(ctx context.Context) (*technitium.ClusterState, error)
}

// loginNodeClient wraps a *technitium.Client built with WithCredentials so
// GetClusterState authenticates itself. nodeClientFactory's signature has no
// context to Login with up front (it mirrors bootstrapClientFactory, called
// from a context-free field), so the token is instead minted lazily on first
// use here.
type loginNodeClient struct {
	*technitium.Client
}

// GetClusterState logs in on first use, since the wrapped client is built
// from credentials alone and never logs in on construction, then delegates.
// A login failure is returned as-is: the caller (the controller) treats any
// error from this method as "node not readable yet", the same handling a
// GetClusterState transport error already gets.
func (n *loginNodeClient) GetClusterState(ctx context.Context) (*technitium.ClusterState, error) {
	if n.Client.Token() == "" {
		if _, err := n.Client.Login(ctx); err != nil {
			return nil, fmt.Errorf("logging in to node: %w", err)
		}
	}
	return n.Client.GetClusterState(ctx)
}

// TechnitiumClusterReconciler provisions and drift-corrects the workload
// backing a TechnitiumCluster: a StatefulSet, a client Service, a headless
// Service, and (unless spec.adminSecretRef is set) a generated admin Secret.
//
// TechnitiumCluster is cluster-scoped but its owned objects are namespaced, so
// they are created in the operator's own namespace rather than alongside the
// CR. A cluster-scoped owner can still own namespaced dependents in any
// namespace, so controllerutil.SetControllerReference on each object is
// enough for owner-reference garbage collection to tear them down when the CR
// is deleted; no finalizer is needed.
type TechnitiumClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// OperatorNamespace is where the owned StatefulSet, Services, and Secret
	// are created. It is the operator's own namespace (POD_NAMESPACE), not the
	// CR's, since the CR itself has no namespace to borrow.
	OperatorNamespace string
	// NewBootstrapClient builds a client to a provisioned instance's API
	// endpoint using its admin credentials. It is a seam so tests can inject
	// a fake token endpoint; production wiring leaves it nil and
	// bootstrapClientFactory supplies a real *technitium.Client.
	NewBootstrapClient func(endpoint, username, password string) (bootstrapAPI, error)
	// NewNodeClient builds a client to a single StatefulSet ordinal's own API
	// endpoint (see nodeEndpoint), using the same admin credentials every pod
	// shares. It is a seam so tests can inject a fake per-node endpoint;
	// production wiring leaves it nil and nodeClientFactory supplies a real,
	// self-authenticating client.
	NewNodeClient func(endpoint, username, password string) (nodeAPI, error)
}

// bootstrapClientFactory returns r.NewBootstrapClient, defaulting it to a
// real Technitium client the first time it is needed. main.go leaves the
// field nil, so production wiring never has to know about this seam.
func (r *TechnitiumClusterReconciler) bootstrapClientFactory() func(endpoint, username, password string) (bootstrapAPI, error) {
	if r.NewBootstrapClient != nil {
		return r.NewBootstrapClient
	}
	return func(endpoint, username, password string) (bootstrapAPI, error) {
		return technitium.NewClient(endpoint, technitium.WithCredentials(username, password))
	}
}

// nodeClientFactory returns r.NewNodeClient, defaulting it to a real
// self-authenticating Technitium client the first time it is needed. main.go
// leaves the field nil, so production wiring never has to know about this
// seam.
func (r *TechnitiumClusterReconciler) nodeClientFactory() func(endpoint, username, password string) (nodeAPI, error) {
	if r.NewNodeClient != nil {
		return r.NewNodeClient
	}
	return func(endpoint, username, password string) (nodeAPI, error) {
		c, err := technitium.NewClient(endpoint, technitium.WithCredentials(username, password))
		if err != nil {
			return nil, err
		}
		return &loginNodeClient{Client: c}, nil
	}
}

// nodeEndpoint is a single StatefulSet ordinal's own API address: the
// headless Service governing the StatefulSet gives each pod a stable DNS name
// of "<sts>-<ordinal>.<headless-service>", unlike the client Service's
// endpoint (used for bootstrap and Zone reconciliation), which load-balances
// across whichever pod happens to answer.
func nodeEndpoint(clusterName string, ordinal int32, namespace string) string {
	return fmt.Sprintf("http://%s-%d.%s.%s.svc:5380", clusterName, ordinal, headlessServiceName(clusterName), namespace)
}

// +kubebuilder:rbac:groups=dns.packet.fail,resources=technitiumclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=dns.packet.fail,resources=technitiumclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.packet.fail,resources=technitiumclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;persistentvolumeclaims;secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Reconcile provisions the Technitium workload for a TechnitiumCluster,
// corrects drift on it, and once the workload is ready mints the durable
// admin API token that carries the instance into phase Ready.
func (r *TechnitiumClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var tc dnsv1alpha1.TechnitiumCluster
	if err := r.Get(ctx, req.NamespacedName, &tc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	sts, err := r.reconcileWorkload(ctx, &tc)
	if err != nil {
		log.Error(err, "Failed to reconcile TechnitiumCluster workload", "cluster", tc.Name)
		if statusErr := r.markDegraded(ctx, req.NamespacedName, err); statusErr != nil {
			log.Error(statusErr, "Failed to update TechnitiumCluster status", "cluster", tc.Name)
		}
		return ctrl.Result{}, err
	}

	desiredReplicas := int32(1)
	if tc.Spec.Replicas != nil {
		desiredReplicas = *tc.Spec.Replicas
	}
	readyReplicas := sts.Status.ReadyReplicas
	endpoint := fmt.Sprintf("http://%s.%s.svc:5380", tc.Name, r.OperatorNamespace)

	// Bootstrap only once the workload itself is up: minting against an
	// endpoint with no listener behind it yet is a wasted round trip that
	// updateStatus's short requeue will simply retry next poll anyway.
	bootstrapped := false
	if readyReplicas >= desiredReplicas {
		bootstrapped, err = r.ensureToken(ctx, &tc, endpoint)
		if err != nil {
			log.Error(err, "Failed to bootstrap TechnitiumCluster admin token", "cluster", tc.Name)
			if statusErr := r.markDegraded(ctx, req.NamespacedName, err); statusErr != nil {
				log.Error(statusErr, "Failed to update TechnitiumCluster status", "cluster", tc.Name)
			}
			return ctrl.Result{}, err
		}
	}

	requeueAfter, err := r.updateStatus(ctx, req.NamespacedName, sts, endpoint, bootstrapped)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// ensureToken mints a durable API token for a bootstrapped instance and
// stores it in the admin Secret's token key. It is idempotent: a Secret that
// already carries a token is left untouched, since minting a second one
// would not invalidate the first (Technitium tokens are independent, and
// non-expiring) but would orphan whatever the Zone controller already holds
// under the original.
//
// A failure from CreateToken itself (the server not accepting connections
// yet, or the generated password not yet applied so the credentials are
// rejected) is expected during the normal bootstrap window. It is logged
// here and reported as "not yet bootstrapped" (false, nil) rather than
// returned as an error, so Reconcile requeues the resource in phase
// Bootstrapping instead of flapping it to Degraded on every poll until the
// server catches up. Only a problem with the Secret itself, missing
// entirely or missing the username/password keys the controller itself
// wrote, is a hard error: that is a configuration fault requeuing will not
// fix on its own.
func (r *TechnitiumClusterReconciler) ensureToken(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster, endpoint string) (bool, error) {
	log := logf.FromContext(ctx)

	secret, secretKey, err := r.resolveAdminSecret(ctx, tc)
	if err != nil {
		return false, err
	}

	if len(secret.Data[adminSecretTokenKey]) > 0 {
		return true, nil
	}

	username := secret.Data[adminSecretUsernameKey]
	if len(username) == 0 {
		return false, fmt.Errorf("admin secret %s has no %q key", secretKey, adminSecretUsernameKey)
	}
	password := secret.Data[adminSecretPasswordKey]
	if len(password) == 0 {
		return false, fmt.Errorf("admin secret %s has no %q key", secretKey, adminSecretPasswordKey)
	}

	apiClient, err := r.bootstrapClientFactory()(endpoint, string(username), string(password))
	if err != nil {
		return false, fmt.Errorf("building bootstrap client for %s: %w", endpoint, err)
	}

	token, err := apiClient.CreateToken(ctx, operatorTokenName)
	if err != nil {
		log.Info("Deferring admin token mint until the instance accepts the generated credentials",
			"cluster", tc.Name, "endpoint", endpoint, "error", err.Error())
		return false, nil
	}

	secret.Data[adminSecretTokenKey] = []byte(token)
	if err := r.Update(ctx, secret); err != nil {
		return false, fmt.Errorf("writing admin token to secret %s: %w", secretKey, err)
	}

	return true, nil
}

// resolveAdminSecret fetches the admin Secret for tc: the generated
// "<name>-admin" Secret, or spec.adminSecretRef when set. It is shared by
// ensureToken (which also writes the minted token back into it) and
// adminCredentials (which only reads username/password for per-node client
// construction).
func (r *TechnitiumClusterReconciler) resolveAdminSecret(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster) (*corev1.Secret, client.ObjectKey, error) {
	secretNamespace := r.OperatorNamespace
	secretName := adminSecretName(tc.Name)
	if tc.Spec.AdminSecretRef != nil {
		secretName = tc.Spec.AdminSecretRef.Name
		if tc.Spec.AdminSecretRef.Namespace != "" {
			secretNamespace = tc.Spec.AdminSecretRef.Namespace
		}
	}
	secretKey := client.ObjectKey{Namespace: secretNamespace, Name: secretName}

	var secret corev1.Secret
	if err := r.Get(ctx, secretKey, &secret); err != nil {
		return nil, secretKey, fmt.Errorf("getting admin secret %s: %w", secretKey, err)
	}
	return &secret, secretKey, nil
}

// adminCredentials reads the username and password out of tc's admin Secret.
// It is what every per-node client is built with: each pod shares the same
// local admin login, there being no separate per-node identity until a real
// cluster (with its own node accounts) exists.
func (r *TechnitiumClusterReconciler) adminCredentials(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster) (username, password string, err error) {
	secret, secretKey, err := r.resolveAdminSecret(ctx, tc)
	if err != nil {
		return "", "", err
	}

	u := secret.Data[adminSecretUsernameKey]
	if len(u) == 0 {
		return "", "", fmt.Errorf("admin secret %s has no %q key", secretKey, adminSecretUsernameKey)
	}
	p := secret.Data[adminSecretPasswordKey]
	if len(p) == 0 {
		return "", "", fmt.Errorf("admin secret %s has no %q key", secretKey, adminSecretPasswordKey)
	}
	return string(u), string(p), nil
}

// reconcileWorkload brings the headless Service, client Service, admin
// Secret, and StatefulSet in line with the spec, creating anything absent. It
// returns the reconciled StatefulSet so the caller can read its observed
// readyReplicas without a second fetch.
func (r *TechnitiumClusterReconciler) reconcileWorkload(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster) (*appsv1.StatefulSet, error) {
	if err := r.reconcileHeadlessService(ctx, tc); err != nil {
		return nil, fmt.Errorf("reconciling headless service: %w", err)
	}
	if err := r.reconcileClientService(ctx, tc); err != nil {
		return nil, fmt.Errorf("reconciling client service: %w", err)
	}
	// A caller-supplied adminSecretRef means the credentials are managed
	// outside this controller; generating one anyway would fight whatever
	// owns that Secret.
	if tc.Spec.AdminSecretRef == nil {
		if err := r.reconcileAdminSecret(ctx, tc); err != nil {
			return nil, fmt.Errorf("reconciling admin secret: %w", err)
		}
	}
	sts, err := r.reconcileStatefulSet(ctx, tc)
	if err != nil {
		return nil, fmt.Errorf("reconciling statefulset: %w", err)
	}
	return sts, nil
}

// instanceLabels is the selector subset used on the StatefulSet, both
// Services, and the pod template: it is what ties a Service's endpoints and a
// StatefulSet's owned Pods to this particular TechnitiumCluster.
func instanceLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "technitium",
		"app.kubernetes.io/instance": name,
	}
}

// commonLabels extends instanceLabels with the managed-by label applied to
// every owned object, selector and non-selector fields alike.
func commonLabels(name string) map[string]string {
	labels := instanceLabels(name)
	labels["app.kubernetes.io/managed-by"] = "technitium-operator"
	return labels
}

func statefulSetName(clusterName string) string   { return clusterName }
func clientServiceName(clusterName string) string { return clusterName }
func headlessServiceName(clusterName string) string {
	return clusterName + "-headless"
}
func adminSecretName(clusterName string) string { return clusterName + "-admin" }

// resolvedAdminSecretName is the Secret the StatefulSet mounts for the admin
// password: the generated "<name>-admin" Secret, or spec.adminSecretRef when
// set. A Secret volume can only reference one in the pod's own namespace, so
// adminSecretRef.Namespace is resolved only for documentation/validation
// purposes elsewhere; the mount always resolves within OperatorNamespace.
func resolvedAdminSecretName(tc *dnsv1alpha1.TechnitiumCluster) string {
	if tc.Spec.AdminSecretRef != nil {
		return tc.Spec.AdminSecretRef.Name
	}
	return adminSecretName(tc.Name)
}

// reconcileHeadlessService ensures the StatefulSet's governing Service
// exists. ClusterIP is only set on creation because it is immutable once
// assigned; leaving it alone on update avoids fighting the apiserver over a
// field we do not actually need to correct.
func (r *TechnitiumClusterReconciler) reconcileHeadlessService(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster) error {
	svc := &corev1.Service{
		Name:      headlessServiceName(tc.Name),
		Namespace: r.OperatorNamespace}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(tc, svc, r.Scheme); err != nil {
			return err
		}
		svc.Labels = commonLabels(tc.Name)
		if svc.CreationTimestamp.IsZero() {
			svc.Spec.ClusterIP = corev1.ClusterIPNone
		}
		svc.Spec.Selector = instanceLabels(tc.Name)
		svc.Spec.Ports = dnsServicePorts()
		return nil
	})
	return err
}

// reconcileClientService ensures the Service clients and Zone resources
// reach the instance through. Type and annotations come from spec so an
// operator can front the instance with a LoadBalancer or wire up
// external-dns without editing the Service by hand.
func (r *TechnitiumClusterReconciler) reconcileClientService(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster) error {
	svc := &corev1.Service{
		Name:      clientServiceName(tc.Name),
		Namespace: r.OperatorNamespace}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(tc, svc, r.Scheme); err != nil {
			return err
		}
		svc.Labels = commonLabels(tc.Name)
		svc.Annotations = tc.Spec.Service.Annotations
		if tc.Spec.Service.Type != "" {
			svc.Spec.Type = tc.Spec.Service.Type
		}
		svc.Spec.Selector = instanceLabels(tc.Name)
		svc.Spec.Ports = dnsServicePorts()
		return nil
	})
	return err
}

// dnsServicePorts is the port set shared by both Services: DNS over UDP and
// TCP, plus the Technitium web/API port.
func dnsServicePorts() []corev1.ServicePort {
	return []corev1.ServicePort{
		{Name: "dns-udp", Port: 53, Protocol: corev1.ProtocolUDP, TargetPort: intstr.FromInt32(53)},
		{Name: "dns-tcp", Port: 53, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(53)},
		{Name: "api", Port: 5380, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(5380)},
	}
}

// reconcileAdminSecret ensures the generated admin credentials Secret exists.
// It never regenerates an existing password and never touches a "token" key,
// since ticket #43's bootstrap flow writes that key once it mints one; wiping
// either on a routine reconcile would invalidate a session the server already
// issued.
func (r *TechnitiumClusterReconciler) reconcileAdminSecret(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster) error {
	secret := &corev1.Secret{
		Name:      adminSecretName(tc.Name),
		Namespace: r.OperatorNamespace}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		if err := controllerutil.SetControllerReference(tc, secret, r.Scheme); err != nil {
			return err
		}
		secret.Labels = commonLabels(tc.Name)
		secret.Type = corev1.SecretTypeOpaque
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		if _, ok := secret.Data[adminSecretUsernameKey]; !ok {
			secret.Data[adminSecretUsernameKey] = []byte("admin")
		}
		if _, ok := secret.Data[adminSecretPasswordKey]; !ok {
			password, err := generatePassword()
			if err != nil {
				return fmt.Errorf("generating admin password: %w", err)
			}
			secret.Data[adminSecretPasswordKey] = []byte(password)
		}
		return nil
	})
	return err
}

// generatePassword produces a 32-byte random value hex-encoded for safe use
// in an environment file mounted from a Secret.
func generatePassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// reconcileStatefulSet ensures the StatefulSet running Technitium exists and
// matches spec. Only replicas and the pod template are re-asserted on update:
// selector, serviceName, and volumeClaimTemplates are immutable once the
// StatefulSet is created, so touching them again would just turn a routine
// drift-correction into a failed update.
func (r *TechnitiumClusterReconciler) reconcileStatefulSet(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster) (*appsv1.StatefulSet, error) {
	sts := &appsv1.StatefulSet{
		Name:      statefulSetName(tc.Name),
		Namespace: r.OperatorNamespace}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		if err := controllerutil.SetControllerReference(tc, sts, r.Scheme); err != nil {
			return err
		}
		sts.Labels = commonLabels(tc.Name)

		replicas := int32(1)
		if tc.Spec.Replicas != nil {
			replicas = *tc.Spec.Replicas
		}
		sts.Spec.Replicas = &replicas
		sts.Spec.Template = podTemplateFor(tc)

		if sts.CreationTimestamp.IsZero() {
			sts.Spec.ServiceName = headlessServiceName(tc.Name)
			sts.Spec.Selector = &metav1.LabelSelector{MatchLabels: instanceLabels(tc.Name)}
			sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{volumeClaimTemplateFor(tc)}
		}
		return nil
	})
	return sts, err
}

// podTemplateFor builds the Technitium pod template from spec. It is rebuilt
// in full on every reconcile so hand-edited images, resources, or env vars
// are corrected back to spec.
func podTemplateFor(tc *dnsv1alpha1.TechnitiumCluster) corev1.PodTemplateSpec {
	env := []corev1.EnvVar{
		// The web console's own password prompt is for interactive use;
		// pointing the server at a file lets it pick up the admin password
		// from the mounted Secret without it ever appearing in the Pod spec
		// or logs.
		{Name: "DNS_SERVER_ADMIN_PASSWORD_FILE", Value: "/etc/technitium/admin/password"},
	}
	if tc.Spec.DNSServerDomain != "" {
		env = append(env, corev1.EnvVar{Name: "DNS_SERVER_DOMAIN", Value: tc.Spec.DNSServerDomain})
	}

	probe := &corev1.Probe{
		// An HTTP path probe is unreliable here: Technitium's root path
		// redirects, and kubelet does not follow redirects when scoring probe
		// success. A bare TCP check on the web API port is a reliable proxy
		// for "the server process is up and listening".
		TCPSocket:           &corev1.TCPSocketAction{Port: intstr.FromInt32(5380)},
		InitialDelaySeconds: 10,
		PeriodSeconds:       10,
	}

	// Run under the restricted Pod Security Standard so the workload schedules in
	// namespaces that enforce it. Technitium binds port 53, so drop all
	// capabilities and add back only NET_BIND_SERVICE. runAsUser/fsGroup 1000
	// let a non-root process own the persistent data volume.
	const nonRootUID = int64(1000)
	podSecurityContext := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		RunAsUser:      ptr.To(nonRootUID),
		RunAsGroup:     ptr.To(nonRootUID),
		FSGroup:        ptr.To(nonRootUID),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	containerSecurityContext := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		RunAsNonRoot:             ptr.To(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
			Add:  []corev1.Capability{"NET_BIND_SERVICE"},
		},
	}

	return corev1.PodTemplateSpec{
		Labels: commonLabels(tc.Name),
		Spec: corev1.PodSpec{
			SecurityContext: podSecurityContext,
			Containers: []corev1.Container{
				{
					Name:  "dns-server",
					Image: tc.Spec.Image,
					Ports: []corev1.ContainerPort{
						{Name: "dns-udp", ContainerPort: 53, Protocol: corev1.ProtocolUDP},
						{Name: "dns-tcp", ContainerPort: 53, Protocol: corev1.ProtocolTCP},
						{Name: "api", ContainerPort: 5380, Protocol: corev1.ProtocolTCP},
					},
					Env:       env,
					Resources: tc.Spec.Resources,
					VolumeMounts: []corev1.VolumeMount{
						{Name: "data", MountPath: "/etc/dns"},
						{Name: "admin", MountPath: "/etc/technitium/admin", ReadOnly: true},
						// Technitium writes logs to /var/log/technitium and uses
						// /tmp, both on the read-only-friendly root filesystem a
						// non-root process cannot create under. Back them with
						// ephemeral volumes the fsGroup can write.
						{Name: "varlog", MountPath: "/var/log/technitium"},
						{Name: "tmp", MountPath: "/tmp"},
					},
					ReadinessProbe:  probe,
					LivenessProbe:   probe,
					SecurityContext: containerSecurityContext,
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "admin",
					Secret: &corev1.SecretVolumeSource{
						SecretName: resolvedAdminSecretName(tc),
						Items: []corev1.KeyToPath{
							{Key: adminSecretPasswordKey, Path: "password"},
						},
					},
				},
				{Name: "varlog", EmptyDir: &corev1.EmptyDirVolumeSource{}},
				{Name: "tmp", EmptyDir: &corev1.EmptyDirVolumeSource{}},
			},
		},
	}
}

// volumeClaimTemplateFor builds the "data" volume claim template, sized and
// classed from spec.storage. It is only applied at StatefulSet creation time:
// volumeClaimTemplates cannot be changed on an existing StatefulSet.
func volumeClaimTemplateFor(tc *dnsv1alpha1.TechnitiumCluster) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{
		Name: "data", Labels: commonLabels(tc.Name),
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: tc.Spec.Storage.Size},
			},
			StorageClassName: tc.Spec.Storage.StorageClassName,
		},
	}
}

// updateStatus re-fetches the TechnitiumCluster and records the observed
// workload state: endpoint, ready replicas, phase, and conditions. It reports
// the requeue interval to use next: short while the workload is still coming
// up or still awaiting bootstrap, long once the instance is fully Ready,
// since readiness and bootstrap progress are only worth polling for quickly
// in the former cases.
func (r *TechnitiumClusterReconciler) updateStatus(ctx context.Context, key client.ObjectKey, sts *appsv1.StatefulSet, endpoint string, bootstrapped bool) (time.Duration, error) {
	var tc dnsv1alpha1.TechnitiumCluster
	if err := r.Get(ctx, key, &tc); err != nil {
		return 0, client.IgnoreNotFound(err)
	}

	desiredReplicas := int32(1)
	if tc.Spec.Replicas != nil {
		desiredReplicas = *tc.Spec.Replicas
	}
	readyReplicas := sts.Status.ReadyReplicas

	changed := false
	if tc.Status.Endpoint != endpoint {
		tc.Status.Endpoint = endpoint
		changed = true
	}
	if tc.Status.ReadyReplicas != readyReplicas {
		tc.Status.ReadyReplicas = readyReplicas
		changed = true
	}
	if tc.Status.ObservedGeneration != tc.Generation {
		tc.Status.ObservedGeneration = tc.Generation
		changed = true
	}

	// Node state is only worth reading once the workload itself is up, the
	// same gate ensureToken applies to bootstrap: querying a pod with no
	// listener behind it yet is a wasted round trip this function's own
	// short requeue will retry next poll anyway.
	if readyReplicas >= desiredReplicas {
		nodes, members := r.buildNodeStatuses(ctx, &tc, desiredReplicas)
		if !reflect.DeepEqual(tc.Status.Nodes, nodes) {
			tc.Status.Nodes = nodes
			changed = true
		}
		if tc.Status.Members != members {
			tc.Status.Members = members
			changed = true
		}
	}

	var (
		phase             dnsv1alpha1.TechnitiumClusterPhase
		reason, message   string
		availableStatus   metav1.ConditionStatus
		progressingStatus metav1.ConditionStatus
		requeueAfter      time.Duration
	)
	switch {
	case readyReplicas < desiredReplicas:
		phase = dnsv1alpha1.TechnitiumClusterPhaseProvisioning
		reason = "WorkloadProvisioning"
		message = "Waiting for the StatefulSet to report ready replicas"
		availableStatus = metav1.ConditionFalse
		progressingStatus = metav1.ConditionTrue
		requeueAfter = clusterPollInterval
	case !bootstrapped:
		phase = dnsv1alpha1.TechnitiumClusterPhaseBootstrapping
		reason = "AwaitingBootstrap"
		message = "Workload is ready; minting the admin API token"
		availableStatus = metav1.ConditionFalse
		progressingStatus = metav1.ConditionTrue
		// Short requeue: bootstrap converges as soon as the server picks up
		// the generated admin password, which is typically seconds away
		// once the pod itself is ready, not the drift-correction cadence.
		requeueAfter = clusterPollInterval
	default:
		phase = dnsv1alpha1.TechnitiumClusterPhaseReady
		reason = "InstanceReady"
		message = "Workload is ready and the admin API token is bootstrapped"
		availableStatus = metav1.ConditionTrue
		progressingStatus = metav1.ConditionFalse
		requeueAfter = clusterDriftInterval
	}

	if tc.Status.Phase != phase {
		tc.Status.Phase = phase
		changed = true
	}

	changed = setClusterCondition(&tc, dnsv1alpha1.TechnitiumClusterConditionProgressing, progressingStatus, reason, message) || changed
	changed = setClusterCondition(&tc, dnsv1alpha1.TechnitiumClusterConditionAvailable, availableStatus, reason, message) || changed
	changed = setClusterCondition(&tc, dnsv1alpha1.TechnitiumClusterConditionDegraded, metav1.ConditionFalse, reason, message) || changed

	if !changed {
		return requeueAfter, nil
	}
	return requeueAfter, r.Status().Update(ctx, &tc)
}

// nodeReadyState and nodeNotReadyState are the placeholder states a node gets
// before clustering exists (this phase implements no init/join yet) or when
// its cluster state could not be read. They are distinct from the real
// cluster membership states (Self, Connected, ...) Technitium itself reports
// once a node has actually joined a cluster.
const (
	nodeReadyState    = "Ready"
	nodeNotReadyState = "NotReady"
)

// roleForOrdinal reports the role a StatefulSet ordinal is assumed to hold
// before a real cluster init/join exists: ordinal 0 is always the node
// cluster/init would run against, so it is Primary independent of anything
// the cluster API reports.
func roleForOrdinal(ordinal int32) string {
	if ordinal == 0 {
		return "Primary"
	}
	return "Secondary"
}

// findClusterMember locates the entry in a clusterNodes response belonging to
// podName. A cluster node's Name is "<hostname>.<clusterDomain>" while podName
// is the bare StatefulSet pod name, so the match is a prefix rather than an
// exact comparison.
func findClusterMember(nodes []technitium.ClusterNode, podName string) *technitium.ClusterNode {
	for i := range nodes {
		if strings.HasPrefix(nodes[i].Name, podName) {
			return &nodes[i]
		}
	}
	return nil
}

// nodeJoined reports whether a node's status.state counts toward
// status.members' joined count: the readiness placeholder Ready (there being
// no real cluster yet in this phase) and the real cluster states Self and
// Connected all mean the node is up and reachable.
func nodeJoined(state string) bool {
	switch state {
	case nodeReadyState, "Self", "Connected":
		return true
	default:
		return false
	}
}

// buildNodeStatuses reads each StatefulSet ordinal's own cluster state and
// reports a TechnitiumClusterNodeStatus per ordinal plus the rendered
// "joined/total" members string. It never returns an error: a node whose
// state could not be read is expected during the startup window (there is no
// init/join in this phase yet, so every node starts out uninitialized) and is
// reflected in that node's own State/Message rather than failing the whole
// status update.
func (r *TechnitiumClusterReconciler) buildNodeStatuses(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster, desiredReplicas int32) ([]dnsv1alpha1.TechnitiumClusterNodeStatus, string) {
	log := logf.FromContext(ctx)

	username, password, err := r.adminCredentials(ctx, tc)
	if err != nil {
		// The admin Secret itself is a controller-managed prerequisite
		// (reconcileAdminSecret, or a caller's adminSecretRef): a problem
		// with it already surfaces as Degraded once ensureToken runs against
		// the same Secret, so this just marks every node unreadable rather
		// than duplicating that failure path.
		log.Info("Skipping node status read: admin credentials unavailable", "cluster", tc.Name, "error", err.Error())
		nodes := make([]dnsv1alpha1.TechnitiumClusterNodeStatus, desiredReplicas)
		for i := range nodes {
			nodes[i] = dnsv1alpha1.TechnitiumClusterNodeStatus{
				Name:    fmt.Sprintf("%s-%d", tc.Name, i),
				Role:    roleForOrdinal(int32(i)),
				State:   "Unknown",
				Message: "admin credentials unavailable",
			}
		}
		return nodes, fmt.Sprintf("0/%d", desiredReplicas)
	}

	nodes := make([]dnsv1alpha1.TechnitiumClusterNodeStatus, desiredReplicas)
	joined := 0
	for i := int32(0); i < desiredReplicas; i++ {
		podName := fmt.Sprintf("%s-%d", tc.Name, i)
		status := dnsv1alpha1.TechnitiumClusterNodeStatus{
			Name:  podName,
			Role:  roleForOrdinal(i),
			State: nodeReadyState,
		}

		nodeClient, err := r.nodeClientFactory()(nodeEndpoint(tc.Name, i, r.OperatorNamespace), username, password)
		if err != nil {
			status.State = nodeNotReadyState
			status.Message = err.Error()
			nodes[i] = status
			continue
		}

		state, err := nodeClient.GetClusterState(ctx)
		if err != nil {
			// Expected pre-cluster and during pod startup: the caller only
			// reaches here once the workload as a whole reports ready, but a
			// single ordinal's own API can still lag the aggregate
			// readyReplicas count by a poll or two.
			log.Info("Node cluster state not yet readable", "cluster", tc.Name, "node", podName, "error", err.Error())
			status.State = nodeNotReadyState
			status.Message = "cluster state not yet reachable"
			nodes[i] = status
			continue
		}

		if state.ClusterInitialized {
			if member := findClusterMember(state.Nodes, podName); member != nil {
				status.Role = member.Type
				status.State = member.State
				if member.ConfigLastSynced != nil {
					status.LastSynced = &metav1.Time{Time: *member.ConfigLastSynced}
				}
			}
		}
		// An uninitialized node (or one whose own report has not yet listed
		// itself) keeps the readiness-derived Role/State set above: there is
		// no real cluster membership to override it with yet.

		if nodeJoined(status.State) {
			joined++
		}
		nodes[i] = status
	}

	return nodes, fmt.Sprintf("%d/%d", joined, desiredReplicas)
}

// markDegraded re-fetches the TechnitiumCluster and records a failed
// reconcile so the failure is visible on the resource, not only in the logs.
func (r *TechnitiumClusterReconciler) markDegraded(ctx context.Context, key client.ObjectKey, cause error) error {
	var tc dnsv1alpha1.TechnitiumCluster
	if err := r.Get(ctx, key, &tc); err != nil {
		return client.IgnoreNotFound(err)
	}

	setClusterCondition(&tc, dnsv1alpha1.TechnitiumClusterConditionDegraded, metav1.ConditionTrue, "ReconcileFailed", cause.Error())
	setClusterCondition(&tc, dnsv1alpha1.TechnitiumClusterConditionProgressing, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setClusterCondition(&tc, dnsv1alpha1.TechnitiumClusterConditionAvailable, metav1.ConditionFalse, "ReconcileFailed", cause.Error())

	return r.Status().Update(ctx, &tc)
}

// setClusterCondition upserts a status condition stamped with the cluster's
// current generation and reports whether it changed anything.
func setClusterCondition(tc *dnsv1alpha1.TechnitiumCluster, condType string, status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&tc.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: tc.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *TechnitiumClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dnsv1alpha1.TechnitiumCluster{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Named("technitiumcluster").
		Complete(r)
}

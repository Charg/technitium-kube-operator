/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/config"
	"github.com/charg/technitium-operator/internal/technitium"
)

// serverSettingsFinalizer guards the teardown hook: while it is present,
// Kubernetes will not remove the ServerSettings object, giving the controller
// a chance to run finalizeServerSettings first. Deletion never touches the
// server (see finalizeServerSettings), but the finalizer still exists for
// parity with the other CRDs and as the place a future non-Retain policy
// would hook in.
const serverSettingsFinalizer = "dns.packet.fail/serversettings-cleanup"

// ServerSettingsAPI is the subset of the Technitium client the reconciler
// depends on. Depending on the interface rather than the concrete client
// keeps the reconciliation logic testable with a fake server.
type ServerSettingsAPI interface {
	GetDNSSettings(ctx context.Context) (*technitium.DNSSettings, error)
	SetDNSSettings(ctx context.Context, opts technitium.SetDNSSettingsOptions) error
}

// cachedServerSettingsClient is one entry in ServerSettingsReconciler's client
// cache: a built ServerSettingsAPI plus the resourceVersion of the admin
// Secret it was built from. Kept separate from Record's and Zone's caches
// (same shape, different element type) rather than sharing a generic cache,
// so this CRD's controller stays independent of theirs.
type cachedServerSettingsClient struct {
	secretResourceVersion string
	api                   ServerSettingsAPI
}

// ServerSettingsReconciler reconciles a ServerSettings object.
type ServerSettingsReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// OperatorNamespace is where a TechnitiumCluster's admin Secret lives when
	// its spec does not override the namespace. It is the operator's own
	// namespace (POD_NAMESPACE), matching the TechnitiumCluster controller.
	OperatorNamespace string
	// NewServerClient resolves a ServerSettings' serverRef to a client for
	// that managed instance. It is a seam so tests inject a fake; production
	// leaves it nil and defaultServerClient resolves the TechnitiumCluster
	// endpoint + admin Secret and builds a real client.
	NewServerClient func(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (ServerSettingsAPI, error)

	// clientCacheMu guards clientCache. Reconcile runs are not necessarily
	// serialized (controller-runtime workers), so the cache needs its own
	// lock rather than relying on the caller.
	clientCacheMu sync.Mutex
	// clientCache holds one built client per TechnitiumCluster name, so a
	// routine drift reconcile reuses the existing client instead of
	// re-reading the Secret and reconstructing it every time.
	clientCache map[string]cachedServerSettingsClient
}

// +kubebuilder:rbac:groups=dns.packet.fail,resources=serversettings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dns.packet.fail,resources=serversettings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.packet.fail,resources=serversettings/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.packet.fail,resources=technitiumclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile ensures the referenced Technitium server's DNS settings match the
// managed groups of the ServerSettings spec.
func (r *ServerSettingsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var settings dnsv1alpha1.ServerSettings
	if err := r.Get(ctx, req.NamespacedName, &settings); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A set deletion timestamp means the resource is being torn down: run the
	// teardown hook and drop the finalizer instead of reconciling desired
	// state.
	if !settings.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalizeServerSettings(ctx, &settings)
	}

	// Register the finalizer before touching the server so a delete that
	// arrives mid-flight still triggers the teardown hook. reconcileServerSettings
	// only reads the spec, so it is safe to continue with the same object
	// after the update.
	if controllerutil.AddFinalizer(&settings, serverSettingsFinalizer) {
		if err := r.Update(ctx, &settings); err != nil {
			return ctrl.Result{}, err
		}
	}

	observed, err := r.reconcileServerSettings(ctx, &settings)
	if err != nil {
		log.Error(err, "Failed to reconcile ServerSettings", "server", settings.Spec.ServerRef.Name)
		if statusErr := r.markDegraded(ctx, req.NamespacedName, err); statusErr != nil {
			log.Error(statusErr, "Failed to update ServerSettings status", "server", settings.Spec.ServerRef.Name)
		}
		return ctrl.Result{}, err
	}

	if err := r.markReady(ctx, req.NamespacedName, observed); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: driftReconcileInterval}, nil
}

// serverClientFor resolves a ServerSettings' serverRef to a ServerSettingsAPI,
// preferring the injected NewServerClient seam over the production resolver.
func (r *ServerSettingsReconciler) serverClientFor(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (ServerSettingsAPI, error) {
	if r.NewServerClient != nil {
		return r.NewServerClient(ctx, serverRef)
	}
	return r.defaultServerClient(ctx, serverRef)
}

// defaultServerClient resolves a TechnitiumCluster's endpoint and admin
// Secret and builds a real Technitium client for it, caching the result by
// the Secret's resourceVersion so a routine reconcile does not re-login on
// every pass. This mirrors RecordReconciler.defaultServerClient; see
// cachedServerSettingsClient for why the cache itself is not shared.
func (r *ServerSettingsReconciler) defaultServerClient(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (ServerSettingsAPI, error) {
	var tc dnsv1alpha1.TechnitiumCluster
	if err := r.Get(ctx, client.ObjectKey{Name: serverRef.Name}, &tc); err != nil {
		// Wrapped rather than replaced so apierrors.IsNotFound still recognizes
		// it, matching how Record and Zone tell "server is gone" apart from
		// any other resolve failure.
		return nil, fmt.Errorf("getting TechnitiumCluster %q: %w", serverRef.Name, err)
	}

	if tc.Status.Endpoint == "" {
		return nil, fmt.Errorf("TechnitiumCluster %q is not ready yet: no endpoint reported", serverRef.Name)
	}

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
		return nil, fmt.Errorf("getting admin secret %s for TechnitiumCluster %q: %w", secretKey, serverRef.Name, err)
	}

	if len(secret.Data[adminSecretTokenKey]) == 0 {
		return nil, fmt.Errorf("TechnitiumCluster %q is not bootstrapped yet: admin secret %s has no token",
			serverRef.Name, secretKey)
	}

	if cached, ok := r.cachedClient(tc.Name, secret.ResourceVersion); ok {
		return cached, nil
	}

	opts, err := config.ClientOptionsFromSecret(&secret)
	if err != nil {
		return nil, err
	}

	api, err := technitium.NewClient(tc.Status.Endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("building Technitium client for %q: %w", serverRef.Name, err)
	}

	r.cacheClient(tc.Name, secret.ResourceVersion, api)
	return api, nil
}

// cachedClient returns the cached client for clusterName when its Secret
// resourceVersion still matches, so the caller knows the credentials have not
// rotated since the client was built.
func (r *ServerSettingsReconciler) cachedClient(clusterName, secretResourceVersion string) (ServerSettingsAPI, bool) {
	r.clientCacheMu.Lock()
	defer r.clientCacheMu.Unlock()

	entry, ok := r.clientCache[clusterName]
	if !ok || entry.secretResourceVersion != secretResourceVersion {
		return nil, false
	}
	return entry.api, true
}

// cacheClient stores a freshly built client under clusterName, keyed for
// invalidation by the admin Secret's resourceVersion at build time.
func (r *ServerSettingsReconciler) cacheClient(clusterName, secretResourceVersion string, api ServerSettingsAPI) {
	r.clientCacheMu.Lock()
	defer r.clientCacheMu.Unlock()

	if r.clientCache == nil {
		r.clientCache = make(map[string]cachedServerSettingsClient)
	}
	r.clientCache[clusterName] = cachedServerSettingsClient{secretResourceVersion: secretResourceVersion, api: api}
}

// finalizeServerSettings clears the finalizer without touching the server.
// DNS settings are instance-wide with no owner to garbage collect: deleting a
// ServerSettings resource leaves the values it applied in place (the only
// DeletionPolicy offered is Retain), so there is nothing to reconcile against
// the server here. The finalizer exists as the teardown hook and for parity
// with the other CRDs, not because this resource currently has cleanup work.
func (r *ServerSettingsReconciler) finalizeServerSettings(ctx context.Context, settings *dnsv1alpha1.ServerSettings) error {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(settings, serverSettingsFinalizer) {
		return nil
	}

	log.Info("Retaining ServerSettings on the Technitium server", "server", settings.Spec.ServerRef.Name)

	controllerutil.RemoveFinalizer(settings, serverSettingsFinalizer)
	return r.Update(ctx, settings)
}

// reconcileServerSettings brings the server's DNS settings in line with the
// managed groups of the spec, and returns the settings the server reports
// after reconciling so the caller can populate status without a second fetch.
// It is idempotent: a server that already matches the desired managed fields
// gets no write at all, avoiding needless churn on every drift tick.
func (r *ServerSettingsReconciler) reconcileServerSettings(ctx context.Context, settings *dnsv1alpha1.ServerSettings) (*technitium.DNSSettings, error) {
	log := logf.FromContext(ctx)

	api, err := r.serverClientFor(ctx, settings.Spec.ServerRef)
	if err != nil {
		return nil, err
	}

	current, err := api.GetDNSSettings(ctx)
	if err != nil {
		return nil, err
	}

	desired := setDNSSettingsOptionsFromSpec(settings.Spec)
	if serverSettingsInSync(desired, current) {
		return current, nil
	}

	if err := api.SetDNSSettings(ctx, desired); err != nil {
		return nil, err
	}
	log.Info("Applied ServerSettings", "server", settings.Spec.ServerRef.Name)

	current, err = api.GetDNSSettings(ctx)
	if err != nil {
		return nil, err
	}
	return current, nil
}

// setDNSSettingsOptionsFromSpec maps the non-nil settings groups of spec onto
// the /api/settings/set parameters. Each group is independent: a nil group
// leaves every field within it unset (untouched on the server), which is how
// this resource avoids fighting the Blocklist CRD or any other writer over
// the fields it does not own.
func setDNSSettingsOptionsFromSpec(spec dnsv1alpha1.ServerSettingsSpec) technitium.SetDNSSettingsOptions {
	var opts technitium.SetDNSSettingsOptions

	if fwd := spec.Forwarders; fwd != nil {
		addresses := fwd.Addresses
		opts.Forwarders = &addresses
		if fwd.Protocol != nil {
			protocol := string(*fwd.Protocol)
			opts.ForwarderProtocol = &protocol
		}
	}

	if rec := spec.Recursion; rec != nil {
		policy := string(rec.Policy)
		opts.Recursion = &policy
		// Always sent alongside the policy (even when empty) so an ACL
		// cleared down to nothing actually clears it on the server, rather
		// than a nil pointer leaving a stale list in place.
		acl := rec.NetworkACL
		opts.RecursionNetworkACL = &acl
	}

	if cache := spec.Cache; cache != nil {
		opts.ServeStale = cache.ServeStale
		opts.ServeStaleTTL = cache.ServeStaleTTLSeconds
		opts.CacheMaximumRecordTTL = cache.MaximumRecordTTLSeconds
		opts.CacheMinimumRecordTTL = cache.MinimumRecordTTLSeconds
	}

	if logging := spec.Logging; logging != nil {
		opts.EnableLogging = logging.Enabled
		opts.LogQueries = logging.LogQueries
		opts.UseLocalTime = logging.UseLocalTime
		opts.MaxLogFileDays = logging.MaxFileDays
	}

	return opts
}

// serverSettingsInSync reports whether current already matches every field
// desired sets. Only fields desired actually carries an opinion on are
// compared: a nil field in desired means that group was absent from the spec
// and is not this resource's concern.
func serverSettingsInSync(desired technitium.SetDNSSettingsOptions, current *technitium.DNSSettings) bool {
	if desired.Forwarders != nil && !stringSlicesEqual(*desired.Forwarders, current.Forwarders) {
		return false
	}
	if desired.ForwarderProtocol != nil && *desired.ForwarderProtocol != current.ForwarderProtocol {
		return false
	}
	if desired.Recursion != nil && *desired.Recursion != current.Recursion {
		return false
	}
	if desired.RecursionNetworkACL != nil && !stringSlicesEqual(*desired.RecursionNetworkACL, current.RecursionNetworkACL) {
		return false
	}
	if desired.ServeStale != nil && *desired.ServeStale != current.ServeStale {
		return false
	}
	if desired.ServeStaleTTL != nil && *desired.ServeStaleTTL != current.ServeStaleTTL {
		return false
	}
	if desired.CacheMaximumRecordTTL != nil && *desired.CacheMaximumRecordTTL != current.CacheMaximumRecordTTL {
		return false
	}
	if desired.CacheMinimumRecordTTL != nil && *desired.CacheMinimumRecordTTL != current.CacheMinimumRecordTTL {
		return false
	}
	if desired.EnableLogging != nil && *desired.EnableLogging != current.EnableLogging {
		return false
	}
	if desired.LogQueries != nil && *desired.LogQueries != current.LogQueries {
		return false
	}
	if desired.UseLocalTime != nil && *desired.UseLocalTime != current.UseLocalTime {
		return false
	}
	if desired.MaxLogFileDays != nil && *desired.MaxLogFileDays != current.MaxLogFileDays {
		return false
	}
	return true
}

// stringSlicesEqual compares two string slices element by element, treating a
// nil slice and an empty slice as equal. A spec group present with its list
// field omitted yields a nil desired slice, while the server reports an empty
// list as []; without this, that pair would read as drift and trigger a
// pointless write on every reconcile.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// observedFromDNSSettings builds the status snapshot for the managed fields
// only. Forwarders, ForwarderProtocol, Recursion, and RecursionNetworkACL are
// always recorded since the server always reports some value for them (even
// "no forwarders configured"); the cache and logging pointer fields mirror
// the server's current values regardless of whether this resource's spec sets
// that group, since the server has a real value there either way.
func observedFromDNSSettings(current *technitium.DNSSettings) *dnsv1alpha1.ObservedServerSettings {
	serveStale := current.ServeStale
	serveStaleTTL := current.ServeStaleTTL
	cacheMaxTTL := current.CacheMaximumRecordTTL
	cacheMinTTL := current.CacheMinimumRecordTTL
	enableLogging := current.EnableLogging
	logQueries := current.LogQueries
	useLocalTime := current.UseLocalTime
	maxLogFileDays := current.MaxLogFileDays

	return &dnsv1alpha1.ObservedServerSettings{
		Forwarders:                   current.Forwarders,
		ForwarderProtocol:            current.ForwarderProtocol,
		Recursion:                    current.Recursion,
		RecursionNetworkACL:          current.RecursionNetworkACL,
		ServeStale:                   &serveStale,
		ServeStaleTTLSeconds:         &serveStaleTTL,
		CacheMaximumRecordTTLSeconds: &cacheMaxTTL,
		CacheMinimumRecordTTLSeconds: &cacheMinTTL,
		LoggingEnabled:               &enableLogging,
		LogQueries:                   &logQueries,
		UseLocalTime:                 &useLocalTime,
		MaxLogFileDays:               &maxLogFileDays,
	}
}

// markReady re-fetches the ServerSettings and records a successful reconcile:
// Ready True, Degraded and Progressing cleared, and Observed set from
// current, the settings snapshot reconcileServerSettings already fetched (no
// need for a second round trip to the server here). Re-fetching the resource
// avoids writing status onto a stale object that another writer has since
// changed.
func (r *ServerSettingsReconciler) markReady(ctx context.Context, key client.ObjectKey, current *technitium.DNSSettings) error {
	var settings dnsv1alpha1.ServerSettings
	if err := r.Get(ctx, key, &settings); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Only write status when something actually changed. A blind Update on
	// every requeue would bump resourceVersion and fire a watch event each
	// drift tick even when the server is already in the desired state.
	changed := setServerSettingsCondition(&settings, conditionReady, metav1.ConditionTrue,
		"SettingsApplied", "ServerSettings reconciled on the Technitium server")
	changed = setServerSettingsCondition(&settings, conditionProgressing, metav1.ConditionFalse,
		"SettingsApplied", "ServerSettings reconciled on the Technitium server") || changed
	changed = setServerSettingsCondition(&settings, conditionDegraded, metav1.ConditionFalse,
		"SettingsApplied", "ServerSettings reconciled on the Technitium server") || changed

	if settings.Status.ObservedGeneration != settings.Generation {
		settings.Status.ObservedGeneration = settings.Generation
		changed = true
	}

	observed := observedFromDNSSettings(current)
	if !reflect.DeepEqual(settings.Status.Observed, observed) {
		settings.Status.Observed = observed
		changed = true
	}

	if !changed {
		return nil
	}

	return r.Status().Update(ctx, &settings)
}

// markDegraded re-fetches the ServerSettings and records a failed reconcile
// so the failure is visible on the resource and not only in the logs. Ready
// flips False and Degraded True, both carrying the cause.
func (r *ServerSettingsReconciler) markDegraded(ctx context.Context, key client.ObjectKey, cause error) error {
	var settings dnsv1alpha1.ServerSettings
	if err := r.Get(ctx, key, &settings); err != nil {
		return client.IgnoreNotFound(err)
	}

	setServerSettingsCondition(&settings, conditionReady, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setServerSettingsCondition(&settings, conditionProgressing, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setServerSettingsCondition(&settings, conditionDegraded, metav1.ConditionTrue, "ReconcileFailed", cause.Error())

	return r.Status().Update(ctx, &settings)
}

// setServerSettingsCondition upserts a status condition stamped with the
// resource's current generation and reports whether it changed anything.
func setServerSettingsCondition(settings *dnsv1alpha1.ServerSettings, condType string, status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&settings.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: settings.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServerSettingsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dnsv1alpha1.ServerSettings{}).
		Named("serversettings").
		Complete(r)
}

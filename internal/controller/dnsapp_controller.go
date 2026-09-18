/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// dnsAppFinalizer guards server-side cleanup: while it is present, Kubernetes
// will not remove the DNSApp object, giving the controller a chance to
// uninstall the app from Technitium before the resource disappears.
const dnsAppFinalizer = "dns.packet.fail/dnsapp-cleanup"

// DNSAppAPI is the subset of the Technitium client the reconciler depends on.
// Depending on the interface rather than the concrete client keeps the
// reconciliation logic testable with a fake server.
type DNSAppAPI interface {
	ListApps(ctx context.Context) ([]technitium.DNSAppInfo, error)
	DownloadAndInstallApp(ctx context.Context, name, downloadURL string) error
	DownloadAndUpdateApp(ctx context.Context, name, downloadURL string) error
	UninstallApp(ctx context.Context, name string) error
	GetAppConfig(ctx context.Context, name string) (string, error)
	SetAppConfig(ctx context.Context, name, appConfig string) error
}

// cachedDNSAppClient is one entry in DNSAppReconciler's client cache: a built
// DNSAppAPI plus the resourceVersion of the admin Secret it was built from.
// Kept separate from the other CRDs' caches (same shape, different element
// type) so this CRD's controller stays independent of theirs.
type cachedDNSAppClient struct {
	secretResourceVersion string
	api                   DNSAppAPI
}

// DNSAppReconciler reconciles a DNSApp object.
type DNSAppReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// OperatorNamespace is where a TechnitiumCluster's admin Secret lives when
	// its spec does not override the namespace. It is the operator's own
	// namespace (POD_NAMESPACE), matching the other CRD controllers.
	OperatorNamespace string
	// NewServerClient resolves a DNSApp's serverRef to a client for that
	// managed instance. It is a seam so tests inject a fake; production leaves
	// it nil and defaultServerClient resolves the TechnitiumCluster endpoint +
	// admin Secret and builds a real client.
	NewServerClient func(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (DNSAppAPI, error)

	// clientCacheMu guards clientCache. Reconcile runs are not necessarily
	// serialized (controller-runtime workers), so the cache needs its own
	// lock rather than relying on the caller.
	clientCacheMu sync.Mutex
	// clientCache holds one built client per TechnitiumCluster name, so a
	// routine drift reconcile reuses the existing client instead of
	// re-reading the Secret and reconstructing it every time.
	clientCache map[string]cachedDNSAppClient
}

// +kubebuilder:rbac:groups=dns.packet.fail,resources=dnsapps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dns.packet.fail,resources=dnsapps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.packet.fail,resources=dnsapps/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.packet.fail,resources=technitiumclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile ensures the referenced Technitium server has the DNS App
// installed, at the declared version, with the declared configuration.
func (r *DNSAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var app dnsv1alpha1.DNSApp
	if err := r.Get(ctx, req.NamespacedName, &app); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A set deletion timestamp means the resource is being torn down: run the
	// teardown hook and drop the finalizer instead of reconciling desired
	// state.
	if !app.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalizeDNSApp(ctx, &app)
	}

	// Register the finalizer before touching the server so a delete that
	// arrives mid-flight still triggers the teardown hook. reconcileDNSApp
	// only reads the spec, so it is safe to continue with the same object
	// after the update.
	if controllerutil.AddFinalizer(&app, dnsAppFinalizer) {
		if err := r.Update(ctx, &app); err != nil {
			return ctrl.Result{}, err
		}
	}

	installedVersion, err := r.reconcileDNSApp(ctx, &app)
	if err != nil {
		log.Error(err, "Failed to reconcile DNSApp", "appName", app.Spec.AppName, "server", app.Spec.ServerRef.Name)
		if statusErr := r.markDegraded(ctx, req.NamespacedName, err); statusErr != nil {
			log.Error(statusErr, "Failed to update DNSApp status", "appName", app.Spec.AppName)
		}
		return ctrl.Result{}, err
	}

	if err := r.markReady(ctx, req.NamespacedName, installedVersion); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: driftReconcileInterval}, nil
}

// serverClientFor resolves a DNSApp's serverRef to a DNSAppAPI, preferring the
// injected NewServerClient seam over the production resolver.
func (r *DNSAppReconciler) serverClientFor(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (DNSAppAPI, error) {
	if r.NewServerClient != nil {
		return r.NewServerClient(ctx, serverRef)
	}
	return r.defaultServerClient(ctx, serverRef)
}

// defaultServerClient resolves a TechnitiumCluster's endpoint and admin
// Secret and builds a real Technitium client for it, caching the result by
// the Secret's resourceVersion so a routine reconcile does not re-login on
// every pass. This mirrors RecordReconciler.defaultServerClient; see
// cachedDNSAppClient for why the cache itself is not shared.
func (r *DNSAppReconciler) defaultServerClient(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (DNSAppAPI, error) {
	var tc dnsv1alpha1.TechnitiumCluster
	if err := r.Get(ctx, client.ObjectKey{Name: serverRef.Name}, &tc); err != nil {
		// Wrapped rather than replaced so apierrors.IsNotFound still recognizes
		// it, matching how Record and Blocklist tell "server is gone" apart from
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
func (r *DNSAppReconciler) cachedClient(clusterName, secretResourceVersion string) (DNSAppAPI, bool) {
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
func (r *DNSAppReconciler) cacheClient(clusterName, secretResourceVersion string, api DNSAppAPI) {
	r.clientCacheMu.Lock()
	defer r.clientCacheMu.Unlock()

	if r.clientCache == nil {
		r.clientCache = make(map[string]cachedDNSAppClient)
	}
	r.clientCache[clusterName] = cachedDNSAppClient{secretResourceVersion: secretResourceVersion, api: api}
}

// reconcileDNSApp brings the server's installed app, its version, and its
// configuration in line with the spec, and returns the version now installed
// so the caller can populate status without a second round trip.
func (r *DNSAppReconciler) reconcileDNSApp(ctx context.Context, app *dnsv1alpha1.DNSApp) (string, error) {
	log := logf.FromContext(ctx)
	spec := app.Spec

	api, err := r.serverClientFor(ctx, spec.ServerRef)
	if err != nil {
		return "", err
	}

	apps, err := api.ListApps(ctx)
	if err != nil {
		return "", err
	}

	current := findAppByName(apps, spec.AppName)
	if current == nil {
		if err := api.DownloadAndInstallApp(ctx, spec.AppName, spec.URL); err != nil {
			// A concurrent install (another replica, a manual action) races us
			// to the same app; treat that as the success it effectively is.
			if !errors.Is(err, technitium.ErrAppAlreadyInstalled) {
				return "", err
			}
		}
		log.Info("Installed DNS App", "appName", spec.AppName)

		apps, err = api.ListApps(ctx)
		if err != nil {
			return "", err
		}
		current = findAppByName(apps, spec.AppName)
	} else if spec.Version != "" && current.Version != spec.Version {
		if err := api.DownloadAndUpdateApp(ctx, spec.AppName, spec.URL); err != nil {
			return "", err
		}
		log.Info("Updated DNS App", "appName", spec.AppName, "version", spec.Version)

		apps, err = api.ListApps(ctx)
		if err != nil {
			return "", err
		}
		current = findAppByName(apps, spec.AppName)
	}

	if err := r.reconcileConfig(ctx, api, spec); err != nil {
		return "", err
	}

	if current == nil {
		// The install/update call reported success but the app is absent from
		// a follow-up list: surface this rather than reporting a version that
		// was never actually observed.
		return "", fmt.Errorf("technitium: app %q not found in app list after install", spec.AppName)
	}
	return current.Version, nil
}

// reconcileConfig applies spec.Config to the app when it is set and differs
// from what is currently configured. A SetAppConfig failure (for example the
// app rejecting a malformed config) is returned as-is so the caller records it
// as a Degraded condition instead of the reconciler ever panicking on it.
func (r *DNSAppReconciler) reconcileConfig(ctx context.Context, api DNSAppAPI, spec dnsv1alpha1.DNSAppSpec) error {
	if spec.Config == "" {
		return nil
	}

	current, err := api.GetAppConfig(ctx, spec.AppName)
	if err != nil {
		return err
	}
	if current == spec.Config {
		return nil
	}

	return api.SetAppConfig(ctx, spec.AppName, spec.Config)
}

// findAppByName returns the installed app named name from apps, or nil when
// it is not present.
func findAppByName(apps []technitium.DNSAppInfo, name string) *technitium.DNSAppInfo {
	for i := range apps {
		if apps[i].Name == name {
			return &apps[i]
		}
	}
	return nil
}

// finalizeDNSApp uninstalls the app from the server (unless orphaned) and then
// clears the finalizer. Modeled on finalizeRecord.
func (r *DNSAppReconciler) finalizeDNSApp(ctx context.Context, app *dnsv1alpha1.DNSApp) error {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(app, dnsAppFinalizer) {
		return nil
	}

	if app.Spec.DeletionPolicy == dnsv1alpha1.DeletionPolicyOrphan {
		log.Info("Orphaning DNSApp: leaving the app installed on the server", "appName", app.Spec.AppName)
	} else {
		api, err := r.serverClientFor(ctx, app.Spec.ServerRef)
		switch {
		case err == nil:
			if err := api.UninstallApp(ctx, app.Spec.AppName); err != nil {
				if !errors.Is(err, technitium.ErrAppNotInstalled) {
					return err
				}
			}
			log.Info("Uninstalled DNS App from the Technitium server", "appName", app.Spec.AppName)
		case apierrors.IsNotFound(err):
			// The TechnitiumCluster itself is gone, so the app it hosted is
			// gone with it: there is nothing left to uninstall, and waiting
			// for it to come back would orphan this finalizer forever.
			log.Info("TechnitiumCluster for DNSApp is gone; skipping server-side uninstall",
				"appName", app.Spec.AppName, "server", app.Spec.ServerRef.Name)
		default:
			// Not ready, not bootstrapped, or some other resolve failure:
			// requeue and retry rather than dropping the finalizer and
			// potentially orphaning an app that still exists on the server.
			return err
		}
	}

	controllerutil.RemoveFinalizer(app, dnsAppFinalizer)
	return r.Update(ctx, app)
}

// markReady re-fetches the DNSApp and records a successful reconcile: Ready
// True, Degraded and Progressing cleared, and status.installedVersion set.
// Re-fetching avoids writing status onto a stale object that another writer
// has since changed.
func (r *DNSAppReconciler) markReady(ctx context.Context, key client.ObjectKey, installedVersion string) error {
	var app dnsv1alpha1.DNSApp
	if err := r.Get(ctx, key, &app); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Only write status when something actually changed. A blind Update on
	// every requeue would bump resourceVersion and fire a watch event each
	// drift tick even when the app is already in the desired state.
	changed := setDNSAppCondition(&app, conditionReady, metav1.ConditionTrue,
		"DNSAppInstalled", "DNS App reconciled on the Technitium server")
	changed = setDNSAppCondition(&app, conditionProgressing, metav1.ConditionFalse,
		"DNSAppInstalled", "DNS App reconciled on the Technitium server") || changed
	changed = setDNSAppCondition(&app, conditionDegraded, metav1.ConditionFalse,
		"DNSAppInstalled", "DNS App reconciled on the Technitium server") || changed

	if app.Status.ObservedGeneration != app.Generation {
		app.Status.ObservedGeneration = app.Generation
		changed = true
	}
	if app.Status.InstalledVersion != installedVersion {
		app.Status.InstalledVersion = installedVersion
		changed = true
	}

	if !changed {
		return nil
	}

	return r.Status().Update(ctx, &app)
}

// markDegraded re-fetches the DNSApp and records a failed reconcile so the
// failure is visible on the resource and not only in the logs. Ready flips
// False and Degraded True, both carrying the cause. This is how an invalid
// config (SetAppConfig rejected by the app) surfaces: as a NotReady/Degraded
// resource, never as a controller crash.
func (r *DNSAppReconciler) markDegraded(ctx context.Context, key client.ObjectKey, cause error) error {
	var app dnsv1alpha1.DNSApp
	if err := r.Get(ctx, key, &app); err != nil {
		return client.IgnoreNotFound(err)
	}

	setDNSAppCondition(&app, conditionReady, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setDNSAppCondition(&app, conditionProgressing, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setDNSAppCondition(&app, conditionDegraded, metav1.ConditionTrue, "ReconcileFailed", cause.Error())

	return r.Status().Update(ctx, &app)
}

// setDNSAppCondition upserts a status condition stamped with the resource's
// current generation and reports whether it changed anything.
func setDNSAppCondition(app *dnsv1alpha1.DNSApp, condType string, status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&app.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: app.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *DNSAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dnsv1alpha1.DNSApp{}).
		Named("dnsapp").
		Complete(r)
}

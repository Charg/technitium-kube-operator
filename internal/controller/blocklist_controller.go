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

// blocklistFinalizer guards server-side cleanup: while it is present,
// Kubernetes will not remove the Blocklist object, giving the controller a
// chance to unwind the block list URLs and manual overrides it applied
// before the resource disappears.
const blocklistFinalizer = "dns.packet.fail/blocklist-cleanup"

// BlocklistAPI is the subset of the Technitium client the reconciler depends
// on. Depending on the interface rather than the concrete client keeps the
// reconciliation logic testable with a fake server.
type BlocklistAPI interface {
	GetDNSSettings(ctx context.Context) (*technitium.DNSSettings, error)
	SetDNSSettings(ctx context.Context, opts technitium.SetDNSSettingsOptions) error
	ForceUpdateBlockLists(ctx context.Context) error
	AddAllowedZone(ctx context.Context, domain string) error
	DeleteAllowedZone(ctx context.Context, domain string) error
	AddBlockedZone(ctx context.Context, domain string) error
	DeleteBlockedZone(ctx context.Context, domain string) error
}

// cachedBlocklistClient is one entry in BlocklistReconciler's client cache: a
// built BlocklistAPI plus the resourceVersion of the admin Secret it was
// built from. Kept separate from the other CRDs' caches (same shape,
// different element type) so this CRD's controller stays independent of
// theirs.
type cachedBlocklistClient struct {
	secretResourceVersion string
	api                   BlocklistAPI
}

// BlocklistReconciler reconciles a Blocklist object.
type BlocklistReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// OperatorNamespace is where a TechnitiumCluster's admin Secret lives when
	// its spec does not override the namespace. It is the operator's own
	// namespace (POD_NAMESPACE), matching the other CRD controllers.
	OperatorNamespace string
	// NewServerClient resolves a Blocklist's serverRef to a client for that
	// managed instance. It is a seam so tests inject a fake; production
	// leaves it nil and defaultServerClient resolves the TechnitiumCluster
	// endpoint + admin Secret and builds a real client.
	NewServerClient func(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (BlocklistAPI, error)

	// clientCacheMu guards clientCache. Reconcile runs are not necessarily
	// serialized (controller-runtime workers), so the cache needs its own
	// lock rather than relying on the caller.
	clientCacheMu sync.Mutex
	// clientCache holds one built client per TechnitiumCluster name, so a
	// routine drift reconcile reuses the existing client instead of
	// re-reading the Secret and reconstructing it every time.
	clientCache map[string]cachedBlocklistClient
}

// appliedBlocklist captures what the reconciler put on the server, so
// markReady can populate status without a second round trip.
type appliedBlocklist struct {
	BlockListURLs  []string
	AllowedDomains []string
	BlockedDomains []string
}

// +kubebuilder:rbac:groups=dns.packet.fail,resources=blocklists,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dns.packet.fail,resources=blocklists/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.packet.fail,resources=blocklists/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.packet.fail,resources=technitiumclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile ensures the referenced Technitium server's blocking configuration
// matches the managed fields of the Blocklist spec.
func (r *BlocklistReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var bl dnsv1alpha1.Blocklist
	if err := r.Get(ctx, req.NamespacedName, &bl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A set deletion timestamp means the resource is being torn down: run the
	// teardown hook and drop the finalizer instead of reconciling desired
	// state.
	if !bl.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalizeBlocklist(ctx, &bl)
	}

	// Register the finalizer before touching the server so a delete that
	// arrives mid-flight still triggers the teardown hook. reconcileBlocklist
	// only reads the spec, so it is safe to continue with the same object
	// after the update.
	if controllerutil.AddFinalizer(&bl, blocklistFinalizer) {
		if err := r.Update(ctx, &bl); err != nil {
			return ctrl.Result{}, err
		}
	}

	applied, err := r.reconcileBlocklist(ctx, &bl)
	if err != nil {
		log.Error(err, "Failed to reconcile Blocklist", "server", bl.Spec.ServerRef.Name)
		if statusErr := r.markDegraded(ctx, req.NamespacedName, err); statusErr != nil {
			log.Error(statusErr, "Failed to update Blocklist status", "server", bl.Spec.ServerRef.Name)
		}
		return ctrl.Result{}, err
	}

	if err := r.markReady(ctx, req.NamespacedName, applied); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: driftReconcileInterval}, nil
}

// serverClientFor resolves a Blocklist's serverRef to a BlocklistAPI,
// preferring the injected NewServerClient seam over the production resolver.
func (r *BlocklistReconciler) serverClientFor(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (BlocklistAPI, error) {
	if r.NewServerClient != nil {
		return r.NewServerClient(ctx, serverRef)
	}
	return r.defaultServerClient(ctx, serverRef)
}

// defaultServerClient resolves a TechnitiumCluster's endpoint and admin
// Secret and builds a real Technitium client for it, caching the result by
// the Secret's resourceVersion so a routine reconcile does not re-login on
// every pass. This mirrors ServerSettingsReconciler.defaultServerClient; see
// cachedBlocklistClient for why the cache itself is not shared.
func (r *BlocklistReconciler) defaultServerClient(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (BlocklistAPI, error) {
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
func (r *BlocklistReconciler) cachedClient(clusterName, secretResourceVersion string) (BlocklistAPI, bool) {
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
func (r *BlocklistReconciler) cacheClient(clusterName, secretResourceVersion string, api BlocklistAPI) {
	r.clientCacheMu.Lock()
	defer r.clientCacheMu.Unlock()

	if r.clientCache == nil {
		r.clientCache = make(map[string]cachedBlocklistClient)
	}
	r.clientCache[clusterName] = cachedBlocklistClient{secretResourceVersion: secretResourceVersion, api: api}
}

// reconcileBlocklist brings the server's blocking configuration in line with
// the spec and returns what it applied, so the caller can populate status
// without re-deriving it from a second GetDNSSettings call.
func (r *BlocklistReconciler) reconcileBlocklist(ctx context.Context, bl *dnsv1alpha1.Blocklist) (*appliedBlocklist, error) {
	log := logf.FromContext(ctx)
	spec := bl.Spec

	api, err := r.serverClientFor(ctx, spec.ServerRef)
	if err != nil {
		return nil, err
	}

	current, err := api.GetDNSSettings(ctx)
	if err != nil {
		return nil, err
	}

	var desired technitium.SetDNSSettingsOptions
	desired.EnableBlocking = spec.Enabled
	if spec.BlockingType != nil {
		blockingType := string(*spec.BlockingType)
		desired.BlockingType = &blockingType
	}
	// Always sent, even when empty, so an emptied URL list actually clears on
	// the server rather than a nil pointer leaving stale URLs in place (same
	// rationale as ServerSettings' recursion ACL).
	blockListURLs := spec.BlockListURLs
	desired.BlockListURLs = &blockListURLs
	desired.BlockListUpdateIntervalHours = spec.UpdateIntervalHours

	// A refresh is owed when the desired URL set differs from what was last
	// successfully applied and refreshed (tracked in status), not from the
	// server's current URLs. status.Applied only advances after both the write
	// and the refresh succeed, so a refresh that fails after the settings write
	// has already landed is retried on the next reconcile instead of being lost
	// once the server reports the new URLs as current.
	urlsNeedRefresh := !stringSlicesEqual(spec.BlockListURLs, bl.Status.AppliedBlockListURLs)

	if !blocklistSettingsInSync(desired, current) {
		if err := api.SetDNSSettings(ctx, desired); err != nil {
			return nil, err
		}
		log.Info("Applied Blocklist settings", "server", spec.ServerRef.Name)
	}

	if urlsNeedRefresh {
		// An added or removed URL needs an immediate refresh rather than
		// waiting for the update interval to roll around. Kept out of the
		// settings-in-sync branch so a refresh owed from a prior failure runs
		// even when the URLs are already written to the server.
		if err := api.ForceUpdateBlockLists(ctx); err != nil {
			return nil, err
		}
	}

	if err := reconcileDomainSet(ctx, bl.Status.AppliedAllowedDomains, spec.AllowedDomains, api.AddAllowedZone, api.DeleteAllowedZone); err != nil {
		return nil, err
	}
	if err := reconcileDomainSet(ctx, bl.Status.AppliedBlockedDomains, spec.BlockedDomains, api.AddBlockedZone, api.DeleteBlockedZone); err != nil {
		return nil, err
	}

	return &appliedBlocklist{
		BlockListURLs:  spec.BlockListURLs,
		AllowedDomains: spec.AllowedDomains,
		BlockedDomains: spec.BlockedDomains,
	}, nil
}

// blocklistSettingsInSync reports whether current already matches every
// field desired sets. Only the fields Blocklist owns are compared: a nil
// field in desired means that value was not part of this reconcile's
// concern (BlockListURLs is always non-nil here, since Blocklist owns the
// full set).
func blocklistSettingsInSync(desired technitium.SetDNSSettingsOptions, current *technitium.DNSSettings) bool {
	if desired.EnableBlocking != nil && *desired.EnableBlocking != current.EnableBlocking {
		return false
	}
	if desired.BlockingType != nil && *desired.BlockingType != current.BlockingType {
		return false
	}
	if desired.BlockListURLs != nil && !stringSlicesEqual(*desired.BlockListURLs, current.BlockListURLs) {
		return false
	}
	if desired.BlockListUpdateIntervalHours != nil && *desired.BlockListUpdateIntervalHours != current.BlockListUpdateIntervalHours {
		return false
	}
	return true
}

// reconcileDomainSet converges a manual override zone (allowed or blocked) on
// the server to match desired, using applied (the set from status, i.e. what
// was applied last reconcile) to know what to remove. It returns the first
// error encountered rather than persisting partial progress: since
// status.Applied is only advanced to desired on full success, a failed
// reconcile requeues and retries from the same starting point, and add/delete
// are idempotent on the server so re-running is safe.
func reconcileDomainSet(ctx context.Context, applied, desired []string, add, del func(ctx context.Context, domain string) error) error {
	appliedSet := make(map[string]struct{}, len(applied))
	for _, domain := range applied {
		appliedSet[domain] = struct{}{}
	}
	desiredSet := make(map[string]struct{}, len(desired))
	for _, domain := range desired {
		desiredSet[domain] = struct{}{}
	}

	for _, domain := range desired {
		if _, ok := appliedSet[domain]; ok {
			continue
		}
		if err := add(ctx, domain); err != nil {
			return err
		}
	}
	for _, domain := range applied {
		if _, ok := desiredSet[domain]; ok {
			continue
		}
		if err := del(ctx, domain); err != nil {
			return err
		}
	}
	return nil
}

// finalizeBlocklist removes the managed block list URLs and manual overrides
// from the server (unless orphaned) and then clears the finalizer. Modeled on
// finalizeRecord.
func (r *BlocklistReconciler) finalizeBlocklist(ctx context.Context, bl *dnsv1alpha1.Blocklist) error {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(bl, blocklistFinalizer) {
		return nil
	}

	if bl.Spec.DeletionPolicy == dnsv1alpha1.DeletionPolicyOrphan {
		log.Info("Orphaning Blocklist: leaving the server-side blocking configuration in place", "server", bl.Spec.ServerRef.Name)
	} else {
		api, err := r.serverClientFor(ctx, bl.Spec.ServerRef)
		switch {
		case err == nil:
			// Only the URLs and overrides this resource owns are removed;
			// EnableBlocking is left untouched since this resource never
			// forced blocking off, only added URLs to block with.
			emptyURLs := []string{}
			if err := api.SetDNSSettings(ctx, technitium.SetDNSSettingsOptions{BlockListURLs: &emptyURLs}); err != nil {
				return err
			}
			if err := api.ForceUpdateBlockLists(ctx); err != nil {
				return err
			}
			for _, domain := range bl.Status.AppliedAllowedDomains {
				if err := api.DeleteAllowedZone(ctx, domain); err != nil {
					return err
				}
			}
			for _, domain := range bl.Status.AppliedBlockedDomains {
				if err := api.DeleteBlockedZone(ctx, domain); err != nil {
					return err
				}
			}
			log.Info("Removed managed Blocklist configuration from the Technitium server", "server", bl.Spec.ServerRef.Name)
		case apierrors.IsNotFound(err):
			// The TechnitiumCluster itself is gone, so there is nothing left
			// to clean up: waiting for it to come back would orphan this
			// finalizer forever.
			log.Info("TechnitiumCluster for Blocklist is gone; skipping server-side cleanup", "server", bl.Spec.ServerRef.Name)
		default:
			// Not ready, not bootstrapped, or some other resolve failure:
			// requeue and retry rather than dropping the finalizer and
			// potentially orphaning server state.
			return err
		}
	}

	controllerutil.RemoveFinalizer(bl, blocklistFinalizer)
	return r.Update(ctx, bl)
}

// markReady re-fetches the Blocklist and records a successful reconcile:
// Ready True, Degraded and Progressing cleared, and the Applied* status
// fields set from applied. Re-fetching avoids writing status onto a stale
// object that another writer has since changed.
func (r *BlocklistReconciler) markReady(ctx context.Context, key client.ObjectKey, applied *appliedBlocklist) error {
	var bl dnsv1alpha1.Blocklist
	if err := r.Get(ctx, key, &bl); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Only write status when something actually changed. A blind Update on
	// every requeue would bump resourceVersion and fire a watch event each
	// drift tick even when the server is already in the desired state.
	changed := setBlocklistCondition(&bl, conditionReady, metav1.ConditionTrue,
		"BlocklistApplied", "Blocklist reconciled on the Technitium server")
	changed = setBlocklistCondition(&bl, conditionProgressing, metav1.ConditionFalse,
		"BlocklistApplied", "Blocklist reconciled on the Technitium server") || changed
	changed = setBlocklistCondition(&bl, conditionDegraded, metav1.ConditionFalse,
		"BlocklistApplied", "Blocklist reconciled on the Technitium server") || changed

	if bl.Status.ObservedGeneration != bl.Generation {
		bl.Status.ObservedGeneration = bl.Generation
		changed = true
	}

	if !reflect.DeepEqual(bl.Status.AppliedBlockListURLs, applied.BlockListURLs) {
		bl.Status.AppliedBlockListURLs = applied.BlockListURLs
		changed = true
	}
	if !reflect.DeepEqual(bl.Status.AppliedAllowedDomains, applied.AllowedDomains) {
		bl.Status.AppliedAllowedDomains = applied.AllowedDomains
		changed = true
	}
	if !reflect.DeepEqual(bl.Status.AppliedBlockedDomains, applied.BlockedDomains) {
		bl.Status.AppliedBlockedDomains = applied.BlockedDomains
		changed = true
	}

	if !changed {
		return nil
	}

	return r.Status().Update(ctx, &bl)
}

// markDegraded re-fetches the Blocklist and records a failed reconcile so the
// failure is visible on the resource and not only in the logs. Ready flips
// False and Degraded True, both carrying the cause.
func (r *BlocklistReconciler) markDegraded(ctx context.Context, key client.ObjectKey, cause error) error {
	var bl dnsv1alpha1.Blocklist
	if err := r.Get(ctx, key, &bl); err != nil {
		return client.IgnoreNotFound(err)
	}

	setBlocklistCondition(&bl, conditionReady, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setBlocklistCondition(&bl, conditionProgressing, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setBlocklistCondition(&bl, conditionDegraded, metav1.ConditionTrue, "ReconcileFailed", cause.Error())

	return r.Status().Update(ctx, &bl)
}

// setBlocklistCondition upserts a status condition stamped with the
// resource's current generation and reports whether it changed anything.
func setBlocklistCondition(bl *dnsv1alpha1.Blocklist, condType string, status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&bl.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: bl.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *BlocklistReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dnsv1alpha1.Blocklist{}).
		Named("blocklist").
		Complete(r)
}

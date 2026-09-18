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
	"strings"
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

// dnssecFinalizer guards server-side cleanup: while it is present, Kubernetes
// will not remove the DNSSEC object, giving the controller a chance to unsign
// the zone before the resource disappears.
const dnssecFinalizer = "dns.packet.fail/dnssec-cleanup"

// DNSSECAPI is the subset of the Technitium client the reconciler depends on.
// Depending on the interface rather than the concrete client keeps the
// reconciliation logic testable with a fake server.
type DNSSECAPI interface {
	SignZone(ctx context.Context, opts technitium.SignZoneOptions) error
	UnsignZone(ctx context.Context, zone string) error
	GetDNSSECProperties(ctx context.Context, zone string) (*technitium.DNSSECProperties, error)
}

// cachedDNSSECClient is one entry in DNSSECReconciler's client cache: a built
// DNSSECAPI plus the resourceVersion of the admin Secret it was built from.
// Kept separate from the other CRDs' caches (same shape, different element
// type) so this CRD's controller stays independent of theirs.
type cachedDNSSECClient struct {
	secretResourceVersion string
	api                   DNSSECAPI
}

// DNSSECReconciler reconciles a DNSSEC object.
type DNSSECReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// OperatorNamespace is where a TechnitiumCluster's admin Secret lives when
	// its spec does not override the namespace. It is the operator's own
	// namespace (POD_NAMESPACE), matching the other CRD controllers.
	OperatorNamespace string
	// NewServerClient resolves a DNSSEC's serverRef to a client for that
	// managed instance. It is a seam so tests inject a fake; production
	// leaves it nil and defaultServerClient resolves the TechnitiumCluster
	// endpoint + admin Secret and builds a real client.
	NewServerClient func(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (DNSSECAPI, error)

	// clientCacheMu guards clientCache. Reconcile runs are not necessarily
	// serialized (controller-runtime workers), so the cache needs its own
	// lock rather than relying on the caller.
	clientCacheMu sync.Mutex
	// clientCache holds one built client per TechnitiumCluster name, so a
	// routine drift reconcile reuses the existing client instead of
	// re-reading the Secret and reconstructing it every time.
	clientCache map[string]cachedDNSSECClient
}

// +kubebuilder:rbac:groups=dns.packet.fail,resources=dnssecs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dns.packet.fail,resources=dnssecs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.packet.fail,resources=dnssecs/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.packet.fail,resources=technitiumclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile ensures the referenced zone's DNSSEC signing state matches the
// DNSSEC spec: it signs an unsigned zone and leaves an already-signed zone
// alone.
func (r *DNSSECReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var d dnsv1alpha1.DNSSEC
	if err := r.Get(ctx, req.NamespacedName, &d); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A set deletion timestamp means the resource is being torn down: run
	// server-side cleanup and drop the finalizer instead of reconciling
	// desired state.
	if !d.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalizeDNSSEC(ctx, &d)
	}

	// Register the finalizer before touching the server so a delete that
	// arrives mid-flight still triggers cleanup. reconcileDNSSEC only reads
	// the spec, so it is safe to continue with the same object after the
	// update.
	if controllerutil.AddFinalizer(&d, dnssecFinalizer) {
		if err := r.Update(ctx, &d); err != nil {
			return ctrl.Result{}, err
		}
	}

	signed, err := r.reconcileDNSSEC(ctx, &d)
	if err != nil {
		log.Error(err, "Failed to reconcile DNSSEC", "zone", d.Spec.Zone, "server", d.Spec.ServerRef.Name)
		if statusErr := r.markDegraded(ctx, req.NamespacedName, err); statusErr != nil {
			log.Error(statusErr, "Failed to update DNSSEC status", "zone", d.Spec.Zone)
		}
		return ctrl.Result{}, err
	}

	if err := r.markReady(ctx, req.NamespacedName, signed); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: driftReconcileInterval}, nil
}

// serverClientFor resolves a DNSSEC's serverRef to a DNSSECAPI, preferring
// the injected NewServerClient seam over the production resolver.
func (r *DNSSECReconciler) serverClientFor(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (DNSSECAPI, error) {
	if r.NewServerClient != nil {
		return r.NewServerClient(ctx, serverRef)
	}
	return r.defaultServerClient(ctx, serverRef)
}

// defaultServerClient resolves a TechnitiumCluster's endpoint and admin
// Secret and builds a real Technitium client for it, caching the result by
// the Secret's resourceVersion so a routine reconcile does not re-login on
// every pass. This mirrors RecordReconciler.defaultServerClient; see
// cachedDNSSECClient for why the cache itself is not shared.
func (r *DNSSECReconciler) defaultServerClient(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (DNSSECAPI, error) {
	var tc dnsv1alpha1.TechnitiumCluster
	if err := r.Get(ctx, client.ObjectKey{Name: serverRef.Name}, &tc); err != nil {
		// Wrapped rather than replaced so apierrors.IsNotFound still recognizes
		// it; finalizeDNSSEC depends on that to tell "server is gone" apart
		// from any other resolve failure.
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
func (r *DNSSECReconciler) cachedClient(clusterName, secretResourceVersion string) (DNSSECAPI, bool) {
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
func (r *DNSSECReconciler) cacheClient(clusterName, secretResourceVersion string, api DNSSECAPI) {
	r.clientCacheMu.Lock()
	defer r.clientCacheMu.Unlock()

	if r.clientCache == nil {
		r.clientCache = make(map[string]cachedDNSSECClient)
	}
	r.clientCache[clusterName] = cachedDNSSECClient{secretResourceVersion: secretResourceVersion, api: api}
}

// reconcileDNSSEC brings the zone's signed state in line with the spec and
// returns whether the zone is signed afterward, so the caller can populate
// status without a second round trip.
//
// Reconciling signing parameters on an already-signed zone (algorithm, key
// sizes, NSEC3 iterations, ...) is out of scope for this first cut: Technitium
// has no single "re-sign with new parameters" call, and getting that
// transition right (key rollover timing, avoiding a validation gap) is a
// separate piece of work. An unsigned zone is signed with the desired
// parameters; a zone already signed with the desired nsecType is left alone;
// a zone signed with the other nsecType is also left alone rather than
// thrashed, and is reported as signed regardless of which type it carries.
func (r *DNSSECReconciler) reconcileDNSSEC(ctx context.Context, d *dnsv1alpha1.DNSSEC) (bool, error) {
	log := logf.FromContext(ctx)
	spec := d.Spec

	api, err := r.serverClientFor(ctx, spec.ServerRef)
	if err != nil {
		return false, err
	}

	props, err := api.GetDNSSECProperties(ctx, spec.Zone)
	if err != nil {
		return false, err
	}

	if props.DNSSECStatus == technitium.DNSSECStatusUnsigned {
		if err := api.SignZone(ctx, signZoneOptionsFromSpec(spec)); err != nil {
			return false, err
		}
		log.Info("Signed zone with DNSSEC", "zone", spec.Zone, "algorithm", spec.Algorithm)
		return true, nil
	}

	return true, nil
}

// signZoneOptionsFromSpec maps a DNSSEC spec onto the sign-zone parameters.
func signZoneOptionsFromSpec(spec dnsv1alpha1.DNSSECSpec) technitium.SignZoneOptions {
	opts := technitium.SignZoneOptions{
		Zone:            spec.Zone,
		Algorithm:       string(spec.Algorithm),
		DNSKeyTTL:       spec.DNSKeyTTL,
		ZSKRolloverDays: spec.ZSKRolloverDays,
		Iterations:      spec.NSEC3Iterations,
		SaltLength:      spec.NSEC3SaltLength,
	}

	if spec.HashAlgorithm != nil {
		hashAlgorithm := string(*spec.HashAlgorithm)
		opts.HashAlgorithm = &hashAlgorithm
	}
	if spec.Curve != nil {
		curve := string(*spec.Curve)
		opts.Curve = &curve
	}
	if spec.NSECType != "" {
		nxProof := string(spec.NSECType)
		opts.NxProof = &nxProof
	}
	opts.KSKKeySize = spec.KSKKeySize
	opts.ZSKKeySize = spec.ZSKKeySize

	return opts
}

// finalizeDNSSEC unsigns the zone (unless orphaned) and then clears the
// finalizer. The finalizer is only removed once the server-side unsign has
// confirmed, so a failed unsign requeues with the resource still intact
// rather than leaking the finalizer. A zone that is already unsigned or gone
// is treated as success.
func (r *DNSSECReconciler) finalizeDNSSEC(ctx context.Context, d *dnsv1alpha1.DNSSEC) error {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(d, dnssecFinalizer) {
		return nil
	}

	if d.Spec.DeletionPolicy == dnsv1alpha1.DeletionPolicyOrphan {
		log.Info("Orphaning DNSSEC: leaving the zone signed", "zone", d.Spec.Zone, "server", d.Spec.ServerRef.Name)
	} else {
		api, err := r.serverClientFor(ctx, d.Spec.ServerRef)
		switch {
		case err == nil:
			if err := api.UnsignZone(ctx, d.Spec.Zone); err != nil {
				if !isDNSSECZoneGone(err) {
					return err
				}
			}
			log.Info("Unsigned zone", "zone", d.Spec.Zone, "server", d.Spec.ServerRef.Name)
		case apierrors.IsNotFound(err):
			// The TechnitiumCluster itself is gone, so whatever zone it served
			// is gone with it: there is nothing left to unsign, and waiting
			// for it to come back would orphan this finalizer forever.
			log.Info("TechnitiumCluster for DNSSEC is gone; skipping server-side unsign",
				"zone", d.Spec.Zone, "server", d.Spec.ServerRef.Name)
		default:
			// Not ready, not bootstrapped, or some other resolve failure:
			// requeue and retry rather than dropping the finalizer and
			// potentially leaving a zone signed that was meant to be unsigned.
			return err
		}
	}

	controllerutil.RemoveFinalizer(d, dnssecFinalizer)
	return r.Update(ctx, d)
}

// isDNSSECZoneGone reports whether err indicates there is nothing left to
// unsign: the zone itself is gone, or it was never (or no longer) signed.
// UnsignZone has no dedicated sentinel of its own; technitium.ErrZoneNotFound
// covers the zone-deleted case via classifyStatus's generic "no such zone"
// matching. The "already unsigned" phrasing has not been confirmed against a
// live server, so it is matched on message text here rather than promoted to
// a sentinel in errors.go.
func isDNSSECZoneGone(err error) bool {
	if errors.Is(err, technitium.ErrZoneNotFound) {
		return true
	}
	if apiErr, ok := errors.AsType[*technitium.APIError](err); ok {
		lower := strings.ToLower(apiErr.Message)
		return strings.Contains(lower, "not signed") || strings.Contains(lower, "unsigned")
	}
	return false
}

// markReady re-fetches the DNSSEC and records a successful reconcile: Ready
// True, Degraded and Progressing cleared, and status.signed set from signed.
// Re-fetching avoids writing status onto a stale object that another writer
// has since changed.
func (r *DNSSECReconciler) markReady(ctx context.Context, key client.ObjectKey, signed bool) error {
	var d dnsv1alpha1.DNSSEC
	if err := r.Get(ctx, key, &d); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Only write status when something actually changed. A blind Update on
	// every requeue would bump resourceVersion and fire a watch event each
	// drift tick even when the zone is already in the desired state.
	changed := setDNSSECCondition(&d, conditionReady, metav1.ConditionTrue,
		"DNSSECSigned", "Zone reconciled on the Technitium server")
	changed = setDNSSECCondition(&d, conditionProgressing, metav1.ConditionFalse,
		"DNSSECSigned", "Zone reconciled on the Technitium server") || changed
	changed = setDNSSECCondition(&d, conditionDegraded, metav1.ConditionFalse,
		"DNSSECSigned", "Zone reconciled on the Technitium server") || changed

	if d.Status.ObservedGeneration != d.Generation {
		d.Status.ObservedGeneration = d.Generation
		changed = true
	}
	if d.Status.Signed != signed {
		d.Status.Signed = signed
		changed = true
	}
	if !changed {
		return nil
	}

	return r.Status().Update(ctx, &d)
}

// markDegraded re-fetches the DNSSEC and records a failed reconcile so the
// failure is visible on the resource and not only in the logs. Ready flips
// False and Degraded True, both carrying the cause.
func (r *DNSSECReconciler) markDegraded(ctx context.Context, key client.ObjectKey, cause error) error {
	var d dnsv1alpha1.DNSSEC
	if err := r.Get(ctx, key, &d); err != nil {
		return client.IgnoreNotFound(err)
	}

	setDNSSECCondition(&d, conditionReady, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setDNSSECCondition(&d, conditionProgressing, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setDNSSECCondition(&d, conditionDegraded, metav1.ConditionTrue, "ReconcileFailed", cause.Error())

	return r.Status().Update(ctx, &d)
}

// setDNSSECCondition upserts a status condition stamped with the resource's
// current generation and reports whether it changed anything.
func setDNSSECCondition(d *dnsv1alpha1.DNSSEC, condType string, status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: d.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *DNSSECReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dnsv1alpha1.DNSSEC{}).
		Named("dnssec").
		Complete(r)
}

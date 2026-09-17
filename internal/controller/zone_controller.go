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
	"time"

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

// driftReconcileInterval is how long to wait before re-reconciling a healthy
// zone. Technitium options can be changed out of band (another operator, the web
// console), so the controller polls periodically to correct drift.
const driftReconcileInterval = 5 * time.Minute

// Condition types reported on a Zone's status. Ready is the summary users key on
// (and the Ready printer column); Degraded carries the cause when a reconcile
// fails; Progressing marks an in-flight create or update.
const (
	conditionReady       = "Ready"
	conditionProgressing = "Progressing"
	conditionDegraded    = "Degraded"
)

// ZoneAPI is the subset of the Technitium client the reconciler depends on.
// Depending on the interface rather than the concrete client keeps the
// reconciliation logic testable with a fake server.
type ZoneAPI interface {
	CreateZone(ctx context.Context, opts technitium.CreateZoneOptions) error
	GetZoneOptions(ctx context.Context, zone string) (*technitium.ZoneOptions, error)
	SetZoneOptions(ctx context.Context, zone string, opts technitium.ZoneOptionsUpdate) error
	DeleteZone(ctx context.Context, zone string) error
}

// zoneFinalizer guards server-side cleanup: while it is present, Kubernetes will
// not remove the Zone object, giving the controller a chance to delete the zone
// from Technitium before the resource disappears.
const zoneFinalizer = "dns.packet.fail/zone-cleanup"

// cachedServerClient is one entry in ZoneReconciler's client cache: a built
// ZoneAPI plus the resourceVersion of the admin Secret it was built from. The
// resourceVersion is the invalidation key, so a token rotation (or a Secret
// recreated with a new one) rebuilds the client on the next reconcile instead
// of reusing stale credentials indefinitely.
type cachedServerClient struct {
	secretResourceVersion string
	api                   ZoneAPI
}

// ZoneReconciler reconciles a Zone object
type ZoneReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// OperatorNamespace is where a TechnitiumCluster's admin Secret lives when
	// its spec does not override the namespace. It is the operator's own
	// namespace (POD_NAMESPACE), matching the TechnitiumCluster controller.
	OperatorNamespace string
	// NewServerClient resolves a Zone's serverRef to a client for that managed
	// instance. It is a seam so tests inject a fake; production leaves it nil
	// and defaultServerClient resolves the TechnitiumCluster endpoint + admin
	// Secret and builds a real client.
	NewServerClient func(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (ZoneAPI, error)

	// clientCacheMu guards clientCache. Reconcile runs are not necessarily
	// serialized (controller-runtime workers), so the cache needs its own lock
	// rather than relying on the caller.
	clientCacheMu sync.Mutex
	// clientCache holds one built client per TechnitiumCluster name, so a
	// routine drift reconcile reuses the existing client instead of re-reading
	// the Secret and reconstructing it every time.
	clientCache map[string]cachedServerClient
}

// +kubebuilder:rbac:groups=dns.packet.fail,resources=zones,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dns.packet.fail,resources=zones/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.packet.fail,resources=zones/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.packet.fail,resources=technitiumclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile ensures the Technitium zone matches the Zone spec: it creates the
// zone when absent and corrects detectable option drift when present.
func (r *ZoneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var zone dnsv1alpha1.Zone
	if err := r.Get(ctx, req.NamespacedName, &zone); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A set deletion timestamp means the resource is being torn down: run
	// server-side cleanup and drop the finalizer instead of reconciling desired
	// state.
	if !zone.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalizeZone(ctx, &zone)
	}

	// Register the finalizer before touching the server so a delete that arrives
	// mid-flight still triggers cleanup. reconcileZone only reads the spec, so it
	// is safe to continue with the same object after the update.
	if controllerutil.AddFinalizer(&zone, zoneFinalizer) {
		if err := r.Update(ctx, &zone); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.reconcileZone(ctx, &zone); err != nil {
		log.Error(err, "Failed to reconcile Zone", "zone", zone.Spec.ZoneName)
		if statusErr := r.markDegraded(ctx, req.NamespacedName, err); statusErr != nil {
			log.Error(statusErr, "Failed to update Zone status", "zone", zone.Spec.ZoneName)
		}
		return ctrl.Result{}, err
	}

	if err := r.markReady(ctx, req.NamespacedName); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: driftReconcileInterval}, nil
}

// serverClientFor resolves a Zone's serverRef to a ZoneAPI, preferring the
// injected NewServerClient seam over the production resolver.
func (r *ZoneReconciler) serverClientFor(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (ZoneAPI, error) {
	if r.NewServerClient != nil {
		return r.NewServerClient(ctx, serverRef)
	}
	return r.defaultServerClient(ctx, serverRef)
}

// defaultServerClient resolves a TechnitiumCluster's endpoint and admin
// Secret and builds a real Technitium client for it, caching the result by
// the Secret's resourceVersion so a routine reconcile does not re-login on
// every pass.
func (r *ZoneReconciler) defaultServerClient(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (ZoneAPI, error) {
	var tc dnsv1alpha1.TechnitiumCluster
	if err := r.Get(ctx, client.ObjectKey{Name: serverRef.Name}, &tc); err != nil {
		// Wrapped rather than replaced so apierrors.IsNotFound still recognizes
		// it; finalizeZone depends on that to tell "server is gone" apart from
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
func (r *ZoneReconciler) cachedClient(clusterName, secretResourceVersion string) (ZoneAPI, bool) {
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
func (r *ZoneReconciler) cacheClient(clusterName, secretResourceVersion string, api ZoneAPI) {
	r.clientCacheMu.Lock()
	defer r.clientCacheMu.Unlock()

	if r.clientCache == nil {
		r.clientCache = make(map[string]cachedServerClient)
	}
	r.clientCache[clusterName] = cachedServerClient{secretResourceVersion: secretResourceVersion, api: api}
}

// finalizeZone removes the zone from the server (unless orphaned) and then clears
// the finalizer. The finalizer is only removed once the server-side delete has
// confirmed, so a failed delete requeues with the resource still intact rather
// than leaking the zone. A zone that is already gone is treated as success.
func (r *ZoneReconciler) finalizeZone(ctx context.Context, zone *dnsv1alpha1.Zone) error {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(zone, zoneFinalizer) {
		return nil
	}

	if zone.Spec.DeletionPolicy == dnsv1alpha1.DeletionPolicyOrphan {
		log.Info("Orphaning Zone: leaving the server-side zone in place", "zone", zone.Spec.ZoneName)
	} else {
		api, err := r.serverClientFor(ctx, zone.Spec.ServerRef)
		switch {
		case err == nil:
			if err := api.DeleteZone(ctx, zone.Spec.ZoneName); err != nil {
				if !errors.Is(err, technitium.ErrZoneNotFound) {
					return err
				}
			}
			log.Info("Deleted Zone from the Technitium server", "zone", zone.Spec.ZoneName)
		case apierrors.IsNotFound(err):
			// The TechnitiumCluster itself is gone, so whatever zones it served
			// are gone with it: there is nothing left to delete, and waiting for
			// it to come back would orphan this finalizer forever.
			log.Info("TechnitiumCluster for Zone is gone; skipping server-side delete",
				"zone", zone.Spec.ZoneName, "server", zone.Spec.ServerRef.Name)
		default:
			// Not ready, not bootstrapped, or some other resolve failure: requeue
			// and retry rather than dropping the finalizer and potentially
			// orphaning a zone that still exists on the server.
			return err
		}
	}

	controllerutil.RemoveFinalizer(zone, zoneFinalizer)
	return r.Update(ctx, zone)
}

// reconcileZone brings the server-side zone in line with the spec. It is
// idempotent: an absent zone is created, an existing "already exists" is a
// success, and a present zone has its detectable options corrected.
func (r *ZoneReconciler) reconcileZone(ctx context.Context, zone *dnsv1alpha1.Zone) error {
	log := logf.FromContext(ctx)
	name := zone.Spec.ZoneName

	api, err := r.serverClientFor(ctx, zone.Spec.ServerRef)
	if err != nil {
		return err
	}

	current, err := api.GetZoneOptions(ctx, name)
	switch {
	case err == nil:
		return r.reconcileOptions(ctx, api, zone, current)
	case !errors.Is(err, technitium.ErrZoneNotFound):
		return err
	}

	if err := api.CreateZone(ctx, createOptionsFromSpec(zone)); err != nil {
		// A concurrent create (another replica, a manual action) races us to the
		// same zone; treat that as the success it effectively is.
		if !errors.Is(err, technitium.ErrZoneAlreadyExists) {
			return err
		}
		return nil
	}

	log.Info("Created Zone", "zone", name, "type", zone.Spec.Type)
	return nil
}

// reconcileOptions corrects drift on a zone that already exists. Only options the
// server reports back through GetZoneOptions can be reconciled.
func (r *ZoneReconciler) reconcileOptions(ctx context.Context, api ZoneAPI, zone *dnsv1alpha1.Zone, current *technitium.ZoneOptions) error {
	log := logf.FromContext(ctx)
	name := zone.Spec.ZoneName

	// Technitium cannot change a live zone's type. Surface the mismatch so an
	// operator can delete and recreate rather than have it silently ignored.
	if desired := string(zone.Spec.Type); desired != "" && !strings.EqualFold(current.Type, desired) {
		log.Info("Zone type differs from spec and cannot be changed in place",
			"zone", name, "current", current.Type, "desired", desired)
	}

	var update technitium.ZoneOptionsUpdate
	if zone.Spec.Catalog != nil && current.Catalog != *zone.Spec.Catalog {
		update.Catalog = zone.Spec.Catalog
	}

	if update == (technitium.ZoneOptionsUpdate{}) {
		return nil
	}

	if err := api.SetZoneOptions(ctx, name, update); err != nil {
		return err
	}

	log.Info("Updated Zone options", "zone", name)
	return nil
}

// createOptionsFromSpec maps a Zone spec onto the create-zone parameters. Forwarder
// and catalog fields are only set when present in the spec so the server applies
// its own defaults otherwise.
func createOptionsFromSpec(zone *dnsv1alpha1.Zone) technitium.CreateZoneOptions {
	spec := zone.Spec
	opts := technitium.CreateZoneOptions{
		Zone:                       spec.ZoneName,
		Type:                       string(spec.Type),
		PrimaryNameServerAddresses: spec.PrimaryNameServerAddresses,
	}
	if spec.Forwarder != nil {
		opts.Forwarder = *spec.Forwarder
	}
	if spec.ForwarderProtocol != nil {
		opts.Protocol = string(*spec.ForwarderProtocol)
	}
	if spec.Catalog != nil {
		opts.Catalog = *spec.Catalog
	}
	return opts
}

// markReady re-fetches the Zone and records a successful reconcile: Ready True,
// Degraded and Progressing cleared. Re-fetching avoids writing status onto a stale
// object that another writer has since changed.
func (r *ZoneReconciler) markReady(ctx context.Context, key client.ObjectKey) error {
	var zone dnsv1alpha1.Zone
	if err := r.Get(ctx, key, &zone); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Only write status when something actually changed. A blind Update on every
	// requeue would bump resourceVersion and fire a watch event each drift tick
	// even when the zone is already in the desired state.
	changed := setCondition(&zone, conditionReady, metav1.ConditionTrue,
		"ZoneReady", "Zone reconciled on the Technitium server")
	changed = setCondition(&zone, conditionProgressing, metav1.ConditionFalse,
		"ZoneReady", "Zone reconciled on the Technitium server") || changed
	changed = setCondition(&zone, conditionDegraded, metav1.ConditionFalse,
		"ZoneReady", "Zone reconciled on the Technitium server") || changed

	if zone.Status.ObservedGeneration != zone.Generation {
		zone.Status.ObservedGeneration = zone.Generation
		changed = true
	}
	if !zone.Status.ZoneCreated {
		zone.Status.ZoneCreated = true
		changed = true
	}
	if !changed {
		return nil
	}

	return r.Status().Update(ctx, &zone)
}

// markDegraded re-fetches the Zone and records a failed reconcile so the failure
// is visible on the resource and not only in the logs. Ready flips False and
// Degraded True, both carrying the cause.
func (r *ZoneReconciler) markDegraded(ctx context.Context, key client.ObjectKey, cause error) error {
	var zone dnsv1alpha1.Zone
	if err := r.Get(ctx, key, &zone); err != nil {
		return client.IgnoreNotFound(err)
	}

	setCondition(&zone, conditionReady, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setCondition(&zone, conditionProgressing, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setCondition(&zone, conditionDegraded, metav1.ConditionTrue, "ReconcileFailed", cause.Error())

	return r.Status().Update(ctx, &zone)
}

// setCondition upserts a status condition stamped with the zone's current
// generation and reports whether it changed anything.
func setCondition(zone *dnsv1alpha1.Zone, condType string, status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&zone.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: zone.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *ZoneReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dnsv1alpha1.Zone{}).
		Named("zone").
		Complete(r)
}

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

// dhcpScopeFinalizer guards server-side cleanup: while it is present,
// Kubernetes will not remove the DHCPScope object, giving the controller a
// chance to disable and delete the scope from Technitium before the resource
// disappears.
const dhcpScopeFinalizer = "dns.packet.fail/dhcpscope-cleanup"

// DHCPScopeAPI is the subset of the Technitium client the reconciler depends
// on. Depending on the interface rather than the concrete client keeps the
// reconciliation logic testable with a fake server.
type DHCPScopeAPI interface {
	ListDHCPScopes(ctx context.Context) ([]technitium.DHCPScopeInfo, error)
	GetDHCPScope(ctx context.Context, name string) (*technitium.DHCPScopeDetails, error)
	SetDHCPScope(ctx context.Context, opts technitium.SetDHCPScopeOptions) error
	EnableDHCPScope(ctx context.Context, name string) error
	DisableDHCPScope(ctx context.Context, name string) error
	DeleteDHCPScope(ctx context.Context, name string) error
	AddReservedLease(ctx context.Context, opts technitium.AddReservedLeaseOptions) error
	RemoveReservedLease(ctx context.Context, scopeName, hardwareAddress string) error
}

// cachedDHCPScopeClient is one entry in DHCPScopeReconciler's client cache: a
// built DHCPScopeAPI plus the resourceVersion of the admin Secret it was built
// from. Kept separate from the other CRDs' caches (same shape, different
// element type) so this CRD's controller stays independent of theirs.
type cachedDHCPScopeClient struct {
	secretResourceVersion string
	api                   DHCPScopeAPI
}

// DHCPScopeReconciler reconciles a DHCPScope object.
type DHCPScopeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// OperatorNamespace is where a TechnitiumCluster's admin Secret lives when
	// its spec does not override the namespace. It is the operator's own
	// namespace (POD_NAMESPACE), matching the other CRD controllers.
	OperatorNamespace string
	// NewServerClient resolves a DHCPScope's serverRef to a client for that
	// managed instance. It is a seam so tests inject a fake; production
	// leaves it nil and defaultServerClient resolves the TechnitiumCluster
	// endpoint + admin Secret and builds a real client.
	NewServerClient func(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (DHCPScopeAPI, error)

	// clientCacheMu guards clientCache. Reconcile runs are not necessarily
	// serialized (controller-runtime workers), so the cache needs its own
	// lock rather than relying on the caller.
	clientCacheMu sync.Mutex
	// clientCache holds one built client per TechnitiumCluster name, so a
	// routine drift reconcile reuses the existing client instead of
	// re-reading the Secret and reconstructing it every time.
	clientCache map[string]cachedDHCPScopeClient
}

// +kubebuilder:rbac:groups=dns.packet.fail,resources=dhcpscopes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dns.packet.fail,resources=dhcpscopes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.packet.fail,resources=dhcpscopes/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.packet.fail,resources=technitiumclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile ensures the Technitium DHCP scope matches the DHCPScope spec: it
// creates or updates the scope, converges its reservations, and applies the
// enabled/disabled toggle.
func (r *DHCPScopeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var scope dnsv1alpha1.DHCPScope
	if err := r.Get(ctx, req.NamespacedName, &scope); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A set deletion timestamp means the resource is being torn down: run
	// server-side cleanup and drop the finalizer instead of reconciling
	// desired state.
	if !scope.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalizeDHCPScope(ctx, &scope)
	}

	// Register the finalizer before touching the server so a delete that
	// arrives mid-flight still triggers cleanup. reconcileDHCPScope only
	// reads the spec, so it is safe to continue with the same object after
	// the update.
	if controllerutil.AddFinalizer(&scope, dhcpScopeFinalizer) {
		if err := r.Update(ctx, &scope); err != nil {
			return ctrl.Result{}, err
		}
	}

	applied, err := r.reconcileDHCPScope(ctx, &scope)
	if err != nil {
		log.Error(err, "Failed to reconcile DHCPScope", "scope", scope.Spec.ScopeName, "server", scope.Spec.ServerRef.Name)
		if statusErr := r.markDegraded(ctx, req.NamespacedName, err); statusErr != nil {
			log.Error(statusErr, "Failed to update DHCPScope status", "scope", scope.Spec.ScopeName)
		}
		return ctrl.Result{}, err
	}

	if err := r.markReady(ctx, req.NamespacedName, applied); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: driftReconcileInterval}, nil
}

// serverClientFor resolves a DHCPScope's serverRef to a DHCPScopeAPI,
// preferring the injected NewServerClient seam over the production resolver.
func (r *DHCPScopeReconciler) serverClientFor(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (DHCPScopeAPI, error) {
	if r.NewServerClient != nil {
		return r.NewServerClient(ctx, serverRef)
	}
	return r.defaultServerClient(ctx, serverRef)
}

// defaultServerClient resolves a TechnitiumCluster's endpoint and admin
// Secret and builds a real Technitium client for it, caching the result by
// the Secret's resourceVersion so a routine reconcile does not re-login on
// every pass. This mirrors the other CRD controllers' defaultServerClient;
// see cachedDHCPScopeClient for why the cache itself is not shared.
func (r *DHCPScopeReconciler) defaultServerClient(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (DHCPScopeAPI, error) {
	var tc dnsv1alpha1.TechnitiumCluster
	if err := r.Get(ctx, client.ObjectKey{Name: serverRef.Name}, &tc); err != nil {
		// Wrapped rather than replaced so apierrors.IsNotFound still recognizes
		// it, matching how Record and Blocklist tell "server is gone" apart
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
func (r *DHCPScopeReconciler) cachedClient(clusterName, secretResourceVersion string) (DHCPScopeAPI, bool) {
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
func (r *DHCPScopeReconciler) cacheClient(clusterName, secretResourceVersion string, api DHCPScopeAPI) {
	r.clientCacheMu.Lock()
	defer r.clientCacheMu.Unlock()

	if r.clientCache == nil {
		r.clientCache = make(map[string]cachedDHCPScopeClient)
	}
	r.clientCache[clusterName] = cachedDHCPScopeClient{secretResourceVersion: secretResourceVersion, api: api}
}

// reconcileDHCPScope brings the server-side scope in line with the spec and
// returns the hardware address set it applied, so the caller can populate
// status without a second round trip. Technitium's set endpoint creates the
// scope when absent, so create and update share one call; enabling and
// disabling are separate calls issued after the scope's core fields and
// reservations are in place.
func (r *DHCPScopeReconciler) reconcileDHCPScope(ctx context.Context, scope *dnsv1alpha1.DHCPScope) ([]string, error) {
	log := logf.FromContext(ctx)
	spec := scope.Spec

	api, err := r.serverClientFor(ctx, spec.ServerRef)
	if err != nil {
		return nil, err
	}

	current, err := api.GetDHCPScope(ctx, spec.ScopeName)
	if err != nil && !errors.Is(err, technitium.ErrDHCPScopeNotFound) {
		return nil, err
	}

	desired := setOptionsFromSpec(spec)
	if current == nil || !scopeCoreFieldsMatch(spec, current) {
		if err := api.SetDHCPScope(ctx, desired); err != nil {
			return nil, err
		}
		log.Info("Applied DHCPScope core fields", "scope", spec.ScopeName)
	}

	applied, err := reconcileReservations(ctx, scope.Status.AppliedReservations, spec.Reservations, spec.ScopeName, api)
	if err != nil {
		return nil, err
	}

	currentlyEnabled := current != nil && current.Enabled
	desiredEnabled := spec.Enabled == nil || *spec.Enabled
	if desiredEnabled && !currentlyEnabled {
		if err := api.EnableDHCPScope(ctx, spec.ScopeName); err != nil {
			return nil, err
		}
		log.Info("Enabled DHCPScope", "scope", spec.ScopeName)
	} else if !desiredEnabled && (current == nil || currentlyEnabled) {
		if err := api.DisableDHCPScope(ctx, spec.ScopeName); err != nil {
			return nil, err
		}
		log.Info("Disabled DHCPScope", "scope", spec.ScopeName)
	}

	return applied, nil
}

// setOptionsFromSpec maps a DHCPScope spec onto the set-scope parameters.
func setOptionsFromSpec(spec dnsv1alpha1.DHCPScopeSpec) technitium.SetDHCPScopeOptions {
	opts := technitium.SetDHCPScopeOptions{
		Name:             spec.ScopeName,
		StartingAddress:  spec.StartingAddress,
		EndingAddress:    spec.EndingAddress,
		SubnetMask:       spec.SubnetMask,
		RouterAddress:    spec.RouterAddress,
		LeaseTimeDays:    spec.LeaseTimeDays,
		LeaseTimeHours:   spec.LeaseTimeHours,
		LeaseTimeMinutes: spec.LeaseTimeMinutes,
		DomainName:       spec.DomainName,
	}
	if spec.DNSServers != nil {
		dnsServers := spec.DNSServers
		opts.DNSServers = &dnsServers
	}
	return opts
}

// scopeCoreFieldsMatch reports whether current already matches the spec's
// range, mask, router, DNS servers, and domain name. Lease time components are
// excluded: Technitium's get-scope response was not confirmed to echo them
// back in a directly comparable shape, so they are always resent on the
// SetDHCPScope call that scopeCoreFieldsMatch gates rather than risked as a
// silent drift the controller can never correct.
func scopeCoreFieldsMatch(spec dnsv1alpha1.DHCPScopeSpec, current *technitium.DHCPScopeDetails) bool {
	if spec.StartingAddress != current.StartingAddress ||
		spec.EndingAddress != current.EndingAddress ||
		spec.SubnetMask != current.SubnetMask {
		return false
	}
	if spec.RouterAddress != nil && *spec.RouterAddress != current.RouterAddress {
		return false
	}
	if spec.DNSServers != nil && !stringSlicesEqual(spec.DNSServers, current.DNSServers) {
		return false
	}
	if spec.LeaseTimeDays != nil || spec.LeaseTimeHours != nil || spec.LeaseTimeMinutes != nil {
		return false
	}
	if spec.DomainName != nil {
		return false
	}
	return true
}

// reconcileReservations converges the scope's static reservations against
// applied (the hardware address set from status, i.e. what was applied last
// reconcile), adding, removing, or updating by hardwareAddress, and returns
// the hardware address set now applied. Modeled on reconcileDomainSet, but a
// reservation carries more than a string, so an existing reservation whose
// ipAddress/hostName/comments changed is removed and re-added rather than
// diffed field by field: Technitium's add-reservation endpoint has no partial
// update, and remove-then-add is idempotent the same way record recreation is
// for rdata drift.
func reconcileReservations(ctx context.Context, applied []string, desired []dnsv1alpha1.DHCPReservation, scopeName string, api DHCPScopeAPI) ([]string, error) {
	appliedSet := make(map[string]struct{}, len(applied))
	for _, mac := range applied {
		appliedSet[mac] = struct{}{}
	}
	desiredByMAC := make(map[string]dnsv1alpha1.DHCPReservation, len(desired))
	for _, reservation := range desired {
		desiredByMAC[reservation.HardwareAddress] = reservation
	}

	for _, reservation := range desired {
		if _, ok := appliedSet[reservation.HardwareAddress]; ok {
			continue
		}
		if err := addReservation(ctx, scopeName, reservation, api); err != nil {
			return nil, err
		}
	}
	for _, mac := range applied {
		if _, ok := desiredByMAC[mac]; ok {
			continue
		}
		if err := api.RemoveReservedLease(ctx, scopeName, mac); err != nil && !errors.Is(err, technitium.ErrDHCPReservationNotFound) {
			return nil, err
		}
	}

	appliedMACs := make([]string, 0, len(desired))
	for _, reservation := range desired {
		appliedMACs = append(appliedMACs, reservation.HardwareAddress)
	}
	return appliedMACs, nil
}

// addReservation issues the AddReservedLease call for one reservation.
func addReservation(ctx context.Context, scopeName string, reservation dnsv1alpha1.DHCPReservation, api DHCPScopeAPI) error {
	return api.AddReservedLease(ctx, technitium.AddReservedLeaseOptions{
		ScopeName:       scopeName,
		HardwareAddress: reservation.HardwareAddress,
		IPAddress:       reservation.IPAddress,
		HostName:        reservation.HostName,
		Comments:        reservation.Comments,
	})
}

// finalizeDHCPScope disables and deletes the scope from the server (unless
// orphaned) and then clears the finalizer. Modeled on finalizeRecord and
// finalizeBlocklist.
func (r *DHCPScopeReconciler) finalizeDHCPScope(ctx context.Context, scope *dnsv1alpha1.DHCPScope) error {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(scope, dhcpScopeFinalizer) {
		return nil
	}

	if scope.Spec.DeletionPolicy == dnsv1alpha1.DeletionPolicyOrphan {
		log.Info("Orphaning DHCPScope: leaving the server-side scope in place", "scope", scope.Spec.ScopeName)
	} else {
		api, err := r.serverClientFor(ctx, scope.Spec.ServerRef)
		switch {
		case err == nil:
			if err := api.DisableDHCPScope(ctx, scope.Spec.ScopeName); err != nil && !errors.Is(err, technitium.ErrDHCPScopeNotFound) {
				return err
			}
			if err := api.DeleteDHCPScope(ctx, scope.Spec.ScopeName); err != nil && !errors.Is(err, technitium.ErrDHCPScopeNotFound) {
				return err
			}
			log.Info("Deleted DHCPScope from the Technitium server", "scope", scope.Spec.ScopeName)
		case apierrors.IsNotFound(err):
			// The TechnitiumCluster itself is gone, so whatever scope it
			// served is gone with it: there is nothing left to delete, and
			// waiting for it to come back would orphan this finalizer
			// forever.
			log.Info("TechnitiumCluster for DHCPScope is gone; skipping server-side delete",
				"scope", scope.Spec.ScopeName, "server", scope.Spec.ServerRef.Name)
		default:
			// Not ready, not bootstrapped, or some other resolve failure:
			// requeue and retry rather than dropping the finalizer and
			// potentially orphaning a scope that still exists on the server.
			return err
		}
	}

	controllerutil.RemoveFinalizer(scope, dhcpScopeFinalizer)
	return r.Update(ctx, scope)
}

// markReady re-fetches the DHCPScope and records a successful reconcile:
// Ready True, Degraded and Progressing cleared, scopeCreated true, and
// appliedReservations set from applied. Re-fetching avoids writing status
// onto a stale object that another writer has since changed.
func (r *DHCPScopeReconciler) markReady(ctx context.Context, key client.ObjectKey, applied []string) error {
	var scope dnsv1alpha1.DHCPScope
	if err := r.Get(ctx, key, &scope); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Only write status when something actually changed. A blind Update on
	// every requeue would bump resourceVersion and fire a watch event each
	// drift tick even when the scope is already in the desired state.
	changed := setDHCPScopeCondition(&scope, conditionReady, metav1.ConditionTrue,
		"DHCPScopeReady", "DHCPScope reconciled on the Technitium server")
	changed = setDHCPScopeCondition(&scope, conditionProgressing, metav1.ConditionFalse,
		"DHCPScopeReady", "DHCPScope reconciled on the Technitium server") || changed
	changed = setDHCPScopeCondition(&scope, conditionDegraded, metav1.ConditionFalse,
		"DHCPScopeReady", "DHCPScope reconciled on the Technitium server") || changed

	if scope.Status.ObservedGeneration != scope.Generation {
		scope.Status.ObservedGeneration = scope.Generation
		changed = true
	}
	if !scope.Status.ScopeCreated {
		scope.Status.ScopeCreated = true
		changed = true
	}
	if !reflect.DeepEqual(scope.Status.AppliedReservations, applied) {
		scope.Status.AppliedReservations = applied
		changed = true
	}

	if !changed {
		return nil
	}

	return r.Status().Update(ctx, &scope)
}

// markDegraded re-fetches the DHCPScope and records a failed reconcile so the
// failure is visible on the resource and not only in the logs. Ready flips
// False and Degraded True, both carrying the cause.
func (r *DHCPScopeReconciler) markDegraded(ctx context.Context, key client.ObjectKey, cause error) error {
	var scope dnsv1alpha1.DHCPScope
	if err := r.Get(ctx, key, &scope); err != nil {
		return client.IgnoreNotFound(err)
	}

	setDHCPScopeCondition(&scope, conditionReady, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setDHCPScopeCondition(&scope, conditionProgressing, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setDHCPScopeCondition(&scope, conditionDegraded, metav1.ConditionTrue, "ReconcileFailed", cause.Error())

	return r.Status().Update(ctx, &scope)
}

// setDHCPScopeCondition upserts a status condition stamped with the
// resource's current generation and reports whether it changed anything.
func setDHCPScopeCondition(scope *dnsv1alpha1.DHCPScope, condType string, status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&scope.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: scope.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *DHCPScopeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dnsv1alpha1.DHCPScope{}).
		Named("dhcpscope").
		Complete(r)
}

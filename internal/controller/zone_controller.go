/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"errors"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
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
}

// ZoneReconciler reconciles a Zone object
type ZoneReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Technitium reconciles zones against the DNS server. It is constructed once
	// at startup and shared across reconciles.
	Technitium ZoneAPI
}

// +kubebuilder:rbac:groups=dns.packet.fail,resources=zones,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dns.packet.fail,resources=zones/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.packet.fail,resources=zones/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile ensures the Technitium zone matches the Zone spec: it creates the
// zone when absent and corrects detectable option drift when present.
func (r *ZoneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var zone dnsv1alpha1.Zone
	if err := r.Get(ctx, req.NamespacedName, &zone); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
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

// reconcileZone brings the server-side zone in line with the spec. It is
// idempotent: an absent zone is created, an existing "already exists" is a
// success, and a present zone has its detectable options corrected.
func (r *ZoneReconciler) reconcileZone(ctx context.Context, zone *dnsv1alpha1.Zone) error {
	log := logf.FromContext(ctx)
	name := zone.Spec.ZoneName

	current, err := r.Technitium.GetZoneOptions(ctx, name)
	switch {
	case err == nil:
		return r.reconcileOptions(ctx, zone, current)
	case !errors.Is(err, technitium.ErrZoneNotFound):
		return err
	}

	if err := r.Technitium.CreateZone(ctx, createOptionsFromSpec(zone)); err != nil {
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
func (r *ZoneReconciler) reconcileOptions(ctx context.Context, zone *dnsv1alpha1.Zone, current *technitium.ZoneOptions) error {
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

	if err := r.Technitium.SetZoneOptions(ctx, name, update); err != nil {
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

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

// defaultRecordTTL mirrors the CRD's kubebuilder default for spec.ttl. Reconcile
// logic reads it as a defensive fallback for a Record built by hand (a fake
// client in a unit test, for instance) rather than admitted through the API
// server, since only the latter applies CRD structural defaults.
const defaultRecordTTL int32 = 3600

// recordFinalizer guards server-side cleanup: while it is present, Kubernetes
// will not remove the Record object, giving the controller a chance to delete
// the record from Technitium before the resource disappears.
const recordFinalizer = "dns.packet.fail/record-cleanup"

// RecordAPI is the subset of the Technitium client the reconciler depends on.
// Depending on the interface rather than the concrete client keeps the
// reconciliation logic testable with a fake server.
type RecordAPI interface {
	AddRecord(ctx context.Context, opts technitium.AddRecordOptions) error
	GetRecords(ctx context.Context, zone, domain string) ([]technitium.Record, error)
	UpdateRecord(ctx context.Context, opts technitium.UpdateRecordOptions) error
	DeleteRecord(ctx context.Context, opts technitium.DeleteRecordOptions) error
}

// cachedRecordServerClient is one entry in RecordReconciler's client cache: a
// built RecordAPI plus the resourceVersion of the admin Secret it was built
// from. Kept separate from ZoneReconciler's cachedServerClient (same shape,
// different element type) rather than sharing a generic cache, so this CRD's
// controller stays independent of Zone's and a change to one cannot regress
// the other.
type cachedRecordServerClient struct {
	secretResourceVersion string
	api                   RecordAPI
}

// RecordReconciler reconciles a Record object.
type RecordReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// OperatorNamespace is where a TechnitiumCluster's admin Secret lives when
	// its spec does not override the namespace. It is the operator's own
	// namespace (POD_NAMESPACE), matching the TechnitiumCluster controller.
	OperatorNamespace string
	// NewServerClient resolves a Record's serverRef to a client for that
	// managed instance. It is a seam so tests inject a fake; production leaves
	// it nil and defaultServerClient resolves the TechnitiumCluster endpoint +
	// admin Secret and builds a real client.
	NewServerClient func(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (RecordAPI, error)

	// clientCacheMu guards clientCache. Reconcile runs are not necessarily
	// serialized (controller-runtime workers), so the cache needs its own lock
	// rather than relying on the caller.
	clientCacheMu sync.Mutex
	// clientCache holds one built client per TechnitiumCluster name, so a
	// routine drift reconcile reuses the existing client instead of re-reading
	// the Secret and reconstructing it every time.
	clientCache map[string]cachedRecordServerClient
}

// +kubebuilder:rbac:groups=dns.packet.fail,resources=records,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dns.packet.fail,resources=records/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=dns.packet.fail,resources=records/finalizers,verbs=update
// +kubebuilder:rbac:groups=dns.packet.fail,resources=technitiumclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile ensures the Technitium record matches the Record spec: it creates
// the record when absent and corrects detectable drift (rdata, TTL) when
// present.
func (r *RecordReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var record dnsv1alpha1.Record
	if err := r.Get(ctx, req.NamespacedName, &record); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// A set deletion timestamp means the resource is being torn down: run
	// server-side cleanup and drop the finalizer instead of reconciling desired
	// state.
	if !record.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.finalizeRecord(ctx, &record)
	}

	// Register the finalizer before touching the server so a delete that
	// arrives mid-flight still triggers cleanup. reconcileRecord only reads the
	// spec, so it is safe to continue with the same object after the update.
	if controllerutil.AddFinalizer(&record, recordFinalizer) {
		if err := r.Update(ctx, &record); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.reconcileRecord(ctx, &record); err != nil {
		log.Error(err, "Failed to reconcile Record", "name", record.Spec.Name, "type", record.Spec.Type)
		if statusErr := r.markDegraded(ctx, req.NamespacedName, err); statusErr != nil {
			log.Error(statusErr, "Failed to update Record status", "name", record.Spec.Name)
		}
		return ctrl.Result{}, err
	}

	if err := r.markReady(ctx, req.NamespacedName); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: driftReconcileInterval}, nil
}

// serverClientFor resolves a Record's serverRef to a RecordAPI, preferring the
// injected NewServerClient seam over the production resolver.
func (r *RecordReconciler) serverClientFor(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (RecordAPI, error) {
	if r.NewServerClient != nil {
		return r.NewServerClient(ctx, serverRef)
	}
	return r.defaultServerClient(ctx, serverRef)
}

// defaultServerClient resolves a TechnitiumCluster's endpoint and admin Secret
// and builds a real Technitium client for it, caching the result by the
// Secret's resourceVersion so a routine reconcile does not re-login on every
// pass. This mirrors ZoneReconciler.defaultServerClient; see
// cachedRecordServerClient for why the cache itself is not shared.
func (r *RecordReconciler) defaultServerClient(ctx context.Context, serverRef dnsv1alpha1.SecretReference) (RecordAPI, error) {
	var tc dnsv1alpha1.TechnitiumCluster
	if err := r.Get(ctx, client.ObjectKey{Name: serverRef.Name}, &tc); err != nil {
		// Wrapped rather than replaced so apierrors.IsNotFound still recognizes
		// it; finalizeRecord depends on that to tell "server is gone" apart from
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
func (r *RecordReconciler) cachedClient(clusterName, secretResourceVersion string) (RecordAPI, bool) {
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
func (r *RecordReconciler) cacheClient(clusterName, secretResourceVersion string, api RecordAPI) {
	r.clientCacheMu.Lock()
	defer r.clientCacheMu.Unlock()

	if r.clientCache == nil {
		r.clientCache = make(map[string]cachedRecordServerClient)
	}
	r.clientCache[clusterName] = cachedRecordServerClient{secretResourceVersion: secretResourceVersion, api: api}
}

// finalizeRecord removes the record from the server (unless orphaned) and then
// clears the finalizer. The finalizer is only removed once the server-side
// delete has confirmed, so a failed delete requeues with the resource still
// intact rather than leaking the record. A record that is already gone is
// treated as success.
func (r *RecordReconciler) finalizeRecord(ctx context.Context, record *dnsv1alpha1.Record) error {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(record, recordFinalizer) {
		return nil
	}

	if record.Spec.DeletionPolicy == dnsv1alpha1.DeletionPolicyOrphan {
		log.Info("Orphaning Record: leaving the server-side record in place", "name", record.Spec.Name, "type", record.Spec.Type)
	} else {
		api, err := r.serverClientFor(ctx, record.Spec.ServerRef)
		switch {
		case err == nil:
			if err := api.DeleteRecord(ctx, deleteOptionsFromSpec(record.Spec)); err != nil {
				if !errors.Is(err, technitium.ErrRecordNotFound) {
					return err
				}
			}
			log.Info("Deleted Record from the Technitium server", "name", record.Spec.Name, "type", record.Spec.Type)
		case apierrors.IsNotFound(err):
			// The TechnitiumCluster itself is gone, so whatever records it
			// served are gone with it: there is nothing left to delete, and
			// waiting for it to come back would orphan this finalizer forever.
			log.Info("TechnitiumCluster for Record is gone; skipping server-side delete",
				"name", record.Spec.Name, "server", record.Spec.ServerRef.Name)
		default:
			// Not ready, not bootstrapped, or some other resolve failure:
			// requeue and retry rather than dropping the finalizer and
			// potentially orphaning a record that still exists on the server.
			return err
		}
	}

	controllerutil.RemoveFinalizer(record, recordFinalizer)
	return r.Update(ctx, record)
}

// reconcileRecord brings the server-side record in line with the spec. It is
// idempotent: an absent record is created, a present record with matching
// rdata and TTL is a success, rdata drift is corrected by recreating the
// record, and TTL-only drift is corrected in place.
func (r *RecordReconciler) reconcileRecord(ctx context.Context, record *dnsv1alpha1.Record) error {
	log := logf.FromContext(ctx)
	spec := record.Spec

	api, err := r.serverClientFor(ctx, spec.ServerRef)
	if err != nil {
		return err
	}

	records, err := api.GetRecords(ctx, spec.Zone, spec.Name)
	if err != nil {
		return err
	}

	current := findRecordByType(records, spec.Type)
	if current == nil {
		if err := api.AddRecord(ctx, addOptionsFromSpec(spec)); err != nil {
			// A concurrent create (another replica, a manual action) races us
			// to the same record; treat that as the success it effectively is.
			if !errors.Is(err, technitium.ErrRecordAlreadyExists) {
				return err
			}
		}
		log.Info("Created Record", "name", spec.Name, "type", spec.Type)
		return nil
	}

	if !recordDataMatches(spec, current) {
		if err := api.DeleteRecord(ctx, deleteOptionsFromCurrent(spec, current)); err != nil && !errors.Is(err, technitium.ErrRecordNotFound) {
			return err
		}
		if err := api.AddRecord(ctx, addOptionsFromSpec(spec)); err != nil && !errors.Is(err, technitium.ErrRecordAlreadyExists) {
			return err
		}
		log.Info("Corrected Record data drift by recreating the record", "name", spec.Name, "type", spec.Type)
		return nil
	}

	if desiredTTL := recordTTLFromSpec(spec); current.TTL != desiredTTL {
		update := technitium.UpdateRecordOptions{
			Zone: spec.Zone, Domain: spec.Name, Type: string(spec.Type), TTL: desiredTTL,
		}
		setRecordValue(&update.RecordValue, spec)
		if err := api.UpdateRecord(ctx, update); err != nil {
			return err
		}
		log.Info("Corrected Record TTL drift", "name", spec.Name, "type", spec.Type, "ttl", desiredTTL)
	}

	return nil
}

// findRecordByType returns the first record of recordType in records, or nil
// when none match. GetRecords is scoped to one domain name already; matching
// by type on top of that is enough to identify "the" record a Record resource
// manages, since a Record owns exactly one (name, type) pair.
func findRecordByType(records []technitium.Record, recordType dnsv1alpha1.RecordType) *technitium.Record {
	for i := range records {
		if records[i].Type == string(recordType) {
			return &records[i]
		}
	}
	return nil
}

// recordDataMatches reports whether current's rdata (and, for MX, priority)
// already matches the spec.
func recordDataMatches(spec dnsv1alpha1.RecordSpec, current *technitium.Record) bool {
	switch spec.Type {
	case dnsv1alpha1.RecordTypeA, dnsv1alpha1.RecordTypeAAAA:
		return current.RData.IPAddress == spec.Data
	case dnsv1alpha1.RecordTypeCNAME:
		return current.RData.CName == spec.Data
	case dnsv1alpha1.RecordTypeTXT:
		return current.RData.Text == spec.Data
	case dnsv1alpha1.RecordTypeNS:
		return current.RData.NameServer == spec.Data
	case dnsv1alpha1.RecordTypePTR:
		return current.RData.PtrName == spec.Data
	case dnsv1alpha1.RecordTypeMX:
		if current.RData.Exchange != spec.Data {
			return false
		}
		if spec.Priority == nil {
			return true
		}
		return current.RData.Preference == *spec.Priority
	default:
		return false
	}
}

// recordTTLFromSpec returns the spec's desired TTL, falling back to
// defaultRecordTTL for a Record built without going through the API server's
// CRD defaulting (see defaultRecordTTL).
func recordTTLFromSpec(spec dnsv1alpha1.RecordSpec) int32 {
	if spec.TTL != nil {
		return *spec.TTL
	}
	return defaultRecordTTL
}

// setRecordValue copies spec.Data (and, for MX, spec.Priority) onto the
// rdata field matching spec.Type. v is a pointer to the technitium.RecordValue
// embedded in AddRecordOptions/UpdateRecordOptions/DeleteRecordOptions, so one
// switch builds the right options for all three.
func setRecordValue(v *technitium.RecordValue, spec dnsv1alpha1.RecordSpec) {
	switch spec.Type {
	case dnsv1alpha1.RecordTypeA, dnsv1alpha1.RecordTypeAAAA:
		v.IPAddress = spec.Data
	case dnsv1alpha1.RecordTypeCNAME:
		v.CName = spec.Data
	case dnsv1alpha1.RecordTypeTXT:
		v.Text = spec.Data
	case dnsv1alpha1.RecordTypeNS:
		v.NameServer = spec.Data
	case dnsv1alpha1.RecordTypePTR:
		v.PtrName = spec.Data
	case dnsv1alpha1.RecordTypeMX:
		v.Exchange = spec.Data
		v.Preference = spec.Priority
	}
}

// addOptionsFromSpec maps a Record spec onto the add-record parameters.
func addOptionsFromSpec(spec dnsv1alpha1.RecordSpec) technitium.AddRecordOptions {
	opts := technitium.AddRecordOptions{
		Zone: spec.Zone, Domain: spec.Name, Type: string(spec.Type), TTL: spec.TTL,
	}
	setRecordValue(&opts.RecordValue, spec)
	return opts
}

// deleteOptionsFromSpec maps a Record spec onto the delete-record parameters,
// identifying the record to remove by the rdata the spec itself carries. Used
// during finalization, where the spec is the only source of truth available.
func deleteOptionsFromSpec(spec dnsv1alpha1.RecordSpec) technitium.DeleteRecordOptions {
	opts := technitium.DeleteRecordOptions{Zone: spec.Zone, Domain: spec.Name, Type: string(spec.Type)}
	setRecordValue(&opts.RecordValue, spec)
	return opts
}

// deleteOptionsFromCurrent maps the server's current record onto the
// delete-record parameters, identifying the record to remove by the rdata
// actually present on the server. Used to correct data drift, where the value
// to delete is the stale one on the server, not the desired one in the spec.
func deleteOptionsFromCurrent(spec dnsv1alpha1.RecordSpec, current *technitium.Record) technitium.DeleteRecordOptions {
	opts := technitium.DeleteRecordOptions{Zone: spec.Zone, Domain: spec.Name, Type: string(spec.Type)}
	opts.RecordValue = technitium.RecordValue{
		IPAddress:  current.RData.IPAddress,
		CName:      current.RData.CName,
		Text:       current.RData.Text,
		NameServer: current.RData.NameServer,
		PtrName:    current.RData.PtrName,
		Exchange:   current.RData.Exchange,
	}
	if current.RData.Preference != 0 {
		pref := current.RData.Preference
		opts.RecordValue.Preference = &pref
	}
	return opts
}

// markReady re-fetches the Record and records a successful reconcile: Ready
// True, Degraded and Progressing cleared. Re-fetching avoids writing status
// onto a stale object that another writer has since changed.
func (r *RecordReconciler) markReady(ctx context.Context, key client.ObjectKey) error {
	var record dnsv1alpha1.Record
	if err := r.Get(ctx, key, &record); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Only write status when something actually changed. A blind Update on
	// every requeue would bump resourceVersion and fire a watch event each
	// drift tick even when the record is already in the desired state.
	changed := setRecordCondition(&record, conditionReady, metav1.ConditionTrue,
		"RecordReady", "Record reconciled on the Technitium server")
	changed = setRecordCondition(&record, conditionProgressing, metav1.ConditionFalse,
		"RecordReady", "Record reconciled on the Technitium server") || changed
	changed = setRecordCondition(&record, conditionDegraded, metav1.ConditionFalse,
		"RecordReady", "Record reconciled on the Technitium server") || changed

	if record.Status.ObservedGeneration != record.Generation {
		record.Status.ObservedGeneration = record.Generation
		changed = true
	}
	if !record.Status.RecordCreated {
		record.Status.RecordCreated = true
		changed = true
	}
	if !changed {
		return nil
	}

	return r.Status().Update(ctx, &record)
}

// markDegraded re-fetches the Record and records a failed reconcile so the
// failure is visible on the resource and not only in the logs. Ready flips
// False and Degraded True, both carrying the cause.
func (r *RecordReconciler) markDegraded(ctx context.Context, key client.ObjectKey, cause error) error {
	var record dnsv1alpha1.Record
	if err := r.Get(ctx, key, &record); err != nil {
		return client.IgnoreNotFound(err)
	}

	setRecordCondition(&record, conditionReady, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setRecordCondition(&record, conditionProgressing, metav1.ConditionFalse, "ReconcileFailed", cause.Error())
	setRecordCondition(&record, conditionDegraded, metav1.ConditionTrue, "ReconcileFailed", cause.Error())

	return r.Status().Update(ctx, &record)
}

// setRecordCondition upserts a status condition stamped with the record's
// current generation and reports whether it changed anything.
func setRecordCondition(record *dnsv1alpha1.Record, condType string, status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&record.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: record.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *RecordReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dnsv1alpha1.Record{}).
		Named("record").
		Complete(r)
}

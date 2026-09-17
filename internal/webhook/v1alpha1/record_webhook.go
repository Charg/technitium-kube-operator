/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package v1alpha1

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

var recordlog = logf.Log.WithName("record-resource")

// recordGroupKind identifies the Record kind in admission error responses.
var recordGroupKind = schema.GroupKind{Group: dnsv1alpha1.GroupVersion.Group, Kind: "Record"}

// SetupRecordWebhookWithManager registers the webhook for Record in the manager.
func SetupRecordWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dnsv1alpha1.Record{}).
		WithValidator(&RecordCustomValidator{}).
		WithDefaulter(&RecordCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-dns-packet-fail-v1alpha1-record,mutating=true,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=records,verbs=create;update,versions=v1alpha1,name=mrecord-v1alpha1.kb.io,admissionReviewVersions=v1

// RecordCustomDefaulter fills in defaults the CRD markers cannot, and
// backstops the marker defaults for clients that submit through the webhook
// path.
type RecordCustomDefaulter struct{}

// Default sets the deletion policy to Delete when the caller leaves it unset.
func (d *RecordCustomDefaulter) Default(_ context.Context, obj *dnsv1alpha1.Record) error {
	if obj.Spec.DeletionPolicy == "" {
		obj.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyDelete
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-dns-packet-fail-v1alpha1-record,mutating=false,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=records,verbs=create;update,versions=v1alpha1,name=vrecord-v1alpha1.kb.io,admissionReviewVersions=v1

// RecordCustomValidator enforces invariants the CRD markers cannot express:
// the MX-only priority requirement and the immutability of name.
type RecordCustomValidator struct{}

// ValidateCreate checks type-specific required fields on a new Record.
func (v *RecordCustomValidator) ValidateCreate(_ context.Context, obj *dnsv1alpha1.Record) (admission.Warnings, error) {
	recordlog.Info("Validating Record on create", "name", obj.GetName())
	return nil, validateRecordSpec(obj, nil)
}

// ValidateUpdate rejects a changed name and re-checks type-specific fields.
func (v *RecordCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *dnsv1alpha1.Record) (admission.Warnings, error) {
	recordlog.Info("Validating Record on update", "name", newObj.GetName())
	return nil, validateRecordSpec(newObj, oldObj)
}

// ValidateDelete has nothing to enforce: finalizer-driven cleanup handles teardown.
func (v *RecordCustomValidator) ValidateDelete(_ context.Context, _ *dnsv1alpha1.Record) (admission.Warnings, error) {
	return nil, nil
}

// validateRecordSpec collects every violation so the caller sees them at once.
// oldRecord is nil on create and the prior object on update.
func validateRecordSpec(record, oldRecord *dnsv1alpha1.Record) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if oldRecord != nil && record.Spec.Name != oldRecord.Spec.Name {
		errs = append(errs, field.Invalid(specPath.Child("name"), record.Spec.Name,
			"name is immutable: delete and recreate the resource to rename a record"))
	}

	if record.Spec.ServerRef.Name == "" {
		errs = append(errs, field.Required(specPath.Child("serverRef").Child("name"),
			"serverRef.name is required: a Record must name the TechnitiumCluster it is created on"))
	}
	// A TechnitiumCluster is cluster-scoped, so serverRef.namespace is
	// meaningless. Reject it rather than silently ignore it, which would
	// mislead anyone expecting cross-namespace targeting.
	if record.Spec.ServerRef.Namespace != "" {
		errs = append(errs, field.Invalid(specPath.Child("serverRef").Child("namespace"), record.Spec.ServerRef.Namespace,
			"serverRef.namespace must not be set: a TechnitiumCluster is cluster-scoped and resolved by name"))
	}

	if record.Spec.Type == dnsv1alpha1.RecordTypeMX && (record.Spec.Priority == nil) {
		errs = append(errs, field.Required(specPath.Child("priority"),
			"priority is required when type is MX"))
	}

	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(recordGroupKind, record.Name, errs)
}

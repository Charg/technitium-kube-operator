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

var dnssecColog = logf.Log.WithName("dnssec-resource")

// dnssecGroupKind identifies the DNSSEC kind in admission error responses.
var dnssecGroupKind = schema.GroupKind{Group: dnsv1alpha1.GroupVersion.Group, Kind: "DNSSEC"}

// SetupDNSSECWebhookWithManager registers the webhook for DNSSEC in the manager.
func SetupDNSSECWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dnsv1alpha1.DNSSEC{}).
		WithValidator(&DNSSECCustomValidator{}).
		WithDefaulter(&DNSSECCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-dns-packet-fail-v1alpha1-dnssec,mutating=true,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=dnssecs,verbs=create;update,versions=v1alpha1,name=mdnssec-v1alpha1.kb.io,admissionReviewVersions=v1

// DNSSECCustomDefaulter fills in defaults the CRD markers cannot, and
// backstops the marker defaults for clients that submit through the webhook
// path.
type DNSSECCustomDefaulter struct{}

// Default sets the deletion policy to Delete and the NSEC type to NSEC when
// the caller leaves them unset.
func (d *DNSSECCustomDefaulter) Default(_ context.Context, obj *dnsv1alpha1.DNSSEC) error {
	dnssecColog.Info("Defaulting for DNSSEC", "name", obj.GetName())

	if obj.Spec.DeletionPolicy == "" {
		obj.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyDelete
	}
	if obj.Spec.NSECType == "" {
		obj.Spec.NSECType = dnsv1alpha1.NSECTypeNSEC
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-dns-packet-fail-v1alpha1-dnssec,mutating=false,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=dnssecs,verbs=create;update,versions=v1alpha1,name=vdnssec-v1alpha1.kb.io,admissionReviewVersions=v1

// DNSSECCustomValidator enforces invariants the CRD markers cannot express:
// serverRef's shape, since a TechnitiumCluster is cluster-scoped, and that the
// algorithm-specific key parameters match the chosen algorithm.
type DNSSECCustomValidator struct{}

// ValidateCreate checks a new DNSSEC.
func (v *DNSSECCustomValidator) ValidateCreate(_ context.Context, obj *dnsv1alpha1.DNSSEC) (admission.Warnings, error) {
	dnssecColog.Info("Validating DNSSEC on create", "name", obj.GetName())
	return nil, validateDNSSECSpec(obj)
}

// ValidateUpdate re-checks an updated DNSSEC.
func (v *DNSSECCustomValidator) ValidateUpdate(_ context.Context, _, newObj *dnsv1alpha1.DNSSEC) (admission.Warnings, error) {
	dnssecColog.Info("Validating DNSSEC on update", "name", newObj.GetName())
	return nil, validateDNSSECSpec(newObj)
}

// ValidateDelete has nothing to enforce: finalizer-driven teardown handles
// deletion.
func (v *DNSSECCustomValidator) ValidateDelete(_ context.Context, _ *dnsv1alpha1.DNSSEC) (admission.Warnings, error) {
	return nil, nil
}

// validateDNSSECSpec collects every violation so the caller sees them at once.
func validateDNSSECSpec(d *dnsv1alpha1.DNSSEC) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if d.Spec.ServerRef.Name == "" {
		errs = append(errs, field.Required(specPath.Child("serverRef").Child("name"),
			"serverRef.name is required: a DNSSEC resource must name the TechnitiumCluster it signs a zone on"))
	}
	// A TechnitiumCluster is cluster-scoped, so serverRef.namespace is
	// meaningless. Reject it rather than silently ignore it, which would
	// mislead anyone expecting cross-namespace targeting.
	if d.Spec.ServerRef.Namespace != "" {
		errs = append(errs, field.Invalid(specPath.Child("serverRef").Child("namespace"), d.Spec.ServerRef.Namespace,
			"serverRef.namespace must not be set: a TechnitiumCluster is cluster-scoped and resolved by name"))
	}
	if d.Spec.Zone == "" {
		errs = append(errs, field.Required(specPath.Child("zone"), "zone is required"))
	}

	// The ECDSA- and RSA-only fields are mutually exclusive: setting an
	// algorithm's parameters while a different algorithm is chosen is either a
	// leftover from switching algorithms or a misunderstanding of which
	// parameters apply, and silently ignoring the mismatched fields would
	// hide that from the caller.
	switch d.Spec.Algorithm {
	case dnsv1alpha1.DNSSECAlgorithmRSA:
		if d.Spec.Curve != nil {
			errs = append(errs, field.Invalid(specPath.Child("curve"), *d.Spec.Curve,
				"curve must not be set when algorithm is RSA"))
		}
	case dnsv1alpha1.DNSSECAlgorithmECDSA:
		if d.Spec.HashAlgorithm != nil {
			errs = append(errs, field.Invalid(specPath.Child("hashAlgorithm"), *d.Spec.HashAlgorithm,
				"hashAlgorithm must not be set when algorithm is ECDSA"))
		}
		if d.Spec.KSKKeySize != nil {
			errs = append(errs, field.Invalid(specPath.Child("kskKeySize"), *d.Spec.KSKKeySize,
				"kskKeySize must not be set when algorithm is ECDSA"))
		}
		if d.Spec.ZSKKeySize != nil {
			errs = append(errs, field.Invalid(specPath.Child("zskKeySize"), *d.Spec.ZSKKeySize,
				"zskKeySize must not be set when algorithm is ECDSA"))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(dnssecGroupKind, d.Name, errs)
}

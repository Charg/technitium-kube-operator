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

var dnsapplog = logf.Log.WithName("dnsapp-resource")

// dnsAppGroupKind identifies the DNSApp kind in admission error responses.
var dnsAppGroupKind = schema.GroupKind{Group: dnsv1alpha1.GroupVersion.Group, Kind: "DNSApp"}

// SetupDNSAppWebhookWithManager registers the webhook for DNSApp in the manager.
func SetupDNSAppWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dnsv1alpha1.DNSApp{}).
		WithValidator(&DNSAppCustomValidator{}).
		WithDefaulter(&DNSAppCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-dns-packet-fail-v1alpha1-dnsapp,mutating=true,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=dnsapps,verbs=create;update,versions=v1alpha1,name=mdnsapp-v1alpha1.kb.io,admissionReviewVersions=v1

// DNSAppCustomDefaulter fills in defaults the CRD markers cannot, and
// backstops the marker defaults for clients that submit through the webhook
// path.
type DNSAppCustomDefaulter struct{}

// Default sets the deletion policy to Delete when the caller leaves it unset.
func (d *DNSAppCustomDefaulter) Default(_ context.Context, obj *dnsv1alpha1.DNSApp) error {
	dnsapplog.Info("Defaulting for DNSApp", "name", obj.GetName())

	if obj.Spec.DeletionPolicy == "" {
		obj.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyDelete
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-dns-packet-fail-v1alpha1-dnsapp,mutating=false,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=dnsapps,verbs=create;update,versions=v1alpha1,name=vdnsapp-v1alpha1.kb.io,admissionReviewVersions=v1

// DNSAppCustomValidator enforces invariants the CRD markers cannot express:
// serverRef's shape, since a TechnitiumCluster is cluster-scoped, plus the
// appName and url required fields.
type DNSAppCustomValidator struct{}

// ValidateCreate checks the spec on a new DNSApp.
func (v *DNSAppCustomValidator) ValidateCreate(_ context.Context, obj *dnsv1alpha1.DNSApp) (admission.Warnings, error) {
	dnsapplog.Info("Validating DNSApp on create", "name", obj.GetName())
	return nil, validateDNSAppSpec(obj)
}

// ValidateUpdate re-checks the spec on an updated DNSApp.
func (v *DNSAppCustomValidator) ValidateUpdate(_ context.Context, _, newObj *dnsv1alpha1.DNSApp) (admission.Warnings, error) {
	dnsapplog.Info("Validating DNSApp on update", "name", newObj.GetName())
	return nil, validateDNSAppSpec(newObj)
}

// ValidateDelete has nothing to enforce: finalizer-driven teardown handles
// deletion.
func (v *DNSAppCustomValidator) ValidateDelete(_ context.Context, _ *dnsv1alpha1.DNSApp) (admission.Warnings, error) {
	return nil, nil
}

// validateDNSAppSpec collects every violation so the caller sees them at once.
func validateDNSAppSpec(app *dnsv1alpha1.DNSApp) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if app.Spec.ServerRef.Name == "" {
		errs = append(errs, field.Required(specPath.Child("serverRef").Child("name"),
			"serverRef.name is required: a DNSApp must name the TechnitiumCluster it installs onto"))
	}
	// A TechnitiumCluster is cluster-scoped, so serverRef.namespace is
	// meaningless. Reject it rather than silently ignore it, which would
	// mislead anyone expecting cross-namespace targeting.
	if app.Spec.ServerRef.Namespace != "" {
		errs = append(errs, field.Invalid(specPath.Child("serverRef").Child("namespace"), app.Spec.ServerRef.Namespace,
			"serverRef.namespace must not be set: a TechnitiumCluster is cluster-scoped and resolved by name"))
	}
	if app.Spec.AppName == "" {
		errs = append(errs, field.Required(specPath.Child("appName"),
			"appName is required: a DNSApp must name the DNS App it installs"))
	}
	if app.Spec.URL == "" {
		errs = append(errs, field.Required(specPath.Child("url"),
			"url is required: a DNSApp must know where to download the app package from"))
	}

	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(dnsAppGroupKind, app.Name, errs)
}

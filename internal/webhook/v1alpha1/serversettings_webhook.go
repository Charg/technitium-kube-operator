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

var serversettingslog = logf.Log.WithName("serversettings-resource")

// serverSettingsGroupKind identifies the ServerSettings kind in admission
// error responses.
var serverSettingsGroupKind = schema.GroupKind{Group: dnsv1alpha1.GroupVersion.Group, Kind: "ServerSettings"}

// SetupServerSettingsWebhookWithManager registers the webhook for ServerSettings in the manager.
func SetupServerSettingsWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dnsv1alpha1.ServerSettings{}).
		WithValidator(&ServerSettingsCustomValidator{}).
		WithDefaulter(&ServerSettingsCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-dns-packet-fail-v1alpha1-serversettings,mutating=true,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=serversettings,verbs=create;update,versions=v1alpha1,name=mserversettings-v1alpha1.kb.io,admissionReviewVersions=v1

// ServerSettingsCustomDefaulter fills in defaults the CRD markers cannot, and
// backstops the marker defaults for clients that submit through the webhook
// path.
type ServerSettingsCustomDefaulter struct{}

// Default sets the deletion policy to Retain when the caller leaves it unset.
func (d *ServerSettingsCustomDefaulter) Default(_ context.Context, obj *dnsv1alpha1.ServerSettings) error {
	if obj.Spec.DeletionPolicy == "" {
		obj.Spec.DeletionPolicy = dnsv1alpha1.SettingsDeletionPolicyRetain
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-dns-packet-fail-v1alpha1-serversettings,mutating=false,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=serversettings,verbs=create;update,versions=v1alpha1,name=vserversettings-v1alpha1.kb.io,admissionReviewVersions=v1

// ServerSettingsCustomValidator enforces invariants the CRD markers cannot
// express: serverRef's shape, since a TechnitiumCluster is cluster-scoped.
type ServerSettingsCustomValidator struct{}

// ValidateCreate checks serverRef on a new ServerSettings.
func (v *ServerSettingsCustomValidator) ValidateCreate(_ context.Context, obj *dnsv1alpha1.ServerSettings) (admission.Warnings, error) {
	serversettingslog.Info("Validating ServerSettings on create", "name", obj.GetName())
	return nil, validateServerSettingsSpec(obj)
}

// ValidateUpdate re-checks serverRef on an updated ServerSettings.
func (v *ServerSettingsCustomValidator) ValidateUpdate(_ context.Context, _, newObj *dnsv1alpha1.ServerSettings) (admission.Warnings, error) {
	serversettingslog.Info("Validating ServerSettings on update", "name", newObj.GetName())
	return nil, validateServerSettingsSpec(newObj)
}

// ValidateDelete has nothing to enforce: finalizer-driven teardown handles
// deletion, and it never touches the server (see finalizeServerSettings).
func (v *ServerSettingsCustomValidator) ValidateDelete(_ context.Context, _ *dnsv1alpha1.ServerSettings) (admission.Warnings, error) {
	return nil, nil
}

// validateServerSettingsSpec collects every violation so the caller sees them
// at once.
func validateServerSettingsSpec(settings *dnsv1alpha1.ServerSettings) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if settings.Spec.ServerRef.Name == "" {
		errs = append(errs, field.Required(specPath.Child("serverRef").Child("name"),
			"serverRef.name is required: a ServerSettings must name the TechnitiumCluster it configures"))
	}
	// A TechnitiumCluster is cluster-scoped, so serverRef.namespace is
	// meaningless. Reject it rather than silently ignore it, which would
	// mislead anyone expecting cross-namespace targeting.
	if settings.Spec.ServerRef.Namespace != "" {
		errs = append(errs, field.Invalid(specPath.Child("serverRef").Child("namespace"), settings.Spec.ServerRef.Namespace,
			"serverRef.namespace must not be set: a TechnitiumCluster is cluster-scoped and resolved by name"))
	}

	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(serverSettingsGroupKind, settings.Name, errs)
}

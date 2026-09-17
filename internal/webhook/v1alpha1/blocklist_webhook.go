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

var blocklistlog = logf.Log.WithName("blocklist-resource")

// blocklistGroupKind identifies the Blocklist kind in admission error
// responses.
var blocklistGroupKind = schema.GroupKind{Group: dnsv1alpha1.GroupVersion.Group, Kind: "Blocklist"}

// SetupBlocklistWebhookWithManager registers the webhook for Blocklist in the manager.
func SetupBlocklistWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dnsv1alpha1.Blocklist{}).
		WithValidator(&BlocklistCustomValidator{}).
		WithDefaulter(&BlocklistCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-dns-packet-fail-v1alpha1-blocklist,mutating=true,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=blocklists,verbs=create;update,versions=v1alpha1,name=mblocklist-v1alpha1.kb.io,admissionReviewVersions=v1

// BlocklistCustomDefaulter fills in defaults the CRD markers cannot, and
// backstops the marker defaults for clients that submit through the webhook
// path.
type BlocklistCustomDefaulter struct{}

// Default sets the deletion policy to Delete when the caller leaves it unset.
// Unlike ServerSettings, a Blocklist's default is Delete: the URLs and manual
// overrides it manages are this resource's own content, not shared server
// state worth retaining by default.
func (d *BlocklistCustomDefaulter) Default(_ context.Context, obj *dnsv1alpha1.Blocklist) error {
	blocklistlog.Info("Defaulting for Blocklist", "name", obj.GetName())

	if obj.Spec.DeletionPolicy == "" {
		obj.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyDelete
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-dns-packet-fail-v1alpha1-blocklist,mutating=false,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=blocklists,verbs=create;update,versions=v1alpha1,name=vblocklist-v1alpha1.kb.io,admissionReviewVersions=v1

// BlocklistCustomValidator enforces invariants the CRD markers cannot
// express: serverRef's shape, since a TechnitiumCluster is cluster-scoped.
type BlocklistCustomValidator struct{}

// ValidateCreate checks serverRef on a new Blocklist.
func (v *BlocklistCustomValidator) ValidateCreate(_ context.Context, obj *dnsv1alpha1.Blocklist) (admission.Warnings, error) {
	blocklistlog.Info("Validating Blocklist on create", "name", obj.GetName())
	return nil, validateBlocklistSpec(obj)
}

// ValidateUpdate re-checks serverRef on an updated Blocklist.
func (v *BlocklistCustomValidator) ValidateUpdate(_ context.Context, _, newObj *dnsv1alpha1.Blocklist) (admission.Warnings, error) {
	blocklistlog.Info("Validating Blocklist on update", "name", newObj.GetName())
	return nil, validateBlocklistSpec(newObj)
}

// ValidateDelete has nothing to enforce: finalizer-driven teardown handles
// deletion.
func (v *BlocklistCustomValidator) ValidateDelete(_ context.Context, _ *dnsv1alpha1.Blocklist) (admission.Warnings, error) {
	return nil, nil
}

// validateBlocklistSpec collects every violation so the caller sees them at
// once.
func validateBlocklistSpec(bl *dnsv1alpha1.Blocklist) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if bl.Spec.ServerRef.Name == "" {
		errs = append(errs, field.Required(specPath.Child("serverRef").Child("name"),
			"serverRef.name is required: a Blocklist must name the TechnitiumCluster it configures"))
	}
	// A TechnitiumCluster is cluster-scoped, so serverRef.namespace is
	// meaningless. Reject it rather than silently ignore it, which would
	// mislead anyone expecting cross-namespace targeting.
	if bl.Spec.ServerRef.Namespace != "" {
		errs = append(errs, field.Invalid(specPath.Child("serverRef").Child("namespace"), bl.Spec.ServerRef.Namespace,
			"serverRef.namespace must not be set: a TechnitiumCluster is cluster-scoped and resolved by name"))
	}

	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(blocklistGroupKind, bl.Name, errs)
}

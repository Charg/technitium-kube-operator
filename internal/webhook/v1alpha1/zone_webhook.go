/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package v1alpha1

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

var zonelog = logf.Log.WithName("zone-resource")

// zoneGroupKind identifies the Zone kind in admission error responses.
var zoneGroupKind = schema.GroupKind{Group: dnsv1alpha1.GroupVersion.Group, Kind: "Zone"}

// SetupZoneWebhookWithManager registers the webhook for Zone in the manager.
func SetupZoneWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dnsv1alpha1.Zone{}).
		WithValidator(&ZoneCustomValidator{}).
		WithDefaulter(&ZoneCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-dns-packet-fail-v1alpha1-zone,mutating=true,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=zones,verbs=create;update,versions=v1alpha1,name=mzone-v1alpha1.kb.io,admissionReviewVersions=v1

// ZoneCustomDefaulter fills in defaults the CRD markers cannot, and backstops the
// marker defaults for clients that submit through the webhook path.
type ZoneCustomDefaulter struct{}

// Default sets the zone type to Primary when the caller leaves it unset.
func (d *ZoneCustomDefaulter) Default(_ context.Context, obj *dnsv1alpha1.Zone) error {
	if obj.Spec.Type == "" {
		obj.Spec.Type = dnsv1alpha1.ZoneTypePrimary
	}
	if obj.Spec.DeletionPolicy == "" {
		obj.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyDelete
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-dns-packet-fail-v1alpha1-zone,mutating=false,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=zones,verbs=create;update,versions=v1alpha1,name=vzone-v1alpha1.kb.io,admissionReviewVersions=v1

// ZoneCustomValidator enforces invariants the CRD markers cannot express:
// type-specific required fields and the immutability of zoneName.
type ZoneCustomValidator struct{}

// ValidateCreate checks type-specific required fields on a new Zone.
func (v *ZoneCustomValidator) ValidateCreate(_ context.Context, obj *dnsv1alpha1.Zone) (admission.Warnings, error) {
	zonelog.Info("Validating Zone on create", "name", obj.GetName())
	return nil, validateSpec(obj, nil)
}

// ValidateUpdate rejects a changed zoneName and re-checks type-specific fields.
func (v *ZoneCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *dnsv1alpha1.Zone) (admission.Warnings, error) {
	zonelog.Info("Validating Zone on update", "name", newObj.GetName())
	return nil, validateSpec(newObj, oldObj)
}

// ValidateDelete has nothing to enforce: finalizer-driven cleanup handles teardown.
func (v *ZoneCustomValidator) ValidateDelete(_ context.Context, _ *dnsv1alpha1.Zone) (admission.Warnings, error) {
	return nil, nil
}

// validateSpec collects every violation so the caller sees them at once. oldZone
// is nil on create and the prior object on update.
func validateSpec(zone, oldZone *dnsv1alpha1.Zone) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if oldZone != nil && zone.Spec.ZoneName != oldZone.Spec.ZoneName {
		errs = append(errs, field.Invalid(specPath.Child("zoneName"), zone.Spec.ZoneName,
			"zoneName is immutable: delete and recreate the resource to rename a zone"))
	}

	if zone.Spec.ServerRef.Name == "" {
		errs = append(errs, field.Required(specPath.Child("serverRef").Child("name"),
			"serverRef.name is required: a Zone must name the TechnitiumCluster it is created on"))
	}
	// A TechnitiumCluster is cluster-scoped, so serverRef.namespace is meaningless.
	// Reject it rather than silently ignore it, which would mislead anyone expecting
	// cross-namespace targeting.
	if zone.Spec.ServerRef.Namespace != "" {
		errs = append(errs, field.Invalid(specPath.Child("serverRef").Child("namespace"), zone.Spec.ServerRef.Namespace,
			"serverRef.namespace must not be set: a TechnitiumCluster is cluster-scoped and resolved by name"))
	}

	switch zone.Spec.Type {
	case dnsv1alpha1.ZoneTypeForwarder:
		if zone.Spec.Forwarder == nil || *zone.Spec.Forwarder == "" {
			errs = append(errs, field.Required(specPath.Child("forwarder"),
				"forwarder is required when type is Forwarder"))
		}
	case dnsv1alpha1.ZoneTypeSecondary, dnsv1alpha1.ZoneTypeStub:
		if len(zone.Spec.PrimaryNameServerAddresses) == 0 {
			errs = append(errs, field.Required(specPath.Child("primaryNameServerAddresses"),
				fmt.Sprintf("primaryNameServerAddresses is required when type is %s", zone.Spec.Type)))
		}
	}

	// dnssecValidation and forwarderProxy only have meaning for a Forwarder zone:
	// every other type queries authoritatively or has no upstream to validate or
	// proxy against.
	if zone.Spec.Type != dnsv1alpha1.ZoneTypeForwarder {
		if zone.Spec.DNSSECValidation != nil {
			errs = append(errs, field.Invalid(specPath.Child("dnssecValidation"), *zone.Spec.DNSSECValidation,
				"dnssecValidation is only meaningful when type is Forwarder"))
		}
		if zone.Spec.ForwarderProxy != nil {
			errs = append(errs, field.Invalid(specPath.Child("forwarderProxy"), zone.Spec.ForwarderProxy.Type,
				"forwarderProxy is only meaningful when type is Forwarder"))
		}
	}

	if proxy := zone.Spec.ForwarderProxy; proxy != nil {
		proxyPath := specPath.Child("forwarderProxy")
		switch proxy.Type {
		case dnsv1alpha1.ProxyTypeHTTP, dnsv1alpha1.ProxyTypeSOCKS5:
			if proxy.Address == nil || *proxy.Address == "" {
				errs = append(errs, field.Required(proxyPath.Child("address"),
					fmt.Sprintf("address is required when forwarderProxy.type is %s", proxy.Type)))
			}
			if proxy.Port == nil {
				errs = append(errs, field.Required(proxyPath.Child("port"),
					fmt.Sprintf("port is required when forwarderProxy.type is %s", proxy.Type)))
			}
		case dnsv1alpha1.ProxyTypeNoProxy:
			if proxy.Address != nil {
				errs = append(errs, field.Invalid(proxyPath.Child("address"), *proxy.Address,
					"address must not be set when forwarderProxy.type is NoProxy"))
			}
			if proxy.Port != nil {
				errs = append(errs, field.Invalid(proxyPath.Child("port"), *proxy.Port,
					"port must not be set when forwarderProxy.type is NoProxy"))
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(zoneGroupKind, zone.Name, errs)
}

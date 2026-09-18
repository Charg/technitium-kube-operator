/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package v1alpha1

import (
	"context"
	"net"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
)

var dhcpscopelog = logf.Log.WithName("dhcpscope-resource")

// dhcpScopeGroupKind identifies the DHCPScope kind in admission error
// responses.
var dhcpScopeGroupKind = schema.GroupKind{Group: dnsv1alpha1.GroupVersion.Group, Kind: "DHCPScope"}

// SetupDHCPScopeWebhookWithManager registers the webhook for DHCPScope in the manager.
func SetupDHCPScopeWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &dnsv1alpha1.DHCPScope{}).
		WithValidator(&DHCPScopeCustomValidator{}).
		WithDefaulter(&DHCPScopeCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-dns-packet-fail-v1alpha1-dhcpscope,mutating=true,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=dhcpscopes,verbs=create;update,versions=v1alpha1,name=mdhcpscope-v1alpha1.kb.io,admissionReviewVersions=v1

// DHCPScopeCustomDefaulter fills in defaults the CRD markers cannot, and
// backstops the marker defaults for clients that submit through the webhook
// path.
type DHCPScopeCustomDefaulter struct{}

// Default sets the deletion policy to Delete and enabled to true when the
// caller leaves them unset.
func (d *DHCPScopeCustomDefaulter) Default(_ context.Context, obj *dnsv1alpha1.DHCPScope) error {
	dhcpscopelog.Info("Defaulting for DHCPScope", "name", obj.GetName())

	if obj.Spec.DeletionPolicy == "" {
		obj.Spec.DeletionPolicy = dnsv1alpha1.DeletionPolicyDelete
	}
	if obj.Spec.Enabled == nil {
		enabled := true
		obj.Spec.Enabled = &enabled
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-dns-packet-fail-v1alpha1-dhcpscope,mutating=false,failurePolicy=fail,sideEffects=None,groups=dns.packet.fail,resources=dhcpscopes,verbs=create;update,versions=v1alpha1,name=vdhcpscope-v1alpha1.kb.io,admissionReviewVersions=v1

// DHCPScopeCustomValidator enforces invariants the CRD markers cannot
// express: serverRef's shape, and that the address range, subnet mask,
// router address, and reservations form a coherent set.
type DHCPScopeCustomValidator struct{}

// ValidateCreate checks a new DHCPScope.
func (v *DHCPScopeCustomValidator) ValidateCreate(_ context.Context, obj *dnsv1alpha1.DHCPScope) (admission.Warnings, error) {
	dhcpscopelog.Info("Validating DHCPScope on create", "name", obj.GetName())
	return nil, validateDHCPScopeSpec(obj)
}

// ValidateUpdate re-checks an updated DHCPScope.
func (v *DHCPScopeCustomValidator) ValidateUpdate(_ context.Context, _, newObj *dnsv1alpha1.DHCPScope) (admission.Warnings, error) {
	dhcpscopelog.Info("Validating DHCPScope on update", "name", newObj.GetName())
	return nil, validateDHCPScopeSpec(newObj)
}

// ValidateDelete has nothing to enforce: finalizer-driven teardown handles
// deletion.
func (v *DHCPScopeCustomValidator) ValidateDelete(_ context.Context, _ *dnsv1alpha1.DHCPScope) (admission.Warnings, error) {
	return nil, nil
}

// validateDHCPScopeSpec collects every violation so the caller sees them at
// once.
func validateDHCPScopeSpec(scope *dnsv1alpha1.DHCPScope) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if scope.Spec.ServerRef.Name == "" {
		errs = append(errs, field.Required(specPath.Child("serverRef").Child("name"),
			"serverRef.name is required: a DHCPScope must name the TechnitiumCluster it configures"))
	}
	// A TechnitiumCluster is cluster-scoped, so serverRef.namespace is
	// meaningless. Reject it rather than silently ignore it, which would
	// mislead anyone expecting cross-namespace targeting.
	if scope.Spec.ServerRef.Namespace != "" {
		errs = append(errs, field.Invalid(specPath.Child("serverRef").Child("namespace"), scope.Spec.ServerRef.Namespace,
			"serverRef.namespace must not be set: a TechnitiumCluster is cluster-scoped and resolved by name"))
	}
	if scope.Spec.ScopeName == "" {
		errs = append(errs, field.Required(specPath.Child("scopeName"), "scopeName is required"))
	}

	startIP, startErrs := validateScopeIP(specPath.Child("startingAddress"), scope.Spec.StartingAddress, "startingAddress")
	errs = append(errs, startErrs...)
	endIP, endErrs := validateScopeIP(specPath.Child("endingAddress"), scope.Spec.EndingAddress, "endingAddress")
	errs = append(errs, endErrs...)

	if startIP != nil && endIP != nil {
		// Compared as 4-byte values so a range like "10.0.0.200" to
		// "10.0.0.100" (accidentally swapped bounds) is caught here rather
		// than reaching the server, which has no reason to reject it on its
		// own.
		if bytesCompare(startIP, endIP) > 0 {
			errs = append(errs, field.Invalid(specPath.Child("startingAddress"), scope.Spec.StartingAddress,
				"startingAddress must not be after endingAddress"))
		}
	}

	var mask net.IPMask
	if scope.Spec.SubnetMask == "" {
		errs = append(errs, field.Required(specPath.Child("subnetMask"), "subnetMask is required"))
	} else {
		maskIP := net.ParseIP(scope.Spec.SubnetMask).To4()
		if maskIP == nil {
			errs = append(errs, field.Invalid(specPath.Child("subnetMask"), scope.Spec.SubnetMask,
				"subnetMask must be a valid IPv4 subnet mask"))
		} else {
			mask = net.IPMask(maskIP)
			if _, bits := mask.Size(); bits == 0 {
				errs = append(errs, field.Invalid(specPath.Child("subnetMask"), scope.Spec.SubnetMask,
					"subnetMask must be a contiguous IPv4 subnet mask"))
				mask = nil
			}
		}
	}

	if scope.Spec.RouterAddress != nil {
		routerErrs := validateAddressInSubnet(specPath.Child("routerAddress"), *scope.Spec.RouterAddress, "routerAddress", startIP, mask)
		errs = append(errs, routerErrs...)
	}

	for i, reservation := range scope.Spec.Reservations {
		reservationPath := specPath.Child("reservations").Index(i)
		if _, err := net.ParseMAC(reservation.HardwareAddress); err != nil {
			errs = append(errs, field.Invalid(reservationPath.Child("hardwareAddress"), reservation.HardwareAddress,
				"hardwareAddress must be a valid MAC address"))
		}
		ipErrs := validateAddressInSubnet(reservationPath.Child("ipAddress"), reservation.IPAddress, "ipAddress", startIP, mask)
		errs = append(errs, ipErrs...)
	}

	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(dhcpScopeGroupKind, scope.Name, errs)
}

// validateScopeIP parses value as an IPv4 address, appending a field error
// under path when it is empty or not a valid IPv4 address. It returns the
// parsed 4-byte address, or nil when parsing failed.
func validateScopeIP(path *field.Path, value, fieldName string) (net.IP, field.ErrorList) {
	var errs field.ErrorList
	if value == "" {
		errs = append(errs, field.Required(path, fieldName+" is required"))
		return nil, errs
	}
	ip := net.ParseIP(value).To4()
	if ip == nil {
		errs = append(errs, field.Invalid(path, value, fieldName+" must be a valid IPv4 address"))
		return nil, errs
	}
	return ip, errs
}

// validateAddressInSubnet parses value as an IPv4 address and, when networkIP
// and mask are both known, checks it falls within the subnet networkIP/mask
// defines. networkIP or mask being nil means an earlier error already
// prevented computing the subnet, so only the address's own validity is
// checked here.
func validateAddressInSubnet(path *field.Path, value, fieldName string, networkIP net.IP, mask net.IPMask) field.ErrorList {
	var errs field.ErrorList
	if value == "" {
		errs = append(errs, field.Required(path, fieldName+" is required"))
		return errs
	}
	ip := net.ParseIP(value).To4()
	if ip == nil {
		errs = append(errs, field.Invalid(path, value, fieldName+" must be a valid IPv4 address"))
		return errs
	}
	if networkIP == nil || mask == nil {
		return errs
	}
	network := &net.IPNet{IP: networkIP.Mask(mask), Mask: mask}
	if !network.Contains(ip) {
		errs = append(errs, field.Invalid(path, value, fieldName+" must fall within the scope's subnet"))
	}
	return errs
}

// bytesCompare compares two equal-length IP byte slices lexicographically,
// which for 4-byte big-endian IPv4 addresses is the same as numeric order.
func bytesCompare(a, b net.IP) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

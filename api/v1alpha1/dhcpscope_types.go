/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DHCPReservation is a static MAC-to-IP lease reservation within a DHCP scope.
type DHCPReservation struct {
	// hardwareAddress is the client's MAC address, for example
	// "00:11:22:33:44:55". It identifies the reservation: the resource owns
	// exactly the reservation set keyed by this field, and a change to it is
	// treated as removing the old reservation and adding a new one.
	// +required
	// +kubebuilder:validation:MinLength=1
	HardwareAddress string `json:"hardwareAddress"`

	// ipAddress is the address reserved for this hardware address. It must
	// fall within the scope's startingAddress/endingAddress range.
	// +required
	// +kubebuilder:validation:MinLength=1
	IPAddress string `json:"ipAddress"`

	// hostName is the client host name associated with the reservation.
	// +optional
	HostName *string `json:"hostName,omitempty"`

	// comments is a free-form note about the reservation.
	// +optional
	Comments *string `json:"comments,omitempty"`
}

// DHCPScopeSpec defines the desired state of DHCPScope.
type DHCPScopeSpec struct {
	// serverRef names the TechnitiumCluster this DHCP scope is created on. The
	// referenced instance must be Ready. Only name is used: a TechnitiumCluster
	// is cluster-scoped, so the webhook rejects serverRef.namespace if set.
	// +required
	ServerRef SecretReference `json:"serverRef"`

	// scopeName is the DHCP scope's name on the server. It is required and
	// immutable: change the name by deleting and recreating the resource,
	// since renaming amounts to deleting one scope and creating a different
	// one.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="scopeName is immutable"
	ScopeName string `json:"scopeName"`

	// startingAddress is the first IP address in the scope's range.
	// +required
	StartingAddress string `json:"startingAddress"`

	// endingAddress is the last IP address in the scope's range.
	// +required
	EndingAddress string `json:"endingAddress"`

	// subnetMask is the subnet mask for the scope's range, for example
	// "255.255.255.0".
	// +required
	SubnetMask string `json:"subnetMask"`

	// routerAddress is the default gateway address offered to clients.
	// +optional
	RouterAddress *string `json:"routerAddress,omitempty"`

	// dnsServers is the set of DNS server addresses offered to clients.
	// +optional
	DNSServers []string `json:"dnsServers,omitempty"`

	// leaseTimeDays is the day component of the lease duration offered to
	// clients.
	// +optional
	LeaseTimeDays *int32 `json:"leaseTimeDays,omitempty"`

	// leaseTimeHours is the hour component of the lease duration offered to
	// clients.
	// +optional
	LeaseTimeHours *int32 `json:"leaseTimeHours,omitempty"`

	// leaseTimeMinutes is the minute component of the lease duration offered
	// to clients.
	// +optional
	LeaseTimeMinutes *int32 `json:"leaseTimeMinutes,omitempty"`

	// domainName is the DNS domain name offered to clients.
	// +optional
	DomainName *string `json:"domainName,omitempty"`

	// enabled turns the scope on or off. Defaults to true, since a DHCPScope
	// resource exists to serve leases.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// reservations are static MAC-to-IP lease reservations. The resource owns
	// exactly this set: reservations are added and removed on the server to
	// match, keyed by hardwareAddress.
	// +optional
	Reservations []DHCPReservation `json:"reservations,omitempty"`

	// deletionPolicy controls what happens to the server-side scope when this
	// resource is deleted. "Delete" (the default) disables and removes the
	// scope from the Technitium server. "Orphan" leaves the server scope
	// intact and only clears the finalizer, which is useful for safe adoption
	// or migration.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// DHCPScopeStatus defines the observed state of DHCPScope.
type DHCPScopeStatus struct {
	// conditions represent the current state of the DHCPScope resource.
	// Each condition has a unique type and reflects the status of a specific
	// aspect of the resource.
	//
	// Condition types set by the controller:
	// - "Ready": the scope exists on the server and matches the spec
	// - "Progressing": the controller is creating or updating the scope
	// - "Degraded": a reconcile failed, with the cause in reason/message
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the .metadata.generation the controller last
	// reconciled. A value behind .metadata.generation means the observed
	// state is stale.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// scopeCreated reports whether the scope currently exists on the
	// Technitium server.
	// +optional
	ScopeCreated bool `json:"scopeCreated,omitempty"`

	// appliedReservations is the set of hardware addresses whose reservations
	// the controller last applied to the server. It is the source of truth
	// for which reservations to remove when one is dropped from the spec or
	// the resource is deleted.
	// +optional
	AppliedReservations []string `json:"appliedReservations,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Scope",type=string,JSONPath=`.spec.scopeName`
// +kubebuilder:printcolumn:name="Server",type=string,JSONPath=`.spec.serverRef.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DHCPScope is the Schema for the dhcpscopes API.
//
// DHCPScope is namespaced, matching Record and Blocklist: it configures a
// referenced TechnitiumCluster by name and lives in whichever namespace owns
// the configuration. DHCP is a separate subsystem from DNS on Technitium, so
// this CRD's serverRef points at the same TechnitiumCluster but manages a
// disjoint set of server-side state.
type DHCPScope struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of DHCPScope
	// +required
	Spec DHCPScopeSpec `json:"spec"`

	// status defines the observed state of DHCPScope
	// +optional
	Status DHCPScopeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DHCPScopeList contains a list of DHCPScope
type DHCPScopeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []DHCPScope `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &DHCPScope{}, &DHCPScopeList{})
		return nil
	})
}

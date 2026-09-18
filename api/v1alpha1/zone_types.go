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

// ZoneType is the category of zone created on the Technitium server. The
// values map to the `type` parameter of the Technitium /api/zones/create API.
// +kubebuilder:validation:Enum=Primary;Secondary;Stub;Forwarder;Catalog
type ZoneType string

const (
	ZoneTypePrimary   ZoneType = "Primary"
	ZoneTypeSecondary ZoneType = "Secondary"
	ZoneTypeStub      ZoneType = "Stub"
	ZoneTypeForwarder ZoneType = "Forwarder"
	ZoneTypeCatalog   ZoneType = "Catalog"
)

// ForwarderProtocol is the transport used to reach the upstream resolver of a
// Forwarder zone. The values map to the `protocol` parameter of the Technitium
// create-zone API.
// +kubebuilder:validation:Enum=Udp;Tcp;Tls;Https;Quic
type ForwarderProtocol string

const (
	ForwarderProtocolUDP   ForwarderProtocol = "Udp"
	ForwarderProtocolTCP   ForwarderProtocol = "Tcp"
	ForwarderProtocolTLS   ForwarderProtocol = "Tls"
	ForwarderProtocolHTTPS ForwarderProtocol = "Https"
	ForwarderProtocolQUIC  ForwarderProtocol = "Quic"
)

// ProxyType is the kind of proxy a Forwarder zone routes its upstream queries
// through. The values map to the `proxyType` parameter of the Technitium
// create-zone API.
// +kubebuilder:validation:Enum=NoProxy;Http;Socks5
type ProxyType string

const (
	ProxyTypeNoProxy ProxyType = "NoProxy"
	ProxyTypeHTTP    ProxyType = "Http"
	ProxyTypeSOCKS5  ProxyType = "Socks5"
)

// ForwarderProxy routes a Forwarder zone's queries to its upstream resolver
// through a proxy instead of dialing it directly.
type ForwarderProxy struct {
	// type selects the proxy protocol. NoProxy disables proxying even when the
	// other fields are set.
	// +required
	Type ProxyType `json:"type"`

	// address is the proxy server's hostname or IP address. Required for Http
	// and Socks5.
	// +optional
	Address *string `json:"address,omitempty"`

	// port is the proxy server's port. Required for Http and Socks5.
	// +optional
	Port *int32 `json:"port,omitempty"`

	// username authenticates to the proxy server, when it requires it.
	// +optional
	Username *string `json:"username,omitempty"`

	// password authenticates to the proxy server, when it requires it. Technitium
	// does not return this value from /api/zones/options/get, so the controller
	// can only set it at zone creation and cannot detect or correct drift on it.
	// +optional
	Password *string `json:"password,omitempty"`
}

// DeletionPolicy controls the fate of the server-side zone when a Zone resource
// is deleted.
// +kubebuilder:validation:Enum=Delete;Orphan
type DeletionPolicy string

const (
	// DeletionPolicyDelete removes the zone from the Technitium server on delete.
	DeletionPolicyDelete DeletionPolicy = "Delete"
	// DeletionPolicyOrphan leaves the server-side zone in place on delete.
	DeletionPolicyOrphan DeletionPolicy = "Orphan"
)

// ZoneSpec defines the desired state of Zone.
type ZoneSpec struct {
	// zoneName is the fully qualified DNS name of the zone, for example
	// "example.com". It is required and immutable: change the name by deleting
	// and recreating the resource, since renaming a live zone on the server is
	// not a safe in-place operation.
	// +required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="zoneName is immutable"
	ZoneName string `json:"zoneName"`

	// serverRef names the TechnitiumCluster this zone is created on. The
	// referenced instance must be Ready. Only name is used: a TechnitiumCluster
	// is cluster-scoped, so the webhook rejects serverRef.namespace if set.
	// +required
	ServerRef SecretReference `json:"serverRef"`

	// type is the category of zone to create on the Technitium server.
	// +kubebuilder:default=Primary
	// +optional
	Type ZoneType `json:"type,omitempty"`

	// primaryNameServerAddresses lists the IP addresses or hostnames of the
	// primary name server. Used by Secondary and Stub zones to locate the
	// upstream primary; ignored by other zone types.
	// +optional
	PrimaryNameServerAddresses []string `json:"primaryNameServerAddresses,omitempty"`

	// forwarder is the address (IP or hostname) of the upstream resolver used
	// by a Forwarder zone. The special value "this-server" forwards to the
	// local DNS server. Ignored by other zone types.
	// +optional
	Forwarder *string `json:"forwarder,omitempty"`

	// forwarderProtocol is the transport used to reach the forwarder of a
	// Forwarder zone. Defaults to Udp on the server when unset. Ignored by
	// other zone types.
	// +optional
	ForwarderProtocol *ForwarderProtocol `json:"forwarderProtocol,omitempty"`

	// catalog is the name of an existing catalog zone that this zone joins as a
	// member. Applies to Primary, Secondary, Stub, and Forwarder zones.
	// +optional
	Catalog *string `json:"catalog,omitempty"`

	// dnssecValidation enables DNSSEC validation of responses from the upstream
	// resolver of a Forwarder zone. Meaningful only for type Forwarder.
	// +optional
	DNSSECValidation *bool `json:"dnssecValidation,omitempty"`

	// forwarderProxy routes a Forwarder zone's upstream queries through a proxy.
	// Meaningful only for type Forwarder.
	// +optional
	ForwarderProxy *ForwarderProxy `json:"forwarderProxy,omitempty"`

	// deletionPolicy controls what happens to the server-side zone when this
	// resource is deleted. "Delete" (the default) removes the zone from the
	// Technitium server. "Orphan" leaves the server zone intact and only clears
	// the finalizer, which is useful for safe adoption or migration.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// ZoneStatus defines the observed state of Zone.
type ZoneStatus struct {
	// conditions represent the current state of the Zone resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Condition types set by the controller:
	// - "Ready": the zone exists on the server and matches the spec
	// - "Progressing": the controller is creating or updating the zone
	// - "Degraded": a reconcile failed, with the cause in reason/message
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the .metadata.generation the controller last
	// reconciled. A value behind .metadata.generation means the observed state
	// is stale.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// zoneCreated reports whether the zone currently exists on the Technitium
	// server.
	// +optional
	ZoneCreated bool `json:"zoneCreated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.zoneName`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Zone is the Schema for the zones API.
//
// Zone is cluster-scoped: a Technitium server exposes one global zone namespace,
// so a zoneName must be unique across the whole cluster rather than per
// Kubernetes namespace. This avoids two namespaces silently declaring conflicting
// state for the same zone on a single server.
type Zone struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Zone
	// +required
	Spec ZoneSpec `json:"spec"`

	// status defines the observed state of Zone
	// +optional
	Status ZoneStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ZoneList contains a list of Zone
type ZoneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Zone `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Zone{}, &ZoneList{})
		return nil
	})
}

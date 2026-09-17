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

// RecordType is the DNS resource record type. The values map to the `type`
// parameter of the Technitium /api/zones/records/add API. Technitium supports
// a much larger set (SRV, CAA, TLSA, ...); this CRD covers the common
// authoritative-zone record types first and can grow the enum later without a
// breaking change.
// +kubebuilder:validation:Enum=A;AAAA;CNAME;TXT;NS;PTR;MX
type RecordType string

const (
	RecordTypeA     RecordType = "A"
	RecordTypeAAAA  RecordType = "AAAA"
	RecordTypeCNAME RecordType = "CNAME"
	RecordTypeTXT   RecordType = "TXT"
	RecordTypeNS    RecordType = "NS"
	RecordTypePTR   RecordType = "PTR"
	RecordTypeMX    RecordType = "MX"
)

// RecordSpec defines the desired state of Record.
type RecordSpec struct {
	// serverRef names the TechnitiumCluster this record is created on. The
	// referenced instance must be Ready. Only name is used: a TechnitiumCluster
	// is cluster-scoped, so the webhook rejects serverRef.namespace if set.
	// +required
	ServerRef SecretReference `json:"serverRef"`

	// zone is the authoritative zone the record is added to, for example
	// "example.com". The zone must already exist on the server; this CRD does
	// not create it.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Zone string `json:"zone"`

	// name is the record's fully qualified domain name, for example
	// "www.example.com" or the wildcard "*.example.com". It maps to the
	// Technitium `domain` parameter. It is required and immutable: change the
	// name by deleting and recreating the resource, since renaming amounts to
	// deleting one record and creating a different one.
	//
	// No DNS-name pattern is enforced here (unlike Zone.zoneName): record
	// names legitimately include wildcard and underscore-prefixed labels (for
	// example "_dmarc.example.com") that a strict hostname pattern would
	// reject.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="name is immutable"
	Name string `json:"name"`

	// type is the DNS resource record type.
	// +required
	Type RecordType `json:"type"`

	// ttl is the TTL, in seconds, that resolvers may cache the record for.
	// +kubebuilder:default=3600
	// +kubebuilder:validation:Minimum=0
	// +optional
	TTL *int32 `json:"ttl,omitempty"`

	// data is the record's primary rdata value. Its meaning depends on type:
	// A/AAAA -> the IP address, CNAME -> the target domain name,
	// TXT -> the text content, NS -> the name server domain name,
	// PTR -> the target domain name, MX -> the mail exchange domain name.
	// +required
	// +kubebuilder:validation:MinLength=1
	Data string `json:"data"`

	// priority is the MX preference value. Required when type is MX and
	// ignored otherwise; lower values are preferred. Validated by the webhook
	// rather than a CRD marker because the requirement is conditional on type.
	// +optional
	Priority *int32 `json:"priority,omitempty"`

	// deletionPolicy controls what happens to the server-side record when this
	// resource is deleted. "Delete" (the default) removes the record from the
	// Technitium server. "Orphan" leaves the server record intact and only
	// clears the finalizer, which is useful for safe adoption or migration.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// RecordStatus defines the observed state of Record.
type RecordStatus struct {
	// conditions represent the current state of the Record resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Condition types set by the controller:
	// - "Ready": the record exists on the server and matches the spec
	// - "Progressing": the controller is creating or updating the record
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

	// recordCreated reports whether the record currently exists on the
	// Technitium server.
	// +optional
	RecordCreated bool `json:"recordCreated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.zone`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Record is the Schema for the records API.
//
// Record is namespaced, unlike Zone and TechnitiumCluster: a DNS record
// belongs to whichever tenant namespace owns the workload it fronts, and two
// namespaces are free to manage records in the same zone (for example
// different services publishing their own subdomains) without contending for
// a single cluster-scoped name.
type Record struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Record
	// +required
	Spec RecordSpec `json:"spec"`

	// status defines the observed state of Record
	// +optional
	Status RecordStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RecordList contains a list of Record
type RecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Record `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Record{}, &RecordList{})
		return nil
	})
}

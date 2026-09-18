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

// DNSSECAlgorithm is the key algorithm used to sign a zone. The values map to
// the `algorithm` parameter of the Technitium /api/zones/dnssec/sign API.
// +kubebuilder:validation:Enum=RSA;ECDSA
type DNSSECAlgorithm string

const (
	DNSSECAlgorithmRSA   DNSSECAlgorithm = "RSA"
	DNSSECAlgorithmECDSA DNSSECAlgorithm = "ECDSA"
)

// DNSSECHashAlgorithm is the hash function backing an RSA signing key. The
// values map to the `hashAlgorithm` parameter of the sign API, meaningful only
// when spec.algorithm is RSA.
// +kubebuilder:validation:Enum=SHA256;SHA384;SHA512
type DNSSECHashAlgorithm string

const (
	DNSSECHashAlgorithmSHA256 DNSSECHashAlgorithm = "SHA256"
	DNSSECHashAlgorithmSHA384 DNSSECHashAlgorithm = "SHA384"
	DNSSECHashAlgorithmSHA512 DNSSECHashAlgorithm = "SHA512"
)

// DNSSECCurve is the elliptic curve backing an ECDSA signing key. The values
// map to the `curve` parameter of the sign API, meaningful only when
// spec.algorithm is ECDSA.
// +kubebuilder:validation:Enum=P256;P384
type DNSSECCurve string

const (
	DNSSECCurveP256 DNSSECCurve = "P256"
	DNSSECCurveP384 DNSSECCurve = "P384"
)

// NSECType is the proof-of-nonexistence mechanism a signed zone uses. The
// values map to the `nxProof` parameter of the sign API.
// +kubebuilder:validation:Enum=NSEC;NSEC3
type NSECType string

const (
	NSECTypeNSEC  NSECType = "NSEC"
	NSECTypeNSEC3 NSECType = "NSEC3"
)

// DNSSECSpec defines the desired state of DNSSEC.
//
// A DNSSEC resource signs one existing zone on the referenced server with the
// given key parameters. It does not create the zone: the Zone CRD owns that,
// and this resource is expected to reference a zone a Zone resource (or some
// other process) has already created.
type DNSSECSpec struct {
	// serverRef names the TechnitiumCluster the zone to sign lives on. The
	// referenced instance must be Ready. Only name is used: a TechnitiumCluster
	// is cluster-scoped, so the webhook rejects serverRef.namespace if set.
	// +required
	ServerRef SecretReference `json:"serverRef"`

	// zone is the name of the existing authoritative zone to sign, for example
	// "example.com". It is required and immutable: re-pointing a DNSSEC
	// resource at a different zone is not a safe in-place operation, since it
	// would unsign one zone and sign another under a single resource identity.
	// Delete and recreate the resource to target a different zone.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="zone is immutable"
	Zone string `json:"zone"`

	// algorithm is the signing key algorithm.
	// +required
	Algorithm DNSSECAlgorithm `json:"algorithm"`

	// hashAlgorithm is the hash function backing the RSA signing keys. Only
	// meaningful when algorithm is RSA; the webhook rejects it set alongside
	// ECDSA.
	// +optional
	HashAlgorithm *DNSSECHashAlgorithm `json:"hashAlgorithm,omitempty"`

	// kskKeySize is the Key Signing Key size, in bits, for RSA. Only
	// meaningful when algorithm is RSA; the webhook rejects it set alongside
	// ECDSA.
	// +optional
	KSKKeySize *int32 `json:"kskKeySize,omitempty"`

	// zskKeySize is the Zone Signing Key size, in bits, for RSA. Only
	// meaningful when algorithm is RSA; the webhook rejects it set alongside
	// ECDSA.
	// +optional
	ZSKKeySize *int32 `json:"zskKeySize,omitempty"`

	// curve is the elliptic curve backing the ECDSA signing keys. Only
	// meaningful when algorithm is ECDSA; the webhook rejects it set alongside
	// RSA.
	// +optional
	Curve *DNSSECCurve `json:"curve,omitempty"`

	// nsecType is the proof-of-nonexistence mechanism the signed zone uses.
	// +kubebuilder:default=NSEC
	// +optional
	NSECType NSECType `json:"nsecType,omitempty"`

	// nsec3Iterations is the NSEC3 hash iteration count. Only meaningful when
	// nsecType is NSEC3.
	// +optional
	NSEC3Iterations *int32 `json:"nsec3Iterations,omitempty"`

	// nsec3SaltLength is the NSEC3 salt length, in bytes. Only meaningful when
	// nsecType is NSEC3.
	// +optional
	NSEC3SaltLength *int32 `json:"nsec3SaltLength,omitempty"`

	// dnsKeyTtl is the TTL, in seconds, of the zone's DNSKEY records. Left
	// unchanged on the server when unset.
	// +optional
	DNSKeyTTL *int32 `json:"dnsKeyTtl,omitempty"`

	// zskRolloverDays is how often, in days, the server automatically rolls
	// the Zone Signing Key over. Zero disables automatic rollover. Left
	// unchanged on the server when unset.
	// +optional
	ZSKRolloverDays *int32 `json:"zskRolloverDays,omitempty"`

	// deletionPolicy controls what happens to the zone's signed state when
	// this resource is deleted. "Delete" (the default) unsigns the zone on
	// the Technitium server. "Orphan" leaves the zone signed and only clears
	// the finalizer, which is useful for safe adoption or migration.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// DNSSECStatus defines the observed state of DNSSEC.
type DNSSECStatus struct {
	// conditions represent the current state of the DNSSEC resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Condition types set by the controller:
	// - "Ready": the zone is signed and matches the spec's nsecType
	// - "Progressing": the controller is signing the zone
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

	// signed reports whether the zone currently carries any DNSSEC signature
	// (NSEC or NSEC3), as last observed on the Technitium server.
	// +optional
	Signed bool `json:"signed,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Zone",type=string,JSONPath=`.spec.zone`
// +kubebuilder:printcolumn:name="Algorithm",type=string,JSONPath=`.spec.algorithm`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DNSSEC is the Schema for the dnssecs API.
//
// DNSSEC is namespaced, matching Record and Blocklist: it configures a zone on
// a referenced TechnitiumCluster by name and lives in whichever namespace owns
// the configuration, independent of Zone's cluster-scoped zone namespace.
type DNSSEC struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of DNSSEC
	// +required
	Spec DNSSECSpec `json:"spec"`

	// status defines the observed state of DNSSEC
	// +optional
	Status DNSSECStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DNSSECList contains a list of DNSSEC
type DNSSECList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []DNSSEC `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &DNSSEC{}, &DNSSECList{})
		return nil
	})
}

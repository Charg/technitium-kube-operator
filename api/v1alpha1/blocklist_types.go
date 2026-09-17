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

// BlockingType controls how the server answers a blocked query. The values map
// to the `blockingType` parameter of the Technitium /api/settings/set API.
// +kubebuilder:validation:Enum=AnyAddress;NxDomain;CustomAddress
type BlockingType string

const (
	// BlockingTypeAnyAddress answers blocked queries with 0.0.0.0 and ::.
	BlockingTypeAnyAddress BlockingType = "AnyAddress"
	// BlockingTypeNxDomain answers blocked queries with NXDOMAIN.
	BlockingTypeNxDomain BlockingType = "NxDomain"
	// BlockingTypeCustomAddress answers blocked queries with configured custom
	// addresses.
	BlockingTypeCustomAddress BlockingType = "CustomAddress"
)

// BlocklistSpec defines the desired state of Blocklist.
//
// A Blocklist owns the blocking surface of the referenced server's settings:
// the block list URLs, the blocking toggle and type, the auto-update interval,
// and the manual allowed and blocked domain overrides. These fields are
// disjoint from the forwarding, recursion, cache, and logging fields owned by
// ServerSettings, so the two resources never contend for the same setting.
type BlocklistSpec struct {
	// serverRef names the TechnitiumCluster whose blocking configuration this
	// resource manages. The referenced instance must be Ready. Only name is
	// used: a TechnitiumCluster is cluster-scoped, so the webhook rejects
	// serverRef.namespace if set.
	// +required
	ServerRef SecretReference `json:"serverRef"`

	// enabled turns domain blocking on or off for the whole server. Defaults to
	// true, since a Blocklist resource exists to enforce blocking.
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// blockListUrls is the full set of block list URLs the server downloads and
	// applies. The resource owns this set exactly: URLs are added and removed to
	// match, and an empty list clears all URLs. A change to the set triggers an
	// immediate block list refresh rather than waiting for the update interval.
	// +optional
	BlockListURLs []string `json:"blockListUrls,omitempty"`

	// updateIntervalHours is how often, in hours, the server re-downloads the
	// block list URLs.
	// +kubebuilder:default=24
	// +kubebuilder:validation:Minimum=1
	// +optional
	UpdateIntervalHours *int32 `json:"updateIntervalHours,omitempty"`

	// blockingType sets how the server answers a blocked query. Left unchanged
	// on the server when unset.
	// +optional
	BlockingType *BlockingType `json:"blockingType,omitempty"`

	// allowedDomains are manual overrides never blocked regardless of the block
	// lists. The resource owns exactly this set: domains are added and removed
	// on the server to match.
	// +optional
	AllowedDomains []string `json:"allowedDomains,omitempty"`

	// blockedDomains are manual overrides always blocked regardless of the block
	// lists. The resource owns exactly this set: domains are added and removed
	// on the server to match.
	// +optional
	BlockedDomains []string `json:"blockedDomains,omitempty"`

	// deletionPolicy controls what happens to the managed blocking
	// configuration when this resource is deleted. "Delete" (the default)
	// removes the managed block list URLs and the allowed and blocked domain
	// overrides from the server. "Orphan" leaves them in place and only clears
	// the finalizer.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// BlocklistStatus defines the observed state of Blocklist.
type BlocklistStatus struct {
	// conditions represent the current state of the Blocklist resource.
	//
	// Condition types set by the controller:
	// - "Ready": the blocking configuration matches the spec
	// - "Progressing": the controller is applying the configuration
	// - "Degraded": a reconcile failed, with the cause in reason/message
	//
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// observedGeneration is the .metadata.generation the controller last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// appliedBlockListUrls is the block list URL set the controller last
	// applied to the server.
	// +optional
	AppliedBlockListURLs []string `json:"appliedBlockListUrls,omitempty"`

	// appliedAllowedDomains is the allowed domain set the controller last
	// applied. It is the source of truth for which entries to remove when a
	// domain is dropped from the spec or the resource is deleted.
	// +optional
	AppliedAllowedDomains []string `json:"appliedAllowedDomains,omitempty"`

	// appliedBlockedDomains is the blocked domain set the controller last
	// applied, used the same way as appliedAllowedDomains.
	// +optional
	AppliedBlockedDomains []string `json:"appliedBlockedDomains,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Server",type=string,JSONPath=`.spec.serverRef.name`
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Blocklist is the Schema for the blocklists API.
//
// Blocklist is namespaced, matching Record and ServerSettings: it configures a
// referenced TechnitiumCluster by name and lives in whichever namespace owns
// the configuration.
type Blocklist struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Blocklist
	// +required
	Spec BlocklistSpec `json:"spec"`

	// status defines the observed state of Blocklist
	// +optional
	Status BlocklistStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// BlocklistList contains a list of Blocklist
type BlocklistList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Blocklist `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Blocklist{}, &BlocklistList{})
		return nil
	})
}

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

// RecursionPolicy controls which clients the DNS server performs recursive
// resolution for. The values map to the `recursion` parameter of the
// Technitium /api/settings/set API.
// +kubebuilder:validation:Enum=Deny;Allow;AllowOnlyForPrivateNetworks;UseSpecifiedNetworkACL
type RecursionPolicy string

const (
	RecursionPolicyDeny                        RecursionPolicy = "Deny"
	RecursionPolicyAllow                       RecursionPolicy = "Allow"
	RecursionPolicyAllowOnlyForPrivateNetworks RecursionPolicy = "AllowOnlyForPrivateNetworks"
	RecursionPolicyUseSpecifiedNetworkACL      RecursionPolicy = "UseSpecifiedNetworkACL"
)

// SettingsDeletionPolicy controls what happens to the managed settings on the
// referenced server when a ServerSettings resource is deleted. Only Retain is
// offered in this first cut: DNS settings are instance-wide and have no natural
// owner to garbage collect, so the safe default is to leave the applied values
// in place and simply drop the finalizer.
// +kubebuilder:validation:Enum=Retain
type SettingsDeletionPolicy string

const (
	// SettingsDeletionPolicyRetain leaves the applied settings on the server
	// untouched when the resource is deleted.
	SettingsDeletionPolicyRetain SettingsDeletionPolicy = "Retain"
)

// ForwarderSettings configures how the server forwards queries it does not
// answer authoritatively. A nil ForwarderSettings on the spec leaves the
// server's forwarding configuration untouched.
type ForwarderSettings struct {
	// addresses is the list of upstream resolvers to forward to, for example
	// ["1.1.1.1", "8.8.8.8"]. An empty list clears all forwarders, returning
	// the server to performing its own recursive resolution.
	// +optional
	Addresses []string `json:"addresses,omitempty"`

	// protocol is the transport used to reach the forwarders. Left unchanged on
	// the server when unset.
	// +kubebuilder:validation:Enum=Udp;Tcp;Tls;Https;Quic
	// +optional
	Protocol *ForwarderProtocol `json:"protocol,omitempty"`
}

// RecursionSettings configures the server's recursion policy. A nil
// RecursionSettings on the spec leaves recursion configuration untouched.
type RecursionSettings struct {
	// policy sets which clients recursion is allowed for.
	// +required
	Policy RecursionPolicy `json:"policy"`

	// networkACL is the ordered access control list of IP or network addresses
	// used only when policy is UseSpecifiedNetworkACL. Prefix an entry with "!"
	// to deny it. An empty list clears the ACL.
	// +optional
	NetworkACL []string `json:"networkACL,omitempty"`
}

// CacheSettings configures the resolver cache. A nil CacheSettings on the spec
// leaves cache configuration untouched, and any nil field within it leaves that
// individual setting untouched. cacheMaximumEntries is intentionally absent: the
// Technitium settings API exposes it read-only, so it cannot be managed here.
type CacheSettings struct {
	// serveStale enables serving expired records from cache when upstreams are
	// unreachable.
	// +optional
	ServeStale *bool `json:"serveStale,omitempty"`

	// serveStaleTtlSeconds is how long, in seconds, an expired record is kept
	// usable for stale answers before it is dropped from cache.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ServeStaleTTLSeconds *int32 `json:"serveStaleTtlSeconds,omitempty"`

	// maximumRecordTtlSeconds caps the TTL a cached record may have.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaximumRecordTTLSeconds *int32 `json:"maximumRecordTtlSeconds,omitempty"`

	// minimumRecordTtlSeconds floors the TTL a cached record may have.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinimumRecordTTLSeconds *int32 `json:"minimumRecordTtlSeconds,omitempty"`
}

// LoggingSettings configures query and file logging. A nil LoggingSettings on
// the spec leaves logging configuration untouched, and any nil field within it
// leaves that individual setting untouched.
type LoggingSettings struct {
	// enabled turns file logging of error and audit entries on or off.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// logQueries logs every query and its response. This is verbose and
	// off by default on the server.
	// +optional
	LogQueries *bool `json:"logQueries,omitempty"`

	// useLocalTime writes log timestamps in local time instead of UTC.
	// +optional
	UseLocalTime *bool `json:"useLocalTime,omitempty"`

	// maxFileDays is how many days of log files to keep. 0 disables automatic
	// deletion.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxFileDays *int32 `json:"maxFileDays,omitempty"`
}

// ServerSettingsSpec defines the desired state of ServerSettings.
//
// Each top-level settings group is optional and independently managed: a nil
// group leaves that area of the server configuration alone, so a ServerSettings
// resource only ever owns the fields it actually sets. This keeps the resource
// from fighting other controllers over the shared /api/settings surface. The
// blocking fields of the settings API are owned by the Blocklist resource, not
// this one.
type ServerSettingsSpec struct {
	// serverRef names the TechnitiumCluster these settings are applied to. The
	// referenced instance must be Ready. Only name is used: a TechnitiumCluster
	// is cluster-scoped, so the webhook rejects serverRef.namespace if set.
	// +required
	ServerRef SecretReference `json:"serverRef"`

	// forwarders configures upstream query forwarding.
	// +optional
	Forwarders *ForwarderSettings `json:"forwarders,omitempty"`

	// recursion configures the recursion policy.
	// +optional
	Recursion *RecursionSettings `json:"recursion,omitempty"`

	// cache configures the resolver cache.
	// +optional
	Cache *CacheSettings `json:"cache,omitempty"`

	// logging configures query and file logging.
	// +optional
	Logging *LoggingSettings `json:"logging,omitempty"`

	// deletionPolicy controls what happens to the applied settings when this
	// resource is deleted. Defaults to Retain, which leaves them in place.
	// +kubebuilder:default=Retain
	// +optional
	DeletionPolicy SettingsDeletionPolicy `json:"deletionPolicy,omitempty"`
}

// ObservedServerSettings is the snapshot of the managed settings as the server
// last reported them, so a reader can compare desired spec against observed
// server state. Only the fields this resource manages are recorded.
type ObservedServerSettings struct {
	// +optional
	Forwarders []string `json:"forwarders,omitempty"`
	// +optional
	ForwarderProtocol string `json:"forwarderProtocol,omitempty"`
	// +optional
	Recursion string `json:"recursion,omitempty"`
	// +optional
	RecursionNetworkACL []string `json:"recursionNetworkACL,omitempty"`
	// +optional
	ServeStale *bool `json:"serveStale,omitempty"`
	// +optional
	ServeStaleTTLSeconds *int32 `json:"serveStaleTtlSeconds,omitempty"`
	// +optional
	CacheMaximumRecordTTLSeconds *int32 `json:"cacheMaximumRecordTtlSeconds,omitempty"`
	// +optional
	CacheMinimumRecordTTLSeconds *int32 `json:"cacheMinimumRecordTtlSeconds,omitempty"`
	// +optional
	LoggingEnabled *bool `json:"loggingEnabled,omitempty"`
	// +optional
	LogQueries *bool `json:"logQueries,omitempty"`
	// +optional
	UseLocalTime *bool `json:"useLocalTime,omitempty"`
	// +optional
	MaxLogFileDays *int32 `json:"maxLogFileDays,omitempty"`
}

// ServerSettingsStatus defines the observed state of ServerSettings.
type ServerSettingsStatus struct {
	// conditions represent the current state of the ServerSettings resource.
	//
	// Condition types set by the controller:
	// - "Ready": the settings have been applied and match the spec
	// - "Progressing": the controller is applying the settings
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

	// observed reflects the managed settings as the server reported them on the
	// last successful reconcile.
	// +optional
	Observed *ObservedServerSettings `json:"observed,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Server",type=string,JSONPath=`.spec.serverRef.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ServerSettings is the Schema for the serversettings API.
//
// ServerSettings is namespaced, matching Record: it configures a referenced
// TechnitiumCluster by name and lives in whichever namespace owns the
// configuration, rather than contending for a single cluster-scoped name.
type ServerSettings struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ServerSettings
	// +required
	Spec ServerSettingsSpec `json:"spec"`

	// status defines the observed state of ServerSettings
	// +optional
	Status ServerSettingsStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ServerSettingsList contains a list of ServerSettings
type ServerSettingsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ServerSettings `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ServerSettings{}, &ServerSettingsList{})
		return nil
	})
}

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

// SecretReference points at the Kubernetes Secret holding a node's Technitium
// credentials. Credentials never live in the custom resource itself.
type SecretReference struct {
	// name is the Secret name, required. The Secret carries either a "token" key
	// or a "username"/"password" pair, the same shape the operator's connection
	// config uses.
	// +required
	Name string `json:"name"`

	// namespace is the Secret's namespace. Because Cluster is cluster-scoped it
	// has no namespace of its own, so this defaults to the operator's namespace
	// when left empty.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ClusterNodeSpec describes one Technitium server participating in the cluster:
// how to reach it and where its admin credentials live.
type ClusterNodeSpec struct {
	// name is a stable identifier for the node, required. It is used in status
	// and to key the node in the desired-state list; it does not have to match
	// the server's DNS domain name.
	// +required
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// endpoint is the base URL of the node's Technitium API, for example
	// "https://ns1.internal:5380", required.
	// +required
	// +kubebuilder:validation:Pattern=`^https?://.+`
	Endpoint string `json:"endpoint"`

	// ipAddresses are the node's own reachable addresses advertised to cluster
	// peers, required. The primary's addresses let secondaries contact it; each
	// secondary's addresses let the primary reach it.
	// +required
	// +kubebuilder:validation:MinItems=1
	IPAddresses []string `json:"ipAddresses"`

	// credentialsSecretRef references the Secret with this node's admin
	// credentials, required. The primary's Secret must carry a username and
	// password, not only a token: joining a secondary requires the primary's
	// local administrator credentials, which the server will not accept as a
	// token.
	// +required
	CredentialsSecretRef SecretReference `json:"credentialsSecretRef"`
}

// ClusterSpec defines the desired state of a Technitium cluster.
type ClusterSpec struct {
	// clusterDomain is the DNS name of the cluster, for example
	// "cluster.example.com". It is required and immutable: the domain is baked
	// into server-side cluster zones at initialization and cannot be changed in
	// place.
	// +required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterDomain is immutable"
	ClusterDomain string `json:"clusterDomain"`

	// primary is the node initialized as the cluster's primary, required.
	// +required
	Primary ClusterNodeSpec `json:"primary"`

	// secondaries are the nodes joined to the cluster as secondaries. Each is
	// joined once, idempotently, after the primary is initialized.
	// +optional
	// +listType=map
	// +listMapKey=name
	Secondaries []ClusterNodeSpec `json:"secondaries,omitempty"`

	// ignoreCertificateErrors skips TLS verification when a secondary contacts
	// the primary to join. Initializing a cluster switches the primary to a
	// self-signed certificate, so a join against a primary without a trusted CA
	// needs this set to true.
	// +kubebuilder:default=false
	// +optional
	IgnoreCertificateErrors bool `json:"ignoreCertificateErrors,omitempty"`
}

// ClusterNodeStatus is the observed membership of a single node, read from that
// node's own /api/admin/cluster/state.
type ClusterNodeStatus struct {
	// name is the node identifier from the spec.
	// +required
	Name string `json:"name"`

	// role is the observed cluster role, "Primary" or "Secondary", empty until
	// the node is a member.
	// +optional
	Role string `json:"role,omitempty"`

	// member reports whether the node's own state shows it as an initialized
	// cluster member.
	// +optional
	Member bool `json:"member,omitempty"`

	// message describes the last observation, carrying the error when the node
	// could not be reached or reconciled.
	// +optional
	Message string `json:"message,omitempty"`

	// lastSyncedTime is when this node's membership was last confirmed against
	// the server.
	// +optional
	LastSyncedTime *metav1.Time `json:"lastSyncedTime,omitempty"`
}

// ClusterStatus defines the observed state of Cluster.
type ClusterStatus struct {
	// conditions represent the current state of the Cluster resource.
	//
	// Condition types set by the controller:
	// - "Ready": every node in the spec is a cluster member
	// - "Progressing": the controller is initializing or joining nodes
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

	// clusterDomain is the cluster domain observed on the primary node.
	// +optional
	ClusterDomain string `json:"clusterDomain,omitempty"`

	// nodes reports per-node observed membership, primary first.
	// +optional
	// +listType=map
	// +listMapKey=name
	Nodes []ClusterNodeStatus `json:"nodes,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.clusterDomain`
// +kubebuilder:printcolumn:name="Primary",type=string,JSONPath=`.spec.primary.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Cluster is the Schema for the clusters API.
//
// Cluster is cluster-scoped: a Technitium cluster is server-global state shared
// across nodes, not something a Kubernetes namespace owns.
//
// Deletion orphans the server-side cluster. Removing a Cluster resource stops
// reconciliation but leaves the running DNS cluster intact; tearing down a live
// cluster on resource deletion is unsafe and is left to a deliberate manual
// operation. There is therefore no finalizer.
type Cluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Cluster
	// +required
	Spec ClusterSpec `json:"spec"`

	// status defines the observed state of Cluster
	// +optional
	Status ClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ClusterList contains a list of Cluster
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Cluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Cluster{}, &ClusterList{})
		return nil
	})
}

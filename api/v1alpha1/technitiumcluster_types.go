/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SecretReference points at a Secret that holds credentials the operator
// consumes when reconciling a TechnitiumCluster.
type SecretReference struct {
	// name is the Secret's name.
	// +required
	Name string `json:"name"`

	// namespace is the Secret's namespace. Defaults to the operator's own
	// namespace when empty, since credential Secrets are typically colocated
	// with the operator rather than scattered across tenant namespaces.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// TechnitiumClusterPhase summarizes where a TechnitiumCluster is in its
// provisioning lifecycle.
type TechnitiumClusterPhase string

const (
	// TechnitiumClusterPhasePending means the controller has not yet started
	// reconciling the resource.
	TechnitiumClusterPhasePending TechnitiumClusterPhase = "Pending"
	// TechnitiumClusterPhaseProvisioning means the underlying workload
	// (StatefulSet, Service, Secret) is being created or updated.
	TechnitiumClusterPhaseProvisioning TechnitiumClusterPhase = "Provisioning"
	// TechnitiumClusterPhaseBootstrapping means the workload is running but the
	// Technitium instance has not yet reported itself ready over its API.
	TechnitiumClusterPhaseBootstrapping TechnitiumClusterPhase = "Bootstrapping"
	// TechnitiumClusterPhaseClustering means the workload is bootstrapped and
	// spec.replicas is greater than 1, but init/join across the nodes has not
	// yet converged: the primary has not initialized, or a secondary has not
	// yet joined.
	TechnitiumClusterPhaseClustering TechnitiumClusterPhase = "Clustering"
	// TechnitiumClusterPhaseReady means the instance is reachable and serving.
	TechnitiumClusterPhaseReady TechnitiumClusterPhase = "Ready"
)

// Condition types set by the TechnitiumCluster controller.
const (
	// TechnitiumClusterConditionAvailable reports whether the instance is
	// currently reachable and serving.
	TechnitiumClusterConditionAvailable = "Available"
	// TechnitiumClusterConditionProgressing reports whether the controller is
	// actively creating or updating the underlying workload.
	TechnitiumClusterConditionProgressing = "Progressing"
	// TechnitiumClusterConditionDegraded reports that a reconcile failed, with
	// the cause in reason/message.
	TechnitiumClusterConditionDegraded = "Degraded"
)

// PVCRetentionPolicy controls what happens to the StatefulSet's
// volumeClaimTemplate PersistentVolumeClaims when a TechnitiumCluster is
// deleted.
// +kubebuilder:validation:Enum=Retain;Delete
type PVCRetentionPolicy string

const (
	// PVCRetentionPolicyRetain leaves the data PVCs in place on CR deletion.
	// StatefulSet volumeClaimTemplate PVCs are not owner-reference garbage
	// collected along with the StatefulSet itself, so this is the safer
	// default: an operator who deletes the wrong TechnitiumCluster by mistake
	// still has the zone data and DNSSEC keys sitting in an orphaned PVC
	// rather than gone.
	PVCRetentionPolicyRetain PVCRetentionPolicy = "Retain"
	// PVCRetentionPolicyDelete removes the data PVCs as part of an
	// intentional teardown.
	PVCRetentionPolicyDelete PVCRetentionPolicy = "Delete"
)

// TechnitiumClusterStorageSpec configures the persistent volume backing the
// Technitium data directory.
type TechnitiumClusterStorageSpec struct {
	// size is the volumeClaimTemplate request mounted at the Technitium data
	// dir (/etc/dns), where zone data, DNSSEC keys, and configuration live.
	// +required
	Size resource.Quantity `json:"size"`

	// storageClassName selects the StorageClass for the volume claim. Leaving
	// it unset defers to the cluster's default StorageClass.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// retentionPolicy controls what happens to the volumeClaimTemplate PVCs
	// when this TechnitiumCluster is deleted. "Retain" (the default) leaves
	// them in place: they are not owner-reference garbage collected along
	// with the StatefulSet, so leaving them requires no special handling,
	// only restraint. "Delete" removes them as part of the finalizer's
	// teardown, for callers who want no storage left behind.
	// +kubebuilder:default=Retain
	// +optional
	RetentionPolicy PVCRetentionPolicy `json:"retentionPolicy,omitempty"`
}

// TechnitiumClusterServiceSpec configures the Service that fronts the
// Technitium instance.
type TechnitiumClusterServiceSpec struct {
	// type is the Kubernetes Service type.
	// +kubebuilder:default=ClusterIP
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// annotations are copied onto the generated Service, for example to
	// configure a cloud load balancer or an external-dns hostname.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// TechnitiumClusterSpec defines the desired state of TechnitiumCluster.
type TechnitiumClusterSpec struct {
	// image is the container image for the Technitium DNS Server.
	// +kubebuilder:default="technitium/dns-server:15.4.0"
	// +optional
	Image string `json:"image,omitempty"`

	// replicas is the number of independent Technitium instances to
	// provision, each with its own PersistentVolumeClaim. It does not wire
	// them into a Technitium cluster (init/join): every replica runs as its
	// own standalone server until that is implemented.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// storage configures the persistent volume backing the Technitium data
	// directory.
	// +required
	Storage TechnitiumClusterStorageSpec `json:"storage"`

	// resources sets compute resource requirements for the Technitium
	// container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// service configures the Service that fronts the Technitium instance.
	// +optional
	Service TechnitiumClusterServiceSpec `json:"service,omitempty"`

	// adminSecretRef points at a Secret holding the Technitium admin
	// credentials. When unset, the operator generates a "<name>-admin" Secret
	// with a random password.
	// +optional
	AdminSecretRef *SecretReference `json:"adminSecretRef,omitempty"`

	// dnsServerDomain sets the Technitium DNS_SERVER_DOMAIN environment
	// variable, used by the server for its own SOA and self-referential
	// records.
	// +optional
	DNSServerDomain string `json:"dnsServerDomain,omitempty"`

	// clusterDomain names the internal domain used to build each node's
	// cluster node name ("<pod>.<clusterDomain>") and the primary node URL
	// passed to secondaries on join. It does not need to be resolvable DNS
	// and node certificates need no matching SAN for it: the operator joins
	// secondaries by primary IP address with certificate verification
	// disabled, since Technitium's inter-node TLS is not meant to authenticate
	// against real DNS in this deployment model. When unset it defaults to
	// spec.dnsServerDomain, or "<name>.local" if that is also unset. Only
	// meaningful when spec.replicas is greater than 1.
	// +optional
	ClusterDomain string `json:"clusterDomain,omitempty"`

	// deletionPolicy controls whether the controller gracefully tears down
	// the in-Technitium cluster state before the owner-reference garbage
	// collector reaps the workload. "Delete" (the default) removes every
	// Secondary from the Primary's cluster membership and then deletes the
	// Primary's own cluster configuration. "Orphan" skips that teardown and
	// leaves the in-Technitium cluster state as-is, which is useful when the
	// workload is being migrated or adopted rather than decommissioned.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// TechnitiumClusterNodeStatus reports one StatefulSet ordinal's observed
// cluster role. It is populated from the workload's own readiness pre-cluster
// (Phase 2 has no init/join yet, so Role and State are inferred rather than
// read from a real Technitium clusterNodes entry) and later, once the node
// answers /api/admin/cluster/state, corrected from that response.
type TechnitiumClusterNodeStatus struct {
	// name is the StatefulSet pod name this status entry describes, for
	// example "<cluster>-0".
	// +required
	Name string `json:"name"`

	// role is Primary for ordinal 0 and Secondary for every other ordinal.
	// Ordinal 0 is always the node cluster/init runs against, so its role
	// never depends on anything the cluster API reports.
	// +optional
	Role string `json:"role,omitempty"`

	// state is the node's cluster membership state (for example Self or
	// Connected once clustering is live) or a readiness-derived placeholder
	// (for example Ready or NotReady) before the node has ever answered the
	// cluster state endpoint.
	// +optional
	State string `json:"state,omitempty"`

	// lastSynced is when this node last reported its configuration in sync
	// with the cluster, taken from the node's own configLastSynced. Nil until
	// the node has been queried successfully at least once.
	// +optional
	LastSynced *metav1.Time `json:"lastSynced,omitempty"`

	// message is a short human-readable reason for the current state, set
	// when a node's cluster state could not be read (for example, the pod is
	// not yet accepting connections).
	// +optional
	Message string `json:"message,omitempty"`
}

// TechnitiumClusterStatus defines the observed state of TechnitiumCluster.
type TechnitiumClusterStatus struct {
	// conditions represent the current state of the TechnitiumCluster resource.
	// Each condition has a unique type and reflects the status of a specific
	// aspect of the resource.
	//
	// Condition types set by the controller:
	// - "Available": the instance is reachable and serving
	// - "Progressing": the controller is creating or updating the workload
	// - "Degraded": a reconcile failed, with the cause in reason/message
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// phase summarizes where the resource is in its provisioning lifecycle.
	// +optional
	Phase TechnitiumClusterPhase `json:"phase,omitempty"`

	// readyReplicas is the number of Technitium instances currently reporting
	// ready.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// endpoint is the in-cluster API URL the operator and Zone resources use
	// to reach this instance.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// observedGeneration is the .metadata.generation the controller last
	// reconciled. A value behind .metadata.generation means the observed state
	// is stale.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// nodes reports each StatefulSet ordinal's observed cluster role and
	// state, one entry per desired replica.
	// +optional
	Nodes []TechnitiumClusterNodeStatus `json:"nodes,omitempty"`

	// members renders joined/total node counts, for example "1/1". It is a
	// string rather than two integer fields because a printer column can only
	// JSONPath into a stored value, not compute one from Nodes.
	// +optional
	Members string `json:"members,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Members",type=string,JSONPath=`.status.members`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TechnitiumCluster is the Schema for the technitiumclusters API.
//
// TechnitiumCluster is cluster-scoped: it owns a Technitium DNS Server
// workload rather than describing a namespaced tenant resource. One instance
// of this resource provisions spec.replicas Technitium nodes; running several
// TechnitiumCluster resources is how a deployment fronts multiple independent
// groups of Technitium servers.
type TechnitiumCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of TechnitiumCluster
	// +required
	Spec TechnitiumClusterSpec `json:"spec"`

	// status defines the observed state of TechnitiumCluster
	// +optional
	Status TechnitiumClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TechnitiumClusterList contains a list of TechnitiumCluster
type TechnitiumClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []TechnitiumCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &TechnitiumCluster{}, &TechnitiumClusterList{})
		return nil
	})
}

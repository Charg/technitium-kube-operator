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

	// replicas is the number of Technitium instances to run. Only a single
	// instance is supported in this phase; multi-node clustering is a future
	// phase, so anything above 1 is rejected at admission.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self <= 1",message="replicas greater than 1 is not supported yet: multi-node clustering is a future phase"
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
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TechnitiumCluster is the Schema for the technitiumclusters API.
//
// TechnitiumCluster is cluster-scoped: it owns a Technitium DNS Server
// workload rather than describing a namespaced tenant resource. One instance
// of this resource provisions one Technitium node; running several is how a
// deployment fronts multiple independent Technitium servers, not how a single
// Technitium instance is scaled (see spec.replicas).
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

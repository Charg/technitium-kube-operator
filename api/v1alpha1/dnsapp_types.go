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

// DNSAppSpec defines the desired state of DNSApp.
//
// A DNSApp owns the install, version, and configuration of exactly one DNS
// App on the referenced server. It does not model per-app configuration
// schemas: config is passed through to the app verbatim, since Technitium DNS
// Apps each define their own configuration format.
type DNSAppSpec struct {
	// serverRef names the TechnitiumCluster this DNS App is installed on. The
	// referenced instance must be Ready. Only name is used: a TechnitiumCluster
	// is cluster-scoped, so the webhook rejects serverRef.namespace if set.
	// +required
	ServerRef SecretReference `json:"serverRef"`

	// appName is the DNS App name as it appears in Technitium. It is required
	// and immutable: renaming amounts to uninstalling one app and installing a
	// different one, not an in-place update.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="appName is immutable"
	AppName string `json:"appName"`

	// url is the store or download URL of the app package, used both to
	// install the app and to update it to a newer version.
	// +required
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// version is the version to keep the app at. When set and it differs from
	// the version currently installed, the controller downloads and updates
	// the app from url. Left unset, the controller only installs the app and
	// never updates an already-installed version.
	// +optional
	Version string `json:"version,omitempty"`

	// config is an opaque per-app configuration blob (raw JSON or another
	// string format the app expects) passed through verbatim to the app's
	// config endpoint. Not modeled per app, since each DNS App defines its own
	// configuration format.
	// +optional
	Config string `json:"config,omitempty"`

	// deletionPolicy controls what happens to the installed app when this
	// resource is deleted. "Delete" (the default) uninstalls the app from the
	// Technitium server. "Orphan" leaves it installed and only clears the
	// finalizer.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// DNSAppStatus defines the observed state of DNSApp.
type DNSAppStatus struct {
	// conditions represent the current state of the DNSApp resource.
	//
	// Condition types set by the controller:
	// - "Ready": the app is installed on the server and matches the spec
	// - "Progressing": the controller is installing, updating, or configuring the app
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

	// installedVersion is the app version last observed installed on the
	// server.
	// +optional
	InstalledVersion string `json:"installedVersion,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="App",type=string,JSONPath=`.spec.appName`
// +kubebuilder:printcolumn:name="Server",type=string,JSONPath=`.spec.serverRef.name`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DNSApp is the Schema for the dnsapps API.
//
// DNSApp is namespaced, matching Record and Blocklist: a DNS App belongs to
// whichever tenant namespace owns the workload or feature it supports, and
// two namespaces are free to manage different apps (or their own instance of
// the config surface) on the same server without contending for a single
// cluster-scoped name.
type DNSApp struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of DNSApp
	// +required
	Spec DNSAppSpec `json:"spec"`

	// status defines the observed state of DNSApp
	// +optional
	Status DNSAppStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DNSAppList contains a list of DNSApp
type DNSAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []DNSApp `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &DNSApp{}, &DNSAppList{})
		return nil
	})
}

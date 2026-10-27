/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

// Package testimage holds the Technitium image shared by the envtest and e2e
// suites, so a version bump touches one test location.
package testimage

// TechnitiumImage is the default Technitium DNS Server image used by tests.
// It must match the kubebuilder default for spec.image in
// api/v1alpha1/technitiumcluster_types.go.
const TechnitiumImage = "technitium/dns-server:15.6.2"

// PreviousTechnitiumImage is an older release than TechnitiumImage, used as
// the starting point of upgrade tests.
const PreviousTechnitiumImage = "technitium/dns-server:15.6.2"

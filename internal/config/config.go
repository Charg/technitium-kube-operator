/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

// Package config derives Technitium client authentication options from a
// Kubernetes Secret. There is no global server connection to configure: each
// Zone resolves its own Technitium instance through its serverRef, and
// credentials are read per instance from that instance's admin Secret.
package config

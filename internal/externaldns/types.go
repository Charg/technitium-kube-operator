/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

// Package externaldns implements an ExternalDNS webhook provider that
// translates ExternalDNS endpoints into Record custom resources. It never
// talks to Technitium itself: the Record controller does the server-side work.
package externaldns

import "strings"

// MediaType is the content type of every webhook response and the version
// ExternalDNS negotiates on GET /.
const MediaType = "application/external.dns.webhook+json;version=1"

// Endpoint mirrors the ExternalDNS endpoint.Endpoint wire format.
type Endpoint struct {
	DNSName          string             `json:"dnsName,omitempty"`
	Targets          []string           `json:"targets,omitempty"`
	RecordType       string             `json:"recordType,omitempty"`
	SetIdentifier    string             `json:"setIdentifier,omitempty"`
	RecordTTL        int64              `json:"recordTTL,omitempty"`
	Labels           map[string]string  `json:"labels,omitempty"`
	ProviderSpecific []ProviderSpecific `json:"providerSpecific,omitempty"`
}

// ProviderSpecific is a provider-specific endpoint property.
type ProviderSpecific struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// Changes mirrors the ExternalDNS plan.Changes wire format.
type Changes struct {
	Create    []*Endpoint `json:"create,omitempty"`
	UpdateOld []*Endpoint `json:"updateOld,omitempty"`
	UpdateNew []*Endpoint `json:"updateNew,omitempty"`
	Delete    []*Endpoint `json:"delete,omitempty"`
}

// DomainFilter mirrors the ExternalDNS domain filter wire format.
type DomainFilter struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// normalizeName lowercases a DNS name and strips trailing dots.
func normalizeName(s string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), ".")
}

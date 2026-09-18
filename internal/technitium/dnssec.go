/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"errors"
	"strconv"
)

// SignZoneOptions describes a zone to sign with DNSSEC. Fields map to the
// query parameters of /api/zones/dnssec/sign. Every field beyond Zone and
// Algorithm is a pointer so a nil leaves the server to apply its own default,
// mirroring SetDNSSettingsOptions' param-builder style.
//
// WHY the param names below: Technitium's APIDOCS.md documents the sign
// endpoint's parameters loosely and the exact casing has not been exercised
// against a live server as part of this change, so these are the best-known
// names carried over from the DNS server's admin UI form fields. GetDNSSEC
// PropertiesTest and the e2e spec exercise the happy path against a real
// instance; a param name mismatch would surface there as an APIError rather
// than silently doing nothing.
type SignZoneOptions struct {
	// Zone is the zone to sign, required.
	Zone string
	// Algorithm is the signing key algorithm, required: "RSA" or "ECDSA".
	Algorithm string

	// HashAlgorithm is the RSA hash function ("SHA256", "SHA384", "SHA512").
	// Meaningful only when Algorithm is RSA.
	HashAlgorithm *string
	// KSKKeySize is the RSA Key Signing Key size in bits. Meaningful only when
	// Algorithm is RSA.
	KSKKeySize *int32
	// ZSKKeySize is the RSA Zone Signing Key size in bits. Meaningful only
	// when Algorithm is RSA.
	ZSKKeySize *int32

	// Curve is the ECDSA curve ("P256", "P384"). Meaningful only when
	// Algorithm is ECDSA.
	Curve *string

	// NxProof is the proof-of-nonexistence mechanism ("NSEC" or "NSEC3").
	// Nil defers to the server default (NSEC).
	NxProof *string
	// Iterations is the NSEC3 hash iteration count. Meaningful only when
	// NxProof is NSEC3.
	Iterations *int32
	// SaltLength is the NSEC3 salt length in bytes. Meaningful only when
	// NxProof is NSEC3.
	SaltLength *int32

	// DNSKeyTTL is the TTL, in seconds, of the zone's DNSKEY records.
	DNSKeyTTL *int32
	// ZSKRolloverDays is the automatic Zone Signing Key rollover interval, in
	// days. Zero disables automatic rollover.
	ZSKRolloverDays *int32
}

// DNSSECProperties is the subset of /api/zones/dnssec/properties/get the
// operator reconciles. The server returns additional key- and NSEC3-specific
// detail not needed here.
type DNSSECProperties struct {
	Name         string `json:"name"`
	DNSSECStatus string `json:"dnssecStatus"`
	DNSKeyTTL    int32  `json:"dnsKeyTtl"`
}

// DNSSEC status values reported by /api/zones/dnssec/properties/get and
// /api/zones/list's dnssecStatus field.
const (
	DNSSECStatusUnsigned        = "Unsigned"
	DNSSECStatusSignedWithNSEC  = "SignedWithNSEC"
	DNSSECStatusSignedWithNSEC3 = "SignedWithNSEC3"
)

// SignZone signs opts.Zone with DNSSEC using the given key parameters.
func (c *Client) SignZone(ctx context.Context, opts SignZoneOptions) error {
	params, err := zoneParams(opts.Zone)
	if err != nil {
		return err
	}

	if opts.Algorithm == "" {
		return errors.New("technitium: algorithm is required")
	}
	params.Set("algorithm", opts.Algorithm)

	if opts.HashAlgorithm != nil {
		params.Set("hashAlgorithm", *opts.HashAlgorithm)
	}
	if opts.KSKKeySize != nil {
		params.Set("kskKeySize", strconv.Itoa(int(*opts.KSKKeySize)))
	}
	if opts.ZSKKeySize != nil {
		params.Set("zskKeySize", strconv.Itoa(int(*opts.ZSKKeySize)))
	}
	if opts.Curve != nil {
		params.Set("curve", *opts.Curve)
	}
	if opts.NxProof != nil {
		params.Set("nxProof", *opts.NxProof)
	}
	if opts.Iterations != nil {
		params.Set("iterations", strconv.Itoa(int(*opts.Iterations)))
	}
	if opts.SaltLength != nil {
		params.Set("saltLength", strconv.Itoa(int(*opts.SaltLength)))
	}
	if opts.DNSKeyTTL != nil {
		params.Set("dnsKeyTtl", strconv.Itoa(int(*opts.DNSKeyTTL)))
	}
	if opts.ZSKRolloverDays != nil {
		params.Set("zskRolloverDays", strconv.Itoa(int(*opts.ZSKRolloverDays)))
	}

	return c.do(ctx, "/api/zones/dnssec/sign", params, nil)
}

// UnsignZone removes DNSSEC signing from zone.
func (c *Client) UnsignZone(ctx context.Context, zone string) error {
	params, err := zoneParams(zone)
	if err != nil {
		return err
	}
	return c.do(ctx, "/api/zones/dnssec/unsign", params, nil)
}

// GetDNSSECProperties fetches a zone's current DNSSEC signing state.
func (c *Client) GetDNSSECProperties(ctx context.Context, zone string) (*DNSSECProperties, error) {
	params, err := zoneParams(zone)
	if err != nil {
		return nil, err
	}

	var props DNSSECProperties
	if err := c.do(ctx, "/api/zones/dnssec/properties/get", params, &props); err != nil {
		return nil, err
	}
	return &props, nil
}

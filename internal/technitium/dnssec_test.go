/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"net/http"
	"testing"
)

func TestSignZone(t *testing.T) {
	hashAlgorithm := "SHA256"
	kskKeySize := int32(2048)
	zskKeySize := int32(1024)
	nxProof := "NSEC3"
	iterations := int32(1)
	saltLength := int32(8)
	dnsKeyTTL := int32(3600)
	zskRolloverDays := int32(90)

	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/zones/dnssec/sign" {
			t.Errorf("path = %q, want /api/zones/dnssec/sign", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("zone"); got != "example.com" {
			t.Errorf("zone = %q, want example.com", got)
		}
		if got := q.Get("algorithm"); got != "RSA" {
			t.Errorf("algorithm = %q, want RSA", got)
		}
		if got := q.Get("hashAlgorithm"); got != hashAlgorithm {
			t.Errorf("hashAlgorithm = %q, want %q", got, hashAlgorithm)
		}
		if got := q.Get("kskKeySize"); got != "2048" {
			t.Errorf("kskKeySize = %q, want 2048", got)
		}
		if got := q.Get("zskKeySize"); got != "1024" {
			t.Errorf("zskKeySize = %q, want 1024", got)
		}
		if got := q.Get("nxProof"); got != nxProof {
			t.Errorf("nxProof = %q, want %q", got, nxProof)
		}
		if got := q.Get("iterations"); got != "1" {
			t.Errorf("iterations = %q, want 1", got)
		}
		if got := q.Get("saltLength"); got != "8" {
			t.Errorf("saltLength = %q, want 8", got)
		}
		if got := q.Get("dnsKeyTtl"); got != "3600" {
			t.Errorf("dnsKeyTtl = %q, want 3600", got)
		}
		if got := q.Get("zskRolloverDays"); got != "90" {
			t.Errorf("zskRolloverDays = %q, want 90", got)
		}
		_, _ = w.Write([]byte(statusOK))
	})

	err := c.SignZone(context.Background(), SignZoneOptions{
		Zone:            "example.com",
		Algorithm:       "RSA",
		HashAlgorithm:   &hashAlgorithm,
		KSKKeySize:      &kskKeySize,
		ZSKKeySize:      &zskKeySize,
		NxProof:         &nxProof,
		Iterations:      &iterations,
		SaltLength:      &saltLength,
		DNSKeyTTL:       &dnsKeyTTL,
		ZSKRolloverDays: &zskRolloverDays,
	})
	if err != nil {
		t.Fatalf("SignZone: %v", err)
	}
}

func TestSignZoneECDSA(t *testing.T) {
	curve := "P256"

	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("algorithm"); got != "ECDSA" {
			t.Errorf("algorithm = %q, want ECDSA", got)
		}
		if got := q.Get("curve"); got != curve {
			t.Errorf("curve = %q, want %q", got, curve)
		}
		if q.Has("hashAlgorithm") || q.Has("kskKeySize") || q.Has("zskKeySize") {
			t.Error("RSA-only parameters must not be sent for an ECDSA sign request")
		}
		_, _ = w.Write([]byte(statusOK))
	})

	err := c.SignZone(context.Background(), SignZoneOptions{
		Zone:      "example.com",
		Algorithm: "ECDSA",
		Curve:     &curve,
	})
	if err != nil {
		t.Fatalf("SignZone: %v", err)
	}
}

func TestSignZoneRequiresZoneAndAlgorithm(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(statusOK))
	})

	if err := c.SignZone(context.Background(), SignZoneOptions{Algorithm: "RSA"}); err == nil {
		t.Error("expected error for empty zone")
	}
	if err := c.SignZone(context.Background(), SignZoneOptions{Zone: "example.com"}); err == nil {
		t.Error("expected error for empty algorithm")
	}
}

func TestUnsignZone(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/zones/dnssec/unsign" {
			t.Errorf("path = %q, want /api/zones/dnssec/unsign", r.URL.Path)
		}
		if got := r.URL.Query().Get("zone"); got != "example.com" {
			t.Errorf("zone = %q, want example.com", got)
		}
		_, _ = w.Write([]byte(statusOK))
	})

	if err := c.UnsignZone(context.Background(), "example.com"); err != nil {
		t.Fatalf("UnsignZone: %v", err)
	}
}

func TestUnsignZoneRequiresZone(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(statusOK))
	})

	if err := c.UnsignZone(context.Background(), ""); err == nil {
		t.Error("expected error for empty zone")
	}
}

func TestGetDNSSECProperties(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/zones/dnssec/properties/get" {
			t.Errorf("path = %q, want /api/zones/dnssec/properties/get", r.URL.Path)
		}
		if got := r.URL.Query().Get("zone"); got != "example.com" {
			t.Errorf("zone = %q, want example.com", got)
		}

		body := `{"response":{"name":"example.com","dnssecStatus":"SignedWithNSEC","dnsKeyTtl":3600},"status":"ok"}`
		_, _ = w.Write([]byte(body))
	})

	props, err := c.GetDNSSECProperties(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("GetDNSSECProperties: %v", err)
	}
	if props.DNSSECStatus != DNSSECStatusSignedWithNSEC {
		t.Errorf("DNSSECStatus = %q, want %q", props.DNSSECStatus, DNSSECStatusSignedWithNSEC)
	}
	if props.DNSKeyTTL != 3600 {
		t.Errorf("DNSKeyTTL = %d, want 3600", props.DNSKeyTTL)
	}
}

func TestGetDNSSECPropertiesRequiresZone(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(statusOK))
	})

	if _, err := c.GetDNSSECProperties(context.Background(), ""); err == nil {
		t.Error("expected error for empty zone")
	}
}

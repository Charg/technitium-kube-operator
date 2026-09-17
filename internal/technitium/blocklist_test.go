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

func TestAllowedAndBlockedZones(t *testing.T) {
	tests := []struct {
		name     string
		call     func(c *Client) error
		wantPath string
	}{
		{
			name:     "add allowed",
			call:     func(c *Client) error { return c.AddAllowedZone(context.Background(), "example.com") },
			wantPath: "/api/allowed/add",
		},
		{
			name:     "delete allowed",
			call:     func(c *Client) error { return c.DeleteAllowedZone(context.Background(), "example.com") },
			wantPath: "/api/allowed/delete",
		},
		{
			name:     "add blocked",
			call:     func(c *Client) error { return c.AddBlockedZone(context.Background(), "ads.example.com") },
			wantPath: "/api/blocked/add",
		},
		{
			name:     "delete blocked",
			call:     func(c *Client) error { return c.DeleteBlockedZone(context.Background(), "ads.example.com") },
			wantPath: "/api/blocked/delete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.wantPath {
					t.Errorf("path = %q, want %q", r.URL.Path, tt.wantPath)
				}
				if r.URL.Query().Get("domain") == "" {
					t.Error("domain parameter missing")
				}
				_, _ = w.Write([]byte(statusOK))
			})

			if err := tt.call(c); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
		})
	}
}

func TestAllowedAndBlockedZonesRequireDomain(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(statusOK))
	})

	if err := c.AddAllowedZone(context.Background(), ""); err == nil {
		t.Error("expected error for empty domain on AddAllowedZone")
	}
	if err := c.DeleteBlockedZone(context.Background(), ""); err == nil {
		t.Error("expected error for empty domain on DeleteBlockedZone")
	}
}

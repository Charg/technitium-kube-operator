/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestGetDNSSettings(t *testing.T) {
	body := `{"status":"ok","response":{` +
		`"recursion":"AllowOnlyForPrivateNetworks","recursionNetworkACL":["192.168.0.0/16"],` +
		`"forwarders":["1.1.1.1","8.8.8.8"],"forwarderProtocol":"Tls",` +
		`"serveStale":true,"serveStaleTtl":259200,"cacheMaximumRecordTtl":604800,"cacheMinimumRecordTtl":10,` +
		`"enableLogging":true,"logQueries":false,"useLocalTime":true,"maxLogFileDays":30,` +
		`"enableBlocking":true,"blockingType":"NxDomain",` +
		`"blockListUrls":["https://example.com/hosts"],"blockListUpdateIntervalHours":24}}`

	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	settings, err := c.GetDNSSettings(context.Background())
	if err != nil {
		t.Fatalf("GetDNSSettings: %v", err)
	}

	if got := settings.Forwarders; len(got) != 2 || got[0] != "1.1.1.1" || got[1] != "8.8.8.8" {
		t.Errorf("forwarders = %v", got)
	}
	if settings.ForwarderProtocol != "Tls" {
		t.Errorf("forwarderProtocol = %q", settings.ForwarderProtocol)
	}
	if settings.Recursion != "AllowOnlyForPrivateNetworks" {
		t.Errorf("recursion = %q", settings.Recursion)
	}
	if len(settings.RecursionNetworkACL) != 1 || settings.RecursionNetworkACL[0] != "192.168.0.0/16" {
		t.Errorf("recursionNetworkACL = %v", settings.RecursionNetworkACL)
	}
	if !settings.ServeStale || settings.ServeStaleTTL != 259200 {
		t.Errorf("serveStale=%v ttl=%d", settings.ServeStale, settings.ServeStaleTTL)
	}
	if settings.CacheMaximumRecordTTL != 604800 || settings.CacheMinimumRecordTTL != 10 {
		t.Errorf("cache max=%d min=%d", settings.CacheMaximumRecordTTL, settings.CacheMinimumRecordTTL)
	}
	if !settings.EnableLogging || settings.LogQueries || !settings.UseLocalTime || settings.MaxLogFileDays != 30 {
		t.Errorf("logging fields: %+v", settings)
	}
	if !settings.EnableBlocking || settings.BlockingType != "NxDomain" || settings.BlockListUpdateIntervalHours != 24 {
		t.Errorf("blocking fields: %+v", settings)
	}
	if len(settings.BlockListURLs) != 1 || settings.BlockListURLs[0] != "https://example.com/hosts" {
		t.Errorf("blockListUrls = %v", settings.BlockListURLs)
	}
}

func TestSetDNSSettings(t *testing.T) {
	tests := []struct {
		name      string
		opts      SetDNSSettingsOptions
		wantQuery url.Values
		// absent lists parameter keys that must not appear in the request.
		absent []string
	}{
		{
			name: "forwarding and recursion",
			opts: SetDNSSettingsOptions{
				Forwarders:          &[]string{"1.1.1.1", "8.8.8.8"},
				ForwarderProtocol:   ptr("Tls"),
				Recursion:           ptr("UseSpecifiedNetworkACL"),
				RecursionNetworkACL: &[]string{"192.168.0.0/16", "!10.0.0.0/8"},
			},
			wantQuery: url.Values{
				"forwarders":          {"1.1.1.1,8.8.8.8"},
				"forwarderProtocol":   {"Tls"},
				"recursion":           {"UseSpecifiedNetworkACL"},
				"recursionNetworkACL": {"192.168.0.0/16,!10.0.0.0/8"},
			},
			absent: []string{"serveStale", "enableBlocking", "blockListUrls"},
		},
		{
			name: "empty forwarder slice removes forwarders",
			opts: SetDNSSettingsOptions{Forwarders: &[]string{}},
			wantQuery: url.Values{
				"forwarders": {"false"},
			},
		},
		{
			name: "cache and logging",
			opts: SetDNSSettingsOptions{
				ServeStale:            ptr(true),
				ServeStaleTTL:         ptr(int32(86400)),
				CacheMaximumRecordTTL: ptr(int32(604800)),
				EnableLogging:         ptr(true),
				LogQueries:            ptr(false),
				UseLocalTime:          ptr(true),
				MaxLogFileDays:        ptr(int32(30)),
			},
			wantQuery: url.Values{
				"serveStale":            {"true"},
				"serveStaleTtl":         {"86400"},
				"cacheMaximumRecordTtl": {"604800"},
				"enableLogging":         {"true"},
				"logQueries":            {"false"},
				"useLocalTime":          {"true"},
				"maxLogFileDays":        {"30"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				assertQuery(t, r, tt.wantQuery)
				for _, key := range tt.absent {
					if r.URL.Query().Has(key) {
						t.Errorf("query %q should be absent when its option is nil", key)
					}
				}
				_, _ = w.Write([]byte(statusOK))
			})

			if err := c.SetDNSSettings(context.Background(), tt.opts); err != nil {
				t.Fatalf("SetDNSSettings: %v", err)
			}
		})
	}
}

func TestSetDNSSettingsBlocking(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		assertQuery(t, r, url.Values{
			"enableBlocking":               {"true"},
			"blockingType":                 {"NxDomain"},
			"blockListUrls":                {"https://a/hosts,https://b/hosts"},
			"blockListUpdateIntervalHours": {"12"},
		})
		_, _ = w.Write([]byte(statusOK))
	})

	err := c.SetDNSSettings(context.Background(), SetDNSSettingsOptions{
		EnableBlocking:               ptr(true),
		BlockingType:                 ptr("NxDomain"),
		BlockListURLs:                &[]string{"https://a/hosts", "https://b/hosts"},
		BlockListUpdateIntervalHours: ptr(int32(12)),
	})
	if err != nil {
		t.Fatalf("SetDNSSettings: %v", err)
	}
}

func TestForceUpdateBlockLists(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/settings/forceUpdateBlockLists" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(statusOK))
	})

	if err := c.ForceUpdateBlockLists(context.Background()); err != nil {
		t.Fatalf("ForceUpdateBlockLists: %v", err)
	}
}

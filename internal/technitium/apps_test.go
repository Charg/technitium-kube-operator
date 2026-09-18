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

const (
	testAppName             = "Split Horizon"
	appNotInstalledBody     = `{"status":"error","errorMessage":"No such app was installed with name: Split Horizon"}`
	appAlreadyInstalledBody = `{"status":"error","errorMessage":"App already installed with name: Split Horizon"}`
)

func TestListApps(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantErrIs error
		wantLen   int
	}{
		{
			name: nameSuccess,
			body: `{"status":"ok","response":{"apps":[` +
				`{"name":"Split Horizon","version":"1.4"},` +
				`{"name":"Advanced Blocking","version":"5.1"}` +
				`]}}`,
			wantLen: 2,
		},
		{
			name:    "no apps installed",
			body:    `{"status":"ok","response":{"apps":[]}}`,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/apps/list" {
					t.Errorf("path = %q, want /api/apps/list", r.URL.Path)
				}
				_, _ = w.Write([]byte(tt.body))
			})

			apps, err := c.ListApps(context.Background())
			assertErr(t, err, tt.wantErrIs, tt.body)
			if tt.wantErrIs != nil {
				return
			}
			if len(apps) != tt.wantLen {
				t.Fatalf("got %d apps, want %d", len(apps), tt.wantLen)
			}
		})
	}

	t.Run("parses name and version", func(t *testing.T) {
		c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ok","response":{"apps":[{"name":"Split Horizon","version":"1.4"}]}}`))
		})

		apps, err := c.ListApps(context.Background())
		if err != nil {
			t.Fatalf("ListApps: %v", err)
		}
		if len(apps) != 1 || apps[0].Name != "Split Horizon" || apps[0].Version != "1.4" {
			t.Errorf("unexpected apps: %+v", apps)
		}
	})
}

func TestDownloadAndInstallApp(t *testing.T) {
	tests := []struct {
		name      string
		appName   string
		url       string
		body      string
		wantErrIs error
		wantQuery url.Values
	}{
		{
			name:      nameSuccess,
			appName:   testAppName,
			url:       "https://download.technitium.com/dns/apps/SplitHorizonApp-v1.4.zip",
			body:      statusOK,
			wantQuery: url.Values{"name": {testAppName}, "url": {"https://download.technitium.com/dns/apps/SplitHorizonApp-v1.4.zip"}},
		},
		{
			name:      "already installed is a sentinel, tolerated as idempotent by callers",
			appName:   testAppName,
			url:       "https://download.technitium.com/dns/apps/SplitHorizonApp-v1.4.zip",
			body:      appAlreadyInstalledBody,
			wantErrIs: ErrAppAlreadyInstalled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/apps/downloadAndInstall" {
					t.Errorf("path = %q, want /api/apps/downloadAndInstall", r.URL.Path)
				}
				assertQuery(t, r, tt.wantQuery)
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.DownloadAndInstallApp(context.Background(), tt.appName, tt.url)
			assertErr(t, err, tt.wantErrIs, tt.body)
		})
	}
}

func TestDownloadAndInstallAppRequiresNameAndURL(t *testing.T) {
	c, err := NewClient("https://dns.internal", WithToken("t"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := c.DownloadAndInstallApp(context.Background(), "", "https://example.com/app.zip"); err == nil {
		t.Error("expected error for empty app name")
	}
	if err := c.DownloadAndInstallApp(context.Background(), testAppName, ""); err == nil {
		t.Error("expected error for empty url")
	}
}

func TestDownloadAndUpdateApp(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/downloadAndUpdate" {
			t.Errorf("path = %q, want /api/apps/downloadAndUpdate", r.URL.Path)
		}
		assertQuery(t, r, url.Values{"name": {testAppName}, "url": {"https://example.com/app-v2.zip"}})
		_, _ = w.Write([]byte(statusOK))
	})

	if err := c.DownloadAndUpdateApp(context.Background(), testAppName, "https://example.com/app-v2.zip"); err != nil {
		t.Fatalf("DownloadAndUpdateApp: %v", err)
	}
}

func TestUninstallApp(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantErrIs error
	}{
		{name: nameSuccess, body: statusOK},
		{
			name:      "not installed is a sentinel, tolerated as idempotent by callers",
			body:      appNotInstalledBody,
			wantErrIs: ErrAppNotInstalled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/apps/uninstall" {
					t.Errorf("path = %q, want /api/apps/uninstall", r.URL.Path)
				}
				assertQuery(t, r, url.Values{"name": {testAppName}})
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.UninstallApp(context.Background(), testAppName)
			assertErr(t, err, tt.wantErrIs, tt.body)
		})
	}
}

func TestUninstallAppRequiresName(t *testing.T) {
	c, err := NewClient("https://dns.internal", WithToken("t"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.UninstallApp(context.Background(), ""); err == nil {
		t.Error("expected error for empty app name")
	}
}

func TestGetAppConfig(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/config/get" {
			t.Errorf("path = %q, want /api/apps/config/get", r.URL.Path)
		}
		assertQuery(t, r, url.Values{"name": {testAppName}})
		_, _ = w.Write([]byte(`{"status":"ok","response":{"config":"{\"key\":\"value\"}"}}`))
	})

	config, err := c.GetAppConfig(context.Background(), testAppName)
	if err != nil {
		t.Fatalf("GetAppConfig: %v", err)
	}
	if config != `{"key":"value"}` {
		t.Errorf("config = %q, want %q", config, `{"key":"value"}`)
	}
}

func TestGetAppConfigRequiresName(t *testing.T) {
	c, err := NewClient("https://dns.internal", WithToken("t"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.GetAppConfig(context.Background(), ""); err == nil {
		t.Error("expected error for empty app name")
	}
}

func TestSetAppConfig(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantErrIs error
	}{
		{name: nameSuccess, body: statusOK},
		{
			name:      "invalid config surfaces as a generic APIError, not a sentinel",
			body:      `{"status":"error","errorMessage":"Config validation failed: unexpected token."}`,
			wantErrIs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/apps/config/set" {
					t.Errorf("path = %q, want /api/apps/config/set", r.URL.Path)
				}
				assertQuery(t, r, url.Values{"name": {testAppName}, "config": {"bad json"}})
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.SetAppConfig(context.Background(), testAppName, "bad json")
			assertErr(t, err, tt.wantErrIs, tt.body)
		})
	}
}

func TestSetAppConfigRequiresName(t *testing.T) {
	c, err := NewClient("https://dns.internal", WithToken("t"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.SetAppConfig(context.Background(), "", "{}"); err == nil {
		t.Error("expected error for empty app name")
	}
}

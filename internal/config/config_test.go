/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package config

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/charg/technitium-operator/internal/technitium"
)

const (
	testSecretName = "creds"
	testURL        = "https://dns:5380"
	testToken      = "abc123"
	testUsername   = "admin"
	testPassword   = "s3cret"
)

func TestBindFlagsSeedsFromEnv(t *testing.T) {
	t.Setenv(envURL, "https://dns.internal:5380")
	t.Setenv(envSecretName, "tech-creds")
	t.Setenv(envSecretNamespace, "dns-system")
	t.Setenv(envInsecure, "true")

	var c Config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c.BindFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if c.TechnitiumURL != "https://dns.internal:5380" {
		t.Errorf("TechnitiumURL = %q, want the env value", c.TechnitiumURL)
	}
	if c.SecretName != "tech-creds" {
		t.Errorf("SecretName = %q, want tech-creds", c.SecretName)
	}
	if c.SecretNamespace != "dns-system" {
		t.Errorf("SecretNamespace = %q, want dns-system", c.SecretNamespace)
	}
	if !c.InsecureSkipVerify {
		t.Errorf("InsecureSkipVerify = false, want true")
	}
}

func TestBindFlagsOverrideEnv(t *testing.T) {
	t.Setenv(envURL, "https://from-env:5380")

	var c Config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c.BindFlags(fs)
	if err := fs.Parse([]string{"--technitium-url=https://from-flag:5380"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if c.TechnitiumURL != "https://from-flag:5380" {
		t.Errorf("TechnitiumURL = %q, want the flag value to override env", c.TechnitiumURL)
	}
}

func TestSecretNamespaceFallsBackToPodNamespace(t *testing.T) {
	t.Setenv(envSecretNamespace, "")
	t.Setenv(envPodNamespace, "operator-ns")

	var c Config
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c.BindFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if c.SecretNamespace != "operator-ns" {
		t.Errorf("SecretNamespace = %q, want fallback to POD_NAMESPACE", c.SecretNamespace)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		// missing is a substring the error must mention when wantErr is true.
		missing string
	}{
		{
			name:    "complete",
			cfg:     Config{TechnitiumURL: testURL, SecretName: testSecretName, SecretNamespace: "ns"},
			wantErr: false,
		},
		{
			name:    "missing url",
			cfg:     Config{SecretName: testSecretName, SecretNamespace: "ns"},
			wantErr: true,
			missing: "TECHNITIUM_URL",
		},
		{
			name:    "missing secret name",
			cfg:     Config{TechnitiumURL: testURL, SecretNamespace: "ns"},
			wantErr: true,
			missing: "TECHNITIUM_CREDENTIALS_SECRET",
		},
		{
			name:    "missing namespace",
			cfg:     Config{TechnitiumURL: testURL, SecretName: testSecretName},
			wantErr: true,
			missing: "namespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), tt.missing) {
				t.Errorf("Validate() error = %q, want it to mention %q", err, tt.missing)
			}
		})
	}
}

func secretWith(data map[string]string) *corev1.Secret {
	d := make(map[string][]byte, len(data))
	for k, v := range data {
		d[k] = []byte(v)
	}
	return &corev1.Secret{Data: d}
}

func TestClientOptionsFromSecret(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		opts, err := ClientOptionsFromSecret(secretWith(map[string]string{secretKeyToken: testToken}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		c, err := technitium.NewClient(testURL, opts...)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if got := c.Token(); got != testToken {
			t.Errorf("token = %q, want abc123", got)
		}
	})

	t.Run("username and password", func(t *testing.T) {
		var gotUser, gotPass string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser = r.URL.Query().Get("user")
			gotPass = r.URL.Query().Get("pass")
			_, _ = w.Write([]byte(`{"status":"ok","token":"issued"}`))
		}))
		defer srv.Close()

		opts, err := ClientOptionsFromSecret(secretWith(map[string]string{secretKeyUsername: testUsername, secretKeyPassword: testPassword}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		c, err := technitium.NewClient(srv.URL, opts...)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		if _, err := c.Login(context.Background()); err != nil {
			t.Fatalf("Login: %v", err)
		}
		if gotUser != testUsername || gotPass != testPassword {
			t.Errorf("login sent (%q, %q), want (admin, s3cret)", gotUser, gotPass)
		}
	})

	t.Run("token preferred over username", func(t *testing.T) {
		opts, err := ClientOptionsFromSecret(secretWith(map[string]string{
			secretKeyToken: testToken, secretKeyUsername: testUsername, secretKeyPassword: testPassword,
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		c, _ := technitium.NewClient(testURL, opts...)
		if c.Token() != testToken {
			t.Errorf("token = %q, want abc123 to take precedence", c.Token())
		}
	})

	t.Run("whitespace trimmed", func(t *testing.T) {
		opts, err := ClientOptionsFromSecret(secretWith(map[string]string{secretKeyToken: "  abc123\n"}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		c, _ := technitium.NewClient(testURL, opts...)
		if c.Token() != testToken {
			t.Errorf("token = %q, want trimmed abc123", c.Token())
		}
	})

	t.Run("empty secret", func(t *testing.T) {
		_, err := ClientOptionsFromSecret(secretWith(map[string]string{}))
		if err == nil {
			t.Fatal("expected error for secret with no credentials")
		}
	})

	t.Run("nil secret", func(t *testing.T) {
		if _, err := ClientOptionsFromSecret(nil); err == nil {
			t.Fatal("expected error for nil secret")
		}
	})
}

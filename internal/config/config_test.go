/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/charg/technitium-operator/internal/technitium"
)

const (
	testURL      = "https://dns:5380"
	testToken    = "abc123"
	testUsername = "admin"
	testPassword = "s3cret"
)

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

func TestUsernamePasswordFromSecret(t *testing.T) {
	t.Run("username and password", func(t *testing.T) {
		username, password, err := UsernamePasswordFromSecret(secretWith(map[string]string{
			secretKeyUsername: testUsername, secretKeyPassword: testPassword,
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if username != testUsername || password != testPassword {
			t.Errorf("got (%q, %q), want (%q, %q)", username, password, testUsername, testPassword)
		}
	})

	t.Run("username trimmed, password untouched", func(t *testing.T) {
		username, password, err := UsernamePasswordFromSecret(secretWith(map[string]string{
			secretKeyUsername: "  admin\n", secretKeyPassword: "s3cret\n",
		}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if username != testUsername {
			t.Errorf("username = %q, want trimmed %q", username, testUsername)
		}
		if password != "s3cret\n" {
			t.Errorf("password = %q, want untrimmed %q", password, "s3cret\n")
		}
	})

	t.Run("missing username", func(t *testing.T) {
		if _, _, err := UsernamePasswordFromSecret(secretWith(map[string]string{secretKeyPassword: testPassword})); err == nil {
			t.Fatal("expected error for missing username")
		}
	})

	t.Run("missing password", func(t *testing.T) {
		if _, _, err := UsernamePasswordFromSecret(secretWith(map[string]string{secretKeyUsername: testUsername})); err == nil {
			t.Fatal("expected error for missing password")
		}
	})

	t.Run("nil secret", func(t *testing.T) {
		if _, _, err := UsernamePasswordFromSecret(nil); err == nil {
			t.Fatal("expected error for nil secret")
		}
	})
}

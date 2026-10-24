/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	nameSuccess      = "success"
	testUser         = "admin"
	testZone         = "example.com"
	paramZone        = "zone"
	statusOK         = `{"status":"ok"}`
	typePrimary      = "Primary"
	paramType        = "type"
	zoneNotFoundBody = `{"status":"error","errorMessage":"No such zone was found: example.com"}`
)

// newTestClient starts an httptest.Server with handler and returns a Client
// pointed at it, with the given token preset.
func newTestClient(t *testing.T, token string, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := NewClient(srv.URL, WithToken(token))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClient(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{name: "valid", baseURL: "https://dns.internal:5380", wantErr: false},
		{name: "empty base URL", baseURL: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(tt.baseURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewClient(%q) err = %v, wantErr %v", tt.baseURL, err, tt.wantErr)
			}
		})
	}
}

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	c, err := NewClient("https://dns.internal:5380/")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.baseURL != "https://dns.internal:5380" {
		t.Fatalf("baseURL = %q, want trailing slash trimmed", c.baseURL)
	}
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name       string
		username   string
		body       string
		wantErr    bool
		wantToken  string
		wantNoCall bool
	}{
		{
			name:      nameSuccess,
			username:  testUser,
			body:      `{"token":"abc123","status":"ok"}`,
			wantToken: "abc123",
		},
		{
			name:      "bad credentials",
			username:  testUser,
			body:      `{"status":"error","errorMessage":"Invalid username or password."}`,
			wantErr:   true,
			wantToken: "",
		},
		{
			name:      "empty token in ok response",
			username:  testUser,
			body:      statusOK,
			wantErr:   true,
			wantToken: "",
		},
		{
			name:       "missing username",
			username:   "",
			wantErr:    true,
			wantNoCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if got := formParams(r).Get("user"); got != tt.username {
					t.Errorf("user param = %q, want %q", got, tt.username)
				}
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c, err := NewClient(srv.URL, WithCredentials(tt.username, "secret"))
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			token, err := c.Login(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Login err = %v, wantErr %v", err, tt.wantErr)
			}
			if token != tt.wantToken {
				t.Errorf("token = %q, want %q", token, tt.wantToken)
			}
			if tt.wantNoCall && called {
				t.Error("expected no HTTP call")
			}
			if !tt.wantErr && c.token != tt.wantToken {
				t.Errorf("stored token = %q, want %q", c.token, tt.wantToken)
			}
		})
	}
}

func TestDoRawSendsTokenAsBearerHeader(t *testing.T) {
	var gotMethod, gotContentType, gotAuth, gotZone string
	var tokenInQuery, tokenInForm bool
	c := newTestClient(t, "tok-42", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		tokenInQuery = r.URL.Query().Has("token")
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		tokenInForm = r.PostForm.Has("token")
		gotZone = r.PostForm.Get(paramZone)
		_, _ = w.Write([]byte(statusOK))
	})

	if err := c.DeleteZone(context.Background(), testZone); err != nil {
		t.Fatalf("DeleteZone: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", gotContentType)
	}
	if gotAuth != "Bearer tok-42" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok-42")
	}
	if tokenInQuery || tokenInForm {
		t.Errorf("token sent as a parameter (query %v, form %v), want only the Authorization header", tokenInQuery, tokenInForm)
	}
	if gotZone != testZone {
		t.Errorf("zone form param = %q, want %q", gotZone, testZone)
	}
}

func TestDoRawStatusClassification(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		httpCode  int
		wantErrIs error
	}{
		{
			name:      "invalid token",
			body:      `{"status":"invalid-token","errorMessage":"Session expired."}`,
			wantErrIs: ErrInvalidToken,
		},
		{
			name:      "generic error yields APIError",
			body:      `{"status":"error","errorMessage":"Something broke."}`,
			wantErrIs: nil,
		},
		{
			name:      "non-envelope body yields APIError with http status",
			body:      `<html>502 Bad Gateway</html>`,
			httpCode:  http.StatusBadGateway,
			wantErrIs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				if tt.httpCode != 0 {
					w.WriteHeader(tt.httpCode)
				}
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.DeleteZone(context.Background(), testZone)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("err = %v, want errors.Is %v", err, tt.wantErrIs)
			}
			if tt.wantErrIs == nil {
				if _, ok := errors.AsType[*APIError](err); !ok {
					t.Fatalf("err = %v, want *APIError", err)
				}
			}
		})
	}
}

func TestNetworkFailure(t *testing.T) {
	// Start a server, capture its URL, then close it so the connection is
	// refused on the next call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c, err := NewClient(srv.URL, WithToken("t"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	srv.Close()

	if err := c.DeleteZone(context.Background(), testZone); err == nil {
		t.Fatal("expected network error, got nil")
	} else if errors.Is(err, ErrZoneNotFound) || errors.Is(err, ErrZoneAlreadyExists) {
		t.Fatalf("network failure misclassified as sentinel: %v", err)
	}
}

// TestTransportErrorsOmitCredentials pins that a failed request never echoes
// the password or token, since callers log these errors and copy them into
// status conditions.
func TestTransportErrorsOmitCredentials(t *testing.T) {
	const (
		password = "hunter2-password"
		token    = "tok-secret-42"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	baseURL := srv.URL
	srv.Close()

	login, err := NewClient(baseURL, WithCredentials(testUser, password))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	authed, err := NewClient(baseURL, WithToken(token))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	calls := map[string]func() error{
		"Login": func() error { _, err := login.Login(context.Background()); return err },
		"CreateToken": func() error {
			_, err := login.CreateToken(context.Background(), "technitium-operator")
			return err
		},
		"ChangePassword": func() error { return authed.ChangePassword(context.Background(), password) },
		"DeleteZone":     func() error { return authed.DeleteZone(context.Background(), testZone) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("expected a connection error, got nil")
			}
			for _, secret := range []string{password, token} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks a credential: %v", err)
				}
			}
		})
	}
}

// formParams returns the request's form parameters, which the client sends as
// a POST body.
func formParams(r *http.Request) url.Values {
	_ = r.ParseForm()
	return r.PostForm
}

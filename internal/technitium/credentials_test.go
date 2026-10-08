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
	"testing"
)

// TestCreateTokenClassifiesRejectedCredentials pins that a rejected
// username/password is distinguishable (errors.Is ErrInvalidCredentials) from
// a connection failure and from other API errors, since the cluster
// controller treats only the former as a non-transient fault.
func TestCreateTokenClassifiesRejectedCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorMessage":"Invalid username or password for user: admin"}`))
	}))
	c, err := NewClient(srv.URL, WithCredentials(testUser, "secret"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err = c.CreateToken(context.Background(), "technitium-operator"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("rejected credentials err = %v, want ErrInvalidCredentials", err)
	}

	// A closed server is a connection failure, not a credentials rejection.
	srv.Close()
	if _, err = c.CreateToken(context.Background(), "technitium-operator"); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("connection failure err = %v, want a non-credentials error", err)
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorMessage":"something else broke"}`))
	}))
	defer other.Close()
	c2, err := NewClient(other.URL, WithCredentials(testUser, "secret"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err = c2.CreateToken(context.Background(), "technitium-operator"); err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("other API error = %v, want a non-credentials error", err)
	}
}

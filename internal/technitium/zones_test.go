/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package technitium

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestCreateZone(t *testing.T) {
	tests := []struct {
		name      string
		opts      CreateZoneOptions
		status    int
		body      string
		wantErrIs error
		wantQuery url.Values
	}{
		{
			name:      nameSuccess,
			opts:      CreateZoneOptions{Zone: testZone, Type: typePrimary},
			body:      `{"response":{"domain":"example.com"},"status":"ok"}`,
			wantQuery: url.Values{paramZone: {testZone}, paramType: {typePrimary}},
		},
		{
			name:      "already exists is a sentinel",
			opts:      CreateZoneOptions{Zone: testZone, Type: typePrimary},
			body:      `{"status":"error","errorMessage":"Zone already exists: example.com"}`,
			wantErrIs: ErrZoneAlreadyExists,
		},
		{
			name: "forwarder params are sent",
			opts: CreateZoneOptions{
				Zone:      "fwd.example.com",
				Type:      "Forwarder",
				Forwarder: "1.1.1.1",
				Protocol:  "Https",
			},
			body: `{"response":{"domain":"fwd.example.com"},"status":"ok"}`,
			wantQuery: url.Values{
				paramZone:   {"fwd.example.com"},
				paramType:   {"Forwarder"},
				"forwarder": {"1.1.1.1"},
				"protocol":  {"Https"},
			},
		},
		{
			name: "secondary primary addresses joined",
			opts: CreateZoneOptions{
				Zone:                       "sec.example.com",
				Type:                       "Secondary",
				PrimaryNameServerAddresses: []string{"10.0.0.1", "10.0.0.2"},
			},
			body: statusOK,
			wantQuery: url.Values{
				paramZone:                    {"sec.example.com"},
				paramType:                    {"Secondary"},
				"primaryNameServerAddresses": {"10.0.0.1,10.0.0.2"},
			},
		},
		{
			name:      "generic error",
			opts:      CreateZoneOptions{Zone: testZone},
			body:      `{"status":"error","errorMessage":"Access was denied."}`,
			wantErrIs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				assertQuery(t, r, tt.wantQuery)
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.CreateZone(context.Background(), tt.opts)
			assertErr(t, err, tt.wantErrIs, tt.body)
		})
	}
}

func TestCreateZoneRequiresName(t *testing.T) {
	c, err := NewClient("https://dns.internal", WithToken("t"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.CreateZone(context.Background(), CreateZoneOptions{}); err == nil {
		t.Fatal("expected error for empty zone name")
	}
}

func TestDeleteZone(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantErrIs error
	}{
		{
			name: nameSuccess,
			body: statusOK,
		},
		{
			name:      "missing zone is a sentinel",
			body:      zoneNotFoundBody,
			wantErrIs: ErrZoneNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				assertQuery(t, r, url.Values{paramZone: {testZone}})
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.DeleteZone(context.Background(), testZone)
			assertErr(t, err, tt.wantErrIs, tt.body)
		})
	}
}

func TestGetZoneOptions(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantErrIs error
		wantName  string
		wantCat   string
	}{
		{
			name:     nameSuccess,
			body:     `{"response":{"name":"example.com","type":"Primary","disabled":false,"catalog":"cat1"},"status":"ok"}`,
			wantName: "example.com",
			wantCat:  "cat1",
		},
		{
			name:      "missing zone",
			body:      zoneNotFoundBody,
			wantErrIs: ErrZoneNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				assertQuery(t, r, url.Values{paramZone: {testZone}})
				_, _ = w.Write([]byte(tt.body))
			})

			opts, err := c.GetZoneOptions(context.Background(), testZone)
			assertErr(t, err, tt.wantErrIs, tt.body)
			if tt.wantErrIs != nil || err != nil {
				if opts != nil {
					t.Errorf("opts = %+v, want nil on error", opts)
				}
				return
			}
			if opts.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", opts.Name, tt.wantName)
			}
			if opts.Catalog != tt.wantCat {
				t.Errorf("Catalog = %q, want %q", opts.Catalog, tt.wantCat)
			}
		})
	}
}

func TestSetZoneOptions(t *testing.T) {
	catalog := "cat2"
	disabled := true

	tests := []struct {
		name      string
		opts      ZoneOptionsUpdate
		body      string
		wantErrIs error
		wantQuery url.Values
	}{
		{
			name:      "sets catalog and disabled",
			opts:      ZoneOptionsUpdate{Catalog: &catalog, Disabled: &disabled},
			body:      statusOK,
			wantQuery: url.Values{paramZone: {testZone}, "catalog": {"cat2"}, "disabled": {"true"}},
		},
		{
			name:      "empty catalog clears membership",
			opts:      ZoneOptionsUpdate{Catalog: ptr("")},
			body:      statusOK,
			wantQuery: url.Values{paramZone: {testZone}, "catalog": {""}},
		},
		{
			name:      "no fields sends only zone",
			opts:      ZoneOptionsUpdate{},
			body:      statusOK,
			wantQuery: url.Values{paramZone: {testZone}},
		},
		{
			name:      "missing zone",
			opts:      ZoneOptionsUpdate{},
			body:      zoneNotFoundBody,
			wantErrIs: ErrZoneNotFound,
			wantQuery: url.Values{paramZone: {testZone}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				assertQuery(t, r, tt.wantQuery)
				_, _ = w.Write([]byte(tt.body))
			})

			err := c.SetZoneOptions(context.Background(), "example.com", tt.opts)
			assertErr(t, err, tt.wantErrIs, tt.body)
		})
	}
}

func TestListZones(t *testing.T) {
	t.Run("single page", func(t *testing.T) {
		c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"response":{"pageNumber":1,"totalPages":1,"totalZones":2,`+
				`"zones":[{"name":"a.com","type":"Primary"},{"name":"b.com","type":"Stub"}]},"status":"ok"}`)
		})

		zones, err := c.ListZones(context.Background())
		if err != nil {
			t.Fatalf("ListZones: %v", err)
		}
		if len(zones) != 2 {
			t.Fatalf("got %d zones, want 2", len(zones))
		}
		if zones[0].Name != "a.com" || zones[1].Name != "b.com" {
			t.Errorf("unexpected zones: %+v", zones)
		}
	})

	t.Run("follows pagination", func(t *testing.T) {
		c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("pageNumber") {
			case "1":
				_, _ = fmt.Fprint(w, `{"response":{"pageNumber":1,"totalPages":2,"totalZones":3,`+
					`"zones":[{"name":"a.com"},{"name":"b.com"}]},"status":"ok"}`)
			case "2":
				_, _ = fmt.Fprint(w, `{"response":{"pageNumber":2,"totalPages":2,"totalZones":3,`+
					`"zones":[{"name":"c.com"}]},"status":"ok"}`)
			default:
				t.Errorf("unexpected pageNumber %q", r.URL.Query().Get("pageNumber"))
			}
		})

		zones, err := c.ListZones(context.Background())
		if err != nil {
			t.Fatalf("ListZones: %v", err)
		}
		if len(zones) != 3 {
			t.Fatalf("got %d zones, want 3", len(zones))
		}
	})

	t.Run("propagates error", func(t *testing.T) {
		c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"status":"invalid-token"}`)
		})

		if _, err := c.ListZones(context.Background()); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("err = %v, want ErrInvalidToken", err)
		}
	})
}

func ptr[T any](v T) *T { return &v }

// assertQuery checks that every wanted parameter is present with the expected
// value. The token parameter is appended by the client and ignored here.
func assertQuery(t *testing.T, r *http.Request, want url.Values) {
	t.Helper()
	if want == nil {
		return
	}
	got := r.URL.Query()
	for key, values := range want {
		if got.Get(key) != values[0] {
			t.Errorf("query %q = %q, want %q", key, got.Get(key), values[0])
		}
	}
}

// assertErr checks err against an expected sentinel. A nil wantErrIs with a
// non-ok body means an APIError is expected; a nil wantErrIs with an ok body
// means no error.
func assertErr(t *testing.T, err error, wantErrIs error, body string) {
	t.Helper()
	switch {
	case wantErrIs != nil:
		if !errors.Is(err, wantErrIs) {
			t.Fatalf("err = %v, want errors.Is %v", err, wantErrIs)
		}
	case isErrorBody(body):
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("err = %v, want *APIError", err)
		}
	default:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func isErrorBody(body string) bool {
	return body != "" && !strings.Contains(body, `"status":"ok"`)
}

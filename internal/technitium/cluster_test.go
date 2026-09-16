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
	"net/url"
	"testing"
)

const (
	testClusterDomain = "cluster.example.com"
	testPrimaryIP     = "10.0.0.1"
	testSecondaryIP   = "10.0.0.3"
)

const clusterStateInitialized = `{"response":{"version":"13.0","dnsServerDomain":"ns1.example.com",` +
	`"clusterInitialized":true,"clusterDomain":"cluster.example.com",` +
	`"clusterNodes":[{"id":1,"name":"ns1.example.com","url":"https://ns1.example.com:5380",` +
	`"ipAddresses":["10.0.0.1"],"type":"Primary","state":"Self","upSince":"2026-01-01T00:00:00Z"}]},"status":"ok"}`

func TestClusterState(t *testing.T) {
	t.Run("uninitialized node", func(t *testing.T) {
		c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"response":{"version":"13.0","dnsServerDomain":"ns1.example.com",` +
				`"clusterInitialized":false},"status":"ok"}`))
		})

		state, err := c.ClusterState(context.Background())
		if err != nil {
			t.Fatalf("ClusterState: %v", err)
		}
		if state.Initialized {
			t.Error("Initialized = true, want false")
		}
		if state.SelfNode() != nil {
			t.Error("SelfNode = non-nil, want nil for a standalone node")
		}
	})

	t.Run("initialized primary", func(t *testing.T) {
		c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(clusterStateInitialized))
		})

		state, err := c.ClusterState(context.Background())
		if err != nil {
			t.Fatalf("ClusterState: %v", err)
		}
		if !state.Initialized {
			t.Fatal("Initialized = false, want true")
		}
		if state.Domain != testClusterDomain {
			t.Errorf("Domain = %q, want cluster.example.com", state.Domain)
		}
		self := state.SelfNode()
		if self == nil {
			t.Fatal("SelfNode = nil, want the primary node")
		}
		if self.Type != ClusterNodeTypePrimary {
			t.Errorf("self type = %q, want Primary", self.Type)
		}
	})

	t.Run("propagates error", func(t *testing.T) {
		c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"status":"invalid-token"}`))
		})
		if _, err := c.ClusterState(context.Background()); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("err = %v, want ErrInvalidToken", err)
		}
	})
}

func TestClusterInit(t *testing.T) {
	tests := []struct {
		name       string
		opts       ClusterInitOptions
		body       string
		wantErrIs  error
		wantQuery  url.Values
		wantNoCall bool
	}{
		{
			name: nameSuccess,
			opts: ClusterInitOptions{ClusterDomain: testClusterDomain, PrimaryNodeIPAddresses: []string{testPrimaryIP, "10.0.0.2"}},
			body: clusterStateInitialized,
			wantQuery: url.Values{
				"clusterDomain":          {"cluster.example.com"},
				"primaryNodeIpAddresses": {"10.0.0.1,10.0.0.2"},
			},
		},
		{
			name:      "already initialized is a sentinel",
			opts:      ClusterInitOptions{ClusterDomain: testClusterDomain, PrimaryNodeIPAddresses: []string{testPrimaryIP}},
			body:      `{"status":"error","errorMessage":"Failed to initialize Cluster: the Cluster is already initialized."}`,
			wantErrIs: ErrClusterAlreadyInitialized,
		},
		{
			name:       "missing domain is rejected before any call",
			opts:       ClusterInitOptions{PrimaryNodeIPAddresses: []string{testPrimaryIP}},
			wantErrIs:  errNonNil,
			wantNoCall: true,
		},
		{
			name:       "missing IP addresses is rejected before any call",
			opts:       ClusterInitOptions{ClusterDomain: testClusterDomain},
			wantErrIs:  errNonNil,
			wantNoCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				called = true
				assertQuery(t, r, tt.wantQuery)
				_, _ = w.Write([]byte(tt.body))
			})

			_, err := c.ClusterInit(context.Background(), tt.opts)
			assertClusterErr(t, err, tt.wantErrIs)
			if tt.wantNoCall && called {
				t.Error("expected no HTTP call for invalid options")
			}
		})
	}
}

func TestClusterInitJoin(t *testing.T) {
	full := ClusterInitJoinOptions{
		SecondaryNodeIPAddresses: []string{testSecondaryIP},
		PrimaryNodeURL:           "https://ns1.example.com:5380",
		PrimaryNodeIPAddress:     testPrimaryIP,
		PrimaryNodeUsername:      testUser,
		PrimaryNodePassword:      "secret",
		IgnoreCertificateErrors:  true,
	}

	tests := []struct {
		name       string
		opts       ClusterInitJoinOptions
		body       string
		wantErrIs  error
		wantQuery  url.Values
		wantNoCall bool
	}{
		{
			name: nameSuccess,
			opts: full,
			body: clusterStateInitialized,
			wantQuery: url.Values{
				"secondaryNodeIpAddresses": {"10.0.0.3"},
				"primaryNodeUrl":           {"https://ns1.example.com:5380"},
				"primaryNodeIpAddress":     {"10.0.0.1"},
				"primaryNodeUsername":      {"admin"},
				"primaryNodePassword":      {"secret"},
				"ignoreCertificateErrors":  {"true"},
			},
		},
		{
			name:      "already joined is a sentinel",
			opts:      full,
			body:      `{"status":"error","errorMessage":"Failed to join Cluster: the Cluster is already initialized."}`,
			wantErrIs: ErrClusterAlreadyInitialized,
		},
		{
			name:      "primary not initialized is a sentinel",
			opts:      full,
			body:      `{"status":"error","errorMessage":"Failed to join Cluster: the Primary node does not have a Cluster initialized."}`,
			wantErrIs: ErrClusterNotInitialized,
		},
		{
			name:       "missing primary URL is rejected before any call",
			opts:       ClusterInitJoinOptions{SecondaryNodeIPAddresses: []string{testSecondaryIP}, PrimaryNodeUsername: testUser},
			wantErrIs:  errNonNil,
			wantNoCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				called = true
				assertQuery(t, r, tt.wantQuery)
				_, _ = w.Write([]byte(tt.body))
			})

			_, err := c.ClusterInitJoin(context.Background(), tt.opts)
			assertClusterErr(t, err, tt.wantErrIs)
			if tt.wantNoCall && called {
				t.Error("expected no HTTP call for invalid options")
			}
		})
	}
}

// errNonNil marks a test case that expects some error without a specific
// sentinel, distinct from a nil "expect success" expectation.
var errNonNil = errors.New("some error")

func assertClusterErr(t *testing.T, err, wantErrIs error) {
	t.Helper()
	switch {
	case wantErrIs == nil:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case errors.Is(wantErrIs, errNonNil):
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
	default:
		if !errors.Is(err, wantErrIs) {
			t.Fatalf("err = %v, want errors.Is %v", err, wantErrIs)
		}
	}
}

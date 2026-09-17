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

func TestGetClusterState(t *testing.T) {
	tests := []struct {
		name                string
		body                string
		wantInitialized     bool
		wantClusterDomain   string
		wantNodeCount       int
		wantFirstNodeName   string
		wantFirstNodeType   string
		wantSecondNodeState string
		wantDNSServerDomain string
	}{
		{
			name:                nameSuccess + " uninitialized",
			body:                `{"response":{"version":"13.4.0","dnsServerDomain":"dns-1","clusterInitialized":false},"status":"ok"}`,
			wantInitialized:     false,
			wantNodeCount:       0,
			wantDNSServerDomain: "dns-1",
		},
		{
			name: nameSuccess + " initialized two nodes",
			body: `{"response":{"version":"13.4.0","dnsServerDomain":"dns-0.cluster.internal",` +
				`"clusterInitialized":true,"clusterDomain":"cluster.internal","clusterNodes":[` +
				`{"id":1,"name":"dns-0.cluster.internal","url":"https://dns-0.cluster.internal:53443",` +
				`"ipAddresses":["10.0.0.1"],"type":"Primary","state":"Self","upSince":"2026-01-01T00:00:00Z"},` +
				`{"id":2,"name":"dns-1.cluster.internal","url":"https://dns-1.cluster.internal:53443",` +
				`"ipAddresses":["10.0.0.2"],"type":"Secondary","state":"Connected",` +
				`"lastSeen":"2026-01-02T00:00:00Z","configLastSynced":"2026-01-02T00:00:00Z"}` +
				`]},"status":"ok"}`,
			wantInitialized:     true,
			wantClusterDomain:   "cluster.internal",
			wantNodeCount:       2,
			wantFirstNodeName:   "dns-0.cluster.internal",
			wantFirstNodeType:   "Primary",
			wantSecondNodeState: "Connected",
			wantDNSServerDomain: "dns-0.cluster.internal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			})

			state, err := c.GetClusterState(context.Background())
			if err != nil {
				t.Fatalf("GetClusterState: %v", err)
			}
			if state.ClusterInitialized != tt.wantInitialized {
				t.Errorf("ClusterInitialized = %v, want %v", state.ClusterInitialized, tt.wantInitialized)
			}
			if state.DNSServerDomain != tt.wantDNSServerDomain {
				t.Errorf("DNSServerDomain = %q, want %q", state.DNSServerDomain, tt.wantDNSServerDomain)
			}
			if state.ClusterDomain != tt.wantClusterDomain {
				t.Errorf("ClusterDomain = %q, want %q", state.ClusterDomain, tt.wantClusterDomain)
			}
			if len(state.Nodes) != tt.wantNodeCount {
				t.Fatalf("len(Nodes) = %d, want %d", len(state.Nodes), tt.wantNodeCount)
			}
			if tt.wantNodeCount == 0 {
				return
			}
			if state.Nodes[0].Name != tt.wantFirstNodeName {
				t.Errorf("Nodes[0].Name = %q, want %q", state.Nodes[0].Name, tt.wantFirstNodeName)
			}
			if state.Nodes[0].Type != tt.wantFirstNodeType {
				t.Errorf("Nodes[0].Type = %q, want %q", state.Nodes[0].Type, tt.wantFirstNodeType)
			}
			if state.Nodes[0].UpSince == nil {
				t.Error("Nodes[0].UpSince = nil, want a parsed timestamp")
			}
			if tt.wantNodeCount < 2 {
				return
			}
			if state.Nodes[1].State != tt.wantSecondNodeState {
				t.Errorf("Nodes[1].State = %q, want %q", state.Nodes[1].State, tt.wantSecondNodeState)
			}
			if state.Nodes[1].LastSeen == nil {
				t.Error("Nodes[1].LastSeen = nil, want a parsed timestamp")
			}
			if state.Nodes[1].ConfigLastSynced == nil {
				t.Error("Nodes[1].ConfigLastSynced = nil, want a parsed timestamp")
			}
		})
	}
}

func TestGetClusterStatePropagatesError(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"invalid-token"}`))
	})

	if _, err := c.GetClusterState(context.Background()); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestInitCluster(t *testing.T) {
	var gotParams url.Values
	body := `{"response":{"version":"13.4.0","dnsServerDomain":"dns-0.cluster.internal",` +
		`"clusterInitialized":true,"clusterDomain":"cluster.internal","clusterNodes":[` +
		`{"id":1,"name":"dns-0.cluster.internal","url":"https://dns-0.cluster.internal:53443",` +
		`"ipAddresses":["10.0.0.1"],"type":"Primary","state":"Self"}]},"status":"ok"}`

	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		gotParams = r.URL.Query()
		_, _ = w.Write([]byte(body))
	})

	state, err := c.InitCluster(context.Background(), InitClusterOptions{
		ClusterDomain:          "cluster.internal",
		PrimaryNodeIPAddresses: []string{"10.0.0.1"},
	})
	if err != nil {
		t.Fatalf("InitCluster: %v", err)
	}

	if got := gotParams.Get("clusterDomain"); got != "cluster.internal" {
		t.Errorf("clusterDomain param = %q, want cluster.internal", got)
	}
	if got := gotParams.Get("primaryNodeIpAddresses"); got != "10.0.0.1" {
		t.Errorf("primaryNodeIpAddresses param = %q, want 10.0.0.1", got)
	}
	if !state.ClusterInitialized {
		t.Error("ClusterInitialized = false, want true")
	}
	if state.ClusterDomain != "cluster.internal" {
		t.Errorf("ClusterDomain = %q, want cluster.internal", state.ClusterDomain)
	}
	if len(state.Nodes) != 1 {
		t.Fatalf("len(Nodes) = %d, want 1", len(state.Nodes))
	}
}

func TestInitClusterAlreadyInitialized(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorMessage":"Cluster is already initialized on this node."}`))
	})

	_, err := c.InitCluster(context.Background(), InitClusterOptions{
		ClusterDomain:          "cluster.internal",
		PrimaryNodeIPAddresses: []string{"10.0.0.1"},
	})
	if !errors.Is(err, ErrClusterAlreadyInitialized) {
		t.Fatalf("err = %v, want errors.Is ErrClusterAlreadyInitialized", err)
	}
}

func TestInitJoinCluster(t *testing.T) {
	var gotParams url.Values
	body := `{"response":{"version":"13.4.0","dnsServerDomain":"dns-1.cluster.internal",` +
		`"clusterInitialized":true,"clusterDomain":"cluster.internal","clusterNodes":[` +
		`{"id":2,"name":"dns-1.cluster.internal","url":"https://dns-1.cluster.internal:53443",` +
		`"ipAddresses":["10.0.0.2"],"type":"Secondary","state":"Self"}]},"status":"ok"}`

	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		gotParams = r.URL.Query()
		_, _ = w.Write([]byte(body))
	})

	state, err := c.InitJoinCluster(context.Background(), InitJoinOptions{
		SecondaryNodeIPAddresses: []string{"10.0.0.2"},
		PrimaryNodeURL:           "https://dns-0.cluster.internal:53443/",
		PrimaryNodeUsername:      "admin",
		PrimaryNodePassword:      "s3cret",
		PrimaryNodeIPAddress:     "10.0.0.1",
		IgnoreCertificateErrors:  true,
	})
	if err != nil {
		t.Fatalf("InitJoinCluster: %v", err)
	}

	wantParams := map[string]string{
		"secondaryNodeIpAddresses": "10.0.0.2",
		"primaryNodeUrl":           "https://dns-0.cluster.internal:53443/",
		"primaryNodeUsername":      "admin",
		"primaryNodePassword":      "s3cret",
		"primaryNodeIpAddress":     "10.0.0.1",
		"ignoreCertificateErrors":  "true",
	}
	for key, want := range wantParams {
		if got := gotParams.Get(key); got != want {
			t.Errorf("%s param = %q, want %q", key, got, want)
		}
	}
	if !state.ClusterInitialized {
		t.Error("ClusterInitialized = false, want true")
	}
}

func TestInitJoinClusterAlreadyInitialized(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorMessage":"This node is already initialized as a Secondary."}`))
	})

	_, err := c.InitJoinCluster(context.Background(), InitJoinOptions{
		SecondaryNodeIPAddresses: []string{"10.0.0.2"},
		PrimaryNodeURL:           "https://dns-0.cluster.internal:53443/",
		PrimaryNodeUsername:      "admin",
		PrimaryNodePassword:      "s3cret",
		PrimaryNodeIPAddress:     "10.0.0.1",
		IgnoreCertificateErrors:  true,
	})
	if !errors.Is(err, ErrClusterAlreadyInitialized) {
		t.Fatalf("err = %v, want errors.Is ErrClusterAlreadyInitialized", err)
	}
}

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
	"testing"
)

const (
	testScopeName  = "test-scope"
	scopeNotFound  = `{"status":"error","errorMessage":"No such scope was found: test-scope"}`
	leaseNotFound  = `{"status":"error","errorMessage":"No reservation was found for this hardware address"}`
	testScopeStart = "192.168.1.100"
	testScopeEnd   = "192.168.1.200"
	testScopeMask  = "255.255.255.0"
)

func TestListDHCPScopes(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dhcp/scopes/list" {
			t.Errorf("path = %q, want /api/dhcp/scopes/list", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","response":{"scopes":[
			{"name":"test-scope","enabled":true,"startingAddress":"192.168.1.100","endingAddress":"192.168.1.200","subnetMask":"255.255.255.0"}
		]}}`))
	})

	scopes, err := c.ListDHCPScopes(context.Background())
	if err != nil {
		t.Fatalf("ListDHCPScopes: %v", err)
	}
	if len(scopes) != 1 || scopes[0].Name != testScopeName {
		t.Fatalf("scopes = %+v, want one scope named %q", scopes, testScopeName)
	}
	if !scopes[0].Enabled {
		t.Error("expected scope to be enabled")
	}
}

func TestGetDHCPScope(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dhcp/scopes/get" {
			t.Errorf("path = %q, want /api/dhcp/scopes/get", r.URL.Path)
		}
		if got := r.URL.Query().Get("name"); got != testScopeName {
			t.Errorf("name param = %q, want %q", got, testScopeName)
		}
		_, _ = w.Write([]byte(`{"status":"ok","response":{
			"name":"test-scope","enabled":true,
			"startingAddress":"192.168.1.100","endingAddress":"192.168.1.200","subnetMask":"255.255.255.0",
			"routerAddress":"192.168.1.1","dnsServers":["1.1.1.1","8.8.8.8"],
			"reservedLeases":[{"hardwareAddress":"00:11:22:33:44:55","address":"192.168.1.150","hostName":"printer","comments":"office"}]
		}}`))
	})

	details, err := c.GetDHCPScope(context.Background(), testScopeName)
	if err != nil {
		t.Fatalf("GetDHCPScope: %v", err)
	}
	if details.RouterAddress != "192.168.1.1" {
		t.Errorf("routerAddress = %q, want 192.168.1.1", details.RouterAddress)
	}
	if len(details.DNSServers) != 2 {
		t.Fatalf("dnsServers = %v, want 2 entries", details.DNSServers)
	}
	if len(details.ReservedLeases) != 1 || details.ReservedLeases[0].HardwareAddress != "00:11:22:33:44:55" {
		t.Fatalf("reservedLeases = %+v, want one lease for 00:11:22:33:44:55", details.ReservedLeases)
	}
}

func TestGetDHCPScopeNotFound(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(scopeNotFound))
	})

	_, err := c.GetDHCPScope(context.Background(), testScopeName)
	if !errors.Is(err, ErrDHCPScopeNotFound) {
		t.Fatalf("err = %v, want errors.Is ErrDHCPScopeNotFound", err)
	}
}

func TestSetDHCPScope(t *testing.T) {
	routerAddress := "192.168.1.1"
	dnsServers := []string{"1.1.1.1", "8.8.8.8"}
	leaseDays := int32(1)
	domainName := "example.com"

	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dhcp/scopes/set" {
			t.Errorf("path = %q, want /api/dhcp/scopes/set", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("name"); got != testScopeName {
			t.Errorf("name = %q, want %q", got, testScopeName)
		}
		if got := q.Get("startingAddress"); got != testScopeStart {
			t.Errorf("startingAddress = %q, want %q", got, testScopeStart)
		}
		if got := q.Get("endingAddress"); got != testScopeEnd {
			t.Errorf("endingAddress = %q, want %q", got, testScopeEnd)
		}
		if got := q.Get("subnetMask"); got != testScopeMask {
			t.Errorf("subnetMask = %q, want %q", got, testScopeMask)
		}
		if got := q.Get("routerAddress"); got != routerAddress {
			t.Errorf("routerAddress = %q, want %q", got, routerAddress)
		}
		if got := q.Get("dnsServers"); got != "1.1.1.1,8.8.8.8" {
			t.Errorf("dnsServers = %q, want comma-joined list", got)
		}
		if got := q.Get("leaseTimeDays"); got != "1" {
			t.Errorf("leaseTimeDays = %q, want 1", got)
		}
		if got := q.Get("domainName"); got != domainName {
			t.Errorf("domainName = %q, want %q", got, domainName)
		}
		_, _ = w.Write([]byte(statusOK))
	})

	opts := SetDHCPScopeOptions{
		Name: testScopeName, StartingAddress: testScopeStart, EndingAddress: testScopeEnd, SubnetMask: testScopeMask,
		RouterAddress: &routerAddress, DNSServers: &dnsServers, LeaseTimeDays: &leaseDays, DomainName: &domainName,
	}
	if err := c.SetDHCPScope(context.Background(), opts); err != nil {
		t.Fatalf("SetDHCPScope: %v", err)
	}
}

func TestSetDHCPScopeRequiresCoreFields(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(statusOK))
	})

	tests := []SetDHCPScopeOptions{
		{StartingAddress: testScopeStart, EndingAddress: testScopeEnd, SubnetMask: testScopeMask},
		{Name: testScopeName, EndingAddress: testScopeEnd, SubnetMask: testScopeMask},
		{Name: testScopeName, StartingAddress: testScopeStart, SubnetMask: testScopeMask},
		{Name: testScopeName, StartingAddress: testScopeStart, EndingAddress: testScopeEnd},
	}
	for _, opts := range tests {
		if err := c.SetDHCPScope(context.Background(), opts); err == nil {
			t.Errorf("SetDHCPScope(%+v): expected error for missing required field", opts)
		}
	}
}

func TestEnableDisableDeleteDHCPScope(t *testing.T) {
	tests := []struct {
		name     string
		call     func(c *Client) error
		wantPath string
	}{
		{name: "enable", call: func(c *Client) error { return c.EnableDHCPScope(context.Background(), testScopeName) }, wantPath: "/api/dhcp/scopes/enable"},
		{name: "disable", call: func(c *Client) error { return c.DisableDHCPScope(context.Background(), testScopeName) }, wantPath: "/api/dhcp/scopes/disable"},
		{name: "delete", call: func(c *Client) error { return c.DeleteDHCPScope(context.Background(), testScopeName) }, wantPath: "/api/dhcp/scopes/delete"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.wantPath {
					t.Errorf("path = %q, want %q", r.URL.Path, tt.wantPath)
				}
				if got := r.URL.Query().Get("name"); got != testScopeName {
					t.Errorf("name param = %q, want %q", got, testScopeName)
				}
				_, _ = w.Write([]byte(statusOK))
			})

			if err := tt.call(c); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
		})
	}
}

func TestDeleteDHCPScopeNotFound(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(scopeNotFound))
	})

	if err := c.DeleteDHCPScope(context.Background(), testScopeName); !errors.Is(err, ErrDHCPScopeNotFound) {
		t.Fatalf("err = %v, want errors.Is ErrDHCPScopeNotFound", err)
	}
}

func TestScopeEndpointsRequireName(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(statusOK))
	})

	if _, err := c.GetDHCPScope(context.Background(), ""); err == nil {
		t.Error("expected error for empty name on GetDHCPScope")
	}
	if err := c.EnableDHCPScope(context.Background(), ""); err == nil {
		t.Error("expected error for empty name on EnableDHCPScope")
	}
	if err := c.DisableDHCPScope(context.Background(), ""); err == nil {
		t.Error("expected error for empty name on DisableDHCPScope")
	}
	if err := c.DeleteDHCPScope(context.Background(), ""); err == nil {
		t.Error("expected error for empty name on DeleteDHCPScope")
	}
}

func TestAddReservedLease(t *testing.T) {
	hostName := "printer"
	comments := "office"

	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dhcp/scopes/addReservedLease" {
			t.Errorf("path = %q, want /api/dhcp/scopes/addReservedLease", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("name"); got != testScopeName {
			t.Errorf("name = %q, want %q", got, testScopeName)
		}
		if got := q.Get("hardwareAddress"); got != "00:11:22:33:44:55" {
			t.Errorf("hardwareAddress = %q, want 00:11:22:33:44:55", got)
		}
		if got := q.Get("ipAddress"); got != "192.168.1.150" {
			t.Errorf("ipAddress = %q, want 192.168.1.150", got)
		}
		if got := q.Get("hostName"); got != hostName {
			t.Errorf("hostName = %q, want %q", got, hostName)
		}
		if got := q.Get("comments"); got != comments {
			t.Errorf("comments = %q, want %q", got, comments)
		}
		_, _ = w.Write([]byte(statusOK))
	})

	opts := AddReservedLeaseOptions{
		ScopeName: testScopeName, HardwareAddress: "00:11:22:33:44:55", IPAddress: "192.168.1.150",
		HostName: &hostName, Comments: &comments,
	}
	if err := c.AddReservedLease(context.Background(), opts); err != nil {
		t.Fatalf("AddReservedLease: %v", err)
	}
}

func TestAddReservedLeaseRequiresFields(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(statusOK))
	})

	tests := []AddReservedLeaseOptions{
		{HardwareAddress: "00:11:22:33:44:55", IPAddress: "192.168.1.150"},
		{ScopeName: testScopeName, IPAddress: "192.168.1.150"},
		{ScopeName: testScopeName, HardwareAddress: "00:11:22:33:44:55"},
	}
	for _, opts := range tests {
		if err := c.AddReservedLease(context.Background(), opts); err == nil {
			t.Errorf("AddReservedLease(%+v): expected error for missing required field", opts)
		}
	}
}

func TestRemoveReservedLease(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dhcp/scopes/removeReservedLease" {
			t.Errorf("path = %q, want /api/dhcp/scopes/removeReservedLease", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("name"); got != testScopeName {
			t.Errorf("name = %q, want %q", got, testScopeName)
		}
		if got := q.Get("hardwareAddress"); got != "00:11:22:33:44:55" {
			t.Errorf("hardwareAddress = %q, want 00:11:22:33:44:55", got)
		}
		_, _ = w.Write([]byte(statusOK))
	})

	if err := c.RemoveReservedLease(context.Background(), testScopeName, "00:11:22:33:44:55"); err != nil {
		t.Fatalf("RemoveReservedLease: %v", err)
	}
}

func TestRemoveReservedLeaseNotFound(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(leaseNotFound))
	})

	err := c.RemoveReservedLease(context.Background(), testScopeName, "00:11:22:33:44:55")
	if !errors.Is(err, ErrDHCPReservationNotFound) {
		t.Fatalf("err = %v, want errors.Is ErrDHCPReservationNotFound", err)
	}
}

func TestRemoveReservedLeaseRequiresFields(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(statusOK))
	})

	if err := c.RemoveReservedLease(context.Background(), "", "00:11:22:33:44:55"); err == nil {
		t.Error("expected error for empty scope name")
	}
	if err := c.RemoveReservedLease(context.Background(), testScopeName, ""); err == nil {
		t.Error("expected error for empty hardware address")
	}
}

/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// The DHCP API is a separate subsystem from DNS on Technitium (/api/dhcp/*
// rather than /api/zones/* or /api/settings/*). The endpoint paths, query
// parameter names, and response field names below follow the same naming
// convention Technitium uses across its DNS API (APIDOCS.md), but unlike the
// DNS endpoints they have not been confirmed against a live server; the e2e
// suite exercising a real instance is the actual signal for whether this
// contract is right.

// DHCPScopeInfo is one entry from /api/dhcp/scopes/list.
type DHCPScopeInfo struct {
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	StartingAddress string `json:"startingAddress"`
	EndingAddress   string `json:"endingAddress"`
	SubnetMask      string `json:"subnetMask"`
}

// DHCPReservedLease is one static reservation as returned by
// /api/dhcp/scopes/get.
type DHCPReservedLease struct {
	HardwareAddress string `json:"hardwareAddress"`
	Address         string `json:"address"`
	HostName        string `json:"hostName,omitempty"`
	Comments        string `json:"comments,omitempty"`
}

// DHCPScopeDetails is the full scope object returned by
// /api/dhcp/scopes/get.
type DHCPScopeDetails struct {
	Name            string   `json:"name"`
	Enabled         bool     `json:"enabled"`
	StartingAddress string   `json:"startingAddress"`
	EndingAddress   string   `json:"endingAddress"`
	SubnetMask      string   `json:"subnetMask"`
	RouterAddress   string   `json:"routerAddress,omitempty"`
	DNSServers      []string `json:"dnsServers,omitempty"`

	ReservedLeases []DHCPReservedLease `json:"reservedLeases,omitempty"`
}

// SetDHCPScopeOptions describes a scope to create or update. Fields map to the
// query parameters of /api/dhcp/scopes/set. Setting a scope that does not yet
// exist creates it, so this one call covers both create and update.
type SetDHCPScopeOptions struct {
	// Name is the scope's name, required.
	Name string
	// StartingAddress is the first IP address in the range, required.
	StartingAddress string
	// EndingAddress is the last IP address in the range, required.
	EndingAddress string
	// SubnetMask is the subnet mask for the range, required.
	SubnetMask string

	// RouterAddress is the default gateway offered to clients. Sent only when
	// non-nil.
	RouterAddress *string
	// DNSServers is the set of DNS servers offered to clients, comma-joined
	// for the request. Sent only when non-nil.
	DNSServers *[]string
	// LeaseTimeDays, LeaseTimeHours, and LeaseTimeMinutes together set the
	// lease duration. Each is sent only when non-nil.
	LeaseTimeDays    *int32
	LeaseTimeHours   *int32
	LeaseTimeMinutes *int32
	// DomainName is the DNS domain offered to clients. Sent only when
	// non-nil.
	DomainName *string
}

// AddReservedLeaseOptions describes a static reservation to add. Fields map to
// the query parameters of /api/dhcp/scopes/addReservedLease.
type AddReservedLeaseOptions struct {
	// ScopeName is the scope the reservation belongs to, required.
	ScopeName string
	// HardwareAddress is the client's MAC address, required.
	HardwareAddress string
	// IPAddress is the address reserved for HardwareAddress, required.
	IPAddress string
	// HostName is the client host name associated with the reservation. Sent
	// only when non-nil.
	HostName *string
	// Comments is a free-form note about the reservation. Sent only when
	// non-nil.
	Comments *string
}

// dhcpScopeNameParam builds the query carrying the required scope name, or an
// error when it is empty. Every scope-scoped endpoint starts from this.
func dhcpScopeNameParam(name string) (url.Values, error) {
	if name == "" {
		return nil, errors.New("technitium: dhcp scope name is required")
	}
	params := url.Values{}
	params.Set("name", name)
	return params, nil
}

// ListDHCPScopes fetches the summary of every DHCP scope configured on the
// server.
func (c *Client) ListDHCPScopes(ctx context.Context) ([]DHCPScopeInfo, error) {
	var resp struct {
		Scopes []DHCPScopeInfo `json:"scopes"`
	}
	if err := c.do(ctx, "/api/dhcp/scopes/list", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Scopes, nil
}

// GetDHCPScope fetches the full configuration, including reserved leases, of
// the named scope. Returns ErrDHCPScopeNotFound when no such scope exists.
func (c *Client) GetDHCPScope(ctx context.Context, name string) (*DHCPScopeDetails, error) {
	params, err := dhcpScopeNameParam(name)
	if err != nil {
		return nil, err
	}

	var details DHCPScopeDetails
	if err := c.do(ctx, "/api/dhcp/scopes/get", params, &details); err != nil {
		return nil, err
	}
	return &details, nil
}

// SetDHCPScope creates the scope when absent or updates it in place when
// present, applying every field of opts that Technitium's set endpoint
// accepts.
func (c *Client) SetDHCPScope(ctx context.Context, opts SetDHCPScopeOptions) error {
	if opts.Name == "" {
		return errors.New("technitium: dhcp scope name is required")
	}
	if opts.StartingAddress == "" || opts.EndingAddress == "" || opts.SubnetMask == "" {
		return errors.New("technitium: dhcp scope startingAddress, endingAddress, and subnetMask are required")
	}

	params := url.Values{}
	params.Set("name", opts.Name)
	params.Set("startingAddress", opts.StartingAddress)
	params.Set("endingAddress", opts.EndingAddress)
	params.Set("subnetMask", opts.SubnetMask)

	if opts.RouterAddress != nil {
		params.Set("routerAddress", *opts.RouterAddress)
	}
	if opts.DNSServers != nil {
		params.Set("dnsServers", strings.Join(*opts.DNSServers, ","))
	}
	if opts.LeaseTimeDays != nil {
		params.Set("leaseTimeDays", strconv.Itoa(int(*opts.LeaseTimeDays)))
	}
	if opts.LeaseTimeHours != nil {
		params.Set("leaseTimeHours", strconv.Itoa(int(*opts.LeaseTimeHours)))
	}
	if opts.LeaseTimeMinutes != nil {
		params.Set("leaseTimeMinutes", strconv.Itoa(int(*opts.LeaseTimeMinutes)))
	}
	if opts.DomainName != nil {
		params.Set("domainName", *opts.DomainName)
	}

	return c.do(ctx, "/api/dhcp/scopes/set", params, nil)
}

// EnableDHCPScope enables the named scope.
func (c *Client) EnableDHCPScope(ctx context.Context, name string) error {
	params, err := dhcpScopeNameParam(name)
	if err != nil {
		return err
	}
	return c.do(ctx, "/api/dhcp/scopes/enable", params, nil)
}

// DisableDHCPScope disables the named scope.
func (c *Client) DisableDHCPScope(ctx context.Context, name string) error {
	params, err := dhcpScopeNameParam(name)
	if err != nil {
		return err
	}
	return c.do(ctx, "/api/dhcp/scopes/disable", params, nil)
}

// DeleteDHCPScope removes the named scope from the server. Returns
// ErrDHCPScopeNotFound when the scope does not exist, letting callers treat
// deletion as idempotent.
func (c *Client) DeleteDHCPScope(ctx context.Context, name string) error {
	params, err := dhcpScopeNameParam(name)
	if err != nil {
		return err
	}
	return c.do(ctx, "/api/dhcp/scopes/delete", params, nil)
}

// AddReservedLease adds a static MAC-to-IP reservation to a scope.
func (c *Client) AddReservedLease(ctx context.Context, opts AddReservedLeaseOptions) error {
	if opts.ScopeName == "" {
		return errors.New("technitium: dhcp scope name is required")
	}
	if opts.HardwareAddress == "" {
		return errors.New("technitium: hardwareAddress is required")
	}
	if opts.IPAddress == "" {
		return errors.New("technitium: ipAddress is required")
	}

	params := url.Values{}
	params.Set("name", opts.ScopeName)
	params.Set("hardwareAddress", opts.HardwareAddress)
	params.Set("ipAddress", opts.IPAddress)
	if opts.HostName != nil {
		params.Set("hostName", *opts.HostName)
	}
	if opts.Comments != nil {
		params.Set("comments", *opts.Comments)
	}

	return c.do(ctx, "/api/dhcp/scopes/addReservedLease", params, nil)
}

// RemoveReservedLease removes a scope's reservation for hardwareAddress.
// Returns ErrDHCPReservationNotFound when no such reservation exists, letting
// callers treat deletion as idempotent.
func (c *Client) RemoveReservedLease(ctx context.Context, scopeName, hardwareAddress string) error {
	if scopeName == "" {
		return errors.New("technitium: dhcp scope name is required")
	}
	if hardwareAddress == "" {
		return errors.New("technitium: hardwareAddress is required")
	}

	params := url.Values{}
	params.Set("name", scopeName)
	params.Set("hardwareAddress", hardwareAddress)

	return c.do(ctx, "/api/dhcp/scopes/removeReservedLease", params, nil)
}

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
	"time"
)

// ClusterNodeType is the role a node plays in a Technitium cluster.
const (
	ClusterNodeTypePrimary   = "Primary"
	ClusterNodeTypeSecondary = "Secondary"
)

// ClusterNodeState is the membership state of a node as seen from the node the
// state was read from. "Self" marks the node answering the request.
const clusterNodeStateSelf = "Self"

// ClusterNodeInfo is a single entry from the clusterNodes array of
// /api/admin/cluster/state.
type ClusterNodeInfo struct {
	ID               int       `json:"id"`
	Name             string    `json:"name"`
	URL              string    `json:"url"`
	IPAddresses      []string  `json:"ipAddresses"`
	Type             string    `json:"type"`
	State            string    `json:"state"`
	UpSince          time.Time `json:"upSince"`
	LastSeen         time.Time `json:"lastSeen"`
	ConfigLastSynced time.Time `json:"configLastSynced"`
}

// IsSelf reports whether this entry describes the node the state was read from.
func (n ClusterNodeInfo) IsSelf() bool {
	return strings.EqualFold(n.State, clusterNodeStateSelf)
}

// ClusterState is the response of /api/admin/cluster/state. When
// Initialized is false the cluster fields are zero: the node is standalone.
type ClusterState struct {
	Version         string `json:"version"`
	DNSServerDomain string `json:"dnsServerDomain"`
	// Initialized reports whether this node is a member of a cluster. It is the
	// idempotency signal for reconciling: an initialized node has already run
	// init or initJoin and must not be re-initialized.
	Initialized bool `json:"clusterInitialized"`
	// Domain is the cluster's domain name, set only when Initialized.
	Domain string `json:"clusterDomain"`
	// Nodes lists cluster membership as this node sees it, set only when
	// Initialized.
	Nodes []ClusterNodeInfo `json:"clusterNodes"`
}

// SelfNode returns the entry describing the node the state was read from, or nil
// when the node is not a cluster member.
func (s *ClusterState) SelfNode() *ClusterNodeInfo {
	for i := range s.Nodes {
		if s.Nodes[i].IsSelf() {
			return &s.Nodes[i]
		}
	}
	return nil
}

// ClusterInitOptions describes the primary node to initialize. Fields map to
// the query parameters of /api/admin/cluster/init.
type ClusterInitOptions struct {
	// ClusterDomain is the DNS name of the cluster, required.
	ClusterDomain string
	// PrimaryNodeIPAddresses are the primary node's own reachable addresses,
	// required. Secondaries use these to contact the primary.
	PrimaryNodeIPAddresses []string
}

// ClusterInitJoinOptions describes how a secondary node joins an existing
// cluster. The call runs against the secondary itself, which then contacts the
// primary using the supplied primary credentials. Fields map to the query
// parameters of /api/admin/cluster/initJoin.
type ClusterInitJoinOptions struct {
	// SecondaryNodeIPAddresses are the joining node's own reachable addresses,
	// required.
	SecondaryNodeIPAddresses []string
	// PrimaryNodeURL is the base URL of the primary node's API, required. It must
	// use the primary's domain name, not an IP address.
	PrimaryNodeURL string
	// PrimaryNodeIPAddress optionally pins the primary's address when its domain
	// name cannot yet be resolved by the secondary.
	PrimaryNodeIPAddress string
	// PrimaryNodeUsername and PrimaryNodePassword authenticate to the primary.
	// The account must be a local administrator: the server rejects SSO accounts
	// for cluster initialization.
	PrimaryNodeUsername string
	PrimaryNodePassword string
	// PrimaryNodeTOTP is the current time-based one-time code when the primary
	// account has two-factor authentication enabled.
	PrimaryNodeTOTP string
	// IgnoreCertificateErrors skips TLS verification of the primary, needed when
	// the primary presents the self-signed certificate that clustering enables.
	IgnoreCertificateErrors bool
}

// ClusterState reads the current cluster membership and role of the server this
// client points at.
func (c *Client) ClusterState(ctx context.Context) (*ClusterState, error) {
	var state ClusterState
	if err := c.do(ctx, "/api/admin/cluster/state", nil, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// ClusterInit initializes the server this client points at as the primary node
// of a new cluster. It returns ErrClusterAlreadyInitialized when the node is
// already a cluster member, letting callers treat initialization as idempotent.
func (c *Client) ClusterInit(ctx context.Context, opts ClusterInitOptions) (*ClusterState, error) {
	if opts.ClusterDomain == "" {
		return nil, errors.New("technitium: cluster domain is required")
	}
	if len(opts.PrimaryNodeIPAddresses) == 0 {
		return nil, errors.New("technitium: at least one primary node IP address is required")
	}

	params := url.Values{}
	params.Set("clusterDomain", opts.ClusterDomain)
	params.Set("primaryNodeIpAddresses", strings.Join(opts.PrimaryNodeIPAddresses, ","))

	var state ClusterState
	if err := c.do(ctx, "/api/admin/cluster/init", params, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// ClusterInitJoin joins the server this client points at to an existing cluster
// as a secondary node. It returns ErrClusterAlreadyInitialized when the node is
// already a member, and ErrClusterNotInitialized when the primary has no cluster
// formed yet, letting callers make the join idempotent and order-aware.
func (c *Client) ClusterInitJoin(ctx context.Context, opts ClusterInitJoinOptions) (*ClusterState, error) {
	if len(opts.SecondaryNodeIPAddresses) == 0 {
		return nil, errors.New("technitium: at least one secondary node IP address is required")
	}
	if opts.PrimaryNodeURL == "" {
		return nil, errors.New("technitium: primary node URL is required")
	}
	if opts.PrimaryNodeUsername == "" {
		return nil, errors.New("technitium: primary node username is required")
	}

	params := url.Values{}
	params.Set("secondaryNodeIpAddresses", strings.Join(opts.SecondaryNodeIPAddresses, ","))
	params.Set("primaryNodeUrl", opts.PrimaryNodeURL)
	params.Set("primaryNodeUsername", opts.PrimaryNodeUsername)
	params.Set("primaryNodePassword", opts.PrimaryNodePassword)
	if opts.PrimaryNodeIPAddress != "" {
		params.Set("primaryNodeIpAddress", opts.PrimaryNodeIPAddress)
	}
	if opts.PrimaryNodeTOTP != "" {
		params.Set("primaryNodeTotp", opts.PrimaryNodeTOTP)
	}
	if opts.IgnoreCertificateErrors {
		params.Set("ignoreCertificateErrors", strconv.FormatBool(true))
	}

	var state ClusterState
	if err := c.do(ctx, "/api/admin/cluster/initJoin", params, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

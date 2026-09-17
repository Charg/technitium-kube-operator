/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ClusterNode is a single member reported by /api/admin/cluster/state once a
// cluster is initialized. Type distinguishes the Primary (the node the
// cluster was initialized on) from Secondary members; State reflects the
// requesting node's own view of that member's reachability, so "Self" only
// ever appears for the node the request was made against.
type ClusterNode struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	IPAddresses []string `json:"ipAddresses"`
	Type        string   `json:"type"`
	State       string   `json:"state"`
	// UpSince, LastSeen, and ConfigLastSynced are pointers because Technitium
	// omits them entirely for a node that has never come up (a Secondary that
	// initJoin'd but has not yet synced), rather than sending a zero time.
	UpSince          *time.Time `json:"upSince,omitempty"`
	LastSeen         *time.Time `json:"lastSeen,omitempty"`
	ConfigLastSynced *time.Time `json:"configLastSynced,omitempty"`
}

// ClusterState is the response of /api/admin/cluster/state. ClusterDomain and
// Nodes are only meaningful when ClusterInitialized is true: an uninitialized
// node reports just Version, DNSServerDomain, and ClusterInitialized=false.
type ClusterState struct {
	Version            string        `json:"version"`
	DNSServerDomain    string        `json:"dnsServerDomain"`
	ClusterInitialized bool          `json:"clusterInitialized"`
	ClusterDomain      string        `json:"clusterDomain"`
	Nodes              []ClusterNode `json:"clusterNodes"`
}

// GetClusterState reports the calling node's own view of cluster membership.
// It succeeds against any node, initialized or not: an uninitialized node
// simply reports ClusterInitialized=false with no Nodes, which is how the
// controller tells "not yet clustered" apart from a transport failure.
func (c *Client) GetClusterState(ctx context.Context) (*ClusterState, error) {
	var state ClusterState
	if err := c.do(ctx, "/api/admin/cluster/state", nil, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// InitClusterOptions describes a cluster init request, run once against the
// node that becomes the Primary.
type InitClusterOptions struct {
	// ClusterDomain is the domain every node's hostname is joined with to form
	// its cluster node name ("<hostname>.<clusterDomain>"). It need not be
	// resolvable DNS: nodes address each other by IP, not by this name.
	ClusterDomain string
	// PrimaryNodeIPAddresses are the IP addresses other nodes use to reach this
	// Primary. In practice this is the pod's own IP.
	PrimaryNodeIPAddresses []string
}

// InitCluster initializes a new Technitium cluster on the calling node,
// making it the Primary. Calling it again on a node already initialized
// fails with ErrClusterAlreadyInitialized, which callers can treat as
// success for idempotent reconciliation.
func (c *Client) InitCluster(ctx context.Context, opts InitClusterOptions) (*ClusterState, error) {
	params := url.Values{}
	params.Set("clusterDomain", opts.ClusterDomain)
	params.Set("primaryNodeIpAddresses", strings.Join(opts.PrimaryNodeIPAddresses, ","))

	var state ClusterState
	if err := c.do(ctx, "/api/admin/cluster/init", params, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// InitJoinOptions describes a cluster join request, run against each node
// that becomes a Secondary. The primary fields identify the already-
// initialized Primary this node is joining.
type InitJoinOptions struct {
	// SecondaryNodeIPAddresses are the IP addresses other nodes use to reach
	// this Secondary. In practice this is the pod's own IP.
	SecondaryNodeIPAddresses []string
	// PrimaryNodeURL is the Primary's own node URL (its domain name, not an
	// IP), as reported in its cluster state. Technitium stores this URL as
	// the node's address in the cluster config, so it must be the domain form
	// even though PrimaryNodeIPAddress below is what is actually dialed.
	PrimaryNodeURL string
	// PrimaryNodeUsername and PrimaryNodePassword are the Primary's local
	// admin credentials, used once to authenticate the join.
	PrimaryNodeUsername string
	PrimaryNodePassword string
	// PrimaryNodeIPAddress is the address actually dialed to reach the
	// Primary. Passing it lets a join succeed against a PrimaryNodeURL whose
	// domain has no real DNS, which is the case for every node name the
	// operator assigns.
	PrimaryNodeIPAddress string
	// IgnoreCertificateErrors skips TLS certificate validation on the join
	// call to the Primary's inter-node port. It is required here because the
	// Primary's certificate has no SAN matching its unresolvable cluster
	// domain name.
	IgnoreCertificateErrors bool
}

// InitJoinCluster joins the calling node to an already-initialized cluster as
// a Secondary. Calling it again on a node already joined fails with
// ErrClusterAlreadyInitialized, which callers can treat as success for
// idempotent reconciliation. Joining before the Primary itself has run
// InitCluster fails with a distinct server error ("the Primary node does not
// have a Cluster initialized"), which callers must order around rather than
// treat as a sentinel.
func (c *Client) InitJoinCluster(ctx context.Context, opts InitJoinOptions) (*ClusterState, error) {
	params := url.Values{}
	params.Set("secondaryNodeIpAddresses", strings.Join(opts.SecondaryNodeIPAddresses, ","))
	params.Set("primaryNodeUrl", opts.PrimaryNodeURL)
	params.Set("primaryNodeUsername", opts.PrimaryNodeUsername)
	params.Set("primaryNodePassword", opts.PrimaryNodePassword)
	params.Set("primaryNodeIpAddress", opts.PrimaryNodeIPAddress)
	params.Set("ignoreCertificateErrors", strconv.FormatBool(opts.IgnoreCertificateErrors))

	var state ClusterState
	if err := c.do(ctx, "/api/admin/cluster/initJoin", params, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

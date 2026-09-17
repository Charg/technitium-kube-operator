/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
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

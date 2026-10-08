/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"net/url"
)

// DashboardStats is the "stats" object of /api/dashboard/stats/get: the
// counters the web console dashboard shows. The Total* query counters are
// aggregates over the requested window (they rise and fall as the window
// slides), not monotonic since-start counters. The zone and cache fields are
// point-in-time sizes.
type DashboardStats struct {
	TotalQueries       int64 `json:"totalQueries"`
	TotalNoError       int64 `json:"totalNoError"`
	TotalServerFailure int64 `json:"totalServerFailure"`
	TotalNxDomain      int64 `json:"totalNxDomain"`
	TotalRefused       int64 `json:"totalRefused"`
	TotalAuthoritative int64 `json:"totalAuthoritative"`
	TotalRecursive     int64 `json:"totalRecursive"`
	TotalCached        int64 `json:"totalCached"`
	TotalBlocked       int64 `json:"totalBlocked"`
	TotalDropped       int64 `json:"totalDropped"`
	TotalClients       int64 `json:"totalClients"`
	Zones              int64 `json:"zones"`
	CachedEntries      int64 `json:"cachedEntries"`
	AllowedZones       int64 `json:"allowedZones"`
	BlockedZones       int64 `json:"blockedZones"`
	AllowListZones     int64 `json:"allowListZones"`
	BlockListZones     int64 `json:"blockListZones"`
}

// GetDashboardStats reads the dashboard counters for the last hour from the
// node the client points at. No "node" parameter is sent, so the answer
// covers only that node's own traffic, never a cluster aggregate: the caller
// polls every node itself. The call needs the Dashboard: View permission.
func (c *Client) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	params := url.Values{}
	params.Set("type", "LastHour")

	var out struct {
		Stats DashboardStats `json:"stats"`
	}
	if err := c.do(ctx, "/api/dashboard/stats/get", params, &out); err != nil {
		return nil, err
	}
	return &out.Stats, nil
}

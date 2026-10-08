/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package technitium

import (
	"context"
	"net/http"
	"testing"
)

func TestGetDashboardStats(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dashboard/stats/get" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get(paramType); got != "LastHour" {
			t.Errorf("type = %q, want LastHour", got)
		}
		if r.URL.Query().Has("node") {
			t.Errorf("node parameter must not be sent")
		}
		_, _ = w.Write([]byte(`{"response":{"stats":{"totalQueries":925,"totalNoError":834,` +
			`"totalServerFailure":1,"totalNxDomain":90,"totalRefused":0,"totalAuthoritative":47,` +
			`"totalRecursive":348,"totalCached":481,"totalBlocked":49,"totalDropped":2,"totalClients":6,` +
			`"zones":19,"cachedEntries":6330,"allowedZones":10,"blockedZones":1,"allowListZones":0,` +
			`"blockListZones":307447},"mainChartData":{"labels":[]}},"status":"ok"}`))
	})

	got, err := c.GetDashboardStats(context.Background())
	if err != nil {
		t.Fatalf("GetDashboardStats: %v", err)
	}
	want := DashboardStats{
		TotalQueries: 925, TotalNoError: 834, TotalServerFailure: 1, TotalNxDomain: 90,
		TotalAuthoritative: 47, TotalRecursive: 348, TotalCached: 481, TotalBlocked: 49,
		TotalDropped: 2, TotalClients: 6, Zones: 19, CachedEntries: 6330, AllowedZones: 10,
		BlockedZones: 1, BlockListZones: 307447,
	}
	if *got != want {
		t.Errorf("stats = %+v, want %+v", *got, want)
	}
}

func TestGetDashboardStatsError(t *testing.T) {
	c := newTestClient(t, "t", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorMessage":"nope"}`))
	})
	if _, err := c.GetDashboardStats(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}

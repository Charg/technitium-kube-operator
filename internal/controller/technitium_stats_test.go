/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/technitium"
)

type fakeStatsNode struct {
	stats *technitium.DashboardStats
	err   error
}

func (f *fakeStatsNode) GetDashboardStats(context.Context) (*technitium.DashboardStats, error) {
	return f.stats, f.err
}

func statsTestCluster(name string, states ...string) *dnsv1alpha1.TechnitiumCluster {
	tc := &dnsv1alpha1.TechnitiumCluster{Name: name}
	for i, state := range states {
		tc.Status.Nodes = append(tc.Status.Nodes, dnsv1alpha1.TechnitiumClusterNodeStatus{
			Name:  name + "-" + string(rune('0'+i)),
			State: state,
		})
	}
	return tc
}

func statsTestSecret(name string, token string) *corev1.Secret {
	s := &corev1.Secret{Namespace: "ops", Name: adminSecretName(name)}
	if token != "" {
		s.Data = map[string][]byte{adminSecretTokenKey: []byte(token)}
	}
	return s
}

func TestTechnitiumStatsPoller(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	good := &fakeStatsNode{stats: &technitium.DashboardStats{
		TotalQueries: 100, TotalNoError: 80, TotalNxDomain: 20, TotalCached: 60, TotalBlocked: 5,
		TotalClients: 3, CachedEntries: 42, Zones: 7, BlockedZones: 2,
	}}
	nodes := map[string]*fakeStatsNode{"dns-0": good, "dns-1": {err: errors.New("boom")}}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		statsTestCluster("dns", "Self", "Connected"), statsTestSecret("dns", "tok"),
		statsTestCluster("fresh", "Ready"), statsTestSecret("fresh", ""),
		statsTestCluster("gone", "Ready"), statsTestSecret("gone", "tok"),
	).Build()

	reg := prometheus.NewRegistry()
	p, err := NewTechnitiumStatsPoller(c, "ops", time.Minute, reg)
	if err != nil {
		t.Fatal(err)
	}
	p.NewStatsClient = func(endpoint string, _ *corev1.Secret) (statsAPI, error) {
		switch endpoint {
		case nodeEndpoint("dns", 0, "ops"):
			return nodes["dns-0"], nil
		case nodeEndpoint("dns", 1, "ops"):
			return nodes["dns-1"], nil
		}
		return good, nil
	}
	m := p.metrics
	ctx := context.Background()

	p.poll(ctx)

	if got := testutil.ToFloat64(m.queries.WithLabelValues("dns", "dns-0", "no_error")); got != 80 {
		t.Errorf("no_error = %v, want 80", got)
	}
	if got := testutil.ToFloat64(m.queriesBySrc.WithLabelValues("dns", "dns-0", "cached")); got != 60 {
		t.Errorf("cached = %v, want 60", got)
	}
	if got := testutil.ToFloat64(m.cacheEntries.WithLabelValues("dns", "dns-0")); got != 42 {
		t.Errorf("cache entries = %v, want 42", got)
	}
	if got := testutil.ToFloat64(m.scrapeSuccess.WithLabelValues("dns", "dns-0")); got != 1 {
		t.Errorf("dns-0 success = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.scrapeSuccess.WithLabelValues("dns", "dns-1")); got != 0 {
		t.Errorf("dns-1 success = %v, want 0", got)
	}
	// Four result series each for dns-0 and gone-0; the failed node and the
	// cluster without a token publish no data.
	if got := testutil.CollectAndCount(m.queries); got != 8 {
		t.Errorf("queries series = %d, want 8", got)
	}
	if got := testutil.CollectAndCount(m.scrapeSuccess); got != 3 {
		t.Errorf("scrape_success series = %d, want 3", got)
	}

	// A node that starts failing loses its data but keeps scrape_success=0.
	nodes["dns-0"] = &fakeStatsNode{err: errors.New("down")}
	p.poll(ctx)
	if got := testutil.CollectAndCount(m.cacheEntries); got != 1 {
		t.Errorf("cache series after failure = %d, want 1 (gone-0 only)", got)
	}
	if got := testutil.ToFloat64(m.scrapeSuccess.WithLabelValues("dns", "dns-0")); got != 0 {
		t.Errorf("dns-0 success after failure = %v, want 0", got)
	}

	// Deleting a cluster removes every series for it.
	if err := c.Delete(ctx, statsTestCluster("gone")); err != nil {
		t.Fatal(err)
	}
	p.poll(ctx)
	if got := testutil.CollectAndCount(m.scrapeSuccess); got != 2 {
		t.Errorf("scrape_success series after delete = %d, want 2", got)
	}
	if got := testutil.CollectAndCount(m.cacheEntries); got != 0 {
		t.Errorf("cache series after delete = %d, want 0", got)
	}
}

func TestTechnitiumStatsPollerSkipsNotReadyNode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := dnsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		statsTestCluster("dns", nodeNotReadyState), statsTestSecret("dns", "tok")).Build()
	p, err := NewTechnitiumStatsPoller(c, "ops", time.Minute, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	p.NewStatsClient = func(string, *corev1.Secret) (statsAPI, error) {
		t.Error("a not-ready node must not be queried")
		return nil, errors.New("unexpected")
	}
	p.poll(context.Background())
	if got := testutil.ToFloat64(p.metrics.scrapeSuccess.WithLabelValues("dns", "dns-0")); got != 0 {
		t.Errorf("success = %v, want 0", got)
	}
}

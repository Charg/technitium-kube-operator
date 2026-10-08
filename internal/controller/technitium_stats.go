/*
Copyright (c) 2026 Chris

Licensed under the MIT License. See the LICENSE file in the project root for
the full license text.
*/

package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dnsv1alpha1 "github.com/charg/technitium-operator/api/v1alpha1"
	"github.com/charg/technitium-operator/internal/config"
	"github.com/charg/technitium-operator/internal/technitium"
)

const (
	// statsPollTimeout bounds one node's stats call so a single hung pod
	// cannot stall the whole polling pass.
	statsPollTimeout = 10 * time.Second

	labelCluster = "cluster"
	labelNode    = "node"
)

// statsAPI is the slice of the Technitium client the poller needs. It is a
// seam so tests can inject a fake node.
type statsAPI interface {
	GetDashboardStats(ctx context.Context) (*technitium.DashboardStats, error)
}

// technitiumStatsMetrics holds the gauges the poller maintains. Technitium
// has no Prometheus endpoint, so the operator polls each node's dashboard
// API and republishes the values here. The query counters are aggregates over
// Technitium's rolling last-hour window, so they are gauges that rise and
// fall, not monotonic counters: do not wrap them in rate().
type technitiumStatsMetrics struct {
	queries       *prometheus.GaugeVec
	queriesBySrc  *prometheus.GaugeVec
	clients       *prometheus.GaugeVec
	cacheEntries  *prometheus.GaugeVec
	zones         *prometheus.GaugeVec
	allowedZones  *prometheus.GaugeVec
	blockedZones  *prometheus.GaugeVec
	allowListSize *prometheus.GaugeVec
	blockListSize *prometheus.GaugeVec
	scrapeSuccess *prometheus.GaugeVec
	lastSuccess   *prometheus.GaugeVec
}

func newTechnitiumStatsMetrics() *technitiumStatsMetrics {
	base := []string{labelCluster, labelNode}
	gauge := func(name, help string, extra ...string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "technitium_dns_" + name, Help: help},
			append(append([]string{}, base...), extra...))
	}
	return &technitiumStatsMetrics{
		queries: gauge("queries_last_hour",
			"Queries answered by the node over Technitium's rolling last-hour window, by response code.", "result"),
		queriesBySrc: gauge("queries_by_source_last_hour",
			"Queries handled by the node over the last hour, by how they were answered (authoritative, recursive, cached, blocked, dropped).", "source"),
		clients:       gauge("clients_last_hour", "Distinct clients that queried the node over the last hour."),
		cacheEntries:  gauge("cache_entries", "Entries currently in the node's DNS cache."),
		zones:         gauge("zones", "Zones hosted by the node."),
		allowedZones:  gauge("allowed_zones", "Domains in the node's allowed zones."),
		blockedZones:  gauge("blocked_zones", "Domains in the node's blocked zones."),
		allowListSize: gauge("allow_list_zones", "Domains loaded from the node's allow lists."),
		blockListSize: gauge("block_list_zones", "Domains loaded from the node's block lists."),
		scrapeSuccess: gauge("stats_scrape_success", "1 if the operator's last attempt to read this node's dashboard stats succeeded, else 0."),
		lastSuccess: gauge("stats_last_success_timestamp_seconds",
			"Unix time of the operator's last successful read of this node's dashboard stats."),
	}
}

func (m *technitiumStatsMetrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{m.queries, m.queriesBySrc, m.clients, m.cacheEntries, m.zones,
		m.allowedZones, m.blockedZones, m.allowListSize, m.blockListSize, m.scrapeSuccess, m.lastSuccess}
}

// dataVecs are the series that carry stats values. They are cleared when a
// node cannot be read, so a dashboard never shows the last good numbers as if
// they were current; the scrape_success series stays to say why.
func (m *technitiumStatsMetrics) dataVecs() []*prometheus.GaugeVec {
	return []*prometheus.GaugeVec{m.queries, m.queriesBySrc, m.clients, m.cacheEntries, m.zones,
		m.allowedZones, m.blockedZones, m.allowListSize, m.blockListSize}
}

func (m *technitiumStatsMetrics) deleteData(cluster, node string) {
	for _, v := range m.dataVecs() {
		v.DeletePartialMatch(prometheus.Labels{labelCluster: cluster, labelNode: node})
	}
}

func (m *technitiumStatsMetrics) deleteNode(cluster, node string) {
	m.deleteData(cluster, node)
	m.scrapeSuccess.DeletePartialMatch(prometheus.Labels{labelCluster: cluster, labelNode: node})
	m.lastSuccess.DeletePartialMatch(prometheus.Labels{labelCluster: cluster, labelNode: node})
}

func (m *technitiumStatsMetrics) set(cluster, node string, s *technitium.DashboardStats) {
	l := prometheus.Labels{labelCluster: cluster, labelNode: node}
	with := func(k, v string) prometheus.Labels {
		return prometheus.Labels{labelCluster: cluster, labelNode: node, k: v}
	}
	for result, v := range map[string]int64{
		"no_error": s.TotalNoError, "server_failure": s.TotalServerFailure,
		"nx_domain": s.TotalNxDomain, "refused": s.TotalRefused,
	} {
		m.queries.With(with("result", result)).Set(float64(v))
	}
	for source, v := range map[string]int64{
		"authoritative": s.TotalAuthoritative, "recursive": s.TotalRecursive,
		"cached": s.TotalCached, "blocked": s.TotalBlocked, "dropped": s.TotalDropped,
	} {
		m.queriesBySrc.With(with("source", source)).Set(float64(v))
	}
	m.clients.With(l).Set(float64(s.TotalClients))
	m.cacheEntries.With(l).Set(float64(s.CachedEntries))
	m.zones.With(l).Set(float64(s.Zones))
	m.allowedZones.With(l).Set(float64(s.AllowedZones))
	m.blockedZones.With(l).Set(float64(s.BlockedZones))
	m.allowListSize.With(l).Set(float64(s.AllowListZones))
	m.blockListSize.With(l).Set(float64(s.BlockListZones))
	m.scrapeSuccess.With(l).Set(1)
	m.lastSuccess.With(l).SetToCurrentTime()
}

// TechnitiumStatsPoller is a manager Runnable that periodically reads each
// ready node's dashboard stats and publishes them as Prometheus gauges. It
// polls in the background rather than on scrape so scrapes stay fast and the
// load on Technitium is bounded by Interval, not by the number of scrapers.
type TechnitiumStatsPoller struct {
	// Client reads TechnitiumClusters and their admin Secrets.
	Client client.Reader
	// OperatorNamespace is where each cluster's admin Secret lives.
	OperatorNamespace string
	// Interval is the time between polling passes.
	Interval time.Duration
	// NewStatsClient builds a client for one node endpoint from the admin
	// Secret. It is a seam so tests can inject a fake node; production wiring
	// leaves it nil and a real token-authenticated client is built.
	NewStatsClient func(endpoint string, secret *corev1.Secret) (statsAPI, error)

	metrics *technitiumStatsMetrics
	// known is the set of cluster/node pairs that have series, so a pass can
	// delete the ones that have gone away. Only touched from the poll loop.
	known map[[2]string]struct{}
}

// NewTechnitiumStatsPoller builds a poller and registers its gauges with reg,
// normally controller-runtime's metrics.Registry.
func NewTechnitiumStatsPoller(c client.Reader, operatorNamespace string, interval time.Duration, reg prometheus.Registerer) (*TechnitiumStatsPoller, error) {
	m := newTechnitiumStatsMetrics()
	for _, col := range m.collectors() {
		if err := reg.Register(col); err != nil {
			return nil, fmt.Errorf("registering Technitium stats metrics: %w", err)
		}
	}
	return &TechnitiumStatsPoller{
		Client:            c,
		OperatorNamespace: operatorNamespace,
		Interval:          interval,
		metrics:           m,
		known:             map[[2]string]struct{}{},
	}, nil
}

// NeedLeaderElection makes the poller run only on the elected leader, so
// replicas of the operator do not each poll every node and export duplicates.
func (p *TechnitiumStatsPoller) NeedLeaderElection() bool { return true }

// Start polls until ctx is cancelled. It satisfies manager.Runnable.
func (p *TechnitiumStatsPoller) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("technitium-stats")
	log.Info("Starting Technitium stats poller", "interval", p.Interval.String())

	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		p.poll(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// poll runs one pass over every TechnitiumCluster.
func (p *TechnitiumStatsPoller) poll(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("technitium-stats")

	var list dnsv1alpha1.TechnitiumClusterList
	if err := p.Client.List(ctx, &list); err != nil {
		// Keep the existing series: a failed list says nothing about the nodes.
		log.Error(err, "Failed to list TechnitiumClusters")
		return
	}

	seen := map[[2]string]struct{}{}
	for i := range list.Items {
		tc := &list.Items[i]
		for _, node := range p.pollCluster(ctx, tc) {
			seen[[2]string{tc.Name, node}] = struct{}{}
		}
	}

	for key := range p.known {
		if _, ok := seen[key]; !ok {
			p.metrics.deleteNode(key[0], key[1])
		}
	}
	p.known = seen
}

// pollCluster reads every node in tc's status and returns the node names it
// published series for. A cluster that is deleting, or has no minted token
// yet, publishes nothing.
func (p *TechnitiumStatsPoller) pollCluster(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster) []string {
	log := logf.FromContext(ctx).WithName("technitium-stats")

	if !tc.DeletionTimestamp.IsZero() || len(tc.Status.Nodes) == 0 {
		return nil
	}

	secretKey, err := adminSecretKeyFor(tc, p.OperatorNamespace)
	if err != nil {
		log.Info("Skipped stats for TechnitiumCluster with invalid admin Secret reference", "cluster", tc.Name, "error", err.Error())
		return nil
	}
	var secret corev1.Secret
	if err := p.Client.Get(ctx, secretKey, &secret); err != nil {
		log.Info("Skipped stats for TechnitiumCluster: admin Secret unavailable", "cluster", tc.Name, "error", err.Error())
		return nil
	}
	if len(secret.Data[adminSecretTokenKey]) == 0 {
		// Not bootstrapped yet; there is nothing to authenticate with.
		return nil
	}

	nodes := make([]string, 0, len(tc.Status.Nodes))
	for _, ns := range tc.Status.Nodes {
		nodes = append(nodes, ns.Name)
		p.pollNode(ctx, tc, &secret, ns)
	}
	return nodes
}

// pollNode reads one node and updates its series. A node that is not ready,
// or whose read fails, loses its data series and reports scrape_success 0.
func (p *TechnitiumStatsPoller) pollNode(ctx context.Context, tc *dnsv1alpha1.TechnitiumCluster, secret *corev1.Secret, ns dnsv1alpha1.TechnitiumClusterNodeStatus) {
	log := logf.FromContext(ctx).WithName("technitium-stats")
	labels := prometheus.Labels{labelCluster: tc.Name, labelNode: ns.Name}

	fail := func(reason string, err error) {
		p.metrics.deleteData(tc.Name, ns.Name)
		p.metrics.scrapeSuccess.With(labels).Set(0)
		if err != nil {
			log.Info("Could not read Technitium stats", "cluster", tc.Name, "node", ns.Name, "reason", reason, "error", err.Error())
		}
	}

	if !nodeJoined(ns.State) {
		fail("not ready", nil)
		return
	}
	ordinal, err := strconv.ParseInt(strings.TrimPrefix(ns.Name, tc.Name+"-"), 10, 32)
	if err != nil {
		fail("unrecognized node name", err)
		return
	}

	api, err := p.statsClient(nodeEndpoint(tc.Name, int32(ordinal), p.OperatorNamespace), secret)
	if err != nil {
		fail("building client", err)
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, statsPollTimeout)
	defer cancel()
	stats, err := api.GetDashboardStats(callCtx)
	if err != nil {
		fail("reading stats", err)
		return
	}
	p.metrics.set(tc.Name, ns.Name, stats)
}

func (p *TechnitiumStatsPoller) statsClient(endpoint string, secret *corev1.Secret) (statsAPI, error) {
	if p.NewStatsClient != nil {
		return p.NewStatsClient(endpoint, secret)
	}
	opts, err := config.ClientOptionsFromSecret(secret)
	if err != nil {
		return nil, err
	}
	return technitium.NewClient(endpoint, opts...)
}

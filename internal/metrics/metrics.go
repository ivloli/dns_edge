// Package metrics defines application-wide Prometheus metric registrations.
// All variables are package-level so callers can increment/observe inline.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// DNSQueriesTotal counts incoming DNS queries by query type and response code.
	// Labels: qtype ("A", "AAAA", "CNAME", …), rcode ("NOERROR", "NXDOMAIN", …).
	DNSQueriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dns_queries_total",
			Help: "Total DNS queries received, by qtype and rcode.",
		},
		[]string{"qtype", "rcode"},
	)

	// DNSQueryDuration measures per-query latency bucketed by qtype.
	DNSQueryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "dns_query_duration_seconds",
			Help:    "DNS query handling latency in seconds.",
			Buckets: []float64{0.00005, 0.0001, 0.0002, 0.0005, 0.001, 0.005, 0.01},
		},
		[]string{"qtype"},
	)

	// SyncTotal counts incremental PG sync attempts.
	// Label result: "success" or "error".
	SyncTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "dns_sync_total",
			Help: "Total incremental PG sync attempts, by result.",
		},
		[]string{"result"},
	)

	// SyncDuration measures the wall-clock time of each successful sync.
	SyncDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "dns_sync_duration_seconds",
		Help:    "Incremental PG sync latency in seconds.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10},
	})

	// SyncLastSuccess is a Unix timestamp of the last successful sync.
	// Zero means no sync has succeeded yet (useful for stale-sync alerting).
	SyncLastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "dns_sync_last_success_timestamp_seconds",
		Help: "Unix timestamp of the last successful incremental PG sync.",
	})
)

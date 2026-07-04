package audit

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// These two metrics live here rather than internal/proxy/metrics.go (where
// spec/09-observability.md lists them) because internal/proxy imports
// internal/audit for the Writer type; declaring them in proxy and
// importing proxy back from here would be a compile-time import cycle.
// The Prometheus name, help text, and labels are exactly as specified —
// only the Go package that registers them differs, which Prometheus never
// observes.
var (
	// AuditBufferUsage reports how many events currently sit in Writer's
	// buffered channel — an early warning for approaching capacity.
	AuditBufferUsage = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "conduit",
			Name:      "audit_buffer_usage",
			Help:      "Current number of events in the audit write buffer.",
		},
		[]string{},
	)

	// AuditEventsDropped counts events discarded because the buffer was
	// full — the critical alerting signal for audit data loss.
	AuditEventsDropped = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "conduit",
			Name:      "audit_events_dropped_total",
			Help:      "Total audit events dropped due to full buffer.",
		},
		[]string{},
	)
)

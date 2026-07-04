package proxy

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics declared here are the ones naturally incremented from within
// internal/proxy itself (proxy.go, sse.go) or from packages that already
// import internal/proxy for its context helpers (internal/auth,
// internal/ratelimit) — importing internal/proxy from those packages
// creates no cycle since proxy doesn't import them back.
//
// Three metrics from spec/09-observability.md's list — audit_buffer_usage,
// audit_events_dropped_total, plugin_latency_seconds, and tenants_active —
// are declared in internal/audit, internal/plugin, and internal/tenant
// instead: proxy imports all three of those packages, so declaring their
// metrics here and importing proxy back from them would be a compile-time
// import cycle. The metric names, labels, and Prometheus namespace are
// identical either way — Prometheus scrapes the process's metrics
// endpoint, not a particular Go package, so this split is invisible to
// anything outside the Go build.
var (
	// ToolCallsTotal counts every MCP tool call processed, labeled by
	// outcome — the primary traffic counter.
	ToolCallsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "conduit",
			Name:      "tool_calls_total",
			Help:      "Total number of MCP tool calls processed.",
		},
		[]string{"tenant_id", "server_name", "tool_name", "status", "policy_action"},
	)

	// ProxyLatencySeconds measures time spent in Conduit's own middleware
	// chain, excluding the upstream round trip — the core <1ms P99 target
	// from spec/02-proxy.md §6.
	ProxyLatencySeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "conduit",
			Name:      "proxy_latency_seconds",
			Help:      "Time spent in the proxy layer (excluding upstream latency).",
			Buckets:   []float64{0.0001, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
		},
		[]string{"tenant_id"},
	)

	// UpstreamLatencySeconds measures time spent waiting for the upstream
	// MCP server, separate from ProxyLatencySeconds so a slow upstream
	// doesn't get blamed on Conduit in a dashboard.
	UpstreamLatencySeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "conduit",
			Name:      "upstream_latency_seconds",
			Help:      "Time spent waiting for the upstream MCP server response.",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0, 5.0, 10.0, 30.0},
		},
		[]string{"tenant_id", "server_name"},
	)

	// AuthDecisionsTotal counts authentication outcomes. Incremented by
	// internal/auth's middleware.
	AuthDecisionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "conduit",
			Name:      "auth_decisions_total",
			Help:      "Total authentication decisions.",
		},
		[]string{"method", "result"},
	)

	// RateLimitDecisionsTotal counts rate-limit outcomes. Incremented by
	// internal/ratelimit's middleware.
	RateLimitDecisionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "conduit",
			Name:      "rate_limit_decisions_total",
			Help:      "Total rate limiting decisions.",
		},
		[]string{"tenant_id", "scope", "result"},
	)

	// PolicyDecisionsTotal counts policy engine outcomes. Registered now;
	// incremented starting Phase 6 when the policy engine itself exists —
	// until then PolicyMiddleware is a no-op and this stays at zero.
	PolicyDecisionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "conduit",
			Name:      "policy_decisions_total",
			Help:      "Total policy engine decisions.",
		},
		[]string{"tenant_id", "rule_name", "action"},
	)

	// ActiveConnections tracks concurrently open SSE proxy connections.
	ActiveConnections = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "conduit",
			Name:      "active_connections",
			Help:      "Current number of active SSE proxy connections.",
		},
		[]string{"tenant_id"},
	)

	// UpstreamErrors counts failures reaching or getting a valid response
	// from an upstream MCP server.
	UpstreamErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "conduit",
			Name:      "upstream_errors_total",
			Help:      "Total errors from upstream MCP servers.",
		},
		[]string{"tenant_id", "server_name", "error_type"},
	)

	// BuildInfo exposes the running binary's version for Grafana's version
	// tracking panel.
	BuildInfo = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "conduit",
			Name:      "build_info",
			Help:      "Build information for Conduit.",
		},
		[]string{"version", "commit", "go_version"},
	)
)

// InitBuildInfo sets the build_info metric to 1 with version labels. Call
// once at startup.
func InitBuildInfo(version, commit, goVersion string) {
	BuildInfo.WithLabelValues(version, commit, goVersion).Set(1)
}

// RecordToolCall increments ToolCallsTotal for one completed tool call.
// status is derived from the HTTP status code Conduit returned to the
// agent: "success" for 2xx, "rate_limited" for 429, "denied" for 403, and
// "error" for everything else.
func RecordToolCall(tenantID, serverName, toolName string, statusCode int, policyAction string) {
	ToolCallsTotal.WithLabelValues(tenantID, serverName, toolName, classifyStatus(statusCode), policyAction).Inc()
}

func classifyStatus(statusCode int) string {
	switch {
	case statusCode == 429:
		return "rate_limited"
	case statusCode == 403:
		return "denied"
	case statusCode >= 200 && statusCode < 300:
		return "success"
	default:
		return "error"
	}
}

// RecordProxyLatency observes the total time Conduit spent handling a
// request, from RequestIDMiddleware to the audit write.
func RecordProxyLatency(tenantID string, d time.Duration) {
	ProxyLatencySeconds.WithLabelValues(tenantID).Observe(d.Seconds())
}

// RecordUpstreamLatency observes how long the upstream MCP server took to
// respond, independent of Conduit's own overhead.
func RecordUpstreamLatency(tenantID, serverName string, d time.Duration) {
	UpstreamLatencySeconds.WithLabelValues(tenantID, serverName).Observe(d.Seconds())
}

// RecordUpstreamError increments UpstreamErrors for a failed upstream call.
func RecordUpstreamError(tenantID, serverName, errorType string) {
	UpstreamErrors.WithLabelValues(tenantID, serverName, errorType).Inc()
}

// classifyUpstreamError maps a transport-level error into one of the
// error_type label values UpstreamErrors documents.
func classifyUpstreamError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "connection refused"):
		return "connection_refused"
	default:
		return "server_error"
	}
}

# Spec 09 — Observability

> Phase: P3 | Files: `internal/proxy/metrics.go`, logging setup in `cmd/conduit/main.go`, OTel setup

---

## 1. Prometheus Metrics

All metrics are registered in `internal/proxy/metrics.go` using `prometheus/client_golang`.

### Required Metrics (13 total)

```go
package proxy

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    // 1. Total tool calls — primary traffic counter
    ToolCallsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Namespace: "conduit",
            Name:      "tool_calls_total",
            Help:      "Total number of MCP tool calls processed.",
        },
        []string{"tenant_id", "server_name", "tool_name", "status", "policy_action"},
        // status: "success" | "error" | "rate_limited" | "denied"
    )

    // 2. Proxy latency histogram — core performance metric
    ProxyLatencySeconds = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Namespace: "conduit",
            Name:      "proxy_latency_seconds",
            Help:      "Time spent in the proxy layer (excluding upstream latency).",
            Buckets:   []float64{0.0001, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
            // 0.1ms, 0.5ms, 1ms, 2ms, 5ms, 10ms, 50ms, 100ms, 500ms, 1s
        },
        []string{"tenant_id"},
    )

    // 3. Upstream latency histogram — separate from proxy overhead
    UpstreamLatencySeconds = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Namespace: "conduit",
            Name:      "upstream_latency_seconds",
            Help:      "Time spent waiting for the upstream MCP server response.",
            Buckets:   []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0, 5.0, 10.0, 30.0},
        },
        []string{"tenant_id", "server_name"},
    )

    // 4. Auth decisions
    AuthDecisionsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Namespace: "conduit",
            Name:      "auth_decisions_total",
            Help:      "Total authentication decisions.",
        },
        []string{"method", "result"},
        // method: "api_key" | "jwt"
        // result: "allow" | "deny" | "error"
    )

    // 5. Rate limit decisions
    RateLimitDecisionsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Namespace: "conduit",
            Name:      "rate_limit_decisions_total",
            Help:      "Total rate limiting decisions.",
        },
        []string{"tenant_id", "scope", "result"},
        // result: "allow" | "deny"
    )

    // 6. Policy decisions
    PolicyDecisionsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Namespace: "conduit",
            Name:      "policy_decisions_total",
            Help:      "Total policy engine decisions.",
        },
        []string{"tenant_id", "rule_name", "action"},
        // action: "allow" | "deny" | "rate_limit"
    )

    // 7. Audit buffer usage — early warning for buffer overflow
    AuditBufferUsage = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Namespace: "conduit",
            Name:      "audit_buffer_usage",
            Help:      "Current number of events in the audit write buffer.",
        },
        []string{},
    )

    // 8. Audit events dropped — critical alert metric
    AuditEventsDropped = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Namespace: "conduit",
            Name:      "audit_events_dropped_total",
            Help:      "Total audit events dropped due to full buffer.",
        },
        []string{},
    )

    // 9. Active SSE connections — capacity planning
    ActiveConnections = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Namespace: "conduit",
            Name:      "active_connections",
            Help:      "Current number of active SSE proxy connections.",
        },
        []string{"tenant_id"},
    )

    // 10. Upstream errors
    UpstreamErrors = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Namespace: "conduit",
            Name:      "upstream_errors_total",
            Help:      "Total errors from upstream MCP servers.",
        },
        []string{"tenant_id", "server_name", "error_type"},
        // error_type: "timeout" | "connection_refused" | "server_error" | "parse_error"
    )

    // 11. Plugin execution latency
    PluginLatencySeconds = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Namespace: "conduit",
            Name:      "plugin_latency_seconds",
            Help:      "Time spent executing plugins.",
            Buckets:   []float64{0.0001, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
        },
        []string{"plugin_name", "hook"},
        // hook: "before" | "after"
    )

    // 12. Tenant count — operational metric
    TenantsActive = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Namespace: "conduit",
            Name:      "tenants_active",
            Help:      "Number of active tenants in the routing table.",
        },
        []string{},
    )

    // 13. Build info — for Grafana version tracking
    BuildInfo = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Namespace: "conduit",
            Name:      "build_info",
            Help:      "Build information for Conduit.",
        },
        []string{"version", "commit", "go_version"},
    )
)

// InitBuildInfo sets the build_info metric to 1 with version labels.
// Call once at startup.
func InitBuildInfo(version, commit, goVersion string) {
    BuildInfo.WithLabelValues(version, commit, goVersion).Set(1)
}
```

### Metrics Server

Expose on a SEPARATE port (`:9090`) so it can be blocked from external traffic:

```go
// In cmd/conduit/main.go during proxy start:
metricsServer := &http.Server{
    Addr:    fmt.Sprintf(":%d", cfg.Server.MetricsPort),  // default: 9090
    Handler: promhttp.Handler(),
}
go metricsServer.ListenAndServe()
```

---

## 2. Structured Logging

Use `github.com/rs/zerolog`. Log format is always JSON in production, pretty-print only for `--log-format=text` in development.

### Logger setup — `cmd/conduit/main.go`

```go
func buildLogger(level, format string) zerolog.Logger {
    lvl, err := zerolog.ParseLevel(level)
    if err != nil {
        lvl = zerolog.InfoLevel
    }
    zerolog.SetGlobalLevel(lvl)

    if format == "text" {
        return zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
            With().Timestamp().Logger()
    }
    return zerolog.New(os.Stdout).With().Timestamp().Logger()
}
```

### Required log fields on every proxy request

```go
// In LoggingMiddleware, log on request completion:
log.Info().
    Str("request_id", requestID).
    Str("tenant_id", tenantID).
    Str("method", r.Method).
    Str("path", r.URL.Path).
    Str("remote_addr", r.RemoteAddr).
    Int("status_code", statusCode).
    Int64("latency_ms", latencyMs).
    Str("tool_name", toolName).
    Str("trace_id", traceID).
    Msg("request completed")
```

### Log levels
- `DEBUG` — per-field parsing, cache hits/misses
- `INFO` — request completed, startup/shutdown events
- `WARN` — Redis errors (fail-open), rate limit config not found (using default), audit buffer at 80%
- `ERROR` — DB write failures, unexpected panics, plugin errors

---

## 3. OpenTelemetry Tracing

### Setup — `cmd/conduit/main.go`

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
    "go.opentelemetry.io/otel/sdk/resource"
    "go.opentelemetry.io/otel/sdk/trace"
    semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

func initTracer(ctx context.Context, cfg *config.ObservabilityConfig) (func(), error) {
    if cfg.OTELEndpoint == "" {
        // Tracing disabled — use no-op tracer
        otel.SetTracerProvider(otel.GetTracerProvider())
        return func() {}, nil
    }

    exporter, err := otlptracegrpc.New(ctx,
        otlptracegrpc.WithEndpoint(cfg.OTELEndpoint),
        otlptracegrpc.WithInsecure(),
    )
    if err != nil {
        return nil, fmt.Errorf("create OTLP exporter: %w", err)
    }

    res, _ := resource.New(ctx,
        resource.WithAttributes(
            semconv.ServiceName("conduit"),
            semconv.ServiceVersion(version),
        ),
    )

    tp := trace.NewTracerProvider(
        trace.WithBatcher(exporter),
        trace.WithResource(res),
        trace.WithSampler(trace.TraceIDRatioBased(cfg.TraceSamplingRate)),
    )
    otel.SetTracerProvider(tp)

    return func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        tp.Shutdown(ctx)
    }, nil
}
```

### Span Hierarchy

Every tool call MUST produce this span tree:

```
conduit.request                              [root span]
  ├── conduit.auth                           [auth middleware]
  │   └── conduit.auth.cache_lookup         [Redis GET]
  ├── conduit.ratelimit                      [rate limit middleware]
  │   └── conduit.ratelimit.lua             [Redis EVALSHA]
  ├── conduit.policy                         [policy engine]
  ├── conduit.plugin.before.{plugin_name}   [each Before plugin]
  ├── conduit.upstream                       [upstream HTTP call]
  ├── conduit.plugin.after.{plugin_name}    [each After plugin]
  └── conduit.audit.write                   [channel enqueue]
```

### Span attributes (on conduit.request)

```go
span.SetAttributes(
    attribute.String("tenant.id", tenantID),
    attribute.String("server.name", serverName),
    attribute.String("tool.name", toolName),
    attribute.String("auth.method", authMethod),
    attribute.String("policy.action", policyAction),
    attribute.Int("http.status_code", statusCode),
    attribute.Int("conduit.latency_ms", proxyLatencyMs),
    attribute.Int("upstream.latency_ms", upstreamLatencyMs),
)
```

---

## 4. Grafana Dashboard — `dashboards/conduit-overview.json`

The Grafana dashboard must include these panels:

| Panel | Visualization | Query |
|---|---|---|
| Requests/sec | Time series | `rate(conduit_tool_calls_total[1m])` |
| P99 Proxy Latency | Stat + Gauge | `histogram_quantile(0.99, rate(conduit_proxy_latency_seconds_bucket[5m]))` |
| P99 Upstream Latency | Stat + Gauge | `histogram_quantile(0.99, rate(conduit_upstream_latency_seconds_bucket[5m]))` |
| Error Rate | Time series | `rate(conduit_tool_calls_total{status="error"}[1m])` |
| Rate Limited Requests | Time series | `rate(conduit_rate_limit_decisions_total{result="deny"}[1m])` |
| Policy Denials | Time series | `rate(conduit_policy_decisions_total{action="deny"}[1m])` |
| Active Connections | Gauge | `sum(conduit_active_connections)` |
| Audit Buffer Usage | Gauge | `conduit_audit_buffer_usage` |
| Audit Events Dropped | Counter | `increase(conduit_audit_events_dropped_total[1h])` |
| Top Tools | Bar chart | `topk(10, sum by (tool_name) (rate(conduit_tool_calls_total[5m])))` |
| Top Tenants | Bar chart | `topk(10, sum by (tenant_id) (rate(conduit_tool_calls_total[5m])))` |
| Build Version | Text panel | `conduit_build_info` |

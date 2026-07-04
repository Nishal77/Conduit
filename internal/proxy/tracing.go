package proxy

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// tracerName identifies Conduit's spans in whatever backend they're
// exported to (Jaeger, Tempo, Datadog, ...) — see spec/09-observability.md §3.
const tracerName = "conduit"

// tracer returns the global tracer for internal/proxy's spans. When no
// OTel SDK has been installed (observability.otel_endpoint is unset),
// otel.Tracer returns a no-op implementation, so every span/attribute call
// below is nearly free and always safe to make unconditionally.
func tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// TracingMiddleware opens the root "conduit.request" span for every proxy
// request — the top of the span hierarchy spec/09-observability.md §3
// requires. It runs right after RequestIDMiddleware so the whole
// auth -> ratelimit -> policy -> plugin -> upstream -> audit chain executes
// inside this span's context.
func TracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer().Start(r.Context(), "conduit.request")
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TraceIDFromContext returns the current span's trace ID as a hex string,
// or "" if tracing is disabled (no-op tracer) or ctx carries no span. Used
// to populate audit.Event.TraceID and structured log lines so a trace in
// Jaeger/Tempo can be cross-referenced with the exact audit row it produced.
func TraceIDFromContext(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// SetRequestSpanAttributes records the standard attributes
// spec/09-observability.md §3 requires on the root span, once the request's
// outcome is known (tenant, tool, auth method, policy action, status).
func SetRequestSpanAttributes(ctx context.Context, tenantID, serverName, toolName, authMethod, policyAction string, statusCode int) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(
		attribute.String("tenant.id", tenantID),
		attribute.String("server.name", serverName),
		attribute.String("tool.name", toolName),
		attribute.String("auth.method", authMethod),
		attribute.String("policy.action", policyAction),
		attribute.Int("http.status_code", statusCode),
	)
}

// startChildSpan is a small helper the rest of internal/proxy (and
// internal/auth, internal/ratelimit) use to open a named child span under
// whatever span is already active on ctx — auth.cache_lookup under
// conduit.auth, ratelimit.lua under conduit.ratelimit, and so on.
func startChildSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return tracer().Start(ctx, name)
}

// StartUpstreamSpan opens the "conduit.upstream" span around the call to
// the upstream MCP server.
func StartUpstreamSpan(ctx context.Context) (context.Context, trace.Span) {
	return startChildSpan(ctx, "conduit.upstream")
}

// StartAuditWriteSpan opens the "conduit.audit.write" span around enqueuing
// an audit event. It's expected to be extremely short (a channel send), but
// spec/09-observability.md §3 lists it explicitly in the span tree so a
// trace makes the non-blocking hand-off to the audit writer visible.
func StartAuditWriteSpan(ctx context.Context) (context.Context, trace.Span) {
	return startChildSpan(ctx, "conduit.audit.write")
}

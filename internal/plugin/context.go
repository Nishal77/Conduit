package plugin

import "context"

// RequestContext carries the request metadata a plugin needs but that
// isn't part of the mcp.Message itself (tenant/agent/server identity,
// timing). internal/plugin can't import internal/proxy for its own context
// helpers — proxy already imports plugin, so that direction would cycle —
// so proxy.go populates this via WithRequestContext before calling
// Registry.RunBefore/RunAfter, and plugins (chiefly HTTPCallbackPlugin, to
// build the "context" field of its wire protocol) read it back with
// RequestContextFromContext.
type RequestContext struct {
	TenantID   string
	AgentID    string
	ServerName string
	RequestID  string
	LatencyMs  int64 // only meaningful in an After hook
}

type contextKey int

const requestContextKey contextKey = iota

// WithRequestContext returns a copy of ctx carrying rc.
func WithRequestContext(ctx context.Context, rc RequestContext) context.Context {
	return context.WithValue(ctx, requestContextKey, rc)
}

// RequestContextFromContext returns the RequestContext stored by
// WithRequestContext, or a zero-value one if none was set.
func RequestContextFromContext(ctx context.Context) RequestContext {
	rc, _ := ctx.Value(requestContextKey).(RequestContext)
	return rc
}

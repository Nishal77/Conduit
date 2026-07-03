package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// contextKey is an unexported type for context keys defined in this
// package, so they can never collide with keys set by other packages.
type contextKey int

const (
	ctxKeyTenantID contextKey = iota
	ctxKeyServerName
	ctxKeyRequestID
	ctxKeyAgentID
	ctxKeySessionID
	ctxKeyAuthMethod
	ctxKeyStartTime
)

// TenantIDFromContext returns the tenant identifier set by AuthMiddleware,
// or "" if the request has not passed through it.
//
// Phase 1 note: with no auth/database layer yet, AuthMiddleware sets this to
// the tenant_slug parsed from the URL. Phase 2 replaces that with the real
// tenant UUID resolved from a validated API key or JWT (ADR-004: tenant_id
// is never trusted from the URL in production) — callers should treat this
// value as opaque and not assume it's a UUID until then.
func TenantIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyTenantID).(string)
	return v
}

// ServerNameFromContext returns the MCP server name segment of the request
// path, set by the proxy handler once routing succeeds.
func ServerNameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyServerName).(string)
	return v
}

// RequestIDFromContext returns the X-Request-ID for this request.
func RequestIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

// AgentIDFromContext returns the agent identifier captured from the MCP
// "initialize" request's clientInfo.name, or "" if unknown.
func AgentIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyAgentID).(string)
	return v
}

// AuthMethodFromContext returns how this request was authenticated:
// "api_key", "jwt", or "none" (Phase 1, before auth exists).
func AuthMethodFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyAuthMethod).(string)
	return v
}

// startTimeFromContext returns the time RequestIDMiddleware started
// processing this request, used to compute total latency for logging/audit.
func startTimeFromContext(ctx context.Context) time.Time {
	v, _ := ctx.Value(ctxKeyStartTime).(time.Time)
	return v
}

// Middleware wraps an http.Handler with additional behavior.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares around h in order, so the first middleware in
// the list is the outermost — it sees the request first and the response
// last.
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// StandardChain returns the middleware chain applied to every proxy
// request, in the exact order specified by spec/02-proxy.md §4. Auth, rate
// limiting, and policy are no-op pass-throughs in Phase 1 — they gain real
// behavior in Phase 2 (auth/ratelimit) and Phase 6 (policy) respectively,
// but stay wired into this same slot so the chain's shape never changes.
func StandardChain(plugins *plugin.Registry) []Middleware {
	return []Middleware{
		RequestIDMiddleware,
		LoggingMiddleware,
		RecoveryMiddleware,
		AuthMiddleware,
		RateLimitMiddleware,
		PolicyMiddleware,
		PluginBeforeMiddleware(plugins),
	}
}

// RequestIDMiddleware assigns each request a unique ID (reusing an
// upstream-supplied X-Request-ID if present), stores the request start time,
// and echoes the ID back on the response.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.New().String()
		}
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		ctx = context.WithValue(ctx, ctxKeyStartTime, time.Now())
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder captures the status code written by downstream handlers so
// LoggingMiddleware can report it after the fact — http.ResponseWriter has
// no way to read back what WriteHeader was called with.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rec *statusRecorder) WriteHeader(status int) {
	rec.status = status
	rec.wroteHeader = true
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if !rec.wroteHeader {
		rec.status = http.StatusOK
		rec.wroteHeader = true
	}
	return rec.ResponseWriter.Write(b)
}

// Flush implements http.Flusher by delegating to the wrapped
// ResponseWriter, if it supports flushing. SSE streaming (sse.go) requires
// this: without it, wrapping the response in statusRecorder would silently
// break every streamed tool call by making the Flusher type assertion fail.
func (rec *statusRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// LoggingMiddleware logs one structured line per request: method, path,
// status, latency, and the identifiers set by earlier middleware.
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		start := startTimeFromContext(r.Context())
		var latencyMs float64
		if !start.IsZero() {
			latencyMs = time.Since(start).Seconds() * 1000
		}

		log.Info().
			Str("request_id", RequestIDFromContext(r.Context())).
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", rec.status).
			Float64("latency_ms", latencyMs).
			Str("tenant_id", TenantIDFromContext(r.Context())).
			Msg("request handled")
	})
}

// RecoveryMiddleware recovers from a panic anywhere downstream, logs it
// with a stack trace, and returns a generic 500 — the panic's actual
// message is never sent to the client, since it could leak internal state.
func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error().
					Interface("panic", rec).
					Str("stack", string(debug.Stack())).
					Str("request_id", RequestIDFromContext(r.Context())).
					Msg("recovered from panic")
				writeError(w, r, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// AuthMiddleware validates the caller's credentials and sets tenant_id in
// context.
//
// Phase 1: no auth backend exists yet, so this is a deliberate no-op that
// trusts the tenant_slug URL segment (already extracted into context by the
// proxy handler before the chain runs — see Proxy.ServeHTTP) and marks the
// auth method as "none". Phase 2 replaces the body of this function with
// real API key / JWT validation and rejects unauthenticated requests with
// 401; every call site and context key stays the same.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxKeyAuthMethod, "none")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RateLimitMiddleware enforces the token-bucket rate limit for the caller.
//
// Phase 1: no Redis-backed limiter exists yet, so this is a no-op
// pass-through. Phase 2 replaces the body with a Redis Lua token-bucket
// check that returns 429 with a Retry-After header when exceeded.
func RateLimitMiddleware(next http.Handler) http.Handler {
	return next
}

// PolicyMiddleware evaluates YAML allow/deny policy rules against the
// request.
//
// Phase 1: no policy engine exists yet, so this is a no-op pass-through.
// Phase 6 replaces the body with a compiled decision-tree evaluation that
// returns 403 when a rule denies the call.
func PolicyMiddleware(next http.Handler) http.Handler {
	return next
}

// PluginBeforeMiddleware runs registered plugins' Before hooks against the
// request body before it's forwarded upstream. With an empty Registry
// (Phase 1's default) this is a no-op: ForTenant returns nothing, so
// RunBefore returns the original message unchanged.
//
// Only requests with a JSON body that parses as an MCP message are run
// through the chain — GET requests (e.g. SSE stream resumption) and
// malformed bodies pass through untouched so a parsing quirk in Phase 1
// never breaks basic proxying.
func PluginBeforeMiddleware(plugins *plugin.Registry) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.Body == nil {
				next.ServeHTTP(w, r)
				return
			}

			body, err := readAndReplaceBody(r)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "failed to read request body")
				return
			}

			msg, err := mcp.ParseMessage(body)
			if err != nil {
				// Not a well-formed MCP message (or not JSON at all) — pass
				// through unchanged rather than reject; only tools/call
				// requests need to survive this parse, and other MCP
				// methods proxy transparently either way.
				next.ServeHTTP(w, r)
				return
			}

			tenantID := TenantIDFromContext(r.Context())
			modified, err := plugins.RunBefore(r.Context(), tenantID, msg)
			if err != nil {
				writeError(w, r, http.StatusForbidden, "request blocked by policy")
				return
			}

			out, err := json.Marshal(modified)
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "failed to re-encode request")
				return
			}
			replaceBody(r, out)
			next.ServeHTTP(w, r)
		})
	}
}

// ErrorResponse is the JSON body Conduit returns for every error it
// generates itself (as opposed to errors proxied through from upstream).
type ErrorResponse struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	RequestID string `json:"request_id"`
}

// errorCodeForStatus maps an HTTP status to Conduit's machine-readable
// error code, per spec/02-proxy.md §4.
func errorCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RATE_LIMITED"
	case http.StatusBadGateway:
		return "UPSTREAM_ERROR"
	case http.StatusServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	case http.StatusGatewayTimeout:
		return "UPSTREAM_TIMEOUT"
	default:
		return "INTERNAL_ERROR"
	}
}

// writeError writes a Conduit-generated error response in the standard
// ErrorResponse shape.
func writeError(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error:     message,
		Code:      errorCodeForStatus(status),
		RequestID: RequestIDFromContext(r.Context()),
	})
}

// loggerFromContext returns a zerolog.Logger enriched with this request's
// identifiers, for handlers that need to log more than one line.
func loggerFromContext(ctx context.Context) zerolog.Logger {
	return log.With().
		Str("request_id", RequestIDFromContext(ctx)).
		Str("tenant_id", TenantIDFromContext(ctx)).
		Logger()
}

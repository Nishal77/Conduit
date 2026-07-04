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

// TenantIDFromContext returns the authenticated tenant identifier set by
// the auth middleware, or "" if the request has not passed through it (or
// no auth middleware is configured — see WithAuthMiddleware).
func TenantIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyTenantID).(string)
	return v
}

// WithTenantID returns a copy of ctx carrying tenantID as the authenticated
// tenant identity (ADR-004: tenant_id must come from a validated credential,
// never the URL or request body). internal/auth's middleware calls this once
// an API key or JWT validates successfully; nothing else in Conduit should.
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKeyTenantID, tenantID)
}

// WithAuthMethod returns a copy of ctx recording how the request was
// authenticated ("api_key", "jwt", or "none"). See AuthMethodFromContext.
func WithAuthMethod(ctx context.Context, method string) context.Context {
	return context.WithValue(ctx, ctxKeyAuthMethod, method)
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

// standardChain returns the middleware chain applied to every proxy
// request, in the exact order specified by spec/02-proxy.md §4. auth and
// rateLimit are injected (see WithAuthMiddleware / WithRateLimitMiddleware
// in proxy.go) so this package doesn't need to import internal/auth or
// internal/ratelimit directly; New defaults both to a no-op pass-through
// when the caller doesn't supply one. Policy stays a no-op until Phase 6.
func standardChain(auth, rateLimit Middleware, plugins *plugin.Registry) []Middleware {
	return []Middleware{
		RequestIDMiddleware,
		LoggingMiddleware,
		RecoveryMiddleware,
		auth,
		rateLimit,
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

// AuthMiddleware is the default auth step: a no-op that marks every request
// unauthenticated ("none") and leaves tenant_id unset. Proxy.New wires this
// in unless the caller supplies a real one via WithAuthMiddleware — which
// main.go does starting in Phase 2, using internal/auth.NewMiddleware to
// validate API keys/JWTs and reject unauthenticated requests with 401. A
// deployment that never configures auth (e.g. a quick local test) still
// gets a working proxy, just with no tenant isolation — the same tradeoff
// spec/02-proxy.md's Phase 1 design accepted.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithAuthMethod(r.Context(), "none")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RateLimitMiddleware is the default rate-limit step: a no-op pass-through.
// Proxy.New wires this in unless the caller supplies a real one via
// WithRateLimitMiddleware — which main.go does starting in Phase 2, using
// internal/ratelimit.NewMiddleware for the Redis token-bucket check.
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

			body, err := ReadAndReplaceBody(r)
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
			ReplaceBody(r, out)
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

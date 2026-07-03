# Spec 02 — Core Proxy

> Phase: P1 | Files: `internal/proxy/proxy.go`, `internal/proxy/sse.go`, `internal/proxy/middleware.go`

---

## 1. Overview

The proxy is the hot path. Every MCP tool call flows through it. It MUST:
- Add <1ms P99 overhead at 1,000 RPS
- Correctly stream SSE without buffering full events in memory
- Handle connection drops gracefully (no goroutine leaks)
- Run plugins Before and After the upstream call
- Write audit events asynchronously (non-blocking)

---

## 2. HTTP Transport — `internal/proxy/proxy.go`

### Proxy struct

```go
package proxy

import (
    "context"
    "net/http"
    "net/http/httputil"
    "time"

    "github.com/conduit-oss/conduit/internal/audit"
    "github.com/conduit-oss/conduit/internal/config"
    "github.com/conduit-oss/conduit/internal/mcp"
    "github.com/conduit-oss/conduit/internal/plugin"
    "github.com/conduit-oss/conduit/internal/tenant"
    "github.com/rs/zerolog"
)

// Proxy is the core MCP reverse proxy.
type Proxy struct {
    cfg       *config.Config
    router    *tenant.Router
    plugins   *plugin.Registry
    auditor   *audit.Writer
    transport http.RoundTripper
    log       zerolog.Logger
}

// New creates a new Proxy with all dependencies injected.
func New(
    cfg *config.Config,
    router *tenant.Router,
    plugins *plugin.Registry,
    auditor *audit.Writer,
    log zerolog.Logger,
) *Proxy

// ServeHTTP implements http.Handler.
// URL pattern: /mcp/{tenant_slug}/{server_name}
// All other paths return 404.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

### URL routing

```
/mcp/{tenant_slug}/{server_name}   → proxy to upstream
/healthz                            → health check (always 200)
/readyz                             → readiness check (checks DB + Redis)
/metrics                            → Prometheus metrics (port 9090 only)
```

Path parsing:
```go
// Extract from URL path using strings.SplitN
// path must have at least 4 segments: ["", "mcp", tenant_slug, server_name]
// If not, return 404 with JSON error body
parts := strings.SplitN(r.URL.Path, "/", 5)
// parts[0]="" parts[1]="mcp" parts[2]=tenantSlug parts[3]=serverName
```

### Custom transport

```go
// transport MUST be created with these settings:
transport := &http.Transport{
    MaxIdleConns:        100,
    MaxIdleConnsPerHost: 10,
    IdleConnTimeout:     90 * time.Second,
    // Do NOT set TLSHandshakeTimeout < 5s for upstream connections
    TLSHandshakeTimeout:   10 * time.Second,
    ExpectContinueTimeout: 1 * time.Second,
    // DisableCompression so we don't interfere with SSE
    DisableCompression: true,
}
```

### Request forwarding

```go
// For SSE connections:
// 1. Detect if the upstream is SSE by checking Content-Type on the upstream response
// 2. If SSE: delegate to SSEProxy.Forward()
// 3. If JSON: read full body, run After plugins, return

// Upstream URL construction:
// upstreamURL = server.UpstreamURL + r.URL.Path[len("/mcp/"+tenantSlug+"/"+serverName):]
// Preserve query string and fragment

// Headers to forward:
// - All original request headers EXCEPT hop-by-hop headers
// - Add: X-Conduit-Tenant-ID, X-Conduit-Request-ID, X-Forwarded-For
// - Remove: Connection, Keep-Alive, Transfer-Encoding, Upgrade, Proxy-Authorization

// Hop-by-hop headers to strip (both directions):
var hopByHopHeaders = []string{
    "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
    "Te", "Trailers", "Transfer-Encoding", "Upgrade",
}
```

---

## 3. SSE Streaming — `internal/proxy/sse.go`

```go
package proxy

import (
    "bufio"
    "context"
    "io"
    "net/http"

    "github.com/conduit-oss/conduit/internal/mcp"
    "github.com/conduit-oss/conduit/internal/plugin"
)

// SSEProxy handles Server-Sent Events streaming between agent and upstream.
type SSEProxy struct {
    plugins *plugin.Registry
}

// Forward pipes the SSE response from upstream to the agent.
// It intercepts each event for plugin.After hooks.
//
// Algorithm:
//   1. Copy upstream SSE headers to response writer
//   2. Flush headers immediately (critical for SSE)
//   3. Use bufio.Scanner to read line-by-line from upstream body
//   4. For each "data: {...}" line:
//      a. Parse the JSON-RPC message
//      b. If it is a tools/call response: run plugin.After hooks
//      c. Write the (possibly modified) line to the response writer
//      d. Write "\n" after each line
//   5. When upstream closes: close response writer
//
// Error handling:
//   - If upstream closes unexpectedly: log warning, return nil (agent will reconnect)
//   - If plugin.After panics: recover, log error, pass original response through
//   - If context is cancelled: stop immediately, return ctx.Err()
func (s *SSEProxy) Forward(
    ctx context.Context,
    w http.ResponseWriter,
    upstreamResp *http.Response,
    callReq *mcp.Message, // the original tools/call request
) error

// SSE response headers that MUST be set:
//   Content-Type: text/event-stream
//   Cache-Control: no-cache
//   X-Accel-Buffering: no
//   Connection: keep-alive (only if HTTP/1.1)

// Flusher MUST be checked:
//   flusher, ok := w.(http.Flusher)
//   if !ok { return errors.New("response writer does not support flushing") }
//   // Call flusher.Flush() after EVERY line written to w
```

### SSE line scanner

```go
// Use bufio.Scanner with a custom split function that handles SSE line endings.
// SSE uses "\n\n" between events, "\n" within events.
// The scanner MUST handle:
//   - Lines ending in \r\n (Windows)
//   - Lines ending in \n (Unix)
//   - Empty lines (event separator — pass through as-is)
//   - Lines starting with "data: " (event payload)
//   - Lines starting with "event: " (event type)
//   - Lines starting with "id: " (event ID)
//   - Lines starting with ": " (comment — pass through)
//   - Lines starting with "retry: " (reconnect hint — pass through)

// Maximum line size: 10MB (protect against malformed upstream)
const maxSSELineBytes = 10 * 1024 * 1024

scanner := bufio.NewScanner(body)
scanner.Buffer(make([]byte, 64*1024), maxSSELineBytes)
```

---

## 4. Middleware Chain — `internal/proxy/middleware.go`

### ContextKey type

```go
package proxy

// contextKey is an unexported type for context keys in this package.
type contextKey int

const (
    ctxKeyTenantID   contextKey = iota // string: tenant UUID
    ctxKeyServerName                   // string: MCP server name
    ctxKeyRequestID                    // string: UUID v4
    ctxKeyAgentID                      // string: agent identifier from initialize
    ctxKeySessionID                    // string: SSE session identifier
    ctxKeyAuthMethod                   // string: "api_key" | "jwt"
    ctxKeyStartTime                    // time.Time: request start
)

// Exported accessors (used by audit, policy, plugins)
func TenantIDFromContext(ctx context.Context) string
func RequestIDFromContext(ctx context.Context) string
func AgentIDFromContext(ctx context.Context) string
func AuthMethodFromContext(ctx context.Context) string
```

### Middleware interface

```go
// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares in order (first = outermost).
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
    for i := len(middlewares) - 1; i >= 0; i-- {
        h = middlewares[i](h)
    }
    return h
}
```

### Standard middleware order

```go
// Applied to every proxy request in this exact order:
middlewares := []Middleware{
    RequestIDMiddleware,    // 1. Assign X-Request-ID (UUID v4)
    LoggingMiddleware,      // 2. Log request start/end with duration
    RecoveryMiddleware,     // 3. Recover from panics, return 500
    AuthMiddleware,         // 4. Validate API key or JWT (sets tenant_id in ctx)
    RateLimitMiddleware,    // 5. Token bucket check (returns 429 if exceeded)
    PolicyMiddleware,       // 6. Evaluate policy rules (returns 403 if denied)
    PluginBeforeMiddleware, // 7. Run plugin.Before hooks
    // ─── Proxy handler (forward to upstream) ───
    // PluginAfter and AuditWrite happen inside the handler, not as middleware
}
```

### RequestIDMiddleware

```go
func RequestIDMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        id := r.Header.Get("X-Request-ID")
        if id == "" {
            id = uuid.New().String() // use google/uuid
        }
        ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
        w.Header().Set("X-Request-ID", id)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### RecoveryMiddleware

```go
// On panic: log with stack trace, return 500 with JSON body
// {"error": "internal server error", "code": "INTERNAL_ERROR", "request_id": "..."}
// NEVER expose panic message to the client
```

### Error response format

```go
// All error responses from Conduit (not upstream) MUST use this format:
type ErrorResponse struct {
    Error     string `json:"error"`      // human-readable message
    Code      string `json:"code"`       // machine-readable error code
    RequestID string `json:"request_id"` // from X-Request-ID context
}

// HTTP status → code mapping:
// 400 → "BAD_REQUEST"
// 401 → "UNAUTHORIZED"
// 403 → "FORBIDDEN"
// 404 → "NOT_FOUND"
// 429 → "RATE_LIMITED"
// 500 → "INTERNAL_ERROR"
// 502 → "UPSTREAM_ERROR"
// 503 → "SERVICE_UNAVAILABLE"
// 504 → "UPSTREAM_TIMEOUT"
```

---

## 5. Health & Readiness

### `/healthz`
- Always returns `200 OK` with body `{"status":"ok"}`
- No authentication required
- Used by Kubernetes liveness probe

### `/readyz`
```go
// Returns 200 if ALL of the following pass, else 503:
// 1. PostgreSQL connection: SELECT 1 (timeout: 2s)
// 2. Redis connection: PING (timeout: 1s)
// 3. Routing table has at least 1 tenant loaded
//
// Response body:
// {"status":"ready","checks":{"postgres":"ok","redis":"ok","routing":"ok"}}
// or
// {"status":"not_ready","checks":{"postgres":"ok","redis":"error","routing":"ok"}}
```

---

## 6. Performance Requirements

These are tested by `k6/load-test.js` before any release:

| Scenario | Target |
|---|---|
| Proxy overhead (auth cache hit, no DB) | P99 < 1ms |
| Proxy overhead (rate limit check via Redis) | P99 < 2ms |
| Proxy overhead (with pii-redactor plugin) | P99 < 5ms |
| SSE streaming throughput | > 5,000 events/sec per connection |
| Goroutine count at 1,000 concurrent sessions | < 10,000 |
| Memory RSS at 1,000 RPS | < 256MB |

### Critical: no goroutine leaks

Every goroutine started by the proxy MUST be tracked:
```go
// Use context cancellation to stop goroutines
// Use sync.WaitGroup to wait for cleanup on shutdown
// Test with goleak in integration tests:
//   defer goleak.VerifyNone(t)
```

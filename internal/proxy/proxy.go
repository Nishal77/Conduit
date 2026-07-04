// Package proxy implements Conduit's core MCP reverse proxy: the hot path
// that every tool call flows through. See spec/02-proxy.md for the full
// design. Package-level goal, restated from that spec: forward requests to
// the correct upstream MCP server with <1ms P99 overhead, streaming SSE
// responses without buffering full events in memory.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/conduit-oss/conduit/internal/audit"
	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/conduit-oss/conduit/internal/tenant"
	"github.com/rs/zerolog"
)

// hopByHopHeaders are stripped in both directions per RFC 7230 §6.1 — they
// describe the connection to the immediate peer and must never be forwarded
// blindly through a proxy.
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailers", "Transfer-Encoding", "Upgrade",
}

// newTransport builds the http.RoundTripper used for every upstream call.
// Settings are tuned for a proxy that holds many concurrent long-lived SSE
// connections: a generous idle pool, and DisableCompression so Conduit
// never has to (or accidentally does) transcode a gzip'd SSE stream, which
// would break line-by-line forwarding in sse.go.
func newTransport() http.RoundTripper {
	return &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	}
}

// ReadyChecker is one dependency /readyz verifies before reporting the
// proxy ready to receive traffic (e.g. "can we reach PostgreSQL").
// Phase 1 ships only a routing-table checker; Phase 2 adds PostgreSQL and
// Redis checkers without changing this interface or /readyz's response
// shape (spec/02-proxy.md §5).
type ReadyChecker interface {
	Name() string
	Check(ctx context.Context) error
}

// routingChecker reports ready once at least one MCP server is registered,
// so /readyz fails fast during startup before the routing table is loaded
// rather than accepting traffic it can't route anywhere.
type routingChecker struct{ router *tenant.Router }

func (c routingChecker) Name() string { return "routing" }
func (c routingChecker) Check(context.Context) error {
	if c.router.Len() == 0 {
		return fmt.Errorf("no MCP servers registered")
	}
	return nil
}

// Proxy is the core MCP reverse proxy.
type Proxy struct {
	cfg       *config.Config
	router    *tenant.Router
	plugins   *plugin.Registry
	auditor   *audit.Writer
	transport http.RoundTripper
	sse       *SSEProxy
	checkers  []ReadyChecker
	log       zerolog.Logger

	authMiddleware      Middleware
	rateLimitMiddleware Middleware

	mcpHandler http.Handler
}

// Option customizes a Proxy at construction time. See WithAuthMiddleware,
// WithRateLimitMiddleware, and WithReadyChecker.
type Option func(*Proxy)

// WithAuthMiddleware replaces the default no-op auth step with a real one —
// main.go passes internal/auth.NewMiddleware(...) here starting in Phase 2.
// Kept as an injected Option rather than a constructor parameter so
// internal/proxy never has to import internal/auth (which itself imports
// internal/store): the dependency points from main.go inward, not between
// internal packages.
func WithAuthMiddleware(mw Middleware) Option {
	return func(p *Proxy) { p.authMiddleware = mw }
}

// WithRateLimitMiddleware replaces the default no-op rate-limit step with a
// real one — main.go passes internal/ratelimit.NewMiddleware(...) here
// starting in Phase 2. See WithAuthMiddleware for why this is an Option.
func WithRateLimitMiddleware(mw Middleware) Option {
	return func(p *Proxy) { p.rateLimitMiddleware = mw }
}

// WithReadyChecker adds an extra dependency /readyz must verify (e.g.
// PostgreSQL, Redis) alongside the always-present routing-table check.
func WithReadyChecker(c ReadyChecker) Option {
	return func(p *Proxy) { p.checkers = append(p.checkers, c) }
}

// New creates a Proxy with all required dependencies injected and any
// number of Options applied on top. With no options, auth and rate limiting
// are no-op pass-throughs (Phase 1 behavior); see WithAuthMiddleware and
// WithRateLimitMiddleware for how later phases enable the real thing.
func New(
	cfg *config.Config,
	router *tenant.Router,
	plugins *plugin.Registry,
	auditor *audit.Writer,
	log zerolog.Logger,
	opts ...Option,
) *Proxy {
	p := &Proxy{
		cfg:                 cfg,
		router:              router,
		plugins:             plugins,
		auditor:             auditor,
		transport:           newTransport(),
		sse:                 &SSEProxy{plugins: plugins},
		checkers:            []ReadyChecker{routingChecker{router: router}},
		log:                 log,
		authMiddleware:      AuthMiddleware,
		rateLimitMiddleware: RateLimitMiddleware,
	}
	for _, opt := range opts {
		opt(p)
	}
	p.mcpHandler = Chain(http.HandlerFunc(p.forward), standardChain(p.authMiddleware, p.rateLimitMiddleware, plugins)...)
	return p
}

// ServeHTTP implements http.Handler. URL pattern: /mcp/{tenant_slug}/{server_name}
// for proxied traffic, plus the unauthenticated /healthz and /readyz probes.
// Every other path returns 404.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		p.handleHealthz(w, r)
	case r.URL.Path == "/readyz":
		p.handleReadyz(w, r)
	case strings.HasPrefix(r.URL.Path, "/mcp/"):
		p.mcpHandler.ServeHTTP(w, r)
	default:
		writeError(w, r, http.StatusNotFound, "not found")
	}
}

// handleHealthz always returns 200 — it answers "is the process alive",
// not "can it serve traffic" (that's /readyz). No auth required; used by
// the Kubernetes liveness probe.
func (p *Proxy) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleReadyz runs every registered ReadyChecker and reports 200 only if
// all of them pass; otherwise 503. Used by the Kubernetes readiness probe.
func (p *Proxy) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	checks := make(map[string]string, len(p.checkers))
	allOK := true
	for _, c := range p.checkers {
		if err := c.Check(ctx); err != nil {
			checks[c.Name()] = "error"
			allOK = false
			continue
		}
		checks[c.Name()] = "ok"
	}

	status := "ready"
	code := http.StatusOK
	if !allOK {
		status = "not_ready"
		code = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": status,
		"checks": checks,
	})
}

// MCPPathSegments parses "/mcp/{tenant_slug}/{server_name}" and returns the
// two segments. It requires exactly that shape (no trailing sub-path) since
// an MCP tool call carries everything it needs in the JSON-RPC body, not
// the URL — see spec/02-proxy.md §2. Exported so ratelimit/auth middleware
// (which run earlier in the chain than the proxy handler) can resolve the
// same server_name for scoped rate limiting without re-deriving the rule.
func MCPPathSegments(path string) (tenantSlug, serverName string, ok bool) {
	parts := strings.SplitN(path, "/", 5)
	if len(parts) != 4 || parts[1] != "mcp" || parts[2] == "" || parts[3] == "" {
		return "", "", false
	}
	return parts[2], parts[3], true
}

// forward is the innermost handler in the /mcp/ chain: it resolves the
// upstream server, forwards the request, streams (or buffers) the
// response back, and — per spec/02-proxy.md's diagram — runs plugin.After
// and writes the audit event itself rather than as separate middleware.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	tenantSlug, serverName, ok := MCPPathSegments(r.URL.Path)
	if !ok {
		writeError(w, r, http.StatusNotFound, "not found")
		return
	}

	srv, err := p.router.Resolve(r.Context(), tenantSlug, serverName)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "mcp server not registered")
		return
	}

	// Read the body now (rather than streaming it straight into the
	// upstream request) so we can parse it once for the audit event and the
	// tools/call detection SSEProxy needs — MCP request bodies are small
	// JSON-RPC envelopes, never the large payloads that flow back over SSE.
	body, err := ReadAndReplaceBody(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "failed to read request body")
		return
	}
	callReq, _ := mcp.ParseMessage(body) // best-effort; nil is fine, see SSEProxy.Forward

	upstreamReq, err := p.buildUpstreamRequest(r, srv, body)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "failed to build upstream request")
		return
	}

	resp, err := p.transport.RoundTrip(upstreamReq)
	if err != nil {
		p.log.Warn().Err(err).Str("server", serverName).Msg("upstream request failed")
		writeError(w, r, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	tenantID := TenantIDFromContext(r.Context()) // set once Phase 2 auth lands; "" today
	statusCode := resp.StatusCode

	if isSSE(resp) {
		if err := p.sse.Forward(r.Context(), w, resp, callReq, tenantID); err != nil {
			p.log.Warn().Err(err).Msg("sse forward ended with error")
		}
	} else {
		statusCode = p.forwardJSON(w, resp, callReq, tenantID)
	}

	p.writeAudit(r, callReq, tenantSlug, serverName, tenantID, statusCode, start)
}

// buildUpstreamRequest constructs the outbound request to srv.UpstreamURL,
// preserving the caller's method, headers (minus hop-by-hop ones), and
// body, and adding the identifying headers documented in
// spec/02-proxy.md §2.
func (p *Proxy) buildUpstreamRequest(r *http.Request, srv *tenant.Server, body []byte) (*http.Request, error) {
	prefix := fmt.Sprintf("/mcp/%s/%s", srv.TenantSlug, srv.Name)
	suffix := strings.TrimPrefix(r.URL.Path, prefix)
	upstreamURL := srv.UpstreamURL + suffix
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	// Deliberately NOT wrapped in a context.WithTimeout here: MCP sessions
	// are long-lived SSE streams (spec/01-mcp-protocol.md §6), so the only
	// correct bound on how long an upstream call may run is "until the
	// client disconnects or the server shuts down" — exactly what r.Context()
	// already gives us. server.timeouts.upstream instead bounds the initial
	// connect/handshake via the transport's dial and TLS handshake timeouts
	// (see newTransport).
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, newBodyReader(body))
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	req.Header = r.Header.Clone()
	for _, h := range hopByHopHeaders {
		req.Header.Del(h)
	}
	req.Header.Set("X-Conduit-Tenant-ID", TenantIDFromContext(r.Context()))
	req.Header.Set("X-Conduit-Request-ID", RequestIDFromContext(r.Context()))
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		req.Header.Set("X-Forwarded-For", xff+", "+clientIP(r))
	} else {
		req.Header.Set("X-Forwarded-For", clientIP(r))
	}

	return req, nil
}

// forwardJSON handles the non-SSE response path: read the full body, run
// plugin.After, write it back to the agent. Used for methods like
// tools/list whose upstream response is a single JSON object rather than a
// stream.
func (p *Proxy) forwardJSON(w http.ResponseWriter, resp *http.Response, callReq *mcp.Message, tenantID string) int {
	body, err := readBodyLimited(resp.Body)
	if err != nil {
		p.log.Warn().Err(err).Msg("failed to read upstream response body")
		w.WriteHeader(http.StatusBadGateway)
		return http.StatusBadGateway
	}

	respMsg, parseErr := mcp.ParseMessage(body)
	if parseErr == nil && callReq != nil {
		if modified := p.plugins.RunAfter(context.Background(), tenantID, callReq, respMsg); modified != nil {
			if out, err := json.Marshal(modified); err == nil {
				body = out
			}
		}
	}

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return resp.StatusCode
}

// writeAudit builds and enqueues the audit event for this request. It never
// blocks — Writer.Write is a non-blocking channel send (ADR-002) — so
// calling it as the very last step of forward() adds negligible latency.
func (p *Proxy) writeAudit(r *http.Request, callReq *mcp.Message, tenantSlug, serverName, tenantID string, statusCode int, start time.Time) {
	if p.auditor == nil {
		return
	}
	toolName := ""
	if callReq != nil {
		toolName = mcp.ExtractToolName(callReq)
	}
	if tenantID == "" {
		tenantID = tenantSlug // Phase 1 fallback until real auth resolves tenant_id.
	}

	p.auditor.Write(audit.Event{
		TenantID:     tenantID,
		AgentID:      AgentIDFromContext(r.Context()),
		SessionID:    RequestIDFromContext(r.Context()),
		ServerName:   serverName,
		ToolName:     toolName,
		StatusCode:   statusCode,
		LatencyMs:    int(time.Since(start).Milliseconds()),
		AuthMethod:   AuthMethodFromContext(r.Context()),
		PolicyAction: "allow", // Phase 6 policy engine sets deny/rate_limited
		TraceID:      "",      // Phase 7 OTel wiring populates this
	})
}

package ratelimit

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/proxy"
	"github.com/conduit-oss/conduit/internal/webhook"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// errorResponse mirrors proxy.ErrorResponse's JSON shape, plus the
// rate-limit-specific retry_after field spec/06-ratelimit.md §5 requires.
type errorResponse struct {
	Error      string `json:"error"`
	Code       string `json:"code"`
	RequestID  string `json:"request_id"`
	RetryAfter int    `json:"retry_after"`
}

// NewMiddleware returns the real rate-limit middleware: it derives
// (tenant_id, server_name, tool_name, agent_id) for the current request and
// checks them against limiter. On denial it returns 429 with Retry-After
// and X-RateLimit-* headers and fires the ratelimit.exceeded webhook event
// (spec/16-webhooks.md §1); on approval it still sets the informational
// X-RateLimit-* headers before calling next. dispatcher may be nil (no
// webhooks configured), in which case the deny path just skips dispatch.
//
// Wire this into the proxy with proxy.WithRateLimitMiddleware(ratelimit.NewMiddleware(...)).
func NewMiddleware(limiter *Limiter, dispatcher *webhook.Dispatcher) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID := proxy.TenantIDFromContext(r.Context())
			_, serverName, _ := proxy.MCPPathSegments(r.URL.Path)
			agentID := proxy.AgentIDFromContext(r.Context())
			toolName := extractToolName(r)

			result, err := limiter.Check(r.Context(), tenantID, serverName, toolName, agentID)
			if err != nil {
				log.Error().Err(err).Str("tenant_id", tenantID).Msg("rate limit check failed")
				writeServiceUnavailable(w, r)
				return
			}

			setRateLimitHeaders(w, result)
			if !result.Allowed {
				proxy.RateLimitDecisionsTotal.WithLabelValues(tenantID, result.Scope, "deny").Inc()
				dispatchRateLimitExceeded(dispatcher, r, tenantID, serverName, toolName, agentID, result)
				writeRateLimited(w, r, result)
				return
			}
			proxy.RateLimitDecisionsTotal.WithLabelValues(tenantID, result.Scope, "allow").Inc()
			next.ServeHTTP(w, r)
		})
	}
}

// dispatchRateLimitExceeded fires the ratelimit.exceeded webhook event in
// the background — Dispatch does its own I/O and must never block the
// request that triggered it. A no-op if dispatcher is nil or tenantID
// doesn't parse as a UUID (unauthenticated request).
func dispatchRateLimitExceeded(dispatcher *webhook.Dispatcher, r *http.Request, tenantID, serverName, toolName, agentID string, result *Result) {
	if dispatcher == nil {
		return
	}
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return
	}
	retryAfter := int(time.Until(result.ResetAt).Seconds())
	if retryAfter < 0 {
		retryAfter = 0
	}
	go dispatcher.Dispatch(context.Background(), tid, "ratelimit.exceeded", map[string]any{
		"tool_name":   toolName,
		"server_name": serverName,
		"agent_id":    agentID,
		"request_id":  proxy.RequestIDFromContext(r.Context()),
		"scope":       result.Scope,
		"limit":       result.Limit,
		"retry_after": retryAfter,
	})
}

// extractToolName peeks the request body for a tools/call method's tool
// name, leaving the body intact for downstream handlers (proxy.ReadAndReplaceBody
// makes it re-readable). Any non-POST request, or a body that isn't a
// well-formed tools/call, yields "" — rate limiting simply skips the tool
// scope in that case rather than failing the request.
func extractToolName(r *http.Request) string {
	if r.Method != http.MethodPost || r.Body == nil {
		return ""
	}
	body, err := proxy.ReadAndReplaceBody(r)
	if err != nil {
		return ""
	}
	msg, err := mcp.ParseMessage(body)
	if err != nil {
		return ""
	}
	return mcp.ExtractToolName(msg)
}

func setRateLimitHeaders(w http.ResponseWriter, result *Result) {
	if result == nil {
		return
	}
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(result.Limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(result.Remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(result.ResetAt.Unix(), 10))
}

func writeRateLimited(w http.ResponseWriter, r *http.Request, result *Result) {
	retryAfter := int(time.Until(result.ResetAt).Seconds())
	if retryAfter < 0 {
		retryAfter = 0
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:      "rate limit exceeded",
		Code:       "RATE_LIMITED",
		RequestID:  proxy.RequestIDFromContext(r.Context()),
		RetryAfter: retryAfter,
	})
}

func writeServiceUnavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:     "rate limiter unavailable",
		Code:      "SERVICE_UNAVAILABLE",
		RequestID: proxy.RequestIDFromContext(r.Context()),
	})
}

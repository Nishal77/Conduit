package ratelimit

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/proxy"
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
// and X-RateLimit-* headers; on approval it still sets the informational
// X-RateLimit-* headers before calling next.
//
// Wire this into the proxy with proxy.WithRateLimitMiddleware(ratelimit.NewMiddleware(...)).
func NewMiddleware(limiter *Limiter) func(http.Handler) http.Handler {
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
				writeRateLimited(w, r, result)
				return
			}
			proxy.RateLimitDecisionsTotal.WithLabelValues(tenantID, result.Scope, "allow").Inc()
			next.ServeHTTP(w, r)
		})
	}
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

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/conduit-oss/conduit/internal/api/httpx"
	"github.com/conduit-oss/conduit/internal/audit"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/rs/zerolog"
)

// AuditHandler implements /api/v1/audit.
type AuditHandler struct {
	audit *store.AuditStore
	log   zerolog.Logger
}

// auditEventJSON is the response shape spec/10-api.md §3 documents.
// request_args and response_meta are included here (unlike the CSV export,
// which spec/07-audit.md §5 explicitly excludes them from) since this is
// the same data an operator can already see via `conduit audit query`.
type auditEventJSON struct {
	ID           string         `json:"id"`
	TenantID     string         `json:"tenant_id"`
	AgentID      string         `json:"agent_id"`
	SessionID    string         `json:"session_id"`
	ServerName   string         `json:"server_name"`
	ToolName     string         `json:"tool_name"`
	RequestArgs  map[string]any `json:"request_args,omitempty"`
	ResponseMeta map[string]any `json:"response_meta,omitempty"`
	StatusCode   int            `json:"status_code"`
	LatencyMs    int            `json:"latency_ms"`
	AuthMethod   string         `json:"auth_method"`
	PolicyAction string         `json:"policy_action"`
	CostUSD      string         `json:"cost_usd"`
	TraceID      string         `json:"trace_id"`
	CreatedAt    string         `json:"created_at"`
}

func toAuditEventJSON(e *store.AuditEvent) auditEventJSON {
	return auditEventJSON{
		ID:           e.ID.String(),
		TenantID:     e.TenantID.String(),
		AgentID:      e.AgentID,
		SessionID:    e.SessionID,
		ServerName:   e.ServerName,
		ToolName:     e.ToolName,
		RequestArgs:  e.RequestArgs,
		ResponseMeta: e.ResponseMeta,
		StatusCode:   e.StatusCode,
		LatencyMs:    e.LatencyMs,
		AuthMethod:   e.AuthMethod,
		PolicyAction: e.PolicyAction,
		CostUSD:      fmt.Sprintf("%.8f", e.CostUSD),
		TraceID:      e.TraceID,
		CreatedAt:    e.CreatedAt.Format(rfc3339),
	}
}

// auditQueryResponse is the exact envelope spec/10-api.md §3 documents for
// GET /api/v1/audit/events — note this uses "events"/"total"/"limit"/"offset"
// rather than the generic listResponse's "items", matching the spec's
// example response body exactly.
type auditQueryResponse struct {
	Events []auditEventJSON `json:"events"`
	Total  int64            `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

// parseAuditQuery builds a store.AuditQuery from the common query
// parameters shared by /audit/events and /audit/export.
func parseAuditQuery(w http.ResponseWriter, r *http.Request) (store.AuditQuery, bool) {
	tenantID, ok := parseUUIDQuery(w, r, "tenant_id")
	if !ok {
		return store.AuditQuery{}, false
	}

	q := store.AuditQuery{TenantID: tenantID, OrderDesc: true}

	if v := r.URL.Query().Get("from"); v != "" {
		t, err := parseAuditTime(v)
		if err != nil {
			httpx.WriteValidationError(w, r, map[string]string{"from": err.Error()})
			return store.AuditQuery{}, false
		}
		q.FromTime = &t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := parseAuditTime(v)
		if err != nil {
			httpx.WriteValidationError(w, r, map[string]string{"to": err.Error()})
			return store.AuditQuery{}, false
		}
		q.ToTime = &t
	}
	if v := r.URL.Query().Get("tool_name"); v != "" {
		q.ToolName = &v
	}
	if v := r.URL.Query().Get("server_name"); v != "" {
		q.ServerName = &v
	}
	if v := r.URL.Query().Get("policy_action"); v != "" {
		q.PolicyAction = &v
	}

	pagination, err := httpx.ParsePagination(r)
	if err != nil {
		var fe *httpx.FieldError
		if errors.As(err, &fe) {
			httpx.WriteValidationError(w, r, map[string]string{fe.Field: fe.Message})
		} else {
			httpx.WriteError(w, r, http.StatusBadRequest, err.Error())
		}
		return store.AuditQuery{}, false
	}
	q.Limit, q.Offset = pagination.Limit, pagination.Offset

	return q, true
}

// parseAuditTime accepts RFC3339 or a relative duration like "24h"/"7d",
// matching spec/08-cli.md §9's query language (shared here since the API
// documents the identical from/to format in spec/10-api.md §3).
func parseAuditTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if strings.HasSuffix(s, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return time.Now().Add(-time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf(`must be RFC3339 or a duration like "24h"/"7d"`)
}

// Query handles GET /api/v1/audit/events.
func (h *AuditHandler) Query(w http.ResponseWriter, r *http.Request) {
	q, ok := parseAuditQuery(w, r)
	if !ok {
		return
	}

	events, total, err := h.audit.Query(r.Context(), q)
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to query audit events")
		return
	}

	items := make([]auditEventJSON, len(events))
	for i, e := range events {
		items[i] = toAuditEventJSON(e)
	}
	httpx.WriteJSON(w, http.StatusOK, auditQueryResponse{Events: items, Total: total, Limit: q.Limit, Offset: q.Offset})
}

// Export handles GET /api/v1/audit/export: a streaming CSV or JSON download
// of every matching event, no limit/offset (spec/10-api.md §3's
// ExportAuditEvents — full export, not a page).
func (h *AuditHandler) Export(w http.ResponseWriter, r *http.Request) {
	q, ok := parseAuditQuery(w, r)
	if !ok {
		return
	}
	q.Limit, q.Offset = 0, 0 // ExportCSV pages internally; ignore pagination params here

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="audit_export.csv"`)
		if err := audit.ExportCSV(r.Context(), h.audit, q, w); err != nil {
			h.log.Error().Err(err).Msg("audit csv export failed mid-stream")
		}
	case "json":
		result, err := audit.Query(r.Context(), h.audit, q)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "failed to export audit events")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, result.Events)
	default:
		httpx.WriteValidationError(w, r, map[string]string{"format": `must be "csv" or "json"`})
	}
}

// Stream handles GET /api/v1/audit/stream: an SSE feed of new audit events
// for a tenant, consumed by the dashboard's /traffic page (spec/11-dashboard.md §4).
func (h *AuditHandler) Stream(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseUUIDQuery(w, r, "tenant_id")
	if !ok {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.WriteError(w, r, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	events, err := h.audit.Stream(r.Context(), tenantID, store.AuditStreamFilter{})
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to start audit stream")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for e := range events {
		payload, err := json.Marshal(toAuditEventJSON(e))
		if err != nil {
			continue
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return
		}
		flusher.Flush()
	}
}

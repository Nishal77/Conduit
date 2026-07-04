package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/conduit-oss/conduit/internal/api/httpx"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/google/uuid"
)

// RateLimitsHandler implements /api/v1/rate-limits.
type RateLimitsHandler struct {
	rateLimits *store.RateLimitStore
}

var validScopes = map[string]bool{"tenant": true, "server": true, "tool": true, "agent": true}

// rateLimitJSON is the response shape spec/10-api.md §3 documents.
type rateLimitJSON struct {
	ID        string  `json:"id"`
	TenantID  string  `json:"tenant_id"`
	Scope     string  `json:"scope"`
	Target    *string `json:"target"`
	Requests  int     `json:"requests"`
	WindowSec int     `json:"window_sec"`
	Burst     *int    `json:"burst"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

func toRateLimitJSON(c *store.RateLimitConfig) rateLimitJSON {
	return rateLimitJSON{
		ID:        c.ID.String(),
		TenantID:  c.TenantID.String(),
		Scope:     c.Scope,
		Target:    c.Target,
		Requests:  c.Requests,
		WindowSec: c.WindowSec,
		Burst:     c.Burst,
		CreatedAt: c.CreatedAt.Format(rfc3339),
		UpdatedAt: c.UpdatedAt.Format(rfc3339),
	}
}

// List handles GET /api/v1/rate-limits?tenant_id={id}.
func (h *RateLimitsHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseUUIDQuery(w, r, "tenant_id")
	if !ok {
		return
	}

	configs, err := h.rateLimits.List(r.Context(), tenantID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to list rate limit configs")
		return
	}

	items := make([]rateLimitJSON, len(configs))
	for i, c := range configs {
		items[i] = toRateLimitJSON(c)
	}
	httpx.WriteJSON(w, http.StatusOK, listResponse[rateLimitJSON]{Items: items, Total: int64(len(items)), Limit: len(items)})
}

// upsertRateLimitRequest is the request body for PUT /api/v1/rate-limits.
type upsertRateLimitRequest struct {
	TenantID  string  `json:"tenant_id"`
	Scope     string  `json:"scope"`
	Target    *string `json:"target"`
	Requests  int     `json:"requests"`
	WindowSec int     `json:"window_sec"`
	Burst     *int    `json:"burst"`
}

// Upsert handles PUT /api/v1/rate-limits.
func (h *RateLimitsHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	var req upsertRateLimitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}

	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		httpx.WriteValidationError(w, r, map[string]string{"tenant_id": "must be a UUID"})
		return
	}
	if details := validateRateLimit(req); len(details) > 0 {
		httpx.WriteValidationError(w, r, details)
		return
	}

	config, err := h.rateLimits.Upsert(r.Context(), store.UpsertRateLimitInput{
		TenantID:  tenantID,
		Scope:     req.Scope,
		Target:    req.Target,
		Requests:  req.Requests,
		WindowSec: req.WindowSec,
		Burst:     req.Burst,
	})
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to upsert rate limit config")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, toRateLimitJSON(config))
}

func validateRateLimit(req upsertRateLimitRequest) map[string]string {
	details := map[string]string{}
	if !validScopes[req.Scope] {
		details["scope"] = "must be one of: tenant, server, tool, agent"
	}
	if req.Requests <= 0 {
		details["requests"] = "must be positive"
	}
	if req.WindowSec <= 0 {
		details["window_sec"] = "must be positive"
	}
	return details
}

// Delete handles DELETE /api/v1/rate-limits/{id}.
func (h *RateLimitsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.rateLimits.Delete(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "rate limit config not found")
		return
	} else if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to delete rate limit config")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

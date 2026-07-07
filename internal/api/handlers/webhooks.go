package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/conduit-oss/conduit/internal/api/httpx"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/google/uuid"
)

// WebhooksHandler implements /api/v1/webhooks.
type WebhooksHandler struct {
	webhooks *store.WebhookStore
}

var validWebhookEvents = map[string]bool{
	"ratelimit.exceeded":    true,
	"policy.violation":      true,
	"tool.call.success":     true,
	"tool.call.error":       true,
	"apikey.revoked":        true,
	"server.health.down":    true,
	"server.health.up":      true,
	"audit.budget.exceeded": true,
}

// webhookJSON is the response shape. secret is deliberately absent — it
// signs every delivery and must never be echoed back once set, the same
// rule api_keys and oauth_applications follow for their own secrets.
type webhookJSON struct {
	ID        string   `json:"id"`
	TenantID  string   `json:"tenant_id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Events    []string `json:"events"`
	Enabled   bool     `json:"enabled"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

func toWebhookJSON(c *store.WebhookConfig) webhookJSON {
	return webhookJSON{
		ID:        c.ID.String(),
		TenantID:  c.TenantID.String(),
		Name:      c.Name,
		URL:       c.URL,
		Events:    c.Events,
		Enabled:   c.Enabled,
		CreatedAt: c.CreatedAt.Format(rfc3339),
		UpdatedAt: c.UpdatedAt.Format(rfc3339),
	}
}

// List handles GET /api/v1/webhooks?tenant_id={id}.
func (h *WebhooksHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseUUIDQuery(w, r, "tenant_id")
	if !ok {
		return
	}

	webhooks, err := h.webhooks.ListByTenant(r.Context(), tenantID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to list webhooks")
		return
	}

	items := make([]webhookJSON, len(webhooks))
	for i, c := range webhooks {
		items[i] = toWebhookJSON(c)
	}
	httpx.WriteJSON(w, http.StatusOK, listResponse[webhookJSON]{Items: items, Total: int64(len(items)), Limit: len(items)})
}

// createWebhookRequest is the request body for POST /api/v1/webhooks.
type createWebhookRequest struct {
	TenantID string   `json:"tenant_id"`
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Secret   string   `json:"secret"`
	Events   []string `json:"events"`
}

// Create handles POST /api/v1/webhooks.
func (h *WebhooksHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}

	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		httpx.WriteValidationError(w, r, map[string]string{"tenant_id": "must be a UUID"})
		return
	}
	if details := validateWebhook(req); len(details) > 0 {
		httpx.WriteValidationError(w, r, details)
		return
	}

	created, err := h.webhooks.Create(r.Context(), store.CreateWebhookInput{
		TenantID: tenantID,
		Name:     req.Name,
		URL:      req.URL,
		Secret:   req.Secret,
		Events:   req.Events,
	})
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to create webhook")
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toWebhookJSON(created))
}

func validateWebhook(req createWebhookRequest) map[string]string {
	details := map[string]string{}
	if req.Name == "" {
		details["name"] = "is required"
	}
	if req.URL == "" {
		details["url"] = "is required"
	}
	if req.Secret == "" {
		details["secret"] = "is required"
	}
	if len(req.Events) == 0 {
		details["events"] = "must include at least one event type"
	}
	for _, e := range req.Events {
		if !validWebhookEvents[e] {
			details["events"] = "contains an unknown event type: " + e
			break
		}
	}
	return details
}

// updateWebhookRequest is the request body for PATCH /api/v1/webhooks/{id}.
type updateWebhookRequest struct {
	Name    *string  `json:"name"`
	URL     *string  `json:"url"`
	Events  []string `json:"events"`
	Enabled *bool    `json:"enabled"`
}

// Update handles PATCH /api/v1/webhooks/{id}.
func (h *WebhooksHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}

	var req updateWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	for _, e := range req.Events {
		if !validWebhookEvents[e] {
			httpx.WriteValidationError(w, r, map[string]string{"events": "contains an unknown event type: " + e})
			return
		}
	}

	updated, err := h.webhooks.Update(r.Context(), id, store.WebhookUpdates{
		Name:    req.Name,
		URL:     req.URL,
		Events:  req.Events,
		Enabled: req.Enabled,
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "webhook not found")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to update webhook")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toWebhookJSON(updated))
}

// Delete handles DELETE /api/v1/webhooks/{id}.
func (h *WebhooksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.webhooks.Delete(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "webhook not found")
		return
	} else if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to delete webhook")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

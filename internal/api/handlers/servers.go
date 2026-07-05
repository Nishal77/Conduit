package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/conduit-oss/conduit/internal/api/httpx"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/conduit-oss/conduit/internal/tenant"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ServersHandler implements /api/v1/servers. routing is invalidated after
// every mutation — see invalidateRouting in tenants.go.
type ServersHandler struct {
	servers *store.MCPServerStore
	routing *tenant.Store
	log     zerolog.Logger
}

var validAuthTypes = map[string]bool{"none": true, "bearer": true, "basic": true, "api_key": true}

// serverJSON is the response shape spec/10-api.md §3 documents. auth_config
// is deliberately absent — it holds upstream credentials and must never be
// echoed back.
type serverJSON struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenant_id"`
	Name           string `json:"name"`
	UpstreamURL    string `json:"upstream_url"`
	AuthType       string `json:"auth_type"`
	HealthCheckURL string `json:"health_check_url,omitempty"`
	Weight         int    `json:"weight"`
	Enabled        bool   `json:"enabled"`
	CreatedAt      string `json:"created_at"`
}

func toServerJSON(s *store.MCPServer) serverJSON {
	healthURL := ""
	if s.HealthCheckURL != nil {
		healthURL = *s.HealthCheckURL
	}
	return serverJSON{
		ID:             s.ID.String(),
		TenantID:       s.TenantID.String(),
		Name:           s.Name,
		UpstreamURL:    s.UpstreamURL,
		AuthType:       s.AuthType,
		HealthCheckURL: healthURL,
		Weight:         s.Weight,
		Enabled:        s.Enabled,
		CreatedAt:      s.CreatedAt.Format(rfc3339),
	}
}

// List handles GET /api/v1/servers?tenant_id={id}.
func (h *ServersHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseUUIDQuery(w, r, "tenant_id")
	if !ok {
		return
	}

	servers, err := h.servers.ListByTenant(r.Context(), tenantID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to list servers")
		return
	}

	items := make([]serverJSON, len(servers))
	for i, s := range servers {
		items[i] = toServerJSON(s)
	}
	httpx.WriteJSON(w, http.StatusOK, listResponse[serverJSON]{Items: items, Total: int64(len(items)), Limit: len(items)})
}

// createServerRequest is the request body for POST /api/v1/servers.
type createServerRequest struct {
	TenantID       string         `json:"tenant_id"`
	Name           string         `json:"name"`
	UpstreamURL    string         `json:"upstream_url"`
	AuthType       string         `json:"auth_type"`
	AuthConfig     map[string]any `json:"auth_config"`
	HealthCheckURL string         `json:"health_check_url"`
	Weight         int            `json:"weight"`
}

// Create handles POST /api/v1/servers.
func (h *ServersHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.AuthType == "" {
		req.AuthType = "none"
	}
	if req.Weight == 0 {
		req.Weight = 100
	}

	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		httpx.WriteValidationError(w, r, map[string]string{"tenant_id": "must be a UUID"})
		return
	}
	if details := validateServer(req); len(details) > 0 {
		httpx.WriteValidationError(w, r, details)
		return
	}

	var healthCheckURL *string
	if req.HealthCheckURL != "" {
		healthCheckURL = &req.HealthCheckURL
	}

	created, err := h.servers.Create(r.Context(), store.CreateServerInput{
		TenantID:       tenantID,
		Name:           req.Name,
		UpstreamURL:    req.UpstreamURL,
		AuthType:       req.AuthType,
		AuthConfig:     req.AuthConfig,
		HealthCheckURL: healthCheckURL,
		Weight:         req.Weight,
	})
	if errors.Is(err, store.ErrConflict) {
		httpx.WriteError(w, r, http.StatusConflict, "a server named '"+req.Name+"' already exists for this tenant")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to register server")
		return
	}

	invalidateRouting(h.routing, r)
	httpx.WriteJSON(w, http.StatusCreated, toServerJSON(created))
}

func validateServer(req createServerRequest) map[string]string {
	details := map[string]string{}
	if req.Name == "" {
		details["name"] = "is required"
	}
	if req.UpstreamURL == "" {
		details["upstream_url"] = "is required"
	}
	if !validAuthTypes[req.AuthType] {
		details["auth_type"] = "must be one of: none, bearer, basic, api_key"
	}
	if req.Weight < 0 {
		details["weight"] = "must be positive"
	}
	return details
}

// Get handles GET /api/v1/servers/{serverID}.
func (h *ServersHandler) Get(w http.ResponseWriter, r *http.Request) {
	server, ok := h.findByID(w, r)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toServerJSON(server))
}

// Update handles PATCH /api/v1/servers/{serverID}.
type updateServerRequest struct {
	UpstreamURL    *string        `json:"upstream_url"`
	AuthType       *string        `json:"auth_type"`
	AuthConfig     map[string]any `json:"auth_config"`
	HealthCheckURL *string        `json:"health_check_url"`
	Weight         *int           `json:"weight"`
	Enabled        *bool          `json:"enabled"`
}

func (h *ServersHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "serverID")
	if !ok {
		return
	}

	var req updateServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.AuthType != nil && !validAuthTypes[*req.AuthType] {
		httpx.WriteValidationError(w, r, map[string]string{"auth_type": "must be one of: none, bearer, basic, api_key"})
		return
	}

	updated, err := h.servers.Update(r.Context(), id, store.ServerUpdates{
		UpstreamURL:    req.UpstreamURL,
		AuthType:       req.AuthType,
		AuthConfig:     req.AuthConfig,
		HealthCheckURL: req.HealthCheckURL,
		Weight:         req.Weight,
		Enabled:        req.Enabled,
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to update server")
		return
	}
	invalidateRouting(h.routing, r)
	httpx.WriteJSON(w, http.StatusOK, toServerJSON(updated))
}

// Delete handles DELETE /api/v1/servers/{serverID}.
func (h *ServersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "serverID")
	if !ok {
		return
	}
	if err := h.servers.Delete(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "server not found")
		return
	} else if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to delete server")
		return
	}
	invalidateRouting(h.routing, r)
	w.WriteHeader(http.StatusNoContent)
}

// Health handles GET /api/v1/servers/{serverID}/health: it pings the
// server's configured health_check_url (if any) and reports the result.
// This is a live check, not a cached status — spec/11-dashboard.md's
// dashboard polls it every 30s itself.
func (h *ServersHandler) Health(w http.ResponseWriter, r *http.Request) {
	server, ok := h.findByID(w, r)
	if !ok {
		return
	}

	if server.HealthCheckURL == nil || *server.HealthCheckURL == "" {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "unknown", "reason": "no health_check_url configured"})
		return
	}

	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(*server.HealthCheckURL)
	if err != nil {
		h.log.Warn().Err(err).Str("server_id", server.ID.String()).Msg("server health check failed")
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "error", "reason": err.Error()})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "error", "status_code": resp.StatusCode})
}

// findByID resolves {serverID} to a server via store.MCPServerStore.GetByID.
func (h *ServersHandler) findByID(w http.ResponseWriter, r *http.Request) (*store.MCPServer, bool) {
	id, ok := parseUUIDParam(w, r, "serverID")
	if !ok {
		return nil, false
	}
	server, err := h.servers.GetByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "server not found")
		return nil, false
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to get server")
		return nil, false
	}
	return server, true
}

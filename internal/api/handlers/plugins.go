package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/conduit-oss/conduit/internal/api/httpx"
	"github.com/conduit-oss/conduit/internal/store"
)

// PluginsHandler implements /api/v1/plugins: the read-only catalog, and a
// tenant's per-plugin enable/config/priority CRUD.
type PluginsHandler struct {
	plugins *store.PluginStore
}

// pluginJSON is the catalog entry shape — the plugins table itself, not a
// tenant's configuration of it (see tenantPluginJSON for that).
type pluginJSON struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	PluginType   string         `json:"plugin_type"`
	Description  string         `json:"description,omitempty"`
	ConfigSchema map[string]any `json:"config_schema"`
	CreatedAt    string         `json:"created_at"`
}

func toPluginJSON(p *store.Plugin) pluginJSON {
	schema := p.ConfigSchema
	if schema == nil {
		schema = map[string]any{}
	}
	return pluginJSON{
		ID:           p.ID.String(),
		Name:         p.Name,
		Version:      p.Version,
		PluginType:   p.PluginType,
		Description:  p.Description,
		ConfigSchema: schema,
		CreatedAt:    p.CreatedAt.Format(rfc3339),
	}
}

// List handles GET /api/v1/plugins: the full catalog, regardless of
// whether any tenant has enabled a given plugin.
func (h *PluginsHandler) List(w http.ResponseWriter, r *http.Request) {
	plugins, err := h.plugins.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to list plugins")
		return
	}

	items := make([]pluginJSON, len(plugins))
	for i, p := range plugins {
		items[i] = toPluginJSON(p)
	}
	httpx.WriteJSON(w, http.StatusOK, listResponse[pluginJSON]{Items: items, Total: int64(len(items)), Limit: len(items)})
}

// tenantPluginJSON is one tenant's configuration of a catalog plugin.
type tenantPluginJSON struct {
	ID         string         `json:"id"`
	TenantID   string         `json:"tenant_id"`
	PluginID   string         `json:"plugin_id"`
	PluginName string         `json:"plugin_name,omitempty"`
	Enabled    bool           `json:"enabled"`
	Config     map[string]any `json:"config"`
	Priority   int            `json:"priority"`
	CreatedAt  string         `json:"created_at"`
	UpdatedAt  string         `json:"updated_at"`
}

func toTenantPluginJSON(tp *store.TenantPlugin) tenantPluginJSON {
	config := tp.Config
	if config == nil {
		config = map[string]any{}
	}
	return tenantPluginJSON{
		ID:        tp.ID.String(),
		TenantID:  tp.TenantID.String(),
		PluginID:  tp.PluginID.String(),
		Enabled:   tp.Enabled,
		Config:    config,
		Priority:  tp.Priority,
		CreatedAt: tp.CreatedAt.Format(rfc3339),
		UpdatedAt: tp.UpdatedAt.Format(rfc3339),
	}
}

// ListForTenant handles GET /api/v1/tenants/{tenantID}/plugins: every
// plugin the tenant has configured (enabled or not).
func (h *PluginsHandler) ListForTenant(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseUUIDParam(w, r, "tenantID")
	if !ok {
		return
	}

	configured, err := h.plugins.ListByTenant(r.Context(), tenantID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to list tenant plugins")
		return
	}

	items := make([]tenantPluginJSON, len(configured))
	for i, tp := range configured {
		items[i] = toTenantPluginJSON(tp)
	}
	httpx.WriteJSON(w, http.StatusOK, listResponse[tenantPluginJSON]{Items: items, Total: int64(len(items)), Limit: len(items)})
}

// upsertTenantPluginRequest is the request body for
// PUT /api/v1/tenants/{tenantID}/plugins/{pluginID}.
type upsertTenantPluginRequest struct {
	Enabled  bool           `json:"enabled"`
	Config   map[string]any `json:"config"`
	Priority int            `json:"priority"`
}

// Upsert handles PUT /api/v1/tenants/{tenantID}/plugins/{pluginID}: enable,
// disable, or reconfigure a plugin for a tenant. The proxy's DB-backed
// plugin loader (cmd/conduit) picks this up on its next refresh, within
// pluginRefreshInterval — there's no cache to explicitly invalidate here
// the way multi-tenant routing needs (see tenant.Store.Invalidate),
// because a plugin config change isn't security-sensitive the way a
// routing change is.
func (h *PluginsHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseUUIDParam(w, r, "tenantID")
	if !ok {
		return
	}
	pluginID, ok := parseUUIDParam(w, r, "pluginID")
	if !ok {
		return
	}

	var req upsertTenantPluginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if _, err := h.plugins.GetByID(r.Context(), pluginID); errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "plugin not found")
		return
	} else if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to look up plugin")
		return
	}

	tp, err := h.plugins.Upsert(r.Context(), store.UpsertTenantPluginInput{
		TenantID: tenantID,
		PluginID: pluginID,
		Enabled:  req.Enabled,
		Config:   req.Config,
		Priority: req.Priority,
	})
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to upsert tenant plugin")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTenantPluginJSON(tp))
}

// Delete handles DELETE /api/v1/tenants/{tenantID}/plugins/{id}, reverting
// the tenant's plugin to "not configured".
func (h *PluginsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.plugins.Delete(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "tenant plugin config not found")
		return
	} else if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to delete tenant plugin config")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

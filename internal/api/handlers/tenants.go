package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/conduit-oss/conduit/internal/api/httpx"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// TenantsHandler implements /api/v1/tenants.
type TenantsHandler struct {
	tenants *store.TenantStore
}

// tenantJSON is the exact response shape spec/10-api.md §3 documents.
type tenantJSON struct {
	ID        string         `json:"id"`
	Slug      string         `json:"slug"`
	Name      string         `json:"name"`
	Plan      string         `json:"plan"`
	Settings  map[string]any `json:"settings"`
	CreatedAt string         `json:"created_at"`
}

func toTenantJSON(t *store.Tenant) tenantJSON {
	settings := t.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	return tenantJSON{
		ID:        t.ID.String(),
		Slug:      t.Slug,
		Name:      t.Name,
		Plan:      t.Plan,
		Settings:  settings,
		CreatedAt: t.CreatedAt.Format(rfc3339),
	}
}

// rfc3339 is the timestamp format spec/10-api.md §1 mandates for every API
// response ("2006-01-02T15:04:05Z07:00" — Go's RFC3339 layout constant is
// exactly that string).
const rfc3339 = "2006-01-02T15:04:05Z07:00"

var slugPattern = regexp.MustCompile(`^[a-z0-9-]{3,64}$`)

var validPlans = map[string]bool{"free": true, "pro": true, "enterprise": true}

// List handles GET /api/v1/tenants.
func (h *TenantsHandler) List(w http.ResponseWriter, r *http.Request) {
	tenants, err := h.tenants.List(r.Context())
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to list tenants")
		return
	}

	items := make([]tenantJSON, len(tenants))
	for i, t := range tenants {
		items[i] = toTenantJSON(t)
	}
	httpx.WriteJSON(w, http.StatusOK, listResponse[tenantJSON]{Items: items, Total: int64(len(items)), Limit: len(items), Offset: 0, HasMore: false})
}

// createTenantRequest is the request body for POST /api/v1/tenants.
type createTenantRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Plan string `json:"plan"`
}

// Create handles POST /api/v1/tenants.
func (h *TenantsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Plan == "" {
		req.Plan = "free"
	}
	if details := validateTenant(req); len(details) > 0 {
		httpx.WriteValidationError(w, r, details)
		return
	}

	tenant, err := h.tenants.Create(r.Context(), req.Slug, req.Name, req.Plan)
	if errors.Is(err, store.ErrConflict) {
		httpx.WriteError(w, r, http.StatusConflict, "tenant with slug '"+req.Slug+"' already exists")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to create tenant")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, toTenantJSON(tenant))
}

func validateTenant(req createTenantRequest) map[string]string {
	details := map[string]string{}
	if !slugPattern.MatchString(req.Slug) {
		details["slug"] = "must match pattern ^[a-z0-9-]{3,64}$"
	}
	if req.Name == "" {
		details["name"] = "is required"
	}
	if !validPlans[req.Plan] {
		details["plan"] = "must be one of: free, pro, enterprise"
	}
	return details
}

// Get handles GET /api/v1/tenants/{tenantID}.
func (h *TenantsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "tenantID")
	if !ok {
		return
	}

	tenant, err := h.tenants.GetByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "tenant not found")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to get tenant")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTenantJSON(tenant))
}

// updateTenantRequest is the request body for PATCH /api/v1/tenants/{tenantID}.
// All fields are optional; only the ones present are changed.
type updateTenantRequest struct {
	Name     *string        `json:"name"`
	Plan     *string        `json:"plan"`
	Settings map[string]any `json:"settings"`
}

// Update handles PATCH /api/v1/tenants/{tenantID}.
func (h *TenantsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "tenantID")
	if !ok {
		return
	}

	var req updateTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Plan != nil && !validPlans[*req.Plan] {
		httpx.WriteValidationError(w, r, map[string]string{"plan": "must be one of: free, pro, enterprise"})
		return
	}

	tenant, err := h.tenants.Update(r.Context(), id, store.TenantUpdates{
		Name:     req.Name,
		Plan:     req.Plan,
		Settings: req.Settings,
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "tenant not found")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to update tenant")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTenantJSON(tenant))
}

// Delete handles DELETE /api/v1/tenants/{tenantID} (soft-delete).
func (h *TenantsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "tenantID")
	if !ok {
		return
	}

	if err := h.tenants.Delete(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "tenant not found")
		return
	} else if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to delete tenant")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listResponse is the pagination envelope every list endpoint uses
// (spec/10-api.md §5).
type listResponse[T any] struct {
	Items   []T   `json:"items"`
	Total   int64 `json:"total"`
	Limit   int   `json:"limit"`
	Offset  int   `json:"offset"`
	HasMore bool  `json:"has_more"`
}

// parseUUIDParam reads a chi URL param and parses it as a UUID, writing a
// 400 (and returning ok=false) if it isn't one.
func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	raw := chi.URLParam(r, name)
	id, err := uuid.Parse(raw)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid "+name+": must be a UUID")
		return uuid.UUID{}, false
	}
	return id, true
}

// parseUUIDQuery is parseUUIDParam's counterpart for query-string
// parameters like ?tenant_id=..., used by every list endpoint scoped to a
// tenant.
func parseUUIDQuery(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		httpx.WriteValidationError(w, r, map[string]string{name: "is required"})
		return uuid.UUID{}, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		httpx.WriteValidationError(w, r, map[string]string{name: "must be a UUID"})
		return uuid.UUID{}, false
	}
	return id, true
}

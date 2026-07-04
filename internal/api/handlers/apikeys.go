package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/conduit-oss/conduit/internal/api/httpx"
	"github.com/conduit-oss/conduit/internal/auth"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/google/uuid"
)

// APIKeysHandler implements /api/v1/api-keys.
type APIKeysHandler struct {
	apiKeys *store.APIKeyStore
	tenants *store.TenantStore
}

// apiKeyJSON is the response shape spec/10-api.md §3 documents. key_hash
// never appears here.
type apiKeyJSON struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  string     `json:"created_at"`
}

func toAPIKeyJSON(k *store.APIKey) apiKeyJSON {
	return apiKeyJSON{
		ID:         k.ID.String(),
		TenantID:   k.TenantID.String(),
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		Scopes:     k.Scopes,
		ExpiresAt:  k.ExpiresAt,
		LastUsedAt: k.LastUsedAt,
		CreatedAt:  k.CreatedAt.Format(rfc3339),
	}
}

// List handles GET /api/v1/api-keys?tenant_id={id}.
func (h *APIKeysHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseUUIDQuery(w, r, "tenant_id")
	if !ok {
		return
	}

	keys, err := h.apiKeys.List(r.Context(), tenantID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to list api keys")
		return
	}

	items := make([]apiKeyJSON, len(keys))
	for i, k := range keys {
		items[i] = toAPIKeyJSON(k)
	}
	httpx.WriteJSON(w, http.StatusOK, listResponse[apiKeyJSON]{Items: items, Total: int64(len(items)), Limit: len(items)})
}

// createAPIKeyRequest is the request body for POST /api/v1/api-keys.
type createAPIKeyRequest struct {
	TenantID  string   `json:"tenant_id"`
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresIn *string  `json:"expires_in"` // "7d" | "30d" | "90d" | "1y" | null
}

// apiKeyWithSecretJSON is returned only on creation — the one time the raw
// key is ever visible (spec/10-api.md §3).
type apiKeyWithSecretJSON struct {
	apiKeyJSON
	Key string `json:"key"`
}

// Create handles POST /api/v1/api-keys.
func (h *APIKeysHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}

	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		httpx.WriteValidationError(w, r, map[string]string{"tenant_id": "must be a UUID"})
		return
	}
	if req.Name == "" {
		httpx.WriteValidationError(w, r, map[string]string{"name": "is required"})
		return
	}
	if _, err := h.tenants.GetByID(r.Context(), tenantID); errors.Is(err, store.ErrNotFound) {
		httpx.WriteValidationError(w, r, map[string]string{"tenant_id": "tenant not found"})
		return
	} else if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to look up tenant")
		return
	}

	if len(req.Scopes) == 0 {
		req.Scopes = []string{"mcp:call"}
	}

	var expiresAt *time.Time
	if req.ExpiresIn != nil && *req.ExpiresIn != "" {
		t, err := parseExpiryDuration(*req.ExpiresIn)
		if err != nil {
			httpx.WriteValidationError(w, r, map[string]string{"expires_in": err.Error()})
			return
		}
		expiresAt = t
	}

	rawKey, keyHash, keyPrefix, err := auth.GenerateAPIKey()
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to generate api key")
		return
	}

	created, err := h.apiKeys.Create(r.Context(), tenantID, req.Name, keyHash, keyPrefix, req.Scopes, expiresAt)
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to create api key")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, apiKeyWithSecretJSON{apiKeyJSON: toAPIKeyJSON(created), Key: rawKey})
}

// Revoke handles DELETE /api/v1/api-keys/{keyID}.
func (h *APIKeysHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "keyID")
	if !ok {
		return
	}

	if err := h.apiKeys.Revoke(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "api key not found")
		return
	} else if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to revoke api key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseExpiryDuration parses "7d" | "30d" | "90d" | "1y" (spec/10-api.md §3)
// into an absolute expiry timestamp.
func parseExpiryDuration(s string) (*time.Time, error) {
	var days int
	switch s {
	case "7d":
		days = 7
	case "30d":
		days = 30
	case "90d":
		days = 90
	case "1y":
		days = 365
	default:
		return nil, errors.New(`must be one of: "7d", "30d", "90d", "1y"`)
	}
	t := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	return &t, nil
}

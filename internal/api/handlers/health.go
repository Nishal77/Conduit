package handlers

import (
	"net/http"

	"github.com/conduit-oss/conduit/internal/api/httpx"
)

// HealthHandler serves GET /api/v1/health — the only unauthenticated route
// on the management API (spec/10-api.md §2).
type HealthHandler struct{}

// Get always returns 200: this endpoint answers "is the process up", the
// same liveness-only contract as the proxy's /healthz. A database outage
// shows up on the individual resource endpoints (500s), not here.
func (h *HealthHandler) Get(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Package middleware holds the management API's own HTTP middleware —
// separate from internal/proxy's, since the two APIs run on different
// ports with different concerns (spec/10-api.md §1).
package middleware

import (
	"net/http"
	"strings"

	"github.com/conduit-oss/conduit/internal/api/httpx"
	"github.com/conduit-oss/conduit/internal/auth"
)

// Auth validates every management API request's Authorization header
// against keyValidator.
//
// spec/10-api.md §1 says auth is "JWT Bearer (same JWT as proxy) or a
// dedicated admin API key" — but JWT issuance doesn't exist until Phase 5's
// OAuth server, and neither does a distinct admin scope. Until then, any
// caller holding a valid Conduit API key (any tenant's) can manage any
// tenant's resources through this API. That's real authentication (you
// must possess a legitimate, non-revoked credential) but not yet
// authorization scoped to "your own tenant only" — Phase 5's RBAC
// (`users`, `tenant_members` tables + role enforcement, per CLAUDE.md §5's
// build plan) narrows this. Until then, treat this API's network exposure
// like the proxy's: reachable only from trusted operators, not agents.
func Auth(keyValidator *auth.APIKeyValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := extractBearer(r)
			if !ok {
				httpx.WriteError(w, r, http.StatusUnauthorized, "missing or malformed Authorization header")
				return
			}

			if _, err := keyValidator.Validate(r.Context(), token); err != nil {
				httpx.WriteError(w, r, http.StatusUnauthorized, "invalid API key")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func extractBearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

package middleware

import (
	"net/http"
	"strings"
)

// corsAllowedMethods, corsAllowedHeaders, and corsExposedHeaders are fixed
// per spec/10-api.md §6 — only the allowed origin list varies by
// deployment (dashboardOrigins).
const (
	corsAllowedMethods = "GET, POST, PATCH, PUT, DELETE, OPTIONS"
	corsAllowedHeaders = "Authorization, Content-Type, X-Request-ID"
	corsExposedHeaders = "X-Request-ID, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset"
	corsMaxAge         = "3600"
)

// CORS returns a middleware that allows cross-origin requests from
// dashboardOrigins (typically the dashboard's own origin in production; an
// empty slice allows every origin, which is fine for local development but
// should always be set explicitly in production).
func CORS(dashboardOrigins []string) func(http.Handler) http.Handler {
	allowAll := len(dashboardOrigins) == 0

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && (allowAll || originAllowed(origin, dashboardOrigins)) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", corsAllowedMethods)
				w.Header().Set("Access-Control-Allow-Headers", corsAllowedHeaders)
				w.Header().Set("Access-Control-Expose-Headers", corsExposedHeaders)
				w.Header().Set("Access-Control-Max-Age", corsMaxAge)
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if strings.EqualFold(a, origin) {
			return true
		}
	}
	return false
}

// JSONContentType sets Content-Type: application/json on every response,
// per spec/10-api.md §1. Handlers that need a different type (the CSV
// export) overwrite it themselves before writing.
func JSONContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

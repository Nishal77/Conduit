package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/conduit-oss/conduit/internal/proxy"
)

// errorResponse mirrors proxy.ErrorResponse's JSON shape. Duplicated here
// (rather than importing the type) because the fields Conduit's error
// contract requires — error, code, request_id — are simple enough that
// re-declaring them avoids a second import of internal/proxy's types for
// what's otherwise already satisfied by proxy.RequestIDFromContext.
type errorResponse struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	RequestID string `json:"request_id"`
}

// NewMiddleware returns the real auth middleware: it validates the
// Authorization header against keyValidator (API keys) or jwtValidator
// (JWT bearer tokens), and on success sets tenant_id + auth_method in
// context via proxy.WithTenantID / proxy.WithAuthMethod. On failure it
// writes a 401 JSON body and does not call next.
//
// Wire this into the proxy with proxy.WithAuthMiddleware(auth.NewMiddleware(...)).
func NewMiddleware(keyValidator *APIKeyValidator, jwtValidator *JWTValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := extractBearer(r)
			if !ok {
				if r.Header.Get("Authorization") == "" {
					writeUnauthorized(w, r, "missing Authorization header")
				} else {
					writeUnauthorized(w, r, "invalid Authorization header format")
				}
				return
			}

			var (
				tenantID   string
				err        error
				authMethod string
			)

			if IsAPIKey(token) {
				authMethod = "api_key"
				tenantID, err = keyValidator.Validate(r.Context(), token)
			} else {
				authMethod = "jwt"
				tenantID, err = jwtValidator.Validate(r.Context(), token)
			}

			if err != nil {
				writeAuthError(w, r, err)
				return
			}

			ctx := proxy.WithTenantID(r.Context(), tenantID)
			ctx = proxy.WithAuthMethod(ctx, authMethod)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractBearer extracts the bearer token from the Authorization header.
// Returns "", false if the header is absent or not in "Bearer <token>" form.
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

// writeAuthError maps a validation error to the specific 401 message
// spec/05-auth.md §4 documents, without ever revealing *why* an API key was
// rejected beyond "expired" (which isn't itself sensitive) — see the
// enumeration-attack note on APIKeyValidator.Validate.
func writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrExpiredAPIKey):
		writeUnauthorized(w, r, "API key has expired")
	case errors.Is(err, ErrExpiredJWT):
		writeUnauthorized(w, r, "JWT has expired")
	case errors.Is(err, ErrInvalidAPIKey):
		writeUnauthorized(w, r, "invalid API key")
	case errors.Is(err, ErrJWTNotSupported):
		writeUnauthorizedCode(w, r, "JWT authentication is not yet configured", "JWT_NOT_SUPPORTED")
	case errors.Is(err, ErrInvalidJWT):
		writeUnauthorized(w, r, "invalid JWT")
	default:
		// An unexpected error (e.g. PostgreSQL unreachable on a cache miss)
		// fails closed: we could not verify the credential, so we don't
		// treat it as valid. 503 signals "try again", not "you're wrong".
		writeServiceUnavailable(w, r, "unable to verify credentials")
	}
}

func writeUnauthorized(w http.ResponseWriter, r *http.Request, message string) {
	writeUnauthorizedCode(w, r, message, "UNAUTHORIZED")
}

func writeUnauthorizedCode(w http.ResponseWriter, r *http.Request, message, code string) {
	writeJSONError(w, r, http.StatusUnauthorized, message, code)
}

func writeServiceUnavailable(w http.ResponseWriter, r *http.Request, message string) {
	writeJSONError(w, r, http.StatusServiceUnavailable, message, "SERVICE_UNAVAILABLE")
}

func writeJSONError(w http.ResponseWriter, r *http.Request, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:     message,
		Code:      code,
		RequestID: proxy.RequestIDFromContext(r.Context()),
	})
}

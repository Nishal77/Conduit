package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/conduit-oss/conduit/internal/proxy"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// tracer returns Conduit's shared tracer. Declared per-package (rather than
// exported from internal/proxy) since importing proxy's tracer() would add
// no value here — otel.Tracer with the same name always returns an
// equivalent Tracer instance, no matter which package asks for it.
func tracer() trace.Tracer { return otel.Tracer("conduit") }

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
			ctx, span := tracer().Start(r.Context(), "conduit.auth")
			defer span.End()

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

			cacheCtx, cacheSpan := tracer().Start(ctx, "conduit.auth.cache_lookup")
			if IsAPIKey(token) {
				authMethod = "api_key"
				tenantID, err = keyValidator.Validate(cacheCtx, token)
			} else {
				authMethod = "jwt"
				tenantID, err = jwtValidator.Validate(cacheCtx, token)
			}
			cacheSpan.End()

			if err != nil {
				proxy.AuthDecisionsTotal.WithLabelValues(authMethod, "deny").Inc()
				writeAuthError(w, r, err)
				return
			}
			proxy.AuthDecisionsTotal.WithLabelValues(authMethod, "allow").Inc()

			reqCtx := proxy.WithTenantID(r.Context(), tenantID)
			reqCtx = proxy.WithAuthMethod(reqCtx, authMethod)
			next.ServeHTTP(w, r.WithContext(reqCtx))
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

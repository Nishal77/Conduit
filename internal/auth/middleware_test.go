package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/conduit-oss/conduit/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestMiddleware(t *testing.T) (http.Handler, *headerCapture) {
	t.Helper()
	redisClient := newTestRedis(t)
	keyValidator := NewAPIKeyValidator(redisClient, nil, time.Minute)
	jwtValidator := NewJWTValidator("test-secret-at-least-32-characters!", "https://conduit")

	capture := &headerCapture{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.tenantID = proxy.TenantIDFromContext(r.Context())
		capture.authMethod = proxy.AuthMethodFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	mw := NewMiddleware(keyValidator, jwtValidator)
	return mw(next), capture
}

// headerCapture records what the wrapped handler saw in context, so tests
// can assert the middleware actually populated tenant_id/auth_method.
type headerCapture struct {
	tenantID   string
	authMethod string
}

func TestMiddleware_MissingAuthorizationHeader(t *testing.T) {
	handler, _ := newTestMiddleware(t)

	req := httptest.NewRequest(http.MethodPost, "/mcp/acme/github", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var body errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "UNAUTHORIZED", body.Code)
	assert.Contains(t, body.Error, "missing")
}

func TestMiddleware_MalformedAuthorizationHeader(t *testing.T) {
	handler, _ := newTestMiddleware(t)

	req := httptest.NewRequest(http.MethodPost, "/mcp/acme/github", nil)
	req.Header.Set("Authorization", "NotBearer sometoken")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var body errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body.Error, "format")
}

func TestMiddleware_JWTNotYetSupported(t *testing.T) {
	handler, _ := newTestMiddleware(t)

	req := httptest.NewRequest(http.MethodPost, "/mcp/acme/github", nil)
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.notarealjwt.signature")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	var body errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "JWT_NOT_SUPPORTED", body.Code)
}

func TestMiddleware_ValidAPIKeySetsTenantContext(t *testing.T) {
	redisClient := newTestRedis(t)
	keyValidator := NewAPIKeyValidator(redisClient, nil, time.Minute)
	jwtValidator := NewJWTValidator("test-secret-at-least-32-characters!", "https://conduit")

	rawKey, hash, _, err := GenerateAPIKey()
	require.NoError(t, err)
	const wantTenantID = "22222222-2222-2222-2222-222222222222"
	require.NoError(t, redisClient.Set(context.Background(), authCacheKeyPrefix+hash, wantTenantID, time.Minute).Err())

	capture := &headerCapture{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.tenantID = proxy.TenantIDFromContext(r.Context())
		capture.authMethod = proxy.AuthMethodFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := NewMiddleware(keyValidator, jwtValidator)(next)

	req := httptest.NewRequest(http.MethodPost, "/mcp/acme/github", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, wantTenantID, capture.tenantID)
	assert.Equal(t, "api_key", capture.authMethod)
}

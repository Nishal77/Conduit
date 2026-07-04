package ratelimit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/conduit-oss/conduit/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiddleware_AllowsAndSetsHeaders(t *testing.T) {
	cfg := defaultTestConfig()
	l := newTestLimiter(t, cfg)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) })
	handler := NewMiddleware(l)(next)

	req := httptest.NewRequest(http.MethodPost, "/mcp/acme/github", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, called, "next handler should run when allowed")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "3", rec.Header().Get("X-RateLimit-Limit"))
	assert.NotEmpty(t, rec.Header().Get("X-RateLimit-Remaining"))
}

func TestMiddleware_DeniesOverLimit(t *testing.T) {
	cfg := defaultTestConfig()
	l := newTestLimiter(t, cfg)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := NewMiddleware(l)(next)

	makeReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/mcp/acme/github", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		ctx := proxy.WithTenantID(req.Context(), "tenant-mw-deny")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}

	for i := 0; i < 3; i++ {
		rec := makeReq()
		require.Equal(t, http.StatusOK, rec.Code, "request %d should be allowed", i+1)
	}

	rec := makeReq()
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))

	var body errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "RATE_LIMITED", body.Code)
	assert.GreaterOrEqual(t, body.RetryAfter, 0)
}

func TestMiddleware_ToolNameExtractedFromBodyWithoutConsumingIt(t *testing.T) {
	cfg := defaultTestConfig()
	l := newTestLimiter(t, cfg)

	var bodyAtHandler []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyAtHandler, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	})
	handler := NewMiddleware(l)(next)

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github/create_issue","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/acme/github", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, reqBody, string(bodyAtHandler), "downstream handler must still see the full original body")
}

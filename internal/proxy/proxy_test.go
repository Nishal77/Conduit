package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/conduit-oss/conduit/internal/audit"
	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/conduit-oss/conduit/internal/tenant"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discardSink is an audit.Sink that throws every batch away — proxy tests
// only care that Write doesn't block, not where events end up.
type discardSink struct{}

func (discardSink) BatchInsert(context.Context, []audit.Event) error { return nil }

func newTestProxy(t *testing.T, upstream string) (*Proxy, *tenant.Router) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Auth.JWTSecret = "01234567890123456789012345678901"
	router := tenant.NewRouter()
	if upstream != "" {
		router.Register(&tenant.Server{TenantSlug: "acme", Name: "github", UpstreamURL: upstream, Enabled: true})
	}
	auditor := audit.New(discardSink{}, &cfg.Audit, zerolog.Nop())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = auditor.Shutdown(ctx)
	})
	return New(cfg, router, plugin.NewRegistry(), auditor, zerolog.Nop()), router
}

func TestHealthz_AlwaysOK(t *testing.T) {
	p, _ := newTestProxy(t, "")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
}

func TestReadyz_NotReadyWithoutRoutes(t *testing.T) {
	p, _ := newTestProxy(t, "")
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "not_ready", body["status"])
}

func TestReadyz_ReadyWithRoutes(t *testing.T) {
	p, _ := newTestProxy(t, "http://example.invalid")
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServeHTTP_UnknownPath404(t *testing.T) {
	p, _ := newTestProxy(t, "")
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	var body ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "NOT_FOUND", body.Code)
}

func TestServeHTTP_UnregisteredServer404(t *testing.T) {
	p, _ := newTestProxy(t, "")
	req := httptest.NewRequest(http.MethodPost, "/mcp/acme/nonexistent", nil)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestServeHTTP_ProxiesJSONResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	}))
	defer upstream.Close()

	p, _ := newTestProxy(t, upstream.URL)

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/acme/github", jsonBody(reqBody))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`, rec.Body.String())
}

func TestServeHTTP_ProxiesSSEResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	p, _ := newTestProxy(t, upstream.URL)

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github/create_issue","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp/acme/github", jsonBody(reqBody))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `"text":"ok"`)
}

func TestMcpPathSegments(t *testing.T) {
	tests := []struct {
		path       string
		wantTenant string
		wantServer string
		wantOK     bool
	}{
		{"/mcp/acme/github", "acme", "github", true},
		{"/mcp/acme/", "", "", false},
		{"/mcp/acme", "", "", false},
		{"/other/acme/github", "", "", false},
		{"/mcp//github", "", "", false},
	}
	for _, tt := range tests {
		tenantSlug, serverName, ok := MCPPathSegments(tt.path)
		assert.Equal(t, tt.wantOK, ok, tt.path)
		if tt.wantOK {
			assert.Equal(t, tt.wantTenant, tenantSlug, tt.path)
			assert.Equal(t, tt.wantServer, serverName, tt.path)
		}
	}
}

func jsonBody(s string) *strings.Reader { return strings.NewReader(s) }

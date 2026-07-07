package plugin_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/stretchr/testify/require"
)

func TestHTTPCallbackPlugin_BeforeAllowsAndModifies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "tools/call", body["method"])

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"action":  "allow",
			"request": map[string]any{"method": "tools/call", "params": map[string]any{"name": "x", "arguments": map[string]any{"redacted": true}}},
		})
	}))
	defer server.Close()

	p := plugin.NewHTTPCallbackPlugin(plugin.HTTPCallbackConfig{Name: "test", Version: "1.0.0", BeforeURL: server.URL})

	req := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: json.RawMessage(`{"name":"x","arguments":{}}`)}
	out, err := p.Before(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "2.0", out.JSONRPC) // envelope preserved
	require.JSONEq(t, `1`, string(out.ID))

	var params mcp.ToolCallParams
	require.NoError(t, json.Unmarshal(out.Params, &params))
	require.JSONEq(t, `{"redacted":true}`, string(params.Arguments))
}

func TestHTTPCallbackPlugin_BeforeBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"action": "block", "message": "PII detected"})
	}))
	defer server.Close()

	p := plugin.NewHTTPCallbackPlugin(plugin.HTTPCallbackConfig{Name: "test", Version: "1.0.0", BeforeURL: server.URL})
	req := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: json.RawMessage(`{}`)}

	_, err := p.Before(context.Background(), req)
	require.True(t, errors.Is(err, plugin.ErrRequestBlocked))
}

func TestHTTPCallbackPlugin_FailsOpenOnTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := plugin.NewHTTPCallbackPlugin(plugin.HTTPCallbackConfig{
		Name: "test", Version: "1.0.0", BeforeURL: server.URL, Timeout: 5 * time.Millisecond,
	})
	req := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: json.RawMessage(`{}`)}

	out, err := p.Before(context.Background(), req)
	require.NoError(t, err)
	require.Same(t, req, out)
}

func TestHTTPCallbackPlugin_FailsOpenOnNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p := plugin.NewHTTPCallbackPlugin(plugin.HTTPCallbackConfig{Name: "test", Version: "1.0.0", BeforeURL: server.URL})
	req := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: json.RawMessage(`{}`)}

	out, err := p.Before(context.Background(), req)
	require.NoError(t, err)
	require.Same(t, req, out)
}

func TestHTTPCallbackPlugin_SignsPayloadWhenSecretConfigured(t *testing.T) {
	const secret = "shh"
	var gotSig string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Conduit-Signature")

		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		require.Equal(t, expected, gotSig)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"action": "allow"})
	}))
	defer server.Close()

	p := plugin.NewHTTPCallbackPlugin(plugin.HTTPCallbackConfig{Name: "test", Version: "1.0.0", BeforeURL: server.URL, Secret: secret})
	req := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: json.RawMessage(`{}`)}

	_, err := p.Before(context.Background(), req)
	require.NoError(t, err)
	require.NotEmpty(t, gotSig)
}

func TestHTTPCallbackPlugin_AfterAppliesModifiedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{"result": map[string]any{"content": []any{}}},
		})
	}))
	defer server.Close()

	p := plugin.NewHTTPCallbackPlugin(plugin.HTTPCallbackConfig{Name: "test", Version: "1.0.0", AfterURL: server.URL})
	req := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call"}
	resp := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: json.RawMessage(`{"content":[{"type":"text","text":"original"}]}`)}

	out, err := p.After(context.Background(), req, resp)
	require.NoError(t, err)
	require.JSONEq(t, `{"content":[]}`, string(out.Result))
}

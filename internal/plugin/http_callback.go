package plugin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/conduit-oss/conduit/internal/mcp"
)

// HTTPCallbackPlugin lets a plugin be written in any language: Conduit
// POSTs the request (or response) payload to a URL and expects a decision
// back over HTTP, rather than requiring a Go binary implementing
// ConduitPlugin directly (spec/14-plugins.md §3).
type HTTPCallbackPlugin struct {
	name       string
	version    string
	beforeURL  string
	afterURL   string
	secret     string
	httpClient *http.Client
}

// HTTPCallbackConfig configures an HTTPCallbackPlugin. BeforeURL and
// AfterURL are each optional — a plugin that only inspects responses can
// leave BeforeURL empty, and vice versa.
type HTTPCallbackConfig struct {
	Name      string
	Version   string
	BeforeURL string
	AfterURL  string
	Secret    string
	Timeout   time.Duration
}

// NewHTTPCallbackPlugin returns an HTTPCallbackPlugin from cfg. A zero
// Timeout defaults to 5s, matching conduit.yaml's plugins.http_timeout
// default.
func NewHTTPCallbackPlugin(cfg HTTPCallbackConfig) *HTTPCallbackPlugin {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &HTTPCallbackPlugin{
		name:       cfg.Name,
		version:    cfg.Version,
		beforeURL:  cfg.BeforeURL,
		afterURL:   cfg.AfterURL,
		secret:     cfg.Secret,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (p *HTTPCallbackPlugin) Name() string    { return p.name }
func (p *HTTPCallbackPlugin) Version() string { return p.version }

// wireContext is the "context" object sent with every callback request —
// everything about the call that isn't part of the MCP message itself.
type wireContext struct {
	TenantID   string `json:"tenant_id,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	LatencyMs  int64  `json:"latency_ms,omitempty"`
}

func contextFrom(ctx context.Context) wireContext {
	rc := RequestContextFromContext(ctx)
	return wireContext{
		TenantID:   rc.TenantID,
		AgentID:    rc.AgentID,
		ServerName: rc.ServerName,
		RequestID:  rc.RequestID,
		LatencyMs:  rc.LatencyMs,
	}
}

type beforeRequestBody struct {
	*mcp.Message
	Context wireContext `json:"context"`
}

type beforeResponseBody struct {
	Action  string       `json:"action"` // "allow" | "block"
	Request *mcp.Message `json:"request,omitempty"`
	Message string       `json:"message,omitempty"`
}

// Before implements ConduitPlugin: it POSTs req to beforeURL and expects an
// allow/block decision back. Per spec/14-plugins.md §3's error handling
// table, any infrastructure failure (timeout, non-200, unreachable,
// malformed JSON) is treated as "allow" — a broken plugin server must never
// become an outage for every tenant routed through it. Only an explicit
// {"action": "block"} response blocks the call.
func (p *HTTPCallbackPlugin) Before(ctx context.Context, req *mcp.Message) (*mcp.Message, error) {
	if p.beforeURL == "" {
		return req, nil
	}

	body, err := json.Marshal(beforeRequestBody{Message: req, Context: contextFrom(ctx)})
	if err != nil {
		return req, nil // can't happen for a well-formed *mcp.Message; fail open regardless
	}

	respBody, err := p.post(ctx, p.beforeURL, "before", body)
	if err != nil {
		return req, nil
	}

	var decision beforeResponseBody
	if err := json.Unmarshal(respBody, &decision); err != nil {
		return req, nil
	}

	if decision.Action == "block" {
		return nil, fmt.Errorf("%w: %s", ErrRequestBlocked, decision.Message)
	}
	if decision.Request != nil {
		// The callback only ever needs to change method/params (e.g.
		// redacting an argument) — jsonrpc/id must survive untouched, or
		// the re-marshaled request becomes a malformed JSON-RPC envelope
		// by the time it reaches the upstream MCP server.
		merged := *req
		merged.Method = decision.Request.Method
		if decision.Request.Params != nil {
			merged.Params = decision.Request.Params
		}
		return &merged, nil
	}
	return req, nil
}

type afterRequestBody struct {
	Request  *mcp.Message `json:"request"`
	Response *mcp.Message `json:"response"`
	Context  wireContext  `json:"context"`
}

type afterResponseBody struct {
	Response *mcp.Message `json:"response"`
}

// After implements ConduitPlugin: it POSTs req/resp to afterURL and applies
// any modified response it returns. Failures are handled the same as
// Before — logged upstream by Registry.RunAfter, never surfaced to the
// agent as an error, since an After hook runs after the real upstream call
// already succeeded.
func (p *HTTPCallbackPlugin) After(ctx context.Context, req, resp *mcp.Message) (*mcp.Message, error) {
	if p.afterURL == "" {
		return resp, nil
	}

	body, err := json.Marshal(afterRequestBody{Request: req, Response: resp, Context: contextFrom(ctx)})
	if err != nil {
		return resp, nil
	}

	respBody, err := p.post(ctx, p.afterURL, "after", body)
	if err != nil {
		return resp, nil
	}

	var decoded afterResponseBody
	if err := json.Unmarshal(respBody, &decoded); err != nil || decoded.Response == nil {
		return resp, nil
	}
	return decoded.Response, nil
}

// Shutdown is a no-op: HTTPCallbackPlugin holds no resources beyond an
// *http.Client, which needs no explicit close.
func (p *HTTPCallbackPlugin) Shutdown(context.Context) error { return nil }

// post sends body to url with the headers spec/14-plugins.md §3 defines,
// including an HMAC-SHA256 signature when a secret is configured, and
// returns the response body (any non-2xx status is an error, matching the
// "treat as allow" contract callers apply on error).
func (p *HTTPCallbackPlugin) post(ctx context.Context, url, hook string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Conduit-Plugin-Version", "1")
	req.Header.Set("X-Conduit-Hook", hook)
	if p.secret != "" {
		req.Header.Set("X-Conduit-Signature", signHMAC(p.secret, body))
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("plugin callback %s returned status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// signHMAC computes the X-Conduit-Signature header value.
func signHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

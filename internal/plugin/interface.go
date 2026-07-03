// Package plugin defines Conduit's extension point: a Before/After hook pair
// that runs around every proxied MCP tool call. See spec/14-plugins.md for
// the full design (HTTP callback plugins, built-ins). This file and
// registry.go are built in Phase 1 because the proxy's middleware chain
// depends on the Registry type even before any real plugin exists — with
// zero registrations, RunBefore/RunAfter are no-ops that pass requests
// through unchanged.
package plugin

import (
	"context"
	"errors"

	"github.com/conduit-oss/conduit/internal/mcp"
)

// ConduitPlugin is the interface all plugins must implement, whether they
// run in-process (Go, implementing this interface directly) or out-of-process
// (any language, via the HTTP callback adapter in http_callback.go).
type ConduitPlugin interface {
	// Name returns the unique plugin identifier (e.g., "pii-redactor").
	Name() string

	// Version returns the plugin's semver version (e.g., "1.0.0").
	Version() string

	// Before is called BEFORE the request is forwarded to the upstream.
	// It may modify the request or return an error to abort the call.
	//
	// To block the request: return (nil, ErrRequestBlocked)
	// To modify the request: return (modifiedReq, nil)
	// To pass through unchanged: return (req, nil)
	Before(ctx context.Context, req *mcp.Message) (*mcp.Message, error)

	// After is called AFTER the upstream response is received.
	// It may modify the response.
	//
	// To modify: return (modifiedResp, nil)
	// To pass through: return (resp, nil)
	// Errors from After are logged but do not affect the response to the agent.
	After(ctx context.Context, req *mcp.Message, resp *mcp.Message) (*mcp.Message, error)

	// Shutdown is called during graceful shutdown. It must complete within
	// the provided context deadline.
	Shutdown(ctx context.Context) error
}

// ErrRequestBlocked is returned by Before to abort a tool call. The proxy
// translates this into a 403 Forbidden response to the agent.
var ErrRequestBlocked = errors.New("request blocked by plugin")

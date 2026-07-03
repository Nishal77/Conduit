# Spec 14 — Plugin System

> Phase: P6 | Files: `internal/plugin/interface.go`, `internal/plugin/registry.go`, `internal/plugin/http_callback.go`, `internal/plugin/builtin/`

---

## 1. Plugin Interface — `internal/plugin/interface.go`

```go
package plugin

import (
    "context"

    "github.com/conduit-oss/conduit/internal/mcp"
)

// ConduitPlugin is the interface all plugins must implement.
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

    // Shutdown is called during graceful shutdown.
    // It must complete within the provided context deadline.
    Shutdown(ctx context.Context) error
}

// ErrRequestBlocked is returned by Before to abort a tool call.
// The proxy returns 403 Forbidden to the agent.
var ErrRequestBlocked = errors.New("request blocked by plugin")
```

---

## 2. Plugin Registry — `internal/plugin/registry.go`

```go
package plugin

import (
    "context"
    "sort"
    "sync"
)

// Registration associates a plugin with its priority for a specific tenant.
type Registration struct {
    Plugin   ConduitPlugin
    Priority int  // lower = runs first (default: 100)
    TenantID string  // empty = applies to all tenants
}

// Registry manages all registered plugins and their execution order.
type Registry struct {
    mu            sync.RWMutex
    registrations []Registration
}

func NewRegistry() *Registry

// Register adds a plugin to the registry.
// Plugins are sorted by priority (ascending) after each registration.
func (r *Registry) Register(reg Registration)

// RunBefore runs all applicable Before hooks in priority order.
// If any plugin returns ErrRequestBlocked, execution stops immediately.
// If any other error occurs, execution continues with the original request (logged).
func (r *Registry) RunBefore(ctx context.Context, tenantID string, req *mcp.Message) (*mcp.Message, error)

// RunAfter runs all applicable After hooks in priority order.
// Errors from After hooks are logged but do not abort the chain.
func (r *Registry) RunAfter(ctx context.Context, tenantID string, req, resp *mcp.Message) *mcp.Message

// ForTenant returns all plugins applicable to a given tenantID.
// Includes plugins with empty TenantID (global) + plugins matching tenantID.
// Sorted by priority ascending.
func (r *Registry) ForTenant(tenantID string) []Registration

// Shutdown calls Shutdown on all registered plugins concurrently.
func (r *Registry) Shutdown(ctx context.Context) error
```

---

## 3. HTTP Callback Plugin — `internal/plugin/http_callback.go`

The HTTP callback plugin enables third-party plugins written in any language. Conduit POSTs the request payload to a URL and expects a modified payload back.

```go
package plugin

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"

    "github.com/conduit-oss/conduit/internal/mcp"
)

// HTTPCallbackPlugin calls an external HTTP endpoint as a plugin.
type HTTPCallbackPlugin struct {
    name        string
    version     string
    beforeURL   string        // POST to this URL for Before hook
    afterURL    string        // POST to this URL for After hook
    secret      string        // HMAC-SHA256 signing secret (optional)
    timeout     time.Duration // default: 5s from config
    httpClient  *http.Client
}

func NewHTTPCallbackPlugin(cfg HTTPCallbackConfig) *HTTPCallbackPlugin

// HTTPCallbackConfig configures an HTTP callback plugin.
type HTTPCallbackConfig struct {
    Name      string
    Version   string
    BeforeURL string
    AfterURL  string
    Secret    string
    Timeout   time.Duration
}
```

### HTTP Callback Protocol

**Before hook request:**

```
POST {before_url}
Content-Type: application/json
X-Conduit-Plugin-Version: 1
X-Conduit-Hook: before
X-Conduit-Tenant-ID: {tenant_id}
X-Conduit-Request-ID: {request_id}
X-Conduit-Signature: sha256={hmac_hex}  (if secret configured)

{
  "method": "tools/call",
  "params": {
    "name": "github/create_issue",
    "arguments": {"title": "bug"}
  },
  "context": {
    "tenant_id": "abc123",
    "agent_id": "my-agent",
    "server_name": "github-mcp"
  }
}
```

**Before hook response (200 OK — allow, possibly modified):**

```json
{
  "action": "allow",
  "request": {
    "method": "tools/call",
    "params": {
      "name": "github/create_issue",
      "arguments": {"title": "[REDACTED]"}
    }
  }
}
```

**Before hook response (200 OK — block):**

```json
{
  "action": "block",
  "message": "PII detected in arguments"
}
```

**After hook request:**

```
POST {after_url}
Content-Type: application/json
X-Conduit-Plugin-Version: 1
X-Conduit-Hook: after

{
  "request": { ... original request ... },
  "response": {
    "result": {
      "content": [{"type": "text", "text": "Issue created"}]
    }
  },
  "context": {
    "tenant_id": "abc123",
    "latency_ms": 42
  }
}
```

**After hook response (200 OK — possibly modified):**

```json
{
  "response": {
    "result": {
      "content": [{"type": "text", "text": "Issue created"}]
    }
  }
}
```

### HMAC Signature Verification

```go
// If secret is configured:
// X-Conduit-Signature: sha256=<hex(HMAC-SHA256(secret, request_body))>
//
// Compute:
mac := hmac.New(sha256.New, []byte(secret))
mac.Write(body)
sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
req.Header.Set("X-Conduit-Signature", sig)
```

### Error Handling for HTTP Callbacks

- HTTP callback timeout → log warning, treat as "allow" (do not block)
- Non-200 response → log warning, treat as "allow"
- Invalid JSON response → log error, treat as "allow"
- Plugin server unreachable → log error, treat as "allow"

Plugins should NEVER block requests due to infrastructure failures.

---

## 4. Built-in Plugins

### `internal/plugin/builtin/pii_redactor.go`

```go
// PII Redactor — detects and redacts PII in request arguments.
//
// Default patterns (compiled at startup):
var piiPatterns = []struct {
    name    string
    pattern *regexp.Regexp
    replace string
}{
    {"email",   regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`), "[EMAIL]"},
    {"phone",   regexp.MustCompile(`\b(?:\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}\b`), "[PHONE]"},
    {"ssn",     regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`), "[SSN]"},
    {"cc",      regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`), "[CARD]"},
    {"apikey",  regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password)\s*[:=]\s*\S+`), "[SECRET]"},
}

// Before: marshal request_args to string, apply patterns, unmarshal back
// Sets audit_event.request_args to redacted version
// Logs count of redactions to metrics
```

### `internal/plugin/builtin/cost_tracker.go`

```go
// Cost Tracker — estimates token cost per tool call.
//
// Cost estimation:
//   1. Count tokens in request_args (rough estimate: len(json)/4)
//   2. Count tokens in response content (same rough estimate)
//   3. Multiply by configured cost per token (per model config)
//
// Default: $0.000002 per token (generic estimate)
// Enterprise: configurable per-model pricing
//
// Sets audit_event.cost_usd
// Updates conduit_cost_usd_total counter metric
```

### `internal/plugin/builtin/circuit_breaker.go`

```go
// Circuit Breaker — prevents cascading failures to upstream servers.
//
// States: closed → open → half-open → closed
// Config (per server, from tenant_plugins.config):
//   failure_threshold: 5       (failures before opening)
//   success_threshold: 2       (successes in half-open before closing)
//   cooldown_sec: 60           (seconds in open state before half-open)
//
// In Before hook:
//   - If circuit is open: return ErrRequestBlocked (returns 503 to agent)
//   - Increment conduit_circuit_breaker_state metric
//
// In After hook:
//   - If response status >= 500: increment failure counter
//   - If response status < 500 and circuit is half-open: increment success counter
```

### `internal/plugin/builtin/logger.go`

```go
// Structured Logger — adds per-plugin structured log fields.
//
// Before: logs "plugin.before" with tool_name, tenant_id
// After: logs "plugin.after" with tool_name, status_code, latency_ms
// Config: log_level (debug|info|warn — default: debug)
```

### `internal/plugin/builtin/transform.go`

```go
// Request/Response Transform — JSONPath-based field manipulation.
//
// Config example:
//   transforms:
//     - hook: before
//       target: "$.params.arguments.env"
//       action: set
//       value: "production"
//     - hook: after
//       target: "$.result.content[0].text"
//       action: prefix
//       value: "[Conduit] "
//
// Supported actions: set, delete, prefix, suffix, replace
```

---

## 5. Plugin Loading

```go
// At startup (cmd/conduit/main.go):
// 1. Register all built-in plugins
// 2. If config.Plugins.Dir != "": scan for .so files (native plugins — Phase 7)
// 3. Load HTTP callback plugin configs from database (tenant_plugins table)
// 4. Build per-tenant plugin lists

// Built-in plugins are always registered but only active
// if enabled = true in tenant_plugins for that tenant.
```

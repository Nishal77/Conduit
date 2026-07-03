# Spec 10 — Management REST API

> Phase: P4 | Files: `internal/api/server.go`, `internal/api/handlers/`

---

## 1. Overview

The management API runs on a **separate port** (`:8081`) from the proxy (`:8080`). This allows network policies to block management access from agent traffic entirely.

- Router: `go-chi/chi/v5`
- Base path: `/api/v1`
- Auth: JWT Bearer (same JWT as proxy) or a dedicated admin API key
- Content-Type: `application/json` on all requests and responses
- All timestamps: ISO 8601 (`2006-01-02T15:04:05Z07:00`)
- All IDs: UUID strings

---

## 2. Server Setup — `internal/api/server.go`

```go
package api

import (
    "net/http"

    "github.com/go-chi/chi/v5"
    "github.com/go-chi/chi/v5/middleware"
    "github.com/conduit-oss/conduit/internal/api/handlers"
    apimiddleware "github.com/conduit-oss/conduit/internal/api/middleware"
    "github.com/conduit-oss/conduit/internal/store"
    "github.com/conduit-oss/conduit/internal/audit"
    "github.com/conduit-oss/conduit/internal/config"
    "github.com/rs/zerolog"
)

// Server is the management API HTTP server.
type Server struct {
    router http.Handler
    cfg    *config.Config
}

// New builds the chi router with all handlers and middleware.
func New(
    cfg *config.Config,
    stores *store.Stores,  // all stores in one struct
    auditor *audit.Writer,
    log zerolog.Logger,
) *Server

// buildRouter returns the chi router.
func buildRouter(cfg *config.Config, h *handlers.Handlers, log zerolog.Logger) http.Handler {
    r := chi.NewRouter()

    // Global middleware (runs for every request)
    r.Use(middleware.RequestID)
    r.Use(middleware.RealIP)
    r.Use(middleware.Logger)          // chi's built-in access log
    r.Use(middleware.Recoverer)
    r.Use(apimiddleware.CORS(cfg))    // configurable allowed origins
    r.Use(apimiddleware.JSONContentType)

    // Routes
    r.Route("/api/v1", func(r chi.Router) {
        // Health — no auth required
        r.Get("/health", h.Health.Get)

        // Everything else requires auth
        r.Group(func(r chi.Router) {
            r.Use(apimiddleware.Auth(cfg))

            r.Route("/tenants", tenantRoutes(h))
            r.Route("/api-keys", apiKeyRoutes(h))
            r.Route("/servers", serverRoutes(h))
            r.Route("/rate-limits", rateLimitRoutes(h))
            r.Route("/audit", auditRoutes(h))
            r.Route("/webhooks", webhookRoutes(h))
            r.Route("/plugins", pluginRoutes(h))
            r.Route("/oauth/applications", oauthAppRoutes(h))  // Phase 5
        })
    })

    // OpenAPI spec
    r.Get("/api/openapi.json", h.OpenAPI.Serve)

    return r
}
```

---

## 3. Endpoint Reference

### Tenants — `/api/v1/tenants`

| Method | Path | Handler | Description |
|---|---|---|---|
| `GET` | `/api/v1/tenants` | `ListTenants` | List all active tenants |
| `POST` | `/api/v1/tenants` | `CreateTenant` | Create a new tenant |
| `GET` | `/api/v1/tenants/{tenantID}` | `GetTenant` | Get tenant by ID |
| `PATCH` | `/api/v1/tenants/{tenantID}` | `UpdateTenant` | Update tenant name/plan/settings |
| `DELETE` | `/api/v1/tenants/{tenantID}` | `DeleteTenant` | Soft-delete tenant |

**POST /api/v1/tenants**
```json
// Request
{"slug": "acme-corp", "name": "Acme Corporation", "plan": "pro"}

// Response 201
{
  "id": "a1b2c3d4-...",
  "slug": "acme-corp",
  "name": "Acme Corporation",
  "plan": "pro",
  "settings": {},
  "created_at": "2026-07-01T00:00:00Z"
}

// Error 409 (slug taken)
{"error": "tenant with slug 'acme-corp' already exists", "code": "CONFLICT"}
```

---

### API Keys — `/api/v1/api-keys`

| Method | Path | Handler | Description |
|---|---|---|---|
| `GET` | `/api/v1/api-keys?tenant_id={id}` | `ListAPIKeys` | List keys for a tenant |
| `POST` | `/api/v1/api-keys` | `CreateAPIKey` | Create a new key (returns raw key ONCE) |
| `DELETE` | `/api/v1/api-keys/{keyID}` | `RevokeAPIKey` | Revoke a key |

**POST /api/v1/api-keys**
```json
// Request
{
  "tenant_id": "a1b2c3d4-...",
  "name": "my-agent-key",
  "scopes": ["mcp:call"],
  "expires_in": "30d"  // optional; "7d", "30d", "90d", "1y", or null
}

// Response 201 — raw key is ONLY returned here
{
  "id": "key-uuid-...",
  "name": "my-agent-key",
  "key": "cnd_4MqyH3xK9vRwP2nL8tFjD6eAuCbGmZsN1oYiXWE",  // ONLY time this appears
  "key_prefix": "cnd_4MqyH3xK",
  "scopes": ["mcp:call"],
  "expires_at": null,
  "created_at": "2026-07-01T00:00:00Z"
}
```

---

### MCP Servers — `/api/v1/servers`

| Method | Path | Handler | Description |
|---|---|---|---|
| `GET` | `/api/v1/servers?tenant_id={id}` | `ListServers` | List servers for a tenant |
| `POST` | `/api/v1/servers` | `RegisterServer` | Register an upstream MCP server |
| `GET` | `/api/v1/servers/{serverID}` | `GetServer` | Get server details |
| `PATCH` | `/api/v1/servers/{serverID}` | `UpdateServer` | Update server config |
| `DELETE` | `/api/v1/servers/{serverID}` | `DeleteServer` | Remove a server |
| `GET` | `/api/v1/servers/{serverID}/health` | `CheckServerHealth` | Ping upstream health |

**POST /api/v1/servers**
```json
// Request
{
  "tenant_id": "a1b2c3d4-...",
  "name": "github-mcp",
  "upstream_url": "http://github-mcp-server:3001",
  "auth_type": "bearer",
  "auth_config": {"token": "ghp_xxx"},
  "health_check_url": "http://github-mcp-server:3001/health",
  "weight": 100
}

// Response 201
{
  "id": "srv-uuid-...",
  "tenant_id": "a1b2c3d4-...",
  "name": "github-mcp",
  "upstream_url": "http://github-mcp-server:3001",
  "auth_type": "bearer",
  "health_check_url": "http://github-mcp-server:3001/health",
  "weight": 100,
  "enabled": true,
  "created_at": "2026-07-01T00:00:00Z"
}
// NOTE: auth_config is NEVER returned in responses (contains secrets)
```

---

### Rate Limits — `/api/v1/rate-limits`

| Method | Path | Handler | Description |
|---|---|---|---|
| `GET` | `/api/v1/rate-limits?tenant_id={id}` | `ListRateLimits` | List configs for a tenant |
| `PUT` | `/api/v1/rate-limits` | `UpsertRateLimit` | Create or update a config |
| `DELETE` | `/api/v1/rate-limits/{id}` | `DeleteRateLimit` | Remove a config |

**PUT /api/v1/rate-limits**
```json
// Request
{
  "tenant_id": "a1b2c3d4-...",
  "scope": "tool",
  "target": "github/delete_repo",
  "requests": 5,
  "window_sec": 3600
}

// Response 200
{
  "id": "rl-uuid-...",
  "tenant_id": "a1b2c3d4-...",
  "scope": "tool",
  "target": "github/delete_repo",
  "requests": 5,
  "window_sec": 3600,
  "burst": null,
  "created_at": "2026-07-01T00:00:00Z",
  "updated_at": "2026-07-01T00:00:00Z"
}
```

---

### Audit — `/api/v1/audit`

| Method | Path | Handler | Description |
|---|---|---|---|
| `GET` | `/api/v1/audit/events?tenant_id={id}&...` | `QueryAuditEvents` | Paginated query |
| `GET` | `/api/v1/audit/export?tenant_id={id}&...` | `ExportAuditEvents` | Streaming CSV/JSON export |
| `GET` | `/api/v1/audit/stream?tenant_id={id}` | `StreamAuditEvents` | SSE live stream |

**GET /api/v1/audit/events**

Query parameters:
```
tenant_id    string    required
from         string    optional, ISO 8601 or relative ("24h", "7d")
to           string    optional (default: now)
tool_name    string    optional, supports "*" suffix glob
server_name  string    optional
policy_action string   optional ("allow" | "deny" | "rate_limited")
limit        int       optional (default: 50, max: 500)
offset       int       optional (default: 0)
```

Response:
```json
{
  "events": [
    {
      "id": "evt-uuid-...",
      "tenant_id": "a1b2c3d4-...",
      "server_name": "github-mcp",
      "tool_name": "github/create_issue",
      "status_code": 200,
      "latency_ms": 1,
      "auth_method": "api_key",
      "policy_action": "allow",
      "cost_usd": "0.00010000",
      "trace_id": "4bf92f3577b34da6...",
      "created_at": "2026-07-01T12:00:00Z"
    }
  ],
  "total": 1234,
  "limit": 50,
  "offset": 0
}
```

---

### Webhooks — `/api/v1/webhooks`

| Method | Path | Handler | Description |
|---|---|---|---|
| `GET` | `/api/v1/webhooks?tenant_id={id}` | `ListWebhooks` | List webhooks |
| `POST` | `/api/v1/webhooks` | `CreateWebhook` | Register a webhook |
| `PATCH` | `/api/v1/webhooks/{id}` | `UpdateWebhook` | Update webhook |
| `DELETE` | `/api/v1/webhooks/{id}` | `DeleteWebhook` | Remove webhook |
| `POST` | `/api/v1/webhooks/{id}/test` | `TestWebhook` | Send a test event |

---

### Plugins — `/api/v1/plugins`

| Method | Path | Handler | Description |
|---|---|---|---|
| `GET` | `/api/v1/plugins` | `ListPlugins` | List all available plugins |
| `GET` | `/api/v1/plugins/tenant?tenant_id={id}` | `ListTenantPlugins` | List enabled plugins for tenant |
| `PUT` | `/api/v1/plugins/tenant` | `EnablePlugin` | Enable/configure a plugin for a tenant |
| `DELETE` | `/api/v1/plugins/tenant/{id}` | `DisablePlugin` | Disable a plugin for a tenant |

---

## 4. Standard Error Response

All errors MUST use this shape:

```json
{
  "error": "human-readable message",
  "code": "MACHINE_READABLE_CODE",
  "request_id": "uuid"
}

// Validation errors add "details":
{
  "error": "validation failed",
  "code": "VALIDATION_ERROR",
  "request_id": "uuid",
  "details": {
    "slug": "must match pattern ^[a-z0-9-]{3,64}$",
    "plan": "must be one of: free, pro, enterprise"
  }
}
```

HTTP status → code mapping is identical to the proxy error mapping.

---

## 5. Pagination Pattern

All list endpoints support `limit` and `offset` pagination:

```json
{
  "items": [...],
  "total": 1234,
  "limit": 50,
  "offset": 0,
  "has_more": true
}
```

- Default limit: 50
- Maximum limit: 500 (return 400 if exceeded)
- Order: always newest first unless otherwise specified

---

## 6. CORS Middleware

```go
// internal/api/middleware/cors.go
// Allowed origins: configurable via config or "*" in development
// Allowed methods: GET, POST, PATCH, PUT, DELETE, OPTIONS
// Allowed headers: Authorization, Content-Type, X-Request-ID
// Exposed headers: X-Request-ID, X-RateLimit-*
// Max age: 3600
```

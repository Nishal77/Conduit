# Spec 13 — Multi-Tenant Routing

> Phase: P5 | Files: `internal/tenant/store.go`, `internal/tenant/router.go`

---

## 1. Routing Model

Each request is routed as follows:

```
Agent request: POST /mcp/{tenant_slug}/{server_name}
                         │                  │
                         ▼                  ▼
               tenant lookup          server lookup
               (by slug → ID)     (by tenant_id + name)
                         │                  │
                         └──────────────────┘
                                  │
                                  ▼
                        upstream_url (for this request)
```

`tenant_id` is ALWAYS extracted from the **validated auth token**, not the URL. The URL slug is used to look up the tenant record, then verified against the token's `tenant_id`. If they differ → 403.

---

## 2. Routing Table — `internal/tenant/store.go`

```go
package tenant

import (
    "context"
    "sync"
    "time"

    "github.com/google/uuid"
    "github.com/conduit-oss/conduit/internal/store"
    "github.com/rs/zerolog"
)

// RoutingEntry caches a single tenant's servers in memory.
type RoutingEntry struct {
    TenantID  uuid.UUID
    TenantSlug string
    Servers   []*store.MCPServer
    LoadedAt  time.Time
}

// RoutingTable caches tenant→server routing in-process.
// Refreshes automatically every 5 seconds.
type RoutingTable struct {
    mu       sync.RWMutex
    entries  map[string]*RoutingEntry  // key: tenant_slug
    byID     map[uuid.UUID]*RoutingEntry
    ttl      time.Duration             // default: 5s
    store    *store.TenantStore
    srvStore *store.MCPServerStore
    log      zerolog.Logger
}

func NewRoutingTable(
    ttl time.Duration,
    store *store.TenantStore,
    srvStore *store.MCPServerStore,
    log zerolog.Logger,
) *RoutingTable

// Start begins the background refresh goroutine.
// Stops when ctx is cancelled.
func (t *RoutingTable) Start(ctx context.Context)

// GetEntry returns the routing entry for a tenant slug.
// Returns ErrTenantNotFound if the tenant does not exist.
// Uses in-process cache with TTL; refreshes from DB on miss or expiry.
func (t *RoutingTable) GetEntry(ctx context.Context, slug string) (*RoutingEntry, error)

// GetServer returns the upstream URL for a specific server within a tenant.
// Uses weighted selection if multiple servers with the same name exist (Phase 5+).
func (t *RoutingTable) GetServer(ctx context.Context, tenantID uuid.UUID, serverName string) (*store.MCPServer, error)

// Invalidate removes a tenant's entry from the cache (called after tenant/server updates).
func (t *RoutingTable) Invalidate(tenantSlug string)

// Refresh forces a reload of all entries from the database.
// Called at startup and every TTL seconds in the background.
func (t *RoutingTable) Refresh(ctx context.Context) error {
    // 1. Load all active tenants: store.List(ctx)
    // 2. For each tenant: load all enabled servers: srvStore.ListByTenant(ctx, tenantID)
    // 3. Update t.entries and t.byID atomically under write lock
    // 4. Update conduit_tenants_active metric
}

var ErrTenantNotFound = errors.New("tenant not found")
var ErrServerNotFound = errors.New("MCP server not found")
```

---

## 3. Router — `internal/tenant/router.go`

```go
package tenant

import (
    "context"
    "net/http"
    "net/url"
    "strings"
)

// Router resolves a proxy request to an upstream URL.
type Router struct {
    table *RoutingTable
}

func NewRouter(table *RoutingTable) *Router

// Resolve returns the upstream URL for a given tenant + server.
//
// Steps:
//   1. Extract tenant_slug and server_name from URL path
//      Path: /mcp/{tenant_slug}/{server_name}[/additional/path...]
//   2. Look up routing entry by slug
//   3. Verify entry.TenantID == tenantIDFromContext(ctx) — prevent cross-tenant access
//   4. Look up server by (tenantID, serverName)
//   5. Construct upstream URL:
//      upstreamURL + path[after /mcp/slug/server_name] + ?querystring
//   6. Return upstream URL and MCPServer
func (r *Router) Resolve(ctx context.Context, req *http.Request) (upstreamURL *url.URL, server *store.MCPServer, err error)

// constructUpstreamURL builds the full upstream URL from the server's base URL and
// the remaining path after the server_name segment.
//
// Example:
//   Request path:    /mcp/acme-corp/github-mcp/events
//   Server base URL: http://github-mcp:3001
//   Upstream URL:    http://github-mcp:3001/events
//
// If remaining path is empty, upstream URL is just the base URL.
// Preserve query string from original request.
func constructUpstreamURL(baseURL, remainingPath, rawQuery string) (*url.URL, error)
```

---

## 4. Weighted Server Selection

When multiple servers share the same name (different weights), use weighted random selection:

```go
// WeightedSelect picks a server from a slice using their Weight fields.
// Higher weight = more likely to be chosen.
//
// Algorithm: generate random int in [0, totalWeight), walk until cumulative weight >= rand
func WeightedSelect(servers []*store.MCPServer) *store.MCPServer {
    totalWeight := 0
    for _, s := range servers {
        totalWeight += s.Weight
    }
    r := rand.Intn(totalWeight)
    cumulative := 0
    for _, s := range servers {
        cumulative += s.Weight
        if r < cumulative {
            return s
        }
    }
    return servers[len(servers)-1]
}
```

Note: In Phase 2, only one server per (tenant, name) is supported. Weighted selection becomes active in Phase 5.

---

## 5. Tenant Isolation Rules (Non-Negotiable)

1. **Never route based on tenant_slug alone** — always verify against the JWT/API key's tenant_id
2. **Server lookup MUST be scoped to tenant_id** — prevent cross-tenant server name collision
3. **Routing table entries are per-tenant** — no global server registry accessible across tenants
4. **auth_config on upstream servers is encrypted at rest** — decrypt at proxy time, never log
5. **Routing table cache TTL = 5 seconds** — stale entries cannot persist longer than this

---

## 6. Upstream Authentication

When forwarding to an upstream MCP server with `auth_type != "none"`, inject credentials:

```go
// auth_type = "bearer"
req.Header.Set("Authorization", "Bearer " + server.AuthConfig["token"].(string))

// auth_type = "basic"
req.SetBasicAuth(server.AuthConfig["username"].(string), server.AuthConfig["password"].(string))

// auth_type = "api_key"
// Look at key_header and key_value fields in auth_config
req.Header.Set(server.AuthConfig["key_header"].(string), server.AuthConfig["key_value"].(string))

// auth_type = "none"
// Do nothing — pass through without modification
```

**CRITICAL**: Never log `auth_config` or any derived credentials. Log only `auth_type`.

---

## 7. Cache Invalidation Hooks

The management API MUST call `RoutingTable.Invalidate(slug)` after:
- Creating a new tenant
- Updating a tenant (name, settings)
- Deleting a tenant
- Registering a new server
- Updating a server
- Deleting a server
- Enabling/disabling a server

This ensures the cache reflects changes within the current TTL period, but explicitly invalidates the specific tenant's entry immediately.

# Spec 06 — Rate Limiting

> Phase: P2 | Files: `internal/ratelimit/limiter.go`, `internal/ratelimit/middleware.go`, `internal/ratelimit/lua/token_bucket.lua`

---

## 1. Overview

Rate limiting uses a **token bucket** algorithm implemented as an atomic Redis Lua script. The bucket is refilled on every check — not on a fixed schedule — making it a "leaky bucket with burst" design.

Fail-open: if Redis is unavailable, requests are allowed through (with a warning log).

Target overhead: **<0.2ms** for a cache-hit rate limit check.

---

## 2. Redis Key Patterns

```
rl:{tenant_id}:{scope}:{target}

Examples:
  rl:abc123:tenant:*              — tenant-wide limit
  rl:abc123:server:github-mcp     — per-server limit
  rl:abc123:tool:github/create_issue  — per-tool limit
  rl:abc123:agent:agent-xyz       — per-agent limit
```

Each key stores a Redis Hash with fields:
- `tokens` — current token count (float, stored as string)
- `last_refill` — unix timestamp (microseconds) of last check

---

## 3. Token Bucket Lua Script — `internal/ratelimit/lua/token_bucket.lua`

```lua
-- Token bucket rate limiter
-- Keys: KEYS[1] = "rl:{tenant_id}:{scope}:{target}"
-- Args: ARGV[1] = capacity (max tokens)
--       ARGV[2] = refill_rate (tokens per second, float)
--       ARGV[3] = requested (tokens to consume, almost always 1)
--       ARGV[4] = now_us (current unix time in microseconds)
--       ARGV[5] = ttl_sec (key TTL in seconds, set to window*2)
--
-- Returns: {allowed, remaining}
--   allowed: 1 if the request is allowed, 0 if rate limited
--   remaining: integer tokens remaining after this request

local key = KEYS[1]
local capacity    = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])  -- tokens per second
local requested   = tonumber(ARGV[3])
local now_us      = tonumber(ARGV[4])
local ttl_sec     = tonumber(ARGV[5])

-- Read current state
local data = redis.call('HMGET', key, 'tokens', 'last_refill')
local tokens     = tonumber(data[1]) or capacity
local last_refill = tonumber(data[2]) or now_us

-- Refill tokens based on elapsed time
local elapsed_sec = math.max(0, (now_us - last_refill) / 1e6)
tokens = math.min(capacity, tokens + elapsed_sec * refill_rate)

-- Attempt to consume
local allowed = 0
if tokens >= requested then
    tokens = tokens - requested
    allowed = 1
end

-- Persist new state
redis.call('HMSET', key, 'tokens', tostring(tokens), 'last_refill', tostring(now_us))
redis.call('EXPIRE', key, ttl_sec)

return {allowed, math.floor(tokens)}
```

---

## 4. Limiter — `internal/ratelimit/limiter.go`

```go
package ratelimit

import (
    "context"
    "errors"
    "fmt"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/rs/zerolog"
    "github.com/conduit-oss/conduit/internal/config"
    "github.com/conduit-oss/conduit/internal/store"
)

// Result is returned by Check.
type Result struct {
    Allowed   bool
    Remaining int
    ResetAt   time.Time // approximate time when bucket refills to capacity
    Limit     int       // configured requests per window
    Window    int       // configured window in seconds
}

// Limiter checks rate limits using Redis token bucket.
type Limiter struct {
    redis     *redis.Client
    rlStore   *store.RateLimitStore
    cfg       *config.RateLimitConfig
    script    *redis.Script  // loaded Lua script
    log       zerolog.Logger
}

func New(
    redis *redis.Client,
    rlStore *store.RateLimitStore,
    cfg *config.RateLimitConfig,
    log zerolog.Logger,
) *Limiter

// Check evaluates rate limits for a tool call.
//
// Scope priority order (most specific wins):
//   1. tool scope:   rl:{tenant_id}:tool:{tool_name}
//   2. server scope: rl:{tenant_id}:server:{server_name}
//   3. agent scope:  rl:{tenant_id}:agent:{agent_id}  (if agent_id in context)
//   4. tenant scope: rl:{tenant_id}:tenant:*
//
// For each scope:
//   1. Load config from DB (or use defaults from cfg)
//   2. Execute Lua script with capacity + refill_rate computed from config
//   3. If any scope returns allowed=0 → return Result{Allowed: false}
//   4. If all scopes allow → return Result{Allowed: true}
//
// On Redis error:
//   if cfg.FailOpen: log warning, return Result{Allowed: true}
//   else: return error (proxy returns 503)
//
// capacity = requests (the limit)
// refill_rate = requests / window_sec
// burst capacity = requests * cfg.BurstMultiplier (if burst is nil in config)
func (l *Limiter) Check(ctx context.Context, tenantID, serverName, toolName, agentID string) (*Result, error)

// configForScope loads the rate limit config for a given scope+target.
// Falls back to default config if no record exists in the database.
func (l *Limiter) configForScope(ctx context.Context, tenantID, scope, target string) (requests, windowSec int, err error) {
    // Query rate_limit_configs WHERE tenant_id=$1 AND scope=$2 AND (target=$3 OR target IS NULL)
    // ORDER BY target IS NULL ASC  (specific target wins over NULL)
    // LIMIT 1
    //
    // If no row: use defaults from cfg
    //   requests = cfg.DefaultPerTenant (1000)
    //   windowSec = 60
}
```

---

## 5. Rate Limit Middleware — `internal/ratelimit/middleware.go`

```go
package ratelimit

import (
    "net/http"
    "strconv"
    "time"
)

// NewMiddleware returns an HTTP middleware that enforces rate limits.
//
// It reads from context (set by auth middleware):
//   - tenant_id (required)
//   - server_name (from URL path)
//   - tool_name (from parsed MCP message body)
//   - agent_id (optional, from initialize params)
//
// On rate limit exceeded:
//   - Returns 429 Too Many Requests
//   - Sets Retry-After header (seconds until reset)
//   - Sets X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset headers
//   - Returns JSON error body
//
// On Redis unavailable (fail-open):
//   - Logs warning with zerolog
//   - Calls next handler (allows the request)
func NewMiddleware(limiter *Limiter) func(http.Handler) http.Handler
```

### Rate limit response headers

```
HTTP/1.1 429 Too Many Requests
Content-Type: application/json
Retry-After: 42
X-RateLimit-Limit: 1000
X-RateLimit-Remaining: 0
X-RateLimit-Reset: 1704067200

{"error": "rate limit exceeded", "code": "RATE_LIMITED", "request_id": "...", "retry_after": 42}
```

On every **allowed** request, also set informational headers:
```
X-RateLimit-Limit: 1000
X-RateLimit-Remaining: 847
X-RateLimit-Reset: 1704067200
```

---

## 6. Store Layer — `internal/store/ratelimits.go`

```go
package store

import (
    "context"
    "time"

    "github.com/google/uuid"
)

// RateLimitConfig matches the rate_limit_configs table row.
type RateLimitConfig struct {
    ID         uuid.UUID
    TenantID   uuid.UUID
    Scope      string   // 'tenant' | 'server' | 'tool' | 'agent'
    Target     *string  // nil = applies to all in scope
    Requests   int
    WindowSec  int
    Burst      *int     // nil = use BurstMultiplier from global config
    CreatedAt  time.Time
    UpdatedAt  time.Time
}

// RateLimitStore provides rate limit config data access.
type RateLimitStore struct {
    db *DB
}

func NewRateLimitStore(db *DB) *RateLimitStore { return &RateLimitStore{db: db} }

// GetForScope loads the most specific rate limit config for a scope+target.
// Returns ErrNotFound if no config exists (caller uses defaults).
func (s *RateLimitStore) GetForScope(ctx context.Context, tenantID uuid.UUID, scope, target string) (*RateLimitConfig, error)

// List returns all rate limit configs for a tenant.
func (s *RateLimitStore) List(ctx context.Context, tenantID uuid.UUID) ([]*RateLimitConfig, error)

// Upsert creates or updates a rate limit config.
func (s *RateLimitStore) Upsert(ctx context.Context, input UpsertRateLimitInput) (*RateLimitConfig, error)

// Delete removes a rate limit config.
func (s *RateLimitStore) Delete(ctx context.Context, id uuid.UUID) error

type UpsertRateLimitInput struct {
    TenantID  uuid.UUID
    Scope     string
    Target    *string
    Requests  int
    WindowSec int
    Burst     *int
}
```

---

## 7. Config Caching

Rate limit configs are read from PostgreSQL on the first request for each scope, then cached in-process for 30 seconds (not Redis). This reduces DB load on the hot path.

```go
// In-process cache (sync.Map or simple map with RWMutex):
// Key: "{tenant_id}:{scope}:{target}"
// Value: *RateLimitConfig + loadedAt time.Time
// TTL: 30s (hard-coded; not configurable in Phase 2)
// Invalidation: on config update via management API, purge the relevant key
```

---

## 8. Lua Script Loading

The Lua script MUST be loaded once at startup using `redis.NewScript()`:

```go
//go:embed lua/token_bucket.lua
var tokenBucketLua string

// In Limiter.New():
l.script = redis.NewScript(tokenBucketLua)

// Execution:
vals, err := l.script.Run(ctx, l.redis,
    []string{key},               // KEYS
    capacity,                    // ARGV[1]
    refillRate,                  // ARGV[2]
    1,                           // ARGV[3] = tokens consumed
    time.Now().UnixMicro(),      // ARGV[4]
    windowSec*2,                 // ARGV[5] = TTL
).Slice()
```

`redis.NewScript()` uses EVALSHA after the first run and falls back to EVAL on cache miss — this is handled transparently by go-redis.

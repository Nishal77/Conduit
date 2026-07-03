# Spec 04 — Database

> Phase: P2 | Files: `migrations/`, `internal/store/`

---

## 1. Migration Conventions

- Files: `migrations/000NNN_<name>.up.sql` and `000NNN_<name>.down.sql`
- Runner: `golang-migrate/migrate/v4` with pgx/v5 driver
- Numbering: zero-padded to 6 digits (`000001`, `000002`, ...)
- Every `.up.sql` MUST have a matching `.down.sql` that undoes it exactly
- Migrations run at startup when `conduit migrate --db-url` is called, NOT automatically on proxy start
- All SQL identifiers in `snake_case`
- All primary keys: `UUID` with `DEFAULT gen_random_uuid()`
- All timestamps: `TIMESTAMPTZ NOT NULL DEFAULT NOW()`

---

## 2. Migration 000001 — Initial Schema

File: `migrations/000001_initial_schema.up.sql`

```sql
-- Enable UUID generation
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ─────────────────────────────────────────
-- TENANTS
-- ─────────────────────────────────────────
CREATE TABLE tenants (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    plan        TEXT        NOT NULL DEFAULT 'free',
    settings    JSONB       NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ,         -- soft-delete; NULL = active

    CONSTRAINT tenants_slug_unique UNIQUE (slug),
    CONSTRAINT tenants_slug_format CHECK (slug ~ '^[a-z0-9-]{3,64}$'),
    CONSTRAINT tenants_plan_values CHECK (plan IN ('free', 'pro', 'enterprise'))
);

CREATE INDEX idx_tenants_slug ON tenants (slug) WHERE deleted_at IS NULL;

-- ─────────────────────────────────────────
-- API KEYS
-- ─────────────────────────────────────────
CREATE TABLE api_keys (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    key_hash     TEXT        NOT NULL,  -- SHA-256(raw_key), hex-encoded
    key_prefix   TEXT        NOT NULL,  -- first 12 chars of raw key for display
    scopes       TEXT[]      NOT NULL DEFAULT '{"mcp:call"}',
    expires_at   TIMESTAMPTZ,           -- NULL = never expires
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at   TIMESTAMPTZ,           -- NULL = active

    CONSTRAINT api_keys_key_hash_unique UNIQUE (key_hash)
);

CREATE INDEX idx_api_keys_key_hash   ON api_keys (key_hash)   WHERE revoked_at IS NULL;
CREATE INDEX idx_api_keys_tenant_id  ON api_keys (tenant_id)  WHERE revoked_at IS NULL;

-- ─────────────────────────────────────────
-- MCP SERVERS
-- ─────────────────────────────────────────
CREATE TABLE mcp_servers (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name             TEXT        NOT NULL,
    upstream_url     TEXT        NOT NULL,
    auth_type        TEXT        NOT NULL DEFAULT 'none',
    auth_config      JSONB       NOT NULL DEFAULT '{}',  -- encrypted at app layer
    health_check_url TEXT,
    weight           INT         NOT NULL DEFAULT 100,
    enabled          BOOLEAN     NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT mcp_servers_tenant_name_unique UNIQUE (tenant_id, name),
    CONSTRAINT mcp_servers_auth_type CHECK (auth_type IN ('none', 'bearer', 'basic', 'api_key')),
    CONSTRAINT mcp_servers_weight_positive CHECK (weight > 0)
);

CREATE INDEX idx_mcp_servers_tenant_id ON mcp_servers (tenant_id) WHERE enabled = true;

-- ─────────────────────────────────────────
-- RATE LIMIT CONFIGURATIONS
-- ─────────────────────────────────────────
CREATE TABLE rate_limit_configs (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    scope       TEXT        NOT NULL,  -- 'tenant' | 'server' | 'tool' | 'agent'
    target      TEXT,                  -- NULL = applies to all in scope
    requests    INT         NOT NULL,
    window_sec  INT         NOT NULL,
    burst       INT,                   -- NULL = no burst (strict)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT rate_limit_configs_scope_values CHECK (scope IN ('tenant', 'server', 'tool', 'agent')),
    CONSTRAINT rate_limit_configs_requests_positive CHECK (requests > 0),
    CONSTRAINT rate_limit_configs_window_positive CHECK (window_sec > 0),
    CONSTRAINT rate_limit_configs_unique UNIQUE (tenant_id, scope, target)
);

CREATE INDEX idx_rate_limit_configs_tenant_id ON rate_limit_configs (tenant_id);
```

File: `migrations/000001_initial_schema.down.sql`

```sql
DROP TABLE IF EXISTS rate_limit_configs;
DROP TABLE IF EXISTS mcp_servers;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS tenants;
```

---

## 3. Migration 000002 — Audit Table

File: `migrations/000002_audit_table.up.sql`

```sql
-- ─────────────────────────────────────────
-- AUDIT EVENTS (append-only, partitioned)
-- ─────────────────────────────────────────
CREATE TABLE audit_events (
    id            UUID        NOT NULL DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL,
    agent_id      TEXT,
    session_id    TEXT,
    server_name   TEXT        NOT NULL,
    tool_name     TEXT        NOT NULL,
    request_args  JSONB,                  -- may be NULL if redacted
    response_meta JSONB,
    status_code   INT         NOT NULL,
    latency_ms    INT         NOT NULL,
    auth_method   TEXT        NOT NULL,   -- 'api_key' | 'jwt'
    policy_action TEXT        NOT NULL,   -- 'allow' | 'deny' | 'rate_limited'
    cost_usd      NUMERIC(12, 8),
    trace_id      TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- No primary key on partitioned table parent; each partition gets one
    CONSTRAINT audit_events_policy_action CHECK (policy_action IN ('allow', 'deny', 'rate_limited')),
    CONSTRAINT audit_events_auth_method CHECK (auth_method IN ('api_key', 'jwt'))
) PARTITION BY RANGE (created_at);

-- Create partitions for 12 months ahead (automate with pg_partman in production)
-- Conduit's startup code creates missing partitions for next 3 months automatically.
CREATE TABLE audit_events_2026_01 PARTITION OF audit_events
    FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');

CREATE TABLE audit_events_2026_02 PARTITION OF audit_events
    FOR VALUES FROM ('2026-02-01') TO ('2026-03-01');

CREATE TABLE audit_events_2026_03 PARTITION OF audit_events
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');

CREATE TABLE audit_events_2026_04 PARTITION OF audit_events
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');

CREATE TABLE audit_events_2026_05 PARTITION OF audit_events
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');

CREATE TABLE audit_events_2026_06 PARTITION OF audit_events
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');

CREATE TABLE audit_events_2026_07 PARTITION OF audit_events
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');

CREATE TABLE audit_events_2026_08 PARTITION OF audit_events
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');

CREATE TABLE audit_events_2026_09 PARTITION OF audit_events
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');

CREATE TABLE audit_events_2026_10 PARTITION OF audit_events
    FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');

CREATE TABLE audit_events_2026_11 PARTITION OF audit_events
    FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');

CREATE TABLE audit_events_2026_12 PARTITION OF audit_events
    FOR VALUES FROM ('2026-12-01') TO ('2027-01-01');

-- Indexes on partition parent propagate to all partitions
CREATE INDEX idx_audit_events_tenant_created ON audit_events (tenant_id, created_at DESC);
CREATE INDEX idx_audit_events_tenant_tool    ON audit_events (tenant_id, tool_name);
CREATE INDEX idx_audit_events_trace_id       ON audit_events (trace_id) WHERE trace_id IS NOT NULL;
CREATE INDEX idx_audit_events_session_id     ON audit_events (session_id) WHERE session_id IS NOT NULL;
```

File: `migrations/000002_audit_table.down.sql`

```sql
DROP TABLE IF EXISTS audit_events CASCADE;
```

---

## 4. Migration 000003 — OAuth Tables

File: `migrations/000003_oauth_tables.up.sql`

```sql
-- ─────────────────────────────────────────
-- OAUTH APPLICATIONS
-- ─────────────────────────────────────────
CREATE TABLE oauth_applications (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          TEXT        NOT NULL,
    client_id     TEXT        NOT NULL,
    client_secret TEXT        NOT NULL,   -- bcrypt(cost=12)
    redirect_uris TEXT[]      NOT NULL,
    grant_types   TEXT[]      NOT NULL DEFAULT '{authorization_code}',
    scopes        TEXT[]      NOT NULL DEFAULT '{mcp:call}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT oauth_applications_client_id_unique UNIQUE (client_id)
);

-- ─────────────────────────────────────────
-- OAUTH AUTHORIZATION CODES (short-lived)
-- ─────────────────────────────────────────
CREATE TABLE oauth_auth_codes (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    app_id        UUID        NOT NULL REFERENCES oauth_applications(id) ON DELETE CASCADE,
    code_hash     TEXT        NOT NULL,   -- SHA-256(code)
    redirect_uri  TEXT        NOT NULL,
    scopes        TEXT[]      NOT NULL,
    code_challenge TEXT,                  -- PKCE: S256 code_challenge (base64url)
    used          BOOLEAN     NOT NULL DEFAULT false,
    expires_at    TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT oauth_auth_codes_code_hash_unique UNIQUE (code_hash)
);

CREATE INDEX idx_oauth_auth_codes_expires ON oauth_auth_codes (expires_at);

-- ─────────────────────────────────────────
-- OAUTH REFRESH TOKENS
-- ─────────────────────────────────────────
CREATE TABLE oauth_refresh_tokens (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    app_id        UUID        NOT NULL REFERENCES oauth_applications(id) ON DELETE CASCADE,
    token_hash    TEXT        NOT NULL,   -- SHA-256(refresh_token)
    scopes        TEXT[]      NOT NULL,
    revoked       BOOLEAN     NOT NULL DEFAULT false,
    expires_at    TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT oauth_refresh_tokens_token_hash_unique UNIQUE (token_hash)
);

CREATE INDEX idx_oauth_refresh_tokens_expires ON oauth_refresh_tokens (expires_at);
```

File: `migrations/000003_oauth_tables.down.sql`

```sql
DROP TABLE IF EXISTS oauth_refresh_tokens;
DROP TABLE IF EXISTS oauth_auth_codes;
DROP TABLE IF EXISTS oauth_applications;
```

---

## 5. Migration 000004 — Plugins & Webhooks

File: `migrations/000004_plugins_webhooks.up.sql`

```sql
-- ─────────────────────────────────────────
-- PLUGINS (global registry)
-- ─────────────────────────────────────────
CREATE TABLE plugins (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT        NOT NULL,
    version     TEXT        NOT NULL,
    plugin_type TEXT        NOT NULL DEFAULT 'builtin',  -- 'builtin' | 'http_callback'
    description TEXT,
    config_schema JSONB     NOT NULL DEFAULT '{}',       -- JSON Schema for plugin config
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT plugins_name_version_unique UNIQUE (name, version),
    CONSTRAINT plugins_type_values CHECK (plugin_type IN ('builtin', 'http_callback'))
);

-- ─────────────────────────────────────────
-- TENANT PLUGINS (per-tenant enable/config)
-- ─────────────────────────────────────────
CREATE TABLE tenant_plugins (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    plugin_id   UUID        NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
    enabled     BOOLEAN     NOT NULL DEFAULT true,
    config      JSONB       NOT NULL DEFAULT '{}',
    priority    INT         NOT NULL DEFAULT 100,  -- lower = runs first
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT tenant_plugins_unique UNIQUE (tenant_id, plugin_id)
);

-- ─────────────────────────────────────────
-- WEBHOOK CONFIGS
-- ─────────────────────────────────────────
CREATE TABLE webhook_configs (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    url         TEXT        NOT NULL,
    secret      TEXT        NOT NULL,   -- HMAC-SHA256 signing secret
    events      TEXT[]      NOT NULL,   -- e.g. '{policy.violation,ratelimit.exceeded}'
    enabled     BOOLEAN     NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_webhook_configs_tenant_id ON webhook_configs (tenant_id) WHERE enabled = true;

-- ─────────────────────────────────────────
-- WEBHOOK DELIVERIES (for retry tracking)
-- ─────────────────────────────────────────
CREATE TABLE webhook_deliveries (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    webhook_id    UUID        NOT NULL REFERENCES webhook_configs(id) ON DELETE CASCADE,
    event_type    TEXT        NOT NULL,
    payload       JSONB       NOT NULL,
    attempts      INT         NOT NULL DEFAULT 0,
    status        TEXT        NOT NULL DEFAULT 'pending',  -- 'pending' | 'delivered' | 'failed'
    last_response TEXT,
    next_retry_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_webhook_deliveries_retry ON webhook_deliveries (next_retry_at)
    WHERE status = 'pending';
```

File: `migrations/000004_plugins_webhooks.down.sql`

```sql
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_configs;
DROP TABLE IF EXISTS tenant_plugins;
DROP TABLE IF EXISTS plugins;
```

---

## 6. Store Layer — `internal/store/`

### `internal/store/db.go`

```go
package store

import (
    "context"
    "fmt"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/conduit-oss/conduit/internal/config"
)

// DB wraps a pgx connection pool.
type DB struct {
    Pool *pgxpool.Pool
}

// New creates and validates a PostgreSQL connection pool.
// It pings the database before returning.
func New(ctx context.Context, cfg *config.DatabaseConfig) (*DB, error) {
    poolCfg, err := pgxpool.ParseConfig(cfg.URL)
    if err != nil {
        return nil, fmt.Errorf("parse db url: %w", err)
    }

    poolCfg.MaxConns = int32(cfg.MaxOpenConns)        // default: 25
    poolCfg.MinConns = int32(cfg.MaxIdleConns)        // default: 5
    poolCfg.MaxConnLifetime = cfg.ConnMaxLifetime     // default: 5m
    poolCfg.MaxConnIdleTime = 5 * time.Minute

    pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
    if err != nil {
        return nil, fmt.Errorf("create pool: %w", err)
    }

    if err := pool.Ping(ctx); err != nil {
        return nil, fmt.Errorf("ping database: %w", err)
    }

    return &DB{Pool: pool}, nil
}

// Close shuts down the connection pool.
func (db *DB) Close() {
    db.Pool.Close()
}

// HealthCheck pings the database. Returns nil if healthy.
func (db *DB) HealthCheck(ctx context.Context) error {
    return db.Pool.Ping(ctx)
}
```

### `internal/store/tenants.go`

```go
package store

import (
    "context"
    "fmt"
    "time"

    "github.com/google/uuid"
)

// Tenant matches the tenants table row.
type Tenant struct {
    ID        uuid.UUID
    Slug      string
    Name      string
    Plan      string
    Settings  map[string]any
    CreatedAt time.Time
    UpdatedAt time.Time
    DeletedAt *time.Time
}

// TenantStore provides tenant data access.
type TenantStore struct {
    db *DB
}

func NewTenantStore(db *DB) *TenantStore { return &TenantStore{db: db} }

// GetBySlug retrieves an active tenant by slug. Returns ErrNotFound if absent.
func (s *TenantStore) GetBySlug(ctx context.Context, slug string) (*Tenant, error)

// GetByID retrieves an active tenant by ID. Returns ErrNotFound if absent.
func (s *TenantStore) GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error)

// List returns all active tenants, ordered by created_at ASC.
func (s *TenantStore) List(ctx context.Context) ([]*Tenant, error)

// Create inserts a new tenant. Returns ErrConflict if slug already exists.
func (s *TenantStore) Create(ctx context.Context, slug, name, plan string) (*Tenant, error)

// Update modifies name, plan, or settings. Uses SET-only-changed pattern.
func (s *TenantStore) Update(ctx context.Context, id uuid.UUID, updates TenantUpdates) (*Tenant, error)

// Delete soft-deletes a tenant by setting deleted_at.
func (s *TenantStore) Delete(ctx context.Context, id uuid.UUID) error

type TenantUpdates struct {
    Name     *string
    Plan     *string
    Settings map[string]any
}
```

### `internal/store/apikeys.go`

```go
package store

import (
    "context"
    "time"

    "github.com/google/uuid"
)

// APIKey matches the api_keys table row.
type APIKey struct {
    ID          uuid.UUID
    TenantID    uuid.UUID
    Name        string
    KeyHash     string    // SHA-256(raw_key), hex
    KeyPrefix   string    // first 12 chars
    Scopes      []string
    ExpiresAt   *time.Time
    LastUsedAt  *time.Time
    CreatedAt   time.Time
    RevokedAt   *time.Time
}

// APIKeyStore provides API key data access.
type APIKeyStore struct {
    db *DB
}

func NewAPIKeyStore(db *DB) *APIKeyStore { return &APIKeyStore{db: db} }

// GetByHash looks up an active, non-expired API key by its SHA-256 hash.
// Updates last_used_at asynchronously (best-effort, no return error).
// Returns ErrNotFound if the key does not exist or is revoked/expired.
func (s *APIKeyStore) GetByHash(ctx context.Context, keyHash string) (*APIKey, error)

// Create inserts a new API key. The caller provides the already-computed hash.
// Returns ErrConflict if the hash already exists (astronomically unlikely but guarded).
func (s *APIKeyStore) Create(ctx context.Context, tenantID uuid.UUID, name, keyHash, keyPrefix string, scopes []string, expiresAt *time.Time) (*APIKey, error)

// List returns all active API keys for a tenant, ordered by created_at DESC.
func (s *APIKeyStore) List(ctx context.Context, tenantID uuid.UUID) ([]*APIKey, error)

// Revoke sets revoked_at on an API key. Returns ErrNotFound if absent.
func (s *APIKeyStore) Revoke(ctx context.Context, id uuid.UUID) error

// UpdateLastUsed sets last_used_at to now. Called on every successful auth.
// Uses a fire-and-forget goroutine with its own timeout context.
func (s *APIKeyStore) UpdateLastUsed(ctx context.Context, id uuid.UUID)
```

### `internal/store/servers.go`

```go
package store

import (
    "context"
    "time"

    "github.com/google/uuid"
)

// MCPServer matches the mcp_servers table row.
type MCPServer struct {
    ID             uuid.UUID
    TenantID       uuid.UUID
    Name           string
    UpstreamURL    string
    AuthType       string
    AuthConfig     map[string]any  // decrypted at app layer
    HealthCheckURL *string
    Weight         int
    Enabled        bool
    CreatedAt      time.Time
    UpdatedAt      time.Time
}

// MCPServerStore provides MCP server data access.
type MCPServerStore struct {
    db *DB
}

func NewMCPServerStore(db *DB) *MCPServerStore { return &MCPServerStore{db: db} }

// GetByTenantAndName returns an enabled server by (tenant_id, name).
func (s *MCPServerStore) GetByTenantAndName(ctx context.Context, tenantID uuid.UUID, name string) (*MCPServer, error)

// ListByTenant returns all enabled servers for a tenant.
func (s *MCPServerStore) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*MCPServer, error)

// Create registers a new MCP server. Returns ErrConflict if (tenant_id, name) exists.
func (s *MCPServerStore) Create(ctx context.Context, input CreateServerInput) (*MCPServer, error)

// Update modifies server fields. Returns ErrNotFound if absent.
func (s *MCPServerStore) Update(ctx context.Context, id uuid.UUID, updates ServerUpdates) (*MCPServer, error)

// Delete removes a server (hard delete — no audit trail needed here).
func (s *MCPServerStore) Delete(ctx context.Context, id uuid.UUID) error

type CreateServerInput struct {
    TenantID       uuid.UUID
    Name           string
    UpstreamURL    string
    AuthType       string
    AuthConfig     map[string]any
    HealthCheckURL *string
    Weight         int
}

type ServerUpdates struct {
    UpstreamURL    *string
    AuthType       *string
    AuthConfig     map[string]any
    HealthCheckURL *string
    Weight         *int
    Enabled        *bool
}
```

---

## 7. Sentinel Errors

```go
package store

import "errors"

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("record not found")

// ErrConflict is returned when a unique constraint would be violated.
var ErrConflict = errors.New("record already exists")

// ErrDeleted is returned when operating on a soft-deleted record.
var ErrDeleted = errors.New("record has been deleted")
```

All store methods MUST return these sentinel errors (not `pgx.ErrNoRows` directly) so callers can use `errors.Is()`.

---

## 8. Query Patterns

```go
// Pattern for single-row query:
row := s.db.Pool.QueryRow(ctx, query, args...)
var t Tenant
err := row.Scan(&t.ID, &t.Slug, ...)
if errors.Is(err, pgx.ErrNoRows) {
    return nil, ErrNotFound
}
if err != nil {
    return nil, fmt.Errorf("query tenant: %w", err)
}

// Pattern for multi-row query:
rows, err := s.db.Pool.Query(ctx, query, args...)
if err != nil {
    return nil, fmt.Errorf("list tenants: %w", err)
}
defer rows.Close()

var results []*Tenant
for rows.Next() {
    var t Tenant
    if err := rows.Scan(&t.ID, &t.Slug, ...); err != nil {
        return nil, fmt.Errorf("scan tenant: %w", err)
    }
    results = append(results, &t)
}
if err := rows.Err(); err != nil {
    return nil, fmt.Errorf("rows error: %w", err)
}
return results, nil

// Pattern for INSERT with RETURNING:
var id uuid.UUID
err = s.db.Pool.QueryRow(ctx, `
    INSERT INTO tenants (slug, name, plan)
    VALUES ($1, $2, $3)
    RETURNING id
`, slug, name, plan).Scan(&id)
if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
    return nil, ErrConflict
}
```

---

## 9. Performance Indexes (Summary)

| Table | Index | Purpose |
|---|---|---|
| `tenants` | `idx_tenants_slug` | Slug lookup on every proxy request (after routing) |
| `api_keys` | `idx_api_keys_key_hash` | Auth on every request (primary hot path) |
| `api_keys` | `idx_api_keys_tenant_id` | List keys for management UI |
| `mcp_servers` | `idx_mcp_servers_tenant_id` | Routing table load |
| `audit_events` | `idx_audit_events_tenant_created` | Paginated audit log queries |
| `audit_events` | `idx_audit_events_tenant_tool` | Filter by tool name |
| `rate_limit_configs` | `idx_rate_limit_configs_tenant_id` | Rate limit config lookup |
| `webhook_deliveries` | `idx_webhook_deliveries_retry` | Retry queue scan |

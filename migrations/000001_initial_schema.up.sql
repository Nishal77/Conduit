-- Enable UUID generation
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- =========================================
-- TENANTS
-- =========================================
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

-- =========================================
-- API KEYS
-- =========================================
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

-- =========================================
-- MCP SERVERS
-- =========================================
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

-- =========================================
-- RATE LIMIT CONFIGURATIONS
-- =========================================
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

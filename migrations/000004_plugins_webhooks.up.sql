-- ─────────────────────────────────────────
-- PLUGINS (catalog of built-in and http_callback plugins)
-- ─────────────────────────────────────────
CREATE TABLE plugins (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT        NOT NULL,
    version       TEXT        NOT NULL,
    plugin_type   TEXT        NOT NULL DEFAULT 'builtin',
    description   TEXT,
    config_schema JSONB       NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

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
    priority    INT         NOT NULL DEFAULT 100,
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
    secret      TEXT        NOT NULL,
    events      TEXT[]      NOT NULL,
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
    status        TEXT        NOT NULL DEFAULT 'pending',
    last_response TEXT,
    next_retry_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT webhook_deliveries_status_values CHECK (status IN ('pending', 'delivered', 'failed'))
);

CREATE INDEX idx_webhook_deliveries_retry ON webhook_deliveries (next_retry_at)
    WHERE status = 'pending';

-- Seed the catalog of built-in plugins shipped in this release
-- (spec/14-plugins.md §4) so tenant_plugins has something to reference —
-- tenants opt in per-plugin via the management API, nothing is enabled by
-- default.
INSERT INTO plugins (name, version, plugin_type, description) VALUES
    ('pii-redactor',    '1.0.0', 'builtin', 'Detects and redacts PII (email, phone, SSN, card, secrets) in request arguments'),
    ('cost-tracker',    '1.0.0', 'builtin', 'Estimates per-call token cost and records it on the audit event'),
    ('circuit-breaker', '1.0.0', 'builtin', 'Opens the circuit and blocks calls to a server after repeated upstream failures'),
    ('logger',          '1.0.0', 'builtin', 'Adds structured before/after log entries for every tool call'),
    ('transform',       '1.0.0', 'builtin', 'JSONPath-based request/response field manipulation');

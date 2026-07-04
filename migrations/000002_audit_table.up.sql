-- =========================================
-- AUDIT EVENTS (append-only, partitioned)
-- =========================================
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

-- =========================================
-- OAUTH APPLICATIONS
-- =========================================
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

-- =========================================
-- OAUTH AUTHORIZATION CODES (short-lived)
-- =========================================
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

-- =========================================
-- OAUTH REFRESH TOKENS
-- =========================================
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

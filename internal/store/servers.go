package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MCPServer matches a row of the mcp_servers table. AuthConfig holds
// upstream credentials (e.g. a bearer token for the MCP server itself,
// distinct from Conduit's own auth); MCPServerStore encrypts it with
// AES-256-GCM before it reaches PostgreSQL and decrypts it on every read,
// so every *MCPServer a caller sees always has the real, usable
// credentials — see crypto.go and spec/13-multitenant.md §5.
type MCPServer struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	Name           string
	UpstreamURL    string
	AuthType       string
	AuthConfig     map[string]any
	HealthCheckURL *string
	Weight         int
	Enabled        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// MCPServerStore provides MCP server data access. encryptionKey may be nil
// (auth_config is stored in plaintext) — main.go only supplies one once
// auth.jwt_secret is available, which every deployment running with a
// database already requires.
type MCPServerStore struct {
	db            *DB
	encryptionKey []byte
}

// NewMCPServerStore returns an MCPServerStore backed by db. encryptionKey
// should be store.DeriveCredentialKey(cfg.Auth.JWTSecret); pass nil to
// store auth_config in plaintext (development only).
func NewMCPServerStore(db *DB, encryptionKey []byte) *MCPServerStore {
	return &MCPServerStore{db: db, encryptionKey: encryptionKey}
}

const mcpServerColumns = "id, tenant_id, name, upstream_url, auth_type, auth_config, health_check_url, weight, enabled, created_at, updated_at"

func (s *MCPServerStore) scanMCPServer(row pgx.Row) (*MCPServer, error) {
	var srv MCPServer
	if err := row.Scan(&srv.ID, &srv.TenantID, &srv.Name, &srv.UpstreamURL, &srv.AuthType, &srv.AuthConfig,
		&srv.HealthCheckURL, &srv.Weight, &srv.Enabled, &srv.CreatedAt, &srv.UpdatedAt); err != nil {
		return nil, err
	}
	if s.encryptionKey != nil {
		decrypted, err := DecryptAuthConfig(s.encryptionKey, srv.AuthConfig)
		if err != nil {
			return nil, fmt.Errorf("decrypt auth_config: %w", err)
		}
		srv.AuthConfig = decrypted
	}
	return &srv, nil
}

// GetByTenantAndName returns an enabled server by (tenant_id, name).
func (s *MCPServerStore) GetByTenantAndName(ctx context.Context, tenantID uuid.UUID, name string) (*MCPServer, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+mcpServerColumns+`
		FROM mcp_servers
		WHERE tenant_id = $1 AND name = $2 AND enabled = true
	`, tenantID, name)

	srv, err := s.scanMCPServer(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get mcp server: %w", err)
	}
	return srv, nil
}

// GetByID returns a server by its primary key, regardless of enabled
// state — used by the management API, which needs to display and manage
// disabled servers too (unlike the proxy's routing path, which only ever
// wants enabled ones).
func (s *MCPServerStore) GetByID(ctx context.Context, id uuid.UUID) (*MCPServer, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+mcpServerColumns+`
		FROM mcp_servers
		WHERE id = $1
	`, id)

	srv, err := s.scanMCPServer(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get mcp server by id: %w", err)
	}
	return srv, nil
}

// ListByTenant returns every enabled server for a tenant.
func (s *MCPServerStore) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*MCPServer, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+mcpServerColumns+`
		FROM mcp_servers
		WHERE tenant_id = $1 AND enabled = true
		ORDER BY name ASC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list mcp servers: %w", err)
	}
	defer rows.Close()

	var results []*MCPServer
	for rows.Next() {
		srv, err := s.scanMCPServer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan mcp server: %w", err)
		}
		results = append(results, srv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// ListAllEnabled returns every enabled server across every tenant, joined
// with the tenant's slug and ID. Used by tenant.Store to bulk-load the
// in-process routing table on its refresh cycle without an N+1 query per
// tenant. Includes everything the proxy's hot path needs to route and
// authenticate a request (weight for load balancing across same-named
// servers, auth_type/auth_config for upstream credential injection) so it
// never has to fall back to a per-request database call.
func (s *MCPServerStore) ListAllEnabled(ctx context.Context) ([]*MCPServerWithTenantSlug, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT t.id, t.slug, s.name, s.upstream_url, s.auth_type, s.auth_config, s.weight, s.enabled
		FROM mcp_servers s
		JOIN tenants t ON t.id = s.tenant_id
		WHERE s.enabled = true AND t.deleted_at IS NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("list all enabled mcp servers: %w", err)
	}
	defer rows.Close()

	var results []*MCPServerWithTenantSlug
	for rows.Next() {
		var r MCPServerWithTenantSlug
		if err := rows.Scan(&r.TenantID, &r.TenantSlug, &r.Name, &r.UpstreamURL, &r.AuthType, &r.AuthConfig, &r.Weight, &r.Enabled); err != nil {
			return nil, fmt.Errorf("scan mcp server: %w", err)
		}
		if s.encryptionKey != nil {
			decrypted, err := DecryptAuthConfig(s.encryptionKey, r.AuthConfig)
			if err != nil {
				return nil, fmt.Errorf("decrypt auth_config: %w", err)
			}
			r.AuthConfig = decrypted
		}
		results = append(results, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// MCPServerWithTenantSlug is the projection tenant.Store needs to populate
// the routing table: just enough to build a tenant.Server without a second
// round trip to resolve tenant_id -> slug.
type MCPServerWithTenantSlug struct {
	TenantID    uuid.UUID
	TenantSlug  string
	Name        string
	UpstreamURL string
	AuthType    string
	AuthConfig  map[string]any
	Weight      int
	Enabled     bool
}

// Create registers a new MCP server. Returns ErrConflict if (tenant_id, name)
// already exists.
func (s *MCPServerStore) Create(ctx context.Context, input CreateServerInput) (*MCPServer, error) {
	authConfig, err := s.prepareAuthConfigForStorage(input.AuthConfig)
	if err != nil {
		return nil, err
	}

	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO mcp_servers (tenant_id, name, upstream_url, auth_type, auth_config, health_check_url, weight)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+mcpServerColumns+`
	`, input.TenantID, input.Name, input.UpstreamURL, input.AuthType, authConfig, input.HealthCheckURL, input.Weight)

	srv, err := s.scanMCPServer(row)
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("create mcp server: %w", err)
	}
	return srv, nil
}

// prepareAuthConfigForStorage normalizes and, if a key is configured,
// encrypts an auth_config value before it's written. See MCPServerStore's
// doc comment on nil-map normalization for why this can't be skipped even
// when encryption is disabled.
func (s *MCPServerStore) prepareAuthConfigForStorage(authConfig map[string]any) (map[string]any, error) {
	if authConfig == nil {
		// The column is NOT NULL DEFAULT '{}', but that default only
		// applies when a column is omitted from the INSERT entirely — a
		// nil Go map still marshals to SQL NULL, not "no value provided",
		// so it must be normalized here rather than relying on the schema.
		authConfig = map[string]any{}
	}
	if s.encryptionKey == nil || len(authConfig) == 0 {
		return authConfig, nil
	}
	encrypted, err := EncryptAuthConfig(s.encryptionKey, authConfig)
	if err != nil {
		return nil, fmt.Errorf("encrypt auth_config: %w", err)
	}
	return encrypted, nil
}

// CreateServerInput carries the fields needed to register a new MCP server.
type CreateServerInput struct {
	TenantID       uuid.UUID
	Name           string
	UpstreamURL    string
	AuthType       string
	AuthConfig     map[string]any
	HealthCheckURL *string
	Weight         int
}

// Update modifies server fields using a SET-only-changed pattern. Returns
// ErrNotFound if id doesn't match an existing server.
func (s *MCPServerStore) Update(ctx context.Context, id uuid.UUID, updates ServerUpdates) (*MCPServer, error) {
	var authConfig map[string]any
	if updates.AuthConfig != nil {
		encrypted, err := s.prepareAuthConfigForStorage(updates.AuthConfig)
		if err != nil {
			return nil, err
		}
		authConfig = encrypted
	}

	row := s.db.Pool.QueryRow(ctx, `
		UPDATE mcp_servers
		SET
			upstream_url     = COALESCE($2, upstream_url),
			auth_type        = COALESCE($3, auth_type),
			auth_config      = COALESCE($4, auth_config),
			health_check_url = COALESCE($5, health_check_url),
			weight           = COALESCE($6, weight),
			enabled          = COALESCE($7, enabled),
			updated_at       = NOW()
		WHERE id = $1
		RETURNING `+mcpServerColumns+`
	`, id, updates.UpstreamURL, updates.AuthType, authConfig, updates.HealthCheckURL, updates.Weight, updates.Enabled)

	srv, err := s.scanMCPServer(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update mcp server: %w", err)
	}
	return srv, nil
}

// ServerUpdates carries the SET-only-changed fields for MCPServerStore.Update.
type ServerUpdates struct {
	UpstreamURL    *string
	AuthType       *string
	AuthConfig     map[string]any
	HealthCheckURL *string
	Weight         *int
	Enabled        *bool
}

// Delete removes a server row outright — hard delete, since (unlike
// tenants) there's no audit requirement to keep a record of a deregistered
// upstream around.
func (s *MCPServerStore) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool.Exec(ctx, `DELETE FROM mcp_servers WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete mcp server: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

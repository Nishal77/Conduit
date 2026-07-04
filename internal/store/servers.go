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
// distinct from Conduit's own auth) and is expected to be encrypted at the
// application layer before it reaches this store — see spec/20-security.md.
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

// MCPServerStore provides MCP server data access.
type MCPServerStore struct {
	db *DB
}

// NewMCPServerStore returns an MCPServerStore backed by db.
func NewMCPServerStore(db *DB) *MCPServerStore { return &MCPServerStore{db: db} }

const mcpServerColumns = "id, tenant_id, name, upstream_url, auth_type, auth_config, health_check_url, weight, enabled, created_at, updated_at"

func scanMCPServer(row pgx.Row) (*MCPServer, error) {
	var s MCPServer
	if err := row.Scan(&s.ID, &s.TenantID, &s.Name, &s.UpstreamURL, &s.AuthType, &s.AuthConfig, &s.HealthCheckURL, &s.Weight, &s.Enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// GetByTenantAndName returns an enabled server by (tenant_id, name).
func (s *MCPServerStore) GetByTenantAndName(ctx context.Context, tenantID uuid.UUID, name string) (*MCPServer, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+mcpServerColumns+`
		FROM mcp_servers
		WHERE tenant_id = $1 AND name = $2 AND enabled = true
	`, tenantID, name)

	srv, err := scanMCPServer(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get mcp server: %w", err)
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
		srv, err := scanMCPServer(rows)
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
// with the tenant's slug. Used by tenant.Store to bulk-load the in-process
// routing table on its refresh cycle without an N+1 query per tenant.
func (s *MCPServerStore) ListAllEnabled(ctx context.Context) ([]*MCPServerWithTenantSlug, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT s.name, s.upstream_url, s.enabled, t.slug
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
		if err := rows.Scan(&r.Name, &r.UpstreamURL, &r.Enabled, &r.TenantSlug); err != nil {
			return nil, fmt.Errorf("scan mcp server: %w", err)
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
	TenantSlug  string
	Name        string
	UpstreamURL string
	Enabled     bool
}

// Create registers a new MCP server. Returns ErrConflict if (tenant_id, name)
// already exists.
func (s *MCPServerStore) Create(ctx context.Context, input CreateServerInput) (*MCPServer, error) {
	authConfig := input.AuthConfig
	if authConfig == nil {
		// The column is NOT NULL DEFAULT '{}', but that default only
		// applies when a column is omitted from the INSERT entirely — a
		// nil Go map still marshals to SQL NULL, not "no value provided",
		// so it must be normalized here rather than relying on the schema.
		authConfig = map[string]any{}
	}

	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO mcp_servers (tenant_id, name, upstream_url, auth_type, auth_config, health_check_url, weight)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+mcpServerColumns+`
	`, input.TenantID, input.Name, input.UpstreamURL, input.AuthType, authConfig, input.HealthCheckURL, input.Weight)

	srv, err := scanMCPServer(row)
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("create mcp server: %w", err)
	}
	return srv, nil
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
	`, id, updates.UpstreamURL, updates.AuthType, updates.AuthConfig, updates.HealthCheckURL, updates.Weight, updates.Enabled)

	srv, err := scanMCPServer(row)
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

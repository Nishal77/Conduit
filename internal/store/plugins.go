package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Plugin matches a row of the plugins table: the catalog of built-in and
// http_callback plugins Conduit knows about. Tenants opt into a plugin (and
// configure it) via a TenantPlugin row, not by editing this catalog.
type Plugin struct {
	ID           uuid.UUID
	Name         string
	Version      string
	PluginType   string // "builtin" | "http_callback"
	Description  string
	ConfigSchema map[string]any
	CreatedAt    time.Time
}

// TenantPlugin matches a row of the tenant_plugins table: one tenant's
// enable/disable state, config, and execution priority for one plugin.
type TenantPlugin struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	PluginID  uuid.UUID
	Enabled   bool
	Config    map[string]any
	Priority  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PluginStore provides plugin catalog and per-tenant enablement data access.
type PluginStore struct{ db *DB }

// NewPluginStore returns a PluginStore backed by db.
func NewPluginStore(db *DB) *PluginStore { return &PluginStore{db: db} }

const pluginColumns = "id, name, version, plugin_type, description, config_schema, created_at"

func scanPlugin(row pgx.Row) (*Plugin, error) {
	var p Plugin
	var description *string
	if err := row.Scan(&p.ID, &p.Name, &p.Version, &p.PluginType, &description, &p.ConfigSchema, &p.CreatedAt); err != nil {
		return nil, err
	}
	if description != nil {
		p.Description = *description
	}
	return &p, nil
}

// List returns every plugin in the catalog, ordered by name.
func (s *PluginStore) List(ctx context.Context) ([]*Plugin, error) {
	rows, err := s.db.Pool.Query(ctx, `SELECT `+pluginColumns+` FROM plugins ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list plugins: %w", err)
	}
	defer rows.Close()

	var results []*Plugin
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, fmt.Errorf("scan plugin: %w", err)
		}
		results = append(results, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// GetByName returns the newest registered version of a plugin by name.
// Conduit's built-in plugins are seeded one row per name (spec/14-plugins.md
// §4's five plugins), so "newest" only matters once a plugin publishes a
// second version.
func (s *PluginStore) GetByName(ctx context.Context, name string) (*Plugin, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+pluginColumns+`
		FROM plugins
		WHERE name = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, name)

	p, err := scanPlugin(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get plugin by name: %w", err)
	}
	return p, nil
}

// GetByID returns a plugin by its primary key.
func (s *PluginStore) GetByID(ctx context.Context, id uuid.UUID) (*Plugin, error) {
	row := s.db.Pool.QueryRow(ctx, `SELECT `+pluginColumns+` FROM plugins WHERE id = $1`, id)
	p, err := scanPlugin(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get plugin by id: %w", err)
	}
	return p, nil
}

const tenantPluginColumns = "id, tenant_id, plugin_id, enabled, config, priority, created_at, updated_at"

func scanTenantPlugin(row pgx.Row) (*TenantPlugin, error) {
	var tp TenantPlugin
	if err := row.Scan(&tp.ID, &tp.TenantID, &tp.PluginID, &tp.Enabled, &tp.Config, &tp.Priority, &tp.CreatedAt, &tp.UpdatedAt); err != nil {
		return nil, err
	}
	return &tp, nil
}

// ListByTenant returns every plugin a tenant has configured (enabled or
// not) — used by the management API's plugin page, which shows the whole
// catalog with each tenant's current state, not just what's active.
func (s *PluginStore) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*TenantPlugin, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+tenantPluginColumns+`
		FROM tenant_plugins
		WHERE tenant_id = $1
		ORDER BY priority ASC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list tenant plugins: %w", err)
	}
	defer rows.Close()

	var results []*TenantPlugin
	for rows.Next() {
		tp, err := scanTenantPlugin(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tenant plugin: %w", err)
		}
		results = append(results, tp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// ListAllEnabled returns every enabled tenant_plugins row across every
// tenant, joined with the plugin's name — the projection
// plugin.DBRegistry's refresh loop needs to build the in-process Registry
// without an N+1 query per tenant, mirroring
// MCPServerStore.ListAllEnabled's shape for the same reason.
func (s *PluginStore) ListAllEnabled(ctx context.Context) ([]*TenantPluginWithName, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT tp.tenant_id, p.name, p.version, p.plugin_type, tp.config, tp.priority
		FROM tenant_plugins tp
		JOIN plugins p ON p.id = tp.plugin_id
		WHERE tp.enabled = true
	`)
	if err != nil {
		return nil, fmt.Errorf("list all enabled tenant plugins: %w", err)
	}
	defer rows.Close()

	var results []*TenantPluginWithName
	for rows.Next() {
		var r TenantPluginWithName
		if err := rows.Scan(&r.TenantID, &r.PluginName, &r.PluginVersion, &r.PluginType, &r.Config, &r.Priority); err != nil {
			return nil, fmt.Errorf("scan tenant plugin: %w", err)
		}
		results = append(results, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// TenantPluginWithName is the projection ListAllEnabled returns.
type TenantPluginWithName struct {
	TenantID      uuid.UUID
	PluginName    string
	PluginVersion string
	PluginType    string
	Config        map[string]any
	Priority      int
}

// UpsertTenantPluginInput carries the fields for PluginStore.Upsert.
type UpsertTenantPluginInput struct {
	TenantID uuid.UUID
	PluginID uuid.UUID
	Enabled  bool
	Config   map[string]any
	Priority int
}

// Upsert creates or updates a tenant's configuration for a plugin, relying
// on the table's unique (tenant_id, plugin_id) constraint to detect
// "update" versus "create".
func (s *PluginStore) Upsert(ctx context.Context, input UpsertTenantPluginInput) (*TenantPlugin, error) {
	if input.Config == nil {
		input.Config = map[string]any{}
	}
	if input.Priority == 0 {
		input.Priority = 100
	}

	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO tenant_plugins (tenant_id, plugin_id, enabled, config, priority)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT ON CONSTRAINT tenant_plugins_unique
		DO UPDATE SET
			enabled    = EXCLUDED.enabled,
			config     = EXCLUDED.config,
			priority   = EXCLUDED.priority,
			updated_at = NOW()
		RETURNING `+tenantPluginColumns+`
	`, input.TenantID, input.PluginID, input.Enabled, input.Config, input.Priority)

	tp, err := scanTenantPlugin(row)
	if err != nil {
		return nil, fmt.Errorf("upsert tenant plugin: %w", err)
	}
	return tp, nil
}

// Delete removes a tenant's configuration for a plugin, reverting it to
// "not configured" (distinct from "configured but disabled"). Returns
// ErrNotFound if id doesn't match an existing row.
func (s *PluginStore) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool.Exec(ctx, `DELETE FROM tenant_plugins WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete tenant plugin: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

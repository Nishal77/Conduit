package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Tenant matches a row of the tenants table.
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

// NewTenantStore returns a TenantStore backed by db.
func NewTenantStore(db *DB) *TenantStore { return &TenantStore{db: db} }

const tenantColumns = "id, slug, name, plan, settings, created_at, updated_at, deleted_at"

func scanTenant(row pgx.Row) (*Tenant, error) {
	var t Tenant
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.Plan, &t.Settings, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// GetBySlug retrieves an active (non-deleted) tenant by slug.
func (s *TenantStore) GetBySlug(ctx context.Context, slug string) (*Tenant, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+tenantColumns+`
		FROM tenants
		WHERE slug = $1 AND deleted_at IS NULL
	`, slug)

	t, err := scanTenant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant by slug: %w", err)
	}
	return t, nil
}

// GetByID retrieves an active tenant by ID.
func (s *TenantStore) GetByID(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+tenantColumns+`
		FROM tenants
		WHERE id = $1 AND deleted_at IS NULL
	`, id)

	t, err := scanTenant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant by id: %w", err)
	}
	return t, nil
}

// List returns every active tenant, oldest first.
func (s *TenantStore) List(ctx context.Context) ([]*Tenant, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+tenantColumns+`
		FROM tenants
		WHERE deleted_at IS NULL
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	var results []*Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		results = append(results, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// Create inserts a new tenant. Returns ErrConflict if slug is already taken.
func (s *TenantStore) Create(ctx context.Context, slug, name, plan string) (*Tenant, error) {
	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO tenants (slug, name, plan)
		VALUES ($1, $2, $3)
		RETURNING `+tenantColumns+`
	`, slug, name, plan)

	t, err := scanTenant(row)
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("create tenant: %w", err)
	}
	return t, nil
}

// TenantUpdates carries the SET-only-changed fields for TenantStore.Update.
// A nil field means "leave unchanged"; Settings is replaced wholesale when
// non-nil (matching JSONB's own replace-not-merge default).
type TenantUpdates struct {
	Name     *string
	Plan     *string
	Settings map[string]any
}

// Update modifies name, plan, and/or settings on an existing tenant. Returns
// ErrNotFound if id doesn't match an active tenant.
func (s *TenantStore) Update(ctx context.Context, id uuid.UUID, updates TenantUpdates) (*Tenant, error) {
	row := s.db.Pool.QueryRow(ctx, `
		UPDATE tenants
		SET
			name       = COALESCE($2, name),
			plan       = COALESCE($3, plan),
			settings   = COALESCE($4, settings),
			updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING `+tenantColumns+`
	`, id, updates.Name, updates.Plan, updates.Settings)

	t, err := scanTenant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update tenant: %w", err)
	}
	return t, nil
}

// Delete soft-deletes a tenant by setting deleted_at. Returns ErrNotFound if
// id doesn't match an active tenant.
func (s *TenantStore) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE tenants SET deleted_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`, id)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RateLimitConfig matches a row of the rate_limit_configs table.
type RateLimitConfig struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Scope     string // "tenant" | "server" | "tool" | "agent"
	Target    *string
	Requests  int
	WindowSec int
	Burst     *int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// RateLimitStore provides rate limit config data access.
type RateLimitStore struct {
	db *DB
}

// NewRateLimitStore returns a RateLimitStore backed by db.
func NewRateLimitStore(db *DB) *RateLimitStore { return &RateLimitStore{db: db} }

const rateLimitColumns = "id, tenant_id, scope, target, requests, window_sec, burst, created_at, updated_at"

func scanRateLimitConfig(row pgx.Row) (*RateLimitConfig, error) {
	var c RateLimitConfig
	if err := row.Scan(&c.ID, &c.TenantID, &c.Scope, &c.Target, &c.Requests, &c.WindowSec, &c.Burst, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// GetForScope loads the most specific rate limit config for (tenantID,
// scope, target): an exact target match wins over the scope-wide (NULL
// target) default. Returns ErrNotFound if neither exists — callers fall
// back to the global defaults in config.RateLimitConfig.
func (s *RateLimitStore) GetForScope(ctx context.Context, tenantID uuid.UUID, scope, target string) (*RateLimitConfig, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+rateLimitColumns+`
		FROM rate_limit_configs
		WHERE tenant_id = $1 AND scope = $2 AND (target = $3 OR target IS NULL)
		ORDER BY target IS NULL ASC
		LIMIT 1
	`, tenantID, scope, target)

	c, err := scanRateLimitConfig(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get rate limit config: %w", err)
	}
	return c, nil
}

// List returns every rate limit config for a tenant.
func (s *RateLimitStore) List(ctx context.Context, tenantID uuid.UUID) ([]*RateLimitConfig, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+rateLimitColumns+`
		FROM rate_limit_configs
		WHERE tenant_id = $1
		ORDER BY scope ASC, target ASC NULLS FIRST
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list rate limit configs: %w", err)
	}
	defer rows.Close()

	var results []*RateLimitConfig
	for rows.Next() {
		c, err := scanRateLimitConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("scan rate limit config: %w", err)
		}
		results = append(results, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// UpsertRateLimitInput carries the fields for RateLimitStore.Upsert.
type UpsertRateLimitInput struct {
	TenantID  uuid.UUID
	Scope     string
	Target    *string
	Requests  int
	WindowSec int
	Burst     *int
}

// Upsert creates or updates the rate limit config for (tenant_id, scope,
// target), relying on the table's unique constraint to detect "update"
// versus "create".
func (s *RateLimitStore) Upsert(ctx context.Context, input UpsertRateLimitInput) (*RateLimitConfig, error) {
	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO rate_limit_configs (tenant_id, scope, target, requests, window_sec, burst)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT ON CONSTRAINT rate_limit_configs_unique
		DO UPDATE SET
			requests   = EXCLUDED.requests,
			window_sec = EXCLUDED.window_sec,
			burst      = EXCLUDED.burst,
			updated_at = NOW()
		RETURNING `+rateLimitColumns+`
	`, input.TenantID, input.Scope, input.Target, input.Requests, input.WindowSec, input.Burst)

	c, err := scanRateLimitConfig(row)
	if err != nil {
		return nil, fmt.Errorf("upsert rate limit config: %w", err)
	}
	return c, nil
}

// Delete removes a rate limit config. Returns ErrNotFound if id doesn't
// match an existing row.
func (s *RateLimitStore) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool.Exec(ctx, `DELETE FROM rate_limit_configs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete rate limit config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

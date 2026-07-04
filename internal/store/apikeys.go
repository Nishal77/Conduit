package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// APIKey matches a row of the api_keys table. The raw key itself is never
// stored — only KeyHash (SHA-256, hex) and KeyPrefix (first 12 chars, for
// display in the dashboard) — see spec/05-auth.md §8.
type APIKey struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	Name       string
	KeyHash    string
	KeyPrefix  string
	Scopes     []string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
	RevokedAt  *time.Time
}

// APIKeyStore provides API key data access.
type APIKeyStore struct {
	db *DB
}

// NewAPIKeyStore returns an APIKeyStore backed by db.
func NewAPIKeyStore(db *DB) *APIKeyStore { return &APIKeyStore{db: db} }

const apiKeyColumns = "id, tenant_id, name, key_hash, key_prefix, scopes, expires_at, last_used_at, created_at, revoked_at"

func scanAPIKey(row pgx.Row) (*APIKey, error) {
	var k APIKey
	if err := row.Scan(&k.ID, &k.TenantID, &k.Name, &k.KeyHash, &k.KeyPrefix, &k.Scopes, &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.RevokedAt); err != nil {
		return nil, err
	}
	return &k, nil
}

// GetByHash looks up a non-revoked API key by its SHA-256 hash. It does NOT
// check expiry — that's the auth validator's job (spec/05-auth.md §3 step
// 5), since "expired" and "not found" are different sentinel errors the
// caller reacts to differently.
func (s *APIKeyStore) GetByHash(ctx context.Context, keyHash string) (*APIKey, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+apiKeyColumns+`
		FROM api_keys
		WHERE key_hash = $1 AND revoked_at IS NULL
	`, keyHash)

	k, err := scanAPIKey(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get api key by hash: %w", err)
	}
	return k, nil
}

// Create inserts a new API key row. The caller supplies the already-computed
// hash and prefix (see auth.GenerateAPIKey) — this store never sees or
// stores the raw key.
func (s *APIKeyStore) Create(ctx context.Context, tenantID uuid.UUID, name, keyHash, keyPrefix string, scopes []string, expiresAt *time.Time) (*APIKey, error) {
	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO api_keys (tenant_id, name, key_hash, key_prefix, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+apiKeyColumns+`
	`, tenantID, name, keyHash, keyPrefix, scopes, expiresAt)

	k, err := scanAPIKey(row)
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}
	return k, nil
}

// List returns every active API key for a tenant, most recently created
// first.
func (s *APIKeyStore) List(ctx context.Context, tenantID uuid.UUID) ([]*APIKey, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+apiKeyColumns+`
		FROM api_keys
		WHERE tenant_id = $1 AND revoked_at IS NULL
		ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var results []*APIKey
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		results = append(results, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// Revoke sets revoked_at on an API key. Returns ErrNotFound if id doesn't
// match an active key.
func (s *APIKeyStore) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE api_keys SET revoked_at = NOW()
		WHERE id = $1 AND revoked_at IS NULL
	`, id)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateLastUsed sets last_used_at to now. It's called on every successful
// authentication, so it fires the update in its own short-lived goroutine
// with an independent timeout rather than making the request that
// authenticated wait on it — a slow write here must never add latency to
// the hot path.
func (s *APIKeyStore) UpdateLastUsed(_ context.Context, id uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := s.db.Pool.Exec(ctx, `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`, id); err != nil {
			log.Warn().Err(err).Str("api_key_id", id.String()).Msg("failed to update api key last_used_at")
		}
	}()
}

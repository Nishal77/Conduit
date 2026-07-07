package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// WebhookConfig matches a row of the webhook_configs table.
type WebhookConfig struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Name      string
	URL       string
	Secret    string
	Events    []string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// WebhookDelivery matches a row of the webhook_deliveries table: one
// attempt (and its retry state) to deliver a single event to a single
// webhook config.
type WebhookDelivery struct {
	ID           uuid.UUID
	WebhookID    uuid.UUID
	EventType    string
	Payload      map[string]any
	Attempts     int
	Status       string // "pending" | "delivered" | "failed"
	LastResponse string
	NextRetryAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WebhookStore provides webhook config and delivery data access.
type WebhookStore struct{ db *DB }

// NewWebhookStore returns a WebhookStore backed by db.
func NewWebhookStore(db *DB) *WebhookStore { return &WebhookStore{db: db} }

const webhookConfigColumns = "id, tenant_id, name, url, secret, events, enabled, created_at, updated_at"

func scanWebhookConfig(row pgx.Row) (*WebhookConfig, error) {
	var c WebhookConfig
	if err := row.Scan(&c.ID, &c.TenantID, &c.Name, &c.URL, &c.Secret, &c.Events, &c.Enabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListForEvent returns every enabled webhook config for tenantID that
// subscribes to eventType — the set Dispatcher.Dispatch fans an event out
// to.
func (s *WebhookStore) ListForEvent(ctx context.Context, tenantID uuid.UUID, eventType string) ([]*WebhookConfig, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+webhookConfigColumns+`
		FROM webhook_configs
		WHERE tenant_id = $1 AND enabled = true AND $2 = ANY(events)
	`, tenantID, eventType)
	if err != nil {
		return nil, fmt.Errorf("list webhooks for event: %w", err)
	}
	defer rows.Close()

	var results []*WebhookConfig
	for rows.Next() {
		c, err := scanWebhookConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("scan webhook config: %w", err)
		}
		results = append(results, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// ListByTenant returns every webhook config for a tenant, for the
// management API's CRUD endpoints.
func (s *WebhookStore) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*WebhookConfig, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+webhookConfigColumns+`
		FROM webhook_configs
		WHERE tenant_id = $1
		ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer rows.Close()

	var results []*WebhookConfig
	for rows.Next() {
		c, err := scanWebhookConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("scan webhook config: %w", err)
		}
		results = append(results, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// CreateWebhookInput carries the fields for WebhookStore.Create.
type CreateWebhookInput struct {
	TenantID uuid.UUID
	Name     string
	URL      string
	Secret   string
	Events   []string
}

// Create registers a new webhook config.
func (s *WebhookStore) Create(ctx context.Context, input CreateWebhookInput) (*WebhookConfig, error) {
	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO webhook_configs (tenant_id, name, url, secret, events)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+webhookConfigColumns+`
	`, input.TenantID, input.Name, input.URL, input.Secret, input.Events)

	c, err := scanWebhookConfig(row)
	if err != nil {
		return nil, fmt.Errorf("create webhook: %w", err)
	}
	return c, nil
}

// WebhookUpdates carries the SET-only-changed fields for WebhookStore.Update.
type WebhookUpdates struct {
	Name    *string
	URL     *string
	Events  []string
	Enabled *bool
}

// Update modifies a webhook config using a SET-only-changed pattern.
// Returns ErrNotFound if id doesn't match an existing row.
func (s *WebhookStore) Update(ctx context.Context, id uuid.UUID, updates WebhookUpdates) (*WebhookConfig, error) {
	row := s.db.Pool.QueryRow(ctx, `
		UPDATE webhook_configs
		SET
			name       = COALESCE($2, name),
			url        = COALESCE($3, url),
			events     = COALESCE($4, events),
			enabled    = COALESCE($5, enabled),
			updated_at = NOW()
		WHERE id = $1
		RETURNING `+webhookConfigColumns+`
	`, id, updates.Name, updates.URL, updates.Events, updates.Enabled)

	c, err := scanWebhookConfig(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update webhook: %w", err)
	}
	return c, nil
}

// Delete removes a webhook config (and, via ON DELETE CASCADE, its delivery
// history). Returns ErrNotFound if id doesn't match an existing row.
func (s *WebhookStore) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool.Exec(ctx, `DELETE FROM webhook_configs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const webhookDeliveryColumns = "id, webhook_id, event_type, payload, attempts, status, last_response, next_retry_at, created_at, updated_at"

func scanWebhookDelivery(row pgx.Row) (*WebhookDelivery, error) {
	var d WebhookDelivery
	var lastResponse *string
	if err := row.Scan(&d.ID, &d.WebhookID, &d.EventType, &d.Payload, &d.Attempts, &d.Status, &lastResponse, &d.NextRetryAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if lastResponse != nil {
		d.LastResponse = *lastResponse
	}
	return &d, nil
}

// CreateDelivery records a new delivery attempt as "pending" before the
// dispatcher's first send, so a delivery exists to update regardless of
// whether that first attempt succeeds.
func (s *WebhookStore) CreateDelivery(ctx context.Context, webhookID uuid.UUID, eventType string, payload map[string]any) (*WebhookDelivery, error) {
	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO webhook_deliveries (webhook_id, event_type, payload)
		VALUES ($1, $2, $3)
		RETURNING `+webhookDeliveryColumns+`
	`, webhookID, eventType, payload)

	d, err := scanWebhookDelivery(row)
	if err != nil {
		return nil, fmt.Errorf("create webhook delivery: %w", err)
	}
	return d, nil
}

// UpdateDelivery records the outcome of a delivery attempt: its new status,
// the (truncated) response Conduit received, how many attempts have now
// been made, and when to retry next (nil clears any pending retry, e.g. on
// success or final failure).
func (s *WebhookStore) UpdateDelivery(ctx context.Context, id uuid.UUID, status, lastResponse string, attempts int, nextRetryAt *time.Time) error {
	tag, err := s.db.Pool.Exec(ctx, `
		UPDATE webhook_deliveries
		SET status = $2, last_response = $3, attempts = $4, next_retry_at = $5, updated_at = NOW()
		WHERE id = $1
	`, id, status, lastResponse, attempts, nextRetryAt)
	if err != nil {
		return fmt.Errorf("update webhook delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListPendingRetries returns up to 100 pending deliveries whose
// next_retry_at has arrived — the batch Retrier.processPending retries on
// each poll.
func (s *WebhookStore) ListPendingRetries(ctx context.Context) ([]*WebhookDelivery, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+webhookDeliveryColumns+`
		FROM webhook_deliveries
		WHERE status = 'pending' AND next_retry_at IS NOT NULL AND next_retry_at <= NOW()
		ORDER BY next_retry_at ASC
		LIMIT 100
	`)
	if err != nil {
		return nil, fmt.Errorf("list pending webhook retries: %w", err)
	}
	defer rows.Close()

	var results []*WebhookDelivery
	for rows.Next() {
		d, err := scanWebhookDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("scan webhook delivery: %w", err)
		}
		results = append(results, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return results, nil
}

// GetConfig returns a webhook config by ID — used by the retrier to
// re-resolve a delivery's target URL/secret before retrying it.
func (s *WebhookStore) GetConfig(ctx context.Context, id uuid.UUID) (*WebhookConfig, error) {
	row := s.db.Pool.QueryRow(ctx, `SELECT `+webhookConfigColumns+` FROM webhook_configs WHERE id = $1`, id)
	c, err := scanWebhookConfig(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get webhook config: %w", err)
	}
	return c, nil
}

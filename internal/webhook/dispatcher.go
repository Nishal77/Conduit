// Package webhook delivers Conduit's outbound event notifications
// (spec/16-webhooks.md): a tenant subscribes a URL to event types like
// ratelimit.exceeded or policy.violation, and Conduit POSTs a signed JSON
// payload to it, retrying on failure with exponential backoff.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// maxResponseBytes bounds how much of a webhook receiver's response body
// Conduit stores as last_response — enough for a meaningful error message,
// not enough for a misbehaving receiver to fill the database.
const maxResponseBytes = 1024

// Dispatcher sends webhook events to every tenant-configured URL subscribed
// to that event type.
type Dispatcher struct {
	webhooks   *store.WebhookStore
	httpClient *http.Client
	retrier    *Retrier
	log        zerolog.Logger
}

// NewDispatcher returns a Dispatcher backed by webhooks. cfg's Timeout
// bounds each delivery attempt (default 10s, matching
// conduit.yaml's webhooks.timeout).
func NewDispatcher(webhooks *store.WebhookStore, cfg *config.WebhooksConfig, log zerolog.Logger) *Dispatcher {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	d := &Dispatcher{
		webhooks:   webhooks,
		httpClient: &http.Client{Timeout: timeout},
		log:        log,
	}
	d.retrier = NewRetrier(webhooks, d, cfg, log)
	return d
}

// Retrier returns the Dispatcher's Retrier, so main.go can Start/Stop its
// background polling loop alongside the dispatcher.
func (d *Dispatcher) Retrier() *Retrier { return d.retrier }

// eventPayload is the JSON body every webhook delivery carries
// (spec/16-webhooks.md §2).
type eventPayload struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	TenantID  string         `json:"tenant_id"`
	CreatedAt string         `json:"created_at"`
	Data      map[string]any `json:"data"`
}

// Dispatch sends eventType to every webhook config tenantID has subscribed
// to it. Intended to be called via `go dispatcher.Dispatch(...)` — it does
// its own I/O and must never block the caller's hot path.
func (d *Dispatcher) Dispatch(ctx context.Context, tenantID uuid.UUID, eventType string, data map[string]any) {
	configs, err := d.webhooks.ListForEvent(ctx, tenantID, eventType)
	if err != nil {
		d.log.Warn().Err(err).Str("event_type", eventType).Msg("failed to list webhooks for event")
		return
	}

	payload := eventPayload{
		ID:        uuid.New().String(),
		Type:      eventType,
		TenantID:  tenantID.String(),
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Data:      data,
	}

	for _, cfg := range configs {
		delivery, err := d.webhooks.CreateDelivery(ctx, cfg.ID, eventType, dataOrEmpty(payload.Data))
		if err != nil {
			d.log.Warn().Err(err).Str("webhook_id", cfg.ID.String()).Msg("failed to record webhook delivery")
			continue
		}

		if err := d.deliver(ctx, cfg, delivery, payload); err != nil {
			d.log.Warn().Err(err).Str("webhook_id", cfg.ID.String()).Str("event_type", eventType).Msg("webhook delivery failed, scheduling retry")
			if schedErr := d.retrier.Schedule(ctx, delivery.ID, delivery.Attempts); schedErr != nil {
				d.log.Warn().Err(schedErr).Msg("failed to schedule webhook retry")
			}
		}
	}
}

func dataOrEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// deliver sends a single delivery attempt for payload to cfg.URL and
// records the outcome. Returns nil on a 2xx response, an error otherwise —
// the caller (Dispatch or Retrier.processPending) decides what to do next
// (schedule a retry, or leave it marked failed).
func (d *Dispatcher) deliver(ctx context.Context, cfg *store.WebhookConfig, delivery *store.WebhookDelivery, payload eventPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Conduit-Event", payload.Type)
	req.Header.Set("X-Conduit-Delivery", delivery.ID.String())
	req.Header.Set("X-Conduit-Signature", signHMAC(cfg.Secret, body))
	req.Header.Set("X-Conduit-Timestamp", fmt.Sprintf("%d", time.Now().Unix()))

	attempts := delivery.Attempts + 1

	resp, err := d.httpClient.Do(req)
	if err != nil {
		_ = d.webhooks.UpdateDelivery(ctx, delivery.ID, "pending", err.Error(), attempts, nil)
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	lastResponse := fmt.Sprintf("%d %s", resp.StatusCode, string(respBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("webhook receiver returned status %d", resp.StatusCode)
		_ = d.webhooks.UpdateDelivery(ctx, delivery.ID, "pending", lastResponse, attempts, nil)
		return err
	}

	if err := d.webhooks.UpdateDelivery(ctx, delivery.ID, "delivered", lastResponse, attempts, nil); err != nil {
		d.log.Warn().Err(err).Msg("failed to mark webhook delivery delivered")
	}
	return nil
}

// signHMAC computes the X-Conduit-Signature header value (spec/16-webhooks.md
// §3): sha256=hex(HMAC-SHA256(secret, body)).
func signHMAC(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

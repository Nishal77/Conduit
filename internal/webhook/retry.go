package webhook

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// pollInterval is how often Retrier checks for deliveries whose
// next_retry_at has arrived (spec/16-webhooks.md §4).
const pollInterval = 10 * time.Second

// defaultBackoff matches conduit.yaml's webhooks.retry_backoff default.
var defaultBackoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 5 * time.Minute, 30 * time.Minute}

// Retrier schedules failed webhook deliveries for retry with exponential
// backoff and polls for ones whose retry time has arrived.
type Retrier struct {
	webhooks   *store.WebhookStore
	dispatcher *Dispatcher
	backoff    []time.Duration
	maxRetries int
	log        zerolog.Logger

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewRetrier returns a Retrier. cfg.RetryBackoff is a comma-separated list
// of durations ("1s,5s,30s,5m,30m"); an empty or unparseable value falls
// back to defaultBackoff. cfg.MaxRetries defaults to len(backoff) when unset.
func NewRetrier(webhooks *store.WebhookStore, dispatcher *Dispatcher, cfg *config.WebhooksConfig, log zerolog.Logger) *Retrier {
	backoff := parseBackoff(cfg.RetryBackoff)
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = len(backoff)
	}
	return &Retrier{
		webhooks:   webhooks,
		dispatcher: dispatcher,
		backoff:    backoff,
		maxRetries: maxRetries,
		log:        log,
		stopCh:     make(chan struct{}),
	}
}

func parseBackoff(spec string) []time.Duration {
	if spec == "" {
		return defaultBackoff
	}
	parts := strings.Split(spec, ",")
	durations := make([]time.Duration, 0, len(parts))
	for _, p := range parts {
		d, err := time.ParseDuration(strings.TrimSpace(p))
		if err != nil {
			return defaultBackoff
		}
		durations = append(durations, d)
	}
	if len(durations) == 0 {
		return defaultBackoff
	}
	return durations
}

// Start begins the retry polling loop in the background. Stops when ctx is
// cancelled or Stop is called.
func (r *Retrier) Start(ctx context.Context) {
	r.wg.Add(1)
	go r.loop(ctx)
}

// Stop signals the polling loop to exit and waits for it to do so.
func (r *Retrier) Stop() {
	close(r.stopCh)
	r.wg.Wait()
}

func (r *Retrier) loop(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.processPending(ctx)
		}
	}
}

// Schedule records a delivery for retry at now + backoff[attempt], or marks
// it permanently failed once attempt reaches maxRetries.
func (r *Retrier) Schedule(ctx context.Context, deliveryID uuid.UUID, attempt int) error {
	if attempt >= r.maxRetries {
		return r.webhooks.UpdateDelivery(ctx, deliveryID, "failed", "max retries exceeded", attempt, nil)
	}
	idx := attempt
	if idx >= len(r.backoff) {
		idx = len(r.backoff) - 1
	}
	nextRetryAt := time.Now().Add(r.backoff[idx])
	return r.webhooks.UpdateDelivery(ctx, deliveryID, "pending", "", attempt, &nextRetryAt)
}

// processPending retries every delivery whose next_retry_at has arrived,
// re-resolving its webhook config (URL/secret may have changed since the
// original attempt) before sending.
func (r *Retrier) processPending(ctx context.Context) {
	deliveries, err := r.webhooks.ListPendingRetries(ctx)
	if err != nil {
		r.log.Warn().Err(err).Msg("failed to list pending webhook retries")
		return
	}

	for _, delivery := range deliveries {
		cfg, err := r.webhooks.GetConfig(ctx, delivery.WebhookID)
		if err != nil {
			r.log.Warn().Err(err).Str("delivery_id", delivery.ID.String()).Msg("webhook config no longer exists, marking delivery failed")
			_ = r.webhooks.UpdateDelivery(ctx, delivery.ID, "failed", "webhook config deleted", delivery.Attempts, nil)
			continue
		}
		if !cfg.Enabled {
			_ = r.webhooks.UpdateDelivery(ctx, delivery.ID, "failed", "webhook disabled", delivery.Attempts, nil)
			continue
		}

		payload := eventPayload{
			ID:        deliveryEventID(delivery),
			Type:      delivery.EventType,
			TenantID:  cfg.TenantID.String(),
			CreatedAt: delivery.CreatedAt.UTC().Format(time.RFC3339Nano),
			Data:      delivery.Payload,
		}

		if err := r.dispatcher.deliver(ctx, cfg, delivery, payload); err != nil {
			if schedErr := r.Schedule(ctx, delivery.ID, delivery.Attempts+1); schedErr != nil {
				r.log.Warn().Err(schedErr).Msg("failed to reschedule webhook retry")
			}
		}
	}
}

// deliveryEventID reuses the delivery's own ID as the retried event's "id"
// field — the original Dispatch call already generated a fresh UUID for
// the first attempt, but that value isn't persisted separately from the
// delivery row, so the delivery ID itself is the stable identifier a
// webhook consumer can use to deduplicate repeated retries of the same
// logical event.
func deliveryEventID(d *store.WebhookDelivery) string { return d.ID.String() }

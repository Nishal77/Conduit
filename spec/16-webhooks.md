# Spec 16 — Webhooks

> Phase: P6 | Files: `internal/webhook/dispatcher.go`, `internal/webhook/retry.go`

---

## 1. Webhook Events

Conduit fires webhooks on these events:

| Event Type | Fired When |
|---|---|
| `ratelimit.exceeded` | A rate limit check returns denied |
| `policy.violation` | A policy rule with action=deny is triggered |
| `tool.call.success` | A tool call completes with status 200 |
| `tool.call.error` | A tool call completes with status >= 400 |
| `apikey.revoked` | An API key is revoked |
| `server.health.down` | An upstream server health check fails |
| `server.health.up` | An upstream server health check recovers |
| `audit.budget.exceeded` | Cost budget for a tenant is exhausted (Phase 8) |

Tenants subscribe to specific events when creating a webhook config.

---

## 2. Webhook Payload

```json
{
  "id": "evt-uuid-...",
  "type": "ratelimit.exceeded",
  "tenant_id": "a1b2c3d4-...",
  "created_at": "2026-07-01T12:00:00.000Z",
  "data": {
    "tool_name": "github/create_issue",
    "server_name": "github-mcp",
    "agent_id": "my-agent",
    "request_id": "req-uuid-...",
    "scope": "tool",
    "limit": 5,
    "window_sec": 3600,
    "retry_after": 42
  }
}
```

Payload structure varies by event type; the `data` field is event-specific JSONB.

---

## 3. Dispatcher — `internal/webhook/dispatcher.go`

```go
package webhook

import (
    "bytes"
    "context"
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "net/http"
    "time"

    "github.com/conduit-oss/conduit/internal/store"
    "github.com/rs/zerolog"
)

// Dispatcher sends webhook events to configured URLs.
type Dispatcher struct {
    webhookStore *store.WebhookStore
    httpClient   *http.Client
    retrier      *Retrier
    log          zerolog.Logger
}

func NewDispatcher(
    webhookStore *store.WebhookStore,
    cfg *config.WebhooksConfig,
    log zerolog.Logger,
) *Dispatcher

// Dispatch sends an event to all webhooks subscribed to that event type.
// Called asynchronously from the proxy (non-blocking).
//
// Algorithm:
//   1. Load all enabled webhook configs for this tenant where event_type IN config.events
//   2. For each config: create a webhook_deliveries record (status: "pending")
//   3. Attempt delivery immediately (first try)
//   4. On success: update delivery status to "delivered"
//   5. On failure: schedule retry via Retrier
//
// This is called in a goroutine — do NOT block the caller.
func (d *Dispatcher) Dispatch(ctx context.Context, tenantID, eventType string, data map[string]any)

// deliver sends a single webhook delivery attempt.
// Returns nil on success (2xx response), error otherwise.
func (d *Dispatcher) deliver(ctx context.Context, cfg *store.WebhookConfig, delivery *store.WebhookDelivery) error {
    // 1. Marshal payload to JSON
    // 2. Compute HMAC-SHA256 signature: sha256={hex(HMAC(secret, body))}
    // 3. POST to cfg.URL with:
    //    Content-Type: application/json
    //    X-Conduit-Event: {event_type}
    //    X-Conduit-Delivery: {delivery_id}
    //    X-Conduit-Signature: sha256={hex}
    //    X-Conduit-Timestamp: {unix_timestamp}
    // 4. Set timeout from config (default: 10s)
    // 5. Read response (discard body after 1KB)
    // 6. Update delivery record: attempts++, last_response
    // 7. Return nil if 2xx, error otherwise
}
```

---

## 4. Retry — `internal/webhook/retry.go`

```go
package webhook

import (
    "context"
    "time"
)

// Retrier schedules failed webhook deliveries for retry.
// Uses exponential backoff as configured.
type Retrier struct {
    store    *store.WebhookStore
    dispatch *Dispatcher
    backoff  []time.Duration  // parsed from config "1s,5s,30s,5m,30m"
    maxRetries int
    log      zerolog.Logger
}

func NewRetrier(store *store.WebhookStore, cfg *config.WebhooksConfig, log zerolog.Logger) *Retrier

// Start begins the retry background goroutine.
// Polls for pending deliveries every 10 seconds.
// Stops when ctx is cancelled.
func (r *Retrier) Start(ctx context.Context)

// Schedule records a delivery for retry.
// Sets next_retry_at = now + backoff[attempt]
func (r *Retrier) Schedule(ctx context.Context, deliveryID string, attempt int) error {
    // attempt 0 → first retry at backoff[0] = 1s
    // attempt 1 → backoff[1] = 5s
    // attempt 2 → backoff[2] = 30s
    // attempt 3 → backoff[3] = 5m
    // attempt 4 → backoff[4] = 30m
    // attempt >= maxRetries → mark as "failed", no more retries
}

// processPending finds all pending deliveries with next_retry_at <= now
// and retries them.
func (r *Retrier) processPending(ctx context.Context) {
    // SELECT * FROM webhook_deliveries WHERE status = 'pending' AND next_retry_at <= NOW()
    // LIMIT 100
    // For each: call dispatcher.deliver(), then schedule next retry or mark delivered/failed
}
```

---

## 5. Webhook Signature Verification (for consumers)

Document this in the OpenAPI spec and README so webhook consumers can verify:

```go
// Verification example (Go):
func verifyWebhook(secret, body []byte, signature string) bool {
    mac := hmac.New(sha256.New, secret)
    mac.Write(body)
    expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(signature))
}
```

```python
# Verification example (Python):
import hmac, hashlib
def verify_webhook(secret: str, body: bytes, signature: str) -> bool:
    expected = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, signature)
```

---

## 6. Store Layer — `internal/store/webhooks.go`

```go
package store

// WebhookConfig matches the webhook_configs table row.
type WebhookConfig struct {
    ID       uuid.UUID
    TenantID uuid.UUID
    Name     string
    URL      string
    Secret   string
    Events   []string
    Enabled  bool
    CreatedAt time.Time
    UpdatedAt time.Time
}

// WebhookDelivery matches the webhook_deliveries table row.
type WebhookDelivery struct {
    ID          uuid.UUID
    WebhookID   uuid.UUID
    EventType   string
    Payload     map[string]any
    Attempts    int
    Status      string  // "pending" | "delivered" | "failed"
    LastResponse string
    NextRetryAt *time.Time
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type WebhookStore struct { db *DB }
func NewWebhookStore(db *DB) *WebhookStore { return &WebhookStore{db: db} }

func (s *WebhookStore) ListForEvent(ctx context.Context, tenantID uuid.UUID, eventType string) ([]*WebhookConfig, error)
func (s *WebhookStore) CreateDelivery(ctx context.Context, webhookID uuid.UUID, eventType string, payload map[string]any) (*WebhookDelivery, error)
func (s *WebhookStore) UpdateDelivery(ctx context.Context, id uuid.UUID, status, lastResponse string, attempts int, nextRetryAt *time.Time) error
func (s *WebhookStore) ListPendingRetries(ctx context.Context) ([]*WebhookDelivery, error)
```

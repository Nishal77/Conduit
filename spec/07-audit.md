# Spec 07 — Audit Log

> Phase: P3 | Files: `internal/audit/writer.go`, `internal/audit/query.go`, `internal/audit/stream.go`

---

## 1. Design Principles

The audit log is **append-only**. The application layer enforces this:
- No UPDATE or DELETE is ever issued against `audit_events` by Conduit
- The table is partitioned by month for efficient pruning (DROP PARTITION)
- Writes are **non-blocking**: events go to a buffered Go channel, then batch-inserted

The proxy MUST NOT block waiting for the audit write. If the buffer is full, the event is dropped (with a metric increment) rather than slowing the request.

---

## 2. Audit Event — `internal/audit/writer.go`

```go
package audit

import (
    "context"
    "time"
)

// Event represents a single audit log entry.
// All fields must be populated before enqueuing.
type Event struct {
    TenantID     string          // UUID string
    AgentID      string          // from initialize params; empty if unknown
    SessionID    string          // SSE session identifier
    ServerName   string          // MCP server name
    ToolName     string          // tools/call tool name; empty for non-call methods
    RequestArgs  map[string]any  // tools/call arguments; nil if redacted or non-call
    ResponseMeta map[string]any  // {status: "success|error", content_blocks: N}
    StatusCode   int             // HTTP status returned to agent
    LatencyMs    int             // total proxy latency in milliseconds
    AuthMethod   string          // "api_key" | "jwt"
    PolicyAction string          // "allow" | "deny" | "rate_limited"
    CostUSD      float64         // 0 if not computed
    TraceID      string          // OTel trace ID; empty if tracing disabled
    CreatedAt    time.Time       // set by Writer, not caller
}
```

---

## 3. Writer — `internal/audit/writer.go`

```go
package audit

import (
    "context"
    "sync"
    "time"

    "github.com/rs/zerolog"
    "github.com/conduit-oss/conduit/internal/store"
    "github.com/conduit-oss/conduit/internal/config"
)

// Writer asynchronously writes audit events to PostgreSQL.
// It uses a buffered channel and a background goroutine for batch inserts.
type Writer struct {
    ch      chan Event          // buffered channel, capacity from config
    store   *store.AuditStore
    cfg     *config.AuditConfig
    log     zerolog.Logger
    wg      sync.WaitGroup
    once    sync.Once
    metrics *writerMetrics
}

// New creates a new Writer and starts the background flush goroutine.
// The background goroutine runs until Shutdown is called.
func New(store *store.AuditStore, cfg *config.AuditConfig, log zerolog.Logger) *Writer {
    w := &Writer{
        ch:    make(chan Event, cfg.BufferSize),  // default: 10,000
        store: store,
        cfg:   cfg,
        log:   log,
    }
    w.wg.Add(1)
    go w.flushLoop()
    return w
}

// Write enqueues an audit event for async writing.
// This MUST return immediately — it MUST NOT block the caller.
//
// If the channel is full:
//   - Drop the event (select with default)
//   - Increment conduit_audit_events_dropped_total metric
//   - Log warning (rate-limited to 1/s to avoid log spam)
func (w *Writer) Write(e Event)

// Shutdown drains the channel and waits for the background goroutine to exit.
// Called during graceful shutdown. Times out after 30 seconds.
func (w *Writer) Shutdown(ctx context.Context) error {
    // 1. Close the channel (signals flushLoop to drain and exit)
    w.once.Do(func() { close(w.ch) })
    // 2. Wait for flushLoop to finish (with ctx deadline)
    done := make(chan struct{})
    go func() { w.wg.Wait(); close(done) }()
    select {
    case <-done:
        return nil
    case <-ctx.Done():
        return fmt.Errorf("audit writer shutdown timed out")
    }
}

// flushLoop reads from the channel and batch-inserts to PostgreSQL.
//
// Batching strategy:
//   - Collect events until EITHER:
//     a. batch reaches 100 events, OR
//     b. flush_interval (default 1s) has elapsed
//   - Then call store.BatchInsert(ctx, batch)
//   - On INSERT error: log error, discard batch (do NOT retry — append-only)
//   - On channel close: flush remaining events, call wg.Done()
func (w *Writer) flushLoop() {
    defer w.wg.Done()

    ticker := time.NewTicker(w.cfg.FlushInterval)
    defer ticker.Stop()

    batch := make([]Event, 0, 100)

    for {
        select {
        case e, ok := <-w.ch:
            if !ok {
                // Channel closed — flush remaining and exit
                if len(batch) > 0 {
                    w.flush(context.Background(), batch)
                }
                return
            }
            batch = append(batch, e)
            if len(batch) >= 100 {
                w.flush(context.Background(), batch)
                batch = batch[:0]
            }

        case <-ticker.C:
            if len(batch) > 0 {
                w.flush(context.Background(), batch)
                batch = batch[:0]
            }
        }
    }
}

// flush inserts a batch of events into PostgreSQL using a single statement.
func (w *Writer) flush(ctx context.Context, events []Event)
```

---

## 4. Audit Store — `internal/store/audit.go`

```go
package store

import (
    "context"
    "time"

    "github.com/google/uuid"
)

// AuditEvent matches the audit_events table row.
type AuditEvent struct {
    ID           uuid.UUID
    TenantID     uuid.UUID
    AgentID      string
    SessionID    string
    ServerName   string
    ToolName     string
    RequestArgs  map[string]any
    ResponseMeta map[string]any
    StatusCode   int
    LatencyMs    int
    AuthMethod   string
    PolicyAction string
    CostUSD      float64
    TraceID      string
    CreatedAt    time.Time
}

// AuditStore provides audit log data access.
type AuditStore struct {
    db *DB
}

func NewAuditStore(db *DB) *AuditStore { return &AuditStore{db: db} }

// BatchInsert inserts multiple audit events in a single PostgreSQL statement.
// Uses pgx batch or COPY protocol for performance.
// This is the ONLY write method — no Update, no Delete.
//
// Use pgx batch:
//   batch := &pgx.Batch{}
//   for _, e := range events {
//       batch.Queue("INSERT INTO audit_events (...) VALUES ($1, ...)", ...)
//   }
//   results := pool.SendBatch(ctx, batch)
//   defer results.Close()
//   for range events { results.Exec() }
func (s *AuditStore) BatchInsert(ctx context.Context, events []AuditEvent) error

// Query returns paginated audit events for a tenant.
func (s *AuditStore) Query(ctx context.Context, q AuditQuery) ([]*AuditEvent, int64, error)

// Stream returns a channel that receives audit events in real-time.
// Used for `conduit audit tail` CLI command and SSE dashboard endpoint.
// The channel is closed when ctx is cancelled.
func (s *AuditStore) Stream(ctx context.Context, tenantID uuid.UUID, filter AuditStreamFilter) (<-chan *AuditEvent, error)

// AuditQuery defines the parameters for a paginated audit log query.
type AuditQuery struct {
    TenantID   uuid.UUID
    FromTime   *time.Time
    ToTime     *time.Time
    ToolName   *string    // partial match, e.g. "github/*"
    ServerName *string
    PolicyAction *string
    Limit      int        // default: 50, max: 500
    Offset     int
    OrderDesc  bool       // true = newest first (default)
}

// AuditStreamFilter defines parameters for a live audit stream.
type AuditStreamFilter struct {
    ToolName   string
    ServerName string
}
```

---

## 5. Query Function — `internal/audit/query.go`

```go
package audit

import (
    "context"
    "time"

    "github.com/conduit-oss/conduit/internal/store"
)

// QueryResult is the response for a paginated audit query.
type QueryResult struct {
    Events []*store.AuditEvent
    Total  int64  // total matching records (for pagination)
    Limit  int
    Offset int
}

// Query runs a filtered, paginated audit log query.
func Query(ctx context.Context, s *store.AuditStore, q store.AuditQuery) (*QueryResult, error)

// ExportCSV writes all matching events to a writer in CSV format.
// Used by `conduit audit export --format csv`.
//
// CSV columns (in order):
//   created_at, tenant_id, agent_id, session_id, server_name, tool_name,
//   status_code, latency_ms, auth_method, policy_action, cost_usd, trace_id
//
// Does NOT export request_args or response_meta (may contain sensitive data).
func ExportCSV(ctx context.Context, s *store.AuditStore, q store.AuditQuery, w io.Writer) error
```

---

## 6. Live Stream — `internal/audit/stream.go`

The live audit stream implements a PostgreSQL LISTEN/NOTIFY pattern:

```go
package audit

import (
    "context"
    "encoding/json"
    "net/http"

    "github.com/jackc/pgx/v5"
    "github.com/conduit-oss/conduit/internal/store"
)

// StreamHandler returns an HTTP handler for SSE-based live audit tailing.
// Used by:
//   - `conduit audit tail` CLI (connects to this endpoint)
//   - Dashboard /audit page (real-time stream)
//
// SSE endpoint: GET /api/v1/tenants/{tenant_id}/audit/stream
// Authentication: managed via auth middleware (same as proxy)
//
// Algorithm:
//   1. Acquire a dedicated pgx connection (NOT from pool — LISTEN is connection-scoped)
//   2. Execute: LISTEN conduit_audit_{tenant_id}
//   3. Set SSE headers on response writer
//   4. Loop: wait for NOTIFY, parse JSON payload, write SSE "data: {...}\n\n"
//   5. Flush after every event
//   6. On ctx cancel (client disconnect): send UNLISTEN, return connection to pool
//
// PostgreSQL trigger (created in migration 000002):
//   CREATE OR REPLACE FUNCTION notify_audit_event() RETURNS trigger AS $$
//   BEGIN
//     PERFORM pg_notify('conduit_audit_' || NEW.tenant_id::text, row_to_json(NEW)::text);
//     RETURN NEW;
//   END;
//   $$ LANGUAGE plpgsql;
//
//   CREATE TRIGGER audit_events_notify
//     AFTER INSERT ON audit_events
//     FOR EACH ROW EXECUTE FUNCTION notify_audit_event();
func StreamHandler(auditStore *store.AuditStore) http.HandlerFunc
```

**Alternative (simpler) approach for Phase 3**: Poll PostgreSQL every 500ms for events with `created_at > last_seen_id`. Use the LISTEN/NOTIFY approach only in Phase 4+ when the dashboard needs it.

---

## 7. Integration with Proxy Middleware

In `internal/proxy/middleware.go`, add audit write AFTER the upstream response:

```go
// Inside the proxy handler, after the upstream response is complete:
func (p *Proxy) afterResponse(ctx context.Context, req *http.Request, statusCode, latencyMs int, toolName string) {
    if !p.cfg.Audit.Enabled {
        return
    }

    event := audit.Event{
        TenantID:     proxy.TenantIDFromContext(ctx),
        AgentID:      proxy.AgentIDFromContext(ctx),
        SessionID:    proxy.SessionIDFromContext(ctx),
        ServerName:   proxy.ServerNameFromContext(ctx),
        ToolName:     toolName,
        StatusCode:   statusCode,
        LatencyMs:    latencyMs,
        AuthMethod:   proxy.AuthMethodFromContext(ctx),
        PolicyAction: proxy.PolicyActionFromContext(ctx),
        TraceID:      trace.SpanFromContext(ctx).SpanContext().TraceID().String(),
        CreatedAt:    time.Now(),
    }

    // Non-blocking — returns immediately
    p.auditor.Write(event)
}
```

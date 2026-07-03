// Package audit implements Conduit's append-only audit log writer (ADR-002,
// ADR-007 in CLAUDE.md). Every tool call flowing through the proxy produces
// one Event; Writer buffers events in memory and batch-flushes them to a
// Sink so the proxy's hot path never blocks on a database write.
//
// The Sink this batches into is intentionally an interface, not a concrete
// PostgreSQL dependency: Phase 1 has no database layer yet (that's
// internal/store, built in Phase 2), so main.go wires Writer to a LogSink
// until Phase 3 adds a PostgreSQL-backed Sink (internal/store.AuditStore).
// Writer's buffering, batching, and shutdown behavior are complete and real
// in Phase 1 — only the final destination of a flushed batch changes later.
package audit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/conduit-oss/conduit/internal/config"
	"github.com/rs/zerolog"
)

// Event represents a single audit log entry. All fields should be populated
// by the caller before enqueuing; CreatedAt is stamped by Write.
type Event struct {
	TenantID     string         // UUID string
	AgentID      string         // from initialize params; empty if unknown
	SessionID    string         // SSE session identifier
	ServerName   string         // MCP server name
	ToolName     string         // tools/call tool name; empty for non-call methods
	RequestArgs  map[string]any // tools/call arguments; nil if redacted or non-call
	ResponseMeta map[string]any // {status: "success|error", content_blocks: N}
	StatusCode   int            // HTTP status returned to agent
	LatencyMs    int            // total proxy latency in milliseconds
	AuthMethod   string         // "api_key" | "jwt"
	PolicyAction string         // "allow" | "deny" | "rate_limited"
	CostUSD      float64        // 0 if not computed
	TraceID      string         // OTel trace ID; empty if tracing disabled
	CreatedAt    time.Time      // set by Writer, not caller
}

// Sink persists a batch of audit events. BatchInsert must not mutate events
// and should return promptly — flushLoop calls it synchronously from the
// single background goroutine, so a slow Sink delays subsequent flushes but
// never blocks proxy request handling (Write only touches the channel).
type Sink interface {
	BatchInsert(ctx context.Context, events []Event) error
}

// batchSize is the number of events collected before a flush is forced,
// even if flush_interval hasn't elapsed yet.
const batchSize = 100

// dropLogInterval rate-limits the "buffer full, dropping event" warning so
// a sustained overload logs at most once per second instead of flooding
// stdout at request volume.
const dropLogInterval = time.Second

// Writer asynchronously writes audit events to a Sink. It uses a buffered
// channel and a single background goroutine for batching, per ADR-002: the
// proxy's Write call is a non-blocking channel send, never a database call.
type Writer struct {
	ch   chan Event
	sink Sink
	cfg  *config.AuditConfig
	log  zerolog.Logger
	wg   sync.WaitGroup
	once sync.Once

	droppedMu   sync.Mutex
	dropped     int64
	lastDropLog time.Time
}

// New creates a Writer and starts its background flush goroutine. The
// goroutine runs until Shutdown is called.
func New(sink Sink, cfg *config.AuditConfig, log zerolog.Logger) *Writer {
	w := &Writer{
		ch:   make(chan Event, cfg.BufferSize),
		sink: sink,
		cfg:  cfg,
		log:  log,
	}
	w.wg.Add(1)
	go w.flushLoop()
	return w
}

// Write enqueues an audit event for asynchronous writing. It returns
// immediately and never blocks the caller: if the buffer is full, the event
// is dropped and a rate-limited warning is logged, rather than slowing down
// the request that generated it.
func (w *Writer) Write(e Event) {
	e.CreatedAt = time.Now().UTC()
	select {
	case w.ch <- e:
	default:
		w.recordDrop()
	}
}

// Dropped returns the total number of events dropped so far because the
// buffer was full. Exposed for the /metrics endpoint (Phase 3).
func (w *Writer) Dropped() int64 {
	w.droppedMu.Lock()
	defer w.droppedMu.Unlock()
	return w.dropped
}

func (w *Writer) recordDrop() {
	w.droppedMu.Lock()
	defer w.droppedMu.Unlock()
	w.dropped++
	if time.Since(w.lastDropLog) >= dropLogInterval {
		w.log.Warn().Int64("total_dropped", w.dropped).Msg("audit buffer full, dropping event")
		w.lastDropLog = time.Now()
	}
}

// Shutdown closes the input channel, waits for flushLoop to drain and exit,
// and returns an error if that takes longer than ctx allows.
func (w *Writer) Shutdown(ctx context.Context) error {
	w.once.Do(func() { close(w.ch) })

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("audit writer shutdown timed out: %w", ctx.Err())
	}
}

// flushLoop reads events off the channel and batch-inserts them into the
// sink, flushing whenever the batch reaches batchSize or flush_interval
// elapses — whichever comes first. On sink error the batch is logged and
// discarded rather than retried, since audit_events is append-only and a
// retry could reorder or duplicate events across batches.
func (w *Writer) flushLoop() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]Event, 0, batchSize)

	for {
		select {
		case e, ok := <-w.ch:
			if !ok {
				if len(batch) > 0 {
					w.flush(context.Background(), batch)
				}
				return
			}
			batch = append(batch, e)
			if len(batch) >= batchSize {
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

func (w *Writer) flush(ctx context.Context, batch []Event) {
	// Copy before handing off: the caller reuses batch's backing array
	// immediately after this call returns.
	toInsert := make([]Event, len(batch))
	copy(toInsert, batch)

	if err := w.sink.BatchInsert(ctx, toInsert); err != nil {
		w.log.Error().Err(err).Int("batch_size", len(toInsert)).Msg("audit batch insert failed, batch discarded")
	}
}

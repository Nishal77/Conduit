package audit

import (
	"context"

	"github.com/rs/zerolog"
)

// LogSink is a Sink that writes each flushed batch to a structured log line
// instead of a database. Conduit uses it until Phase 3 introduces
// internal/store.AuditStore (a PostgreSQL-backed Sink); wiring a different
// Sink into audit.New is the only change needed to switch destinations —
// Writer's buffering and batching logic is unaffected.
type LogSink struct {
	log zerolog.Logger
}

// NewLogSink returns a Sink that logs each batch at info level.
func NewLogSink(log zerolog.Logger) *LogSink {
	return &LogSink{log: log}
}

// BatchInsert logs the batch size and, at debug level, each tool call in it.
func (s *LogSink) BatchInsert(_ context.Context, events []Event) error {
	s.log.Info().Int("count", len(events)).Msg("audit batch flushed")
	for _, e := range events {
		s.log.Debug().
			Str("tenant_id", e.TenantID).
			Str("server", e.ServerName).
			Str("tool", e.ToolName).
			Int("status", e.StatusCode).
			Int("latency_ms", e.LatencyMs).
			Str("policy_action", e.PolicyAction).
			Msg("audit event")
	}
	return nil
}

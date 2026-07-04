package audit

import (
	"context"

	"github.com/conduit-oss/conduit/internal/store"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// PostgresSink is the production Sink: it converts each flushed batch of
// audit.Event into store.AuditEvent and inserts them via store.AuditStore's
// single batched round trip. main.go wires this in once PostgreSQL is
// reachable; LogSink (sink.go) remains the fallback for the no-database
// compatibility mode Phase 1 introduced.
type PostgresSink struct {
	store *store.AuditStore
}

// NewPostgresSink returns a Sink backed by auditStore.
func NewPostgresSink(auditStore *store.AuditStore) *PostgresSink {
	return &PostgresSink{store: auditStore}
}

// BatchInsert converts and persists a batch of events. TenantID is expected
// to be a UUID string (set by the real auth middleware — see
// proxy.WithTenantID); an event whose TenantID doesn't parse as a UUID
// (e.g. captured while running in Phase 1/2's no-auth compatibility mode)
// is logged and dropped rather than failing the whole batch, since one bad
// row must never sink the other 99 in it.
func (s *PostgresSink) BatchInsert(ctx context.Context, events []Event) error {
	rows := make([]store.AuditEvent, 0, len(events))
	for _, e := range events {
		tenantID, err := uuid.Parse(e.TenantID)
		if err != nil {
			log.Warn().Str("tenant_id", e.TenantID).Msg("dropping audit event with non-UUID tenant_id (auth not configured?)")
			continue
		}
		rows = append(rows, store.AuditEvent{
			TenantID:     tenantID,
			AgentID:      e.AgentID,
			SessionID:    e.SessionID,
			ServerName:   e.ServerName,
			ToolName:     e.ToolName,
			RequestArgs:  e.RequestArgs,
			ResponseMeta: e.ResponseMeta,
			StatusCode:   e.StatusCode,
			LatencyMs:    e.LatencyMs,
			AuthMethod:   e.AuthMethod,
			PolicyAction: e.PolicyAction,
			CostUSD:      e.CostUSD,
			TraceID:      e.TraceID,
			CreatedAt:    e.CreatedAt,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return s.store.BatchInsert(ctx, rows)
}

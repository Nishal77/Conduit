package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AuditEvent matches a row of the audit_events table. It is the
// PostgreSQL-side counterpart of audit.Event — internal/audit converts
// between the two (see internal/audit's postgres sink) so this package
// never has to import internal/audit and create a dependency cycle.
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

// AuditStore provides audit log data access. It is append-only by design:
// BatchInsert is the only write method — no Update, no Delete — enforcing
// ADR-007's append-only guarantee at the application layer, on top of the
// partitioned table itself.
type AuditStore struct {
	db *DB
}

// NewAuditStore returns an AuditStore backed by db.
func NewAuditStore(db *DB) *AuditStore { return &AuditStore{db: db} }

// BatchInsert inserts every event in a single round trip using pgx's batch
// protocol (one network round trip covers all N inserts, rather than N
// round trips), which is what makes internal/audit.Writer's 100-event /
// 1-second batching worthwhile.
func (s *AuditStore) BatchInsert(ctx context.Context, events []AuditEvent) error {
	if len(events) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, e := range events {
		batch.Queue(`
			INSERT INTO audit_events (
				tenant_id, agent_id, session_id, server_name, tool_name,
				request_args, response_meta, status_code, latency_ms,
				auth_method, policy_action, cost_usd, trace_id, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		`, e.TenantID, nullableString(e.AgentID), nullableString(e.SessionID), e.ServerName, e.ToolName,
			e.RequestArgs, e.ResponseMeta, e.StatusCode, e.LatencyMs,
			e.AuthMethod, e.PolicyAction, nullableCost(e.CostUSD), nullableString(e.TraceID), e.CreatedAt)
	}

	results := s.db.Pool.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()

	for range events {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("batch insert audit event: %w", err)
		}
	}
	return nil
}

// nullableString converts "" to a SQL NULL — agent_id, session_id, and
// trace_id are all optional per spec/07-audit.md, and an empty string is
// how audit.Event represents "unknown" for them.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nullableCost converts 0 to a SQL NULL. cost_usd is only meaningful once
// Phase 8's cost estimator runs; until then every event would otherwise
// store a literal 0.00000000 instead of "not computed".
func nullableCost(c float64) *float64 {
	if c == 0 {
		return nil
	}
	return &c
}

// AuditQuery defines the parameters for a paginated audit log query.
type AuditQuery struct {
	TenantID     uuid.UUID
	FromTime     *time.Time
	ToTime       *time.Time
	ToolName     *string // exact match or "prefix*" per spec/08-cli.md §9
	ServerName   *string
	PolicyAction *string
	Limit        int // default: 50, max: 500
	Offset       int
	OrderDesc    bool // true = newest first (default)
}

// normalize applies AuditQuery's documented defaults and caps.
func (q AuditQuery) normalize() AuditQuery {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 500 {
		q.Limit = 500
	}
	return q
}

// Query returns paginated audit events for a tenant matching q's filters,
// plus the total count of matching rows (ignoring Limit/Offset) for
// pagination.
func (s *AuditStore) Query(ctx context.Context, q AuditQuery) ([]*AuditEvent, int64, error) {
	q = q.normalize()
	where, args := q.buildWhere()

	order := "DESC"
	if !q.OrderDesc {
		order = "ASC"
	}

	var total int64
	countSQL := "SELECT COUNT(*) FROM audit_events WHERE " + where
	if err := s.db.Pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit events: %w", err)
	}

	selectSQL := fmt.Sprintf(`
		SELECT id, tenant_id, COALESCE(agent_id, ''), COALESCE(session_id, ''),
			server_name, tool_name, request_args, response_meta, status_code,
			latency_ms, auth_method, policy_action, COALESCE(cost_usd, 0),
			COALESCE(trace_id, ''), created_at
		FROM audit_events
		WHERE %s
		ORDER BY created_at %s
		LIMIT %d OFFSET %d
	`, where, order, q.Limit, q.Offset)

	rows, err := s.db.Pool.Query(ctx, selectSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query audit events: %w", err)
	}
	defer rows.Close()

	var events []*AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.TenantID, &e.AgentID, &e.SessionID, &e.ServerName, &e.ToolName,
			&e.RequestArgs, &e.ResponseMeta, &e.StatusCode, &e.LatencyMs, &e.AuthMethod, &e.PolicyAction,
			&e.CostUSD, &e.TraceID, &e.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan audit event: %w", err)
		}
		events = append(events, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("rows error: %w", err)
	}
	return events, total, nil
}

// buildWhere translates q's filters into a SQL WHERE clause (without the
// "WHERE" keyword) and its positional args, shared by Query's count and
// select statements so they can never drift out of sync with each other.
func (q AuditQuery) buildWhere() (string, []any) {
	clause := "tenant_id = $1"
	args := []any{q.TenantID}

	addFilter := func(sqlFragment string, value any) {
		args = append(args, value)
		clause += fmt.Sprintf(" AND %s $%d", sqlFragment, len(args))
	}

	if q.FromTime != nil {
		addFilter("created_at >=", *q.FromTime)
	}
	if q.ToTime != nil {
		addFilter("created_at <=", *q.ToTime)
	}
	if q.ServerName != nil {
		addFilter("server_name =", *q.ServerName)
	}
	if q.PolicyAction != nil {
		addFilter("policy_action =", *q.PolicyAction)
	}
	if q.ToolName != nil {
		if isPrefixPattern(*q.ToolName) {
			addFilter("tool_name LIKE", toolNamePrefix(*q.ToolName)+"%")
		} else {
			addFilter("tool_name =", *q.ToolName)
		}
	}

	return clause, args
}

// isPrefixPattern reports whether a --tool filter value like "github/*"
// should be matched as a prefix rather than an exact tool name.
func isPrefixPattern(pattern string) bool {
	return len(pattern) > 0 && pattern[len(pattern)-1] == '*'
}

func toolNamePrefix(pattern string) string {
	return pattern[:len(pattern)-1]
}

// AuditStreamFilter narrows a live audit stream to a subset of events.
type AuditStreamFilter struct {
	ToolName   string // exact match or "prefix*"
	ServerName string
}

// streamPollInterval is how often Stream polls for new rows. spec/07-audit.md
// §6 explicitly allows this simpler alternative to PostgreSQL LISTEN/NOTIFY
// for Phase 3, reserving LISTEN/NOTIFY for Phase 4's dashboard.
const streamPollInterval = 500 * time.Millisecond

// Stream returns a channel that receives audit events for tenantID as they
// are inserted, newest-first polling replaced with oldest-first delivery so
// a consumer (conduit audit tail) sees events in the order they happened.
// The channel is closed when ctx is cancelled or done.
func (s *AuditStore) Stream(ctx context.Context, tenantID uuid.UUID, filter AuditStreamFilter) (<-chan *AuditEvent, error) {
	out := make(chan *AuditEvent)

	go func() {
		defer close(out)

		lastSeen := time.Now()
		ticker := time.NewTicker(streamPollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fromTime := lastSeen
				q := AuditQuery{
					TenantID:  tenantID,
					FromTime:  &fromTime,
					Limit:     500,
					OrderDesc: false,
				}
				if filter.ServerName != "" {
					q.ServerName = &filter.ServerName
				}
				if filter.ToolName != "" {
					q.ToolName = &filter.ToolName
				}

				events, _, err := s.Query(ctx, q)
				if err != nil {
					// A transient query error shouldn't kill a long-running
					// tail session — skip this tick and try again.
					continue
				}

				for _, e := range events {
					// FromTime is inclusive, so a row exactly at lastSeen
					// would be re-delivered every tick; skip it.
					if !e.CreatedAt.After(lastSeen) {
						continue
					}
					select {
					case out <- e:
						lastSeen = e.CreatedAt
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return out, nil
}

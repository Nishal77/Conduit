package audit

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"

	"github.com/conduit-oss/conduit/internal/store"
)

// QueryResult is the response for a paginated audit query — the shape
// `conduit audit query --output json` prints directly.
type QueryResult struct {
	Events []*store.AuditEvent `json:"events"`
	Total  int64               `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

// Query runs a filtered, paginated audit log query against s.
func Query(ctx context.Context, s *store.AuditStore, q store.AuditQuery) (*QueryResult, error) {
	events, total, err := s.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query audit events: %w", err)
	}
	return &QueryResult{Events: events, Total: total, Limit: q.Limit, Offset: q.Offset}, nil
}

// csvColumns are the columns ExportCSV writes, in order. request_args and
// response_meta are deliberately excluded — spec/07-audit.md §5 calls this
// out explicitly, since either may contain sensitive tool-call arguments
// that shouldn't end up in a CSV file on someone's laptop.
var csvColumns = []string{
	"created_at", "tenant_id", "agent_id", "session_id", "server_name", "tool_name",
	"status_code", "latency_ms", "auth_method", "policy_action", "cost_usd", "trace_id",
}

// ExportCSV streams every event matching q to w in CSV format, fetching in
// pages of q.Limit (or 500 if unset) rather than loading the full result
// set into memory — an export can reasonably cover months of traffic.
func ExportCSV(ctx context.Context, s *store.AuditStore, q store.AuditQuery, w io.Writer) error {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 500
	}
	q.OrderDesc = false // export in chronological order

	writer := csv.NewWriter(w)
	if err := writer.Write(csvColumns); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}

	offset := 0
	for {
		q.Offset = offset
		events, _, err := s.Query(ctx, q)
		if err != nil {
			return fmt.Errorf("query audit events: %w", err)
		}
		if len(events) == 0 {
			break
		}

		for _, e := range events {
			row := []string{
				e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
				e.TenantID.String(),
				e.AgentID,
				e.SessionID,
				e.ServerName,
				e.ToolName,
				strconv.Itoa(e.StatusCode),
				strconv.Itoa(e.LatencyMs),
				e.AuthMethod,
				e.PolicyAction,
				strconv.FormatFloat(e.CostUSD, 'f', -1, 64),
				e.TraceID,
			}
			if err := writer.Write(row); err != nil {
				return fmt.Errorf("write csv row: %w", err)
			}
		}

		if len(events) < q.Limit {
			break
		}
		offset += len(events)
	}

	writer.Flush()
	return writer.Error()
}

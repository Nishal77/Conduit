package builtin

import (
	"context"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/rs/zerolog"
)

// StructuredLogger adds a structured log line before and after every tool
// call it sees, independent of (and in addition to) the proxy's own
// per-request access log — useful for a tenant that wants plugin-level
// visibility without parsing the main request log.
type StructuredLogger struct {
	log zerolog.Logger
}

// NewStructuredLogger returns a StructuredLogger writing through log.
func NewStructuredLogger(log zerolog.Logger) *StructuredLogger {
	return &StructuredLogger{log: log}
}

func (l *StructuredLogger) Name() string    { return "logger" }
func (l *StructuredLogger) Version() string { return "1.0.0" }

func (l *StructuredLogger) Before(ctx context.Context, req *mcp.Message) (*mcp.Message, error) {
	rc := plugin.RequestContextFromContext(ctx)
	l.log.Debug().
		Str("event", "plugin.before").
		Str("tool_name", mcp.ExtractToolName(req)).
		Str("tenant_id", rc.TenantID).
		Msg("tool call starting")
	return req, nil
}

func (l *StructuredLogger) After(ctx context.Context, req, resp *mcp.Message) (*mcp.Message, error) {
	rc := plugin.RequestContextFromContext(ctx)
	statusCode := 200
	if resp.Error != nil {
		statusCode = 500
	}
	l.log.Debug().
		Str("event", "plugin.after").
		Str("tool_name", mcp.ExtractToolName(req)).
		Int("status_code", statusCode).
		Int64("latency_ms", rc.LatencyMs).
		Msg("tool call finished")
	return resp, nil
}

func (l *StructuredLogger) Shutdown(context.Context) error { return nil }

var _ plugin.ConduitPlugin = (*StructuredLogger)(nil)

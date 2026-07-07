package builtin_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin/builtin"
	"github.com/stretchr/testify/require"
)

func toolCallMessage(t *testing.T, args string) *mcp.Message {
	t.Helper()
	params, err := json.Marshal(mcp.ToolCallParams{Name: "send_email", Arguments: json.RawMessage(args)})
	require.NoError(t, err)
	return &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: params}
}

func TestPIIRedactor_RedactsEmail(t *testing.T) {
	r := builtin.NewPIIRedactor()
	req := toolCallMessage(t, `{"to":"alice@example.com","body":"hi"}`)

	out, err := r.Before(context.Background(), req)
	require.NoError(t, err)

	var params mcp.ToolCallParams
	require.NoError(t, json.Unmarshal(out.Params, &params))
	require.Contains(t, string(params.Arguments), "[EMAIL]")
	require.NotContains(t, string(params.Arguments), "alice@example.com")
}

func TestPIIRedactor_LeavesCleanArgsUnchanged(t *testing.T) {
	r := builtin.NewPIIRedactor()
	req := toolCallMessage(t, `{"title":"buy milk"}`)

	out, err := r.Before(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, req, out)
}

func TestPIIRedactor_IgnoresNonToolCallMessages(t *testing.T) {
	r := builtin.NewPIIRedactor()
	req := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/list"}

	out, err := r.Before(context.Background(), req)
	require.NoError(t, err)
	require.Same(t, req, out)
}

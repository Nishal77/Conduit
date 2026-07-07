package builtin_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/conduit-oss/conduit/internal/plugin/builtin"
	"github.com/stretchr/testify/require"
)

func TestCostTracker_AccumulatesRequestAndResponseCost(t *testing.T) {
	c := builtin.NewCostTracker()
	acc := &plugin.CostAccumulator{}
	ctx := plugin.WithCostAccumulator(context.Background(), acc)

	req := toolCallMessage(t, `{"title":"buy milk"}`)
	_, err := c.Before(ctx, req)
	require.NoError(t, err)
	require.Greater(t, acc.Total(), 0.0)

	requestOnly := acc.Total()

	resp := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: json.RawMessage(`{"content":[{"type":"text","text":"done"}]}`)}
	_, err = c.After(ctx, req, resp)
	require.NoError(t, err)
	require.Greater(t, acc.Total(), requestOnly)
}

func TestCostTracker_NoAccumulatorInContextIsANoOp(t *testing.T) {
	c := builtin.NewCostTracker()
	req := toolCallMessage(t, `{}`)

	out, err := c.Before(context.Background(), req)
	require.NoError(t, err)
	require.Same(t, req, out)
}

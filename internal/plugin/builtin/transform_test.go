package builtin_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin/builtin"
	"github.com/stretchr/testify/require"
)

func TestTransformPlugin_SetOnRequestArgument(t *testing.T) {
	p := builtin.NewTransformPlugin([]builtin.Transform{
		{Hook: "before", Target: "$.params.arguments.env", Action: "set", Value: "production"},
	})

	req := toolCallMessage(t, `{"env":"dev"}`)
	out, err := p.Before(context.Background(), req)
	require.NoError(t, err)

	var params mcp.ToolCallParams
	require.NoError(t, json.Unmarshal(out.Params, &params))
	var args map[string]any
	require.NoError(t, json.Unmarshal(params.Arguments, &args))
	require.Equal(t, "production", args["env"])
}

func TestTransformPlugin_PrefixOnResponseContentText(t *testing.T) {
	p := builtin.NewTransformPlugin([]builtin.Transform{
		{Hook: "after", Target: "$.result.content[0].text", Action: "prefix", Value: "[Conduit] "},
	})

	resp := &mcp.Message{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Result:  json.RawMessage(`{"content":[{"type":"text","text":"Issue created"}]}`),
	}
	out, err := p.After(context.Background(), nil, resp)
	require.NoError(t, err)

	var result mcp.ToolCallResult
	require.NoError(t, json.Unmarshal(out.Result, &result))
	require.Equal(t, "[Conduit] Issue created", result.Content[0].Text)
}

func TestTransformPlugin_DeleteField(t *testing.T) {
	p := builtin.NewTransformPlugin([]builtin.Transform{
		{Hook: "before", Target: "$.params.arguments.debug", Action: "delete"},
	})

	req := toolCallMessage(t, `{"debug":true,"title":"x"}`)
	out, err := p.Before(context.Background(), req)
	require.NoError(t, err)

	var params mcp.ToolCallParams
	require.NoError(t, json.Unmarshal(out.Params, &params))
	var args map[string]any
	require.NoError(t, json.Unmarshal(params.Arguments, &args))
	require.NotContains(t, args, "debug")
	require.Equal(t, "x", args["title"])
}

func TestTransformPlugin_UnresolvablePathIsANoOp(t *testing.T) {
	p := builtin.NewTransformPlugin([]builtin.Transform{
		{Hook: "before", Target: "$.params.arguments.nonexistent.deeper", Action: "set", Value: "x"},
	})

	req := toolCallMessage(t, `{"title":"x"}`)
	out, err := p.Before(context.Background(), req)
	require.NoError(t, err)
	require.JSONEq(t, string(req.Params), string(out.Params))
}

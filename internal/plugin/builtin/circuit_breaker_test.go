package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/conduit-oss/conduit/internal/plugin/builtin"
	"github.com/stretchr/testify/require"
)

func ctxForServer(serverName string) context.Context {
	return plugin.WithRequestContext(context.Background(), plugin.RequestContext{TenantID: "acme", ServerName: serverName})
}

func TestCircuitBreaker_OpensAfterThresholdFailures(t *testing.T) {
	cb := builtin.NewCircuitBreaker(builtin.CircuitBreakerConfig{FailureThreshold: 2, Cooldown: time.Hour})
	ctx := ctxForServer("flaky")
	req := toolCallMessage(t, `{}`)
	failResp := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Error: &mcp.RPCError{Code: -1, Message: "boom"}}

	_, err := cb.Before(ctx, req)
	require.NoError(t, err)
	_, err = cb.After(ctx, req, failResp)
	require.NoError(t, err)

	_, err = cb.Before(ctx, req)
	require.NoError(t, err)
	_, err = cb.After(ctx, req, failResp)
	require.NoError(t, err)

	_, err = cb.Before(ctx, req)
	require.True(t, errors.Is(err, plugin.ErrRequestBlocked))
}

func TestCircuitBreaker_ClosesAfterHalfOpenSuccesses(t *testing.T) {
	cb := builtin.NewCircuitBreaker(builtin.CircuitBreakerConfig{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: time.Millisecond})
	ctx := ctxForServer("recovering")
	req := toolCallMessage(t, `{}`)
	failResp := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Error: &mcp.RPCError{Code: -1, Message: "boom"}}
	okResp := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: json.RawMessage(`{"content":[]}`)}

	_, _ = cb.Before(ctx, req)
	_, _ = cb.After(ctx, req, failResp)

	_, err := cb.Before(ctx, req)
	require.True(t, errors.Is(err, plugin.ErrRequestBlocked))

	time.Sleep(2 * time.Millisecond)

	_, err = cb.Before(ctx, req) // cooldown elapsed -> half-open, allowed through
	require.NoError(t, err)
	_, err = cb.After(ctx, req, okResp)
	require.NoError(t, err)

	_, err = cb.Before(ctx, req) // circuit closed again
	require.NoError(t, err)
}

func TestCircuitBreaker_ScopedPerTenantAndServer(t *testing.T) {
	cb := builtin.NewCircuitBreaker(builtin.CircuitBreakerConfig{FailureThreshold: 1, Cooldown: time.Hour})
	req := toolCallMessage(t, `{}`)
	failResp := &mcp.Message{JSONRPC: "2.0", ID: json.RawMessage("1"), Error: &mcp.RPCError{Code: -1, Message: "boom"}}

	acmeCtx := ctxForServer("shared-server")
	_, _ = cb.Before(acmeCtx, req)
	_, _ = cb.After(acmeCtx, req, failResp)

	_, err := cb.Before(acmeCtx, req)
	require.True(t, errors.Is(err, plugin.ErrRequestBlocked))

	globexCtx := plugin.WithRequestContext(context.Background(), plugin.RequestContext{TenantID: "globex", ServerName: "shared-server"})
	_, err = cb.Before(globexCtx, req)
	require.NoError(t, err, "a different tenant's circuit for the same server name must be independent")
}

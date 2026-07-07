package builtin

import (
	"context"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
)

// defaultCostPerToken is spec/14-plugins.md §4's generic per-token estimate
// ($0.000002/token) used when no per-model pricing is configured —
// Enterprise per-model pricing is a Phase 8 concern (cost budgets).
const defaultCostPerToken = 0.000002

// CostTracker estimates the token cost of each tool call from the size of
// its request arguments and response content, and reports the result via
// the request's plugin.CostAccumulator (see that type's doc comment for
// why — ConduitPlugin's hooks have no other way to return a value like
// this to the caller).
//
// Token counting here is deliberately the crude len(json)/4 heuristic the
// spec calls for, not a real tokenizer — a real one would need to know
// which model's vocabulary to use, which Conduit (a protocol-level proxy)
// has no reliable way to determine from an MCP tool call alone.
type CostTracker struct {
	costPerToken float64
}

// NewCostTracker returns a CostTracker using the default per-token cost.
func NewCostTracker() *CostTracker {
	return &CostTracker{costPerToken: defaultCostPerToken}
}

func (c *CostTracker) Name() string    { return "cost-tracker" }
func (c *CostTracker) Version() string { return "1.0.0" }

// Before records the request-side cost estimate against the request's
// CostAccumulator. A no-op if the caller didn't attach one (e.g. a plugin
// exercised directly in a test, outside the proxy's middleware chain).
func (c *CostTracker) Before(ctx context.Context, req *mcp.Message) (*mcp.Message, error) {
	if req.Method != "tools/call" {
		return req, nil
	}
	if acc := plugin.CostAccumulatorFromContext(ctx); acc != nil {
		acc.AddRequestCost(estimateTokens(len(req.Params)) * c.costPerToken)
	}
	return req, nil
}

// After records the response-side cost estimate against the same
// CostAccumulator Before used — both hooks run within the same request's
// context, so they share the same accumulator instance.
func (c *CostTracker) After(ctx context.Context, _, resp *mcp.Message) (*mcp.Message, error) {
	if acc := plugin.CostAccumulatorFromContext(ctx); acc != nil {
		acc.AddResponseCost(estimateTokens(len(resp.Result)) * c.costPerToken)
	}
	return resp, nil
}

func (c *CostTracker) Shutdown(context.Context) error { return nil }

// estimateTokens applies the spec's rough len(json)/4 heuristic.
func estimateTokens(byteLen int) float64 {
	return float64(byteLen) / 4
}

var _ plugin.ConduitPlugin = (*CostTracker)(nil)

package plugin

import (
	"context"
	"sync"
)

// CostAccumulator collects the estimated USD cost of a single tool call
// across whichever plugins in its chain compute one (currently just
// builtin.CostTracker). It's threaded through the request context as a
// pointer because ConduitPlugin's Before/After hooks return only a
// (possibly modified) message — there's no dedicated return channel for a
// plugin to report a value like cost back to the caller. internal/proxy
// creates one per request (RequestContextMiddleware) and reads Total()
// back when writing the audit event.
type CostAccumulator struct {
	mu              sync.Mutex
	requestCostUSD  float64
	responseCostUSD float64
}

// AddRequestCost adds to the request-side portion of the estimate (called
// from a Before hook, before the response is known).
func (c *CostAccumulator) AddRequestCost(usd float64) {
	c.mu.Lock()
	c.requestCostUSD += usd
	c.mu.Unlock()
}

// AddResponseCost adds to the response-side portion of the estimate
// (called from an After hook).
func (c *CostAccumulator) AddResponseCost(usd float64) {
	c.mu.Lock()
	c.responseCostUSD += usd
	c.mu.Unlock()
}

// Total returns the accumulated cost so far.
func (c *CostAccumulator) Total() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestCostUSD + c.responseCostUSD
}

type costAccumulatorKey struct{}

// WithCostAccumulator returns a copy of ctx carrying acc.
func WithCostAccumulator(ctx context.Context, acc *CostAccumulator) context.Context {
	return context.WithValue(ctx, costAccumulatorKey{}, acc)
}

// CostAccumulatorFromContext returns the *CostAccumulator stored by
// WithCostAccumulator, or nil if none was set — callers must check for nil
// (e.g. a test that exercises a plugin directly, without going through the
// proxy's middleware chain).
func CostAccumulatorFromContext(ctx context.Context) *CostAccumulator {
	acc, _ := ctx.Value(costAccumulatorKey{}).(*CostAccumulator)
	return acc
}

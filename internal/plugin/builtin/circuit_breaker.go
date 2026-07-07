package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/conduit-oss/conduit/internal/plugin"
)

// breakerState is one of the three states in the standard circuit breaker
// state machine (spec/14-plugins.md §4).
type breakerState int

const (
	stateClosed breakerState = iota
	stateOpen
	stateHalfOpen
)

// CircuitBreakerConfig configures failure/success thresholds and cooldown.
// Zero values fall back to the spec's stated defaults.
type CircuitBreakerConfig struct {
	FailureThreshold int           // failures before opening (default: 5)
	SuccessThreshold int           // successes in half-open before closing (default: 2)
	Cooldown         time.Duration // time in open state before half-open (default: 60s)
}

func (c CircuitBreakerConfig) withDefaults() CircuitBreakerConfig {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.SuccessThreshold <= 0 {
		c.SuccessThreshold = 2
	}
	if c.Cooldown <= 0 {
		c.Cooldown = 60 * time.Second
	}
	return c
}

// CircuitBreaker prevents cascading failures to an upstream server: after
// enough consecutive failures it "opens" and blocks calls outright for a
// cooldown period, then allows a trickle of calls through ("half-open") to
// test recovery before fully "closing" again.
//
// State is tracked per (tenant_id, server_name) pair, since a single
// CircuitBreaker instance is shared across every tenant that enables it —
// one tenant's failing server must not trip the breaker for another
// tenant's healthy one.
type CircuitBreaker struct {
	cfg CircuitBreakerConfig

	mu       sync.Mutex
	circuits map[string]*circuitState
}

type circuitState struct {
	state     breakerState
	failures  int
	successes int
	openedAt  time.Time
}

// NewCircuitBreaker returns a CircuitBreaker with cfg (defaults applied for
// any zero field).
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{cfg: cfg.withDefaults(), circuits: make(map[string]*circuitState)}
}

func (b *CircuitBreaker) Name() string    { return "circuit-breaker" }
func (b *CircuitBreaker) Version() string { return "1.0.0" }

func circuitKey(tenantID, serverName string) string { return tenantID + "/" + serverName }

// Before blocks the call with ErrRequestBlocked if the circuit for this
// (tenant, server) pair is open, or transitions it to half-open once the
// cooldown has elapsed.
func (b *CircuitBreaker) Before(ctx context.Context, req *mcp.Message) (*mcp.Message, error) {
	rc := plugin.RequestContextFromContext(ctx)
	if rc.ServerName == "" {
		return req, nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.circuitFor(rc.TenantID, rc.ServerName)

	if c.state == stateOpen {
		if time.Since(c.openedAt) < b.cfg.Cooldown {
			return nil, fmt.Errorf("%w: circuit open for server %q", plugin.ErrRequestBlocked, rc.ServerName)
		}
		c.state = stateHalfOpen
		c.successes = 0
	}
	return req, nil
}

// After inspects the upstream response for failure (an MCP-level error or a
// tool result with isError=true) and updates the circuit's failure/success
// counters accordingly.
func (b *CircuitBreaker) After(ctx context.Context, _, resp *mcp.Message) (*mcp.Message, error) {
	rc := plugin.RequestContextFromContext(ctx)
	if rc.ServerName == "" {
		return resp, nil
	}

	failed := resp.Error != nil
	if !failed {
		var result mcp.ToolCallResult
		if err := json.Unmarshal(resp.Result, &result); err == nil {
			failed = result.IsError
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.circuitFor(rc.TenantID, rc.ServerName)

	if failed {
		c.failures++
		c.successes = 0
		if c.state != stateOpen && c.failures >= b.cfg.FailureThreshold {
			c.state = stateOpen
			c.openedAt = time.Now()
		}
		return resp, nil
	}

	c.failures = 0
	if c.state == stateHalfOpen {
		c.successes++
		if c.successes >= b.cfg.SuccessThreshold {
			c.state = stateClosed
		}
	}
	return resp, nil
}

func (b *CircuitBreaker) Shutdown(context.Context) error { return nil }

// circuitFor returns the circuitState for key, creating a fresh (closed)
// one on first use. Caller must hold b.mu.
func (b *CircuitBreaker) circuitFor(tenantID, serverName string) *circuitState {
	key := circuitKey(tenantID, serverName)
	c, ok := b.circuits[key]
	if !ok {
		c = &circuitState{}
		b.circuits[key] = c
	}
	return c
}

var _ plugin.ConduitPlugin = (*CircuitBreaker)(nil)

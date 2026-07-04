// Package ratelimit enforces per-tenant token-bucket rate limits using an
// atomic Redis Lua script (ADR-003 in CLAUDE.md): a single round trip that
// reads, refills, and (if capacity allows) debits a bucket, so concurrent
// requests can never race each other into over-consuming it.
package ratelimit

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// tracer returns Conduit's shared tracer, identical to internal/proxy's
// (otel.Tracer is keyed by name, not by which package calls it).
func tracer() trace.Tracer { return otel.Tracer("conduit") }

//go:embed lua/token_bucket.lua
var tokenBucketLua string

// configCacheTTL is how long a rate limit config loaded from PostgreSQL is
// cached in-process before being reloaded — see spec/06-ratelimit.md §7.
// Hard-coded rather than configurable in Phase 2, matching the spec.
const configCacheTTL = 30 * time.Second

// defaultWindowSec is used whenever no rate_limit_configs row exists for a
// scope: cfg.DefaultPerTenant requests per this many seconds.
const defaultWindowSec = 60

// Result is returned by Limiter.Check.
type Result struct {
	Allowed   bool
	Remaining int
	ResetAt   time.Time // approximate time the bucket refills to capacity
	Limit     int       // configured requests per window
	Window    int       // configured window in seconds
	Scope     string    // which scope produced this result: "tool" | "server" | "agent" | "tenant"
}

// scopeQuery is one (scope, target) pair Check evaluates, in priority order
// — most specific first, so a single denial short-circuits the rest.
type scopeQuery struct {
	scope  string
	target string
}

// cachedConfig is one entry in Limiter's in-process rate-limit-config cache.
type cachedConfig struct {
	requests  int
	windowSec int
	loadedAt  time.Time
}

// Limiter checks rate limits using a Redis token bucket, with rate limit
// configuration cached in-process for configCacheTTL to keep PostgreSQL off
// the hot path (spec/06-ratelimit.md §7).
type Limiter struct {
	redis   *redis.Client
	rlStore *store.RateLimitStore
	cfg     *config.RateLimitConfig
	script  *redis.Script
	log     zerolog.Logger

	cacheMu sync.RWMutex
	cache   map[string]cachedConfig
}

// New returns a Limiter. rlStore may be nil — in that case every scope uses
// cfg's defaults and PostgreSQL is never consulted, which keeps the limiter
// usable in deployments that haven't wired up a database yet.
func New(redisClient *redis.Client, rlStore *store.RateLimitStore, cfg *config.RateLimitConfig, log zerolog.Logger) *Limiter {
	return &Limiter{
		redis:   redisClient,
		rlStore: rlStore,
		cfg:     cfg,
		script:  redis.NewScript(tokenBucketLua),
		log:     log,
		cache:   make(map[string]cachedConfig),
	}
}

// Check evaluates every applicable scope for a tool call — tool, server,
// agent (if agentID is known), then tenant — most specific first, per
// spec/06-ratelimit.md §4. Each scope has its own independent bucket, so a
// single call can be rejected by e.g. a tight per-tool limit even while the
// tenant-wide bucket still has plenty of capacity.
//
// The first scope that denies short-circuits the rest and is returned
// directly. If every scope allows, Check returns the tightest (smallest
// Remaining) of the results — the most informative one for the
// X-RateLimit-* response headers.
//
// On a Redis error: if cfg.FailOpen, logs a warning and returns an allowed
// Result; otherwise returns the error (the caller turns this into a 503 —
// spec/06-ratelimit.md doesn't want an unreachable Redis to silently
// disable rate limiting in a strict deployment).
func (l *Limiter) Check(ctx context.Context, tenantID, serverName, toolName, agentID string) (*Result, error) {
	ctx, span := tracer().Start(ctx, "conduit.ratelimit")
	defer span.End()

	queries := make([]scopeQuery, 0, 4)
	if toolName != "" {
		queries = append(queries, scopeQuery{"tool", toolName})
	}
	if serverName != "" {
		queries = append(queries, scopeQuery{"server", serverName})
	}
	if agentID != "" {
		queries = append(queries, scopeQuery{"agent", agentID})
	}
	queries = append(queries, scopeQuery{"tenant", "*"})

	var tightest *Result
	for _, q := range queries {
		requests, windowSec, err := l.configForScope(ctx, tenantID, q.scope, q.target)
		if err != nil {
			return nil, fmt.Errorf("load rate limit config for scope %s: %w", q.scope, err)
		}

		res, err := l.checkBucket(ctx, tenantID, q.scope, q.target, requests, windowSec)
		if err != nil {
			if l.cfg.FailOpen {
				l.log.Warn().Err(err).Str("scope", q.scope).Msg("rate limiter redis error, failing open")
				return &Result{Allowed: true}, nil
			}
			return nil, err
		}

		if !res.Allowed {
			return res, nil
		}
		if tightest == nil || res.Remaining < tightest.Remaining {
			tightest = res
		}
	}
	return tightest, nil
}

// checkBucket runs the token bucket Lua script for a single (scope, target)
// key and translates its {allowed, remaining} reply into a Result.
func (l *Limiter) checkBucket(ctx context.Context, tenantID, scope, target string, requests, windowSec int) (*Result, error) {
	ctx, span := tracer().Start(ctx, "conduit.ratelimit.lua")
	defer span.End()

	key := fmt.Sprintf("rl:%s:%s:%s", tenantID, scope, target)
	capacity := float64(requests) * l.cfg.BurstMultiplier
	refillRate := float64(requests) / float64(windowSec)
	nowUs := time.Now().UnixMicro()
	ttlSec := windowSec * 2

	vals, err := l.script.Run(ctx, l.redis, []string{key}, capacity, refillRate, 1, nowUs, ttlSec).Slice()
	if err != nil {
		return nil, fmt.Errorf("execute token bucket script: %w", err)
	}
	if len(vals) != 2 {
		return nil, fmt.Errorf("unexpected token bucket script reply: %v", vals)
	}

	allowed, ok := vals[0].(int64)
	if !ok {
		return nil, fmt.Errorf("unexpected token bucket 'allowed' type: %T", vals[0])
	}
	remaining, ok := vals[1].(int64)
	if !ok {
		return nil, fmt.Errorf("unexpected token bucket 'remaining' type: %T", vals[1])
	}

	return &Result{
		Allowed:   allowed == 1,
		Remaining: int(remaining),
		ResetAt:   time.Now().Add(time.Duration(windowSec) * time.Second),
		Limit:     requests,
		Window:    windowSec,
		Scope:     scope,
	}, nil
}

// configForScope loads the rate limit config for (tenantID, scope, target),
// preferring the 30s in-process cache over a PostgreSQL round trip.
func (l *Limiter) configForScope(ctx context.Context, tenantID, scope, target string) (requests, windowSec int, err error) {
	cacheKey := tenantID + ":" + scope + ":" + target

	l.cacheMu.RLock()
	if c, ok := l.cache[cacheKey]; ok && time.Since(c.loadedAt) < configCacheTTL {
		l.cacheMu.RUnlock()
		return c.requests, c.windowSec, nil
	}
	l.cacheMu.RUnlock()

	requests, windowSec = l.cfg.DefaultPerTenant, defaultWindowSec

	if l.rlStore != nil {
		if tenantUUID, parseErr := uuid.Parse(tenantID); parseErr == nil {
			cfg, err := l.rlStore.GetForScope(ctx, tenantUUID, scope, target)
			switch {
			case errors.Is(err, store.ErrNotFound):
				// use defaults set above
			case err != nil:
				return 0, 0, err
			default:
				requests, windowSec = cfg.Requests, cfg.WindowSec
			}
		}
		// tenantID isn't a valid UUID (e.g. auth middleware isn't
		// configured and TenantIDFromContext returned a URL slug) — skip
		// the database and fall back to defaults rather than issuing a
		// query that can never match.
	}

	l.cacheMu.Lock()
	l.cache[cacheKey] = cachedConfig{requests: requests, windowSec: windowSec, loadedAt: time.Now()}
	l.cacheMu.Unlock()

	return requests, windowSec, nil
}

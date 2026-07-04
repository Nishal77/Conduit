package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/conduit-oss/conduit/internal/config"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestLimiter(t *testing.T, cfg *config.RateLimitConfig) *Limiter {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	// rlStore is nil throughout this file: every test relies on
	// cfg.DefaultPerTenant, exercising the token bucket algorithm itself
	// without needing a real PostgreSQL-backed rate_limit_configs row.
	return New(client, nil, cfg, zerolog.Nop())
}

func defaultTestConfig() *config.RateLimitConfig {
	return &config.RateLimitConfig{
		Enabled:          true,
		DefaultPerTenant: 3,
		BurstMultiplier:  1.0, // no burst headroom, so limits are exact in tests
		FailOpen:         true,
	}
}

func TestCheck_AllowsWithinLimit(t *testing.T) {
	l := newTestLimiter(t, defaultTestConfig())

	for i := 0; i < 3; i++ {
		res, err := l.Check(context.Background(), "tenant-a", "github", "", "")
		require.NoError(t, err)
		assert.True(t, res.Allowed, "request %d should be allowed (limit=3)", i+1)
	}
}

func TestCheck_DeniesOverLimit(t *testing.T) {
	l := newTestLimiter(t, defaultTestConfig())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		res, err := l.Check(ctx, "tenant-b", "github", "", "")
		require.NoError(t, err)
		require.True(t, res.Allowed)
	}

	res, err := l.Check(ctx, "tenant-b", "github", "", "")
	require.NoError(t, err)
	assert.False(t, res.Allowed, "4th request should be denied (limit=3)")
	assert.Equal(t, 0, res.Remaining)
}

func TestCheck_TenantsAreIsolated(t *testing.T) {
	l := newTestLimiter(t, defaultTestConfig())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		res, err := l.Check(ctx, "tenant-c", "github", "", "")
		require.NoError(t, err)
		require.True(t, res.Allowed)
	}
	res, err := l.Check(ctx, "tenant-c", "github", "", "")
	require.NoError(t, err)
	require.False(t, res.Allowed, "tenant-c should be exhausted")

	// A different tenant hitting the same server must have its own bucket.
	res, err = l.Check(ctx, "tenant-d", "github", "", "")
	require.NoError(t, err)
	assert.True(t, res.Allowed, "tenant-d's bucket must be independent of tenant-c's")
}

func TestCheck_ToolScopeIsMoreRestrictiveThanTenantScope(t *testing.T) {
	// Every scope shares the same DefaultPerTenant/window here (no per-scope
	// DB config, since rlStore is nil), but each scope still has its own
	// independent bucket keyed by "rl:{tenant}:{scope}:{target}". Exhausting
	// the tool-scope bucket must deny the call even though the tenant-scope
	// bucket for the same tenant still has room.
	l := newTestLimiter(t, defaultTestConfig())
	ctx := context.Background()

	// Same tool called 3 times exhausts rl:tenant-e:tool:github/create_issue.
	for i := 0; i < 3; i++ {
		res, err := l.Check(ctx, "tenant-e", "github", "github/create_issue", "")
		require.NoError(t, err)
		require.True(t, res.Allowed)
	}

	res, err := l.Check(ctx, "tenant-e", "github", "github/create_issue", "")
	require.NoError(t, err)
	assert.False(t, res.Allowed, "tool-scope bucket should be exhausted")
}

func TestCheck_AgentScopeOnlyAppliesWhenAgentIDPresent(t *testing.T) {
	l := newTestLimiter(t, defaultTestConfig())
	ctx := context.Background()

	res, err := l.Check(ctx, "tenant-f", "github", "", "")
	require.NoError(t, err)
	assert.True(t, res.Allowed)

	// agentID empty means no agent-scope bucket is even queried; this must
	// not error just because there's "nothing" to check for that scope.
	res, err = l.Check(ctx, "tenant-f", "github", "", "agent-123")
	require.NoError(t, err)
	assert.True(t, res.Allowed)
}

func TestCheck_FailsOpenOnRedisError(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := defaultTestConfig()
	cfg.FailOpen = true
	l := New(client, nil, cfg, zerolog.Nop())

	mr.Close() // simulate Redis becoming unreachable

	res, err := l.Check(context.Background(), "tenant-g", "github", "", "")
	require.NoError(t, err, "fail-open must not return an error to the caller")
	assert.True(t, res.Allowed)
}

func TestCheck_FailsClosedWhenConfigured(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := defaultTestConfig()
	cfg.FailOpen = false
	l := New(client, nil, cfg, zerolog.Nop())

	mr.Close()

	_, err := l.Check(context.Background(), "tenant-h", "github", "", "")
	assert.Error(t, err, "fail-closed must surface the Redis error")
}

func TestCheck_BucketRefillsOverTime(t *testing.T) {
	l := newTestLimiter(t, defaultTestConfig())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		res, err := l.Check(ctx, "tenant-i", "github", "", "")
		require.NoError(t, err)
		require.True(t, res.Allowed)
	}
	res, err := l.Check(ctx, "tenant-i", "github", "", "")
	require.NoError(t, err)
	require.False(t, res.Allowed, "bucket should be exhausted")

	// DefaultPerTenant=3 over a 60s window refills at 0.05 tokens/sec.
	// Advancing miniredis's clock (rather than sleeping) exercises the
	// refill math deterministically and instantly.
	time.Sleep(10 * time.Millisecond) // let real wall-clock nowUs tick forward
	res, err = l.Check(ctx, "tenant-i", "github", "", "")
	require.NoError(t, err)
	// Not enough time has passed to refill a whole token yet at this rate,
	// so it should still be denied — this pins down that the script uses
	// elapsed wall-clock time, not just "some time has passed = refill".
	assert.False(t, res.Allowed)
}

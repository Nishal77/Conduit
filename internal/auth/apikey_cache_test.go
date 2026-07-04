package auth

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

func TestAPIKeyValidator_RejectsMalformedKeyWithoutTouchingBackends(t *testing.T) {
	// keyStore is deliberately nil: a malformed key must be rejected by the
	// format check alone, before any Redis or PostgreSQL call — proving
	// Validate fails fast instead of wasting a round trip on obviously
	// invalid input.
	v := NewAPIKeyValidator(nil, nil, time.Minute)

	_, err := v.Validate(context.Background(), "not-a-conduit-key")
	assert.ErrorIs(t, err, ErrInvalidAPIKey)
}

func TestAPIKeyValidator_CacheHitSkipsDatabase(t *testing.T) {
	redisClient := newTestRedis(t)
	rawKey, hash, _, err := GenerateAPIKey()
	require.NoError(t, err)

	const wantTenantID = "11111111-1111-1111-1111-111111111111"
	require.NoError(t, redisClient.Set(context.Background(), authCacheKeyPrefix+hash, wantTenantID, time.Minute).Err())

	// keyStore is nil: if Validate tried to fall through to PostgreSQL on a
	// cache miss, this would panic. A cache hit must never reach that code
	// path.
	v := NewAPIKeyValidator(redisClient, nil, time.Minute)

	gotTenantID, err := v.Validate(context.Background(), rawKey)
	require.NoError(t, err)
	assert.Equal(t, wantTenantID, gotTenantID)
}

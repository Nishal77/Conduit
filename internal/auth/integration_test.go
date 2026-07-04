//go:build integration

// Integration tests covering the parts of APIKeyValidator.Validate that a
// pure unit test can't reach: a Redis cache miss falling through to a real
// PostgreSQL lookup. See internal/store/integration_test.go for how
// TEST_DATABASE_URL / TEST_REDIS_URL (or testcontainers-go) are resolved.
package auth_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/conduit-oss/conduit/internal/auth"
	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("conduit"),
			tcpostgres.WithUsername("conduit"),
			tcpostgres.WithPassword("conduit"),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Terminate(ctx) })
		dbURL, err = c.ConnectionString(ctx, "sslmode=disable")
		require.NoError(t, err)
	}

	require.NoError(t, store.Migrate(dbURL))
	db, err := store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxOpenConns: 5, MaxIdleConns: 1, ConnMaxLifetime: 5 * time.Minute})
	require.NoError(t, err)
	t.Cleanup(db.Close)
	return db
}

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()

	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		c, err := tcredis.Run(ctx, "redis:7-alpine")
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Terminate(ctx) })
		redisURL, err = c.ConnectionString(ctx)
		require.NoError(t, err)
	}

	opts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(ctx).Err())
	return client
}

func TestAPIKeyValidator_DatabaseFallback(t *testing.T) {
	db := testDB(t)
	redisClient := testRedis(t)
	ctx := context.Background()

	tenants := store.NewTenantStore(db)
	apiKeys := store.NewAPIKeyStore(db)

	tenant, err := tenants.Create(ctx, "auth-fallback-integration", "Fallback Test", "free")
	require.NoError(t, err)

	rawKey, hash, prefix, err := auth.GenerateAPIKey()
	require.NoError(t, err)
	_, err = apiKeys.Create(ctx, tenant.ID, "integration-key", hash, prefix, []string{"mcp:call"}, nil)
	require.NoError(t, err)

	validator := auth.NewAPIKeyValidator(redisClient, apiKeys, time.Minute)

	t.Run("cache miss falls through to database and caches the result", func(t *testing.T) {
		gotTenantID, err := validator.Validate(ctx, rawKey)
		require.NoError(t, err)
		require.Equal(t, tenant.ID.String(), gotTenantID)

		cached, err := redisClient.Get(ctx, "authcache:"+hash).Result()
		require.NoError(t, err)
		require.Equal(t, tenant.ID.String(), cached)
	})

	t.Run("unknown key returns ErrInvalidAPIKey, not a crash or 500", func(t *testing.T) {
		unknownRaw, _, _, err := auth.GenerateAPIKey()
		require.NoError(t, err)
		_, err = validator.Validate(ctx, unknownRaw)
		require.ErrorIs(t, err, auth.ErrInvalidAPIKey)
	})

	t.Run("revoked key returns ErrInvalidAPIKey", func(t *testing.T) {
		revokedRaw, revokedHash, revokedPrefix, err := auth.GenerateAPIKey()
		require.NoError(t, err)
		created, err := apiKeys.Create(ctx, tenant.ID, "revoked-key", revokedHash, revokedPrefix, []string{"mcp:call"}, nil)
		require.NoError(t, err)
		require.NoError(t, apiKeys.Revoke(ctx, created.ID))

		_, err = validator.Validate(ctx, revokedRaw)
		require.ErrorIs(t, err, auth.ErrInvalidAPIKey)
	})

	t.Run("expired key returns ErrExpiredAPIKey", func(t *testing.T) {
		expiredRaw, expiredHash, expiredPrefix, err := auth.GenerateAPIKey()
		require.NoError(t, err)
		past := time.Now().Add(-time.Hour)
		_, err = apiKeys.Create(ctx, tenant.ID, "expired-key", expiredHash, expiredPrefix, []string{"mcp:call"}, &past)
		require.NoError(t, err)

		_, err = validator.Validate(ctx, expiredRaw)
		require.ErrorIs(t, err, auth.ErrExpiredAPIKey)
	})
}

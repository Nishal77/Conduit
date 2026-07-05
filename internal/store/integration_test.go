//go:build integration

// Integration tests against real PostgreSQL and Redis (spec/19-testing.md).
// Run with `make test-int` — requires Docker (testcontainers-go spins up
// throwaway postgres:16-alpine / redis:7-alpine containers per run) or a
// TEST_DATABASE_URL / TEST_REDIS_URL already pointing at a real instance.
// Excluded from the default `go test ./...` / `make test` run via the
// integration build tag, since it needs infrastructure unit tests don't.
package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// testDB returns a ready-to-use *store.DB with migrations applied, backed
// either by TEST_DATABASE_URL (if set — e.g. a locally running Postgres) or
// a fresh testcontainers-go Postgres container.
func testDB(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		pgContainer, err := tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("conduit"),
			tcpostgres.WithUsername("conduit"),
			tcpostgres.WithPassword("conduit"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			),
		)
		require.NoError(t, err)
		t.Cleanup(func() { _ = pgContainer.Terminate(ctx) })

		dbURL, err = pgContainer.ConnectionString(ctx, "sslmode=disable")
		require.NoError(t, err)
	}

	require.NoError(t, store.Migrate(dbURL))

	db, err := store.New(ctx, &config.DatabaseConfig{
		URL:             dbURL,
		MaxOpenConns:    5,
		MaxIdleConns:    1,
		ConnMaxLifetime: 5 * time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(db.Close)
	return db
}

// testRedis returns a ready-to-use *redis.Client, backed either by
// TEST_REDIS_URL or a fresh testcontainers-go Redis container.
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	ctx := context.Background()

	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		redisContainer, err := tcredis.Run(ctx, "redis:7-alpine")
		require.NoError(t, err)
		t.Cleanup(func() { _ = redisContainer.Terminate(ctx) })

		redisURL, err = redisContainer.ConnectionString(ctx)
		require.NoError(t, err)
	}

	opts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.Ping(ctx).Err())
	return client
}

func TestTenantStore_CreateGetListDelete(t *testing.T) {
	db := testDB(t)
	tenants := store.NewTenantStore(db)
	ctx := context.Background()

	created, err := tenants.Create(ctx, "acme-integration", "Acme Integration", "pro")
	require.NoError(t, err)
	require.Equal(t, "acme-integration", created.Slug)

	_, err = tenants.Create(ctx, "acme-integration", "Duplicate", "free")
	require.ErrorIs(t, err, store.ErrConflict)

	bySlug, err := tenants.GetBySlug(ctx, "acme-integration")
	require.NoError(t, err)
	require.Equal(t, created.ID, bySlug.ID)

	byID, err := tenants.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.Slug, byID.Slug)

	all, err := tenants.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, all)

	newName := "Renamed"
	updated, err := tenants.Update(ctx, created.ID, store.TenantUpdates{Name: &newName})
	require.NoError(t, err)
	require.Equal(t, "Renamed", updated.Name)

	require.NoError(t, tenants.Delete(ctx, created.ID))
	_, err = tenants.GetByID(ctx, created.ID)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestAPIKeyStore_CreateGetByHashRevoke(t *testing.T) {
	db := testDB(t)
	tenants := store.NewTenantStore(db)
	apiKeys := store.NewAPIKeyStore(db)
	ctx := context.Background()

	tenant, err := tenants.Create(ctx, "apikey-integration", "API Key Test", "free")
	require.NoError(t, err)

	key, err := apiKeys.Create(ctx, tenant.ID, "test-key", "deadbeef", "cnd_deadbee", []string{"mcp:call"}, nil)
	require.NoError(t, err)

	found, err := apiKeys.GetByHash(ctx, "deadbeef")
	require.NoError(t, err)
	require.Equal(t, key.ID, found.ID)

	apiKeys.UpdateLastUsed(ctx, key.ID)
	require.Eventually(t, func() bool {
		k, err := apiKeys.GetByHash(ctx, "deadbeef")
		return err == nil && k.LastUsedAt != nil
	}, 2*time.Second, 50*time.Millisecond)

	require.NoError(t, apiKeys.Revoke(ctx, key.ID))
	_, err = apiKeys.GetByHash(ctx, "deadbeef")
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestMCPServerStore_CreateListAllEnabled(t *testing.T) {
	db := testDB(t)
	tenants := store.NewTenantStore(db)
	servers := store.NewMCPServerStore(db, nil)
	ctx := context.Background()

	tenant, err := tenants.Create(ctx, "server-integration", "Server Test", "free")
	require.NoError(t, err)

	_, err = servers.Create(ctx, store.CreateServerInput{
		TenantID:    tenant.ID,
		Name:        "github",
		UpstreamURL: "http://localhost:3001",
		AuthType:    "none",
		Weight:      100,
	})
	require.NoError(t, err)

	byName, err := servers.GetByTenantAndName(ctx, tenant.ID, "github")
	require.NoError(t, err)
	require.Equal(t, "http://localhost:3001", byName.UpstreamURL)

	all, err := servers.ListAllEnabled(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, all)

	found := false
	for _, s := range all {
		if s.TenantSlug == "server-integration" && s.Name == "github" {
			found = true
		}
	}
	require.True(t, found, "ListAllEnabled should include the server just created")
}

func TestRateLimitStore_UpsertGetForScope(t *testing.T) {
	db := testDB(t)
	tenants := store.NewTenantStore(db)
	rateLimits := store.NewRateLimitStore(db)
	ctx := context.Background()

	tenant, err := tenants.Create(ctx, "ratelimit-integration", "Rate Limit Test", "free")
	require.NoError(t, err)

	target := "github/create_issue"
	_, err = rateLimits.Upsert(ctx, store.UpsertRateLimitInput{
		TenantID:  tenant.ID,
		Scope:     "tool",
		Target:    &target,
		Requests:  5,
		WindowSec: 60,
	})
	require.NoError(t, err)

	cfg, err := rateLimits.GetForScope(ctx, tenant.ID, "tool", target)
	require.NoError(t, err)
	require.Equal(t, 5, cfg.Requests)

	_, err = rateLimits.GetForScope(ctx, tenant.ID, "tool", "nonexistent-tool")
	require.ErrorIs(t, err, store.ErrNotFound)
}

// TestRedisConnectivity is a smoke test proving testRedis (used by
// internal/auth and internal/ratelimit's own integration tests) actually
// works, independent of any PostgreSQL setup.
func TestRedisConnectivity(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()

	require.NoError(t, client.Set(ctx, "conduit-integration-test", "ok", time.Minute).Err())
	val, err := client.Get(ctx, "conduit-integration-test").Result()
	require.NoError(t, err)
	require.Equal(t, "ok", val)
}

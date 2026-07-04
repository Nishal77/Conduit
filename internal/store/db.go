// Package store is Conduit's PostgreSQL query layer. Every table gets one
// *Store type (TenantStore, APIKeyStore, ...) that owns a small set of
// hand-written queries — no ORM, per CLAUDE.md §15, since auditability of
// exactly what SQL runs against the database matters for a security-facing
// gateway.
//
// Every store method returns the sentinel errors in errors.go (ErrNotFound,
// ErrConflict) instead of leaking pgx.ErrNoRows or *pgconn.PgError, so
// callers can use errors.Is() without importing pgx themselves.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/conduit-oss/conduit/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a pgx connection pool. All *Store types take a *DB so the pool
// is created once at startup and shared across every table's store.
type DB struct {
	Pool *pgxpool.Pool
}

// New creates and validates a PostgreSQL connection pool, pinging the
// database before returning so a misconfigured DATABASE_URL fails at
// startup rather than on the first request.
func New(ctx context.Context, cfg *config.DatabaseConfig) (*DB, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}

	poolCfg.MaxConns = int32(cfg.MaxOpenConns)
	poolCfg.MinConns = int32(cfg.MaxIdleConns)
	poolCfg.MaxConnLifetime = cfg.ConnMaxLifetime
	poolCfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// Close shuts down the connection pool. Safe to call once during graceful
// shutdown; blocks until all in-flight queries finish or their context
// expires.
func (db *DB) Close() {
	db.Pool.Close()
}

// HealthCheck pings the database. Used by the proxy's /readyz endpoint
// (see proxy.ReadyChecker).
func (db *DB) HealthCheck(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}

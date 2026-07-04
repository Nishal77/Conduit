package store

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/conduit-oss/conduit/migrations"
	"github.com/golang-migrate/migrate/v4"
	// Blank-imported so its init() registers the "pgx5" driver with
	// golang-migrate before NewWithSourceInstance parses dbURL's scheme.
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Migrator wraps golang-migrate's *migrate.Migrate with the operations
// `conduit migrate` exposes (spec/08-cli.md §4): apply all pending
// migrations, roll back, step N migrations in either direction, or report
// the current version. Migrations never run automatically on proxy start
// (spec/04-database.md §1) — a Migrator is only ever constructed by the
// `conduit migrate` command.
type Migrator struct {
	m *migrate.Migrate
}

// NewMigrator opens a Migrator against dbURL using the migrations embedded
// in the binary at build time (migrations/embed.go). dbURL uses the
// standard "postgres://" scheme everywhere else in Conduit; golang-migrate's
// pgx/v5 driver registers itself under "pgx5" instead, so NewMigrator
// rewrites the scheme before handing the URL off.
func NewMigrator(dbURL string) (*Migrator, error) {
	migrateURL, err := toPgx5Scheme(dbURL)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}

	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("load embedded migrations: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, migrateURL)
	if err != nil {
		return nil, fmt.Errorf("init migrator: %w", err)
	}
	return &Migrator{m: m}, nil
}

// Close releases the Migrator's database connection. Safe to call once.
func (mg *Migrator) Close() error {
	sourceErr, dbErr := mg.m.Close()
	if sourceErr != nil {
		return fmt.Errorf("close migration source: %w", sourceErr)
	}
	if dbErr != nil {
		return fmt.Errorf("close migration database connection: %w", dbErr)
	}
	return nil
}

// Up applies every pending migration. Returns nil if the schema was already
// up to date.
func (mg *Migrator) Up() error {
	if err := mg.m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// Down rolls back every applied migration. Returns nil if there was nothing
// to roll back.
func (mg *Migrator) Down() error {
	if err := mg.m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("rollback migrations: %w", err)
	}
	return nil
}

// Steps applies (n > 0) or rolls back (n < 0) exactly |n| migrations.
func (mg *Migrator) Steps(n int) error {
	if err := mg.m.Steps(n); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("step migrations: %w", err)
	}
	return nil
}

// Version reports the current migration version and whether the most
// recent migration left the schema in a dirty (partially applied) state.
// Returns (0, false, nil) if no migration has ever been applied.
func (mg *Migrator) Version() (version uint, dirty bool, err error) {
	version, dirty, err = mg.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read migration version: %w", err)
	}
	return version, dirty, nil
}

// Migrate applies every pending migration to dbURL in one call — the
// common case (`conduit migrate` with no flags), and what internal/store's
// own integration tests use to prepare a database.
func Migrate(dbURL string) error {
	mg, err := NewMigrator(dbURL)
	if err != nil {
		return err
	}
	defer func() { _ = mg.Close() }()
	return mg.Up()
}

// toPgx5Scheme rewrites a postgres:// or postgresql:// URL to use the pgx5
// scheme golang-migrate's driver expects, leaving every other part (host,
// credentials, query params like sslmode) untouched.
func toPgx5Scheme(dbURL string) (string, error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", err
	}
	u.Scheme = "pgx5"
	return u.String(), nil
}

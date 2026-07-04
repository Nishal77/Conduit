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

// Migrate applies every pending migration in migrations/ (embedded into the
// binary at build time — see migrations/embed.go) to dbURL. It is invoked
// explicitly via `conduit migrate`, never automatically on proxy start
// (spec/04-database.md §1): schema changes on a production database should
// be a deliberate, observable operation, not a side effect of a pod
// restarting.
//
// dbURL uses the standard "postgres://" scheme everywhere else in Conduit
// (config, pgxpool); golang-migrate's pgx/v5 driver registers itself under
// "pgx5" instead, so Migrate rewrites the scheme before handing the URL off.
//
// Returns nil if the schema was already up to date.
func Migrate(dbURL string) error {
	migrateURL, err := toPgx5Scheme(dbURL)
	if err != nil {
		return fmt.Errorf("parse db url: %w", err)
	}

	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, migrateURL)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
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

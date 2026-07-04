package main

import (
	"fmt"

	"github.com/conduit-oss/conduit/internal/store"
	"github.com/spf13/cobra"
)

func newMigrateCmd() *cobra.Command {
	var (
		dbURL      string
		configPath string
		down       bool
		steps      int
		showVer    bool
	)
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply or roll back database migrations",
		Long:  "Applies every pending migration in migrations/ (embedded in the binary) to --db-url. Never runs automatically on proxy start — see spec/04-database.md.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolvedURL, err := resolveMigrateDBURL(cmd, dbURL, configPath)
			if err != nil {
				return err
			}

			mg, err := store.NewMigrator(resolvedURL)
			if err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			defer func() { _ = mg.Close() }()

			out := cmd.OutOrStdout()

			switch {
			case showVer:
				version, dirty, err := mg.Version()
				if err != nil {
					return fmt.Errorf("migrate: %w", err)
				}
				if dirty {
					fmt.Fprintf(out, "Current version: %d (dirty)\n", version)
				} else {
					fmt.Fprintf(out, "Current version: %d\n", version)
				}
				return nil

			case down:
				if err := mg.Down(); err != nil {
					return fmt.Errorf("migrate: %w", err)
				}
				fmt.Fprintln(out, "All migrations rolled back.")
				return nil

			case steps != 0:
				if err := mg.Steps(steps); err != nil {
					return fmt.Errorf("migrate: %w", err)
				}
				fmt.Fprintf(out, "Applied %d migration step(s).\n", steps)
				return nil

			default:
				if err := mg.Up(); err != nil {
					return fmt.Errorf("migrate: %w", err)
				}
				version, _, err := mg.Version()
				if err != nil {
					return fmt.Errorf("migrate: %w", err)
				}
				fmt.Fprintf(out, "All migrations applied. Current version: %d\n", version)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&dbURL, "db-url", "", "PostgreSQL URL (overrides config; required if no config file)")
	cmd.Flags().StringVar(&configPath, "config", envOr("CONDUIT_CONFIG", "conduit.yaml"), "path to conduit.yaml")
	cmd.Flags().BoolVar(&down, "down", false, "rollback the last migration (default: run all up)")
	cmd.Flags().IntVar(&steps, "steps", 0, "number of migration steps to apply (default: all)")
	cmd.Flags().BoolVar(&showVer, "version", false, "show current migration version and exit")
	cmd.MarkFlagsMutuallyExclusive("down", "steps", "version")
	return cmd
}

// resolveMigrateDBURL prefers --db-url, then falls back to loading
// conduit.yaml (which itself falls back to $DATABASE_URL) — this lets
// `conduit migrate` work either standalone (CI, first-time setup) or
// alongside the rest of Conduit's config.
func resolveMigrateDBURL(cmd *cobra.Command, dbURL, configPath string) (string, error) {
	if dbURL != "" {
		return dbURL, nil
	}
	if v := envOr("DATABASE_URL", ""); v != "" {
		return v, nil
	}
	cfg, err := loadConfigPartial(cmd, configPath)
	if err != nil {
		return "", fmt.Errorf("--db-url or DATABASE_URL is required (and %w)", err)
	}
	return cfg.Database.URL, nil
}

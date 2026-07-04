package main

import (
	"context"
	"fmt"

	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/spf13/cobra"
)

// loadConfig loads and fully validates conduit.yaml from configPath,
// applying the --log-level persistent flag (spec/08-cli.md §1) on top of
// whatever the config file or environment set. Used by `conduit proxy
// start`, which needs every field (auth.jwt_secret included) to be correct
// before it can safely serve traffic.
func loadConfig(cmd *cobra.Command, configPath string) (*config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	applyLogLevelFlag(cmd, cfg)
	return cfg, nil
}

// loadConfigPartial loads conduit.yaml without requiring the whole thing to
// validate — used by `conduit apikey`, `conduit audit`, and `conduit
// migrate`, none of which start the proxy and so shouldn't be blocked by
// e.g. auth.jwt_secret being unset in the environment they're run from.
func loadConfigPartial(cmd *cobra.Command, configPath string) (*config.Config, error) {
	cfg, err := config.LoadPartial(configPath)
	if err != nil {
		return nil, err
	}
	applyLogLevelFlag(cmd, cfg)
	return cfg, nil
}

func applyLogLevelFlag(cmd *cobra.Command, cfg *config.Config) {
	if logLevel, _ := cmd.Flags().GetString("log-level"); logLevel != "" && cmd.Flags().Changed("log-level") {
		cfg.Observability.LogLevel = logLevel
	}
}

// connectDB is the shared "I just need the database" path used by the
// apikey and audit command families — unlike `conduit proxy start`, these
// commands have no fallback mode: without PostgreSQL there is nothing for
// them to do.
func connectDB(ctx context.Context, cfg *config.Config) (*store.DB, error) {
	db, err := store.New(ctx, &cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return db, nil
}

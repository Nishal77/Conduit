// Command conduit is the Conduit binary: the MCP gateway proxy plus its
// CLI tooling. See spec/08-cli.md for the full command tree. The management
// API (Phase 4) will add a `conduit serve` companion process; for now every
// command that needs data talks to PostgreSQL/Redis directly.
package main

import (
	"fmt"
	"os"

	"github.com/conduit-oss/conduit/internal/config"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// version, commit, and built are overridden at build time via:
//
//	go build -ldflags "-X main.version=v0.1.0 -X main.commit=abc1234 -X main.built=2026-07-01T00:00:00Z"
var (
	version = "dev"
	commit  = "none"
	built   = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "conduit",
		Short: "Conduit is the production gateway for the Model Context Protocol (MCP).",
		Long:  "Conduit sits between AI agents and upstream MCP servers, enforcing auth, rate limits, policy, and audit logging on every tool call.",
	}

	// Global flags (spec/08-cli.md §1). --config is also accepted per
	// subcommand (some commands default it differently via $CONDUIT_CONFIG),
	// so only --log-level is truly global here.
	root.PersistentFlags().String("log-level", "", "log level: debug|info|warn|error (default: from conduit.yaml)")

	proxyCmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run and manage the MCP reverse proxy",
	}
	proxyCmd.AddCommand(newProxyStartCmd())

	root.AddCommand(
		proxyCmd,
		newMigrateCmd(),
		newAPIKeyCmd(),
		newAuditCmd(),
		newVersionCmd(),
	)

	return root
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newLogger builds the process-wide zerolog logger from observability
// config: JSON to stdout by default, or a human-readable console writer
// when log_format is "console" (handy for local development).
func newLogger(cfg *config.Config) zerolog.Logger {
	level, err := zerolog.ParseLevel(cfg.Observability.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	if cfg.Observability.LogFormat == "console" {
		return zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).With().Timestamp().Logger()
	}
	return zerolog.New(os.Stdout).With().Timestamp().Logger()
}

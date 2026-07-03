// Command conduit is the Conduit binary: the MCP gateway proxy plus (in
// later phases) its management API and CLI tooling. See spec/08-cli.md for
// the full command tree; Phase 1 implements only `conduit proxy start` and
// `conduit version` — enough to run the core reverse proxy end to end.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/conduit-oss/conduit/internal/audit"
	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/conduit-oss/conduit/internal/proxy"
	"github.com/conduit-oss/conduit/internal/tenant"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// version is overridden at build time via:
//
//	go build -ldflags "-X main.version=v0.1.0"
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// proxyStartFlags holds the flags for `conduit proxy start`.
type proxyStartFlags struct {
	configPath string
	// Phase 1 has no database-backed server registry (that's Phase 2), so
	// a single upstream route can be registered directly via flags. This is
	// a development convenience, not the production routing mechanism.
	demoTenant   string
	demoServer   string
	demoUpstream string
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "conduit",
		Short: "Conduit is the production gateway for the Model Context Protocol (MCP).",
		Long:  "Conduit sits between AI agents and upstream MCP servers, enforcing auth, rate limits, policy, and audit logging on every tool call.",
	}

	proxyCmd := &cobra.Command{
		Use:   "proxy",
		Short: "Run and manage the MCP reverse proxy",
	}
	proxyCmd.AddCommand(newProxyStartCmd())
	root.AddCommand(proxyCmd)
	root.AddCommand(newVersionCmd())

	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Conduit version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), version)
			return nil
		},
	}
}

func newProxyStartCmd() *cobra.Command {
	flags := &proxyStartFlags{}
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the MCP proxy listener",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProxy(context.Background(), flags)
		},
	}
	cmd.Flags().StringVar(&flags.configPath, "config", envOr("CONDUIT_CONFIG", "conduit.yaml"), "path to conduit.yaml")
	cmd.Flags().StringVar(&flags.demoTenant, "demo-tenant", "", "(dev only) tenant slug to register a single static route for")
	cmd.Flags().StringVar(&flags.demoServer, "demo-server", "", "(dev only) MCP server name for the static route")
	cmd.Flags().StringVar(&flags.demoUpstream, "demo-upstream", "", "(dev only) upstream base URL for the static route")
	return cmd
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// runProxy wires together every Phase 1 component and serves traffic until
// the process receives SIGINT/SIGTERM, then drains in-flight work before
// exiting.
func runProxy(ctx context.Context, flags *proxyStartFlags) error {
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := newLogger(cfg)

	router := tenant.NewRouter()
	if flags.demoTenant != "" && flags.demoServer != "" && flags.demoUpstream != "" {
		router.Register(&tenant.Server{
			TenantSlug:  flags.demoTenant,
			Name:        flags.demoServer,
			UpstreamURL: flags.demoUpstream,
			Enabled:     true,
		})
		logger.Info().
			Str("tenant", flags.demoTenant).
			Str("server", flags.demoServer).
			Str("upstream", flags.demoUpstream).
			Msg("registered development route")
	}

	plugins := plugin.NewRegistry()
	auditor := audit.New(audit.NewLogSink(logger), &cfg.Audit, logger)
	p := proxy.New(cfg, router, plugins, auditor, logger)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      p,
		ReadTimeout:  cfg.Server.Timeouts.Read,
		WriteTimeout: cfg.Server.Timeouts.Write,
		IdleTimeout:  cfg.Server.Timeouts.Idle,
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info().Int("port", cfg.Server.Port).Msg("conduit proxy listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info().Msg("shutdown signal received, draining")
	case err := <-serveErr:
		return fmt.Errorf("proxy server: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("proxy server shutdown error")
	}
	if err := auditor.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("audit writer shutdown error")
	}
	if err := plugins.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("plugin shutdown error")
	}
	return nil
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

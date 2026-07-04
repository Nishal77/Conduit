// Command conduit is the Conduit binary: the MCP gateway proxy plus its
// management API and CLI tooling. See spec/08-cli.md for the full command
// tree; Phase 2 adds `conduit migrate`, PostgreSQL-backed routing, API key
// auth, and Redis rate limiting on top of Phase 1's core proxy.
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
	"github.com/conduit-oss/conduit/internal/auth"
	"github.com/conduit-oss/conduit/internal/config"
	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/conduit-oss/conduit/internal/proxy"
	"github.com/conduit-oss/conduit/internal/ratelimit"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/conduit-oss/conduit/internal/tenant"
	"github.com/redis/go-redis/v9"
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
	// A single upstream route can still be registered directly via flags,
	// bypassing PostgreSQL entirely. Useful for local testing without a
	// database — see runProxy's "compatibility mode" fallback.
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
	root.AddCommand(newMigrateCmd())

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

func newMigrateCmd() *cobra.Command {
	var dbURL string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending database migrations",
		Long:  "Applies every pending migration in migrations/ (embedded in the binary) to --db-url. Never runs automatically on proxy start — see spec/04-database.md.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dbURL == "" {
				dbURL = envOr("DATABASE_URL", "")
			}
			if dbURL == "" {
				return fmt.Errorf("--db-url or DATABASE_URL is required")
			}
			if err := store.Migrate(dbURL); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "migrations applied")
			return nil
		},
	}
	cmd.Flags().StringVar(&dbURL, "db-url", "", "PostgreSQL connection URL (defaults to $DATABASE_URL)")
	return cmd
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

// runProxy wires together every component and serves traffic until the
// process receives SIGINT/SIGTERM, then drains in-flight work before
// exiting.
//
// If PostgreSQL is reachable, the proxy runs in full Phase 2 mode: routes
// load from mcp_servers (refreshed every 5s), requests need a valid API
// key, and Redis enforces per-tenant rate limits. If it isn't — no
// DATABASE_URL configured, or the database is down — Conduit logs a
// warning and falls back to Phase 1 compatibility mode: no auth, no rate
// limiting, and (optionally) a single static route from --demo-*. This
// keeps `conduit proxy start` usable for a quick local test without
// standing up infrastructure first.
func runProxy(ctx context.Context, flags *proxyStartFlags) error {
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := newLogger(cfg)
	router := tenant.NewRouter()
	plugins := plugin.NewRegistry()
	auditor := audit.New(audit.NewLogSink(logger), &cfg.Audit, logger)

	proxyOpts, cleanupDeps := wireDataLayer(ctx, cfg, router, logger)
	defer cleanupDeps()

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

	p := proxy.New(cfg, router, plugins, auditor, logger, proxyOpts...)

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

// wireDataLayer attempts to connect PostgreSQL and Redis and, if both
// succeed, returns the proxy.Options that enable real auth, rate limiting,
// and database-backed routing (plus their /readyz checkers). On any
// connection failure it logs a warning and returns an empty option set —
// see runProxy's doc comment for the resulting fallback behavior. The
// returned cleanup func closes whatever was successfully opened and must
// always be called (via defer), even on the fallback path where it's a
// no-op.
func wireDataLayer(ctx context.Context, cfg *config.Config, router *tenant.Router, logger zerolog.Logger) (opts []proxy.Option, cleanup func()) {
	cleanup = func() {}

	db, err := store.New(ctx, &cfg.Database)
	if err != nil {
		logger.Warn().Err(err).Msg("database unavailable, running without auth/rate-limiting/database routing (use --demo-* flags for a local test route)")
		return nil, cleanup
	}

	redisClient := redis.NewClient(mustParseRedisURL(cfg.Redis.URL, cfg.Redis.PoolSize))
	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Warn().Err(err).Msg("redis unavailable, running without auth/rate-limiting/database routing")
		db.Close()
		return nil, cleanup
	}

	// store.NewTenantStore is intentionally not instantiated here: nothing
	// in the proxy's request path needs it (routing keys off mcp_servers,
	// not tenants directly), and it has no other caller until Phase 4's
	// management API. Wiring it into main.go now would just be a pool
	// warmed for no one.
	apiKeys := store.NewAPIKeyStore(db)
	servers := store.NewMCPServerStore(db)
	rateLimits := store.NewRateLimitStore(db)

	routingStore := tenant.NewStore(router, servers, logger)
	if err := routingStore.Start(ctx); err != nil {
		logger.Warn().Err(err).Msg("initial routing table load failed, continuing with an empty table")
	}

	keyValidator := auth.NewAPIKeyValidator(redisClient, apiKeys, cfg.Auth.APIKeyCacheTTL)
	jwtValidator := auth.NewJWTValidator(cfg.Auth.JWTSecret, "https://conduit")
	limiter := ratelimit.New(redisClient, rateLimits, &cfg.RateLimit, logger)

	logger.Info().Msg("database and redis connected, auth and rate limiting enabled")

	cleanup = func() {
		routingStore.Stop()
		_ = redisClient.Close()
		db.Close()
	}

	opts = []proxy.Option{
		proxy.WithAuthMiddleware(auth.NewMiddleware(keyValidator, jwtValidator)),
		proxy.WithRateLimitMiddleware(ratelimit.NewMiddleware(limiter)),
		proxy.WithReadyChecker(dbReadyChecker{db}),
		proxy.WithReadyChecker(redisReadyChecker{redisClient}),
	}
	return opts, cleanup
}

// mustParseRedisURL parses cfg.Redis.URL into go-redis options. A malformed
// URL here means the config failed to validate earlier (config.Validate
// checks the redis:// scheme), so a panic at startup is appropriate — this
// is a configuration bug, not a runtime condition to recover from.
func mustParseRedisURL(redisURL string, poolSize int) *redis.Options {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		panic(fmt.Sprintf("invalid redis.url %q (should have been caught by config.Validate): %v", redisURL, err))
	}
	opts.PoolSize = poolSize
	return opts
}

// dbReadyChecker adapts store.DB to proxy.ReadyChecker.
type dbReadyChecker struct{ db *store.DB }

func (c dbReadyChecker) Name() string                    { return "postgres" }
func (c dbReadyChecker) Check(ctx context.Context) error { return c.db.HealthCheck(ctx) }

// redisReadyChecker adapts a *redis.Client to proxy.ReadyChecker.
type redisReadyChecker struct{ client *redis.Client }

func (c redisReadyChecker) Name() string { return "redis" }
func (c redisReadyChecker) Check(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
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

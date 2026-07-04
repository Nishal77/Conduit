package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
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
	"github.com/conduit-oss/conduit/internal/tracing"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

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

func newProxyStartCmd() *cobra.Command {
	flags := &proxyStartFlags{}
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the MCP proxy listener, metrics server, and (if configured) tracing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProxy(cmd, flags)
		},
	}
	cmd.Flags().StringVar(&flags.configPath, "config", envOr("CONDUIT_CONFIG", "conduit.yaml"), "path to conduit.yaml")
	cmd.Flags().StringVar(&flags.demoTenant, "demo-tenant", "", "(dev only) tenant slug to register a single static route for")
	cmd.Flags().StringVar(&flags.demoServer, "demo-server", "", "(dev only) MCP server name for the static route")
	cmd.Flags().StringVar(&flags.demoUpstream, "demo-upstream", "", "(dev only) upstream base URL for the static route")
	return cmd
}

// runProxy wires together every component and serves traffic until the
// process receives SIGINT/SIGTERM, then drains in-flight work before
// exiting.
//
// If PostgreSQL is reachable, the proxy runs in full mode: routes load from
// mcp_servers (refreshed every 5s), requests need a valid API key, Redis
// enforces per-tenant rate limits, and audit events persist to
// audit_events. If it isn't — no DATABASE_URL configured, or the database
// is down — Conduit logs a warning and falls back to compatibility mode: no
// auth, no rate limiting, audit events only logged (not persisted), and
// (optionally) a single static route from --demo-*. This keeps
// `conduit proxy start` usable for a quick local test without standing up
// infrastructure first.
//
// Exit codes: 0 clean shutdown, 1 startup error, 2 runtime error (spec/08-cli.md §3).
func runProxy(cmd *cobra.Command, flags *proxyStartFlags) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := loadConfig(cmd, flags.configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := newLogger(cfg)
	proxy.InitBuildInfo(version, commit, runtime.Version())

	shutdownTracing, err := tracing.Setup(ctx, &cfg.Observability, version)
	if err != nil {
		return fmt.Errorf("set up tracing: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			logger.Warn().Err(err).Msg("tracing shutdown error")
		}
	}()

	router := tenant.NewRouter()
	plugins := plugin.NewRegistry()

	proxyOpts, auditSink, cleanupDeps := wireDataLayer(ctx, cfg, router, logger)
	defer cleanupDeps()

	auditor := audit.New(auditSink, &cfg.Audit, logger)

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

	// Metrics run on their own port (spec/09-observability.md §1) so a
	// network policy can block it from agent traffic entirely, same as the
	// management API port.
	metricsSrv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.MetricsPort),
		Handler: promhttp.Handler(),
	}

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 2)
	go func() {
		logger.Info().Int("port", cfg.Server.Port).Msg("conduit proxy listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("proxy server: %w", err)
		}
	}()
	go func() {
		logger.Info().Int("port", cfg.Server.MetricsPort).Msg("conduit metrics listening")
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("metrics server: %w", err)
		}
	}()

	select {
	case <-runCtx.Done():
		logger.Info().Msg("shutdown signal received, draining")
	case err := <-serveErr:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("proxy server shutdown error")
	}
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("metrics server shutdown error")
	}
	if err := auditor.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("audit writer shutdown error")
	}
	if err := plugins.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("plugin shutdown error")
	}
	return nil
}

// wireDataLayer attempts to connect PostgreSQL and Redis. On success it
// returns the proxy.Options that enable real auth, rate limiting, and
// database-backed routing (plus their /readyz checkers) and a
// PostgreSQL-backed audit Sink; on any connection failure it logs a
// warning and returns an empty option set plus a LogSink fallback — see
// runProxy's doc comment for the resulting behavior. The returned cleanup
// func closes whatever was successfully opened and must always be called
// (via defer), even on the fallback path where it's a no-op.
func wireDataLayer(ctx context.Context, cfg *config.Config, router *tenant.Router, logger zerolog.Logger) (opts []proxy.Option, sink audit.Sink, cleanup func()) {
	cleanup = func() {}
	sink = audit.NewLogSink(logger)

	db, err := store.New(ctx, &cfg.Database)
	if err != nil {
		logger.Warn().Err(err).Msg("database unavailable, running without auth/rate-limiting/database routing/audit persistence (use --demo-* flags for a local test route)")
		return nil, sink, cleanup
	}

	redisClient := redis.NewClient(mustParseRedisURL(cfg.Redis.URL, cfg.Redis.PoolSize))
	if err := redisClient.Ping(ctx).Err(); err != nil {
		logger.Warn().Err(err).Msg("redis unavailable, running without auth/rate-limiting/database routing")
		db.Close()
		return nil, sink, cleanup
	}

	// store.NewTenantStore is intentionally not instantiated here: nothing
	// in the proxy's request path needs it (routing keys off mcp_servers,
	// not tenants directly), and it has no other caller until Phase 4's
	// management API. Wiring it into main.go now would just be a pool
	// warmed for no one.
	apiKeys := store.NewAPIKeyStore(db)
	servers := store.NewMCPServerStore(db)
	rateLimits := store.NewRateLimitStore(db)
	auditStore := store.NewAuditStore(db)

	routingStore := tenant.NewStore(router, servers, logger)
	if err := routingStore.Start(ctx); err != nil {
		logger.Warn().Err(err).Msg("initial routing table load failed, continuing with an empty table")
	}

	keyValidator := auth.NewAPIKeyValidator(redisClient, apiKeys, cfg.Auth.APIKeyCacheTTL)
	jwtValidator := auth.NewJWTValidator(cfg.Auth.JWTSecret, "https://conduit")
	limiter := ratelimit.New(redisClient, rateLimits, &cfg.RateLimit, logger)

	logger.Info().Msg("database and redis connected, auth, rate limiting, and audit persistence enabled")

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
	return opts, audit.NewPostgresSink(auditStore), cleanup
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

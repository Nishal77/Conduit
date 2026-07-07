package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/conduit-oss/conduit/internal/plugin"
	"github.com/conduit-oss/conduit/internal/plugin/builtin"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/rs/zerolog"
)

// pluginRefreshInterval mirrors tenant.Store's 5s TTL (spec/13-multitenant.md's
// routing-table cache pattern) — a tenant_plugins change becomes active
// within this window, without a proxy restart.
const pluginRefreshInterval = 5 * time.Second

// pluginLoader keeps a plugin.Registry in sync with the tenant_plugins
// table by polling PostgreSQL, the same design tenant.Store uses for the
// routing table. It lives here rather than in internal/plugin because
// building a concrete instance from a tenant_plugins row needs
// internal/plugin/builtin, which already imports internal/plugin — the
// reverse import would cycle. cmd/conduit, as the composition root, is
// free to import both.
type pluginLoader struct {
	registry    *plugin.Registry
	plugins     *store.PluginStore
	httpTimeout time.Duration
	log         zerolog.Logger

	mu        sync.Mutex
	instances map[string]cachedPlugin // key: tenantID + "/" + pluginName

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// cachedPlugin remembers the config a plugin instance was built with, so a
// refresh that finds no change can keep reusing it — rebuilding on every
// tick would silently reset any in-memory state a plugin like
// builtin.CircuitBreaker depends on to function at all.
type cachedPlugin struct {
	instance   plugin.ConduitPlugin
	configHash string
}

// newPluginLoader returns a pluginLoader that will keep registry populated
// from plugins.
func newPluginLoader(registry *plugin.Registry, plugins *store.PluginStore, httpTimeout time.Duration, log zerolog.Logger) *pluginLoader {
	return &pluginLoader{
		registry:    registry,
		plugins:     plugins,
		httpTimeout: httpTimeout,
		log:         log,
		instances:   make(map[string]cachedPlugin),
		stopCh:      make(chan struct{}),
	}
}

// Start performs an initial synchronous load and then refreshes every
// pluginRefreshInterval in the background until Stop is called.
func (l *pluginLoader) Start(ctx context.Context) error {
	if err := l.reload(ctx); err != nil {
		return err
	}
	l.wg.Add(1)
	go l.refreshLoop()
	return nil
}

// Stop signals the background refresh loop to exit and waits for it to do
// so. It does not shut down the plugin instances it built — those are
// registered into the same plugin.Registry the proxy uses, so
// Registry.Shutdown (called once, by runProxy, after every loader has
// stopped) is the single owner of that responsibility. Calling Shutdown
// from both places would shut down every instance twice.
func (l *pluginLoader) Stop() {
	close(l.stopCh)
	l.wg.Wait()
}

func (l *pluginLoader) refreshLoop() {
	defer l.wg.Done()

	ticker := time.NewTicker(pluginRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), pluginRefreshInterval)
			if err := l.reload(ctx); err != nil {
				l.log.Warn().Err(err).Msg("failed to refresh plugin registry, keeping previous set")
			}
			cancel()
		}
	}
}

// reload fetches every enabled tenant_plugins row, reuses any existing
// plugin instance whose config hasn't changed (preserving its in-memory
// state), builds instances for anything new or changed, shuts down
// instances no longer enabled, and atomically swaps the registry.
func (l *pluginLoader) reload(ctx context.Context) error {
	rows, err := l.plugins.ListAllEnabled(ctx)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	seen := make(map[string]bool, len(rows))
	regs := make([]plugin.Registration, 0, len(rows))

	for _, row := range rows {
		key := row.TenantID.String() + "/" + row.PluginName
		seen[key] = true

		configBytes, _ := json.Marshal(row.Config)
		hash := string(configBytes)

		cached, ok := l.instances[key]
		if !ok || cached.configHash != hash {
			if ok {
				_ = cached.instance.Shutdown(ctx) // config changed: cleanly retire the old instance
			}
			built, buildErr := buildPlugin(row, l.httpTimeout, l.log)
			if buildErr != nil {
				l.log.Warn().Err(buildErr).Str("plugin", row.PluginName).Str("tenant_id", row.TenantID.String()).Msg("skipping unbuildable plugin")
				delete(l.instances, key)
				continue
			}
			cached = cachedPlugin{instance: built, configHash: hash}
			l.instances[key] = cached
		}

		regs = append(regs, plugin.Registration{Plugin: cached.instance, Priority: row.Priority, TenantID: row.TenantID.String()})
	}

	for key, cached := range l.instances {
		if !seen[key] {
			_ = cached.instance.Shutdown(ctx)
			delete(l.instances, key)
		}
	}

	l.registry.ReplaceAll(regs)
	return nil
}

// buildPlugin constructs the concrete ConduitPlugin a tenant_plugins row
// describes. Built-in plugin names must match the seed data in
// migrations/000004_plugins_webhooks.up.sql exactly.
func buildPlugin(row *store.TenantPluginWithName, httpTimeout time.Duration, log zerolog.Logger) (plugin.ConduitPlugin, error) {
	switch row.PluginType {
	case "http_callback":
		return plugin.NewHTTPCallbackPlugin(plugin.HTTPCallbackConfig{
			Name:      row.PluginName,
			Version:   row.PluginVersion,
			BeforeURL: stringConfig(row.Config, "before_url"),
			AfterURL:  stringConfig(row.Config, "after_url"),
			Secret:    stringConfig(row.Config, "secret"),
			Timeout:   httpTimeout,
		}), nil

	case "builtin":
		switch row.PluginName {
		case "pii-redactor":
			return builtin.NewPIIRedactor(), nil
		case "cost-tracker":
			return builtin.NewCostTracker(), nil
		case "circuit-breaker":
			return builtin.NewCircuitBreaker(builtin.CircuitBreakerConfig{
				FailureThreshold: intConfig(row.Config, "failure_threshold"),
				SuccessThreshold: intConfig(row.Config, "success_threshold"),
				Cooldown:         time.Duration(intConfig(row.Config, "cooldown_sec")) * time.Second,
			}), nil
		case "logger":
			return builtin.NewStructuredLogger(log), nil
		case "transform":
			return builtin.NewTransformPlugin(parseTransforms(row.Config)), nil
		default:
			return nil, fmt.Errorf("unknown builtin plugin %q", row.PluginName)
		}

	default:
		return nil, fmt.Errorf("unknown plugin_type %q", row.PluginType)
	}
}

func stringConfig(config map[string]any, key string) string {
	s, _ := config[key].(string)
	return s
}

func intConfig(config map[string]any, key string) int {
	// encoding/json unmarshals every JSON number into float64 when the
	// destination is map[string]any.
	f, _ := config[key].(float64)
	return int(f)
}

// parseTransforms reads the "transforms" array documented in
// spec/14-plugins.md §4's transform plugin config example.
func parseTransforms(config map[string]any) []builtin.Transform {
	raw, ok := config["transforms"].([]any)
	if !ok {
		return nil
	}
	transforms := make([]builtin.Transform, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		transforms = append(transforms, builtin.Transform{
			Hook:   stringConfig(m, "hook"),
			Target: stringConfig(m, "target"),
			Action: stringConfig(m, "action"),
			Value:  stringConfig(m, "value"),
		})
	}
	return transforms
}

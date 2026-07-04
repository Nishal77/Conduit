// Package config loads and validates Conduit's runtime configuration.
//
// Configuration is layered, highest precedence first:
//  1. Explicit environment variables (JWT_SECRET, DATABASE_URL, REDIS_URL,
//     CONDUIT_PORT, ...) — see the envBindings table below.
//  2. Values from the YAML file passed to Load.
//  3. Built-in defaults (setDefaults), matching the shipped conduit.yaml.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/viper"
)

// Config is the root configuration object for the Conduit binary.
type Config struct {
	Server        ServerConfig        `mapstructure:"server"`
	Database      DatabaseConfig      `mapstructure:"database"`
	Redis         RedisConfig         `mapstructure:"redis"`
	Auth          AuthConfig          `mapstructure:"auth"`
	RateLimit     RateLimitConfig     `mapstructure:"rate_limiting"`
	Audit         AuditConfig         `mapstructure:"audit"`
	Observability ObservabilityConfig `mapstructure:"observability"`
	Plugins       PluginsConfig       `mapstructure:"plugins"`
	Policy        PolicyConfig        `mapstructure:"policy"`
	Webhooks      WebhooksConfig      `mapstructure:"webhooks"`
}

// ServerConfig controls the three HTTP listeners Conduit exposes.
type ServerConfig struct {
	Port           int           `mapstructure:"port"`
	ManagementPort int           `mapstructure:"management_port"`
	MetricsPort    int           `mapstructure:"metrics_port"`
	TLS            TLSConfig     `mapstructure:"tls"`
	Timeouts       TimeoutConfig `mapstructure:"timeouts"`
	// CORSOrigins lists origins allowed to call the management API
	// (spec/10-api.md §6) — typically the dashboard's own origin in
	// production. Empty means "allow any origin," which is fine for local
	// development but should always be set explicitly in production.
	CORSOrigins []string `mapstructure:"cors_origins"`
}

// TLSConfig configures optional TLS termination on the proxy listener.
type TLSConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
}

// TimeoutConfig sets net/http server and upstream-dial timeouts.
type TimeoutConfig struct {
	Read     time.Duration `mapstructure:"read"`
	Write    time.Duration `mapstructure:"write"`
	Idle     time.Duration `mapstructure:"idle"`
	Upstream time.Duration `mapstructure:"upstream"`
}

// DatabaseConfig configures the PostgreSQL connection pool.
type DatabaseConfig struct {
	URL             string        `mapstructure:"url"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
}

// RedisConfig configures the Redis client used for rate limiting and caching.
type RedisConfig struct {
	URL      string `mapstructure:"url"`
	PoolSize int    `mapstructure:"pool_size"`
}

// AuthConfig configures API key and JWT authentication.
type AuthConfig struct {
	JWTSecret       string        `mapstructure:"jwt_secret"`
	AccessTokenTTL  time.Duration `mapstructure:"access_token_ttl"`
	RefreshTokenTTL time.Duration `mapstructure:"refresh_token_ttl"`
	APIKeyPrefix    string        `mapstructure:"api_key_prefix"`
	APIKeyCacheTTL  time.Duration `mapstructure:"api_key_cache_ttl"`
}

// RateLimitConfig configures the default token-bucket rate limiting policy.
type RateLimitConfig struct {
	Enabled          bool    `mapstructure:"enabled"`
	DefaultPerTenant int     `mapstructure:"default_per_tenant"`
	BurstMultiplier  float64 `mapstructure:"burst_multiplier"`
	FailOpen         bool    `mapstructure:"fail_open"`
}

// AuditConfig configures the append-only audit log writer.
type AuditConfig struct {
	Enabled       bool          `mapstructure:"enabled"`
	BufferSize    int           `mapstructure:"buffer_size"`
	FlushInterval time.Duration `mapstructure:"flush_interval"`
	RedactArgs    bool          `mapstructure:"redact_args"`
	RetentionDays int           `mapstructure:"retention_days"`
}

// ObservabilityConfig configures logging, tracing, and metrics export.
type ObservabilityConfig struct {
	OTELEndpoint      string  `mapstructure:"otel_endpoint"`
	LogLevel          string  `mapstructure:"log_level"`
	LogFormat         string  `mapstructure:"log_format"`
	TraceSamplingRate float64 `mapstructure:"trace_sampling_rate"`
}

// PluginsConfig configures native (in-process) plugin loading.
type PluginsConfig struct {
	Dir         string        `mapstructure:"dir"`
	HTTPTimeout time.Duration `mapstructure:"http_timeout"`
}

// PolicyConfig configures the YAML policy engine and its hot-reload.
type PolicyConfig struct {
	File           string        `mapstructure:"file"`
	ReloadInterval time.Duration `mapstructure:"reload_interval"`
}

// WebhooksConfig configures outbound webhook delivery and retry behavior.
type WebhooksConfig struct {
	MaxRetries   int           `mapstructure:"max_retries"`
	RetryBackoff string        `mapstructure:"retry_backoff"`
	Timeout      time.Duration `mapstructure:"timeout"`
}

// envBindings maps config keys to the non-standard (unprefixed or
// short-form) environment variables documented in spec/03-config.md §4.
// Every other key is still reachable via the generic CONDUIT_<NESTED_KEY>
// convention through viper's AutomaticEnv.
var envBindings = map[string]string{
	"auth.jwt_secret":             "JWT_SECRET",
	"database.url":                "DATABASE_URL",
	"redis.url":                   "REDIS_URL",
	"server.port":                 "CONDUIT_PORT",
	"server.management_port":      "CONDUIT_MANAGEMENT_PORT",
	"server.metrics_port":         "CONDUIT_METRICS_PORT",
	"observability.log_level":     "CONDUIT_LOG_LEVEL",
	"observability.log_format":    "CONDUIT_LOG_FORMAT",
	"observability.otel_endpoint": "CONDUIT_OTEL_ENDPOINT",
}

// setDefaults populates v with every default value from the shipped
// conduit.yaml, so Load produces a complete Config even when the file
// omits a section entirely.
func setDefaults(v *viper.Viper) {
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.management_port", 8081)
	v.SetDefault("server.metrics_port", 9090)
	v.SetDefault("server.tls.enabled", false)
	v.SetDefault("server.timeouts.read", 30*time.Second)
	v.SetDefault("server.timeouts.write", 60*time.Second)
	v.SetDefault("server.timeouts.idle", 120*time.Second)
	v.SetDefault("server.timeouts.upstream", 30*time.Second)

	v.SetDefault("database.url", "postgres://conduit:conduit@localhost:5432/conduit?sslmode=disable")
	v.SetDefault("database.max_open_conns", 25)
	v.SetDefault("database.max_idle_conns", 5)
	v.SetDefault("database.conn_max_lifetime", 5*time.Minute)

	v.SetDefault("redis.url", "redis://localhost:6379/0")
	v.SetDefault("redis.pool_size", 10)

	v.SetDefault("auth.access_token_ttl", time.Hour)
	v.SetDefault("auth.refresh_token_ttl", 720*time.Hour)
	v.SetDefault("auth.api_key_prefix", "cnd_")
	v.SetDefault("auth.api_key_cache_ttl", 5*time.Minute)

	v.SetDefault("rate_limiting.enabled", true)
	v.SetDefault("rate_limiting.default_per_tenant", 1000)
	v.SetDefault("rate_limiting.burst_multiplier", 1.5)
	v.SetDefault("rate_limiting.fail_open", true)

	v.SetDefault("audit.enabled", true)
	v.SetDefault("audit.buffer_size", 10000)
	v.SetDefault("audit.flush_interval", time.Second)
	v.SetDefault("audit.redact_args", false)
	v.SetDefault("audit.retention_days", 90)

	v.SetDefault("observability.otel_endpoint", "")
	v.SetDefault("observability.log_level", "info")
	v.SetDefault("observability.log_format", "json")
	v.SetDefault("observability.trace_sampling_rate", 1.0)

	v.SetDefault("plugins.dir", "")
	v.SetDefault("plugins.http_timeout", 5*time.Second)

	v.SetDefault("policy.file", "")
	v.SetDefault("policy.reload_interval", 10*time.Second)

	v.SetDefault("webhooks.max_retries", 5)
	v.SetDefault("webhooks.retry_backoff", "1s,5s,30s,5m,30m")
	v.SetDefault("webhooks.timeout", 10*time.Second)
}

// newViper builds a viper instance with defaults and environment variable
// bindings applied, but does not yet read any config file.
func newViper() (*viper.Viper, error) {
	v := viper.New()
	setDefaults(v)

	// AutomaticEnv + SetEnvKeyReplacer covers the general CONDUIT_<NESTED_KEY>
	// convention (e.g. CONDUIT_SERVER_TIMEOUTS_READ) for Get()-style access.
	v.SetEnvPrefix("CONDUIT")
	v.AutomaticEnv()

	// viper's Unmarshal only picks up automatic env vars for keys it already
	// knows about, so we bind every documented override explicitly. This
	// also covers the handful of env vars that don't follow the CONDUIT_
	// prefix convention (JWT_SECRET, DATABASE_URL, REDIS_URL, CONDUIT_PORT).
	for key, env := range envBindings {
		if err := v.BindEnv(key, env); err != nil {
			return nil, fmt.Errorf("bind env %s: %w", env, err)
		}
	}
	return v, nil
}

// Load reads configuration from the YAML file at path (if path is "", only
// defaults and environment variables apply) and returns a validated Config.
// Use this for anything that starts the proxy itself; for CLI tooling that
// only needs a handful of fields (e.g. database.url) and shouldn't be
// blocked by an unrelated field like auth.jwt_secret being unset, use
// LoadPartial instead.
func Load(path string) (*Config, error) {
	cfg, err := LoadPartial(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

// LoadPartial reads configuration exactly like Load, but skips Validate.
//
// `conduit apikey` and `conduit audit` only ever touch Database (and
// Observability, for --log-level), so requiring auth.jwt_secret or
// distinct server ports to be configured just to run a one-shot admin
// command would be a needless operational hurdle — those commands may
// well run from an operator's laptop that has DATABASE_URL but not the
// proxy's JWT signing secret.
func LoadPartial(path string) (*Config, error) {
	v, err := newViper()
	if err != nil {
		return nil, err
	}

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config file %q: %w", path, err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return &cfg, nil
}

// DefaultConfig returns a Config with all built-in defaults applied and no
// file or environment overrides. It does not call Validate — callers such
// as unit tests are expected to override the fields they care about (most
// commonly Auth.JWTSecret) before relying on validated behavior.
func DefaultConfig() *Config {
	v, err := newViper()
	if err != nil {
		// newViper only fails if a BindEnv call is malformed, which would be
		// a programming error caught immediately by any test that calls this.
		panic(fmt.Sprintf("config: default viper setup failed: %v", err))
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		panic(fmt.Sprintf("config: unmarshal defaults failed: %v", err))
	}
	return &cfg
}

// Validate checks that c is complete and internally consistent. Load calls
// this automatically; callers that build a Config by hand (tests, embedders)
// should call it too before starting the proxy.
func (c *Config) Validate() error {
	if err := validatePort("server.port", c.Server.Port); err != nil {
		return err
	}
	if err := validatePort("server.management_port", c.Server.ManagementPort); err != nil {
		return err
	}
	if err := validatePort("server.metrics_port", c.Server.MetricsPort); err != nil {
		return err
	}
	if c.Server.Port == c.Server.ManagementPort ||
		c.Server.Port == c.Server.MetricsPort ||
		c.Server.ManagementPort == c.Server.MetricsPort {
		return errors.New("server.port, server.management_port, and server.metrics_port must all be distinct")
	}

	if len(c.Auth.JWTSecret) < 32 {
		return errors.New("auth.jwt_secret must be set and at least 32 characters (set via JWT_SECRET)")
	}

	if err := validateURLScheme("database.url", c.Database.URL, "postgres", "postgresql"); err != nil {
		return err
	}
	if err := validateURLScheme("redis.url", c.Redis.URL, "redis", "rediss"); err != nil {
		return err
	}

	if c.Audit.BufferSize < 100 {
		return fmt.Errorf("audit.buffer_size must be >= 100, got %d", c.Audit.BufferSize)
	}

	if c.RateLimit.BurstMultiplier < 1.0 {
		return fmt.Errorf("rate_limiting.burst_multiplier must be >= 1.0, got %f", c.RateLimit.BurstMultiplier)
	}

	return nil
}

func validatePort(field string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s must be in range [1, 65535], got %d", field, port)
	}
	return nil
}

func validateURLScheme(field, raw string, allowedSchemes ...string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", field, err)
	}
	for _, scheme := range allowedSchemes {
		if u.Scheme == scheme {
			return nil
		}
	}
	return fmt.Errorf("%s must use scheme %v, got %q", field, allowedSchemes, raw)
}

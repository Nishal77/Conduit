# Spec 03 — Configuration

> Phase: P1 | Files: `internal/config/config.go`, `conduit.yaml`

---

## 1. Config Struct — `internal/config/config.go`

```go
package config

import (
    "fmt"
    "time"

    "github.com/spf13/viper"
)

// Config is the root configuration object.
// Loaded from conduit.yaml + environment variable overrides.
type Config struct {
    Server      ServerConfig      `mapstructure:"server"`
    Database    DatabaseConfig    `mapstructure:"database"`
    Redis       RedisConfig       `mapstructure:"redis"`
    Auth        AuthConfig        `mapstructure:"auth"`
    RateLimit   RateLimitConfig   `mapstructure:"rate_limiting"`
    Audit       AuditConfig       `mapstructure:"audit"`
    Observability ObservabilityConfig `mapstructure:"observability"`
    Plugins     PluginsConfig     `mapstructure:"plugins"`
    Policy      PolicyConfig      `mapstructure:"policy"`
    Webhooks    WebhooksConfig    `mapstructure:"webhooks"`
}

type ServerConfig struct {
    Port           int           `mapstructure:"port"`            // default: 8080
    ManagementPort int           `mapstructure:"management_port"` // default: 8081
    MetricsPort    int           `mapstructure:"metrics_port"`    // default: 9090
    TLS            TLSConfig     `mapstructure:"tls"`
    Timeouts       TimeoutConfig `mapstructure:"timeouts"`
}

type TLSConfig struct {
    Enabled  bool   `mapstructure:"enabled"`   // default: false
    CertFile string `mapstructure:"cert_file"`
    KeyFile  string `mapstructure:"key_file"`
}

type TimeoutConfig struct {
    Read     time.Duration `mapstructure:"read"`     // default: 30s
    Write    time.Duration `mapstructure:"write"`    // default: 60s
    Idle     time.Duration `mapstructure:"idle"`     // default: 120s
    Upstream time.Duration `mapstructure:"upstream"` // default: 30s
}

type DatabaseConfig struct {
    URL             string        `mapstructure:"url"`
    MaxOpenConns    int           `mapstructure:"max_open_conns"`    // default: 25
    MaxIdleConns    int           `mapstructure:"max_idle_conns"`    // default: 5
    ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"` // default: 5m
}

type RedisConfig struct {
    URL      string `mapstructure:"url"`       // default: redis://localhost:6379/0
    PoolSize int    `mapstructure:"pool_size"` // default: 10
}

type AuthConfig struct {
    JWTSecret        string        `mapstructure:"jwt_secret"`         // REQUIRED
    AccessTokenTTL   time.Duration `mapstructure:"access_token_ttl"`   // default: 1h
    RefreshTokenTTL  time.Duration `mapstructure:"refresh_token_ttl"`  // default: 720h
    APIKeyPrefix     string        `mapstructure:"api_key_prefix"`     // default: "cnd_"
    APIKeyCacheTTL   time.Duration `mapstructure:"api_key_cache_ttl"`  // default: 5m
}

type RateLimitConfig struct {
    Enabled            bool    `mapstructure:"enabled"`              // default: true
    DefaultPerTenant   int     `mapstructure:"default_per_tenant"`   // default: 1000 req/min
    BurstMultiplier    float64 `mapstructure:"burst_multiplier"`     // default: 1.5
    FailOpen           bool    `mapstructure:"fail_open"`            // default: true
}

type AuditConfig struct {
    Enabled       bool          `mapstructure:"enabled"`        // default: true; cannot be false in OSS
    BufferSize    int           `mapstructure:"buffer_size"`    // default: 10000
    FlushInterval time.Duration `mapstructure:"flush_interval"` // default: 1s
    RedactArgs    bool          `mapstructure:"redact_args"`    // default: false
    RetentionDays int           `mapstructure:"retention_days"` // default: 90
}

type ObservabilityConfig struct {
    OTELEndpoint       string  `mapstructure:"otel_endpoint"`        // default: "" (disabled)
    LogLevel           string  `mapstructure:"log_level"`            // default: "info"
    LogFormat          string  `mapstructure:"log_format"`           // default: "json"
    TraceSamplingRate  float64 `mapstructure:"trace_sampling_rate"`  // default: 1.0
}

type PluginsConfig struct {
    Dir         string        `mapstructure:"dir"`          // default: ""
    HTTPTimeout time.Duration `mapstructure:"http_timeout"` // default: 5s
}

type PolicyConfig struct {
    File           string        `mapstructure:"file"`            // default: ""
    ReloadInterval time.Duration `mapstructure:"reload_interval"` // default: 10s
}

type WebhooksConfig struct {
    MaxRetries   int    `mapstructure:"max_retries"`   // default: 5
    RetryBackoff string `mapstructure:"retry_backoff"` // default: "1s,5s,30s,5m,30m"
    Timeout      time.Duration `mapstructure:"timeout"` // default: 10s
}
```

---

## 2. Load Function

```go
// Load reads configuration from the given file path and applies
// environment variable overrides.
//
// Environment variable override rules:
//   - Prefix: CONDUIT_
//   - Nested keys joined with _: server.port → CONDUIT_SERVER_PORT
//   - Special cases (no prefix needed):
//       DATABASE_URL overrides database.url
//       REDIS_URL overrides redis.url
//       JWT_SECRET overrides auth.jwt_secret
//
// Returns an error if:
//   - The file does not exist (unless path is "")
//   - The YAML is invalid
//   - Required fields are missing (jwt_secret in production)
//   - Port numbers are out of range [1, 65535]
func Load(path string) (*Config, error)

// Validate checks that the config is complete and consistent.
// Called automatically by Load.
func (c *Config) Validate() error {
    // MUST check:
    // 1. Server.Port != Server.ManagementPort != Server.MetricsPort
    // 2. Auth.JWTSecret is not empty and at least 32 characters
    // 3. Database.URL is a valid postgres:// URL if database is enabled
    // 4. Redis.URL is a valid redis:// URL
    // 5. Audit.BufferSize >= 100
    // 6. RateLimit.BurstMultiplier >= 1.0
}

// DefaultConfig returns a Config with all defaults applied.
// Used when no config file is provided (e.g., in tests).
func DefaultConfig() *Config
```

---

## 3. Default `conduit.yaml`

```yaml
# conduit.yaml — Conduit development configuration
# Copy to /etc/conduit/conduit.yaml in production

server:
  port: 8080
  management_port: 8081
  metrics_port: 9090
  tls:
    enabled: false
    cert_file: ""
    key_file: ""
  timeouts:
    read: 30s
    write: 60s
    idle: 120s
    upstream: 30s

database:
  url: "postgres://conduit:conduit@localhost:5432/conduit?sslmode=disable"
  max_open_conns: 25
  max_idle_conns: 5
  conn_max_lifetime: 5m

redis:
  url: "redis://localhost:6379/0"
  pool_size: 10

auth:
  jwt_secret: "${JWT_SECRET}"  # Must be set via environment variable
  access_token_ttl: 1h
  refresh_token_ttl: 720h      # 30 days
  api_key_prefix: "cnd_"
  api_key_cache_ttl: 5m

rate_limiting:
  enabled: true
  default_per_tenant: 1000     # requests per minute
  burst_multiplier: 1.5
  fail_open: true              # if Redis is down, allow requests

audit:
  enabled: true
  buffer_size: 10000
  flush_interval: 1s
  redact_args: false
  retention_days: 90

observability:
  otel_endpoint: ""            # set to "http://localhost:4317" to enable
  log_level: "info"
  log_format: "json"
  trace_sampling_rate: 1.0

plugins:
  dir: ""                      # empty = no native plugins loaded
  http_timeout: 5s

policy:
  file: ""                     # empty = no policy enforcement
  reload_interval: 10s

webhooks:
  max_retries: 5
  retry_backoff: "1s,5s,30s,5m,30m"
  timeout: 10s
```

---

## 4. Environment Variable Overrides

| Environment Variable | Config Path | Required |
|---|---|---|
| `JWT_SECRET` | `auth.jwt_secret` | Yes (production) |
| `DATABASE_URL` | `database.url` | Yes |
| `REDIS_URL` | `redis.url` | Yes |
| `CONDUIT_PORT` | `server.port` | No |
| `CONDUIT_MANAGEMENT_PORT` | `server.management_port` | No |
| `CONDUIT_METRICS_PORT` | `server.metrics_port` | No |
| `CONDUIT_LOG_LEVEL` | `observability.log_level` | No |
| `CONDUIT_LOG_FORMAT` | `observability.log_format` | No |
| `CONDUIT_OTEL_ENDPOINT` | `observability.otel_endpoint` | No |
| `CONDUIT_CONFIG` | config file path | No (default: `conduit.yaml`) |
| `CONDUIT_WORM_S3_BUCKET` | enterprise only | No |
| `CONDUIT_WORM_S3_REGION` | enterprise only | No |

---

## 5. go.mod Requirements

```go
module github.com/conduit-oss/conduit

go 1.23

require (
    github.com/jackc/pgx/v5 v5.7.0
    github.com/redis/go-redis/v9 v9.7.0
    github.com/golang-migrate/migrate/v4 v4.18.0
    github.com/spf13/cobra v1.8.1
    github.com/spf13/viper v1.19.0
    github.com/go-chi/chi/v5 v5.1.0
    github.com/rs/zerolog v1.33.0
    github.com/prometheus/client_golang v1.20.0
    go.opentelemetry.io/otel v1.31.0
    go.opentelemetry.io/otel/trace v1.31.0
    go.opentelemetry.io/otel/sdk v1.31.0
    go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.31.0
    github.com/lestrrat-go/jwx/v2 v2.1.2
    golang.org/x/crypto v0.29.0
    github.com/fsnotify/fsnotify v1.7.0
    gopkg.in/yaml.v3 v3.0.1
    github.com/google/uuid v1.6.0
)

require (
    github.com/testcontainers/testcontainers-go v0.34.0
    github.com/stretchr/testify v1.9.0
)
```

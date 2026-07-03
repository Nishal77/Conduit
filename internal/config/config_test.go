package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validSecret() string {
	return "01234567890123456789012345678901" // 33 chars
}

func TestDefaultConfig_MatchesConduitYAML(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, 8081, cfg.Server.ManagementPort)
	assert.Equal(t, 9090, cfg.Server.MetricsPort)
	assert.Equal(t, 30*time.Second, cfg.Server.Timeouts.Read)
	assert.Equal(t, "cnd_", cfg.Auth.APIKeyPrefix)
	assert.Equal(t, 1000, cfg.RateLimit.DefaultPerTenant)
	assert.Equal(t, 10000, cfg.Audit.BufferSize)
	assert.True(t, cfg.RateLimit.FailOpen)
}

func TestLoad_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conduit.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server:
  port: 9999
auth:
  jwt_secret: "`+validSecret()+`"
`), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, 9999, cfg.Server.Port)
	// unspecified fields still get defaults
	assert.Equal(t, 8081, cfg.Server.ManagementPort)
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/conduit.yaml")
	assert.Error(t, err)
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conduit.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
auth:
  jwt_secret: "`+validSecret()+`"
database:
  url: "postgres://file-value/db"
`), 0o644))

	t.Setenv("DATABASE_URL", "postgres://env-value/db")
	t.Setenv("JWT_SECRET", validSecret())
	t.Setenv("CONDUIT_PORT", "7777")

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "postgres://env-value/db", cfg.Database.URL)
	assert.Equal(t, 7777, cfg.Server.Port)
}

func TestValidate_RejectsShortJWTSecret(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWTSecret = "too-short"
	assert.Error(t, cfg.Validate())
}

func TestValidate_RejectsDuplicatePorts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWTSecret = validSecret()
	cfg.Server.ManagementPort = cfg.Server.Port
	assert.Error(t, cfg.Validate())
}

func TestValidate_RejectsOutOfRangePort(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWTSecret = validSecret()
	cfg.Server.Port = 70000
	assert.Error(t, cfg.Validate())
}

func TestValidate_RejectsBadDatabaseURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWTSecret = validSecret()
	cfg.Database.URL = "mysql://localhost/db"
	assert.Error(t, cfg.Validate())
}

func TestValidate_RejectsBadRedisURL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWTSecret = validSecret()
	cfg.Redis.URL = "http://localhost:6379"
	assert.Error(t, cfg.Validate())
}

func TestValidate_RejectsSmallAuditBuffer(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWTSecret = validSecret()
	cfg.Audit.BufferSize = 10
	assert.Error(t, cfg.Validate())
}

func TestValidate_RejectsSubOneBurstMultiplier(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWTSecret = validSecret()
	cfg.RateLimit.BurstMultiplier = 0.5
	assert.Error(t, cfg.Validate())
}

func TestValidate_AcceptsValidConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWTSecret = validSecret()
	assert.NoError(t, cfg.Validate())
}

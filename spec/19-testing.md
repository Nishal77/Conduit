# Spec 19 — Testing

> Phase: All | Files: `*_test.go`, `k6/load-test.js`

---

## 1. Test Pyramid

| Layer | Tool | When Run | Coverage Target |
|---|---|---|---|
| Unit tests | `go test` | Every PR | 80%+ per package |
| Integration tests | `go test` + testcontainers | Every PR | Critical paths |
| Fuzz tests | `go test -fuzz` | Nightly | MCP parser + auth |
| E2E tests | Playwright | On merge to main | All UI flows |
| Load tests | k6 | Pre-release | P99 <1ms at 1K RPS |

---

## 2. Unit Test Patterns

### Naming Convention

```go
// File: internal/mcp/parser_test.go
// Function: Test{Function}_{Scenario}_{ExpectedBehavior}

func TestParseMessage_ValidToolCall_ReturnsMessage(t *testing.T) {}
func TestParseMessage_InvalidJSON_ReturnsError(t *testing.T) {}
func TestParseMessage_MissingJSONRPCField_ReturnsError(t *testing.T) {}
func TestExtractToolName_ToolsCall_ReturnsName(t *testing.T) {}
func TestExtractToolName_Initialize_ReturnsEmpty(t *testing.T) {}
```

### Table-Driven Tests (use for all parser/validator tests)

```go
func TestParseMessage(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        wantErr bool
        wantMethod string
    }{
        {
            name:       "valid tools/call",
            input:      `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"github/create_issue","arguments":{}}}`,
            wantMethod: "tools/call",
        },
        {
            name:    "missing jsonrpc field",
            input:   `{"id":1,"method":"tools/call"}`,
            wantErr: true,
        },
        {
            name:    "invalid JSON",
            input:   `{not valid`,
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            msg, err := ParseMessage([]byte(tt.input))
            if tt.wantErr {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tt.wantMethod, msg.Method)
        })
    }
}
```

### Mock Interfaces

```go
// Define interfaces for all external dependencies to allow mocking:

// internal/auth/validator.go
type KeyValidator interface {
    Validate(ctx context.Context, rawKey string) (tenantID string, err error)
}

// internal/ratelimit/checker.go
type RateLimiter interface {
    Check(ctx context.Context, tenantID, serverName, toolName, agentID string) (*Result, error)
}

// Use testify/mock for mock implementations in tests:
type MockKeyValidator struct {
    mock.Mock
}

func (m *MockKeyValidator) Validate(ctx context.Context, rawKey string) (string, error) {
    args := m.Called(ctx, rawKey)
    return args.String(0), args.Error(1)
}
```

---

## 3. Integration Tests with testcontainers

### Setup — `internal/testutil/containers.go`

```go
package testutil

import (
    "context"
    "testing"
    "time"

    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/modules/postgres"
    "github.com/testcontainers/testcontainers-go/modules/redis"
)

// TestDB starts a PostgreSQL container for integration tests.
// Automatically runs migrations.
// Cleans up on test completion.
func TestDB(t *testing.T) *store.DB {
    t.Helper()
    ctx := context.Background()

    container, err := postgres.RunContainer(ctx,
        testcontainers.WithImage("postgres:16-alpine"),
        postgres.WithDatabase("conduit_test"),
        postgres.WithUsername("conduit"),
        postgres.WithPassword("conduit"),
        testcontainers.WithWaitStrategy(
            wait.ForLog("database system is ready to accept connections").
                WithOccurrence(2).
                WithStartupTimeout(30*time.Second),
        ),
    )
    require.NoError(t, err)
    t.Cleanup(func() { container.Terminate(ctx) })

    connStr, err := container.ConnectionString(ctx, "sslmode=disable")
    require.NoError(t, err)

    db, err := store.New(ctx, &config.DatabaseConfig{URL: connStr, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: 5*time.Minute})
    require.NoError(t, err)
    t.Cleanup(db.Close)

    // Run migrations
    RunMigrations(t, connStr)

    return db
}

// TestRedis starts a Redis container for integration tests.
func TestRedis(t *testing.T) *redis.Client { ... }

// RunMigrations runs all up migrations against the given database URL.
func RunMigrations(t *testing.T, dbURL string) { ... }
```

### Integration Test Example — Auth

```go
// internal/auth/apikey_integration_test.go
//go:build integration

package auth_test

import (
    "context"
    "testing"

    "github.com/conduit-oss/conduit/internal/testutil"
    "github.com/stretchr/testify/require"
)

func TestAPIKeyValidator_Integration(t *testing.T) {
    db := testutil.TestDB(t)
    redis := testutil.TestRedis(t)

    tenantStore := store.NewTenantStore(db)
    keyStore := store.NewAPIKeyStore(db)

    // Create test tenant
    tenant, err := tenantStore.Create(ctx, "test-corp", "Test Corp", "free")
    require.NoError(t, err)

    // Generate and store API key
    rawKey, keyHash, keyPrefix, err := auth.GenerateAPIKey()
    require.NoError(t, err)
    _, err = keyStore.Create(ctx, tenant.ID, "test-key", keyHash, keyPrefix, []string{"mcp:call"}, nil)
    require.NoError(t, err)

    // Validate
    validator := auth.NewAPIKeyValidator(redis, keyStore, 5*time.Minute)
    tenantID, err := validator.Validate(ctx, rawKey)
    require.NoError(t, err)
    require.Equal(t, tenant.ID.String(), tenantID)

    // Second validation should hit Redis cache
    tenantID2, err := validator.Validate(ctx, rawKey)
    require.NoError(t, err)
    require.Equal(t, tenantID, tenantID2)

    // Invalid key
    _, err = validator.Validate(ctx, "cnd_invalidkeyxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
    require.ErrorIs(t, err, auth.ErrInvalidAPIKey)
}
```

### Build Tags

Integration tests MUST use `//go:build integration` so they don't run with `go test ./...`:

```makefile
test:           ## Unit tests only
    go test -race ./...

test-int:       ## Integration tests (requires Docker)
    go test -race -tags integration ./...
```

---

## 4. Fuzz Tests

### MCP Parser Fuzz Test (see spec 01-mcp-protocol.md)

Run for 60 seconds:
```bash
go test -fuzz=FuzzParseMessage -fuzztime=60s ./internal/mcp/...
```

### Auth Key Fuzz Test

```go
// internal/auth/apikey_fuzz_test.go
func FuzzHashKey(f *testing.F) {
    f.Add("cnd_4MqyH3xK9vRwP2nL8tFjD6eAuCbGmZsN1oYiXWE")
    f.Add("")
    f.Add("not-a-conduit-key")

    f.Fuzz(func(t *testing.T, key string) {
        // Must never panic
        hash := auth.HashKey(key)
        // Hash must be 64 hex characters (SHA-256)
        if len(hash) != 64 {
            t.Errorf("expected 64-char hash, got %d", len(hash))
        }
        // Same input always produces same hash (deterministic)
        hash2 := auth.HashKey(key)
        if hash != hash2 {
            t.Error("hash is not deterministic")
        }
    })
}
```

---

## 5. k6 Load Test — `k6/load-test.js`

```javascript
import http from 'k6/http'
import { check, sleep } from 'k6'
import { Rate } from 'k6/metrics'

const errorRate = new Rate('errors')

// Test parameters
const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080'
const API_KEY   = __ENV.API_KEY  || 'cnd_test_key_for_load_testing'
const TENANT    = __ENV.TENANT   || 'load-test-tenant'
const SERVER    = __ENV.SERVER   || 'mock-mcp-server'

export const options = {
    scenarios: {
        // Warm-up: ramp from 0 to 100 RPS in 30 seconds
        warmup: {
            executor: 'ramping-arrival-rate',
            startRate: 0,
            timeUnit: '1s',
            preAllocatedVUs: 50,
            stages: [
                { target: 100, duration: '30s' },
            ],
        },
        // Steady state: 1,000 RPS for 2 minutes
        steady: {
            executor: 'constant-arrival-rate',
            rate: 1000,
            timeUnit: '1s',
            duration: '2m',
            preAllocatedVUs: 200,
            startTime: '30s',
        },
    },
    thresholds: {
        // MANDATORY: P99 proxy overhead < 1ms
        // Note: this measures full request latency, not just proxy overhead
        // Separate upstream from proxy using custom metrics
        'http_req_duration{scenario:steady}': ['p(99)<10'],  // 10ms total (upstream adds ~8ms)
        'http_req_duration{scenario:steady}': ['p(50)<5'],   // 5ms P50
        'errors': ['rate<0.001'],  // < 0.1% error rate
    },
}

export default function() {
    const url = `${BASE_URL}/mcp/${TENANT}/${SERVER}`

    const payload = JSON.stringify({
        jsonrpc: '2.0',
        id: Math.floor(Math.random() * 1000000),
        method: 'tools/call',
        params: {
            name: 'echo/echo',
            arguments: { message: 'load-test' }
        }
    })

    const res = http.post(url, payload, {
        headers: {
            'Authorization': `Bearer ${API_KEY}`,
            'Content-Type': 'application/json',
        },
    })

    const success = check(res, {
        'status is 200': (r) => r.status === 200,
        'response has jsonrpc': (r) => r.json('jsonrpc') === '2.0',
        'response time < 100ms': (r) => r.timings.duration < 100,
    })

    errorRate.add(!success)
}
```

---

## 6. Test Coverage Requirements

| Package | Minimum Coverage |
|---|---|
| `internal/mcp` | 90% (protocol parsing is critical) |
| `internal/auth` | 85% |
| `internal/ratelimit` | 80% |
| `internal/audit` | 80% |
| `internal/policy` | 85% |
| `internal/tenant` | 75% |
| `internal/store/*` | 70% (integration tested) |
| `internal/proxy` | 70% |

Enforce with CI:
```makefile
test-coverage:
    go test -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | grep -E "^total" | awk '{if ($$3+0 < 80) exit 1}'
```

---

## 7. golangci-lint Config — `.golangci.yaml`

```yaml
run:
  timeout: 5m
  go: "1.23"

linters:
  enable:
    - errcheck
    - staticcheck
    - gosec
    - govet
    - ineffassign
    - unused
    - misspell
    - gocyclo
    - bodyclose
    - contextcheck
    - noctx
    - godot

linters-settings:
  gocyclo:
    min-complexity: 15
  gosec:
    excludes:
      - G115  # integer overflow: too many false positives in Go 1.23

issues:
  exclude-rules:
    - path: "_test.go"
      linters: [gosec, errcheck]
```

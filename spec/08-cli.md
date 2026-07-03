# Spec 08 — CLI

> Phase: P3 | File: `cmd/conduit/main.go`

---

## 1. Framework

Use `github.com/spf13/cobra`. All commands live in `cmd/conduit/main.go` for Phase 3. Refactor to `cmd/conduit/cmd/` subdirectory in Phase 4.

Global flags (available on all commands):
```
--config string   Path to conduit.yaml (default: "conduit.yaml")
--log-level string  Log level: debug|info|warn|error (default: "info")
```

---

## 2. Root Command

```go
var rootCmd = &cobra.Command{
    Use:   "conduit",
    Short: "Production MCP gateway",
    Long:  "Conduit is a production gateway for the Model Context Protocol (MCP).",
}
```

---

## 3. `conduit proxy start`

```
conduit proxy start [flags]

Flags:
  --config string   Path to conduit.yaml (default: "conduit.yaml")

Behavior:
  1. Load config from --config path (or CONDUIT_CONFIG env var)
  2. Validate config (call config.Validate())
  3. Connect to PostgreSQL (store.New)
  4. Connect to Redis (redis.NewClient)
  5. Start audit writer (audit.New)
  6. Start proxy HTTP server on config.Server.Port
  7. Start management API on config.Server.ManagementPort (Phase 4)
  8. Start metrics server on config.Server.MetricsPort
  9. Log "conduit proxy started" with port info
  10. Block until SIGINT or SIGTERM received
  11. Graceful shutdown:
      a. Stop accepting new connections (30s timeout)
      b. Wait for in-flight requests to complete
      c. Shutdown audit writer (drain + flush)
      d. Close PostgreSQL pool
      e. Close Redis client
      f. Exit 0

Exit codes:
  0 = clean shutdown
  1 = startup error (config invalid, cannot connect to DB/Redis)
  2 = runtime error (panic recovered, written to log)
```

---

## 4. `conduit migrate`

```
conduit migrate [flags]

Flags:
  --db-url string   PostgreSQL URL (overrides config; required if no config file)
  --config string   Path to conduit.yaml
  --down            Rollback the last migration (default: run all up)
  --steps int       Number of migration steps to apply (default: all)
  --version         Show current migration version and exit

Behavior:
  - Uses golang-migrate/migrate/v4 with pgx driver
  - Migrations path: embedded via go:embed migrations/*.sql
  - On success: logs each migration applied with version number
  - On error: logs error and exit 1 (golang-migrate handles partial rollback)
  - Never runs migrations automatically on proxy start — always explicit

Output (--down, --steps, --version are mutually exclusive):
  Applied migration 000001_initial_schema (took 42ms)
  Applied migration 000002_audit_table (took 18ms)
  All migrations applied. Current version: 2
```

---

## 5. `conduit apikey create`

```
conduit apikey create [flags]

Flags:
  --name string    Name/description for this API key (required)
  --tenant string  Tenant slug (required)
  --config string  Path to conduit.yaml
  --expires string Expiry duration, e.g. "30d", "90d", "1y" (default: never)

Output on success:
  API Key created successfully

  Name:       my-agent-key
  Tenant:     acme-corp
  Key:        cnd_4MqyH3xK9vRwP2nL8tFjD6eAuCbGmZsN1oYiXWE
  Key prefix: cnd_4MqyH3xK
  Expires:    never
  Created:    2026-07-01T00:00:00Z

  ⚠  Store this key securely — it will not be shown again.

Exit behavior:
  - The raw key is printed ONCE and never retrievable again
  - Exit 0 on success, 1 on error
```

---

## 6. `conduit apikey list`

```
conduit apikey list [flags]

Flags:
  --tenant string  Tenant slug (required)
  --config string  Path to conduit.yaml
  --json           Output as JSON array

Table output (default):
  ID                                   Name             Prefix        Created              Last Used
  a1b2c3d4-...                         my-agent-key     cnd_4MqyH3xK  2026-07-01 12:00:00  2026-07-02 08:00:00
  e5f6a7b8-...                         ci-key           cnd_9XzWqRvT   2026-06-15 09:00:00  never

JSON output (--json):
  [
    {
      "id": "a1b2c3d4-...",
      "name": "my-agent-key",
      "key_prefix": "cnd_4MqyH3xK",
      "scopes": ["mcp:call"],
      "created_at": "2026-07-01T12:00:00Z",
      "last_used_at": "2026-07-02T08:00:00Z",
      "expires_at": null
    }
  ]
```

---

## 7. `conduit apikey revoke`

```
conduit apikey revoke <id> [flags]

Arguments:
  id    UUID of the API key to revoke

Flags:
  --config string  Path to conduit.yaml
  --force          Skip confirmation prompt

Behavior:
  - Shows confirmation prompt unless --force:
      Revoke key "my-agent-key" (cnd_4MqyH3xK)? [y/N]
  - On confirm: sets revoked_at in DB, flushes Redis cache entry
  - Output: "API key revoked."
  - Exit 0 on success, 1 on error
```

---

## 8. `conduit audit tail`

```
conduit audit tail [flags]

Flags:
  --tenant string    Tenant slug to stream events for (required)
  --tool string      Filter by tool name prefix, e.g. "github/*"
  --since string     Start from this time (e.g. "5m", "1h", "2026-07-01T00:00:00Z")
  --config string    Path to conduit.yaml
  --json             Output raw JSON events (default: pretty table)

Behavior:
  1. Connect to management API SSE endpoint: GET /api/v1/tenants/{slug}/audit/stream
  2. For Phase 3 (no mgmt API yet): connect directly to PostgreSQL and poll
  3. Display events as they arrive

Table output (default):
  TIME                 TENANT     TOOL                     STATUS  LATENCY  POLICY
  12:00:00.123         acme-corp  github/create_issue      200     0.8ms    allow
  12:00:01.456         acme-corp  stripe/create_charge     200     1.2ms    allow
  12:00:02.789         acme-corp  github/delete_repo       403     0.1ms    deny

JSON output (--json):
  {"tenant_id":"...","tool_name":"github/create_issue","status_code":200,...}

  (one JSON object per line, newline-delimited)

Exit:
  CTRL+C to stop — clean exit
```

---

## 9. `conduit audit query`

```
conduit audit query [flags]

Flags:
  --tenant string   Tenant slug (required)
  --from string     Start time (ISO 8601 or relative: "24h", "7d")
  --to string       End time (default: now)
  --tool string     Filter by tool name (exact or prefix with *)
  --limit int       Number of results (default: 50, max: 500)
  --offset int      Pagination offset
  --output string   Output format: "table" | "json" | "csv" (default: "table")
  --config string   Path to conduit.yaml

Table output:
  TIME                 TOOL                     STATUS  LATENCY  POLICY  COST
  2026-07-01 12:00:00  github/create_issue      200     0.8ms    allow   $0.0001
  2026-07-01 12:00:01  stripe/create_charge     200     1.2ms    allow   $0.0002

JSON output:
  {"events": [...], "total": 1234, "limit": 50, "offset": 0}

CSV output:
  created_at,tool_name,status_code,latency_ms,policy_action,cost_usd
  2026-07-01T12:00:00Z,github/create_issue,200,1,allow,0.0001
```

---

## 10. `conduit audit export`

```
conduit audit export [flags]

Flags:
  --tenant string   Tenant slug (required)
  --from string     Start time (required)
  --to string       End time (required)
  --format string   Export format: "csv" | "json" (default: "csv")
  --output string   Output file path (default: stdout)
  --config string   Path to conduit.yaml

Behavior:
  - Streams all matching events (no limit/offset — full export)
  - Writes to --output file or stdout
  - Shows progress bar if writing to file
  - On completion: "Exported 12,345 events to audit_2026-07.csv"
```

---

## 11. `conduit version`

```
conduit version [--json]

Default output:
  conduit v0.1.0 (commit: abc1234, built: 2026-07-01T00:00:00Z)

JSON output:
  {"version":"0.1.0","commit":"abc1234","built":"2026-07-01T00:00:00Z","go":"1.23.0"}
```

Version is injected at build time via `-ldflags`:
```makefile
VERSION ?= $(shell git describe --tags --always --dirty)
COMMIT  ?= $(shell git rev-parse --short HEAD)
BUILT   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

build:
    go build -ldflags="-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.built=$(BUILT)" \
        -o bin/conduit ./cmd/conduit
```

---

## 12. `conduit completion`

```
conduit completion [bash|zsh|fish|powershell]

Generates shell completion scripts. Standard cobra behavior.
```

---

## 13. Main Entry Point Structure

```go
// cmd/conduit/main.go

package main

import (
    "os"

    "github.com/spf13/cobra"
)

var (
    version = "dev"
    commit  = "none"
    built   = "unknown"
)

func main() {
    rootCmd := buildRootCmd()
    if err := rootCmd.Execute(); err != nil {
        os.Exit(1)
    }
}

func buildRootCmd() *cobra.Command {
    root := &cobra.Command{
        Use:   "conduit",
        Short: "Production MCP gateway",
    }

    // Global flags
    root.PersistentFlags().String("config", "conduit.yaml", "Path to conduit.yaml")
    root.PersistentFlags().String("log-level", "info", "Log level (debug|info|warn|error)")

    // Subcommands
    root.AddCommand(
        buildProxyCmd(),
        buildMigrateCmd(),
        buildAPIKeyCmd(),
        buildAuditCmd(),
        buildVersionCmd(version, commit, built),
        buildCompletionCmd(root),
    )

    return root
}
```

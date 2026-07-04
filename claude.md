# CLAUDE.md — Conduit Project Master Context

> This file is the single source of truth for building Conduit.
> Every AI session working on this project should read this file first.
> Keep it updated as decisions are made and phases complete.

---

## 1. What Is Conduit?

**Conduit** is the production gateway for the Model Context Protocol (MCP).

MCP (Model Context Protocol) is Anthropic's open standard — now a Linux Foundation project (Dec 2025) — that defines how AI agents call external tools. There are 97M monthly SDK downloads, 12,000+ MCP servers, and zero production-ready open-source gateways. Every team deploying AI agents is either building auth/rate-limiting/audit themselves or running without it.

Conduit fills that gap.

**One-line pitch:** "Kong / Traefik for AI agent tool calls."

**What it does:**
- Sits between AI agents and upstream MCP servers
- Enforces authentication (API keys + OAuth 2.0 with PKCE)
- Enforces per-tenant rate limiting (Redis token bucket)
- Routes tool calls to the correct isolated upstream server pool (multi-tenant)
- Logs every tool call to an append-only audit log (EU AI Act compliance)
- Evaluates YAML policy rules (allow/deny) before forwarding
- Runs plugins (Before/After hooks) for PII redaction, cost tracking, etc.
- Exposes Prometheus metrics + OTel traces for full observability

**Score:** 96/100 — 🌍 Category-Defining Open Source Opportunity
**License:** Apache 2.0
**GitHub org:** conduit-oss
**Repository:** github.com/conduit-oss/conduit

---

## 2. Tech Stack (Non-Negotiable)

| Layer | Technology | Why |
|---|---|---|
| Proxy / API | **Go 1.23** | Net/http, SSE streaming, <1ms overhead, single binary |
| Frontend Dashboard | **TypeScript + Next.js 15** | shadcn/ui, Tailwind, App Router |
| Primary Database | **PostgreSQL 16** | ACID, partitioned audit table, pgx/v5 driver |
| Cache / Rate Limiting | **Redis 7** | Atomic Lua scripts, token bucket, token cache |
| Observability | **OpenTelemetry + Prometheus** | OTel Go SDK, prometheus/client_golang |
| Logging | **zerolog** | Structured JSON, trace_id correlation |
| CLI | **cobra** | Standard Go CLI framework |
| Migrations | **golang-migrate** | SQL migration files, up/down |
| Container | **Docker (multi-stage, distroless)** | Target <20MB image |
| Kubernetes | **Helm 3 + controller-runtime** | CRD operator for ConduitTenant/ConduitServer |
| TypeScript SDK | **@conduit/sdk (npm)** | Full management API client + OAuth helper |
| Go SDK | **conduit/sdk/go** | Self-registration for MCP server authors |
| CI/CD | **GitHub Actions + GoReleaser** | Multi-arch build, semver releases |
| Testing | **go test + testcontainers-go + Playwright + k6** | Real PG/Redis in integration tests |

---

## 3. Repository Structure

```
conduit/
├── cmd/
│   └── conduit/
│       └── main.go                  # Binary entrypoint
├── internal/
│   ├── proxy/                       # Core reverse proxy + SSE streaming
│   │   ├── proxy.go
│   │   ├── sse.go
│   │   └── middleware.go
│   ├── auth/                        # API key + OAuth 2.0 validation
│   │   ├── apikey.go
│   │   ├── oauth.go
│   │   ├── jwt.go
│   │   └── middleware.go
│   ├── ratelimit/                   # Token bucket, Redis Lua, config loader
│   │   ├── limiter.go
│   │   ├── lua/
│   │   │   └── token_bucket.lua
│   │   └── middleware.go
│   ├── mcp/                         # MCP protocol types + message parser
│   │   ├── types.go
│   │   ├── parser.go
│   │   └── parser_fuzz_test.go
│   ├── audit/                       # Append-only audit log writer
│   │   ├── writer.go
│   │   ├── query.go
│   │   └── stream.go
│   ├── policy/                      # YAML policy engine + hot-reload
│   │   ├── engine.go
│   │   ├── loader.go
│   │   └── types.go
│   ├── plugin/                      # Plugin interface + registry + HTTP callback
│   │   ├── interface.go
│   │   ├── registry.go
│   │   ├── http_callback.go
│   │   └── builtin/
│   │       ├── pii_redactor.go
│   │       ├── cost_tracker.go
│   │       ├── transform.go
│   │       ├── circuit_breaker.go
│   │       └── logger.go
│   ├── tenant/                      # Tenant store + routing table
│   │   ├── store.go
│   │   └── router.go
│   ├── cost/                        # Cost estimation + budget enforcement
│   │   ├── estimator.go
│   │   └── budget.go
│   ├── webhook/                     # Webhook delivery + retry
│   │   ├── dispatcher.go
│   │   └── retry.go
│   ├── api/                         # REST management API (chi router)
│   │   ├── server.go
│   │   ├── handlers/
│   │   │   ├── tenants.go
│   │   │   ├── apikeys.go
│   │   │   ├── servers.go
│   │   │   ├── ratelimits.go
│   │   │   ├── audit.go
│   │   │   ├── plugins.go
│   │   │   ├── webhooks.go
│   │   │   └── oauth.go
│   │   └── middleware/
│   │       ├── auth.go
│   │       └── cors.go
│   ├── store/                       # PostgreSQL query layer (pgx/v5)
│   │   ├── db.go
│   │   ├── tenants.go
│   │   ├── apikeys.go
│   │   ├── servers.go
│   │   ├── audit.go
│   │   └── ratelimits.go
│   └── config/                      # YAML config loader + validation
│       └── config.go
├── pkg/
│   └── sdk/                         # Go SDK (also published as separate module)
│       ├── client.go
│       └── types.go
├── migrations/
│   ├── 000001_initial_schema.up.sql
│   ├── 000001_initial_schema.down.sql
│   ├── 000002_oauth_tables.up.sql
│   └── ...
├── helm/
│   ├── Chart.yaml
│   ├── values.yaml
│   └── templates/
│       ├── deployment.yaml
│       ├── service.yaml
│       ├── ingress.yaml
│       ├── hpa.yaml
│       ├── configmap.yaml
│       └── crds/
│           ├── conduit-tenant.yaml
│           └── conduit-server.yaml
├── dashboard/                       # Next.js TypeScript frontend
│   ├── app/
│   │   ├── layout.tsx
│   │   ├── page.tsx
│   │   ├── (dashboard)/
│   │   │   ├── traffic/
│   │   │   ├── audit/
│   │   │   ├── api-keys/
│   │   │   ├── servers/
│   │   │   ├── rate-limits/
│   │   │   └── plugins/
│   │   └── (auth)/
│   ├── components/
│   ├── lib/
│   └── package.json
├── sdk/
│   └── typescript/                  # @conduit/sdk npm package
│       ├── src/
│       │   ├── client.ts
│       │   ├── types.ts
│       │   └── index.ts
│       └── package.json
├── plugins/
│   └── examples/                    # Example community plugins
├── docker/
│   ├── Dockerfile
│   └── docker-compose.yml
├── dashboards/
│   └── conduit-overview.json        # Grafana dashboard
├── docs/                            # Documentation source
├── examples/                        # Example conduit.yaml configs
│   ├── single-tenant/
│   ├── multi-tenant/
│   └── kubernetes/
├── .github/
│   ├── workflows/
│   │   ├── ci.yml
│   │   ├── release.yml
│   │   └── security.yml
│   └── ISSUE_TEMPLATE/
├── conduit.yaml                     # Default config (development)
├── go.mod
├── go.sum
├── Makefile
├── CONTRIBUTING.md
├── SECURITY.md
├── CODE_OF_CONDUCT.md
└── README.md
```

---

## 4. Core Architecture

```
AI Agent (Claude / GPT / LangChain)
         │
         │  MCP JSON-RPC 2.0 over SSE
         ▼
┌─────────────────────────────────────────────────────┐
│                CONDUIT PROXY (:8080)                │
│                                                     │
│  ┌──────────────── Middleware Chain ──────────────┐ │
│  │  1. Auth        (API Key / OAuth JWT)          │ │
│  │  2. Rate Limit  (Redis Lua token bucket)       │ │
│  │  3. Policy      (YAML allow/deny engine)       │ │
│  │  4. Plugin.Before (pii-redactor, etc.)        │ │
│  │  5. MCP Forward (reverse proxy)               │ │
│  │  6. Plugin.After (cost-tracker, etc.)         │ │
│  │  7. Audit Write (async, non-blocking)         │ │
│  └────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────┘
         │
         │  tenant_id (from JWT/API key) → routing table
         ▼
┌──────────────────────────────────┐
│   Upstream MCP Server Pool       │
│   github-mcp  :3001              │
│   stripe-mcp  :3002              │
│   custom-mcp  :3003              │
└──────────────────────────────────┘

Support Services:
  PostgreSQL  ─── Tenants, API keys, OAuth apps, servers,
                  audit_events (partitioned), rate_limit_configs
  Redis       ─── Token bucket state, auth token cache,
                  routing table cache (5s TTL)

Management Layer:
  REST API (:8081) ─── Full CRUD for all resources
  Next.js UI        ─── Dashboard, live traffic, audit explorer
  Prometheus (:9090) ── All metrics
  OTel exporter     ── Traces → Jaeger / Tempo / Datadog
```

### Request Flow (per tool call)
1. Agent sends `POST /mcp/{tenant}/{server}` with `Authorization: Bearer <token>`
2. **Auth middleware**: validates API key (SHA-256 lookup in Redis cache → PostgreSQL fallback) or JWT signature. Extracts `tenant_id`. Rejects → 401.
3. **Rate limit middleware**: executes Redis Lua token bucket script. Exceeded → 429.
4. **Policy engine**: evaluates compiled YAML rules. Denied → 403.
5. **Plugin.Before hooks**: runs in-process (Go) or HTTP callback plugins sequentially.
6. **MCP forward**: proxies JSON-RPC 2.0 message to upstream SSE server. Streams response back.
7. **Plugin.After hooks**: runs post-response plugins (cost tracking, logging).
8. **Audit write**: sends audit event to buffered Go channel (capacity 10K). Returns to agent immediately. Background goroutine batch-inserts to PostgreSQL.
9. Total overhead target: **<1ms P99**.

---

## 5. Key Design Decisions (ADRs)

### ADR-001: Go for the proxy core
- net/http + bufio.Scanner for zero-copy SSE streaming
- Single static binary, <20MB Docker image
- <1ms proxy overhead is achievable in Go, not Node.js

### ADR-002: Non-blocking audit writes
- Audit events go to a buffered Go channel (cap: 10K)
- Background goroutine batch-inserts (every 1s or 100 events)
- Proxy latency is zero-coupled from PostgreSQL write throughput
- On shutdown: drain channel with 30s timeout before exit

### ADR-003: Redis Lua for rate limiting
- Single atomic operation = no race conditions between goroutines
- Token bucket: refill on each check, never stores negative values
- Key pattern: `rl:{tenant_id}:{scope}:{target}` (e.g., `rl:abc:tool:github/create_issue`)
- Fail-open mode: if Redis unavailable, log warning and allow request

### ADR-004: Tenant isolation via auth token
- `tenant_id` ALWAYS extracted from validated JWT or API key record
- Never trust tenant_id from request body or URL
- Routing table cached in-process with 5s TTL (zero Redis for routing)

### ADR-005: Plugin interface over generics
```go
type ConduitPlugin interface {
    Name()    string
    Version() string
    Before(ctx context.Context, req *MCPRequest)  (*MCPRequest, error)
    After(ctx context.Context, req *MCPRequest, resp *MCPResponse) (*MCPResponse, error)
    Shutdown(ctx context.Context) error
}
```
- HTTP callback plugins: Conduit POSTs JSON to a URL, waits for response
- This allows Python/Node/Rust plugins without Go compilation

### ADR-006: Policy engine is synchronous and in-process
- YAML compiled to decision tree at startup + hot-reload (fsnotify, 10s)
- Evaluation: <0.1ms (no I/O in path)
- Rules evaluated in order; first match wins

### ADR-007: PostgreSQL audit table is append-only
- No UPDATE, no DELETE on `audit_events` table — enforced at application level
- Monthly RANGE partitioning on `created_at`
- WORM mode (Enterprise): S3 Object Lock or immutable partition policy

### ADR-008: Management API on separate port
- Proxy: :8080
- Management API: :8081
- Metrics: :9090
- Network policy can block :8081 from agent traffic entirely

---

## 6. Database Schema (Key Tables)

```sql
-- Core tenant
CREATE TABLE tenants (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        TEXT UNIQUE NOT NULL,
    name        TEXT NOT NULL,
    plan        TEXT NOT NULL DEFAULT 'free',
    settings    JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at  TIMESTAMPTZ
);

-- API keys (never store raw key)
CREATE TABLE api_keys (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id),
    name        TEXT NOT NULL,
    key_hash    TEXT NOT NULL UNIQUE,   -- SHA-256 of raw key
    key_prefix  TEXT NOT NULL,          -- first 8 chars for display
    expires_at  TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at  TIMESTAMPTZ
);

-- Upstream MCP servers
CREATE TABLE mcp_servers (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id),
    name         TEXT NOT NULL,
    upstream_url TEXT NOT NULL,
    auth_type    TEXT NOT NULL DEFAULT 'none',
    auth_config  JSONB NOT NULL DEFAULT '{}',  -- AES-256-GCM encrypted
    health_check_url TEXT,
    weight       INT NOT NULL DEFAULT 100,
    enabled      BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(tenant_id, name)
);

-- Audit log (append-only, partitioned by month)
CREATE TABLE audit_events (
    id            UUID NOT NULL DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL,
    agent_id      TEXT,
    session_id    TEXT,
    server_name   TEXT NOT NULL,
    tool_name     TEXT NOT NULL,
    request_args  JSONB,          -- optionally redacted by pii-redactor plugin
    response_meta JSONB,
    status_code   INT NOT NULL,
    latency_ms    INT NOT NULL,
    auth_method   TEXT NOT NULL,
    policy_action TEXT NOT NULL,  -- allow | deny | rate_limited
    cost_usd      NUMERIC(12,8),
    trace_id      TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
) PARTITION BY RANGE (created_at);

-- Create monthly partitions (automate with pg_partman in production)
CREATE TABLE audit_events_2026_01 PARTITION OF audit_events
    FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');

-- OAuth applications
CREATE TABLE oauth_applications (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id),
    name          TEXT NOT NULL,
    client_id     TEXT NOT NULL UNIQUE,
    client_secret TEXT NOT NULL,   -- bcrypt(cost=12)
    redirect_uris TEXT[] NOT NULL,
    grant_types   TEXT[] NOT NULL DEFAULT '{authorization_code}',
    scopes        TEXT[] NOT NULL DEFAULT '{mcp:call}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Rate limit configurations
CREATE TABLE rate_limit_configs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id),
    scope       TEXT NOT NULL,   -- tenant | server | tool | agent
    target      TEXT,            -- NULL = applies to all in scope
    requests    INT NOT NULL,
    window_sec  INT NOT NULL,
    burst       INT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(tenant_id, scope, target)
);
```

---

## 7. API Key Format

```
cnd_<base64url(32 random bytes)>
     └─ 43 characters
Total: 47 characters

Example: cnd_4MqyH3xK9vRwP2nL8tFjD6eAuCbGmZsN1oYiXWE

Storage: SHA-256(raw_key) stored in api_keys.key_hash
Display: first 8 chars as key_prefix (e.g., "cnd_4Mqy")
```

---

## 8. MCP Message Types Handled

| MCP Method | Conduit Handling |
|---|---|
| `initialize` | Pass-through; record session start in audit |
| `tools/list` | Pass-through; optionally filter by policy |
| `tools/call` | Full middleware chain (auth→ratelimit→policy→plugin→audit) |
| `resources/list` | Pass-through |
| `resources/read` | Pass-through; optionally rate-limit |
| `prompts/list` | Pass-through |
| `prompts/get` | Pass-through |
| `notifications/*` | Pass-through; log in audit |
| `$/cancelRequest` | Pass-through immediately; cancel upstream if possible |

---

## 9. Build Phases

### STATUS TRACKING
Update this table as phases complete:

| Phase | Name | Weeks | Status |
|---|---|---|---|
| P1 | Repository & Core Proxy | 1–4 | ✅ Complete |
| P2 | Auth, Rate Limiting & Database | 5–8 | ✅ Complete |
| P3 | Audit Log, CLI & Docker (MVP 🚀) | 9–12 | ⬜ Not started |
| P4 | Management API & TypeScript Dashboard | 13–16 | ⬜ Not started |
| P5 | OAuth 2.0 & Multi-Tenant Routing | 17–20 | ⬜ Not started |
| P6 | Plugin System & Policy Engine | 21–24 | ⬜ Not started |
| P7 | Kubernetes, SDKs & Full Observability | 25–28 | ⬜ Not started |
| P8 | Enterprise Features | 29–40 | ⬜ Not started |
| P9 | OSS-Ready Launch & Community | 41–48 | ⬜ Not started |

---

### PHASE 1 — Repository & Core Proxy (Weeks 1–4)

**Goal:** A working MCP reverse proxy. Transparent pass-through with correct SSE streaming and JSON-RPC 2.0 handling. No auth, no state, just a proxy.

**Deliverables:**
- [ ] GitHub org `conduit-oss` created; Go module `github.com/conduit-oss/conduit` initialised
- [ ] Full directory structure per Section 3 above (empty files with package declarations)
- [ ] `internal/mcp/parser.go` — JSON-RPC 2.0 message parser
- [ ] `internal/mcp/parser_fuzz_test.go` — fuzz test seeded with valid MCP messages
- [ ] `internal/proxy/proxy.go` — net/http reverse proxy with custom transport
- [ ] `internal/proxy/sse.go` — SSE streaming proxy (bufio.Scanner, correct headers)
- [ ] `internal/config/config.go` — conduit.yaml loader (viper)
- [ ] `internal/proxy/middleware.go` — middleware chain skeleton (no-op auth/ratelimit stubs)
- [ ] `cmd/conduit/main.go` — binary entrypoint (cobra root command)
- [ ] `.golangci.yaml` — linter config (errcheck, staticcheck, gosec, govet)
- [ ] `.github/workflows/ci.yml` — lint + unit test on every push/PR
- [ ] `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`, `LICENSE` (Apache 2.0)
- [ ] `README.md` — project mission, architecture diagram (ASCII), quickstart stub
- [ ] `Makefile` — targets: build, test, lint, run, docker-build
- [ ] Unit tests for protocol parser reaching 80%+ coverage

**Done when:**
- `make run` starts the proxy on :8080
- MCP tool call sent directly to proxy flows through to the upstream server
- P99 overhead <1ms in isolated benchmark (no auth, no DB)

**Key files to write:**
```
cmd/conduit/main.go
internal/config/config.go
internal/mcp/types.go
internal/mcp/parser.go
internal/proxy/proxy.go
internal/proxy/sse.go
internal/proxy/middleware.go
conduit.yaml
go.mod
Makefile
```

---

### PHASE 2 — Auth, Rate Limiting & Database (Weeks 5–8)

**Goal:** API key auth + Redis rate limiting + PostgreSQL schema. The core security layer.

**Deliverables:**
- [ ] `migrations/000001_initial_schema.up.sql` — tenants, api_keys, mcp_servers, rate_limit_configs
- [ ] `internal/store/db.go` — pgx/v5 connection pool (25 max, 5 idle, 5m lifetime)
- [ ] `internal/store/tenants.go` — tenant CRUD queries
- [ ] `internal/store/apikeys.go` — API key lookup by hash, last_used_at update
- [ ] `internal/store/servers.go` — server registration queries
- [ ] `internal/auth/apikey.go` — SHA-256 key validation, Redis cache (5min TTL)
- [ ] `internal/auth/middleware.go` — auth middleware: validates key, sets tenant_id in context
- [ ] `internal/ratelimit/lua/token_bucket.lua` — atomic Redis token bucket script
- [ ] `internal/ratelimit/limiter.go` — loads config from DB, executes Lua, returns allow/deny
- [ ] `internal/ratelimit/middleware.go` — rate limit middleware, returns 429 with Retry-After
- [ ] `internal/tenant/store.go` — tenant routing table (in-process map, 5s TTL refresh)
- [ ] `internal/tenant/router.go` — routes tenant_id → upstream server URL
- [ ] Full integration test suite using testcontainers-go (real PostgreSQL + Redis)

**Done when:**
- Invalid API key → 401 Unauthorized
- Valid key but rate limit exceeded → 429 Too Many Requests with `Retry-After` header
- Valid key, within limits → tool call proxied successfully
- Auth middleware overhead <0.1ms (Redis cache hit)
- Rate limit Lua script overhead <0.2ms

**Key files to write:**
```
migrations/000001_initial_schema.up.sql
internal/store/db.go
internal/store/apikeys.go
internal/auth/apikey.go
internal/auth/middleware.go
internal/ratelimit/lua/token_bucket.lua
internal/ratelimit/limiter.go
internal/ratelimit/middleware.go
internal/tenant/store.go
internal/tenant/router.go
```

---

### PHASE 3 — Audit Log, CLI & Docker (Weeks 9–12) 🚀 MVP

**Goal:** Complete the MVP. Add audit logging, the CLI, Prometheus metrics, and Docker packaging. This is the version launched on HN.

**Deliverables:**
- [ ] `migrations/000002_audit_table.up.sql` — audit_events table with monthly partitions
- [ ] `internal/audit/writer.go` — buffered channel writer (cap 10K), batch PostgreSQL insert
- [ ] `internal/audit/query.go` — paginated query, date range filter, tool name filter
- [ ] `internal/audit/stream.go` — SSE-based live tail for `conduit audit tail`
- [ ] `cmd/conduit/main.go` — full cobra CLI:
  - `conduit proxy start --config conduit.yaml`
  - `conduit proxy stop`
  - `conduit migrate --db-url`
  - `conduit audit tail --tenant --tool --since`
  - `conduit audit query --from --to --limit --output json`
  - `conduit audit export --format csv`
  - `conduit apikey create --name --tenant`
  - `conduit apikey list --tenant`
  - `conduit apikey revoke <id>`
  - `conduit version`
  - `conduit completion`
- [ ] `internal/proxy/middleware.go` — add audit write call (async, non-blocking)
- [ ] Prometheus metrics: all 13 metrics defined in PRD Section 17.2
- [ ] Structured JSON logging (zerolog) with trace_id on every log line
- [ ] `docker/Dockerfile` — 3-stage: builder → migrator → distroless/static final
- [ ] `docker/docker-compose.yml` — conduit + postgres:16-alpine + redis:7-alpine
- [ ] `/healthz` and `/readyz` endpoints
- [ ] `dashboards/conduit-overview.json` — Grafana dashboard
- [ ] `examples/single-tenant/conduit.yaml` — working single-tenant example config
- [ ] `k6/load-test.js` — validates <1ms P99 at 1,000 RPS
- [ ] README quickstart: 5-minute demo using Docker Compose

**Done when:**
- `docker compose up` starts full stack in <30s
- `conduit audit tail` shows live events in terminal
- k6 load test passes: P99 <1ms at 1,000 RPS
- `docker run conduit/conduit version` works
- Every tool call appears in the audit log within 2s

**Key files to write:**
```
migrations/000002_audit_table.up.sql
internal/audit/writer.go
internal/audit/query.go
internal/audit/stream.go
internal/proxy/metrics.go
docker/Dockerfile
docker/docker-compose.yml
k6/load-test.js
dashboards/conduit-overview.json
```

---

### PHASE 4 — Management API & TypeScript Dashboard (Weeks 13–16)

**Goal:** REST management API + Next.js dashboard. Full GUI for managing tenants, keys, servers, rate limits, and viewing live traffic.

**Deliverables:**
- [ ] `internal/api/server.go` — chi router setup, CORS, request logging, recovery
- [ ] `internal/api/middleware/auth.go` — JWT validation for management API
- [ ] All handler files in `internal/api/handlers/`:
  - `tenants.go` — CRUD for tenants
  - `apikeys.go` — create, list, revoke
  - `servers.go` — register, list, health-check, remove
  - `ratelimits.go` — set, list, delete
  - `audit.go` — query, export, SSE stream endpoint
  - `webhooks.go` — CRUD for webhooks
  - `oauth.go` — OAuth application CRUD
- [ ] OpenAPI 3.1 spec (generated via swaggo annotations)
- [ ] `dashboard/` — full Next.js 15 app:
  - Layout with sidebar navigation
  - `/traffic` — real-time tool call stream (WebSocket or SSE)
  - `/audit` — paginated audit log with date/tool/tenant filters
  - `/api-keys` — create, list, revoke with copy-to-clipboard
  - `/servers` — register + health status per server
  - `/rate-limits` — configure per-tenant and per-tool limits
  - `/plugins` — list plugins, enable/disable per tenant
- [ ] shadcn/ui components: DataTable, charts (recharts), badges, dialogs
- [ ] Playwright E2E tests for: create API key, view audit log, register server

**Done when:**
- Full GUI management without touching CLI or database
- Live traffic visible in browser within 500ms of tool call
- OpenAPI spec generated at `/api/openapi.json`

---

### PHASE 5 — OAuth 2.0 & Multi-Tenant Routing (Weeks 17–20)

**Goal:** Full OAuth 2.0 server (PKCE + Client Credentials) and dynamic multi-tenant routing from PostgreSQL.

**Deliverables:**
- [ ] `migrations/000003_oauth_tables.up.sql` — oauth_applications, refresh_tokens, auth_codes
- [ ] `internal/auth/oauth.go`:
  - Authorization endpoint (`/oauth/authorize`)
  - Token endpoint (`/oauth/token`) — code exchange + client credentials
  - Introspection endpoint (`/oauth/introspect`)
  - Revocation endpoint (`/oauth/revoke`)
  - PKCE verifier (S256)
  - Refresh token rotation (SHA-256 hash stored)
- [ ] `internal/auth/jwt.go` — sign/verify JWTs, include tenant_id + scopes in claims
- [ ] Update `internal/auth/middleware.go` — handle both API key and JWT Bearer
- [ ] `internal/tenant/router.go` — dynamic routing: tenant_id → server pool (from DB, 5s cache)
- [ ] Multi-server per tenant: round-robin + weighted routing
- [ ] OAuth application management in dashboard
- [ ] RBAC: `users`, `tenant_members` tables + role enforcement in API middleware
- [ ] Full OAuth flow Playwright test (authorization code + PKCE)

**Done when:**
- Interactive agent (browser-based) completes OAuth PKCE flow end-to-end
- Machine agent uses client credentials for M2M auth
- 10 isolated tenants on one gateway, no data leakage between tenants

---

### PHASE 6 — Plugin System & Policy Engine (Weeks 21–24)

**Goal:** Extensible plugin system (Go interface + HTTP callbacks) and YAML policy engine. This is what makes Conduit a platform, not just a proxy.

**Deliverables:**
- [ ] `internal/plugin/interface.go` — ConduitPlugin interface definition
- [ ] `internal/plugin/registry.go` — plugin registration, ordering, lifecycle
- [ ] `internal/plugin/http_callback.go` — HTTP callback plugin (POST JSON to URL, parse response)
- [ ] Built-in plugins:
  - `internal/plugin/builtin/pii_redactor.go` — regex + pattern detection, redacts request_args
  - `internal/plugin/builtin/cost_tracker.go` — token count estimation, writes to audit event
  - `internal/plugin/builtin/transform.go` — JSONPath-based request/response transform
  - `internal/plugin/builtin/circuit_breaker.go` — failure threshold, half-open cooldown
  - `internal/plugin/builtin/logger.go` — structured log enrichment per call
- [ ] `internal/policy/engine.go` — compile YAML rules to decision tree
- [ ] `internal/policy/loader.go` — fsnotify hot-reload, reload on file change
- [ ] `internal/policy/types.go` — Rule, Condition, Action types
- [ ] `internal/webhook/dispatcher.go` — send webhook on events (policy.violation, ratelimit.exceeded, etc.)
- [ ] `internal/webhook/retry.go` — exponential backoff: 1s, 5s, 30s, 5m, 30m
- [ ] `migrations/000004_plugins_webhooks.up.sql` — plugins, tenant_plugins, webhook_configs tables
- [ ] Plugin management in dashboard
- [ ] Plugin SDK example (Python HTTP callback server)

**Done when:**
- Community member can write a plugin in Python by implementing the HTTP callback protocol
- Policy blocks `github/delete_repo` calls in <0.1ms
- Webhook fires within 2s of a rate limit breach

---

### PHASE 7 — Kubernetes, SDKs & Full Observability (Weeks 25–28)

**Goal:** Production Kubernetes deployment, TypeScript and Go SDKs, complete OTel tracing, automated releases.

**Deliverables:**
- [ ] `helm/` — full Helm chart:
  - Deployment (3 replicas default, non-root user)
  - Service (ClusterIP: proxy :8080, management :8081, metrics :9090)
  - Ingress (nginx, SSE timeout annotations: proxy-read-timeout: 3600)
  - HPA (2–20 replicas, CPU target 60%)
  - PodDisruptionBudget (minAvailable: 1)
  - ConfigMap for conduit.yaml
  - Secret for JWT_SECRET, DATABASE_URL, REDIS_URL
  - Subcharts: postgresql (bitnami), redis (bitnami)
- [ ] `helm/templates/crds/` — ConduitTenant + ConduitServer CRDs
- [ ] CRD controller (`internal/controller/`) using controller-runtime
- [ ] `sdk/typescript/` — `@conduit/sdk` npm package:
  - `ConduitClient` class with auto-refresh OAuth
  - `.servers`, `.tenants`, `.apiKeys`, `.rateLimits`, `.audit`, `.webhooks`, `.plugins`
  - `.audit.stream()` returns AsyncIterator of audit events
  - Full TypeScript types for all API responses
  - Published to npm
- [ ] `pkg/sdk/` — Go SDK:
  - `NewClient(Config)` with API key or OAuth
  - `.Servers.Register()`, `.Audit.Stream()`
  - Published as separate Go module
- [ ] OTel full span hierarchy wired in proxy (see ADR-005)
- [ ] OTel export config (OTLP gRPC to collector endpoint)
- [ ] `.github/workflows/release.yml` — GoReleaser: multi-arch binaries + Docker push + npm publish
- [ ] `docs/` — documentation source (Docusaurus) with:
  - Quickstart (Docker Compose)
  - Kubernetes deployment guide
  - SDK reference (TypeScript + Go)
  - Plugin development guide

**Done when:**
- `helm install conduit ./helm` deploys full stack on local k3d cluster
- `npm install @conduit/sdk` + 10 lines of code connects to gateway
- Every trace has full span hierarchy visible in Jaeger
- `git tag v0.2.0` triggers automated release with binaries on GitHub

---

### PHASE 8 — Enterprise Features (Weeks 29–40)

**Goal:** Features that convert enterprise evaluations into paying contracts. EU AI Act compliance, SSO, WORM audit, cost budgets, SIEM integrations.

**Sub-phases:**

**P8a (Weeks 29–32): Cost & SIEM**
- [ ] `internal/cost/estimator.go` — per-model token cost estimation
- [ ] `internal/cost/budget.go` — pre-flight budget check (reject if exhausted)
- [ ] `migrations/000005_cost_budgets.up.sql` — cost_budgets table
- [ ] Cost analytics dashboard page (spend by tenant/tool/period, burn rate chart)
- [ ] SIEM integrations (webhook-based push):
  - Splunk HEC (`/services/collector/event`)
  - Elastic Beats (HTTP output)
  - Datadog Events API
- [ ] `internal/plugin/builtin/siem_exporter.go` — pushes audit events to configured SIEM

**P8b (Weeks 33–36): SSO & WORM**
- [ ] SAML 2.0 SP implementation (crewjam/saml)
- [ ] OIDC federation (coreos/go-oidc) — Okta, Azure AD, Google Workspace
- [ ] IdP group → Conduit RBAC role mapping (via claims config)
- [ ] SSO configuration UI in dashboard
- [ ] `migrations/000006_sso.up.sql` — sso_configs, sso_sessions tables
- [ ] WORM audit mode:
  - S3 Object Lock integration (boto3 via sidecar or Go aws-sdk)
  - Immutable partition policy (PostgreSQL row-level security, no DELETE)
  - Tamper-evident hash chain (each event references SHA-256 of previous)

**P8c (Weeks 37–40): Compliance & Air-Gapped**
- [ ] EU AI Act compliance report generator:
  - One-click PDF export (uses ReportLab-equivalent in Go: wkhtmltopdf or chromedp)
  - Maps audit events to Art. 12, 13, 14, 15 obligations
  - Date range selector, tenant filter
- [ ] SOC 2 evidence package:
  - CC6.1–CC9.2 evidence auto-generated from audit log + config
  - Downloadable ZIP with timestamped reports
- [ ] Air-gapped mode:
  - `conduit.yaml: air_gapped: true`
  - No external HTTP calls (OTel, webhooks, SIEM disabled)
  - Self-contained Docker image with embedded assets (no CDN)
- [ ] Python SDK (`pip install conduit-sdk`):
  - LangChain + AutoGen + CrewAI integration helpers
- [ ] Tenant self-service portal (tenants manage own keys, servers, limits)

**Done when:**
- EU AI Act audit report generated in <60 seconds
- SAML SSO login working with Okta test IdP
- Air-gapped deployment passes: no outbound connections in `tcpdump`

---

### PHASE 9 — OSS-Ready Launch & Community (Weeks 41–48)

**Goal:** Polish to category leadership. v1.0.0 stable release, security audit, CNCF Sandbox application, full docs, plugin registry, community infrastructure.

**Deliverables:**
- [ ] Third-party security audit (Cure53 or Trail of Bits) — fix all critical/high findings
- [ ] CVE disclosure process + security advisory template
- [ ] v1.0.0 release — stable API guarantee, semver commitment, LTS policy documented
- [ ] `docs.conduit.io` — full Docusaurus site:
  - Getting Started (Docker Compose)
  - Kubernetes deployment
  - Plugin development guide (Go + HTTP callback)
  - API reference (OpenAPI embedded)
  - SDK reference (TypeScript + Go + Python)
  - Architecture deep-dive
  - MCP spec compatibility matrix
  - Integration guides: LangChain, AutoGen, CrewAI, Claude, LiteLLM
  - EU AI Act compliance guide
  - SOC 2 guide
  - Upgrade guide (v0.x → v1.0)
- [ ] Plugin registry (`conduit.io/plugins`):
  - Submission process (PR to conduit-oss/plugin-registry)
  - Each accepted plugin gets a registry page + blog mention
- [ ] CNCF Landscape submission + CNCF Sandbox application
- [ ] MCP spec compatibility matrix (maintained, versioned)
- [ ] 20+ GitHub issues tagged "good first issue"
- [ ] Discord server: #general, #help, #showcase, #plugin-development, #roadmap
- [ ] Monthly community call (async-friendly, recorded to YouTube)
- [ ] Conference talk CFPs submitted: KubeCon, AI Engineer Summit, GopherCon
- [ ] LTS release cadence: minor releases every 6 weeks, LTS every 6 months

**Launch sequence:**
1. Security audit complete + findings fixed
2. v1.0.0 tagged → GoReleaser publishes binaries + Docker image
3. docs.conduit.io live
4. HN: "Show HN: Conduit — production MCP gateway, v1.0 stable"
5. Reddit: r/golang, r/MachineLearning, r/devops
6. Twitter/X thread with architecture diagram + demo GIF
7. CNCF Sandbox application submitted
8. Blog post: "We built the Kong for AI agents. Here's what we learned."

**Done when:**
- v1.0.0 released on GitHub with multi-arch binaries
- docs.conduit.io live and indexed by Google
- CNCF Sandbox application submitted
- 500+ Discord members within 2 weeks of launch

---

## 10. Configuration Reference

```yaml
# conduit.yaml — complete reference
server:
  port: 8080
  management_port: 8081
  metrics_port: 9090
  tls:
    enabled: false
    cert_file: /certs/tls.crt
    key_file: /certs/tls.key
  timeouts:
    read: 30s
    write: 60s
    idle: 120s
    upstream: 30s

database:
  url: "postgres://conduit:password@localhost:5432/conduit?sslmode=disable"
  max_open_conns: 25
  max_idle_conns: 5
  conn_max_lifetime: 5m

redis:
  url: "redis://localhost:6379/0"
  pool_size: 10

auth:
  jwt_secret: "${JWT_SECRET}"
  access_token_ttl: 1h
  refresh_token_ttl: 720h
  api_key_prefix: "cnd_"

rate_limiting:
  enabled: true
  default_per_tenant: 1000   # requests/minute
  burst_multiplier: 1.5
  fail_open: true            # allow if Redis unavailable

audit:
  enabled: true
  buffer_size: 10000
  flush_interval: 1s
  redact_args: false
  retention_days: 90

observability:
  otel_endpoint: "http://otel-collector:4317"
  log_level: "info"
  log_format: "json"
  trace_sampling_rate: 1.0

plugins:
  dir: "/etc/conduit/plugins"
  http_timeout: 5s

policy:
  file: "/etc/conduit/policies.yaml"
  reload_interval: 10s

webhooks:
  max_retries: 5
  retry_backoff: "1s,5s,30s,5m,30m"
  timeout: 10s
```

---

## 11. Policy File Format

```yaml
# /etc/conduit/policies.yaml
version: "1"
rules:
  - name: block-destructive-github-tools
    match:
      tool_name: "github/delete_*"
    action: deny
    message: "Destructive GitHub operations are blocked by policy"

  - name: restrict-salesforce-reports
    match:
      tool_name: "salesforce/generate_report"
      tenant_id: "acme-corp"
    action: rate_limit
    rate_limit:
      requests: 5
      window: 1h

  - name: production-env-readonly
    match:
      tool_name: "*"
      agent_tag: "env:production"
    action: deny
    except:
      tool_name_prefix: "*.read_*"
```

---

## 12. Performance Targets (Non-Negotiable)

| Metric | Target | Measurement |
|---|---|---|
| P50 proxy overhead | <0.3ms | k6, 1K RPS |
| P99 proxy overhead | <1ms | k6, 1K RPS |
| P99 at 5K RPS | <2ms | k6, 5K RPS |
| Auth middleware (cache hit) | <0.1ms | Isolated benchmark |
| Rate limit check | <0.2ms | Isolated benchmark |
| Policy evaluation | <0.1ms | In-process benchmark |
| Audit writes | Non-blocking | Proxy latency unchanged |
| Memory at idle | <64MB RSS | docker stats |
| Memory at 1K RPS | <256MB RSS | docker stats |
| Docker image size | <20MB | docker image ls |
| Startup time | <2s | Time to /readyz 200 |

---

## 13. Environment Variables

```bash
# Required in production
JWT_SECRET=<64-char random string>
DATABASE_URL=postgres://conduit:password@host:5432/conduit
REDIS_URL=redis://host:6379/0

# Optional overrides
CONDUIT_PORT=8080
CONDUIT_MANAGEMENT_PORT=8081
CONDUIT_METRICS_PORT=9090
CONDUIT_LOG_LEVEL=info
CONDUIT_LOG_FORMAT=json
CONDUIT_OTEL_ENDPOINT=http://otel-collector:4317
CONDUIT_CONFIG=/etc/conduit/conduit.yaml

# Enterprise only
CONDUIT_WORM_S3_BUCKET=conduit-audit-worm
CONDUIT_WORM_S3_REGION=us-east-1
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
```

---

## 14. Makefile Targets

```makefile
make build          # Build binary to ./bin/conduit
make run            # Run with ./conduit.yaml
make test           # Unit tests
make test-int       # Integration tests (requires Docker)
make test-e2e       # Playwright E2E tests
make test-load      # k6 load test (requires running stack)
make lint           # golangci-lint
make docker-build   # Build Docker image
make docker-run     # docker compose up -d
make migrate-up     # Run pending migrations
make migrate-down   # Rollback last migration
make generate       # go generate (mocks, OTel, swaggo)
make release        # GoReleaser dry-run
make fuzz           # Run fuzz tests (60 seconds)
make security       # govulncheck + trivy
```

---

## 15. Go Module Dependency Budget

Keep dependencies lean. Every new dependency requires justification.

**Approved dependencies (core):**
```go
require (
    github.com/jackc/pgx/v5               // PostgreSQL driver
    github.com/redis/go-redis/v9           // Redis client
    github.com/golang-migrate/migrate/v4   // DB migrations
    github.com/spf13/cobra                 // CLI
    github.com/spf13/viper                 // Config
    github.com/go-chi/chi/v5              // HTTP router (management API)
    github.com/rs/zerolog                  // Structured logging
    github.com/prometheus/client_golang    // Metrics
    go.opentelemetry.io/otel              // Tracing
    github.com/lestrrat-go/jwx/v2         // JWT handling
    golang.org/x/crypto                   // bcrypt
    github.com/fsnotify/fsnotify          // Policy hot-reload
    gopkg.in/yaml.v3                      // Policy YAML parsing
)

require (
    github.com/testcontainers/testcontainers-go  // Integration tests only
    github.com/stretchr/testify                  // Test assertions
)
```

**Not allowed without discussion:**
- ORM (use raw pgx queries — auditability matters)
- `gorilla/mux` (use chi)
- Any ML library in core proxy

---

## 16. Commit Message Convention

```
type(scope): description

Types: feat, fix, perf, refactor, test, docs, chore, security
Scopes: proxy, auth, ratelimit, audit, policy, plugin, api, cli, dashboard, sdk, helm, infra

Examples:
feat(proxy): add SSE streaming support for MCP protocol
fix(ratelimit): correct token bucket refill calculation on edge case
perf(audit): batch PostgreSQL inserts to reduce write overhead
security(auth): use constant-time comparison for API key validation
test(ratelimit): add integration test with real Redis
docs(cli): add --json flag documentation for all commands
```

---

## 17. Current Working Session

**Last updated:** 2026-07-03

**What's been done:**
- Full research and competitive analysis completed
- Complete PRD generated (66-page PDF: `Conduit_PRD.pdf`)
- Build roadmap defined (9 phases, 48 weeks)
- This CLAUDE.md created
- **Phase 1 complete**: `internal/mcp` (types, parser, fuzz test — 500k+ execs clean),
  `internal/config` (viper loader + env overrides + validation), `internal/proxy`
  (reverse proxy, SSE streaming, 7-step middleware chain), `internal/tenant`
  (in-memory router), `internal/plugin` (ConduitPlugin interface + Registry,
  pulled forward from Phase 6 since proxy.go depends on it), `internal/audit`
  (buffered non-blocking Writer + LogSink, pulled forward from Phase 3 since
  proxy.go depends on it — PostgreSQL-backed Sink lands in Phase 3),
  `cmd/conduit` (cobra CLI: `proxy start`, `version`). All tests pass
  (`go test -race ./...`), verified end-to-end manually: JSON tool calls and
  SSE-streamed tool calls both proxy correctly, `/healthz` and `/readyz` work.
  OSS boilerplate (README, LICENSE, CONTRIBUTING, SECURITY, CODE_OF_CONDUCT,
  .golangci.yaml, CI workflow) in place.

- **Phase 2 complete**: `migrations/000001_initial_schema` (tenants, api_keys,
  mcp_servers, rate_limit_configs) embedded into the binary via `migrations/embed.go`
  and applied through `conduit migrate --db-url`. `internal/store` (db.go, tenants.go,
  apikeys.go, servers.go, ratelimits.go) — hand-written pgx/v5 queries, sentinel errors
  (ErrNotFound/ErrConflict), no ORM. `internal/auth` (API key generation/hashing,
  Redis-cached validator with PostgreSQL fallback, constant-time hash comparison, JWT
  validator stubbed until Phase 5). `internal/ratelimit` (Redis Lua token bucket,
  4-scope priority check: tool > server > agent > tenant, 30s in-process config cache,
  fail-open/fail-closed both covered). `internal/tenant/store.go` (5s-refresh
  DB-backed routing table). `internal/proxy` gained a functional-options pattern
  (`WithAuthMiddleware`, `WithRateLimitMiddleware`, `WithReadyChecker`) so Phase 1's
  no-op middleware slots can be swapped for the real ones without changing the
  middleware chain's shape. `cmd/conduit` gained `migrate` and wires the full stack
  when PostgreSQL/Redis are reachable, falling back to Phase 1's demo-flag mode
  otherwise. Verified end-to-end against real local PostgreSQL 15 and Redis (no
  Docker available in this session — used native `pg_ctl`/`redis-server` instead):
  missing/invalid API key → 401, valid key → 200 proxied, exceeding a 3-req/min
  limit → 429 with `Retry-After`, `/readyz` reports postgres/redis/routing all "ok".
  Integration test suite (testcontainers-go, `internal/store` + `internal/auth`,
  build-tagged `integration`) also run and passed against the same local instances via
  `TEST_DATABASE_URL`/`TEST_REDIS_URL` — caught and fixed a real NOT NULL constraint
  bug in `MCPServerStore.Create` along the way. Unit tests for `internal/ratelimit`
  use miniredis (real Lua script execution, no network). `go test -race ./...` clean,
  `gofmt`/`go vet` clean.

**Next action:**
- Start Phase 3: audit log persistence (PostgreSQL-backed Sink replacing LogSink),
  full CLI (`conduit audit tail/query/export`, `apikey create/list/revoke`),
  Prometheus metrics, Docker packaging (MVP milestone)
- Read `spec/07-audit.md`, `spec/08-cli.md`, `spec/09-observability.md` first

---

## 18. Useful References

- MCP Specification: https://spec.modelcontextprotocol.io/
- MCP GitHub: https://github.com/modelcontextprotocol
- Linux Foundation MCP: https://lfaidata.foundation/projects/mcp/
- MCP SDK (TypeScript): https://github.com/modelcontextprotocol/typescript-sdk
- MCP SDK (Python): https://github.com/modelcontextprotocol/python-sdk
- Kong Architecture: https://docs.konghq.com/gateway/latest/
- Traefik Middleware: https://doc.traefik.io/traefik/middlewares/overview/
- CNCF Sandbox Process: https://github.com/cncf/toc/blob/main/process/sandbox.md
- EU AI Act text: https://eur-lex.europa.eu/legal-content/EN/TXT/?uri=CELEX:32024R1689
- OAuth 2.0 RFC 6749: https://www.rfc-editor.org/rfc/rfc6749
- PKCE RFC 7636: https://www.rfc-editor.org/rfc/rfc7636
- JSON-RPC 2.0 spec: https://www.jsonrpc.org/specification
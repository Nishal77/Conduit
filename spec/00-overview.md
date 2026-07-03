# Conduit Spec — How to Use This Folder

> **For AI assistants:** Read the relevant spec file before writing any code for that subsystem.
> Every spec file is self-contained and implementation-ready. Follow it exactly.
> When a spec says "MUST", there is no alternative. When it says "SHOULD", use judgement.

---

## What Is This Folder?

`spec/` contains the complete technical specification for every subsystem in Conduit.
Each file maps to one or more build phases. You should read the spec file before
implementing, and check off deliverables in `CLAUDE.md` as you complete them.

## File Index

| File | Subsystem | Phase |
|---|---|---|
| `01-mcp-protocol.md` | MCP message types, JSON-RPC 2.0, SSE session lifecycle | P1 |
| `02-proxy.md` | Core reverse proxy, SSE streaming, middleware chain | P1 |
| `03-config.md` | YAML config loader, env var overrides, validation | P1 |
| `04-database.md` | Full PostgreSQL schema, indexes, partitions, migrations | P2 |
| `05-auth.md` | API key hashing, Redis cache, auth middleware | P2 |
| `06-ratelimit.md` | Token bucket, Redis Lua script, rate limit middleware | P2 |
| `07-audit.md` | Append-only audit writer, batch insert, query API, live tail | P3 |
| `08-cli.md` | All cobra commands, flags, output formats | P3 |
| `09-observability.md` | Prometheus metrics, OTel spans, zerolog structured logging | P3 |
| `10-api.md` | REST management API — all 35+ endpoints, request/response shapes | P4 |
| `11-dashboard.md` | Next.js 15 app structure, pages, components, WebSocket feed | P4 |
| `12-oauth.md` | OAuth 2.0 authorization code + PKCE + client credentials | P5 |
| `13-multitenant.md` | Tenant routing table, isolation rules, weighted pool | P5 |
| `14-plugins.md` | ConduitPlugin interface, registry, HTTP callback protocol | P6 |
| `15-policy.md` | Policy YAML DSL, engine compilation, hot-reload | P6 |
| `16-webhooks.md` | Webhook events, delivery, retry with exponential backoff | P6 |
| `17-kubernetes.md` | Helm chart structure, CRD definitions, HPA, operator | P7 |
| `18-sdk.md` | TypeScript SDK (@conduit/sdk) and Go SDK (sdk/go) | P7 |
| `19-testing.md` | Unit test patterns, integration test setup, fuzz tests, k6 | All |
| `20-security.md` | Credential storage, transport security, input validation | All |

---

## How AI Should Use These Specs

### Before writing any file:
1. Read `CLAUDE.md` (project root) for the architecture and phase context.
2. Read the relevant spec file(s) for the subsystem you are building.
3. Follow the exact function signatures, struct fields, and error codes specified.
4. Do not invent alternatives unless a spec says "implementation detail".

### Naming conventions:
- Go packages: `lowercase`, one word, matching directory name
- Go exported types: `PascalCase`
- Go unexported: `camelCase`
- Go errors: `ErrXxx` for sentinel errors, `XxxError` for error types
- SQL identifiers: `snake_case`
- JSON keys: `snake_case`
- HTTP headers: `Title-Case`
- TypeScript types: `PascalCase`
- TypeScript variables: `camelCase`

### File path conventions:
```
conduit/
  cmd/conduit/main.go          ← binary entry point
  internal/<pkg>/<file>.go     ← private packages
  pkg/sdk/                     ← public Go SDK
  migrations/000NNN_<name>.sql ← numbered migrations
  dashboard/                   ← Next.js app root
  helm/                        ← Helm chart root
  sdk/typescript/src/          ← TypeScript SDK source
```

### Error handling rules:
- NEVER use `panic` except in `main()` during startup validation
- ALWAYS wrap errors with `fmt.Errorf("context: %w", err)`
- Return sentinel errors for known conditions (`ErrNotFound`, `ErrUnauthorized`)
- Log at `warn` for handled errors, `error` for unexpected conditions
- HTTP handlers MUST return JSON error bodies: `{"error": "message", "code": "ERROR_CODE"}`

### Context propagation rules:
- Every function that does I/O MUST accept `ctx context.Context` as first argument
- Pass `tenant_id`, `trace_id`, and `request_id` via context using typed keys
- Never store tenant_id in a package-level variable

---

## Build Order

Always build in phase order. Each phase depends on the previous:

```
P1 (proxy) → P2 (auth+db) → P3 (audit+cli) → P4 (api+ui)
→ P5 (oauth+routing) → P6 (plugins+policy) → P7 (k8s+sdk)
→ P8 (enterprise) → P9 (launch)
```

Within a phase, build in this order:
1. Database migrations (SQL files)
2. Store layer (PostgreSQL queries)
3. Business logic (internal packages)
4. Middleware (HTTP layer)
5. CLI commands
6. Tests

---

## Versioning Contract

- `v0.x.y` — pre-stable; breaking changes allowed between minors
- `v1.0.0` — stable; semver guarantees apply
- Management REST API is versioned: `/api/v1/...`
- Proxy endpoints are NOT versioned (they are transparent MCP proxies)
- Plugin HTTP callback protocol is versioned: `X-Conduit-Plugin-Version: 1`

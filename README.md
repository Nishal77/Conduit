# Conduit

**Conduit is the production gateway for the Model Context Protocol (MCP).**

Kong / Traefik for AI agent tool calls — Conduit sits between AI agents and
your upstream MCP servers, enforcing authentication, rate limiting, policy,
and audit logging on every tool call, with sub-millisecond overhead.

> Status: pre-alpha. Phases 1–4 are implemented: core reverse proxy, SSE
> streaming, PostgreSQL-backed routing, API key authentication, Redis rate
> limiting, persisted audit log, the full CLI, Prometheus metrics,
> OpenTelemetry tracing, Docker packaging, a REST management API, and a
> Next.js dashboard. Phases 5–9 (OAuth, plugins, Kubernetes, enterprise
> features) are in progress. See [CLAUDE.md](claude.md) for the full build
> plan and phase status.

## Dashboard

```bash
cd dashboard
npm install
npm run dev
```

Open `http://localhost:3000`, sign in with the management API URL
(`http://localhost:8081` by default), a tenant slug, and an API key created
via `conduit apikey create`. See [spec/11-dashboard.md](spec/11-dashboard.md)
for the full page list.

## Why

There are 97M+ monthly MCP SDK downloads and 12,000+ MCP servers, and zero
production-ready open-source gateways. Every team deploying AI agents ends
up building auth, rate limiting, and audit logging themselves, or running
without any of it. Conduit fills that gap.

## Architecture

```
AI Agent (Claude / GPT / LangChain)
         │  MCP JSON-RPC 2.0 over SSE
         ▼
┌─────────────────────────────────────────────────────┐
│                CONDUIT PROXY (:8080)                 │
│  RequestID → Logging → Recovery → Auth → RateLimit    │
│  → Policy → Plugin.Before → forward → Plugin.After    │
│  → Audit (async)                                      │
└─────────────────────────────────────────────────────┘
         │  tenant_id → routing table
         ▼
┌──────────────────────────────────┐
│   Upstream MCP Server Pool        │
│   github-mcp  stripe-mcp  ...     │
└──────────────────────────────────┘
```

## Quickstart

### Docker Compose (full stack: proxy + PostgreSQL + Redis)

```bash
export JWT_SECRET=$(openssl rand -hex 32)
docker compose -f docker/docker-compose.yml up -d
```

This builds the image, runs migrations, and starts Conduit on `:8080`
(proxy), `:8081` (management API, Phase 4), and `:9090` (Prometheus
metrics). Register a tenant and an upstream server, then issue an API key:

```bash
docker compose -f docker/docker-compose.yml exec postgres \
  psql -U conduit -c "INSERT INTO tenants (slug, name) VALUES ('acme', 'Acme Inc');"
docker compose -f docker/docker-compose.yml exec postgres \
  psql -U conduit -c "INSERT INTO mcp_servers (tenant_id, name, upstream_url)
    SELECT id, 'github', 'http://your-upstream:3001' FROM tenants WHERE slug='acme';"

./bin/conduit apikey create --name demo --tenant acme --config conduit.yaml

curl -X POST http://localhost:8080/mcp/acme/github \
  -H "Authorization: Bearer <key from above>" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

### Local build (no database — quick pass-through test only)

Requires Go 1.23+.

```bash
export JWT_SECRET=$(openssl rand -hex 32)
make run
./bin/conduit proxy start \
  --demo-tenant acme --demo-server github --demo-upstream http://localhost:3001

curl -X POST http://localhost:8080/mcp/acme/github \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

Without PostgreSQL/Redis reachable, Conduit falls back to this
no-auth/no-rate-limit compatibility mode — `/readyz` will report `postgres`
and `redis` as unavailable, which is expected here.

`/mcp/{tenant_slug}/{server_name}` is the transparent proxy endpoint;
`/healthz` and `/readyz` are liveness/readiness probes; `/metrics` (on the
metrics port) is Prometheus-scrapable.

### CLI

```bash
./bin/conduit migrate --db-url "$DATABASE_URL"       # apply schema
./bin/conduit apikey create --name ci --tenant acme  # prints the raw key once
./bin/conduit apikey list --tenant acme --json
./bin/conduit audit tail --tenant acme                # live-tails new tool calls
./bin/conduit audit query --tenant acme --from 24h --output csv
./bin/conduit version --json
```

## Development

```bash
make build      # build ./bin/conduit
make test       # unit tests
make test-race  # unit tests with the race detector
make test-int   # integration tests against real PostgreSQL + Redis (Docker or TEST_DATABASE_URL/TEST_REDIS_URL)
make lint       # golangci-lint
make fuzz       # 60s fuzz run of the MCP parser
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full contributor workflow and
[spec/](spec/) for the implementation-ready technical specification of every
subsystem.

## License

Apache 2.0 — see [LICENSE](LICENSE).

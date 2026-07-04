# Conduit

**Conduit is the production gateway for the Model Context Protocol (MCP).**

Kong / Traefik for AI agent tool calls — Conduit sits between AI agents and
your upstream MCP servers, enforcing authentication, rate limiting, policy,
and audit logging on every tool call, with sub-millisecond overhead.

> Status: pre-alpha. Phases 1–2 are implemented: core reverse proxy, SSE
> streaming, PostgreSQL-backed routing, API key authentication, and Redis
> rate limiting. Phases 3–9 (audit persistence, dashboard, OAuth, plugins,
> Kubernetes, enterprise features) are in progress. See
> [CLAUDE.md](claude.md) for the full build plan and phase status.

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

Requires Go 1.23+.

```bash
git clone https://github.com/conduit-oss/conduit
cd conduit
export JWT_SECRET=$(openssl rand -hex 32)
make run
```

This starts the proxy on `:8080` using [conduit.yaml](conduit.yaml). Register
a development route and send a tool call through it:

```bash
./bin/conduit proxy start \
  --demo-tenant acme --demo-server github --demo-upstream http://localhost:3001

curl -X POST http://localhost:8080/mcp/acme/github \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

`/mcp/{tenant_slug}/{server_name}` is the transparent proxy endpoint;
`/healthz` and `/readyz` are liveness/readiness probes.

Full multi-tenant routing (backed by PostgreSQL), authentication, and rate
limiting land in Phase 2 — see the build roadmap in [CLAUDE.md](claude.md)
§9.

## Development

```bash
make build   # build ./bin/conduit
make test    # unit tests
make lint    # golangci-lint
make fuzz    # 60s fuzz run of the MCP parser
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full contributor workflow and
[spec/](spec/) for the implementation-ready technical specification of every
subsystem.

## License

Apache 2.0 — see [LICENSE](LICENSE).

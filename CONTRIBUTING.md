# Contributing to Conduit

Thanks for considering a contribution. Conduit is built in phases (see
[CLAUDE.md](claude.md) §9) — check the phase status table before starting
work so you're not duplicating something already in flight.

## Development setup

```bash
git clone https://github.com/conduit-oss/conduit
cd conduit
go mod download
make build
make test
```

Go 1.23+ is required. No database or Redis is needed to work on Phase 1
code (the core proxy); later phases document their own local dependencies
in `docker/docker-compose.yml`.

## Before opening a PR

- `make test` and `make lint` must pass locally.
- New packages that do I/O need tests; if you're touching `internal/mcp`,
  extend the fuzz corpus in `parser_fuzz_test.go` rather than only adding a
  table test.
- Follow the conventions in [spec/00-overview.md](spec/00-overview.md) —
  exported Go types are `PascalCase`, errors are wrapped with
  `fmt.Errorf("context: %w", err)`, and every I/O function takes
  `ctx context.Context` as its first argument.
- Keep commit messages in the format described in CLAUDE.md §16:
  `type(scope): description` (e.g. `fix(proxy): handle empty SSE body`).

## Reporting bugs

Open a GitHub issue with steps to reproduce. For security vulnerabilities,
see [SECURITY.md](SECURITY.md) instead of filing a public issue.

## Code of Conduct

This project follows the [Code of Conduct](CODE_OF_CONDUCT.md). By
participating, you agree to abide by it.

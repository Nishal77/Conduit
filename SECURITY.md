# Security Policy

## Reporting a Vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Instead, email **security@conduit-oss.dev** (or use GitHub's private
[Security Advisories](https://github.com/conduit-oss/conduit/security/advisories)
feature) with:

- A description of the vulnerability and its impact
- Steps to reproduce (a minimal repro is ideal)
- Any relevant logs, config, or PoC code

We aim to acknowledge reports within 3 business days and to ship a fix or
mitigation within 90 days, coordinating disclosure timing with you.

## Supported Versions

Until v1.0.0 ships, only the latest `v0.x` release receives security fixes.
After v1.0.0, see the LTS policy in [CLAUDE.md](claude.md) §9 (Phase 9).

## Scope

In scope: the Conduit proxy, management API, CLI, TypeScript/Go SDKs, and
Helm chart in this repository. Out of scope: upstream MCP servers you
connect Conduit to, and third-party plugins not maintained in this
repository.

See [spec/20-security.md](spec/20-security.md) for the design-level security
requirements (credential storage, transport security, input validation)
every subsystem is built against.

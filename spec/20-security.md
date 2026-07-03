# Spec 20 — Security

> Phase: All | Apply from day one

---

## 1. Credential Storage Rules

| Credential | Storage | Never |
|---|---|---|
| API Key (raw) | Never stored — returned once at creation | Log, store, return after creation |
| API Key (hash) | `SHA-256(raw_key)`, hex-encoded in PostgreSQL | Store raw key |
| JWT Secret | Environment variable `JWT_SECRET` | Config file, logs, error messages |
| OAuth `client_secret` | `bcrypt(cost=12)` in PostgreSQL | Store plaintext |
| Upstream auth tokens | AES-256-GCM encrypted in `mcp_servers.auth_config` | Store plaintext in any field |
| Refresh tokens | `SHA-256(raw_token)` in PostgreSQL | Store raw token |
| Database URL | Environment variable `DATABASE_URL` | Config file checked into git |
| Redis URL | Environment variable `REDIS_URL` | Config file checked into git |

### Upstream Auth Config Encryption

```go
// internal/store/crypto.go
package store

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "io"
)

// EncryptJSON encrypts a map to AES-256-GCM ciphertext.
// Returns base64url-encoded "{nonce}{ciphertext}".
// Key: 32 bytes from CONDUIT_ENCRYPTION_KEY env var.
func EncryptJSON(data map[string]any, key []byte) (string, error)

// DecryptJSON decrypts AES-256-GCM ciphertext to a map.
func DecryptJSON(ciphertext string, key []byte) (map[string]any, error)
```

---

## 2. Transport Security

### TLS Configuration

```go
// When TLS is enabled (config.Server.TLS.Enabled):
tlsCfg := &tls.Config{
    MinVersion: tls.VersionTLS12,
    // Prefer TLS 1.3 cipher suites (auto-selected by Go 1.23)
    // Disable weak cipher suites:
    CipherSuites: []uint16{
        tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
        tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
        tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
        tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
    },
    // Require client cert for management API (optional, enterprise feature)
}
```

### Security Headers

Set on all HTTP responses from both proxy and management API:

```go
// internal/api/middleware/security_headers.go
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("X-XSS-Protection", "1; mode=block")
        w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
        w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
        // Only set CSP on management API (not proxy — SSE clients may need framing)
        if isManagementAPI(r) {
            w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'")
        }
        next.ServeHTTP(w, r)
    })
}
```

---

## 3. Input Validation

### Request Size Limits

```go
// On the proxy HTTP server:
server := &http.Server{
    Handler: http.MaxBytesHandler(handler, 10*1024*1024), // 10MB max request body
    ReadHeaderTimeout: 10 * time.Second,
    WriteTimeout:      cfg.Server.Timeouts.Write,
    IdleTimeout:       cfg.Server.Timeouts.Idle,
}
```

### MCP Message Validation

```go
// In parser.go — strict validation:
// 1. JSON must be valid (no trailing data, no control characters outside strings)
// 2. jsonrpc field must be exactly "2.0" (string comparison)
// 3. method (if present) must be a non-empty string, max 256 chars
// 4. id (if present) must be string, number, or null — not array or object
// 5. params (if present) must be object or array — not primitive
// 6. Total message size check is enforced by the SSE line scanner (10MB max)
```

### Management API Validation

```go
// All POST/PATCH/PUT request bodies are validated before handler logic:
// - Slug format: ^[a-z0-9-]{3,64}$
// - URLs: must parse with net/url.Parse and have http or https scheme
// - Port numbers: must be in [1, 65535]
// - Enums: validated against allowlist
// - String lengths: all text fields have max length (usually 256 or 2048)
```

---

## 4. Authentication Security

```go
// 1. Constant-time comparison for all security-sensitive comparisons
//    (see spec 05-auth.md section 7)

// 2. API key lookup must NOT be vulnerable to timing attacks:
//    - Key validation takes the SAME time whether key exists or not
//    - Use constant-time comparison on the hash bytes AFTER DB lookup

// 3. JWT signature verification: use lestrrat-go/jwx/v2 (well-audited library)
//    - Explicitly set algorithm to HS256 (never allow "none")
//    - Check exp before returning success

// 4. bcrypt cost for OAuth client_secret: MUST be >= 12
//    - cost=12 adds ~300ms latency to OAuth token exchange — acceptable
//    - cost=14 is more secure but adds ~1.2s — use for enterprise tier

// 5. Rate limit auth endpoint:
//    - 10 failed auth attempts per IP per minute → 429
//    - Tracked in Redis: "authlimit:{ip}" → counter, TTL 60s
```

---

## 5. SQL Injection Prevention

```go
// ALWAYS use parameterized queries with pgx:

// WRONG (never do this):
query := "SELECT * FROM tenants WHERE slug = '" + slug + "'"

// RIGHT:
row := pool.QueryRow(ctx, "SELECT * FROM tenants WHERE slug = $1", slug)

// NEVER use fmt.Sprintf to build SQL queries
// NEVER concatenate user input into SQL
// Dynamic ORDER BY: use an allowlist of column names, never interpolate directly
```

---

## 6. Secret Scanning Prevention

Add to `.gitignore`:
```
.env
.env.*
*.pem
*.key
conduit.yaml.local
secrets/
```

Add `.github/workflows/security.yml`:
```yaml
name: Security

on: [push, pull_request]

jobs:
  scan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Secret scanning (gitleaks)
        uses: gitleaks/gitleaks-action@v2
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}

      - name: Vulnerability scan (govulncheck)
        run: |
          go install golang.org/x/vuln/cmd/govulncheck@latest
          govulncheck ./...

      - name: Container scan (trivy)
        uses: aquasecurity/trivy-action@master
        with:
          image-ref: conduit/conduit:latest
          severity: CRITICAL,HIGH
          exit-code: 1
```

---

## 7. Audit Log Integrity

The audit log is append-only at the application level. For enterprise WORM compliance:

```go
// Hash chain: each audit event references the hash of the previous event
// Implemented in Phase 8 (P8b).
// Algorithm:
//   event.previous_hash = SHA-256(previous_event_json)
//   event.event_hash    = SHA-256(event_json_with_previous_hash)
//
// This provides tamper-evidence: any modification to a past event breaks the chain.
// Verifiable by an external auditor with access to the raw event data.
```

---

## 8. Dependency Security

```makefile
# Run before every release:
security:
    go install golang.org/x/vuln/cmd/govulncheck@latest
    govulncheck ./...
    # Audit npm dependencies
    cd sdk/typescript && npm audit --audit-level=high
    cd dashboard && npm audit --audit-level=high
```

### Approved dependencies only (see spec 03-config.md section 5)

Never add a dependency without:
1. Checking it in `govulncheck` output
2. Reviewing the library's GitHub issues for recent security CVEs
3. Checking the library has active maintenance (commits in last 90 days)

---

## 9. Disclosure Policy

In `SECURITY.md`:

```markdown
# Security Policy

## Reporting a Vulnerability

Email security@conduit.io with:
- Description of the vulnerability
- Steps to reproduce
- Potential impact

We will respond within 48 hours and aim to patch critical vulnerabilities within 7 days.

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.x     | ✅ Yes     |
| 0.x     | ❌ No      |
```

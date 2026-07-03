# Spec 05 — Authentication

> Phase: P2 | Files: `internal/auth/apikey.go`, `internal/auth/middleware.go`, `internal/auth/jwt.go`

---

## 1. Overview

Conduit supports two authentication methods:
- **API Key** — `Authorization: Bearer cnd_<key>` — used by machine agents and CLI
- **JWT Bearer** — `Authorization: Bearer <jwt>` — issued by Conduit's OAuth server (Phase 5)

Both methods MUST extract `tenant_id` from the validated token, NEVER from request parameters.

Authentication overhead targets:
- Cache hit (Redis): **<0.1ms**
- Cache miss (PostgreSQL): **<5ms**

---

## 2. API Key Format

```
cnd_<base64url(32 random bytes)>
     └─ 43 characters (no padding)
Total: 47 characters (prefix "cnd_" + 43)

Example: cnd_4MqyH3xK9vRwP2nL8tFjD6eAuCbGmZsN1oYiXWE

Storage rule:
  key_hash   = hex(SHA-256(raw_key))   — stored in database
  key_prefix = raw_key[:12]            — stored for display only
```

Generation:
```go
// GenerateAPIKey creates a new API key.
// Returns (rawKey, keyHash, keyPrefix).
func GenerateAPIKey() (rawKey, keyHash, keyPrefix string, err error) {
    buf := make([]byte, 32)
    if _, err = rand.Read(buf); err != nil {
        return
    }
    rawKey = "cnd_" + base64.RawURLEncoding.EncodeToString(buf)
    sum := sha256.Sum256([]byte(rawKey))
    keyHash = hex.EncodeToString(sum[:])
    keyPrefix = rawKey[:12]
    return
}
```

---

## 3. API Key Validation — `internal/auth/apikey.go`

```go
package auth

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "errors"
    "fmt"
    "strings"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/conduit-oss/conduit/internal/store"
)

// ErrInvalidAPIKey is returned when the key is malformed, revoked, or unknown.
var ErrInvalidAPIKey = errors.New("invalid API key")

// ErrExpiredAPIKey is returned when the key has passed its expires_at.
var ErrExpiredAPIKey = errors.New("API key expired")

// APIKeyValidator validates API keys using Redis cache + PostgreSQL fallback.
type APIKeyValidator struct {
    redis    *redis.Client
    keyStore *store.APIKeyStore
    cacheTTL time.Duration  // from config: auth.api_key_cache_ttl (default 5m)
}

func NewAPIKeyValidator(redis *redis.Client, keyStore *store.APIKeyStore, cacheTTL time.Duration) *APIKeyValidator

// Validate validates an API key and returns the associated tenant_id.
//
// Algorithm:
//   1. Check format: must start with "cnd_" and be 47 chars total
//   2. Compute keyHash = hex(SHA-256(rawKey))
//   3. Check Redis: GET rl:authcache:{keyHash}
//      → hit: return cached tenant_id string
//   4. Miss: query PostgreSQL via APIKeyStore.GetByHash(ctx, keyHash)
//      → ErrNotFound → return ErrInvalidAPIKey
//   5. Check expiry: if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt)
//      → return ErrExpiredAPIKey
//   6. Cache in Redis: SET rl:authcache:{keyHash} {tenantID} EX {cacheTTL}
//   7. Fire-and-forget: update last_used_at in PostgreSQL
//   8. Return tenantID string
//
// Redis cache key pattern: "authcache:{keyHash}"
// Redis cache value: "{tenant_id}" (UUID string)
// Redis cache TTL: config.Auth.APIKeyCacheTTL (default 5m)
//
// On Redis failure: log warning, fall through to PostgreSQL (never block auth)
func (v *APIKeyValidator) Validate(ctx context.Context, rawKey string) (tenantID string, err error)

// HashKey computes the SHA-256 hash of a raw API key.
// Exported for use in key creation flow.
func HashKey(rawKey string) string {
    sum := sha256.Sum256([]byte(rawKey))
    return hex.EncodeToString(sum[:])
}

// IsAPIKey returns true if the string looks like a Conduit API key.
func IsAPIKey(s string) bool {
    return strings.HasPrefix(s, "cnd_") && len(s) == 47
}
```

---

## 4. Auth Middleware — `internal/auth/middleware.go`

```go
package auth

import (
    "context"
    "net/http"
    "strings"
)

// Middleware validates the Authorization header and sets tenant_id in context.
//
// Accepted formats:
//   Authorization: Bearer cnd_<key>    → API key path
//   Authorization: Bearer <jwt>        → JWT path (Phase 5)
//
// On success: sets ctxKeyTenantID and ctxKeyAuthMethod in context, calls next
// On failure: writes JSON 401 and returns (does NOT call next)
//
// The middleware MUST distinguish API key from JWT:
//   - If bearer token starts with "cnd_" → validate as API key
//   - Otherwise → validate as JWT
//   - In Phase 1–4: JWT is not yet supported; return 401 with code "JWT_NOT_SUPPORTED"
func NewMiddleware(keyValidator *APIKeyValidator, jwtValidator *JWTValidator) func(http.Handler) http.Handler

// extractBearer extracts the bearer token from the Authorization header.
// Returns "", false if absent or malformed.
func extractBearer(r *http.Request) (string, bool) {
    h := r.Header.Get("Authorization")
    if h == "" {
        return "", false
    }
    parts := strings.SplitN(h, " ", 2)
    if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
        return "", false
    }
    return strings.TrimSpace(parts[1]), true
}
```

### Error responses from auth middleware

```json
// Missing Authorization header
HTTP/1.1 401 Unauthorized
{"error": "missing Authorization header", "code": "UNAUTHORIZED", "request_id": "..."}

// Malformed header (not "Bearer <token>")
HTTP/1.1 401 Unauthorized
{"error": "invalid Authorization header format", "code": "UNAUTHORIZED", "request_id": "..."}

// Invalid API key (wrong format, revoked, or not found)
HTTP/1.1 401 Unauthorized
{"error": "invalid API key", "code": "UNAUTHORIZED", "request_id": "..."}

// Expired API key
HTTP/1.1 401 Unauthorized
{"error": "API key has expired", "code": "UNAUTHORIZED", "request_id": "..."}
```

**IMPORTANT**: Never reveal in the error message whether the key "doesn't exist" vs "is revoked". Always return the same generic message to prevent enumeration attacks.

---

## 5. JWT Validator — `internal/auth/jwt.go`

> Phase 5 implementation. Phase 1–4: stub that always returns `ErrJWTNotSupported`.

```go
package auth

import (
    "context"
    "errors"
    "time"

    "github.com/lestrrat-go/jwx/v2/jwt"
    "github.com/lestrrat-go/jwx/v2/jwa"
    "github.com/lestrrat-go/jwx/v2/jwk"
)

var ErrJWTNotSupported = errors.New("JWT authentication not yet configured")
var ErrInvalidJWT = errors.New("invalid JWT")
var ErrExpiredJWT = errors.New("JWT has expired")

// JWTValidator validates JWTs signed by Conduit's OAuth server.
type JWTValidator struct {
    secretKey []byte  // HMAC-SHA256 signing key from config.Auth.JWTSecret
    issuer    string  // "https://conduit" or configured URL
}

func NewJWTValidator(secret, issuer string) *JWTValidator

// Validate parses and validates a JWT, returning the tenant_id claim.
//
// Algorithm:
//   1. Parse JWT using lestrrat-go/jwx/v2 with jwa.HS256
//   2. Verify signature using config.Auth.JWTSecret
//   3. Check exp claim (must be in the future)
//   4. Check iss claim (must match config issuer)
//   5. Extract "tenant_id" string claim
//   6. Return tenantID string
//
// JWT claims (all required):
//   sub:       string  — subject (user or app ID)
//   tenant_id: string  — UUID of the tenant
//   scopes:    string  — space-separated scopes (e.g. "mcp:call mcp:admin")
//   iss:       string  — issuer URL
//   exp:       int64   — expiry unix timestamp
//   iat:       int64   — issued-at unix timestamp
//   jti:       string  — unique token ID (UUID)
func (v *JWTValidator) Validate(ctx context.Context, rawJWT string) (tenantID string, err error)

// IssueAccessToken creates a new access JWT for a given tenant + scopes.
// Used by the OAuth token endpoint (Phase 5).
func (v *JWTValidator) IssueAccessToken(tenantID, subject string, scopes []string, ttl time.Duration) (string, error)
```

---

## 6. Redis Cache Key Patterns

All Redis keys used by the auth system:

| Key Pattern | Value | TTL | Purpose |
|---|---|---|---|
| `authcache:{keyHash}` | `{tenant_id}` (string) | 5m (configurable) | API key → tenant lookup |
| `authcache:jwt:{jti}` | `"revoked"` | Until JWT exp | JWT revocation list |

**Namespacing**: All auth cache keys are prefixed with `authcache:` to avoid collision with rate limit keys (`rl:...`).

---

## 7. Constant-Time Comparison

API key comparison MUST use constant-time equality to prevent timing attacks:

```go
import "crypto/subtle"

// When comparing key hashes:
// WRONG: if stored == computed { ... }
// RIGHT:
if subtle.ConstantTimeCompare([]byte(storedHash), []byte(computedHash)) != 1 {
    return ErrInvalidAPIKey
}
```

In practice, because we're comparing SHA-256 hashes stored in PostgreSQL (retrieved by hash index), the constant-time comparison is on the hash bytes after lookup, not during lookup. Both the stored and computed hash must be compared before using the result.

---

## 8. Security Rules (Non-Negotiable)

1. **NEVER log a raw API key** — only log the key_prefix (first 12 chars)
2. **NEVER store a raw API key** — only store the SHA-256 hash
3. **NEVER return a raw API key from the API** — only return it once at creation time
4. **NEVER trust tenant_id from request body or URL** — always extract from validated token
5. **Use constant-time comparison** for any security-sensitive byte comparison
6. **Fail closed on PostgreSQL errors** — if DB is unreachable and not in Redis cache, return 503 (not allow)
7. **Fail open on Redis errors** — if Redis is unreachable, fall through to PostgreSQL (log warning)

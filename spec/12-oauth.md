# Spec 12 — OAuth 2.0

> Phase: P5 | Files: `internal/auth/oauth.go`, `internal/auth/jwt.go`, `migrations/000003_oauth_tables.up.sql`

---

## 1. Supported Grant Types

| Grant Type | Use Case | Flow |
|---|---|---|
| `authorization_code` + PKCE | Interactive browser agent | Browser → Conduit → Upstream |
| `client_credentials` | Machine-to-machine (M2M) | Direct token request |
| `refresh_token` | Token renewal | Exchange refresh token for new access token |

---

## 2. Endpoints

All OAuth endpoints live on the **management API** server (`:8081`):

| Method | Path | Description |
|---|---|---|
| `GET` | `/oauth/authorize` | Authorization endpoint (redirect-based) |
| `POST` | `/oauth/token` | Token endpoint (code exchange, client credentials, refresh) |
| `POST` | `/oauth/introspect` | Token introspection (RFC 7662) |
| `POST` | `/oauth/revoke` | Token revocation (RFC 7009) |
| `GET` | `/.well-known/oauth-authorization-server` | OAuth server metadata (RFC 8414) |

---

## 3. Authorization Endpoint

```
GET /oauth/authorize
  ?client_id=<id>
  &redirect_uri=<url>
  &response_type=code
  &scope=mcp:call
  &state=<random>
  &code_challenge=<base64url(SHA256(verifier))>
  &code_challenge_method=S256

Behavior:
  1. Validate client_id (lookup in oauth_applications)
  2. Validate redirect_uri is in app.redirect_uris
  3. Validate response_type == "code"
  4. Validate code_challenge_method == "S256" (PKCE required)
  5. Show login page (if user not authenticated) OR
     Show consent page (list requested scopes)
  6. On user approval:
     a. Generate 32-byte random auth code
     b. Store: code_hash = SHA256(code), redirect_uri, scopes, code_challenge, expires_at = now+10m
     c. Redirect to redirect_uri?code=<code>&state=<state>
  7. On user denial:
     Redirect to redirect_uri?error=access_denied&state=<state>
```

---

## 4. Token Endpoint

```
POST /oauth/token
Content-Type: application/x-www-form-urlencoded

Grant: authorization_code
  grant_type=authorization_code
  &code=<code>
  &redirect_uri=<url>
  &client_id=<id>
  &code_verifier=<verifier>  ← PKCE

Grant: client_credentials
  grant_type=client_credentials
  &client_id=<id>
  &client_secret=<secret>
  &scope=mcp:call

Grant: refresh_token
  grant_type=refresh_token
  &refresh_token=<token>
  &client_id=<id>
```

### Authorization Code Exchange — Algorithm

```go
// 1. Validate grant_type == "authorization_code"
// 2. Lookup oauth_auth_codes WHERE code_hash = SHA256(code) AND used = false AND expires_at > now
//    → not found or used: return error "invalid_grant"
// 3. Verify redirect_uri matches stored redirect_uri
// 4. Verify PKCE: BASE64URL(SHA256(code_verifier)) == stored code_challenge
// 5. Mark code as used (UPDATE SET used = true)
// 6. Issue access token (JWT, TTL from config)
// 7. Issue refresh token (random 32 bytes, store SHA256 hash)
// 8. Return TokenResponse
```

### Client Credentials — Algorithm

```go
// 1. Lookup oauth_applications WHERE client_id = $1
// 2. Verify bcrypt.CompareHashAndPassword(app.client_secret, client_secret)
// 3. Validate requested scopes are subset of app.scopes
// 4. Issue access token (JWT)
// 5. No refresh token for client_credentials
// 6. Return TokenResponse
```

### Token Response

```json
{
  "access_token": "<jwt>",
  "token_type": "Bearer",
  "expires_in": 3600,
  "refresh_token": "<opaque token>",  // omitted for client_credentials
  "scope": "mcp:call"
}
```

### Error Response (RFC 6749 format)

```json
{
  "error": "invalid_grant",
  "error_description": "Authorization code is invalid or expired"
}
```

Standard error codes: `invalid_request`, `invalid_client`, `invalid_grant`, `unauthorized_client`, `unsupported_grant_type`, `invalid_scope`.

---

## 5. JWT Claims

```go
// Access token JWT claims:
type JWTClaims struct {
    Issuer    string   `json:"iss"`        // "https://conduit" or config URL
    Subject   string   `json:"sub"`        // user ID or app client_id
    Audience  []string `json:"aud"`        // ["conduit"]
    ExpiresAt int64    `json:"exp"`        // unix timestamp
    IssuedAt  int64    `json:"iat"`
    JWTID     string   `json:"jti"`        // UUID, for revocation
    TenantID  string   `json:"tenant_id"`  // REQUIRED: UUID string
    Scopes    string   `json:"scope"`      // space-separated
}

// Signing algorithm: HS256 (symmetric, using config.Auth.JWTSecret)
// In Phase 8+: support RS256 (asymmetric) for federation
```

---

## 6. PKCE Implementation

```go
// PKCE RFC 7636 — S256 method only (plain is not allowed)

// Verifier: 43-128 chars, [A-Z a-z 0-9 - . _ ~]
// Challenge: BASE64URL(SHA256(verifier)) — no padding

func VerifyPKCE(codeVerifier, storedChallenge string) bool {
    h := sha256.Sum256([]byte(codeVerifier))
    computed := base64.RawURLEncoding.EncodeToString(h[:])
    return subtle.ConstantTimeCompare([]byte(computed), []byte(storedChallenge)) == 1
}
```

---

## 7. Refresh Token Rotation

Each time a refresh token is used, the old one is revoked and a new one is issued:

```go
// On refresh_token grant:
// 1. Lookup oauth_refresh_tokens WHERE token_hash = SHA256(token) AND revoked = false AND expires_at > now
// 2. UPDATE SET revoked = true
// 3. Issue new access token + new refresh token
// 4. Store new refresh token hash

// Refresh token TTL: config.Auth.RefreshTokenTTL (default: 720h = 30 days)
// Access token TTL: config.Auth.AccessTokenTTL (default: 1h)
```

---

## 8. Token Introspection

```
POST /oauth/introspect
Content-Type: application/x-www-form-urlencoded
Authorization: Basic <client_id:client_secret>

token=<jwt_or_refresh_token>

Response:
{
  "active": true,
  "scope": "mcp:call",
  "client_id": "<client_id>",
  "tenant_id": "<uuid>",
  "exp": 1704067200
}

// If token is invalid/expired:
{"active": false}
```

---

## 9. OAuth Application Management

Exposed via management API at `/api/v1/oauth/applications`:

```
POST /api/v1/oauth/applications — create app, returns client_id + client_secret (ONCE)
GET  /api/v1/oauth/applications?tenant_id={id} — list apps (no secrets)
PATCH /api/v1/oauth/applications/{id} — update redirect_uris, name, scopes
DELETE /api/v1/oauth/applications/{id} — delete app
POST /api/v1/oauth/applications/{id}/rotate-secret — issue new client_secret
```

`client_secret` is:
- Generated as 32 random bytes, base64url-encoded
- Stored as `bcrypt(cost=12)`
- Returned ONCE at creation or rotation

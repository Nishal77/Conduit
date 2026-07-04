// Package auth implements Conduit's two authentication methods: API keys
// (this file) and JWT bearer tokens (jwt.go). Both validate a credential
// and resolve it to a tenant_id — the only source of truth for "who is
// making this call" (ADR-004: tenant_id is never trusted from the request
// body or URL).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/conduit-oss/conduit/internal/store"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
)

// ErrInvalidAPIKey is returned when the key is malformed, revoked, or
// unknown. Deliberately generic — see the security note on Validate below.
var ErrInvalidAPIKey = errors.New("invalid API key")

// ErrExpiredAPIKey is returned when the key has passed its expires_at.
var ErrExpiredAPIKey = errors.New("API key expired")

// apiKeyPrefix is prepended to every generated key and is how the auth
// middleware distinguishes an API key from a JWT bearer token.
const apiKeyPrefix = "cnd_"

// apiKeyTotalLen is the full length of a generated key: 4-char prefix + 43
// base64url characters (32 random bytes, unpadded).
const apiKeyTotalLen = 47

// authCacheKeyPrefix namespaces API-key cache entries in Redis so they
// never collide with rate-limit keys, which use "rl:...".
const authCacheKeyPrefix = "authcache:"

// GenerateAPIKey creates a new, cryptographically random API key. rawKey is
// returned to the caller exactly once — Conduit stores only keyHash
// (SHA-256, hex) and keyPrefix (first 12 chars, for display), per
// spec/05-auth.md §8's "never store a raw API key" rule.
func GenerateAPIKey() (rawKey, keyHash, keyPrefix string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generate random key: %w", err)
	}
	rawKey = apiKeyPrefix + base64.RawURLEncoding.EncodeToString(buf)
	keyHash = HashKey(rawKey)
	keyPrefix = rawKey[:12]
	return rawKey, keyHash, keyPrefix, nil
}

// HashKey computes the SHA-256 hash (hex-encoded) of a raw API key. Exported
// so the key-creation flow (management API, Phase 4) can compute the same
// hash Validate will look up later.
func HashKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
}

// IsAPIKey reports whether s looks like a Conduit API key (as opposed to a
// JWT). This is a format check only — it does not mean the key is valid.
func IsAPIKey(s string) bool {
	return strings.HasPrefix(s, apiKeyPrefix) && len(s) == apiKeyTotalLen
}

// APIKeyValidator validates API keys using a Redis cache in front of
// PostgreSQL. A cache hit costs one Redis round trip (<0.1ms target); a
// cache miss costs one indexed PostgreSQL lookup (<5ms target) plus a
// cache-fill write.
type APIKeyValidator struct {
	redis    *redis.Client
	keyStore *store.APIKeyStore
	cacheTTL time.Duration
}

// NewAPIKeyValidator returns a validator backed by redisClient and keyStore.
// cacheTTL is how long a validated key's tenant_id is cached in Redis
// (config: auth.api_key_cache_ttl, default 5m).
func NewAPIKeyValidator(redisClient *redis.Client, keyStore *store.APIKeyStore, cacheTTL time.Duration) *APIKeyValidator {
	return &APIKeyValidator{redis: redisClient, keyStore: keyStore, cacheTTL: cacheTTL}
}

// Validate checks rawKey's format, looks it up (Redis cache, falling back
// to PostgreSQL), and returns the tenant_id it belongs to.
//
// Security notes (spec/05-auth.md §8):
//   - Every failure path returns the same ErrInvalidAPIKey (except expiry,
//     which is distinguished only because it's not a secret — telling a
//     caller "your key expired" doesn't help an attacker enumerate keys the
//     way "this key doesn't exist" vs "this key is revoked" would).
//   - The raw key is never logged; only HashKey's output ever leaves this
//     function's stack.
//   - A Redis outage fails open (fall through to PostgreSQL); a PostgreSQL
//     outage on a cache miss fails closed (returns an error, which the auth
//     middleware turns into a 503 — never silently allow an unverifiable
//     request through).
func (v *APIKeyValidator) Validate(ctx context.Context, rawKey string) (tenantID string, err error) {
	if !IsAPIKey(rawKey) {
		return "", ErrInvalidAPIKey
	}
	keyHash := HashKey(rawKey)
	cacheKey := authCacheKeyPrefix + keyHash

	if v.redis != nil {
		cached, err := v.redis.Get(ctx, cacheKey).Result()
		if err == nil {
			return cached, nil
		}
		if !errors.Is(err, redis.Nil) {
			log.Warn().Err(err).Msg("auth cache read failed, falling back to database")
		}
	}

	key, err := v.keyStore.GetByHash(ctx, keyHash)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrInvalidAPIKey
	}
	if err != nil {
		// Fail closed: we could not verify this key one way or the other.
		return "", fmt.Errorf("look up api key: %w", err)
	}

	// Defense in depth: GetByHash already filtered by key_hash = $1 in SQL,
	// but re-verify in application code with a constant-time comparison
	// (spec/05-auth.md §7) so a future refactor of the query layer can't
	// silently turn this into a timing oracle.
	if !constantTimeEqual(key.KeyHash, keyHash) {
		return "", ErrInvalidAPIKey
	}

	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		return "", ErrExpiredAPIKey
	}

	tenantID = key.TenantID.String()

	if v.redis != nil {
		if err := v.redis.Set(ctx, cacheKey, tenantID, v.cacheTTL).Err(); err != nil {
			log.Warn().Err(err).Msg("auth cache write failed")
		}
	}

	v.keyStore.UpdateLastUsed(ctx, key.ID)

	return tenantID, nil
}

// constantTimeEqual compares two hex-encoded hashes in constant time, per
// spec/05-auth.md §7. Exposed for callers that fetch a hash through a path
// other than Validate (e.g. a future admin "verify this key" endpoint) and
// need the same timing-attack protection.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

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
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

// OAuthError is a JSON-RPC-free RFC 6749 §5.2 error: the OAuth token and
// authorize endpoints report failures as {"error": code, "error_description":
// message}, a different shape from Conduit's usual ErrorResponse — OAuth is
// a standard other clients (browser SDKs, other MCP gateways) implement
// against, so Conduit follows the spec's wire format exactly here rather
// than its own conventions.
type OAuthError struct {
	Code        string // invalid_request | invalid_client | invalid_grant | unauthorized_client | unsupported_grant_type | invalid_scope
	Description string
}

func (e *OAuthError) Error() string { return e.Description }

func newOAuthError(code, description string) *OAuthError {
	return &OAuthError{Code: code, Description: description}
}

// authCodeTTL is fixed at 10 minutes per spec/12-oauth.md §3 — short-lived
// by design, unlike access/refresh token TTLs which are configurable.
const authCodeTTL = 10 * time.Minute

// TokenResponse is the JSON body every successful grant returns
// (spec/12-oauth.md §4).
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope"`
}

// IntrospectResponse is the JSON body for POST /oauth/introspect (RFC 7662).
type IntrospectResponse struct {
	Active   bool   `json:"active"`
	Scope    string `json:"scope,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	TenantID string `json:"tenant_id,omitempty"`
	Exp      int64  `json:"exp,omitempty"`
}

// OAuthServer implements Conduit's OAuth 2.0 authorization server: the
// authorization_code (with mandatory PKCE), client_credentials, and
// refresh_token grants, plus introspection and revocation.
//
// redisClient may be nil (revocation of already-issued access tokens then
// silently no-ops, matching pre-blocklist behavior) — every other grant and
// introspection path works without it. When set, it backs a short-lived
// jti blocklist: access tokens are stateless signed JWTs (spec/12-oauth.md
// §5's JWTClaims.JWTID exists "for revocation"), so unlike refresh tokens
// there is no database row to mark revoked — Revoke instead records the
// token's jti in Redis until its natural expiry, and Introspect checks it.
//
// Note this blocklist is consulted by /oauth/introspect only, not by the
// proxy's own request-path JWT validation (internal/auth/middleware.go),
// which stays a pure signature/expiry check with no Redis round-trip on
// the hot path — that's the latency benefit of using JWTs at all
// (ADR-004's <0.1ms auth budget). A revoked-but-unexpired access token can
// therefore still authenticate direct MCP calls until it naturally expires;
// access_token_ttl (default 1h) bounds that exposure window.
type OAuthServer struct {
	apps          *store.OAuthApplicationStore
	codes         *store.OAuthAuthCodeStore
	refreshTokens *store.OAuthRefreshTokenStore
	jwt           *JWTValidator
	redis         *redis.Client

	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

// NewOAuthServer returns an OAuthServer wired to its stores and the JWT
// issuer used to mint access tokens. redisClient may be nil — see
// OAuthServer's doc comment.
func NewOAuthServer(
	apps *store.OAuthApplicationStore,
	codes *store.OAuthAuthCodeStore,
	refreshTokens *store.OAuthRefreshTokenStore,
	jwt *JWTValidator,
	redisClient *redis.Client,
	accessTokenTTL, refreshTokenTTL time.Duration,
) *OAuthServer {
	return &OAuthServer{
		apps:            apps,
		codes:           codes,
		refreshTokens:   refreshTokens,
		jwt:             jwt,
		redis:           redisClient,
		accessTokenTTL:  accessTokenTTL,
		refreshTokenTTL: refreshTokenTTL,
	}
}

// revokedJTIKey namespaces revoked-access-token entries in Redis, mirroring
// internal/ratelimit's "rl:" key-prefix convention.
func revokedJTIKey(jti string) string { return "revoked-jti:" + jti }

// blocklistJTI records jti as revoked until ttl elapses — after that its
// underlying JWT would have expired naturally anyway, so there's nothing
// left to protect against.
func (s *OAuthServer) blocklistJTI(ctx context.Context, jti string, ttl time.Duration) {
	if s.redis == nil || jti == "" || ttl <= 0 {
		return
	}
	_ = s.redis.Set(ctx, revokedJTIKey(jti), "1", ttl).Err()
}

// isJTIRevoked reports whether jti was blocklisted by a prior Revoke call.
// A Redis error fails open (not revoked) — the same fail-open policy
// internal/ratelimit uses (ADR-003), so a Redis outage degrades revocation
// checking rather than rejecting every access token in the system.
func (s *OAuthServer) isJTIRevoked(ctx context.Context, jti string) bool {
	if s.redis == nil || jti == "" {
		return false
	}
	n, err := s.redis.Exists(ctx, revokedJTIKey(jti)).Result()
	return err == nil && n > 0
}

// ValidateAuthorizeRequest checks a GET /oauth/authorize request's
// parameters against spec/12-oauth.md §3 steps 1-4, returning the resolved
// application on success.
func (s *OAuthServer) ValidateAuthorizeRequest(ctx context.Context, clientID, redirectURI, responseType, codeChallengeMethod string) (*store.OAuthApplication, error) {
	app, err := s.apps.GetByClientID(ctx, clientID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newOAuthError("invalid_client", "unknown client_id")
	}
	if err != nil {
		return nil, fmt.Errorf("look up oauth application: %w", err)
	}

	if !containsString(app.RedirectURIs, redirectURI) {
		return nil, newOAuthError("invalid_request", "redirect_uri is not registered for this client")
	}
	if responseType != "code" {
		return nil, newOAuthError("unsupported_grant_type", `response_type must be "code"`)
	}
	if codeChallengeMethod != "S256" {
		return nil, newOAuthError("invalid_request", "code_challenge_method must be S256 (PKCE is required)")
	}

	return app, nil
}

// IssueAuthorizationCode generates and stores a new authorization code
// after the user (or, until Conduit has its own login UI, the caller)
// approves the request — spec/12-oauth.md §3 step 6.
func (s *OAuthServer) IssueAuthorizationCode(ctx context.Context, app *store.OAuthApplication, redirectURI string, scopes []string, codeChallenge string) (rawCode string, err error) {
	rawCode, codeHash, err := generateOpaqueToken()
	if err != nil {
		return "", fmt.Errorf("generate authorization code: %w", err)
	}

	_, err = s.codes.Create(ctx, store.CreateAuthCodeInput{
		TenantID:      app.TenantID,
		AppID:         app.ID,
		CodeHash:      codeHash,
		RedirectURI:   redirectURI,
		Scopes:        scopes,
		CodeChallenge: codeChallenge,
		TTL:           authCodeTTL,
	})
	if err != nil {
		return "", fmt.Errorf("store authorization code: %w", err)
	}
	return rawCode, nil
}

// ExchangeAuthorizationCode implements the authorization_code grant
// (spec/12-oauth.md §4's algorithm).
func (s *OAuthServer) ExchangeAuthorizationCode(ctx context.Context, code, redirectURI, clientID, codeVerifier string) (*TokenResponse, error) {
	app, err := s.apps.GetByClientID(ctx, clientID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newOAuthError("invalid_client", "unknown client_id")
	}
	if err != nil {
		return nil, fmt.Errorf("look up oauth application: %w", err)
	}

	codeHash := hashToken(code)
	authCode, err := s.codes.GetValidByHash(ctx, codeHash)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newOAuthError("invalid_grant", "authorization code is invalid, used, or expired")
	}
	if err != nil {
		return nil, fmt.Errorf("look up authorization code: %w", err)
	}

	if authCode.AppID != app.ID {
		return nil, newOAuthError("invalid_grant", "authorization code was not issued to this client")
	}
	if authCode.RedirectURI != redirectURI {
		return nil, newOAuthError("invalid_grant", "redirect_uri does not match the one used to obtain the code")
	}
	if authCode.CodeChallenge == nil || !VerifyPKCE(codeVerifier, *authCode.CodeChallenge) {
		return nil, newOAuthError("invalid_grant", "PKCE code_verifier does not match code_challenge")
	}

	if err := s.codes.MarkUsed(ctx, authCode.ID); err != nil {
		return nil, fmt.Errorf("mark authorization code used: %w", err)
	}

	return s.issueTokenPair(ctx, app, authCode.TenantID, authCode.Scopes, true)
}

// ClientCredentials implements the client_credentials grant
// (spec/12-oauth.md §4's algorithm) — machine-to-machine, no user
// interaction and no refresh token.
func (s *OAuthServer) ClientCredentials(ctx context.Context, clientID, clientSecret string, requestedScopes []string) (*TokenResponse, error) {
	app, err := s.apps.GetByClientID(ctx, clientID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newOAuthError("invalid_client", "unknown client_id")
	}
	if err != nil {
		return nil, fmt.Errorf("look up oauth application: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(app.ClientSecret), []byte(clientSecret)); err != nil {
		return nil, newOAuthError("invalid_client", "client authentication failed")
	}

	scopes := requestedScopes
	if len(scopes) == 0 {
		scopes = app.Scopes
	} else if !isSubset(scopes, app.Scopes) {
		return nil, newOAuthError("invalid_scope", "requested scope exceeds what this client is allowed")
	}

	return s.issueTokenPair(ctx, app, app.TenantID, scopes, false)
}

// RefreshToken implements the refresh_token grant with rotation
// (spec/12-oauth.md §7): the presented token is revoked and replaced with
// a new access/refresh pair, regardless of outcome — a stolen refresh
// token can be used at most once before rotation invalidates it, giving
// the legitimate holder a signal (their next refresh will fail) that
// something is wrong.
func (s *OAuthServer) RefreshToken(ctx context.Context, rawToken, clientID string) (*TokenResponse, error) {
	app, err := s.apps.GetByClientID(ctx, clientID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newOAuthError("invalid_client", "unknown client_id")
	}
	if err != nil {
		return nil, fmt.Errorf("look up oauth application: %w", err)
	}

	tokenHash := hashToken(rawToken)
	refreshToken, err := s.refreshTokens.GetValidByHash(ctx, tokenHash)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newOAuthError("invalid_grant", "refresh token is invalid, revoked, or expired")
	}
	if err != nil {
		return nil, fmt.Errorf("look up refresh token: %w", err)
	}
	if refreshToken.AppID != app.ID {
		return nil, newOAuthError("invalid_grant", "refresh token was not issued to this client")
	}

	if err := s.refreshTokens.Revoke(ctx, refreshToken.ID); err != nil {
		return nil, fmt.Errorf("revoke used refresh token: %w", err)
	}

	return s.issueTokenPair(ctx, app, refreshToken.TenantID, refreshToken.Scopes, true)
}

// issueTokenPair mints an access token (always) and a refresh token (when
// withRefresh is true — never for client_credentials, per spec/12-oauth.md §4).
func (s *OAuthServer) issueTokenPair(ctx context.Context, app *store.OAuthApplication, tenantID uuid.UUID, scopes []string, withRefresh bool) (*TokenResponse, error) {
	accessToken, err := s.jwt.IssueAccessToken(tenantID.String(), app.ClientID, scopes, s.accessTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("issue access token: %w", err)
	}

	resp := &TokenResponse{
		AccessToken: accessToken,
		TokenType:   "Bearer",
		ExpiresIn:   int64(s.accessTokenTTL.Seconds()),
		Scope:       strings.Join(scopes, " "),
	}

	if withRefresh {
		rawRefresh, refreshHash, err := generateOpaqueToken()
		if err != nil {
			return nil, fmt.Errorf("generate refresh token: %w", err)
		}
		if _, err := s.refreshTokens.Create(ctx, store.CreateRefreshTokenInput{
			TenantID:  tenantID,
			AppID:     app.ID,
			TokenHash: refreshHash,
			Scopes:    scopes,
			TTL:       s.refreshTokenTTL,
		}); err != nil {
			return nil, fmt.Errorf("store refresh token: %w", err)
		}
		resp.RefreshToken = rawRefresh
	}

	return resp, nil
}

// Introspect implements RFC 7662: report whether a token (JWT access token
// or opaque refresh token) is currently valid. Per the RFC, an
// unrecognized or invalid token yields {"active": false}, not an error —
// "active: false" is a valid, successful response.
func (s *OAuthServer) Introspect(ctx context.Context, token string) (*IntrospectResponse, error) {
	if claims, err := s.jwt.ParseClaims(token); err == nil {
		if s.isJTIRevoked(ctx, claims.JWTID) {
			return &IntrospectResponse{Active: false}, nil
		}
		return &IntrospectResponse{
			Active:   true,
			Scope:    claims.Scope,
			ClientID: claims.Subject,
			TenantID: claims.TenantID,
			Exp:      claims.ExpiresAt,
		}, nil
	}

	refreshToken, err := s.refreshTokens.GetValidByHash(ctx, hashToken(token))
	if err != nil {
		return &IntrospectResponse{Active: false}, nil
	}
	app, err := s.apps.GetByID(ctx, refreshToken.AppID)
	if err != nil {
		return &IntrospectResponse{Active: false}, nil
	}
	return &IntrospectResponse{
		Active:   true,
		Scope:    strings.Join(refreshToken.Scopes, " "),
		ClientID: app.ClientID,
		TenantID: refreshToken.TenantID.String(),
		Exp:      refreshToken.ExpiresAt.Unix(),
	}, nil
}

// Revoke implements RFC 7009: best-effort revocation of a refresh token.
// Conduit's access tokens are stateless JWTs with no revocation list, so
// only refresh tokens can actually be revoked here — per RFC 7009 §2.2,
// the endpoint returns success either way rather than revealing whether
// the presented token was a valid, unknown, or already-revoked one.
func (s *OAuthServer) Revoke(ctx context.Context, token string) error {
	if claims, err := s.jwt.ParseClaims(token); err == nil {
		remaining := time.Until(time.Unix(claims.ExpiresAt, 0))
		s.blocklistJTI(ctx, claims.JWTID, remaining)
		return nil
	}

	refreshToken, err := s.refreshTokens.GetValidByHash(ctx, hashToken(token))
	if err != nil {
		return nil // unknown/already-revoked token: still a "successful" revocation per RFC 7009
	}
	return s.refreshTokens.Revoke(ctx, refreshToken.ID)
}

// VerifyPKCE checks a PKCE code_verifier against the code_challenge stored
// at authorization time (RFC 7636, S256 method only — spec/12-oauth.md §6).
func VerifyPKCE(codeVerifier, storedChallenge string) bool {
	h := sha256.Sum256([]byte(codeVerifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(storedChallenge)) == 1
}

// generateOpaqueToken creates a 32-byte random token (authorization codes,
// refresh tokens), returning both the raw value to hand to the caller and
// its SHA-256 hash to persist — mirroring GenerateAPIKey's
// never-store-the-raw-value rule.
func generateOpaqueToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

// GenerateClientSecret creates a new OAuth client secret: 32 random bytes,
// base64url-encoded, returned once alongside its bcrypt(cost=12) hash for
// storage (spec/12-oauth.md §9). Exported for the management API's
// application-create and rotate-secret handlers.
func GenerateClientSecret() (rawSecret, secretHash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate client secret: %w", err)
	}
	rawSecret = base64.RawURLEncoding.EncodeToString(buf)

	// cost=12 per spec/12-oauth.md §9, not bcrypt.DefaultCost (10) —
	// client secrets are long-lived credentials worth the extra hashing
	// time, unlike a login password checked on every request.
	hashed, err := bcrypt.GenerateFromPassword([]byte(rawSecret), 12)
	if err != nil {
		return "", "", fmt.Errorf("hash client secret: %w", err)
	}
	return rawSecret, string(hashed), nil
}

// hashToken computes the SHA-256 hash (hex-encoded) of an opaque token,
// used for both authorization codes and refresh tokens.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// isSubset reports whether every element of want appears in allowed.
func isSubset(want, allowed []string) bool {
	for _, w := range want {
		if !containsString(allowed, w) {
			return false
		}
	}
	return true
}

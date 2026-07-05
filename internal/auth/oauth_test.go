//go:build integration

// OAuth grant-flow tests against a real PostgreSQL database — see
// internal/store/integration_test.go for how TEST_DATABASE_URL is
// resolved. OAuthServer's logic is tightly coupled to its three stores'
// exact query semantics (used/expired code handling, refresh token
// rotation), so testing against a real database is more trustworthy here
// than mocking store interfaces that don't otherwise exist in this codebase.
package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/conduit-oss/conduit/internal/auth"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestOAuthServer(t *testing.T, db *store.DB) (*auth.OAuthServer, *store.Tenant, *store.OAuthApplication, string) {
	return newTestOAuthServerWithRedis(t, db, nil)
}

func newTestOAuthServerWithRedis(t *testing.T, db *store.DB, redisClient *redis.Client) (*auth.OAuthServer, *store.Tenant, *store.OAuthApplication, string) {
	t.Helper()
	ctx := context.Background()

	tenants := store.NewTenantStore(db)
	apps := store.NewOAuthApplicationStore(db)
	codes := store.NewOAuthAuthCodeStore(db)
	refreshTokens := store.NewOAuthRefreshTokenStore(db)
	jwtValidator := auth.NewJWTValidator("test-oauth-secret-32-characters!!", "https://conduit")

	tenant, err := tenants.Create(ctx, "oauth-test-"+uniqueSuffix(), "OAuth Test", "free")
	require.NoError(t, err)

	rawSecret, secretHash, err := auth.GenerateClientSecret()
	require.NoError(t, err)

	app, err := apps.Create(ctx, store.CreateOAuthAppInput{
		TenantID:         tenant.ID,
		Name:             "test-app",
		ClientID:         "client-" + uniqueSuffix(),
		ClientSecretHash: secretHash,
		RedirectURIs:     []string{"https://example.com/callback"},
		Scopes:           []string{"mcp:call"},
	})
	require.NoError(t, err)

	server := auth.NewOAuthServer(apps, codes, refreshTokens, jwtValidator, redisClient, time.Hour, 720*time.Hour)
	return server, tenant, app, rawSecret
}

func uniqueSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// pkcePair returns a valid PKCE verifier/challenge pair (RFC 7636, S256).
func pkcePair() (verifier, challenge string) {
	verifier = "a-verifier-that-is-at-least-43-characters-long-1234567890"
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func TestOAuthServer_AuthorizationCodeFlow(t *testing.T) {
	db := testDB(t)
	server, _, app, _ := newTestOAuthServer(t, db)
	ctx := context.Background()

	require.NoError(t, func() error {
		_, err := server.ValidateAuthorizeRequest(ctx, app.ClientID, app.RedirectURIs[0], "code", "S256")
		return err
	}())

	verifier, challenge := pkcePair()
	code, err := server.IssueAuthorizationCode(ctx, app, app.RedirectURIs[0], []string{"mcp:call"}, challenge)
	require.NoError(t, err)
	require.NotEmpty(t, code)

	tokens, err := server.ExchangeAuthorizationCode(ctx, code, app.RedirectURIs[0], app.ClientID, verifier)
	require.NoError(t, err)
	require.NotEmpty(t, tokens.AccessToken)
	require.NotEmpty(t, tokens.RefreshToken)
	require.Equal(t, "Bearer", tokens.TokenType)
	require.Equal(t, "mcp:call", tokens.Scope)

	t.Run("code cannot be exchanged twice", func(t *testing.T) {
		_, err := server.ExchangeAuthorizationCode(ctx, code, app.RedirectURIs[0], app.ClientID, verifier)
		require.Error(t, err)
		var oauthErr *auth.OAuthError
		require.ErrorAs(t, err, &oauthErr)
		require.Equal(t, "invalid_grant", oauthErr.Code)
	})

	t.Run("wrong pkce verifier is rejected", func(t *testing.T) {
		verifier2, challenge2 := pkcePair()
		_ = verifier2
		code2, err := server.IssueAuthorizationCode(ctx, app, app.RedirectURIs[0], []string{"mcp:call"}, challenge2)
		require.NoError(t, err)

		_, err = server.ExchangeAuthorizationCode(ctx, code2, app.RedirectURIs[0], app.ClientID, "wrong-verifier-that-is-long-enough-1234567890")
		require.Error(t, err)
		var oauthErr *auth.OAuthError
		require.ErrorAs(t, err, &oauthErr)
		require.Equal(t, "invalid_grant", oauthErr.Code)
	})

	t.Run("wrong redirect_uri is rejected", func(t *testing.T) {
		_, challenge3 := pkcePair()
		code3, err := server.IssueAuthorizationCode(ctx, app, app.RedirectURIs[0], []string{"mcp:call"}, challenge3)
		require.NoError(t, err)

		_, err = server.ExchangeAuthorizationCode(ctx, code3, "https://attacker.example/callback", app.ClientID, "irrelevant-verifier-1234567890123456789012")
		require.Error(t, err)
	})
}

func TestOAuthServer_ValidateAuthorizeRequest_Rejections(t *testing.T) {
	db := testDB(t)
	server, _, app, _ := newTestOAuthServer(t, db)
	ctx := context.Background()

	t.Run("unknown client", func(t *testing.T) {
		_, err := server.ValidateAuthorizeRequest(ctx, "does-not-exist", app.RedirectURIs[0], "code", "S256")
		var oauthErr *auth.OAuthError
		require.ErrorAs(t, err, &oauthErr)
		require.Equal(t, "invalid_client", oauthErr.Code)
	})

	t.Run("unregistered redirect_uri", func(t *testing.T) {
		_, err := server.ValidateAuthorizeRequest(ctx, app.ClientID, "https://not-registered.example", "code", "S256")
		var oauthErr *auth.OAuthError
		require.ErrorAs(t, err, &oauthErr)
		require.Equal(t, "invalid_request", oauthErr.Code)
	})

	t.Run("pkce required", func(t *testing.T) {
		_, err := server.ValidateAuthorizeRequest(ctx, app.ClientID, app.RedirectURIs[0], "code", "plain")
		var oauthErr *auth.OAuthError
		require.ErrorAs(t, err, &oauthErr)
		require.Equal(t, "invalid_request", oauthErr.Code)
	})
}

func TestOAuthServer_ClientCredentialsFlow(t *testing.T) {
	db := testDB(t)
	server, _, app, rawSecret := newTestOAuthServer(t, db)
	ctx := context.Background()

	tokens, err := server.ClientCredentials(ctx, app.ClientID, rawSecret, []string{"mcp:call"})
	require.NoError(t, err)
	require.NotEmpty(t, tokens.AccessToken)
	require.Empty(t, tokens.RefreshToken, "client_credentials must not issue a refresh token")

	t.Run("wrong secret is rejected", func(t *testing.T) {
		_, err := server.ClientCredentials(ctx, app.ClientID, "wrong-secret", nil)
		var oauthErr *auth.OAuthError
		require.ErrorAs(t, err, &oauthErr)
		require.Equal(t, "invalid_client", oauthErr.Code)
	})

	t.Run("scope escalation is rejected", func(t *testing.T) {
		_, err := server.ClientCredentials(ctx, app.ClientID, rawSecret, []string{"mcp:admin"})
		var oauthErr *auth.OAuthError
		require.ErrorAs(t, err, &oauthErr)
		require.Equal(t, "invalid_scope", oauthErr.Code)
	})
}

func TestOAuthServer_RefreshTokenRotation(t *testing.T) {
	db := testDB(t)
	server, _, app, _ := newTestOAuthServer(t, db)
	ctx := context.Background()

	verifier, challenge := pkcePair()
	code, err := server.IssueAuthorizationCode(ctx, app, app.RedirectURIs[0], []string{"mcp:call"}, challenge)
	require.NoError(t, err)
	first, err := server.ExchangeAuthorizationCode(ctx, code, app.RedirectURIs[0], app.ClientID, verifier)
	require.NoError(t, err)

	second, err := server.RefreshToken(ctx, first.RefreshToken, app.ClientID)
	require.NoError(t, err)
	require.NotEmpty(t, second.AccessToken)
	require.NotEqual(t, first.RefreshToken, second.RefreshToken)

	t.Run("rotated-out token cannot be reused", func(t *testing.T) {
		_, err := server.RefreshToken(ctx, first.RefreshToken, app.ClientID)
		var oauthErr *auth.OAuthError
		require.ErrorAs(t, err, &oauthErr)
		require.Equal(t, "invalid_grant", oauthErr.Code)
	})
}

func TestOAuthServer_IntrospectAndRevoke(t *testing.T) {
	db := testDB(t)
	server, _, app, rawSecret := newTestOAuthServer(t, db)
	ctx := context.Background()

	tokens, err := server.ClientCredentials(ctx, app.ClientID, rawSecret, []string{"mcp:call"})
	require.NoError(t, err)

	resp, err := server.Introspect(ctx, tokens.AccessToken)
	require.NoError(t, err)
	require.True(t, resp.Active)
	require.Equal(t, "mcp:call", resp.Scope)

	inactive, err := server.Introspect(ctx, "not-a-real-token")
	require.NoError(t, err)
	require.False(t, inactive.Active)

	verifier, challenge := pkcePair()
	code, err := server.IssueAuthorizationCode(ctx, app, app.RedirectURIs[0], []string{"mcp:call"}, challenge)
	require.NoError(t, err)
	authCodeTokens, err := server.ExchangeAuthorizationCode(ctx, code, app.RedirectURIs[0], app.ClientID, verifier)
	require.NoError(t, err)

	require.NoError(t, server.Revoke(ctx, authCodeTokens.RefreshToken))

	afterRevoke, err := server.Introspect(ctx, authCodeTokens.RefreshToken)
	require.NoError(t, err)
	require.False(t, afterRevoke.Active)

	// Revoking an unknown token must not error (RFC 7009 §2.2).
	require.NoError(t, server.Revoke(ctx, "definitely-not-a-token"))
}

// TestOAuthServer_RevokeAccessTokenWithBlocklist covers the Redis-backed
// jti blocklist: without a Redis client (see the other tests in this file,
// which all pass nil), Revoke on an access token is a documented no-op, but
// with one wired in, a revoked access token must show up as inactive on
// introspection even though its signature and expiry are still valid.
func TestOAuthServer_RevokeAccessTokenWithBlocklist(t *testing.T) {
	db := testDB(t)
	redisClient := testRedis(t)
	server, _, app, rawSecret := newTestOAuthServerWithRedis(t, db, redisClient)
	ctx := context.Background()

	tokens, err := server.ClientCredentials(ctx, app.ClientID, rawSecret, []string{"mcp:call"})
	require.NoError(t, err)

	active, err := server.Introspect(ctx, tokens.AccessToken)
	require.NoError(t, err)
	require.True(t, active.Active)

	require.NoError(t, server.Revoke(ctx, tokens.AccessToken))

	afterRevoke, err := server.Introspect(ctx, tokens.AccessToken)
	require.NoError(t, err)
	require.False(t, afterRevoke.Active)
}

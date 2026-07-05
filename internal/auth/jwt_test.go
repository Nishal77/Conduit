package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testIssuer = "https://conduit"
const testSecret = "some-secret-at-least-32-characters-long"

func TestJWTValidator_IssueAndValidateRoundTrip(t *testing.T) {
	v := NewJWTValidator(testSecret, testIssuer)

	tenantID := "11111111-1111-1111-1111-111111111111"
	token, err := v.IssueAccessToken(tenantID, "agent-1", []string{"mcp:call"}, time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	gotTenantID, err := v.Validate(context.Background(), token)
	require.NoError(t, err)
	assert.Equal(t, tenantID, gotTenantID)
}

func TestJWTValidator_RejectsGarbage(t *testing.T) {
	v := NewJWTValidator(testSecret, testIssuer)
	_, err := v.Validate(context.Background(), "not-a-jwt")
	assert.ErrorIs(t, err, ErrInvalidJWT)
}

func TestJWTValidator_RejectsWrongSignature(t *testing.T) {
	issuer := NewJWTValidator(testSecret, testIssuer)
	token, err := issuer.IssueAccessToken("tenant-1", "agent-1", []string{"mcp:call"}, time.Hour)
	require.NoError(t, err)

	wrongKeyValidator := NewJWTValidator("a-completely-different-secret-key!!", testIssuer)
	_, err = wrongKeyValidator.Validate(context.Background(), token)
	assert.ErrorIs(t, err, ErrInvalidJWT)
}

func TestJWTValidator_RejectsWrongIssuer(t *testing.T) {
	issuer := NewJWTValidator(testSecret, "https://not-conduit")
	token, err := issuer.IssueAccessToken("tenant-1", "agent-1", []string{"mcp:call"}, time.Hour)
	require.NoError(t, err)

	validator := NewJWTValidator(testSecret, testIssuer)
	_, err = validator.Validate(context.Background(), token)
	assert.ErrorIs(t, err, ErrInvalidJWT)
}

func TestJWTValidator_RejectsExpiredToken(t *testing.T) {
	v := NewJWTValidator(testSecret, testIssuer)
	token, err := v.IssueAccessToken("tenant-1", "agent-1", []string{"mcp:call"}, -time.Hour)
	require.NoError(t, err)

	_, err = v.Validate(context.Background(), token)
	assert.ErrorIs(t, err, ErrExpiredJWT)
}

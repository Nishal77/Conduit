package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// ErrInvalidJWT is returned when a JWT fails signature verification, has a
// malformed structure, or is missing a required claim.
var ErrInvalidJWT = errors.New("invalid JWT")

// ErrExpiredJWT is returned when a JWT's exp claim has passed.
var ErrExpiredJWT = errors.New("JWT has expired")

// jwtIssuer is the conduit-internal audience value every access token
// carries — spec/12-oauth.md §5 uses the literal "conduit".
const jwtAudience = "conduit"

// JWTValidator validates and issues JWTs for Conduit's OAuth 2.0 server
// (spec/12-oauth.md). Tokens are signed with HS256 using a secret shared
// between the issuer and validator — both are the same Conduit process, so
// there's no need for asymmetric keys until Phase 8's federation work.
type JWTValidator struct {
	secretKey []byte
	issuer    string
}

// NewJWTValidator returns a JWTValidator configured with the HMAC signing
// secret and issuer JWTs must present.
func NewJWTValidator(secret, issuer string) *JWTValidator {
	return &JWTValidator{secretKey: []byte(secret), issuer: issuer}
}

// JWTClaims is the parsed, verified claim set from an access token
// (spec/12-oauth.md §5), returned by ParseClaims for callers — like
// /oauth/introspect — that need more than just the tenant_id Validate
// returns.
type JWTClaims struct {
	Issuer    string
	Subject   string
	TenantID  string
	Scope     string
	IssuedAt  int64
	ExpiresAt int64
	JWTID     string
}

// Validate parses and validates a JWT, returning the tenant_id claim. It's
// a thin wrapper over ParseClaims for the common case (internal/proxy's
// and internal/api's auth middleware only need the tenant_id).
func (v *JWTValidator) Validate(_ context.Context, rawJWT string) (tenantID string, err error) {
	claims, err := v.ParseClaims(rawJWT)
	if err != nil {
		return "", err
	}
	return claims.TenantID, nil
}

// ParseClaims parses and validates a JWT, returning its full claim set.
//
// Algorithm (spec/12-oauth.md §5):
//  1. Parse the JWT and verify its HS256 signature using secretKey
//  2. Check exp (must be in the future) — jwx's WithValidate does this
//  3. Check iss matches the configured issuer
//  4. Extract the required tenant_id string claim
func (v *JWTValidator) ParseClaims(rawJWT string) (*JWTClaims, error) {
	token, err := jwt.Parse([]byte(rawJWT),
		jwt.WithKey(jwa.HS256, v.secretKey),
		jwt.WithValidate(true),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired()) {
			return nil, ErrExpiredJWT
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidJWT, err)
	}

	if token.Issuer() != v.issuer {
		return nil, fmt.Errorf("%w: unexpected issuer %q", ErrInvalidJWT, token.Issuer())
	}

	tenantID, ok := stringClaim(token, "tenant_id")
	if !ok {
		return nil, fmt.Errorf("%w: missing tenant_id claim", ErrInvalidJWT)
	}
	scope, _ := stringClaim(token, "scope")

	exp := token.Expiration()
	iat := token.IssuedAt()

	return &JWTClaims{
		Issuer:    token.Issuer(),
		Subject:   token.Subject(),
		TenantID:  tenantID,
		Scope:     scope,
		IssuedAt:  iat.Unix(),
		ExpiresAt: exp.Unix(),
		JWTID:     token.JwtID(),
	}, nil
}

// stringClaim reads a custom claim and type-asserts it to a non-empty
// string, the shape both tenant_id and scope are always encoded as.
func stringClaim(token jwt.Token, name string) (string, bool) {
	raw, ok := token.Get(name)
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	return s, ok && s != ""
}

// IssueAccessToken creates a new access JWT for a given tenant + scopes,
// signed with secretKey. Used by the OAuth token endpoint for every grant
// type (spec/12-oauth.md §4).
func (v *JWTValidator) IssueAccessToken(tenantID, subject string, scopes []string, ttl time.Duration) (string, error) {
	now := time.Now()

	builder := jwt.NewBuilder().
		Issuer(v.issuer).
		Subject(subject).
		Audience([]string{jwtAudience}).
		IssuedAt(now).
		Expiration(now.Add(ttl)).
		JwtID(uuid.NewString()).
		Claim("tenant_id", tenantID).
		Claim("scope", strings.Join(scopes, " "))

	token, err := builder.Build()
	if err != nil {
		return "", fmt.Errorf("build jwt: %w", err)
	}

	signed, err := jwt.Sign(token, jwt.WithKey(jwa.HS256, v.secretKey))
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return string(signed), nil
}

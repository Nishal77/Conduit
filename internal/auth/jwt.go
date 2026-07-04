package auth

import (
	"context"
	"errors"
)

// ErrJWTNotSupported is returned by JWTValidator.Validate until Phase 5
// wires up Conduit's own OAuth server. A bearer token that isn't shaped
// like an API key (see IsAPIKey) is assumed to be a JWT and routed here;
// today that always fails closed rather than silently accepting an
// unverified token.
var ErrJWTNotSupported = errors.New("JWT authentication not yet configured")

// ErrInvalidJWT is returned by JWTValidator.Validate for a JWT that fails
// signature, issuer, or claim validation. Reserved for the Phase 5
// implementation.
var ErrInvalidJWT = errors.New("invalid JWT")

// ErrExpiredJWT is returned by JWTValidator.Validate for a JWT whose exp
// claim has passed. Reserved for the Phase 5 implementation.
var ErrExpiredJWT = errors.New("JWT has expired")

// JWTValidator will validate JWTs issued by Conduit's own OAuth 2.0 server
// (Phase 5: authorization code + PKCE + client credentials, see
// spec/12-oauth.md). Every request routed here today gets ErrJWTNotSupported
// — the type exists now so internal/proxy's auth wiring never needs to
// change shape when Phase 5 fills in real signature verification.
type JWTValidator struct {
	secretKey []byte
	issuer    string
}

// NewJWTValidator returns a JWTValidator configured with the HMAC signing
// secret and issuer JWTs must present. Phase 1-4: accepted for interface
// stability, unused until Validate has a real implementation.
func NewJWTValidator(secret, issuer string) *JWTValidator {
	return &JWTValidator{secretKey: []byte(secret), issuer: issuer}
}

// Validate always returns ErrJWTNotSupported today. See spec/05-auth.md §5
// for the full Phase 5 algorithm (parse via lestrrat-go/jwx, verify HS256
// signature, check exp/iss, extract tenant_id claim).
func (v *JWTValidator) Validate(_ context.Context, _ string) (tenantID string, err error) {
	return "", ErrJWTNotSupported
}

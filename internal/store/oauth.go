package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OAuthApplication matches a row of the oauth_applications table.
// ClientSecret is always a bcrypt hash — the raw secret is only ever
// returned to the caller once, at creation or rotation time.
type OAuthApplication struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Name         string
	ClientID     string
	ClientSecret string
	RedirectURIs []string
	GrantTypes   []string
	Scopes       []string
	CreatedAt    time.Time
}

// OAuthApplicationStore provides OAuth client application data access.
type OAuthApplicationStore struct {
	db *DB
}

// NewOAuthApplicationStore returns an OAuthApplicationStore backed by db.
func NewOAuthApplicationStore(db *DB) *OAuthApplicationStore { return &OAuthApplicationStore{db: db} }

const oauthAppColumns = "id, tenant_id, name, client_id, client_secret, redirect_uris, grant_types, scopes, created_at"

func scanOAuthApp(row pgx.Row) (*OAuthApplication, error) {
	var a OAuthApplication
	if err := row.Scan(&a.ID, &a.TenantID, &a.Name, &a.ClientID, &a.ClientSecret,
		&a.RedirectURIs, &a.GrantTypes, &a.Scopes, &a.CreatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// GetByClientID looks up an application by its public client_id — the hot
// path for every token and authorize request.
func (s *OAuthApplicationStore) GetByClientID(ctx context.Context, clientID string) (*OAuthApplication, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+oauthAppColumns+`
		FROM oauth_applications
		WHERE client_id = $1
	`, clientID)

	app, err := scanOAuthApp(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get oauth application by client_id: %w", err)
	}
	return app, nil
}

// GetByID looks up an application by its primary key.
func (s *OAuthApplicationStore) GetByID(ctx context.Context, id uuid.UUID) (*OAuthApplication, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+oauthAppColumns+`
		FROM oauth_applications
		WHERE id = $1
	`, id)

	app, err := scanOAuthApp(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get oauth application: %w", err)
	}
	return app, nil
}

// ListByTenant returns every OAuth application registered for a tenant.
func (s *OAuthApplicationStore) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]*OAuthApplication, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT `+oauthAppColumns+`
		FROM oauth_applications
		WHERE tenant_id = $1
		ORDER BY created_at DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list oauth applications: %w", err)
	}
	defer rows.Close()

	var apps []*OAuthApplication
	for rows.Next() {
		a, err := scanOAuthApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan oauth application: %w", err)
		}
		apps = append(apps, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return apps, nil
}

// CreateOAuthAppInput carries the fields needed to register a new OAuth
// application. ClientSecretHash is the already-bcrypt-hashed secret — this
// store never sees the raw one.
type CreateOAuthAppInput struct {
	TenantID         uuid.UUID
	Name             string
	ClientID         string
	ClientSecretHash string
	RedirectURIs     []string
	GrantTypes       []string
	Scopes           []string
}

// Create inserts a new OAuth application.
func (s *OAuthApplicationStore) Create(ctx context.Context, input CreateOAuthAppInput) (*OAuthApplication, error) {
	if len(input.GrantTypes) == 0 {
		input.GrantTypes = []string{"authorization_code"}
	}
	if len(input.Scopes) == 0 {
		input.Scopes = []string{"mcp:call"}
	}

	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO oauth_applications (tenant_id, name, client_id, client_secret, redirect_uris, grant_types, scopes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+oauthAppColumns+`
	`, input.TenantID, input.Name, input.ClientID, input.ClientSecretHash, input.RedirectURIs, input.GrantTypes, input.Scopes)

	app, err := scanOAuthApp(row)
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("create oauth application: %w", err)
	}
	return app, nil
}

// OAuthAppUpdates carries the SET-only-changed fields for
// OAuthApplicationStore.Update.
type OAuthAppUpdates struct {
	Name         *string
	RedirectURIs []string
	Scopes       []string
}

// Update modifies an application's name, redirect URIs, and/or scopes.
func (s *OAuthApplicationStore) Update(ctx context.Context, id uuid.UUID, updates OAuthAppUpdates) (*OAuthApplication, error) {
	row := s.db.Pool.QueryRow(ctx, `
		UPDATE oauth_applications
		SET
			name          = COALESCE($2, name),
			redirect_uris = COALESCE($3, redirect_uris),
			scopes        = COALESCE($4, scopes)
		WHERE id = $1
		RETURNING `+oauthAppColumns+`
	`, id, updates.Name, updates.RedirectURIs, updates.Scopes)

	app, err := scanOAuthApp(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update oauth application: %w", err)
	}
	return app, nil
}

// RotateSecret replaces an application's client_secret hash — used by the
// "rotate-secret" management API endpoint, which generates a new raw
// secret and hands this store only its bcrypt hash.
func (s *OAuthApplicationStore) RotateSecret(ctx context.Context, id uuid.UUID, newSecretHash string) (*OAuthApplication, error) {
	row := s.db.Pool.QueryRow(ctx, `
		UPDATE oauth_applications SET client_secret = $2
		WHERE id = $1
		RETURNING `+oauthAppColumns+`
	`, id, newSecretHash)

	app, err := scanOAuthApp(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rotate oauth application secret: %w", err)
	}
	return app, nil
}

// Delete removes an OAuth application.
func (s *OAuthApplicationStore) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool.Exec(ctx, `DELETE FROM oauth_applications WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete oauth application: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------
// Authorization codes
// ---------------------------------------------------------------------

// OAuthAuthCode matches a row of the oauth_auth_codes table.
type OAuthAuthCode struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	AppID         uuid.UUID
	CodeHash      string
	RedirectURI   string
	Scopes        []string
	CodeChallenge *string
	Used          bool
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

// OAuthAuthCodeStore provides authorization code data access.
type OAuthAuthCodeStore struct {
	db *DB
}

// NewOAuthAuthCodeStore returns an OAuthAuthCodeStore backed by db.
func NewOAuthAuthCodeStore(db *DB) *OAuthAuthCodeStore { return &OAuthAuthCodeStore{db: db} }

const oauthAuthCodeColumns = "id, tenant_id, app_id, code_hash, redirect_uri, scopes, code_challenge, used, expires_at, created_at"

func scanOAuthAuthCode(row pgx.Row) (*OAuthAuthCode, error) {
	var c OAuthAuthCode
	if err := row.Scan(&c.ID, &c.TenantID, &c.AppID, &c.CodeHash, &c.RedirectURI,
		&c.Scopes, &c.CodeChallenge, &c.Used, &c.ExpiresAt, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateAuthCodeInput carries the fields needed to record a newly issued
// authorization code.
type CreateAuthCodeInput struct {
	TenantID      uuid.UUID
	AppID         uuid.UUID
	CodeHash      string
	RedirectURI   string
	Scopes        []string
	CodeChallenge string
	TTL           time.Duration
}

// Create inserts a new authorization code record.
func (s *OAuthAuthCodeStore) Create(ctx context.Context, input CreateAuthCodeInput) (*OAuthAuthCode, error) {
	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO oauth_auth_codes (tenant_id, app_id, code_hash, redirect_uri, scopes, code_challenge, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+oauthAuthCodeColumns+`
	`, input.TenantID, input.AppID, input.CodeHash, input.RedirectURI, input.Scopes,
		input.CodeChallenge, time.Now().Add(input.TTL))

	code, err := scanOAuthAuthCode(row)
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("create oauth auth code: %w", err)
	}
	return code, nil
}

// GetValidByHash looks up an authorization code by its SHA-256 hash,
// returning ErrNotFound if it doesn't exist, has already been used, or has
// expired — spec/12-oauth.md §4 treats all three as the single
// "invalid_grant" outcome, so this store method collapses them into one
// sentinel rather than making the caller distinguish (which would leak
// which case applied, an oracle for guessing codes).
func (s *OAuthAuthCodeStore) GetValidByHash(ctx context.Context, codeHash string) (*OAuthAuthCode, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+oauthAuthCodeColumns+`
		FROM oauth_auth_codes
		WHERE code_hash = $1 AND used = false AND expires_at > NOW()
	`, codeHash)

	code, err := scanOAuthAuthCode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get oauth auth code: %w", err)
	}
	return code, nil
}

// MarkUsed flags a code as consumed so it can never be exchanged twice.
func (s *OAuthAuthCodeStore) MarkUsed(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool.Exec(ctx, `UPDATE oauth_auth_codes SET used = true WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("mark oauth auth code used: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------
// Refresh tokens
// ---------------------------------------------------------------------

// OAuthRefreshToken matches a row of the oauth_refresh_tokens table.
type OAuthRefreshToken struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	AppID     uuid.UUID
	TokenHash string
	Scopes    []string
	Revoked   bool
	ExpiresAt time.Time
	CreatedAt time.Time
}

// OAuthRefreshTokenStore provides refresh token data access.
type OAuthRefreshTokenStore struct {
	db *DB
}

// NewOAuthRefreshTokenStore returns an OAuthRefreshTokenStore backed by db.
func NewOAuthRefreshTokenStore(db *DB) *OAuthRefreshTokenStore {
	return &OAuthRefreshTokenStore{db: db}
}

const oauthRefreshTokenColumns = "id, tenant_id, app_id, token_hash, scopes, revoked, expires_at, created_at"

func scanOAuthRefreshToken(row pgx.Row) (*OAuthRefreshToken, error) {
	var t OAuthRefreshToken
	if err := row.Scan(&t.ID, &t.TenantID, &t.AppID, &t.TokenHash, &t.Scopes, &t.Revoked, &t.ExpiresAt, &t.CreatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateRefreshTokenInput carries the fields needed to record a newly
// issued refresh token.
type CreateRefreshTokenInput struct {
	TenantID  uuid.UUID
	AppID     uuid.UUID
	TokenHash string
	Scopes    []string
	TTL       time.Duration
}

// Create inserts a new refresh token record.
func (s *OAuthRefreshTokenStore) Create(ctx context.Context, input CreateRefreshTokenInput) (*OAuthRefreshToken, error) {
	row := s.db.Pool.QueryRow(ctx, `
		INSERT INTO oauth_refresh_tokens (tenant_id, app_id, token_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+oauthRefreshTokenColumns+`
	`, input.TenantID, input.AppID, input.TokenHash, input.Scopes, time.Now().Add(input.TTL))

	token, err := scanOAuthRefreshToken(row)
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, fmt.Errorf("create oauth refresh token: %w", err)
	}
	return token, nil
}

// GetValidByHash looks up a non-revoked, non-expired refresh token by its
// SHA-256 hash. See OAuthAuthCodeStore.GetValidByHash for why invalid,
// revoked, and expired all collapse into ErrNotFound.
func (s *OAuthRefreshTokenStore) GetValidByHash(ctx context.Context, tokenHash string) (*OAuthRefreshToken, error) {
	row := s.db.Pool.QueryRow(ctx, `
		SELECT `+oauthRefreshTokenColumns+`
		FROM oauth_refresh_tokens
		WHERE token_hash = $1 AND revoked = false AND expires_at > NOW()
	`, tokenHash)

	token, err := scanOAuthRefreshToken(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get oauth refresh token: %w", err)
	}
	return token, nil
}

// Revoke marks a refresh token as revoked — called both explicitly (POST
// /oauth/revoke) and implicitly on every use, per refresh token rotation
// (spec/12-oauth.md §7).
func (s *OAuthRefreshTokenStore) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Pool.Exec(ctx, `UPDATE oauth_refresh_tokens SET revoked = true WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("revoke oauth refresh token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/conduit-oss/conduit/internal/api/httpx"
	"github.com/conduit-oss/conduit/internal/auth"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// OAuthHandler implements the OAuth 2.0 endpoints from spec/12-oauth.md §2:
// /oauth/authorize, /oauth/token, /oauth/introspect, /oauth/revoke, and the
// RFC 8414 metadata document. All of them live on the management API
// (:8081), outside the API-key-required route group server.go sets up for
// /api/v1 — an OAuth client has no Conduit API key yet; that's the whole
// point of the token endpoint.
type OAuthHandler struct {
	oauth        *auth.OAuthServer
	apps         *store.OAuthApplicationStore
	keyValidator *auth.APIKeyValidator
	issuer       string
}

// oauthErrorResponse is RFC 6749 §5.2's error shape — deliberately not
// httpx.ErrorResponse, since OAuth clients are written against the
// standard, not Conduit's own conventions.
type oauthErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	httpx.WriteJSON(w, status, oauthErrorResponse{Error: code, ErrorDescription: description})
}

// writeOAuthOrInternalError translates an *auth.OAuthError into the RFC
// 6749 wire format, or falls back to a generic 500 for anything else
// (a database outage, not a client mistake).
func writeOAuthOrInternalError(w http.ResponseWriter, err error) {
	var oauthErr *auth.OAuthError
	if errors.As(err, &oauthErr) {
		writeOAuthError(w, http.StatusBadRequest, oauthErr.Code, oauthErr.Description)
		return
	}
	writeOAuthError(w, http.StatusInternalServerError, "server_error", "an unexpected error occurred")
}

// Authorize handles GET /oauth/authorize (spec/12-oauth.md §3).
//
// Conduit has no end-user account/session model yet (that's Phase 8's SSO
// work), so there's no meaningful login or consent page to render. Instead,
// the caller identifies who is approving the request with the same Conduit
// API key used everywhere else, passed as ?api_key= (a query parameter,
// not a header, since this endpoint is reached via browser redirect). The
// request is auto-approved once that key's tenant matches the OAuth
// application's tenant — equivalent to "this app is already trusted by
// this tenant," which is as much consent as a single-tenant API key can
// express today.
func (h *OAuthHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	responseType := q.Get("response_type")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")
	state := q.Get("state")
	scope := q.Get("scope")
	apiKey := q.Get("api_key")

	// Everything up through PKCE method is validated before we trust
	// redirect_uri enough to send the browser back there — an attacker
	// supplying an unregistered redirect_uri must never receive a
	// redirect, even an error one (RFC 6749 §4.1.2.1).
	app, err := h.oauth.ValidateAuthorizeRequest(r.Context(), clientID, redirectURI, responseType, codeChallengeMethod)
	if err != nil {
		writeOAuthOrInternalError(w, err)
		return
	}
	if codeChallenge == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code_challenge is required")
		return
	}

	deny := func(reason string) {
		redirectWithError(w, r, redirectURI, reason, state)
	}

	if apiKey == "" {
		deny("access_denied")
		return
	}
	tenantID, err := h.keyValidator.Validate(r.Context(), apiKey)
	if err != nil || tenantID != app.TenantID.String() {
		deny("access_denied")
		return
	}

	scopes := strings.Fields(scope)
	if len(scopes) == 0 {
		scopes = app.Scopes
	}

	code, err := h.oauth.IssueAuthorizationCode(r.Context(), app, redirectURI, scopes, codeChallenge)
	if err != nil {
		writeOAuthOrInternalError(w, err)
		return
	}

	dest, _ := url.Parse(redirectURI)
	values := dest.Query()
	values.Set("code", code)
	if state != "" {
		values.Set("state", state)
	}
	dest.RawQuery = values.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

func redirectWithError(w http.ResponseWriter, r *http.Request, redirectURI, errCode, state string) {
	dest, err := url.Parse(redirectURI)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed redirect_uri")
		return
	}
	values := dest.Query()
	values.Set("error", errCode)
	if state != "" {
		values.Set("state", state)
	}
	dest.RawQuery = values.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// Token handles POST /oauth/token (spec/12-oauth.md §4). Per RFC 6749 §2.3.1
// and the client_secret_basic/client_secret_post methods this endpoint's
// well-known metadata advertises, the client may authenticate either via
// the HTTP Basic Authorization header or via client_id/client_secret form
// fields — clientCredentialsFromRequest checks both, preferring Basic auth
// when present.
func (h *OAuthHandler) Token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	clientID, clientSecret := clientCredentialsFromRequest(r)

	grantType := r.FormValue("grant_type")
	var (
		tokens *auth.TokenResponse
		err    error
	)

	switch grantType {
	case "authorization_code":
		tokens, err = h.oauth.ExchangeAuthorizationCode(r.Context(),
			r.FormValue("code"), r.FormValue("redirect_uri"), clientID, r.FormValue("code_verifier"))

	case "client_credentials":
		var scopes []string
		if s := r.FormValue("scope"); s != "" {
			scopes = strings.Fields(s)
		}
		tokens, err = h.oauth.ClientCredentials(r.Context(), clientID, clientSecret, scopes)

	case "refresh_token":
		tokens, err = h.oauth.RefreshToken(r.Context(), r.FormValue("refresh_token"), clientID)

	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", fmt.Sprintf("grant_type %q is not supported", grantType))
		return
	}

	if err != nil {
		writeOAuthOrInternalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, tokens)
}

// clientCredentialsFromRequest extracts client_id/client_secret from either
// the HTTP Basic Authorization header (client_secret_basic) or the request
// body (client_secret_post), per RFC 6749 §2.3.1. Basic auth takes
// precedence when both are somehow present.
func clientCredentialsFromRequest(r *http.Request) (clientID, clientSecret string) {
	if id, secret, ok := r.BasicAuth(); ok {
		return id, secret
	}
	return r.FormValue("client_id"), r.FormValue("client_secret")
}

// Introspect handles POST /oauth/introspect (RFC 7662, spec/12-oauth.md §8).
// Requires the calling client to authenticate via HTTP Basic auth.
func (h *OAuthHandler) Introspect(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateClient(r) {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}

	resp, err := h.oauth.Introspect(r.Context(), r.FormValue("token"))
	if err != nil {
		writeOAuthOrInternalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// Revoke handles POST /oauth/revoke (RFC 7009).
func (h *OAuthHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateClient(r) {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}

	if err := h.oauth.Revoke(r.Context(), r.FormValue("token")); err != nil {
		writeOAuthOrInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// authenticateClient verifies the Basic auth credentials RFC 7662/7009
// require on introspection and revocation requests.
func (h *OAuthHandler) authenticateClient(r *http.Request) bool {
	clientID, clientSecret, ok := r.BasicAuth()
	if !ok {
		return false
	}
	app, err := h.apps.GetByClientID(r.Context(), clientID)
	if err != nil {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(app.ClientSecret), []byte(clientSecret)) == nil
}

// WellKnownMetadata handles GET /.well-known/oauth-authorization-server
// (RFC 8414).
func (h *OAuthHandler) WellKnownMetadata(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"issuer":                                h.issuer,
		"authorization_endpoint":                h.issuer + "/oauth/authorize",
		"token_endpoint":                        h.issuer + "/oauth/token",
		"introspection_endpoint":                h.issuer + "/oauth/introspect",
		"revocation_endpoint":                   h.issuer + "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "client_credentials", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"scopes_supported":                      []string{"mcp:call"},
	})
}

// ---------------------------------------------------------------------
// OAuth application management — /api/v1/oauth/applications
// ---------------------------------------------------------------------

// OAuthAppsHandler implements /api/v1/oauth/applications (spec/12-oauth.md §9).
type OAuthAppsHandler struct {
	apps    *store.OAuthApplicationStore
	tenants *store.TenantStore
}

type oauthAppJSON struct {
	ID           string   `json:"id"`
	TenantID     string   `json:"tenant_id"`
	Name         string   `json:"name"`
	ClientID     string   `json:"client_id"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types"`
	Scopes       []string `json:"scopes"`
	CreatedAt    string   `json:"created_at"`
}

func toOAuthAppJSON(a *store.OAuthApplication) oauthAppJSON {
	return oauthAppJSON{
		ID:           a.ID.String(),
		TenantID:     a.TenantID.String(),
		Name:         a.Name,
		ClientID:     a.ClientID,
		RedirectURIs: a.RedirectURIs,
		GrantTypes:   a.GrantTypes,
		Scopes:       a.Scopes,
		CreatedAt:    a.CreatedAt.Format(rfc3339),
	}
}

type oauthAppWithSecretJSON struct {
	oauthAppJSON
	ClientSecret string `json:"client_secret"`
}

// List handles GET /api/v1/oauth/applications?tenant_id={id}.
func (h *OAuthAppsHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := parseUUIDQuery(w, r, "tenant_id")
	if !ok {
		return
	}
	apps, err := h.apps.ListByTenant(r.Context(), tenantID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to list oauth applications")
		return
	}
	items := make([]oauthAppJSON, len(apps))
	for i, a := range apps {
		items[i] = toOAuthAppJSON(a)
	}
	httpx.WriteJSON(w, http.StatusOK, listResponse[oauthAppJSON]{Items: items, Total: int64(len(items)), Limit: len(items)})
}

type createOAuthAppRequest struct {
	TenantID     string   `json:"tenant_id"`
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types"`
	Scopes       []string `json:"scopes"`
}

// Create handles POST /api/v1/oauth/applications.
func (h *OAuthAppsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createOAuthAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}

	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		httpx.WriteValidationError(w, r, map[string]string{"tenant_id": "must be a UUID"})
		return
	}
	if req.Name == "" {
		httpx.WriteValidationError(w, r, map[string]string{"name": "is required"})
		return
	}
	if len(req.RedirectURIs) == 0 {
		httpx.WriteValidationError(w, r, map[string]string{"redirect_uris": "at least one is required"})
		return
	}
	if _, err := h.tenants.GetByID(r.Context(), tenantID); errors.Is(err, store.ErrNotFound) {
		httpx.WriteValidationError(w, r, map[string]string{"tenant_id": "tenant not found"})
		return
	}

	rawSecret, secretHash, err := auth.GenerateClientSecret()
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to generate client secret")
		return
	}

	app, err := h.apps.Create(r.Context(), store.CreateOAuthAppInput{
		TenantID:         tenantID,
		Name:             req.Name,
		ClientID:         "cnd_client_" + uuid.NewString(),
		ClientSecretHash: secretHash,
		RedirectURIs:     req.RedirectURIs,
		GrantTypes:       req.GrantTypes,
		Scopes:           req.Scopes,
	})
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to create oauth application")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, oauthAppWithSecretJSON{oauthAppJSON: toOAuthAppJSON(app), ClientSecret: rawSecret})
}

type updateOAuthAppRequest struct {
	Name         *string  `json:"name"`
	RedirectURIs []string `json:"redirect_uris"`
	Scopes       []string `json:"scopes"`
}

// Update handles PATCH /api/v1/oauth/applications/{id}.
func (h *OAuthAppsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var req updateOAuthAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}

	app, err := h.apps.Update(r.Context(), id, store.OAuthAppUpdates{
		Name:         req.Name,
		RedirectURIs: req.RedirectURIs,
		Scopes:       req.Scopes,
	})
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "oauth application not found")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to update oauth application")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toOAuthAppJSON(app))
}

// Delete handles DELETE /api/v1/oauth/applications/{id}.
func (h *OAuthAppsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.apps.Delete(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "oauth application not found")
		return
	} else if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to delete oauth application")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RotateSecret handles POST /api/v1/oauth/applications/{id}/rotate-secret.
func (h *OAuthAppsHandler) RotateSecret(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}

	rawSecret, secretHash, err := auth.GenerateClientSecret()
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to generate client secret")
		return
	}

	app, err := h.apps.RotateSecret(r.Context(), id, secretHash)
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "oauth application not found")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusInternalServerError, "failed to rotate client secret")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, oauthAppWithSecretJSON{oauthAppJSON: toOAuthAppJSON(app), ClientSecret: rawSecret})
}

package store

// Stores bundles every table's store into one struct so callers that need
// several of them — chiefly internal/api's handlers — can take a single
// dependency instead of separate constructor parameters per table.
type Stores struct {
	Tenants      *TenantStore
	APIKeys      *APIKeyStore
	Servers      *MCPServerStore
	RateLimits   *RateLimitStore
	Audit        *AuditStore
	OAuthApps    *OAuthApplicationStore
	OAuthCodes   *OAuthAuthCodeStore
	OAuthRefresh *OAuthRefreshTokenStore
	Plugins      *PluginStore
	Webhooks     *WebhookStore
}

// NewStores builds a Stores bundle backed by db. credentialEncryptionKey
// (see DeriveCredentialKey) encrypts MCP server auth_config at rest; pass
// nil to store it in plaintext (development only).
func NewStores(db *DB, credentialEncryptionKey []byte) *Stores {
	return &Stores{
		Tenants:      NewTenantStore(db),
		APIKeys:      NewAPIKeyStore(db),
		Servers:      NewMCPServerStore(db, credentialEncryptionKey),
		RateLimits:   NewRateLimitStore(db),
		Audit:        NewAuditStore(db),
		OAuthApps:    NewOAuthApplicationStore(db),
		OAuthCodes:   NewOAuthAuthCodeStore(db),
		OAuthRefresh: NewOAuthRefreshTokenStore(db),
		Plugins:      NewPluginStore(db),
		Webhooks:     NewWebhookStore(db),
	}
}

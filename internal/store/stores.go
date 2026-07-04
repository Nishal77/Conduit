package store

// Stores bundles every table's store into one struct so callers that need
// several of them — chiefly internal/api's handlers — can take a single
// dependency instead of five separate constructor parameters.
type Stores struct {
	Tenants    *TenantStore
	APIKeys    *APIKeyStore
	Servers    *MCPServerStore
	RateLimits *RateLimitStore
	Audit      *AuditStore
}

// NewStores builds a Stores bundle backed by db.
func NewStores(db *DB) *Stores {
	return &Stores{
		Tenants:    NewTenantStore(db),
		APIKeys:    NewAPIKeyStore(db),
		Servers:    NewMCPServerStore(db),
		RateLimits: NewRateLimitStore(db),
		Audit:      NewAuditStore(db),
	}
}

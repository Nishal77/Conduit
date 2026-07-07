// Package handlers implements every internal/api endpoint. Each resource
// (tenants, API keys, servers, ...) gets its own small struct with one
// method per HTTP operation, matching chi's http.HandlerFunc signature —
// aggregated into Handlers so internal/api/server.go can wire routes with
// h.Tenants.List, h.APIKeys.Create, and so on.
package handlers

import (
	"github.com/conduit-oss/conduit/internal/auth"
	"github.com/conduit-oss/conduit/internal/store"
	"github.com/conduit-oss/conduit/internal/tenant"
	"github.com/rs/zerolog"
)

// Handlers aggregates every resource's handler struct.
type Handlers struct {
	Health     *HealthHandler
	Tenants    *TenantsHandler
	APIKeys    *APIKeysHandler
	Servers    *ServersHandler
	RateLimits *RateLimitsHandler
	Audit      *AuditHandler
	OAuth      *OAuthHandler
	OAuthApps  *OAuthAppsHandler
	Plugins    *PluginsHandler
	Webhooks   *WebhooksHandler
}

// New builds every handler, wiring in the shared stores and logger they
// need. The management API only ever reads audit history (the proxy is the
// only writer, on its own hot path), so Handlers takes no audit.Writer.
// routing may be nil (e.g. in tests that don't run the proxy's routing
// loader); when set, tenant and server mutations invalidate it immediately
// per spec/13-multitenant.md §7.
func New(stores *store.Stores, oauthServer *auth.OAuthServer, keyValidator *auth.APIKeyValidator, issuer string, routing *tenant.Store, log zerolog.Logger) *Handlers {
	return &Handlers{
		Health:     &HealthHandler{},
		Tenants:    &TenantsHandler{tenants: stores.Tenants, routing: routing},
		APIKeys:    &APIKeysHandler{apiKeys: stores.APIKeys, tenants: stores.Tenants},
		Servers:    &ServersHandler{servers: stores.Servers, routing: routing, log: log},
		RateLimits: &RateLimitsHandler{rateLimits: stores.RateLimits},
		Audit:      &AuditHandler{audit: stores.Audit, log: log},
		OAuth:      &OAuthHandler{oauth: oauthServer, apps: stores.OAuthApps, keyValidator: keyValidator, issuer: issuer},
		OAuthApps:  &OAuthAppsHandler{apps: stores.OAuthApps, tenants: stores.Tenants},
		Plugins:    &PluginsHandler{plugins: stores.Plugins},
		Webhooks:   &WebhooksHandler{webhooks: stores.Webhooks},
	}
}

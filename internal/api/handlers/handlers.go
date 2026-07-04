// Package handlers implements every internal/api endpoint. Each resource
// (tenants, API keys, servers, ...) gets its own small struct with one
// method per HTTP operation, matching chi's http.HandlerFunc signature —
// aggregated into Handlers so internal/api/server.go can wire routes with
// h.Tenants.List, h.APIKeys.Create, and so on.
//
// Not present here: webhooks, oauth applications, and plugins handlers,
// even though spec/10-api.md documents their routes. Those endpoints read
// and write tables (webhook_configs, oauth_applications, tenant_plugins)
// that don't exist until migrations 000003 (Phase 5) and 000004 (Phase 6)
// — see spec/00-overview.md's build order ("database migrations" is step 1
// within a phase, before any handler that depends on them). They're added
// alongside their respective phases instead of as empty stubs now.
package handlers

import (
	"github.com/conduit-oss/conduit/internal/store"
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
}

// New builds every handler, wiring in the shared stores and logger they
// need. The management API only ever reads audit history (the proxy is the
// only writer, on its own hot path), so Handlers takes no audit.Writer.
func New(stores *store.Stores, log zerolog.Logger) *Handlers {
	return &Handlers{
		Health:     &HealthHandler{},
		Tenants:    &TenantsHandler{tenants: stores.Tenants},
		APIKeys:    &APIKeysHandler{apiKeys: stores.APIKeys, tenants: stores.Tenants},
		Servers:    &ServersHandler{servers: stores.Servers, log: log},
		RateLimits: &RateLimitsHandler{rateLimits: stores.RateLimits},
		Audit:      &AuditHandler{audit: stores.Audit, log: log},
	}
}

// Package tenant resolves an incoming proxy request's (tenant, server) pair
// to the upstream MCP server it should be forwarded to.
//
// Per ADR-004 (see CLAUDE.md §5), tenant_id is never trusted from the
// request body or URL — the proxy path segment only selects *which*
// upstream to route to; the *authorization* to act as that tenant comes
// from the auth middleware, which resolves tenant_id from the validated API
// key or JWT.
//
// This is the Phase 1 in-memory implementation: a static routing table
// loaded at startup. Phase 2/5 replace the loader with one that reads
// mcp_servers from PostgreSQL and refreshes it on a 5s ticker, but the
// Router type and its Resolve contract stay the same so the proxy package
// never has to change.
package tenant

import (
	"context"
	"fmt"
	"math/rand"
	"sync"

	"github.com/google/uuid"
)

// Server describes a single upstream MCP server registered to a tenant.
// TenantID, AuthType, AuthConfig, and Weight are populated from
// mcp_servers starting in Phase 5 — see store.MCPServerWithTenantSlug and
// spec/13-multitenant.md §4 (weighted selection) and §6 (upstream auth).
type Server struct {
	TenantID    uuid.UUID
	TenantSlug  string
	Name        string
	UpstreamURL string
	AuthType    string
	AuthConfig  map[string]any
	Weight      int
	Enabled     bool
}

// ErrServerNotFound is returned by Resolve when no enabled server matches
// the given tenant slug and server name.
var ErrServerNotFound = fmt.Errorf("mcp server not found")

// Router resolves (tenant_slug, server_name) to an upstream Server. It is
// safe for concurrent use: reads (Resolve) take a read lock, writes
// (Register/Remove) take a write lock, so routing never blocks on itself.
//
// A routing key can map to more than one Server — multiple upstreams
// registered under the same (tenant, name) for weighted load balancing
// (spec/13-multitenant.md §4) — so Resolve picks among them with
// WeightedSelect.
type Router struct {
	mu      sync.RWMutex
	servers map[string][]*Server // key: tenantSlug + "/" + serverName
}

// NewRouter returns an empty Router. Callers populate it with Register
// before serving traffic.
func NewRouter() *Router {
	return &Router{servers: make(map[string][]*Server)}
}

// Register adds a routing entry for a server, alongside any other server
// already registered under the same (tenant, name) key.
func (r *Router) Register(srv *Server) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := routeKey(srv.TenantSlug, srv.Name)
	r.servers[key] = append(r.servers[key], srv)
}

// Remove deletes every routing entry for (tenantSlug, serverName), if any.
func (r *Router) Remove(tenantSlug, serverName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.servers, routeKey(tenantSlug, serverName))
}

// Resolve returns an upstream Server for the given tenant slug and server
// name, chosen by WeightedSelect when more than one is registered under
// that key. It returns ErrServerNotFound if no enabled server matches —
// registered-but-disabled and never-registered are the same outcome from
// the caller's point of view (nothing to route to).
func (r *Router) Resolve(_ context.Context, tenantSlug, serverName string) (*Server, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	candidates := r.servers[routeKey(tenantSlug, serverName)]
	enabled := make([]*Server, 0, len(candidates))
	for _, s := range candidates {
		if s.Enabled {
			enabled = append(enabled, s)
		}
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf("%w: tenant=%q server=%q", ErrServerNotFound, tenantSlug, serverName)
	}
	return WeightedSelect(enabled), nil
}

// WeightedSelect picks a server at random from servers, weighted by each
// server's Weight field: a server with twice the weight of another is
// twice as likely to be chosen. A zero total weight (e.g. every candidate
// has Weight 0) falls back to a uniform pick over all candidates rather
// than panicking on rand.Intn(0).
func WeightedSelect(servers []*Server) *Server {
	if len(servers) == 1 {
		return servers[0]
	}

	total := 0
	for _, s := range servers {
		total += s.Weight
	}
	if total <= 0 {
		return servers[rand.Intn(len(servers))]
	}

	r := rand.Intn(total)
	cumulative := 0
	for _, s := range servers {
		cumulative += s.Weight
		if r < cumulative {
			return s
		}
	}
	return servers[len(servers)-1]
}

// Len reports how many routing keys are currently registered, regardless
// of enabled state. Used by the /readyz handler's routing-table check.
func (r *Router) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.servers)
}

// ReplaceAll atomically swaps the entire routing table for servers,
// grouping by (tenantSlug, name) so weighted selection has every candidate
// to choose from. Store's periodic refresh (store.go) uses this rather
// than diffing old vs. new: a full swap is simpler to reason about and
// just as cheap at Conduit's expected server-count scale (thousands, not
// millions), and it guarantees a server removed from the database
// disappears from routing on the very next refresh instead of requiring an
// explicit Remove call.
func (r *Router) ReplaceAll(servers []*Server) {
	next := make(map[string][]*Server, len(servers))
	for _, s := range servers {
		key := routeKey(s.TenantSlug, s.Name)
		next[key] = append(next[key], s)
	}
	r.mu.Lock()
	r.servers = next
	r.mu.Unlock()
}

func routeKey(tenantSlug, serverName string) string {
	return tenantSlug + "/" + serverName
}

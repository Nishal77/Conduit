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
	"sync"
)

// Server describes a single upstream MCP server registered to a tenant.
type Server struct {
	TenantSlug  string
	Name        string
	UpstreamURL string
	Enabled     bool
}

// ErrServerNotFound is returned by Resolve when no enabled server matches
// the given tenant slug and server name.
var ErrServerNotFound = fmt.Errorf("mcp server not found")

// Router resolves (tenant_slug, server_name) to an upstream Server. It is
// safe for concurrent use: reads (Resolve) take a read lock, writes
// (Register/Remove) take a write lock, so routing never blocks on itself.
type Router struct {
	mu      sync.RWMutex
	servers map[string]*Server // key: tenantSlug + "/" + serverName
}

// NewRouter returns an empty Router. Callers populate it with Register
// before serving traffic.
func NewRouter() *Router {
	return &Router{servers: make(map[string]*Server)}
}

// Register adds or replaces the routing entry for a server.
func (r *Router) Register(srv *Server) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.servers[routeKey(srv.TenantSlug, srv.Name)] = srv
}

// Remove deletes the routing entry for a server, if present.
func (r *Router) Remove(tenantSlug, serverName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.servers, routeKey(tenantSlug, serverName))
}

// Resolve returns the upstream Server for the given tenant slug and server
// name. It returns ErrServerNotFound if no such server is registered, or if
// it is registered but disabled — from the caller's point of view those are
// the same outcome (nothing to route to).
func (r *Router) Resolve(_ context.Context, tenantSlug, serverName string) (*Server, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	srv, ok := r.servers[routeKey(tenantSlug, serverName)]
	if !ok || !srv.Enabled {
		return nil, fmt.Errorf("%w: tenant=%q server=%q", ErrServerNotFound, tenantSlug, serverName)
	}
	return srv, nil
}

// Len reports how many servers are currently registered, regardless of
// enabled state. Used by the /readyz handler's routing-table check.
func (r *Router) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.servers)
}

// ReplaceAll atomically swaps the entire routing table for servers. Store's
// periodic refresh (store.go) uses this rather than diffing old vs. new: a
// full swap is simpler to reason about and just as cheap at Conduit's
// expected server-count scale (thousands, not millions), and it guarantees
// a server removed from the database disappears from routing on the very
// next refresh instead of requiring an explicit Remove call.
func (r *Router) ReplaceAll(servers []*Server) {
	next := make(map[string]*Server, len(servers))
	for _, s := range servers {
		next[routeKey(s.TenantSlug, s.Name)] = s
	}
	r.mu.Lock()
	r.servers = next
	r.mu.Unlock()
}

func routeKey(tenantSlug, serverName string) string {
	return tenantSlug + "/" + serverName
}

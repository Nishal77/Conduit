package plugin

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/conduit-oss/conduit/internal/mcp"
	"github.com/rs/zerolog/log"
)

// defaultPriority is used when a Registration doesn't set one explicitly.
// Lower priority values run first.
const defaultPriority = 100

// Registration associates a plugin with its execution priority and, for
// multi-tenant deployments, the single tenant it applies to.
type Registration struct {
	Plugin   ConduitPlugin
	Priority int    // lower = runs first (default: 100)
	TenantID string // empty = applies to all tenants
}

// Registry manages every registered plugin and runs the Before/After hook
// chain around a proxied tool call. A Registry with zero registrations is
// valid and behaves as a no-op pass-through — this is the state the proxy
// runs in until Phase 6 wires up real plugins.
type Registry struct {
	mu            sync.RWMutex
	registrations []Registration
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a plugin to the registry. Registrations are kept sorted by
// priority (ascending) so RunBefore/RunAfter never need to sort on the hot
// path.
func (r *Registry) Register(reg Registration) {
	if reg.Priority == 0 {
		reg.Priority = defaultPriority
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registrations = append(r.registrations, reg)
	sort.SliceStable(r.registrations, func(i, j int) bool {
		return r.registrations[i].Priority < r.registrations[j].Priority
	})
}

// ForTenant returns every registration applicable to tenantID: plugins
// registered globally (empty TenantID) plus plugins registered specifically
// for tenantID, in priority order.
func (r *Registry) ForTenant(tenantID string) []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Registration, 0, len(r.registrations))
	for _, reg := range r.registrations {
		if reg.TenantID == "" || reg.TenantID == tenantID {
			out = append(out, reg)
		}
	}
	return out
}

// RunBefore runs all applicable Before hooks in priority order, threading
// the (possibly modified) request through each plugin in turn.
//
// If a plugin returns ErrRequestBlocked, execution stops immediately and
// that error propagates to the caller — the proxy turns this into a 403.
// Any other error is logged and treated as "no change": the chain continues
// with the request as it was before that plugin ran, so one misbehaving
// plugin can't take down the whole request path.
func (r *Registry) RunBefore(ctx context.Context, tenantID string, req *mcp.Message) (*mcp.Message, error) {
	current := req
	for _, reg := range r.ForTenant(tenantID) {
		modified, err := reg.Plugin.Before(ctx, current)
		if err != nil {
			if errors.Is(err, ErrRequestBlocked) {
				return nil, err
			}
			log.Warn().Err(err).Str("plugin", reg.Plugin.Name()).Msg("plugin Before hook failed, continuing with unmodified request")
			continue
		}
		current = modified
	}
	return current, nil
}

// RunAfter runs all applicable After hooks in priority order, threading the
// (possibly modified) response through each plugin in turn. Errors are
// logged and otherwise ignored — a broken After hook must never turn a
// successful upstream call into a failure the agent sees.
func (r *Registry) RunAfter(ctx context.Context, tenantID string, req, resp *mcp.Message) *mcp.Message {
	current := resp
	for _, reg := range r.ForTenant(tenantID) {
		modified, err := reg.Plugin.After(ctx, req, current)
		if err != nil {
			log.Warn().Err(err).Str("plugin", reg.Plugin.Name()).Msg("plugin After hook failed, continuing with unmodified response")
			continue
		}
		current = modified
	}
	return current
}

// Shutdown calls Shutdown on every registered plugin concurrently and waits
// for all of them to finish or for ctx to be cancelled, whichever is first.
// It returns the first error encountered, if any, but always waits for
// every plugin to be given a chance to clean up.
func (r *Registry) Shutdown(ctx context.Context) error {
	r.mu.RLock()
	regs := make([]Registration, len(r.registrations))
	copy(regs, r.registrations)
	r.mu.RUnlock()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for _, reg := range regs {
		wg.Add(1)
		go func(p ConduitPlugin) {
			defer wg.Done()
			if err := p.Shutdown(ctx); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				log.Warn().Err(err).Str("plugin", p.Name()).Msg("plugin shutdown failed")
			}
		}(reg.Plugin)
	}
	wg.Wait()
	return firstErr
}

package tenant

import (
	"context"
	"sync"
	"time"

	"github.com/conduit-oss/conduit/internal/store"
	"github.com/rs/zerolog"
)

// refreshInterval is how often Store reloads the routing table from
// PostgreSQL — CLAUDE.md's ADR-004 calls for a 5s TTL on the in-process
// routing cache so a newly registered server becomes routable without a
// proxy restart, while keeping PostgreSQL off the per-request hot path.
const refreshInterval = 5 * time.Second

// Store keeps a tenant.Router in sync with the mcp_servers table by
// polling PostgreSQL on refreshInterval and swapping in the latest set of
// enabled servers. It is the Phase 2 replacement for the manual
// Router.Register calls Phase 1's --demo-* flags used.
type Store struct {
	router  *Router
	servers *store.MCPServerStore
	log     zerolog.Logger

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewStore returns a Store that will keep router populated from servers.
func NewStore(router *Router, servers *store.MCPServerStore, log zerolog.Logger) *Store {
	return &Store{
		router:  router,
		servers: servers,
		log:     log,
		stopCh:  make(chan struct{}),
	}
}

// Start performs an initial synchronous load (so the routing table is
// populated before the proxy starts accepting traffic) and then refreshes
// it every 5 seconds in the background until Stop is called.
func (s *Store) Start(ctx context.Context) error {
	if err := s.reload(ctx); err != nil {
		return err
	}

	s.wg.Add(1)
	go s.refreshLoop()
	return nil
}

// Stop signals the background refresh loop to exit and waits for it to do
// so. Safe to call even if Start's initial load failed.
func (s *Store) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

func (s *Store) refreshLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), refreshInterval)
			if err := s.reload(ctx); err != nil {
				s.log.Warn().Err(err).Msg("failed to refresh routing table, keeping previous table")
			}
			cancel()
		}
	}
}

// reload fetches every enabled server across every tenant and atomically
// replaces the router's table. A query error leaves the existing table in
// place — a routing table that's briefly stale is far better than one that
// suddenly goes empty because of a transient database blip.
func (s *Store) reload(ctx context.Context) error {
	rows, err := s.servers.ListAllEnabled(ctx)
	if err != nil {
		return err
	}

	servers := make([]*Server, 0, len(rows))
	for _, row := range rows {
		servers = append(servers, &Server{
			TenantSlug:  row.TenantSlug,
			Name:        row.Name,
			UpstreamURL: row.UpstreamURL,
			Enabled:     row.Enabled,
		})
	}

	s.router.ReplaceAll(servers)
	s.log.Debug().Int("server_count", len(servers)).Msg("routing table refreshed")
	return nil
}

package tenant_test

import (
	"context"
	"testing"

	"github.com/conduit-oss/conduit/internal/tenant"
	"github.com/stretchr/testify/require"
)

func TestRouter_ResolveNotFound(t *testing.T) {
	r := tenant.NewRouter()
	_, err := r.Resolve(context.Background(), "acme", "github")
	require.ErrorIs(t, err, tenant.ErrServerNotFound)
}

func TestRouter_ResolveSkipsDisabledServers(t *testing.T) {
	r := tenant.NewRouter()
	r.Register(&tenant.Server{TenantSlug: "acme", Name: "github", UpstreamURL: "http://one", Enabled: false})

	_, err := r.Resolve(context.Background(), "acme", "github")
	require.ErrorIs(t, err, tenant.ErrServerNotFound)
}

func TestRouter_ResolveReturnsOnlyEnabledCandidate(t *testing.T) {
	r := tenant.NewRouter()
	r.Register(&tenant.Server{TenantSlug: "acme", Name: "github", UpstreamURL: "http://disabled", Enabled: false, Weight: 100})
	r.Register(&tenant.Server{TenantSlug: "acme", Name: "github", UpstreamURL: "http://enabled", Enabled: true, Weight: 100})

	srv, err := r.Resolve(context.Background(), "acme", "github")
	require.NoError(t, err)
	require.Equal(t, "http://enabled", srv.UpstreamURL)
}

func TestRouter_ResolveIsScopedPerTenant(t *testing.T) {
	r := tenant.NewRouter()
	r.Register(&tenant.Server{TenantSlug: "acme", Name: "github", UpstreamURL: "http://acme-github", Enabled: true})
	r.Register(&tenant.Server{TenantSlug: "globex", Name: "github", UpstreamURL: "http://globex-github", Enabled: true})

	srv, err := r.Resolve(context.Background(), "acme", "github")
	require.NoError(t, err)
	require.Equal(t, "http://acme-github", srv.UpstreamURL)
}

func TestRouter_ReplaceAllDropsRemovedServers(t *testing.T) {
	r := tenant.NewRouter()
	r.Register(&tenant.Server{TenantSlug: "acme", Name: "github", UpstreamURL: "http://old", Enabled: true})

	r.ReplaceAll([]*tenant.Server{
		{TenantSlug: "acme", Name: "stripe", UpstreamURL: "http://new", Enabled: true},
	})

	_, err := r.Resolve(context.Background(), "acme", "github")
	require.ErrorIs(t, err, tenant.ErrServerNotFound)

	srv, err := r.Resolve(context.Background(), "acme", "stripe")
	require.NoError(t, err)
	require.Equal(t, "http://new", srv.UpstreamURL)
}

func TestWeightedSelect_SingleServerAlwaysWins(t *testing.T) {
	only := &tenant.Server{UpstreamURL: "http://solo"}
	for i := 0; i < 20; i++ {
		require.Same(t, only, tenant.WeightedSelect([]*tenant.Server{only}))
	}
}

func TestWeightedSelect_ZeroTotalWeightDoesNotPanic(t *testing.T) {
	servers := []*tenant.Server{
		{UpstreamURL: "http://a", Weight: 0},
		{UpstreamURL: "http://b", Weight: 0},
	}
	require.NotPanics(t, func() {
		picked := tenant.WeightedSelect(servers)
		require.Contains(t, []string{"http://a", "http://b"}, picked.UpstreamURL)
	})
}

func TestWeightedSelect_AllWeightOnOneServerAlwaysPicksIt(t *testing.T) {
	winner := &tenant.Server{UpstreamURL: "http://winner", Weight: 100}
	loser := &tenant.Server{UpstreamURL: "http://loser", Weight: 0}

	for i := 0; i < 20; i++ {
		require.Same(t, winner, tenant.WeightedSelect([]*tenant.Server{winner, loser}))
	}
}

func TestWeightedSelect_DistributionRoughlyMatchesWeights(t *testing.T) {
	heavy := &tenant.Server{UpstreamURL: "http://heavy", Weight: 90}
	light := &tenant.Server{UpstreamURL: "http://light", Weight: 10}
	servers := []*tenant.Server{heavy, light}

	heavyCount := 0
	const trials = 2000
	for i := 0; i < trials; i++ {
		if tenant.WeightedSelect(servers) == heavy {
			heavyCount++
		}
	}

	// Expect roughly 90% — allow a wide margin since this is randomized,
	// just enough to catch a badly broken cumulative-weight walk.
	require.Greater(t, heavyCount, trials*70/100)
	require.Less(t, heavyCount, trials*99/100)
}

func TestRouter_Len(t *testing.T) {
	r := tenant.NewRouter()
	require.Equal(t, 0, r.Len())

	r.Register(&tenant.Server{TenantSlug: "acme", Name: "github", Enabled: true})
	require.Equal(t, 1, r.Len())

	// A second server under the same key groups into the same routing
	// entry — Len counts routing keys, not individual server records.
	r.Register(&tenant.Server{TenantSlug: "acme", Name: "github", Enabled: true})
	require.Equal(t, 1, r.Len())

	r.Register(&tenant.Server{TenantSlug: "acme", Name: "stripe", Enabled: true})
	require.Equal(t, 2, r.Len())
}

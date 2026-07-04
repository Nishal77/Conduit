package tenant

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// TenantsActive lives here rather than internal/proxy/metrics.go (where
// spec/09-observability.md lists it) because internal/proxy imports
// internal/tenant for the Router type; declaring it in proxy and importing
// proxy back from here would be a compile-time import cycle. The
// Prometheus name, help text, and labels are exactly as specified.
var TenantsActive = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Namespace: "conduit",
		Name:      "tenants_active",
		Help:      "Number of active tenants in the routing table.",
	},
	[]string{},
)

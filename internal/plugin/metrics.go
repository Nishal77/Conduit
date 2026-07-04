package plugin

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PluginLatencySeconds lives here rather than internal/proxy/metrics.go
// (where spec/09-observability.md lists it) because internal/proxy imports
// internal/plugin for the Registry type; declaring it in proxy and
// importing proxy back from here would be a compile-time import cycle. The
// Prometheus name, help text, and labels are exactly as specified.
var PluginLatencySeconds = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: "conduit",
		Name:      "plugin_latency_seconds",
		Help:      "Time spent executing plugins.",
		Buckets:   []float64{0.0001, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
	},
	[]string{"plugin_name", "hook"},
)

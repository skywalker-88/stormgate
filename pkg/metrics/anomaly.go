package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	// --- Anomaly detection ---
	AnomaliesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "stormgate",
			Name:      "anomalies_total",
			Help:      "Count of detected traffic anomalies (spikes) per route and client.",
		},
		[]string{"route", "client"},
	)

	ActiveKeys = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "stormgate",
			Name:      "anomaly_active_keys",
			Help:      "Current number of active {route,client} keys tracked by the anomaly detector.",
		},
	)

	AnomalousClients = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "stormgate",
			Name:      "anomalous_clients",
			Help:      "Number of distinct clients flagged as anomalous in the recent window, per route.",
		},
		[]string{"route"},
	)

	// --- Mitigation ladder (overrides / blocks) ---
	OverridesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "stormgate",
			Name:      "overrides_total",
			Help:      "Total number of per {route,client} overrides applied, labeled by reason.",
		},
		[]string{"route", "reason"},
	)

	BlocksTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "stormgate",
			Name:      "blocks_total",
			Help:      "Total number of temporary blocks applied, labeled by reason.",
		},
		[]string{"route", "reason"},
	)

	ActiveOverrides = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "stormgate",
			Name:      "active_overrides",
			Help:      "Number of currently active overrides per route.",
		},
		[]string{"route"},
	)

	ActiveBlocks = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "stormgate",
			Name:      "active_blocks",
			Help:      "Number of currently active blocks per route.",
		},
		[]string{"route"},
	)

	registerOnce sync.Once
)

// RegisterAnomalyMetrics registers all anomaly + mitigation metrics once.
func RegisterAnomalyMetrics(reg prometheus.Registerer) {
	registerOnce.Do(func() {
		// Anomaly
		reg.MustRegister(AnomaliesTotal)
		reg.MustRegister(ActiveKeys)
		reg.MustRegister(AnomalousClients)

		// Mitigation
		reg.MustRegister(OverridesTotal)
		reg.MustRegister(BlocksTotal)
		reg.MustRegister(ActiveOverrides)
		reg.MustRegister(ActiveBlocks)
	})
}

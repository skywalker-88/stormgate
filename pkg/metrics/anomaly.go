package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	AnomaliesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "stormgate",
			Name:      "anomalies_total",
			Help:      "Count of detected traffic anomalies (spikes) per route and client.",
		},
		[]string{"route", "client"},
	)
	ActiveKeys = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "stormgate",
		Name:      "anomaly_active_keys",
		Help:      "Current number of active {route,client} keys tracked by the anomaly detector.",
	})
	AnomalousClients = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "stormgate",
			Name:      "anomalous_clients",
			Help:      "Number of distinct clients flagged as anomalous in the recent window, per route.",
		},
		[]string{"route"},
	)
	registerOnce sync.Once
)

func RegisterAnomalyMetrics(reg prometheus.Registerer) {
	registerOnce.Do(func() {
		reg.MustRegister(AnomaliesTotal)
		reg.MustRegister(ActiveKeys)
		reg.MustRegister(AnomalousClients)
	})
}

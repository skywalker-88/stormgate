package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// stormgate_limited_total{route}
	Limited = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stormgate_limited_total",
			Help: "Total requests rejected due to rate limiting.",
		},
		[]string{"route"},
	)
)

func init() {
	prometheus.MustRegister(Limited)
}

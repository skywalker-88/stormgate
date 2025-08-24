package httpserver

import (
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	"github.com/skywalker-88/stormgate/internal/anom"
	Lm "github.com/skywalker-88/stormgate/internal/middleware"
	"github.com/skywalker-88/stormgate/pkg/config"
	"github.com/skywalker-88/stormgate/pkg/metrics"
)

// Metrics (single registration for app + tests)
var Requests = prometheus.NewCounterVec(
	prometheus.CounterOpts{Name: "stormgate_requests_total"},
	[]string{"code", "route"},
)

func init() {
	prometheus.MustRegister(Requests)
}

// statusRecorder captures the response status for metrics.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.code = code
	sr.ResponseWriter.WriteHeader(code)
}

type RouterDeps struct {
	Cfg *config.Config
	RL  *Lm.RateLimiter
}

// NewRouter builds the Chi router. If proxy is nil, only local routes are served.
func NewRouter(d RouterDeps, proxy *httputil.ReverseProxy) (http.Handler, func()) {
	r := chi.NewRouter()

	// Built-in safety middlewares
	r.Use(chimw.RequestID, chimw.RealIP, chimw.Recoverer)

	// NEW: zerolog access logging (reads ACCESS_LOG / ACCESS_LOG_SAMPLE)
	r.Use(Lm.AccessLoggerFromEnv())

	// Anomaly detection middleware
	metrics.RegisterAnomalyMetrics(prometheus.DefaultRegisterer)
	ad := anom.NewDetector(anom.Config{
		Enabled:               d.Cfg.Anomaly.Enabled,
		WindowSeconds:         d.Cfg.Anomaly.WindowSeconds,
		Buckets:               d.Cfg.Anomaly.Buckets,
		ThresholdMultiplier:   d.Cfg.Anomaly.ThresholdMultiplier,
		EWMAAlpha:             d.Cfg.Anomaly.EWMAAlpha,
		TTLSeconds:            d.Cfg.Anomaly.TTLSeconds,
		EvictEverySeconds:     d.Cfg.Anomaly.EvictEverySeconds,
		KeepSuspiciousSeconds: d.Cfg.Anomaly.KeepSuspiciousSeconds,
	})
	log.Info().
		Bool("enabled", d.Cfg.Anomaly.Enabled).
		Int("window_seconds", d.Cfg.Anomaly.WindowSeconds).
		Int("buckets", d.Cfg.Anomaly.Buckets).
		Float64("threshold_multiplier", d.Cfg.Anomaly.ThresholdMultiplier).
		Float64("ewma_alpha", d.Cfg.Anomaly.EWMAAlpha).
		Int("ttl_seconds", d.Cfg.Anomaly.TTLSeconds).
		Int("evict_every_seconds", d.Cfg.Anomaly.EvictEverySeconds).
		Int("keep_suspicious_seconds", d.Cfg.Anomaly.KeepSuspiciousSeconds).
		Msg("anomaly_config")
	r.Use(ad.Middleware)

	cleanup := func() {
		ad.Close() // stop janitor goroutine
	}

	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Optional: encourage caching so repeated hits don’t even reach StormGate via proxies
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"stormgate","version":"0.1.0","status":"ok","hint":"see /health and /metrics"}`))
	})

	// Local endpoints
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		if IsDraining() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"draining"}` + "\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})

	r.Handle("/metrics", promhttp.Handler())

	limRead := d.Cfg.Limits.Routes["/read"]
	limSearch := d.Cfg.Limits.Routes["/search"]
	if limRead.RPS == 0 {
		limRead = d.Cfg.Limits.Default
	}
	if limSearch.RPS == 0 {
		limSearch = d.Cfg.Limits.Default
	}

	// TODO(stormgate): remove local /read and /search once a proper backend is wired,
	// and apply rate limiting before proxy for those backend routes.
	// Local demo endpoints (rate-limited)
	r.With(func(next http.Handler) http.Handler { return d.RL.Limit("/read", limRead, next) }).Get("/read", func(w http.ResponseWriter, _ *http.Request) {
		Requests.WithLabelValues("200", "/read").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"msg":"read ok"}`))
	})

	r.With(func(next http.Handler) http.Handler { return d.RL.Limit("/search", limSearch, next) }).Get("/search", func(w http.ResponseWriter, _ *http.Request) {
		Requests.WithLabelValues("200", "/search").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"msg":"search ok"}`))
	})

	// -------- Proxy prefix from env --------
	prefix := strings.TrimSpace(os.Getenv("PROXY_PREFIX")) // e.g., "/api"
	if prefix == "" {
		r.NotFound(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		}))
		return r, cleanup
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimRight(prefix, "/") // normalize

	if proxy != nil {
		// Only <prefix>/* is proxied; strip <prefix> for upstream
		r.Mount(prefix, http.StripPrefix(prefix, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			sr := &statusRecorder{ResponseWriter: w, code: 200}
			proxy.ServeHTTP(sr, req)
			Requests.WithLabelValues(strconv.Itoa(sr.code), "proxy").Inc()
		})))
	} else {
		// Deterministic behavior if no proxy injected
		r.Route(prefix, func(api chi.Router) {
			api.NotFound(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":"bad_gateway"}`))
			}))
		})
	}

	// Everything else (non-local, non-prefix) is NOT proxied
	r.NotFound(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}`))
	}))

	return r, cleanup
}

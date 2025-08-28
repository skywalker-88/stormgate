package httpserver

import (
	"net/http"
	"net/http/httputil"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"

	"github.com/skywalker-88/stormgate/internal/anom"
	Lm "github.com/skywalker-88/stormgate/internal/middleware"
	"github.com/skywalker-88/stormgate/internal/rl"
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
	Cfg       *config.Config
	RL        *Lm.RateLimiter
	Mitigator rl.Mitigator // optional: future admin endpoints may use this
}

// NewRouter builds the Chi router. If proxy is nil, only local routes are served.
func NewRouter(d RouterDeps, proxy *httputil.ReverseProxy) (http.Handler, func()) {
	r := chi.NewRouter()

	// Built-in safety middlewares
	r.Use(chimw.RequestID, chimw.RealIP, chimw.Recoverer)

	// zerolog access logging (reads ACCESS_LOG / ACCESS_LOG_SAMPLE)
	r.Use(Lm.AccessLoggerFromEnv())

	// Anomaly detection middleware (keeps /metrics and /health excluded inside the detector)
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
	}, anom.Deps{
		Mit: d.RL.Mit,
		Cfg: d.Cfg,
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
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		if IsDraining() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"draining"}` + "\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
	})

	r.Handle("/metrics", promhttp.Handler())

	// ---- Local demo endpoints (rate-limited) ----
	readLim := rl.EffectiveLimit(d.Cfg, "/read")
	searchLim := rl.EffectiveLimit(d.Cfg, "/search")

	// /read
	r.With(func(next http.Handler) http.Handler { return d.RL.Limit("/read", readLim, next) }).
		Get("/read", func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(5 * time.Millisecond)
			Requests.WithLabelValues("200", "/read").Inc()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"msg":"read ok"}`))
		})

	// /search
	r.With(func(next http.Handler) http.Handler { return d.RL.Limit("/search", searchLim, next) }).
		Get("/search", func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(40 * time.Millisecond)
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

	// Build the proxy handler (captures status for metrics)
	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sr := &statusRecorder{ResponseWriter: w, code: 200}
		proxy.ServeHTTP(sr, req)
		Requests.WithLabelValues(strconv.Itoa(sr.code), "proxy").Inc()
	})

	if proxy != nil {
		// Mount a router at the prefix so we can apply per-subroute limits if configured.
		r.Route(prefix, func(api chi.Router) {
			// 1) Collect all configured routes that are more specific than the prefix.
			var specific []string
			for route := range d.Cfg.Limits.Routes {
				if route == "" || !strings.HasPrefix(route, "/") {
					continue
				}
				if route == prefix {
					continue
				}
				if strings.HasPrefix(route, prefix+"/") {
					specific = append(specific, route)
				}
			}
			// Longest-first so deeper paths bind before the fallback.
			sort.Slice(specific, func(i, j int) bool { return len(specific[i]) > len(specific[j]) })

			// 2) For each specific route (/api/search, /api/users, …) apply its own policy.
			for _, route := range specific {
				subPath := strings.TrimPrefix(route, prefix) // e.g., "/search"
				if subPath == "" {
					continue
				}
				base := rl.EffectiveLimit(d.Cfg, route)

				api.Route(subPath, func(sr chi.Router) {
					// Limit by the specific route key, but always strip <prefix> before proxying upstream.
					limited := d.RL.Limit(route, base, http.StripPrefix(prefix, proxyHandler))
					// Match both the exact path and any children under it.
					sr.Handle("/", limited)
					sr.Handle("/*", limited)
				})
			}

			// 3) Fallback for anything else under the prefix -> use the prefix-level policy.
			prefixBase := rl.EffectiveLimit(d.Cfg, prefix)
			prefixLimited := d.RL.Limit(prefix, prefixBase, http.StripPrefix(prefix, proxyHandler))
			api.Handle("/", prefixLimited)
			api.Handle("/*", prefixLimited)
		})

	} else {
		// Stub handler when no proxy exists, still rate-limited (prefix-level)
		r.Route(prefix, func(api chi.Router) {
			prefixBase := rl.EffectiveLimit(d.Cfg, prefix)
			stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true,"via":"stub","path":"` + r.URL.Path + `"}`))
			})
			api.Handle("/", d.RL.Limit(prefix, prefixBase, stub))
			api.Handle("/*", d.RL.Limit(prefix, prefixBase, stub))
		})
	}

	// Everything else (non-local, non-prefix) is NOT proxied
	r.NotFound(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}`))
	}))

	return r, cleanup
}

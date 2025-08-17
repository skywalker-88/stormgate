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
	"github.com/skywalker-88/stormgate/internal/middleware"
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

// NewRouter builds the Chi router. If proxy is nil, only local routes are served.
func NewRouter(proxy *httputil.ReverseProxy) http.Handler {
	r := chi.NewRouter()

	// Built-in safety middlewares
	r.Use(chimw.RequestID, chimw.RealIP, chimw.Recoverer)

	// NEW: zerolog access logging (reads ACCESS_LOG / ACCESS_LOG_SAMPLE)
	r.Use(middleware.AccessLoggerFromEnv())

	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Optional: encourage caching so repeated hits don’t even reach StormGate via proxies
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"stormgate","version":"0.1.0","status":"ok","hint":"see /health and /metrics"}`))
	})

	// Local endpoints
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	r.Handle("/metrics", promhttp.Handler())

	// TODO(stormgate): remove local /read and /search once a proper backend is wired,
	// and apply rate limiting before proxy for those backend routes.
	r.Get("/read", func(w http.ResponseWriter, _ *http.Request) {
		Requests.WithLabelValues("200", "/read").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"msg":"read ok"}`))
	})

	r.Get("/search", func(w http.ResponseWriter, _ *http.Request) {
		// simulate a slightly heavier call (optional)
		// time.Sleep(40 * time.Millisecond)
		Requests.WithLabelValues("200", "/search").Inc()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"msg":"search ok"}`))
	})

	// -------- Proxy prefix from env --------
	prefix := strings.TrimSpace(os.Getenv("PROXY_PREFIX")) // e.g., "/api"
	if prefix == "" {
		// No prefix configured: do NOT proxy anything. Unknown paths → 404.
		r.NotFound(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		}))
		return r
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

	return r
}

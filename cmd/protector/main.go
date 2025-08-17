package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/skywalker-88/stormgate/internal/httpserver"
)

// MakeReverseProxy lives in main: build once, inject into the router.
// Director sets standard X-Forwarded-* headers; ErrorHandler returns JSON 502.
func MakeReverseProxy(target string) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	rp := httputil.NewSingleHostReverseProxy(u)

	orig := rp.Director
	rp.Director = func(req *http.Request) {
		// capture client/host/proto BEFORE director mutates the request
		origHost := req.Host
		origProto := "http"
		if req.TLS != nil {
			origProto = "https"
		}
		if v := req.Header.Get("X-Forwarded-Proto"); v != "" {
			origProto = v
		}

		client := req.RemoteAddr
		if host, _, err := net.SplitHostPort(client); err == nil && host != "" {
			client = host
		}
		xff := req.Header.Get("X-Forwarded-For")

		// apply default director changes (scheme/host/path rewrite)
		orig(req)

		// set forwarded headers
		if xff == "" {
			req.Header.Set("X-Forwarded-For", client)
		} else {
			req.Header.Set("X-Forwarded-For", xff+", "+client)
		}
		req.Header.Set("X-Forwarded-Host", origHost)
		req.Header.Set("X-Forwarded-Proto", origProto)
	}

	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"bad_gateway"}`))
	}

	return rp, nil
}

func main() {
	// Structured console logs
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	// (Optional for now) Redis client — used later for distributed rate limiting
	rdb := redis.NewClient(&redis.Options{
		Addr:     getenv("REDIS_ADDR", "redis:6379"),
		Password: "",
		DB:       0,
	})

	// Build reverse proxy target (backend may not exist yet — that’s fine; we’ll return 502)
	backend := getenv("BACKEND_URL", "http://demo-backend:8081")
	proxy, err := MakeReverseProxy(backend)
	if err != nil {
		log.Fatal().Err(err).Str("backend", backend).Msg("invalid BACKEND_URL")
	}

	// Build router (handles /health, /metrics, dev /read & /search; mounts proxy under /api/* per router)
	router := httpserver.NewRouter(proxy)

	// Startup logs
	addr := getenv("STORMGATE_HTTP_ADDR", ":8080")
	log.Info().Str("addr", addr).Str("backend", backend).Msg("StormGate starting")

	// Non-fatal Redis ping
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Warn().Err(err).Msg("redis not reachable yet")
	} else {
		log.Info().Msg("redis reachable")
	}

	// TODO(stormgate): swap to http.Server + graceful Shutdown(ctx) on SIGINT/SIGTERM
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatal().Err(err).Msg("server stopped")
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/skywalker-88/stormgate/internal/httpserver"
	Lm "github.com/skywalker-88/stormgate/internal/middleware"
	"github.com/skywalker-88/stormgate/internal/rl"
	"github.com/skywalker-88/stormgate/pkg/config"
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
		_, _ = w.Write([]byte(`{"error":"bad_gateway"}` + "\n"))
	}

	return rp, nil
}

func main() {
	// ------- Logging setup -------
	// Console pretty logs; change LOG_LEVEL to "debug" to see detector debug lines.
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	switch strings.ToLower(getenv("LOG_LEVEL", "info")) {
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	// ---- Load config (with env fallbacks) ----
	cfgPath := os.Getenv("STORMGATE_CONFIG")
	if cfgPath == "" {
		cfgPath = "configs/policies.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal().Err(err).Str("config", cfgPath).Msg("load config")
	}

	// Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr:     getenv("REDIS_ADDR", "redis:6379"),
		Password: "",
		DB:       0,
	})

	limiter := rl.New(rdb)
	rlmw := Lm.NewRateLimiter(limiter, cfg)

	// Build reverse proxy target (backend may not exist yet — that’s fine; we’ll return 502)
	backend := getenv("BACKEND_URL", "http://demo-backend:8081")
	proxy, err := MakeReverseProxy(backend)
	if err != nil {
		log.Fatal().Err(err).Str("backend", backend).Msg("invalid BACKEND_URL")
	}

	// Build router (handles /health, /metrics, dev /read & /search; mounts proxy under /api/* per router)
	router, cleanup := httpserver.NewRouter(httpserver.RouterDeps{Cfg: cfg, RL: rlmw}, proxy)

	// Startup logs
	addr := getenv("STORMGATE_HTTP_ADDR", ":8080")
	log.Info().
		Str("addr", addr).
		Str("backend", backend).
		Str("config", cfgPath).
		Str("log_level", zerolog.GlobalLevel().String()).
		Msg("StormGate starting")

	// Non-fatal Redis ping
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Warn().Err(err).Msg("redis not reachable yet")
	} else {
		log.Info().Msg("redis reachable")
	}

	// http.Server with sane timeouts
	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,  // slowloris protection
		WriteTimeout:      15 * time.Second, // bound handler writes
		IdleTimeout:       60 * time.Second, // keep-alive lifetime
	}

	// Serve in background
	go func() {
		log.Info().Str("addr", srv.Addr).Msg("http server listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("server stopped unexpectedly")
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	sig := <-quit
	log.Info().Str("signal", sig.String()).Msg("shutdown requested; draining")

	httpserver.SetDraining(true)

	shCtx, shCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shCancel()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Error().Err(err).Msg("server shutdown did not complete in time; forcing close")
		_ = srv.Close()
	} else {
		log.Info().Msg("http server shut down cleanly")
	}

	// Cleanup any resources (e.g., stop anomaly janitor)
	if cleanup != nil {
		cleanup()
	}

	// Close external resources
	if err := rdb.Close(); err != nil {
		log.Warn().Err(err).Msg("redis close")
	} else {
		log.Info().Msg("redis closed")
	}

	log.Info().Msg("stormgate exited")
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

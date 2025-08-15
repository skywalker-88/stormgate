package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var (
	requests = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "stormgate_requests_total"},
		[]string{"code", "route"},
	)
)

func mustRegisterMetrics() {
	prometheus.MustRegister(requests)
}

func main() {
	// structured JSON logs
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	// Redis client (leave running even if Redis not up yet; we’ll wire it later)
	rdb := redis.NewClient(&redis.Options{
		Addr:     getenv("REDIS_ADDR", "localhost:6379"),
		Password: "",
		DB:       0,
	})
	_ = rdb // (we’ll use this in the limiter next)

	mustRegisterMetrics()

	r := chi.NewRouter()

	// health
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// metrics
	r.Handle("/metrics", promhttp.Handler())

	// sample backend passthrough placeholder (for now just 200)
	r.Get("/read", func(w http.ResponseWriter, r *http.Request) {
		requests.WithLabelValues("200", "/read").Inc()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"msg":"read ok"}`))
	})

	// optional: quick Redis ping to verify connectivity on startup (non-fatal)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Warn().Err(err).Msg("redis not reachable yet")
	} else {
		log.Info().Msg("redis reachable")
	}

	addr := ":8080"
	log.Info().Str("addr", addr).Msg("StormGate protector starting")
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatal().Err(err).Msg("server stopped")
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

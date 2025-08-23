package middleware

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/skywalker-88/stormgate/internal/rl"
	"github.com/skywalker-88/stormgate/pkg/config"
	"github.com/skywalker-88/stormgate/pkg/metrics"
)

type RateLimiter struct {
	L   *rl.Limiter
	Cfg *config.Config
}

func NewRateLimiter(l *rl.Limiter, cfg *config.Config) *RateLimiter {
	return &RateLimiter{L: l, Cfg: cfg}
}

func (r *RateLimiter) keyFrom(req *http.Request, route string) string {
	id := ""
	src := r.Cfg.Identity.Source
	if strings.HasPrefix(strings.ToLower(src), "header:") {
		h := strings.TrimSpace(strings.SplitN(src, ":", 2)[1])
		if v := req.Header.Get(h); v != "" {
			id = v
		}
	}
	if id == "" {
		id = clientIP(req)
	}
	if id == "" {
		id = "anon"
	}
	return "rl:" + route + ":" + id
}

func clientIP(r *http.Request) string {
	// prefer X-Forwarded-For (left-most)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (r *RateLimiter) Limit(route string, lim config.Limit, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		key := r.keyFrom(req, route)
		allowed, remaining, retryAfter, resetAfter, err := r.L.Consume(req.Context(), key, lim.RPS, lim.Burst, lim.Cost)
		if err != nil {
			log.Error().Err(err).Str("key", key).Msg("limiter error; allowing request")
			next.ServeHTTP(w, req)
			return
		}
		w.Header().Set("X-StormGate", "protector")
		w.Header().Set("X-RateLimit-Limit", formatFloat(lim.RPS))
		w.Header().Set("X-RateLimit-Remaining", formatFloat(remaining))
		w.Header().Set("X-RateLimit-Reset", formatDuration(resetAfter))
		if !allowed {
			metrics.Limited.WithLabelValues(route).Inc()
			log.Info().
				Str("route", route).
				Str("key", key).
				Float64("remaining", remaining).
				Int64("burst", lim.Burst).
				Float64("rps", lim.RPS).
				Dur("retry_after", retryAfter).
				Msg("rate_limited")
			if retryAfter > 0 {
				w.Header().Set("Retry-After", formatSeconds(retryAfter))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
			return
		}
		next.ServeHTTP(w, req)
	})
}

func formatFloat(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmtFloat(f), "0"), ".")
}

func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }

func formatDuration(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) }

func formatSeconds(d time.Duration) string {
	return strconv.FormatInt(int64((d+time.Second-1)/time.Second), 10)
}

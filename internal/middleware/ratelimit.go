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

const globalKeyPrefix = "rl:global:"

type RateLimiter struct {
	L   *rl.Limiter
	Cfg *config.Config
	Mit rl.Mitigator // mitigation (overrides, blocks)
}

func NewRateLimiter(l *rl.Limiter, cfg *config.Config, mit rl.Mitigator) *RateLimiter {
	return &RateLimiter{L: l, Cfg: cfg, Mit: mit}
}

// ---------- identity / keys ----------

func (r *RateLimiter) clientIDFrom(req *http.Request) string {
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
	return id
}

func (r *RateLimiter) rlKey(route, clientID string) string { return "rl:" + route + ":" + clientID }
func (r *RateLimiter) globalKey(clientID string) string    { return globalKeyPrefix + clientID }

func clientIP(req *http.Request) string {
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err == nil {
		return host
	}
	return req.RemoteAddr
}

func (r *RateLimiter) hasGlobalClientLimit() bool {
	return r != nil && r.Cfg != nil && (r.Cfg.Limits.GlobalClient.RPS > 0 || r.Cfg.Limits.GlobalClient.Burst > 0)
}

// ---------- main middleware ----------

func (r *RateLimiter) Limit(route string, base config.Limit, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		clientID := r.clientIDFrom(req)

		allowlisted := rl.IsAllowlisted(r.Cfg, clientID)

		// 0) Blocks (deny fast) — now SKIPPED for allowlisted clients
		if r.Mit != nil && !allowlisted {
			if bl, _ := r.Mit.GetBlock(req.Context(), route, clientID); bl != nil {
				w.Header().Set("X-StormGate", "protector")
				w.Header().Set("X-StormGate-Block", bl.Reason)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests) // or 403
				_, _ = w.Write([]byte(`{"error":"blocked"}`))
				return
			}
		}

		// 1) Route effective limits (apply override with rails)
		effRPS := base.RPS
		effBurst := base.Burst
		overrideApplied := false
		if r.Mit != nil && !allowlisted {
			if ov, _ := r.Mit.GetOverride(req.Context(), route, clientID); ov != nil {
				overrideApplied = true
				minRPS := r.Cfg.Mitigation.MinRPS
				minBurst := int64(r.Cfg.Mitigation.MinBurst)
				if ov.RPS > 0 && float64(ov.RPS) < effRPS {
					effRPS = float64(ov.RPS)
				}
				if ov.Burst > 0 && int64(ov.Burst) < effBurst {
					effBurst = int64(ov.Burst)
				}
				if effRPS < minRPS {
					effRPS = minRPS
				}
				if effBurst < minBurst {
					effBurst = minBurst
				}
			}
		}

		// 2) Global client effective limits (base only for now)
		globalEnabled := r.hasGlobalClientLimit()
		gRPS := r.Cfg.Limits.GlobalClient.RPS
		gBurst := r.Cfg.Limits.GlobalClient.Burst

		// 3) Consume GLOBAL first (avoid half-consume drift if it denies)
		if globalEnabled {
			gKey := r.globalKey(clientID)
			gAllowed, gRemaining, gRetryAfter, gResetAfter, gErr :=
				r.L.Consume(req.Context(), gKey, gRPS, gBurst, base.Cost)
			if gErr != nil {
				log.Error().Err(gErr).Str("key", gKey).Msg("global limiter error; allowing request")
			} else {
				// Always expose global headers when enabled
				w.Header().Set("X-ClientRateLimit-Limit", formatFloat(gRPS))
				w.Header().Set("X-ClientRateLimit-Remaining", formatFloat(gRemaining))
				w.Header().Set("X-ClientRateLimit-Reset", formatDuration(gResetAfter))

				if !gAllowed {
					if gRetryAfter > 0 {
						w.Header().Set("Retry-After", formatSeconds(gRetryAfter))
					}
					w.Header().Set("X-StormGate", "protector")
					w.Header().Set("X-StormGate-Denied-By", "global")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"error":"rate_limited_global"}`))
					metrics.Limited.WithLabelValues(route).Inc() // keep route label for continuity
					return
				}
			}
		}

		// 4) Consume ROUTE bucket (existing behavior, now with effective limits)
		key := r.rlKey(route, clientID)
		allowed, remaining, retryAfter, resetAfter, err :=
			r.L.Consume(req.Context(), key, effRPS, effBurst, base.Cost)
		if err != nil {
			log.Error().Err(err).Str("key", key).Msg("limiter error; allowing request")
			next.ServeHTTP(w, req)
			return
		}

		// 5) Headers & decision
		w.Header().Set("X-StormGate", "protector")
		if overrideApplied {
			w.Header().Set("X-StormGate-Override", "1")
		}
		w.Header().Set("X-RateLimit-Limit", formatFloat(effRPS))
		w.Header().Set("X-RateLimit-Remaining", formatFloat(remaining))
		w.Header().Set("X-RateLimit-Reset", formatDuration(resetAfter))

		if !allowed {
			if retryAfter > 0 {
				w.Header().Set("Retry-After", formatSeconds(retryAfter))
			}
			w.Header().Set("X-StormGate-Denied-By", "route")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
			metrics.Limited.WithLabelValues(route).Inc()
			return
		}

		next.ServeHTTP(w, req)
	})
}

// ---------- tiny helpers ----------

func formatFloat(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmtFloat(f), "0"), ".")
}
func fmtFloat(f float64) string             { return strconv.FormatFloat(f, 'f', 3, 64) }
func formatDuration(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) }
func formatSeconds(d time.Duration) string {
	return strconv.FormatInt(int64((d+time.Second-1)/time.Second), 10)
}

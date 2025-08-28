package anom

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/skywalker-88/stormgate/internal/rl"
	"github.com/skywalker-88/stormgate/pkg/config"
	"github.com/skywalker-88/stormgate/pkg/metrics"
)

// Config controls the anomaly detector behavior.
type Config struct {
	Enabled             bool
	WindowSeconds       int
	Buckets             int
	ThresholdMultiplier float64
	EWMAAlpha           float64

	// Eviction/TTL
	TTLSeconds            int
	EvictEverySeconds     int
	KeepSuspiciousSeconds int
}

// Deps lets the detector apply mitigation when an anomaly fires.
type Deps struct {
	Mit rl.Mitigator
	Cfg *config.Config
}

type bucketState struct {
	counts   []int64
	idx      int
	tsSec    int64
	total    int64
	baseline float64
}

type perKey struct {
	sync.Mutex
	state       *bucketState
	lastSeen    int64 // unix seconds
	lastAnomaly int64 // unix seconds
}

// Detector tracks per {route,client} windows and detects spikes.
type Detector struct {
	cfg      Config
	deps     Deps
	keys     sync.Map
	perRoute sync.Map
	stop     chan struct{}
}

type routeState struct {
	sync.Mutex
	clients map[string]int64 // client -> lastAnomalyUnix
}

func NewDetector(cfg Config, deps Deps) *Detector {
	if cfg.WindowSeconds <= 0 {
		cfg.WindowSeconds = 10
	}
	if cfg.Buckets <= 0 {
		cfg.Buckets = cfg.WindowSeconds
	}
	if cfg.EWMAAlpha <= 0 {
		cfg.EWMAAlpha = 0.2
	}
	if cfg.ThresholdMultiplier <= 0 {
		cfg.ThresholdMultiplier = 5.0
	}
	if cfg.EvictEverySeconds <= 0 {
		cfg.EvictEverySeconds = 30
	}
	if cfg.TTLSeconds < 0 {
		cfg.TTLSeconds = 0
	}
	if cfg.KeepSuspiciousSeconds < 0 {
		cfg.KeepSuspiciousSeconds = 0
	}

	d := &Detector{cfg: cfg, deps: deps, stop: make(chan struct{})}
	if cfg.TTLSeconds > 0 || cfg.KeepSuspiciousSeconds > 0 {
		go d.janitor()
	}
	return d
}

func (d *Detector) Close() {
	if d.stop != nil {
		close(d.stop)
	}
}

// Middleware observes each request; logs + increments metric on anomalies (no blocking).
func (d *Detector) Middleware(next http.Handler) http.Handler {
	if !d.cfg.Enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Path
		route := raw
		if d.deps.Cfg != nil {
			route = rl.NormalizeRoute(d.deps.Cfg, raw)
		}
		if route == "/metrics" || route == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		client := d.clientIDFrom(r)

		if d.observe(route, client) {
			metrics.AnomaliesTotal.WithLabelValues(route, client).Inc()
			log.Warn().Str("route", route).Str("client", client).Msg("anomaly_detected")

			// Apply mitigation if wired and not allowlisted
			if d.deps.Mit != nil && d.deps.Cfg != nil && !rl.IsAllowlisted(d.deps.Cfg, client) {
				d.onAnomaly(route, client)
			}
		}

		next.ServeHTTP(w, r)
	})
}

// observe updates the window for {route,client} and returns true if anomalous.
func (d *Detector) observe(route, client string) bool {
	key := route + "|" + client
	pkIface, _ := d.keys.LoadOrStore(key, &perKey{})
	pk := pkIface.(*perKey)

	nowSec := time.Now().Unix()
	atomic.StoreInt64(&pk.lastSeen, nowSec)

	pk.Lock()
	defer pk.Unlock()

	if pk.state == nil {
		pk.state = &bucketState{
			counts:   make([]int64, d.cfg.Buckets),
			idx:      0,
			tsSec:    nowSec,
			total:    0,
			baseline: 0,
		}
	}

	delta := nowSec - pk.state.tsSec
	if delta < 0 {
		delta = 0
	}
	if delta > 0 {
		steps := int(delta)
		if steps >= len(pk.state.counts) {
			for i := range pk.state.counts {
				pk.state.counts[i] = 0
			}
			pk.state.total = 0
			pk.state.idx = 0
		} else {
			for i := 0; i < steps; i++ {
				pk.state.idx = (pk.state.idx + 1) % len(pk.state.counts)
				pk.state.total -= pk.state.counts[pk.state.idx]
				pk.state.counts[pk.state.idx] = 0
			}
		}
		pk.state.tsSec = nowSec
	}

	pk.state.counts[pk.state.idx]++
	pk.state.total++

	current := float64(pk.state.total)
	prev := pk.state.baseline
	threshold := d.cfg.ThresholdMultiplier * maxFloat(1.0, prev)

	isAnom := current > threshold

	if isAnom {
		atomic.StoreInt64(&pk.lastAnomaly, nowSec)
		if d.cfg.KeepSuspiciousSeconds > 0 {
			if !(d.deps.Cfg != nil && rl.IsAllowlisted(d.deps.Cfg, client)) {
				rsIface, _ := d.perRoute.LoadOrStore(route, &routeState{clients: make(map[string]int64)})
				rs := rsIface.(*routeState)
				rs.Lock()
				rs.clients[client] = nowSec
				metrics.AnomalousClients.WithLabelValues(route).Set(float64(len(rs.clients)))
				rs.Unlock()
			}
		}
	}

	alpha := d.cfg.EWMAAlpha
	if prev == 0 {
		pk.state.baseline = alpha * current
	} else {
		pk.state.baseline = alpha*current + (1.0-alpha)*prev
	}

	return isAnom
}

// onAnomaly applies a scoped override with TTL and escalates on repeat offenders.
func (d *Detector) onAnomaly(route, client string) {
	ctx := context.Background()

	// 1) Determine ramp factor/step from existing override (if any)
	step := 0
	factor := 0.5
	if d.deps.Cfg.Mitigation.StepRamp.Enabled {
		if ov, _ := d.deps.Mit.GetOverride(ctx, route, client); ov != nil {
			step = ov.Step + 1
		}
		steps := d.deps.Cfg.Mitigation.StepRamp.Steps
		if len(steps) > 0 {
			if step >= len(steps) {
				step = len(steps) - 1
			}
			factor = steps[step]
		}
	}

	// 2) Base policy for this route
	base := rl.EffectiveLimit(d.deps.Cfg, route)

	// 3) Compute effective clamped values with rails
	minRPS := d.deps.Cfg.Mitigation.MinRPS
	minBurst := int64(d.deps.Cfg.Mitigation.MinBurst)

	newRPS := clampFloat(minRPS, factor*base.RPS, base.RPS)
	newBurst := clampInt(minBurst, int64(float64(base.Burst)*factor), base.Burst)

	// 4) Set override with TTL (shared across replicas)
	ttl := time.Duration(d.deps.Cfg.Mitigation.OverrideTTLSeconds) * time.Second
	if err := d.deps.Mit.SetOverride(ctx, route, client, rl.Override{
		RPS:   int(newRPS),
		Burst: int(newBurst),
		Step:  step,
	}, ttl); err != nil {
		log.Error().Err(err).Str("route", route).Str("client", client).Msg("override_failed")
	} else {
		metrics.OverridesTotal.WithLabelValues(route, "anomaly").Inc()
		// DO NOT touch ActiveOverrides here; kept in sync by RefreshActiveGauges().
	}

	// 5) Escalate if repeat offender within window
	window := time.Duration(d.deps.Cfg.Mitigation.RepeatOffender.WindowSeconds) * time.Second
	streak, _ := d.deps.Mit.IncrStreak(ctx, route, client, window)
	if streak >= int64(d.deps.Cfg.Mitigation.RepeatOffender.Threshold) {
		bttl := time.Duration(d.deps.Cfg.Mitigation.BlockTTLSeconds) * time.Second
		if err := d.deps.Mit.SetBlock(ctx, route, client, rl.Block{Reason: "repeat_offender"}, bttl); err != nil {
			log.Error().Err(err).Str("route", route).Str("client", client).Msg("block_failed")
		} else {
			metrics.BlocksTotal.WithLabelValues(route, "repeat_offender").Inc()
			// DO NOT touch ActiveBlocks here; kept in sync by RefreshActiveGauges().
			_ = d.deps.Mit.ResetStreak(ctx, route, client)
			log.Warn().Str("route", route).Str("client", client).Msg("block_started")
		}
	}

	log.Info().
		Str("route", route).
		Str("client", client).
		Int("rps", int(newRPS)).
		Int("burst", int(newBurst)).
		Int("step", step).
		Msg("override_applied")
}

func (d *Detector) janitor() {
	ticker := time.NewTicker(time.Duration(d.cfg.EvictEverySeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			now := time.Now().Unix()
			ttl := int64(d.cfg.TTLSeconds)
			keepSusp := int64(d.cfg.KeepSuspiciousSeconds)

			survivors := 0
			d.keys.Range(func(k, v any) bool {
				pk := v.(*perKey)
				last := atomic.LoadInt64(&pk.lastSeen)
				la := atomic.LoadInt64(&pk.lastAnomaly)

				evict := false
				if ttl > 0 && last > 0 && now-last > ttl {
					if !(keepSusp > 0 && la > 0 && now-la <= keepSusp) {
						evict = true
					}
				}

				if evict {
					d.keys.Delete(k)
				} else {
					survivors++
				}
				return true
			})

			metrics.ActiveKeys.Set(float64(survivors))

			if d.cfg.KeepSuspiciousSeconds > 0 {
				cutoff := now - int64(d.cfg.KeepSuspiciousSeconds)
				d.perRoute.Range(func(rk, rv any) bool {
					route := rk.(string)
					rs := rv.(*routeState)
					rs.Lock()
					for c, t := range rs.clients {
						if t < cutoff {
							delete(rs.clients, c)
						}
					}
					metrics.AnomalousClients.WithLabelValues(route).Set(float64(len(rs.clients)))
					rs.Unlock()
					return true
				})
			}
		}
	}
}

func (d *Detector) clientIDFrom(r *http.Request) string {
	// Prefer configured identity source (e.g., "header:X-API-Key")
	if d.deps.Cfg != nil {
		src := d.deps.Cfg.Identity.Source
		if strings.HasPrefix(strings.ToLower(src), "header:") {
			h := strings.TrimSpace(strings.SplitN(src, ":", 2)[1])
			if v := r.Header.Get(h); v != "" {
				return v
			}
		}
	}
	// Fallback to IP (first XFF, else RemoteAddr)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func clampFloat(minVal, v, maxVal float64) float64 {
	if v < minVal {
		return minVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

func clampInt(minVal, v, maxVal int64) int64 {
	if v < minVal {
		return minVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

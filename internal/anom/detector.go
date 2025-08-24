package anom

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/skywalker-88/stormgate/pkg/metrics"
)

// Config controls the anomaly detector behavior.
type Config struct {
	Enabled             bool
	WindowSeconds       int     // sliding window length in seconds (e.g., 10)
	Buckets             int     // number of 1s buckets across the window (e.g., 10 -> 1s resolution)
	ThresholdMultiplier float64 // spike if current_window > multiplier * baseline (with floor 1.0)
	EWMAAlpha           float64 // baseline smoothing factor (0..1), higher = more reactive

	// Eviction/TTL
	TTLSeconds            int // evict idle keys after this many seconds (>0 to enable)
	EvictEverySeconds     int // how often the janitor scans
	KeepSuspiciousSeconds int // keep keys that had anomalies for this long (sticky memory)
}

type bucketState struct {
	counts   []int64 // per-second buckets
	idx      int     // current bucket index
	tsSec    int64   // unix seconds corresponding to counts[idx]
	total    int64   // sum of all buckets in the window
	baseline float64 // EWMA of window total
}

type perKey struct {
	sync.Mutex
	state       *bucketState
	lastSeen    int64 // unix seconds; updated atomically
	lastAnomaly int64 // unix seconds; updated atomically when anomaly occurs
}

// Detector tracks per {route,client} windows and detects spikes.
type Detector struct {
	cfg      Config
	keys     sync.Map
	perRoute sync.Map
	stop     chan struct{} // close to stop janitor
}

// routeState tracks per-route anomaly state.
type routeState struct {
	sync.Mutex
	clients map[string]int64 // client -> lastAnomalyUnix
}

// NewDetector creates a spike detector using a bucketed sliding window + EWMA baseline.
func NewDetector(cfg Config) *Detector {
	if cfg.WindowSeconds <= 0 {
		cfg.WindowSeconds = 10
	}
	if cfg.Buckets <= 0 {
		cfg.Buckets = cfg.WindowSeconds // default: 1s buckets
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

	d := &Detector{cfg: cfg, stop: make(chan struct{})}

	// Start janitor if *either* TTL-based eviction or sticky pruning is needed.
	if cfg.TTLSeconds > 0 || cfg.KeepSuspiciousSeconds > 0 {
		go d.janitor()
	}
	return d
}

// Close stops the janitor goroutine; call this in your shutdown path.
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
		route := r.URL.Path
		if route == "/metrics" || route == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		client := clientID(r)

		if d.observe(route, client) {
			metrics.AnomaliesTotal.WithLabelValues(route, client).Inc()
			// keep a concise, useful event
			log.Warn().
				Str("route", route).
				Str("client", client).
				Msg("anomaly_detected")
		}

		next.ServeHTTP(w, r)
	})
}

// observe updates the window for {route,client} and returns true if this request is anomalous.
// It performs the anomaly check AGAINST the PREVIOUS baseline (pre-check), then updates EWMA.
func (d *Detector) observe(route, client string) bool {
	key := route + "|" + client
	pkIface, _ := d.keys.LoadOrStore(key, &perKey{})
	pk := pkIface.(*perKey)

	nowSec := time.Now().Unix()
	// Mark last seen immediately (no lock needed).
	atomic.StoreInt64(&pk.lastSeen, nowSec)

	pk.Lock()
	defer pk.Unlock()

	// Initialize state on first touch.
	if pk.state == nil {
		pk.state = &bucketState{
			counts:   make([]int64, d.cfg.Buckets),
			idx:      0,
			tsSec:    nowSec,
			total:    0,
			baseline: 0,
		}
	}

	// Advance the ring to the current second.
	delta := nowSec - pk.state.tsSec
	if delta < 0 {
		delta = 0
	}
	if delta > 0 {
		steps := int(delta)
		if steps >= len(pk.state.counts) {
			// Window fully moved; reset all.
			for i := range pk.state.counts {
				pk.state.counts[i] = 0
			}
			pk.state.total = 0
			pk.state.idx = 0
		} else {
			// Rotate 1 bucket per elapsed second.
			for i := 0; i < steps; i++ {
				pk.state.idx = (pk.state.idx + 1) % len(pk.state.counts)
				pk.state.total -= pk.state.counts[pk.state.idx]
				pk.state.counts[pk.state.idx] = 0
			}
		}
		pk.state.tsSec = nowSec
	}

	// Record this request into the current bucket & window total.
	pk.state.counts[pk.state.idx]++
	pk.state.total++

	// ---- Pre-check then update baseline ----
	current := float64(pk.state.total)
	prev := pk.state.baseline
	threshold := d.cfg.ThresholdMultiplier * maxFloat(1.0, prev)

	isAnom := current > threshold

	// Sticky memory: remember last anomaly time.
	if isAnom {
		atomic.StoreInt64(&pk.lastAnomaly, nowSec)

		// Track this client in the per-route set and update the gauge (only if sticky window enabled)
		if d.cfg.KeepSuspiciousSeconds > 0 {
			rsIface, _ := d.perRoute.LoadOrStore(route, &routeState{clients: make(map[string]int64)})
			rs := rsIface.(*routeState)
			rs.Lock()
			rs.clients[client] = nowSec
			metrics.AnomalousClients.WithLabelValues(route).Set(float64(len(rs.clients)))
			rs.Unlock()
		}
	}

	// Update EWMA after the check so we don't mask first spikes.
	alpha := d.cfg.EWMAAlpha
	if prev == 0 {
		pk.state.baseline = alpha * current
	} else {
		pk.state.baseline = alpha*current + (1.0-alpha)*prev
	}

	return isAnom
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
				// only consider eviction when TTL is enabled and the key is idle past TTL
				if ttl > 0 && last > 0 && now-last > ttl {
					// sticky: keep keys that had a recent anomaly
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

			// Prune per-route recent anomaly sets and refresh the route gauge.
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

// clientID returns the best-effort client identifier.
// Prefer the first IP in X-Forwarded-For if present; otherwise RemoteAddr.
func clientID(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
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

package rl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/skywalker-88/stormgate/pkg/metrics"
)

type Override struct {
	RPS   int   `json:"rps"`
	Burst int   `json:"burst"`
	Step  int   `json:"step,omitempty"` // ramp step index (0-based)
	Exp   int64 `json:"exp,omitempty"`
}

type Block struct {
	Reason string `json:"reason"`
	Exp    int64  `json:"exp,omitempty"`
}

type Mitigator interface {
	// Overrides
	GetOverride(ctx context.Context, route, client string) (*Override, error)
	SetOverride(ctx context.Context, route, client string, ov Override, ttl time.Duration) error
	ClearOverride(ctx context.Context, route, client string) error

	// Blocks
	GetBlock(ctx context.Context, route, client string) (*Block, error)
	SetBlock(ctx context.Context, route, client string, b Block, ttl time.Duration) error
	ClearBlock(ctx context.Context, route, client string) error

	// Repeat-offender streak
	IncrStreak(ctx context.Context, route, client string, window time.Duration) (int64, error)
	ResetStreak(ctx context.Context, route, client string) error

	// Metrics helpers (optional): refresh active override/block gauges by scanning Redis.
	RefreshActiveGauges(ctx context.Context) error
}

type RedisMitigator struct{ rdb *redis.Client }

func NewRedisMitigator(rdb *redis.Client) *RedisMitigator { return &RedisMitigator{rdb: rdb} }

func keyOverride(route, client string) string { return fmt.Sprintf("sg:override:%s:%s", route, client) }
func keyBlock(route, client string) string    { return fmt.Sprintf("sg:block:%s:%s", route, client) }
func keyStreak(route, client string) string {
	return fmt.Sprintf("sg:anom:streak:%s:%s", route, client)
}

// ------- Overrides -------

func (m *RedisMitigator) GetOverride(ctx context.Context, route, client string) (*Override, error) {
	b, err := m.rdb.Get(ctx, keyOverride(route, client)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ov Override
	if err := json.Unmarshal(b, &ov); err != nil {
		// Be lenient: if corrupt, drop it
		_ = m.rdb.Del(ctx, keyOverride(route, client)).Err()
		return nil, nil
	}
	return &ov, nil
}

func (m *RedisMitigator) SetOverride(ctx context.Context, route, client string, ov Override, ttl time.Duration) error {
	ov.Exp = time.Now().Add(ttl).Unix()
	j, _ := json.Marshal(ov)
	// NOTE: we intentionally DON'T increment Prometheus counters here to avoid
	// double counting across code paths (detector/admin). Increment at call site.
	return m.rdb.Set(ctx, keyOverride(route, client), j, ttl).Err()
}

func (m *RedisMitigator) ClearOverride(ctx context.Context, route, client string) error {
	return m.rdb.Del(ctx, keyOverride(route, client)).Err()
}

// -------- Blocks --------

func (m *RedisMitigator) GetBlock(ctx context.Context, route, client string) (*Block, error) {
	b, err := m.rdb.Get(ctx, keyBlock(route, client)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var bl Block
	if err := json.Unmarshal(b, &bl); err != nil {
		_ = m.rdb.Del(ctx, keyBlock(route, client)).Err()
		return nil, nil
	}
	return &bl, nil
}

func (m *RedisMitigator) SetBlock(ctx context.Context, route, client string, bl Block, ttl time.Duration) error {
	bl.Exp = time.Now().Add(ttl).Unix()
	j, _ := json.Marshal(bl)
	// NOTE: counters should be incremented by the caller (e.g., detector) to avoid duplicates.
	return m.rdb.Set(ctx, keyBlock(route, client), j, ttl).Err()
}

func (m *RedisMitigator) ClearBlock(ctx context.Context, route, client string) error {
	return m.rdb.Del(ctx, keyBlock(route, client)).Err()
}

// ---- Repeat-offender streak ----
// Increment counter and keep it alive for the window.
func (m *RedisMitigator) IncrStreak(ctx context.Context, route, client string, window time.Duration) (int64, error) {
	k := keyStreak(route, client)
	pipe := m.rdb.Pipeline()
	inc := pipe.Incr(ctx, k)
	pipe.Expire(ctx, k, window)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, err
	}
	return inc.Val(), nil
}

func (m *RedisMitigator) ResetStreak(ctx context.Context, route, client string) error {
	return m.rdb.Del(ctx, keyStreak(route, client)).Err()
}

// ---- Metrics scan helpers ----

// RefreshActiveGauges scans Redis and sets stormgate_active_overrides{route}
// and stormgate_active_blocks{route} from the *current* keys in the store.
// Call this on a ticker (e.g., every 15–30s) from main or router init.
//
// This yields cluster-wide accurate gauges (vs. per-process increments).
func (m *RedisMitigator) RefreshActiveGauges(ctx context.Context) error {
	// Clear previous series so routes that go to zero don’t linger
	metrics.ActiveOverrides.Reset()
	metrics.ActiveBlocks.Reset()

	// Overrides
	ovCounts, err := m.countByRoute(ctx, "sg:override:*")
	if err != nil {
		return err
	}
	for route, n := range ovCounts {
		metrics.ActiveOverrides.WithLabelValues(route).Set(float64(n))
	}

	// Blocks
	blCounts, err := m.countByRoute(ctx, "sg:block:*")
	if err != nil {
		return err
	}
	for route, n := range blCounts {
		metrics.ActiveBlocks.WithLabelValues(route).Set(float64(n))
	}

	return nil
}

// countByRoute scans for a pattern like "sg:override:*" and returns a map[route]count.
// Keys are of the form: "sg:override:<route>:<client>" or "sg:block:<route>:<client>"
func (m *RedisMitigator) countByRoute(ctx context.Context, match string) (map[string]int, error) {
	out := make(map[string]int)
	var cursor uint64
	for {
		keys, next, err := m.rdb.Scan(ctx, cursor, match, 1000).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			parts := strings.SplitN(k, ":", 4) // ["sg","override","<route>","<client>"]
			if len(parts) >= 3 {
				route := parts[2]
				if route != "" {
					out[route]++
				}
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return out, nil
}

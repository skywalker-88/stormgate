package rl

import (
	"context"
	_ "embed"
	"errors"
	"time"

	redis "github.com/redis/go-redis/v9"
)

//go:embed limiter.lua
var limiterLua string

var script = redis.NewScript(limiterLua)

// Limiter wraps a Redis-backed token bucket with variable cost.
type Limiter struct {
	rdb   *redis.Client
	clock func() time.Time
}

func New(rdb *redis.Client) *Limiter {
	return &Limiter{rdb: rdb, clock: time.Now}
}

// Consume tries to consume `cost` tokens from key at `rps` with `burst`.
// Returns (allowed, remainingTokens, retryAfter, resetAfter, err)
func (l *Limiter) Consume(ctx context.Context, key string, rps float64, burst int64, cost int64) (bool, float64, time.Duration, time.Duration, error) {
	if rps <= 0 || burst <= 0 || cost <= 0 {
		return false, 0, 0, 0, errors.New("invalid limiter parameters")
	}
	nowMs := l.clock().UnixMilli()
	res, err := script.Run(ctx, l.rdb, []string{key}, nowMs, rps, burst, cost).Result()
	if err != nil {
		return false, 0, 0, 0, err
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 4 {
		return false, 0, 0, 0, errors.New("unexpected script return")
	}
	allowed := arr[0].(int64) == 1
	remaining, _ := arr[1].(float64)
	retryMs, _ := arr[2].(int64)
	resetMs, _ := arr[3].(int64)
	return allowed, remaining, time.Duration(retryMs) * time.Millisecond, time.Duration(resetMs) * time.Millisecond, nil
}

package ratelimit

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
)

// memberCounter provides globally unique sorted set member IDs across all goroutines.
var memberCounter atomic.Int64

// Prometheus metrics for rate limiting observability.
var (
	rateLimitTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_rate_limit_total",
		Help: "Total rate limit decisions by channel and result",
	}, []string{"channel", "result"})

	rateLimitCurrent = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_rate_limit_current",
		Help: "Current sliding window request count per channel",
	}, []string{"channel"})
)

// RedisClient abstracts the Redis operations used by the limiter,
// enabling test doubles (e.g., miniredis).
type RedisClient interface {
	redis.Cmdable
}

// Result describes the outcome of a rate limit check.
type Result struct {
	Allowed    bool
	Current    int64
	Limit      int64
	RetryAfter time.Duration // Non-zero when Allowed == false
}

// Limiter implements a sliding window rate limiter backed by Redis Sorted Sets.
// Each channel gets its own sorted set key. Members are scored by timestamp (ms),
// and old entries outside the window are pruned on every check.
type Limiter struct {
	rdb    RedisClient
	config Config
	burst  *BurstDetector
}

// NewLimiter creates a rate limiter with the given Redis client and configuration.
func NewLimiter(rdb RedisClient, cfg Config) *Limiter {
	l := &Limiter{
		rdb:    rdb,
		config: cfg,
		burst:  NewBurstDetector(rdb, cfg),
	}
	return l
}

// Allow checks whether a request for the given channel should be allowed.
// It atomically updates the sliding window in Redis using a pipeline.
func (l *Limiter) Allow(ctx context.Context, channel string) (Result, error) {
	if !l.config.Enabled {
		return Result{Allowed: true}, nil
	}

	cl := l.config.LimitFor(channel)

	// Check burst cooldown first — if channel is in cooldown, reject immediately
	if l.burst != nil {
		inCooldown, err := l.burst.InCooldown(ctx, channel)
		if err != nil {
			log.Printf("[ratelimit] burst cooldown check error for %s: %v", channel, err)
			// On error, allow the request (fail open)
		} else if inCooldown {
			rateLimitTotal.WithLabelValues(channel, "rejected").Inc()
			return Result{
				Allowed:    false,
				Limit:      cl.MaxRequests,
				RetryAfter: time.Duration(cl.CooldownSec) * time.Second,
			}, nil
		}
	}

	now := time.Now().UnixMilli()
	windowMs := cl.Window.Milliseconds()
	windowStart := now - windowMs
	key := fmt.Sprintf("ratelimit:%s", channel)
	member := fmt.Sprintf("%d:%d", now, memberCounter.Add(1))

	pipe := l.rdb.Pipeline()

	// Remove entries outside the sliding window
	pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(windowStart, 10))

	// Add current request
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: member})

	// Count entries in window
	countCmd := pipe.ZCard(ctx, key)

	// Set key expiry to window duration (auto-cleanup)
	pipe.Expire(ctx, key, cl.Window+time.Second)

	_, err := pipe.Exec(ctx)
	if err != nil {
		return Result{Allowed: true}, fmt.Errorf("redis pipeline error: %w", err)
	}

	count := countCmd.Val()

	// Update Prometheus gauge
	rateLimitCurrent.WithLabelValues(channel).Set(float64(count))

	// Check burst detection asynchronously
	if l.burst != nil {
		l.burst.RecordAndCheck(ctx, channel, cl)
	}

	if count > cl.MaxRequests {
		rateLimitTotal.WithLabelValues(channel, "rejected").Inc()
		// Calculate retry-after based on oldest entry in window
		retryAfter := cl.Window / time.Duration(cl.MaxRequests)
		log.Printf("[ratelimit] channel=%s rejected: count=%d limit=%d", channel, count, cl.MaxRequests)
		return Result{
			Allowed:    false,
			Current:    count,
			Limit:      cl.MaxRequests,
			RetryAfter: retryAfter,
		}, nil
	}

	rateLimitTotal.WithLabelValues(channel, "allowed").Inc()
	return Result{
		Allowed: true,
		Current: count,
		Limit:   cl.MaxRequests,
	}, nil
}

// BurstDetector returns the limiter's burst detector (for testing).
func (l *Limiter) GetBurstDetector() *BurstDetector {
	return l.burst
}

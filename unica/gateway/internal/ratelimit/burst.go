package ratelimit

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
)

// Prometheus counter for burst detection events.
var burstDetectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "gateway_burst_detected_total",
	Help: "Total burst detection events by channel",
}, []string{"channel"})

// BurstDetector monitors request rates within a short window (10 seconds)
// and triggers a temporary cooldown when the rate exceeds a configurable
// multiplier of the normal rate.
type BurstDetector struct {
	rdb         RedisClient
	config      Config
	burstWindow time.Duration // Fixed at 10 seconds per spec
}

// NewBurstDetector creates a burst detector with the given Redis client and config.
func NewBurstDetector(rdb RedisClient, cfg Config) *BurstDetector {
	return &BurstDetector{
		rdb:         rdb,
		config:      cfg,
		burstWindow: 10 * time.Second,
	}
}

// RecordAndCheck adds the current request to the burst tracking window
// and checks whether the channel has exceeded burst thresholds.
// If burst is detected, it activates cooldown for the channel.
func (b *BurstDetector) RecordAndCheck(ctx context.Context, channel string, cl ChannelLimit) {
	now := time.Now().UnixMilli()
	windowStart := now - b.burstWindow.Milliseconds()
	key := fmt.Sprintf("burst:%s", channel)
	member := fmt.Sprintf("%d:%d", now, memberCounter.Add(1))

	pipe := b.rdb.Pipeline()
	pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(windowStart, 10))
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: member})
	countCmd := pipe.ZCard(ctx, key)
	pipe.Expire(ctx, key, b.burstWindow+time.Second)

	_, err := pipe.Exec(ctx)
	if err != nil {
		log.Printf("[burst] redis pipeline error for %s: %v", channel, err)
		return
	}

	count := countCmd.Val()

	// Calculate burst threshold: normal rate scaled to 10s window * multiplier
	// normalRate = MaxRequests / Window (in seconds) * burstWindow (in seconds)
	normalRateInBurstWindow := float64(cl.MaxRequests) / cl.Window.Seconds() * b.burstWindow.Seconds()
	burstThreshold := int64(normalRateInBurstWindow * cl.BurstMultiplier)

	if burstThreshold < 1 {
		burstThreshold = 1
	}

	if count > burstThreshold {
		log.Printf("[burst] channel=%s burst detected: count=%d threshold=%d (cooldown=%ds)",
			channel, count, burstThreshold, cl.CooldownSec)
		burstDetectedTotal.WithLabelValues(channel).Inc()
		b.activateCooldown(ctx, channel, cl.CooldownSec)
	}
}

// activateCooldown sets a Redis key that causes the limiter to reject
// all requests for the channel during the cooldown period.
func (b *BurstDetector) activateCooldown(ctx context.Context, channel string, cooldownSec int) {
	key := fmt.Sprintf("burst:cooldown:%s", channel)
	err := b.rdb.Set(ctx, key, "1", time.Duration(cooldownSec)*time.Second).Err()
	if err != nil {
		log.Printf("[burst] failed to set cooldown for %s: %v", channel, err)
	}
}

// InCooldown checks whether the channel is currently in burst cooldown.
func (b *BurstDetector) InCooldown(ctx context.Context, channel string) (bool, error) {
	key := fmt.Sprintf("burst:cooldown:%s", channel)
	n, err := b.rdb.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("cooldown check error: %w", err)
	}
	return n > 0, nil
}

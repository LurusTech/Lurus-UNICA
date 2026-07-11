package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupBurstTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return mr, rdb
}

// burstTestConfig creates a config specifically for burst detection tests
// with a realistic BurstMultiplier of 3.0.
func burstTestConfig(maxRequests int64, window time.Duration) Config {
	return Config{
		Enabled: true,
		Default: ChannelLimit{
			MaxRequests:     maxRequests,
			Window:          window,
			BurstMultiplier: 3.0,
			CooldownSec:     5,
		},
		Channels: map[string]ChannelLimit{},
	}
}

func TestBurstDetector_NoBurstUnderThreshold(t *testing.T) {
	_, rdb := setupBurstTestRedis(t)
	cfg := burstTestConfig(600, time.Minute)
	detector := NewBurstDetector(rdb, cfg)
	ctx := context.Background()

	cl := cfg.LimitFor("wechat")
	// Normal rate for 10s window: 600/60*10 = 100 requests
	// Burst threshold: 100 * 3 = 300
	// Send fewer than threshold
	for i := 0; i < 50; i++ {
		detector.RecordAndCheck(ctx, "wechat", cl)
	}

	// Should not be in cooldown
	inCooldown, err := detector.InCooldown(ctx, "wechat")
	if err != nil {
		t.Fatalf("InCooldown error: %v", err)
	}
	if inCooldown {
		t.Error("should NOT be in cooldown with only 50 requests (threshold=300)")
	}
}

func TestBurstDetector_TriggersOnBurst(t *testing.T) {
	_, rdb := setupBurstTestRedis(t)
	// Low limit to make burst easy to trigger: 60/min => 10/10s => burst at 30
	cfg := burstTestConfig(60, time.Minute)
	detector := NewBurstDetector(rdb, cfg)
	ctx := context.Background()

	cl := cfg.LimitFor("test-ch")
	// Normal rate in 10s: 60/60*10 = 10
	// Burst threshold: 10 * 3 = 30
	// Send 31 requests to trigger burst
	for i := 0; i < 31; i++ {
		detector.RecordAndCheck(ctx, "test-ch", cl)
	}

	inCooldown, err := detector.InCooldown(ctx, "test-ch")
	if err != nil {
		t.Fatalf("InCooldown error: %v", err)
	}
	if !inCooldown {
		t.Error("should be in cooldown after burst (31 requests, threshold=30)")
	}
}

func TestBurstDetector_CooldownExpires(t *testing.T) {
	mr, rdb := setupBurstTestRedis(t)
	cfg := burstTestConfig(60, time.Minute)
	cfg.Default.CooldownSec = 2
	detector := NewBurstDetector(rdb, cfg)
	ctx := context.Background()

	cl := cfg.LimitFor("expire-ch")
	// Trigger burst
	for i := 0; i < 35; i++ {
		detector.RecordAndCheck(ctx, "expire-ch", cl)
	}

	// Verify cooldown is active
	inCooldown, _ := detector.InCooldown(ctx, "expire-ch")
	if !inCooldown {
		t.Fatal("expected cooldown to be active")
	}

	// Fast-forward past cooldown
	mr.FastForward(3 * time.Second)

	// Cooldown should have expired
	inCooldown, _ = detector.InCooldown(ctx, "expire-ch")
	if inCooldown {
		t.Error("cooldown should have expired after 3 seconds (cooldown=2s)")
	}
}

func TestBurstDetector_IsolatedByChannel(t *testing.T) {
	_, rdb := setupBurstTestRedis(t)
	cfg := burstTestConfig(60, time.Minute)
	detector := NewBurstDetector(rdb, cfg)
	ctx := context.Background()

	cl := cfg.LimitFor("burst-a")
	// Trigger burst on channel A
	for i := 0; i < 35; i++ {
		detector.RecordAndCheck(ctx, "burst-a", cl)
	}

	// Channel A should be in cooldown
	inCooldown, _ := detector.InCooldown(ctx, "burst-a")
	if !inCooldown {
		t.Error("channel burst-a should be in cooldown")
	}

	// Channel B should NOT be in cooldown
	inCooldown, _ = detector.InCooldown(ctx, "burst-b")
	if inCooldown {
		t.Error("channel burst-b should NOT be in cooldown")
	}
}

func TestBurstDetector_LimiterIntegration(t *testing.T) {
	_, rdb := setupBurstTestRedis(t)
	cfg := burstTestConfig(60, time.Minute)
	cfg.Default.CooldownSec = 5
	limiter := NewLimiter(rdb, cfg)
	ctx := context.Background()

	// The limiter's Allow() calls burst.RecordAndCheck after each allowed request.
	// With 60/min rate and burst multiplier 3: burst threshold = 10*3 = 30
	// Each Allow() call records in BOTH the rate limiter sorted set AND the burst sorted set.
	// Send enough requests to trigger burst but stay within rate limit of 60.
	for i := 0; i < 31; i++ {
		limiter.Allow(ctx, "int-ch")
	}

	// After burst triggers, next request should be rejected due to cooldown
	result, err := limiter.Allow(ctx, "int-ch")
	if err != nil {
		t.Fatalf("Allow() error: %v", err)
	}
	if result.Allowed {
		t.Error("request should be rejected during burst cooldown")
	}
}

func TestBurstDetector_WindowSlides(t *testing.T) {
	mr, rdb := setupBurstTestRedis(t)
	cfg := burstTestConfig(60, time.Minute)
	detector := NewBurstDetector(rdb, cfg)
	ctx := context.Background()

	cl := cfg.LimitFor("slide-ch")

	// Send 20 requests (under threshold of 30)
	for i := 0; i < 20; i++ {
		detector.RecordAndCheck(ctx, "slide-ch", cl)
	}

	// Fast-forward 11 seconds so the burst window entries expire
	mr.FastForward(11 * time.Second)

	// Send another 20 requests -- should not trigger burst because old entries expired
	for i := 0; i < 20; i++ {
		detector.RecordAndCheck(ctx, "slide-ch", cl)
	}

	inCooldown, _ := detector.InCooldown(ctx, "slide-ch")
	if inCooldown {
		t.Error("should NOT be in cooldown after window slide (20 requests < 30 threshold)")
	}
}

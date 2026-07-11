# STORY-011: Rate Limiting + Anti-Abuse

**Epic:** EPIC-001 (Multi-Channel Gateway)
**Priority:** Should Have
**Story Points:** 3
**Status:** Not Started
**Assigned To:** Unassigned
**Created:** 2026-03-06
**Sprint:** 5

---

## User Story

As a system,
I want per-channel API rate limiting and anti-abuse protection,
So that platform rate limits are respected and malicious requests are blocked.

---

## Description

### Background
With 5 channels now active, the gateway handles significant inbound traffic. Without rate limiting, a burst of requests (legitimate or malicious) could exhaust platform API quotas, cause upstream throttling, or degrade service. This story adds a defensive layer to protect both UNICA and the connected platforms.

### Scope
**In scope:**
- Sliding window rate limiter using Redis
- Per-channel configurable rate limits
- 429 Too Many Requests response with Retry-After header
- Burst detection and throttling for abnormal patterns
- Rate limit metrics for Prometheus

**Out of scope:**
- IP-based blocking/WAF (handled at infrastructure level)
- Per-user rate limiting (future enhancement)
- Platform-side rate limit response handling (already handled by adapters)

### User Flow
1. Inbound webhook request arrives at gateway
2. Rate limiter checks sliding window counter for the channel
3. If within limit: request proceeds to adapter processing
4. If exceeded: return 429 with Retry-After header, log event
5. If burst detected: temporarily throttle channel, emit alert

---

## Acceptance Criteria

- [ ] Sliding window rate limiter implemented using Redis (ZRANGEBYSCORE or Lua script)
- [ ] Rate limits configurable per channel type (e.g., WeChat: 1000/min, Douyin: 500/min)
- [ ] Default rate limits applied when no channel-specific config exists
- [ ] 429 response returned with `Retry-After` header when limit exceeded
- [ ] Burst detection: flag when request rate exceeds 3x normal within 10-second window
- [ ] Burst triggers temporary throttle (configurable cooldown period)
- [ ] Rate limit events logged with channel, current count, and limit
- [ ] Prometheus metrics exposed: `gateway_rate_limit_total{channel, result=allowed|rejected}`
- [ ] Rate limiter does not add >2ms latency to normal requests
- [ ] Unit tests for rate limiter logic
- [ ] Integration test with concurrent request simulation

---

## Technical Notes

### Components
- **New files:**
  - `unica/gateway/internal/ratelimit/limiter.go` - Core sliding window rate limiter
  - `unica/gateway/internal/ratelimit/config.go` - Rate limit configuration per channel
  - `unica/gateway/internal/ratelimit/burst.go` - Burst detection logic
  - `unica/gateway/internal/ratelimit/limiter_test.go`
  - `unica/gateway/internal/ratelimit/burst_test.go`
- **Modified files:**
  - `unica/gateway/internal/handler/inbound.go` - Add rate limit middleware check before adapter dispatch
  - `unica/gateway/cmd/gateway/main.go` - Initialize rate limiter with config

### Algorithm: Sliding Window (Redis Sorted Set)
```go
// Pseudo-code for sliding window rate limiter
func (l *Limiter) Allow(ctx context.Context, key string) (bool, error) {
    now := time.Now().UnixMilli()
    windowStart := now - l.windowMs

    pipe := l.redis.Pipeline()
    pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(windowStart, 10))
    pipe.ZAdd(ctx, key, redis.Z{Score: float64(now), Member: now})
    pipe.ZCard(ctx, key)
    pipe.Expire(ctx, key, l.windowDuration)
    // ...
    count := results[2]
    return count <= l.limit, nil
}
```

### Configuration Structure
```go
type ChannelLimit struct {
    Channel     string        // "wechat", "douyin", etc.
    MaxRequests int64         // max requests per window
    Window      time.Duration // sliding window size (e.g., 1 minute)
    BurstMultiplier float64   // burst = normal_rate * multiplier in 10s
    CooldownSec int           // throttle duration on burst
}
```

### Integration Point
Rate limiter is applied as middleware in the inbound handler, before adapter dispatch:
```
Request → Rate Limiter Check → Adapter.VerifyWebhook() → Adapter.ParseInbound() → ...
```

### Prometheus Metrics
- `gateway_rate_limit_total{channel, result}` - Counter
- `gateway_rate_limit_current{channel}` - Gauge (current window count)
- `gateway_burst_detected_total{channel}` - Counter

---

## Dependencies

**Prerequisite Stories:**
- STORY-005: Gateway Core - Redis Streams (Done)

**Blocked Stories:**
- None

**External Dependencies:**
- None

---

## Definition of Done

- [ ] Code implemented and committed to feature branch
- [ ] Unit tests written and passing
  - [ ] Sliding window allows requests within limit
  - [ ] Sliding window rejects requests exceeding limit
  - [ ] Window slides correctly (old entries expire)
  - [ ] Burst detection triggers on abnormal patterns
  - [ ] Burst cooldown releases after timeout
- [ ] Integration test with concurrent goroutines simulating burst
- [ ] Rate limiter integrated into gateway inbound handler
- [ ] Prometheus metrics exposed and verified
- [ ] Service deploys successfully to K3s dev environment
- [ ] Acceptance criteria validated
- [ ] No critical or high severity bugs

---

## Story Points Breakdown

- **Rate limiter core**: 1 point
- **Burst detection**: 1 point
- **Integration + metrics**: 0.5 points
- **Testing**: 0.5 points
- **Total:** 3 points

**Rationale:** Straightforward Redis-based implementation with well-known algorithm. Burst detection adds moderate complexity.

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created

**Actual Effort:** TBD

---

**This story was created using BMAD Method v6 - Phase 4 (Implementation Planning)**

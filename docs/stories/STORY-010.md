# STORY-010: Token Auto-Management

**Epic:** EPIC-001 (Multi-Channel Gateway)
**Priority:** Must Have
**Story Points:** 3
**Status:** Done
**Sprint:** 2
**Created:** 2026-03-05

---

## User Story

As a system, I want platform Access Tokens automatically refreshed before expiration, so that API calls never fail due to expired tokens.

---

## Description

### Background
All social platforms (WeChat, Douyin, Xiaohongshu, Taobao, Kuaishou) require Access Tokens for API calls. These tokens have limited lifetimes (e.g., WeChat: 2 hours). If a token expires, all outbound messages for that channel fail until a new token is obtained.

This story implements a centralized token management service that proactively refreshes tokens before expiration and provides them to channel adapters on demand. The service must handle concurrent token requests efficiently (no thundering herd) and alert on refresh failures.

### Scope
**In scope:**
- Token storage in Redis with TTL-based expiration
- Proactive refresh when TTL < configurable buffer (default 5 minutes before expiry)
- Background refresh goroutine per channel
- Singleflight pattern to deduplicate concurrent refresh requests
- Token provider interface (extensible to all platforms)
- WeChat token refresh implementation (first platform)
- Token refresh failure alerting (publish to `unica:alerts` stream)
- Token accessor API for adapters

**Out of scope:**
- Platform-specific token refresh for Douyin/XHS/Taobao/Kuaishou (will be added when those adapters are built)
- OAuth2 user-level tokens (this is for server-to-server app tokens only)

### Token Lifecycle
```
Startup:
  For each configured channel:
    1. Check Redis for existing token (channel:{id}:token)
    2. If missing or TTL < buffer: refresh immediately
    3. Schedule background refresh at (TTL - buffer) interval

Runtime:
  Adapter calls GetToken(channelID):
    1. Read from Redis → if valid, return immediately
    2. If expired/missing → trigger refresh (singleflight)
    3. Return new token

Background:
  Refresh goroutine per channel:
    1. Sleep until TTL - buffer
    2. Call platform API to get new token
    3. Store in Redis with TTL = expires_in
    4. On failure: retry 3x, then publish alert
```

---

## Acceptance Criteria

- [ ] Token stored in Redis key `channel:{channel_id}:token` with value `{access_token}`
- [ ] Redis TTL set to token's `expires_in` minus 5-minute buffer
- [ ] Background goroutine refreshes token before expiration
- [ ] `GetToken(channelID)` returns valid token within 10ms (cache hit)
- [ ] Concurrent `GetToken` calls during refresh deduplicated via singleflight
- [ ] WeChat token refresh implemented: `GET https://api.weixin.qq.com/cgi-bin/token`
- [ ] Token refresh failure retried 3 times with exponential backoff
- [ ] After 3 refresh failures, alert event published to `unica:alerts` stream
- [ ] Alert includes channel ID, platform type, error details
- [ ] Token works for all platform channels (extensible TokenRefresher interface)
- [ ] Graceful shutdown stops all background refresh goroutines

---

## Technical Notes

### Token Manager Design
```go
type TokenManager struct {
    rdb        *redis.Client
    refreshers map[string]TokenRefresher // channelID -> refresher
    sfGroup    singleflight.Group
    buffer     time.Duration
}

type TokenRefresher interface {
    Platform() string
    Refresh(ctx context.Context, credentials ChannelCredentials) (*TokenResult, error)
}

type TokenResult struct {
    AccessToken string
    ExpiresIn   int // seconds
}
```

### WeChat Token Refresh
```go
func (w *WeChatRefresher) Refresh(ctx context.Context, creds ChannelCredentials) (*TokenResult, error) {
    url := fmt.Sprintf(
        "https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s",
        creds.AppID, creds.AppSecret,
    )
    // HTTP GET, parse response JSON
    // Response: {"access_token":"TOKEN","expires_in":7200}
}
```

### Singleflight Pattern
```go
func (tm *TokenManager) GetToken(ctx context.Context, channelID string) (string, error) {
    // Try Redis first
    token, err := tm.rdb.Get(ctx, fmt.Sprintf("channel:%s:token", channelID)).Result()
    if err == nil {
        return token, nil
    }
    // Cache miss — refresh with singleflight
    result, err, _ := tm.sfGroup.Do(channelID, func() (interface{}, error) {
        return tm.refreshToken(ctx, channelID)
    })
    return result.(string), err
}
```

### Components
- `gateway/internal/token/manager.go` — TokenManager with background refresh
- `gateway/internal/token/refresher.go` — TokenRefresher interface
- `gateway/internal/token/wechat.go` — WeChat token refresh implementation

### Configuration
```
TOKEN_REFRESH_BUFFER=5m
TOKEN_REFRESH_RETRY_MAX=3
TOKEN_REFRESH_RETRY_BACKOFF=2s
```

### Edge Cases
- Redis restart: tokens lost — background goroutine will refresh on next cycle
- Platform API rate limit on token endpoint: respect Retry-After header
- Multiple gateway replicas: singleflight is per-process, but Redis SET is atomic — last write wins (same token, harmless)
- Channel credentials updated: need to restart token refresh for that channel

---

## Dependencies

**Prerequisite:**
- STORY-001 (Redis must be running)

**Blocks:**
- STORY-007 (WeChat Adapter needs access_token for Customer Service API)
- All future channel adapters need token management

---

## Definition of Done

- [ ] TokenManager starts background refresh for configured channels
- [ ] GetToken returns valid token from Redis cache
- [ ] Singleflight prevents thundering herd on cache miss
- [ ] WeChat token refresh calls API and stores result
- [ ] Token refresh failure triggers alert to `unica:alerts`
- [ ] Unit tests for token manager, singleflight, refresh logic (>=80% coverage)
- [ ] Integration test: verify token stored in Redis with correct TTL
- [ ] Code committed to `gateway/internal/token/`

---

## Story Points Breakdown

- **Token manager + background refresh:** 1 point
- **WeChat refresher + singleflight:** 1 point
- **Testing + alerting:** 1 point
- **Total:** 3 points

**Rationale:** Well-understood pattern (cache-aside with proactive refresh). Go's singleflight package handles the hard concurrency part.

# STORY-009: Message Deduplication + Dead-Letter Queue

**Epic:** EPIC-001 (Multi-Channel Gateway)
**Priority:** Must Have
**Story Points:** 3
**Status:** Done
**Sprint:** 2
**Created:** 2026-03-05

---

## User Story

As a system, I want duplicate messages discarded and permanently failed messages routed to a dead-letter queue, so that processing is idempotent and failures are recoverable.

---

## Description

### Background
Social platform webhooks are not guaranteed exactly-once delivery. WeChat retries callbacks up to 3 times if it doesn't receive a timely response. Other platforms have similar retry behavior. Without deduplication, customers would see duplicate AI responses or duplicate conversations in Chatwoot.

Additionally, some messages may permanently fail processing (malformed content, unsupported types, downstream errors after retries). These need a dead-letter queue for manual inspection and potential replay.

### Scope
**In scope:**
- Redis SET NX-based deduplication using platform_msg_id as key
- Configurable TTL for dedup keys (default 24h)
- Dedup middleware integrated into Gateway inbound pipeline
- Retry logic with exponential backoff (3 retries max)
- Dead-letter stream (`unica:dead-letter`) for permanently failed messages
- Dead-letter message metadata (failure reason, retry count, timestamps)
- Query API for dead-letter messages (for manual inspection)

**Out of scope:**
- Automatic dead-letter replay (manual process for now)
- Cross-service deduplication (only gateway-level dedup)
- Message ordering guarantees (Redis Streams handles this)

### Processing Flow
```
Inbound message arrives at Gateway:
  1. Extract platform_msg_id from StandardMessage
  2. SET NX dedup:{platform_msg_id} with TTL 24h
     - If SET succeeded (new message): continue processing
     - If SET failed (duplicate): log, return 200, discard
  3. Process message (publish to stream)
     - On failure: retry with exponential backoff
     - After 3 failures: move to dead-letter stream
```

---

## Acceptance Criteria

- [ ] Duplicate messages detected via Redis SET NX with `dedup:{platform_msg_id}` key
- [ ] Dedup key TTL is configurable (default 24h)
- [ ] Duplicate messages logged with original message ID and discarded
- [ ] Duplicate detection returns 200 to webhook caller (platform should not retry)
- [ ] Failed message processing retried 3 times with exponential backoff (1s, 2s, 4s)
- [ ] After 3 failures, message moved to `unica:dead-letter` Redis Stream
- [ ] Dead-letter entries include: original message, failure reason, retry count, timestamps
- [ ] `GET /api/v1/gateway/dead-letter` returns paginated dead-letter messages
- [ ] Dead-letter entries queryable by time range and channel
- [ ] Messages without platform_msg_id use fallback key: `{from_user}:{create_time}`
- [ ] Dedup middleware does not add >5ms latency to inbound pipeline

---

## Technical Notes

### Dedup Implementation
```go
func (d *Deduplicator) IsDuplicate(ctx context.Context, msgID string) (bool, error) {
    key := fmt.Sprintf("dedup:%s", msgID)
    ok, err := d.rdb.SetNX(ctx, key, "1", d.ttl).Result()
    if err != nil {
        return false, err // Redis error — don't dedup, let it through
    }
    return !ok, nil // ok=false means key existed = duplicate
}
```

### Dead-Letter Stream Entry
```json
{
  "original_message": { ... },
  "failure_reason": "outbound dispatch failed: HTTP 500 from adapter",
  "retry_count": 3,
  "first_attempt_at": "2026-03-05T10:00:00Z",
  "last_attempt_at": "2026-03-05T10:00:07Z",
  "channel_id": "ch_wx_001",
  "platform_msg_id": "wx_msg_789"
}
```

### Retry Backoff
```go
func retryWithBackoff(fn func() error, maxRetries int) error {
    for i := 0; i < maxRetries; i++ {
        if err := fn(); err == nil {
            return nil
        }
        time.Sleep(time.Duration(math.Pow(2, float64(i))) * time.Second)
    }
    return fmt.Errorf("max retries exceeded")
}
```

### Components
- `gateway/internal/dedup/dedup.go` — Deduplication logic (Redis SET NX)
- `gateway/internal/dedup/deadletter.go` — Dead-letter stream management
- `gateway/internal/dedup/handler.go` — Dead-letter query API handler

### Configuration
```
DEDUP_TTL=24h
DEDUP_MAX_RETRIES=3
DEDUP_BACKOFF_BASE=1s
DEAD_LETTER_STREAM=unica:dead-letter
```

### Edge Cases
- Redis unavailable during dedup check — let message through (better duplicate than lost)
- Event messages (subscribe/unsubscribe) may lack MsgId — use fallback dedup key
- Very high throughput may cause Redis memory pressure from dedup keys — TTL ensures cleanup

---

## Dependencies

**Prerequisite:**
- STORY-005 (Gateway Core — Redis Streams, provides the pipeline to add dedup middleware)

**Blocks:**
- None directly, but required for production reliability of all channel adapters

---

## Definition of Done

- [ ] Dedup middleware integrated into gateway inbound pipeline
- [ ] Duplicate messages correctly detected and discarded
- [ ] Retry logic with exponential backoff implemented
- [ ] Dead-letter stream receives permanently failed messages
- [ ] Dead-letter query API functional
- [ ] Unit tests for dedup logic and retry logic (>=80% coverage)
- [ ] Integration test: send duplicate message, verify only one processed
- [ ] Code committed to `gateway/internal/dedup/`

---

## Story Points Breakdown

- **Dedup logic (Redis SET NX):** 1 point
- **Retry + dead-letter:** 1 point
- **Testing:** 1 point
- **Total:** 3 points

**Rationale:** Straightforward Redis operations with well-known patterns. Low complexity but high reliability impact.

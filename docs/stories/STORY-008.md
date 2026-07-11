# STORY-008: Douyin Adapter

**Epic:** EPIC-001 (Multi-Channel Gateway)
**Priority:** Must Have
**Story Points:** 5
**Status:** Not Started
**Sprint:** 4
**Created:** 2026-03-05

---

## User Story

As a customer on Douyin, I want my messages received and replies delivered, so that I can get customer service on Douyin.

---

## Description

### Background
Douyin (抖音) is one of the 5 target platforms for UNICA. Unlike WeChat's XML-based protocol, Douyin's Open Platform uses JSON for message payloads and HMAC-SHA256 for webhook signature verification. This adapter is the second channel adapter after WeChat and follows the same ChannelAdapter interface (STORY-006).

The WeChat adapter (STORY-007) established the pattern — this story replicates the pattern for Douyin's specific protocol.

### Scope
**In scope:**
- HTTP webhook endpoint for Douyin message push callbacks
- HMAC-SHA256 signature verification per Douyin spec
- JSON message parsing for supported types: text, image, video, card
- Conversion from Douyin JSON to StandardMessage
- Conversion from StandardMessage to Douyin reply format
- Outbound reply via Douyin Open API (私信接口)
- Webhook verification endpoint (Douyin challenge request)
- Media URL mapping for image/video content

**Development Mode: Mock Adapter**
- No enterprise developer account available yet (personal account cannot access im API)
- Implement full adapter logic with mock webhook endpoint for testing
- Use simulated Douyin message payloads for unit/integration tests
- When enterprise account is ready, swap mock endpoint for real Douyin API — no code structure changes needed

**Out of scope:**
- Douyin live stream messages
- Douyin comment replies (only private messages)
- Douyin ad/marketing API
- Token management (handled by STORY-010, extending to Douyin)

### User Flow
1. Customer sends private message to Douyin business account
2. Douyin server pushes message to adapter webhook endpoint
3. Adapter verifies HMAC-SHA256 signature
4. Adapter parses JSON payload and converts to StandardMessage
5. StandardMessage forwarded to Gateway for stream publishing
6. When reply arrives from outbound stream:
7. Adapter converts StandardMessage to Douyin format
8. Adapter calls Douyin Open API to send reply
9. Customer sees reply in Douyin private message

---

## Acceptance Criteria

- [ ] Webhook verification endpoint handles Douyin challenge request
- [ ] POST `/webhook/douyin` receives and processes Douyin JSON messages
- [ ] HMAC-SHA256 signature verification rejects invalid signatures with 403
- [ ] Text messages parsed correctly (content, sender, receiver, timestamp)
- [ ] Image messages parsed correctly (media URL extracted)
- [ ] Video messages parsed correctly (media URL extracted)
- [ ] Card messages handled (converted to link type StandardMessage)
- [ ] All inbound messages converted to valid StandardMessage JSON
- [ ] Outbound text replies sent via Douyin Open API successfully
- [ ] Outbound image replies sent via Douyin Open API successfully
- [ ] Invalid/unsupported message types logged and gracefully skipped
- [ ] Token refresher extended to support Douyin access_token
- [ ] Integration test with mock Douyin webhook passes

---

## Technical Notes

### Douyin Message Format (Inbound JSON)
```json
{
  "event": "im",
  "from_user_id": "user_open_id_xxx",
  "to_user_id": "app_user_id_xxx",
  "message_type": "text",
  "content": "{\"text\":\"Hello\"}",
  "msg_id": "msg_123456",
  "create_time": 1680000000
}
```

### Douyin Reply API (Outbound JSON)
```
POST https://open.douyin.com/api/im/send/msg/
Authorization: Bearer {access_token}

{
  "to_user_id": "user_open_id_xxx",
  "msg_type": "text",
  "content": "{\"text\":\"Reply message\"}"
}
```

### HMAC-SHA256 Signature Verification
```go
func verifySignature(secret, timestamp, nonce, body, signature string) bool {
    raw := timestamp + nonce + body
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(raw))
    expected := hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(signature))
}
```

### Components
- `gateway/internal/adapter/douyin/adapter.go` — ChannelAdapter interface implementation
- `gateway/internal/adapter/douyin/handler.go` — HTTP webhook handler
- `gateway/internal/adapter/douyin/signature.go` — HMAC-SHA256 verification
- `gateway/internal/token/douyin.go` — Douyin token refresher

### Configuration (environment variables)
```
DOUYIN_CLIENT_KEY=xxxxxxxxxxxxxxxx
DOUYIN_CLIENT_SECRET=xxxxxxxxxxxxxxxx
DOUYIN_WEBHOOK_SECRET=xxxxxxxxxxxxxxxx
```

### Edge Cases
- Douyin may retry failed webhook callbacks — dedup via msg_id (STORY-009)
- Content field in Douyin messages is a JSON string within JSON — requires double parsing
- Douyin access_token has shorter TTL than WeChat — token refresher must handle this
- Rate limits differ from WeChat — respect Douyin-specific limits

---

## Dependencies

**Prerequisite:**
- STORY-005 (Gateway Core — Redis Streams integration)
- STORY-006 (Standard Message Format + Adapter Interface)
- STORY-010 (Token Auto-Management — extend for Douyin)

**Blocks:**
- Sprint 5 full channel coverage validation

**External Dependencies:**
- Enterprise Douyin Open Platform account (deferred — develop with mock first)
- When enterprise account ready: register application, whitelist IP, enable im permissions

---

## Definition of Done

- [ ] Douyin adapter implements ChannelAdapter interface
- [ ] Webhook verification handles Douyin challenge
- [ ] Inbound message parsing handles text, image, video, card types
- [ ] HMAC-SHA256 signature verification passes for valid, rejects invalid
- [ ] Outbound messages sent via Douyin Open API
- [ ] Token refresher extended for Douyin
- [ ] Unit tests for JSON parsing, signature verification (>=80% coverage)
- [ ] Integration test with mock webhook passes
- [ ] Mock server for local testing committed
- [ ] Code committed to `gateway/internal/adapter/douyin/`
- [ ] Adapter registered in adapter registry

---

## Story Points Breakdown

- **JSON parsing + message conversion:** 1 point
- **HMAC-SHA256 signature verification:** 1 point
- **Douyin Open API outbound:** 1 point
- **Token refresher extension + testing:** 2 points
- **Total:** 5 points

**Rationale:** Simpler than WeChat (JSON vs XML, no encryption layer). The adapter pattern is established — this is primarily protocol translation work.

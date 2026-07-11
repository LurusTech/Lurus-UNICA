# STORY-013: Taobao Adapter

**Epic:** EPIC-001 (Multi-Channel Gateway)
**Priority:** Must Have
**Story Points:** 5
**Status:** Not Started
**Assigned To:** Unassigned
**Created:** 2026-03-06
**Sprint:** 5

---

## User Story

As a customer on Taobao,
I want my messages received and replies delivered,
So that I can get customer service on Taobao.

---

## Description

### Background
UNICA already supports WeChat, Douyin, and Xiaohongshu channels. Taobao is a core e-commerce platform where customers frequently need product consultation and after-sales support. Adding Taobao expands coverage to 4 of 5 target channels.

### Scope
**In scope:**
- Taobao Open Platform webhook/polling message ingestion
- Signature verification per Taobao spec
- Message parsing (text, image, order-related) to StandardMessage
- Outbound reply via Taobao Open API
- Token refresher for Taobao access tokens
- Webhook route registration in gateway
- Unit tests and integration tests with mock

**Out of scope:**
- Taobao order data synchronization
- Taobao store management features
- Rich card/mini-program message types (future enhancement)

### User Flow
1. Customer sends message via Taobao IM (app or web)
2. Taobao pushes message to UNICA webhook endpoint (or UNICA polls via API)
3. Gateway verifies signature, parses message to StandardMessage
4. Message published to `unica:inbound` Redis Stream
5. Router processes and generates AI/human response
6. Response published to `unica:outbound` Redis Stream
7. Gateway consumes outbound, formats for Taobao API, sends reply
8. Customer receives reply in Taobao IM

---

## Acceptance Criteria

- [ ] Webhook/polling endpoint receives Taobao messages (per Taobao Open Platform API spec)
- [ ] Signature verification passes per Taobao spec (HMAC-MD5 or RSA, per API version)
- [ ] JSON/HTTP message parsing for text, image message types
- [ ] Messages converted to StandardMessage format with correct PlatformMeta
- [ ] PlatformMsgID correctly extracted for deduplication
- [ ] Outbound reply sent via Taobao customer service message API
- [ ] Token refresher implemented for Taobao access token lifecycle
- [ ] Adapter registered in gateway Registry and webhook route mounted
- [ ] Unit tests covering: signature verification, inbound parsing, outbound formatting, send message
- [ ] Integration test with mock Taobao API server
- [ ] Test coverage >= 80% for adapter package

---

## Technical Notes

### Components
- **New files** (follow existing adapter pattern):
  - `unica/gateway/internal/adapter/taobao/adapter.go` - ChannelAdapter implementation
  - `unica/gateway/internal/adapter/taobao/handler.go` - HTTP webhook handler
  - `unica/gateway/internal/adapter/taobao/signature.go` - Signature verification
  - `unica/gateway/internal/adapter/taobao/adapter_test.go`
  - `unica/gateway/internal/adapter/taobao/handler_test.go`
  - `unica/gateway/internal/adapter/taobao/signature_test.go`
  - `unica/gateway/internal/token/taobao.go` - Token refresher
- **Modified files**:
  - `unica/gateway/cmd/gateway/main.go` - Register Taobao adapter + webhook route

### API Details
- **Taobao Open Platform**: Uses HTTP POST for message push (or HTTP polling for some API versions)
- **Signature**: Taobao uses `sign` parameter with HMAC-MD5 (sorted params + app_secret)
- **Message API**: `https://eco.taobao.com/router/rest` with `taobao.miniapp.message.send` or customer service API
- **Token**: OAuth2 flow, refresh via `taobao.top.auth.token.refresh`

### Key Differences from Other Adapters
- Taobao may use **polling model** instead of webhook push (research required during implementation)
- If polling: implement a background goroutine with configurable poll interval
- Signature uses HMAC-MD5 (not SHA1/SHA256 like WeChat/Douyin)
- Message format is JSON but wrapped in Taobao's TOP protocol envelope

### Reference Implementation
Follow the pattern established by Douyin adapter (`unica/gateway/internal/adapter/douyin/`):
- Struct: `Adapter{cfg, channelID, httpClient, getToken, sendURL}`
- Implement: `VerifyWebhook()`, `ParseInbound()`, `FormatOutbound()`, `SendMessage()`
- Handler: HTTP POST handler with signature check + challenge support

### Edge Cases
- Taobao may send order-related system notifications (filter or map to event type)
- Rate limits on Taobao API (respect X-RateLimit headers)
- Polling mode: handle empty responses gracefully, avoid busy-loop
- Session expiry: Taobao sessions have limited validity, need proactive refresh

---

## Dependencies

**Prerequisite Stories:**
- STORY-005: Gateway Core - Redis Streams (Done)
- STORY-006: Standard Message Format + Adapter Interface (Done)
- STORY-010: Token Auto-Management (Done)

**Blocked Stories:**
- None

**External Dependencies:**
- Taobao Open Platform developer account and API access approved
- Taobao app credentials (App Key, App Secret) configured

---

## Definition of Done

- [ ] Code implemented and committed to feature branch
- [ ] Unit tests written and passing (>= 80% coverage)
  - [ ] Signature verification tests (valid/invalid/tampered)
  - [ ] Inbound message parsing tests (text, image, edge cases)
  - [ ] Outbound formatting tests
  - [ ] Send message tests (mocked HTTP)
  - [ ] Token refresher tests
- [ ] Integration test with mock Taobao server passing
- [ ] Adapter registered in gateway, webhook route accessible
- [ ] Service deploys successfully to K3s dev environment
- [ ] Acceptance criteria validated
- [ ] No critical or high severity bugs

---

## Story Points Breakdown

- **Adapter implementation**: 2 points
- **Signature + polling/webhook**: 1 point
- **Token refresher**: 0.5 points
- **Testing**: 1.5 points
- **Total:** 5 points

**Rationale:** Similar complexity to Douyin (5pts) and Xiaohongshu (5pts) adapters. Potential polling model adds slight complexity but offset by established adapter pattern.

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created

**Actual Effort:** TBD

---

**This story was created using BMAD Method v6 - Phase 4 (Implementation Planning)**

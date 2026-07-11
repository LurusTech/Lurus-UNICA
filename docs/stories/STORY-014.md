# STORY-014: Kuaishou Adapter

**Epic:** EPIC-001 (Multi-Channel Gateway)
**Priority:** Must Have
**Story Points:** 5
**Status:** Not Started
**Assigned To:** Unassigned
**Created:** 2026-03-06
**Sprint:** 5

---

## User Story

As a customer on Kuaishou,
I want my messages received and replies delivered,
So that I can get customer service on Kuaishou.

---

## Description

### Background
Kuaishou is the 5th and final target channel for UNICA. Completing this adapter achieves full multi-channel coverage across all 5 platforms (WeChat, Douyin, Xiaohongshu, Taobao, Kuaishou). Kuaishou's API follows a webhook-based model similar to Douyin.

### Scope
**In scope:**
- Kuaishou Open Platform webhook message ingestion
- Signature verification per Kuaishou spec
- Message parsing (text, image, video) to StandardMessage
- Outbound reply via Kuaishou Open API
- Token refresher for Kuaishou access tokens
- Webhook route registration in gateway
- Unit tests and integration tests with mock

**Out of scope:**
- Kuaishou live-stream commerce integration
- Kuaishou short video comment replies
- Rich interactive message types

### User Flow
1. Customer sends message via Kuaishou IM
2. Kuaishou pushes message to UNICA webhook endpoint
3. Gateway verifies signature, parses message to StandardMessage
4. Message published to `unica:inbound` Redis Stream
5. Router processes and generates AI/human response
6. Response published to `unica:outbound` Redis Stream
7. Gateway consumes outbound, formats for Kuaishou API, sends reply
8. Customer receives reply in Kuaishou IM

---

## Acceptance Criteria

- [ ] Webhook endpoint receives Kuaishou message push
- [ ] Signature verification passes per Kuaishou Open Platform spec
- [ ] JSON message parsing for text, image, video message types
- [ ] Messages converted to StandardMessage format with correct PlatformMeta
- [ ] PlatformMsgID correctly extracted for deduplication
- [ ] Outbound reply sent via Kuaishou customer service message API
- [ ] Token refresher implemented for Kuaishou access token lifecycle
- [ ] Adapter registered in gateway Registry and webhook route mounted
- [ ] Challenge/verification request handled for webhook setup
- [ ] Unit tests covering: signature verification, inbound parsing, outbound formatting, send message
- [ ] Integration test with mock Kuaishou API server
- [ ] Test coverage >= 80% for adapter package

---

## Technical Notes

### Components
- **New files** (follow existing adapter pattern):
  - `unica/gateway/internal/adapter/kuaishou/adapter.go` - ChannelAdapter implementation
  - `unica/gateway/internal/adapter/kuaishou/handler.go` - HTTP webhook handler
  - `unica/gateway/internal/adapter/kuaishou/signature.go` - Signature verification
  - `unica/gateway/internal/adapter/kuaishou/adapter_test.go`
  - `unica/gateway/internal/adapter/kuaishou/handler_test.go`
  - `unica/gateway/internal/adapter/kuaishou/signature_test.go`
  - `unica/gateway/internal/token/kuaishou.go` - Token refresher
- **Modified files**:
  - `unica/gateway/cmd/gateway/main.go` - Register Kuaishou adapter + webhook route

### API Details
- **Kuaishou Open Platform**: Webhook-based message push (similar to Douyin)
- **Signature**: HMAC-SHA256 based verification (similar to Douyin pattern)
- **Message API**: `https://open.kuaishou.com/openapi/mp/developer/message/send`
- **Token**: OAuth2 flow with access_token + refresh_token

### Reference Implementation
Follow the Douyin adapter pattern exactly (`unica/gateway/internal/adapter/douyin/`):
- Struct: `Adapter{cfg, channelID, httpClient, getToken, sendURL}`
- Implement: `VerifyWebhook()`, `ParseInbound()`, `FormatOutbound()`, `SendMessage()`
- Handler: HTTP POST with signature verification + challenge response
- Signature: HMAC-SHA256 (reuse similar logic from Douyin `signature.go`)

### Key Similarities to Douyin
- Both use JSON message format
- Both use HMAC-SHA256 signature verification
- Both support challenge request for webhook verification
- Both use OAuth2 token management

### Edge Cases
- Kuaishou may send live-stream related notifications (filter to supported types)
- Video messages may include HLS URLs (map to `video` content type with URL)
- Rate limits on Kuaishou API
- Webhook retry: Kuaishou retries failed deliveries, dedup handles this

---

## Dependencies

**Prerequisite Stories:**
- STORY-005: Gateway Core - Redis Streams (Done)
- STORY-006: Standard Message Format + Adapter Interface (Done)
- STORY-010: Token Auto-Management (Done)

**Blocked Stories:**
- None

**External Dependencies:**
- Kuaishou Open Platform developer account and API access approved
- Kuaishou app credentials (App Key, App Secret) configured

---

## Definition of Done

- [ ] Code implemented and committed to feature branch
- [ ] Unit tests written and passing (>= 80% coverage)
  - [ ] Signature verification tests (valid/invalid/tampered)
  - [ ] Inbound message parsing tests (text, image, video, edge cases)
  - [ ] Outbound formatting tests
  - [ ] Send message tests (mocked HTTP)
  - [ ] Challenge request handling tests
  - [ ] Token refresher tests
- [ ] Integration test with mock Kuaishou server passing
- [ ] Adapter registered in gateway, webhook route accessible
- [ ] Service deploys successfully to K3s dev environment
- [ ] Acceptance criteria validated
- [ ] No critical or high severity bugs

---

## Story Points Breakdown

- **Adapter implementation**: 2 points
- **Signature + webhook handler**: 1 point
- **Token refresher**: 0.5 points
- **Testing**: 1.5 points
- **Total:** 5 points

**Rationale:** Nearly identical complexity to Douyin adapter (5pts). Webhook model, HMAC-SHA256 signature, JSON messages - well-established pattern.

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created

**Actual Effort:** TBD

---

**This story was created using BMAD Method v6 - Phase 4 (Implementation Planning)**

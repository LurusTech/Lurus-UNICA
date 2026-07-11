# STORY-012: Xiaohongshu Adapter

**Epic:** EPIC-001 (Multi-Channel Gateway)
**Priority:** Must Have
**Story Points:** 5
**Status:** Not Started
**Sprint:** 4
**Created:** 2026-03-05

---

## User Story

As a customer on Xiaohongshu, I want my messages received and replies delivered, so that I can get customer service on Xiaohongshu.

---

## Description

### Background
Xiaohongshu (小红书) is one of the 5 target platforms for UNICA. Xiaohongshu's Open Platform provides a messaging API for business accounts to receive and respond to customer private messages. Like Douyin, it uses JSON for message payloads. This is the third channel adapter, following the established ChannelAdapter pattern.

### Scope
**In scope:**
- HTTP webhook endpoint for Xiaohongshu message push callbacks
- Signature verification per Xiaohongshu spec
- JSON message parsing for supported types: text, image, video, note link
- Conversion from Xiaohongshu JSON to StandardMessage
- Conversion from StandardMessage to Xiaohongshu reply format
- Outbound reply via Xiaohongshu Open API
- Webhook verification endpoint (challenge request)
- Media URL mapping for image/video content

**Development Mode: Mock Adapter**
- No enterprise developer account available yet (personal account cannot access messaging API)
- Implement full adapter logic with mock webhook endpoint for testing
- Use simulated XHS message payloads for unit/integration tests
- When enterprise account is ready, swap mock endpoint for real XHS API — no code structure changes needed

**Out of scope:**
- Xiaohongshu note comments
- Xiaohongshu ad/marketing API
- Xiaohongshu store/e-commerce integration
- Token management (handled by STORY-010, extending to XHS)

### User Flow
1. Customer sends private message to Xiaohongshu business account
2. Xiaohongshu server pushes message to adapter webhook endpoint
3. Adapter verifies signature
4. Adapter parses JSON payload and converts to StandardMessage
5. StandardMessage forwarded to Gateway for stream publishing
6. When reply arrives from outbound stream:
7. Adapter converts StandardMessage to Xiaohongshu format
8. Adapter calls Xiaohongshu API to send reply
9. Customer sees reply in Xiaohongshu private message

---

## Acceptance Criteria

- [ ] Webhook verification endpoint handles Xiaohongshu challenge request
- [ ] POST `/webhook/xiaohongshu` receives and processes XHS JSON messages
- [ ] Signature verification rejects invalid signatures with 403
- [ ] Text messages parsed correctly
- [ ] Image messages parsed correctly (media URL extracted)
- [ ] Video messages parsed correctly (media URL extracted)
- [ ] Note link messages handled (converted to link type StandardMessage)
- [ ] All inbound messages converted to valid StandardMessage JSON
- [ ] Outbound text replies sent via XHS API successfully
- [ ] Outbound image replies sent via XHS API successfully
- [ ] Invalid/unsupported message types logged and gracefully skipped
- [ ] Token refresher extended to support XHS access_token
- [ ] Integration test with mock XHS webhook passes

---

## Technical Notes

### Components
- `gateway/internal/adapter/xiaohongshu/adapter.go` — ChannelAdapter interface implementation
- `gateway/internal/adapter/xiaohongshu/handler.go` — HTTP webhook handler
- `gateway/internal/adapter/xiaohongshu/signature.go` — Signature verification
- `gateway/internal/token/xiaohongshu.go` — XHS token refresher

### Configuration (environment variables)
```
XHS_APP_ID=xxxxxxxxxxxxxxxx
XHS_APP_SECRET=xxxxxxxxxxxxxxxx
XHS_WEBHOOK_SECRET=xxxxxxxxxxxxxxxx
```

### Edge Cases
- XHS may retry failed webhook callbacks — dedup via msg_id (STORY-009)
- XHS note link messages contain note_id — convert to link type with note URL
- XHS API rate limits — respect platform-specific limits
- Media URLs may have expiration — download or cache if needed

---

## Dependencies

**Prerequisite:**
- STORY-005 (Gateway Core — Redis Streams integration)
- STORY-006 (Standard Message Format + Adapter Interface)
- STORY-010 (Token Auto-Management — extend for XHS)

**Blocks:**
- Sprint 5 full channel coverage validation

**External Dependencies:**
- Enterprise Xiaohongshu Open Platform account (deferred — develop with mock first)
- When enterprise account ready: register application, enable messaging permissions

---

## Definition of Done

- [ ] XHS adapter implements ChannelAdapter interface
- [ ] Webhook verification handles XHS challenge
- [ ] Inbound message parsing handles text, image, video, note link types
- [ ] Signature verification passes for valid, rejects invalid
- [ ] Outbound messages sent via XHS API
- [ ] Token refresher extended for XHS
- [ ] Unit tests for JSON parsing, signature verification (>=80% coverage)
- [ ] Integration test with mock webhook passes
- [ ] Mock server for local testing committed
- [ ] Code committed to `gateway/internal/adapter/xiaohongshu/`
- [ ] Adapter registered in adapter registry

---

## Story Points Breakdown

- **JSON parsing + message conversion:** 1 point
- **Signature verification:** 1 point
- **XHS API outbound:** 1 point
- **Token refresher extension + testing:** 2 points
- **Total:** 5 points

**Rationale:** Same complexity as Douyin adapter. JSON-based, established pattern. Primary work is mapping XHS-specific message types and API endpoints.

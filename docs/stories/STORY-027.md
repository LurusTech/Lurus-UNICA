# STORY-027: Satisfaction Survey

**Epic:** EPIC-004 (Human Agent Workspace)
**Priority:** Should Have
**Story Points:** 3
**Status:** Not Started
**Sprint:** 4
**Created:** 2026-03-05

---

## User Story

As a business, I want satisfaction surveys sent after conversations close, so that we can measure customer experience quality.

---

## Description

### Background
After a customer service conversation ends (either AI-only or human-assisted), the system should automatically send a satisfaction survey to the customer. The customer rates their experience (1-5 stars) by replying with a number or clicking a button. The rating is stored in conversation metadata for reporting (STORY-029).

This addresses FR-020 (Satisfaction Survey).

### Scope
**In scope:**
- Auto-send survey message when conversation state transitions to `closed`
- Survey message template (text-based, works across all platforms)
- Customer reply parsed as satisfaction rating (1-5)
- Rating stored in conversation metadata (DB)
- Survey timeout: if no reply in 24h, mark as "no_response"
- Configurable: enable/disable survey per product line

**Out of scope:**
- Rich survey UI (buttons, cards) — text-only for cross-platform compatibility
- Multi-question surveys (single rating only)
- Free-text feedback collection
- Real-time satisfaction dashboard (STORY-029 handles reporting)

### Survey Flow
```
1. Conversation transitions to "closed" state
2. State manager publishes close event
3. Survey handler checks:
   - Is survey enabled for this product line?
   - Was conversation long enough (>= 2 messages from customer)?
   - Has survey already been sent for this conversation?
4. If yes: compose survey message and publish to unica:outbound
5. Customer receives: "感谢您的咨询！请为本次服务评分（1-5分，5分最高）"
6. Customer replies with a number (1-5)
7. Gateway receives reply -> Router detects survey context
8. Rating stored in conversations.satisfaction_score
9. Conversation remains closed (survey reply does not reopen)
```

---

## Acceptance Criteria

- [ ] Survey message sent automatically after conversation closes
- [ ] Survey only sent if conversation has >= 2 customer messages
- [ ] Survey only sent once per conversation (idempotent)
- [ ] Customer can reply with 1-5 to rate
- [ ] Rating stored in conversation metadata (satisfaction_score column)
- [ ] Invalid reply (not 1-5) sends a gentle retry prompt (once)
- [ ] No reply within 24h → mark as "no_response"
- [ ] Survey can be enabled/disabled per product line
- [ ] Survey reply does not reopen the conversation
- [ ] Works across all channels (WeChat, Douyin, XHS)

---

## Technical Notes

### Database Schema
```sql
ALTER TABLE conversations ADD COLUMN satisfaction_score SMALLINT;
ALTER TABLE conversations ADD COLUMN survey_sent_at TIMESTAMPTZ;
ALTER TABLE conversations ADD COLUMN survey_status VARCHAR(20) DEFAULT 'not_sent';
-- survey_status: 'not_sent', 'sent', 'completed', 'no_response'
```

### Survey Message Template
```
感谢您的咨询！请为本次服务评分：
回复数字 1-5（5分最高）
1⭐ 非常不满意
2⭐ 不满意
3⭐ 一般
4⭐ 满意
5⭐ 非常满意
```

### Survey Context Detection
```
Redis Key:   survey:pending:{conversation_id}
Value:       { "sent_at": timestamp, "channel_id": "...", "customer_id": "..." }
TTL:         24h

When inbound message arrives for a conversation in "closed" state:
  1. Check if survey:pending:{conversation_id} exists in Redis
  2. If yes: parse message as rating (1-5)
  3. Store rating, clear Redis key
  4. If not valid number: send retry message (only once)
```

### Components
- `router/internal/survey/handler.go` — Survey dispatch + rating collection
- `router/internal/survey/handler_test.go` — Unit tests
- Update `router/internal/state/manager.go` — Hook survey dispatch on close transition
- Update `router/internal/routing/router.go` — Detect survey reply context
- DB migration: add satisfaction columns to conversations table

### Product Line Config
```json
// product_lines.config_json
{
  "survey": {
    "enabled": true,
    "min_customer_messages": 2,
    "timeout_hours": 24
  }
}
```

### Metrics
```
survey_sent_total                 counter   {product_line}
survey_completed_total            counter   {product_line, score}
survey_timeout_total              counter   {product_line}
survey_avg_score                  gauge     {product_line}
```

---

## Dependencies

**Prerequisite:**
- STORY-015 (Conversation State Machine — close event)
- STORY-005 (Gateway Core — outbound message delivery)

**Blocks:**
- STORY-029 (AI Effectiveness Reporting — uses satisfaction data)

**External Dependencies:**
- None

---

## Definition of Done

- [ ] Survey sent automatically on conversation close
- [ ] Rating collection works (1-5 parsed and stored)
- [ ] Survey idempotent (not sent twice for same conversation)
- [ ] Timeout handling marks "no_response" after 24h
- [ ] Per-product-line enable/disable works
- [ ] DB migration applied (satisfaction columns)
- [ ] Unit tests for survey handler (>=80% coverage)
- [ ] Integration test: close conversation -> survey sent -> rating collected
- [ ] Code committed to `router/internal/survey/`

---

## Story Points Breakdown

- **Survey dispatch logic:** 1 point
- **Rating collection + context detection:** 1 point
- **DB migration + config + testing:** 1 point
- **Total:** 3 points

**Rationale:** Low-moderate complexity. Core logic is straightforward (send message on close, parse reply). Main subtlety is the survey context detection — recognizing that a reply to a closed conversation is a survey response, not a new conversation.

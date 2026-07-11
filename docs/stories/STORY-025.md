# STORY-025: Conversation History Sync + Context Display

**Epic:** EPIC-004 (Human Agent Workspace)
**Priority:** Must Have
**Story Points:** 5
**Status:** Not Started
**Sprint:** 4
**Created:** 2026-03-05

---

## User Story

As a human agent, I want to see the customer's complete conversation history including AI interactions, so that I have full context.

---

## Description

### Background
STORY-017 (AI-Human Handoff) syncs the current AI conversation to Chatwoot when a handoff occurs. However, agents also need access to the customer's previous conversations (across sessions) and a clear visual distinction between AI and human messages. This story deepens the Chatwoot integration to provide full historical context.

This addresses FR-017 (History View) and FR-019 (Customer Info Sidebar).

### Scope
**In scope:**
- Sync all AI conversation messages to Chatwoot on handoff (not just current session)
- AI messages visually distinguished from human messages (sender label/type)
- Customer's previous conversations accessible in Chatwoot contact view
- Search within conversation history (leverage Chatwoot built-in search)
- AI intent summary displayed at conversation top (as private note)
- Customer metadata displayed in Chatwoot contact sidebar (platform, first seen, tags)

**Out of scope:**
- Real-time AI message streaming to Chatwoot during AI processing phase
- Cross-product-line conversation history (each product line isolated)
- Custom Chatwoot UI modifications (use API + native features only)

### Context Display Flow
```
Handoff occurs (STORY-017):
  1. Fetch full conversation history from unica_core.messages
  2. For each message, push to Chatwoot with correct sender type:
     - Customer messages: message_type = "incoming"
     - AI responses: message_type = "outgoing", sender label = "AI Assistant"
  3. Post AI intent summary as private note (visible to agent only)
  4. Update Chatwoot contact with customer metadata:
     - Platform identity (WeChat OpenID, Douyin UID, etc.)
     - Channel source
     - First seen timestamp
     - Previous conversation count
     - Tags (if any)

Agent views conversation:
  - Sees full message history with AI/human labels
  - Sees intent summary at top
  - Can view customer's previous conversations via Chatwoot contact page
  - Can search messages within conversation
```

---

## Acceptance Criteria

- [ ] All AI conversation messages synced to Chatwoot on handoff
- [ ] AI messages labeled distinctly from human agent messages
- [ ] Customer's previous closed conversations accessible in Chatwoot contact view
- [ ] Intent summary displayed as private note at top of conversation
- [ ] Customer metadata visible in Chatwoot contact sidebar (platform, channel, first_seen)
- [ ] Search within conversation history works (Chatwoot native)
- [ ] Message ordering preserved (chronological) after sync
- [ ] Large conversation history (>50 messages) synced without timeout
- [ ] Sync does not create duplicate messages on repeated handoffs

---

## Technical Notes

### History Sync Logic
```go
// router/internal/handoff/history_sync.go

func (h *HandoffHandler) SyncConversationHistory(ctx context.Context, conv *Conversation, cwConvID int) error {
    // 1. Fetch all messages for this conversation from DB
    messages, err := h.msgRepo.GetByConversationID(ctx, conv.ID)

    // 2. Check already-synced watermark to avoid duplicates
    lastSynced := h.getLastSyncedMsgID(ctx, conv.ID)

    // 3. For each unsent message, push to Chatwoot
    for _, msg := range messages {
        if msg.ID <= lastSynced { continue }
        cwMsg := mapToChatwootMessage(msg)
        h.chatwoot.SendMessage(ctx, accountID, cwConvID, cwMsg)
    }

    // 4. Update watermark
    h.setLastSyncedMsgID(ctx, conv.ID, messages[len(messages)-1].ID)
}
```

### Message Sender Mapping
```
UNICA sender_type  -> Chatwoot message_type
"customer"         -> "incoming"
"ai"               -> "outgoing" (content_attributes: { sender_name: "AI Assistant" })
"agent"            -> "outgoing"
"system"           -> "outgoing" (private: true, as note)
```

### Customer Contact Enrichment
```go
// Update Chatwoot contact with UNICA customer metadata
type ContactUpdate struct {
    Name             string            `json:"name"`
    Identifier       string            `json:"identifier"`
    CustomAttributes map[string]string `json:"custom_attributes"`
}

// custom_attributes:
// {
//   "platform": "wechat",
//   "channel_name": "品牌A公众号",
//   "first_seen": "2026-03-01T10:00:00Z",
//   "total_conversations": "5",
//   "tags": "VIP,frequent"
// }
```

### Deduplication (Sync Watermark)
```
Redis Key:   history_sync:{unica_conversation_id}
Value:       last_synced_message_id
TTL:         7d (cleanup after conversation closed)
```

### Components
- `router/internal/handoff/history_sync.go` — Full history sync logic
- `router/internal/handoff/history_sync_test.go` — Unit tests
- Update `router/internal/bridge/chatwoot.go` — Add contact update, batch message push
- Update `router/internal/handoff/handler.go` — Call history sync on handoff

### Metrics
```
history_sync_messages_total       counter   {product_line}
history_sync_duration_seconds     histogram
history_sync_errors_total         counter   {error_type}
```

---

## Dependencies

**Prerequisite:**
- STORY-024 (Chatwoot Custom Channel — base integration)
- STORY-017 (AI-Human Handoff — triggers sync)

**Blocks:**
- None

**External Dependencies:**
- None

---

## Definition of Done

- [ ] Full conversation history synced to Chatwoot on handoff
- [ ] AI vs human messages visually distinguishable
- [ ] Customer metadata visible in Chatwoot contact sidebar
- [ ] Intent summary shown as private note
- [ ] No duplicate messages on repeated handoffs (watermark dedup)
- [ ] Large history sync handles 50+ messages without timeout
- [ ] Previous conversations visible via Chatwoot contact page
- [ ] Unit tests for history sync logic (>=80% coverage)
- [ ] Integration test: handoff with history sync verified end-to-end
- [ ] Code committed to `router/internal/handoff/` and `router/internal/bridge/`

---

## Story Points Breakdown

- **History sync logic + dedup:** 2 points
- **Contact enrichment + metadata:** 1 point
- **Chatwoot client extensions:** 1 point
- **Testing:** 1 point
- **Total:** 5 points

**Rationale:** Moderate complexity. Main work is the bulk message sync with dedup, contact metadata mapping, and ensuring message ordering. Leverages existing Chatwoot client from STORY-024.

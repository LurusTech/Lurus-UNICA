# STORY-017: AI-Human Handoff with Context

**Epic:** EPIC-002 (Conversation Routing & Management)
**Priority:** Must Have
**Story Points:** 5
**Status:** Not Started
**Sprint:** 3
**Created:** 2026-03-05

---

## User Story

As a human agent, I want to see the full AI conversation history and intent summary when a conversation is handed off, so that I can continue without asking the customer to repeat.

---

## Description

### Background
When the AI cannot confidently handle a customer's question (STORY-022), the conversation must seamlessly transition to a human agent. The critical requirement is context preservation — the human agent must see everything the AI discussed with the customer, plus an AI-generated summary of the customer's intent. Without this, the agent would ask the customer to repeat themselves, creating a frustrating experience.

This story implements the handoff mechanism: consuming handoff events, packaging conversation context, and routing to Chatwoot for human handling.

### Scope
**In scope:**
- Consume handoff events from `unica:handoff` stream
- Generate AI intent summary (via Dify summarization call)
- Package full conversation history for Chatwoot
- Create/update conversation in Chatwoot with context
- Transition conversation state to `human_processing`
- Ensure customer receives holding message during transition
- Handoff latency target: < 2s from trigger to Chatwoot

**Out of scope:**
- Agent scheduling and distribution (STORY-018)
- Chatwoot custom channel creation (STORY-024 — this story uses Chatwoot API)
- Satisfaction survey post-close (STORY-027)

### Handoff Flow
```
1. STORY-022 publishes handoff event to unica:handoff stream
2. Router's handoff consumer reads event
3. Load full conversation history from messages table
4. Generate intent summary:
   - Call Dify with summarization prompt:
     "Summarize this customer conversation in 1-2 sentences.
      Focus on: what the customer wants, what was tried, why handoff."
   - Input: last N messages as context
5. Package context for Chatwoot:
   - All messages (AI + customer) with timestamps
   - Intent summary as first "note" message
   - Customer metadata (platform, channel, tags)
6. Create conversation in Chatwoot (via custom channel API)
   - Or update existing conversation if already created
7. Send all messages to Chatwoot conversation
8. Update conversation state: human_processing
9. Update Redis session: assigned to Chatwoot conversation_id
10. XACK the handoff event
```

---

## Acceptance Criteria

- [ ] Handoff triggered when AI confidence < threshold (STORY-022)
- [ ] Handoff triggered by user keyword ("转人工", etc.)
- [ ] Full conversation history (all AI + customer messages) sent to Chatwoot
- [ ] AI-generated intent summary attached as conversation note in Chatwoot
- [ ] Intent summary includes: customer intent, what was discussed, handoff reason
- [ ] Handoff event published to `unica:handoff` stream with full context
- [ ] Conversation state transitions to `human_processing`
- [ ] Customer receives holding message ("正在为您转接人工客服，请稍候...")
- [ ] Subsequent customer messages routed to Chatwoot (not AI)
- [ ] Handoff latency < 2s from trigger to conversation visible in Chatwoot
- [ ] Handoff reason logged and visible in conversation metadata

---

## Technical Notes

### Handoff Consumer
```go
// unica/router/internal/handoff/handler.go

type HandoffHandler struct {
    db            *sql.DB
    rdb           *redis.Client
    stateManager  *state.Manager
    difyClient    *bridge.DifyClient
    chatwootClient *bridge.ChatwootClient
}

type HandoffEvent struct {
    ConversationID  string  `json:"conversation_id"`
    ProductLineID   string  `json:"product_line_id"`
    Reason          string  `json:"reason"`
    ConfidenceScore float64 `json:"confidence_score"`
    SuppressedReply string  `json:"ai_response_suppressed"`
    CustomerMessage string  `json:"customer_message"`
    Timestamp       string  `json:"timestamp"`
}

func (h *HandoffHandler) Handle(ctx context.Context, event *HandoffEvent) error {
    // 1. Load conversation history
    messages, err := h.loadMessages(ctx, event.ConversationID)

    // 2. Generate intent summary
    summary, err := h.generateSummary(ctx, event.ProductLineID, messages)

    // 3. Send to Chatwoot
    cwConvID, err := h.sendToChatwoot(ctx, event, messages, summary)

    // 4. Update state
    h.stateManager.Transition(event.ConversationID, state.HumanProcessing)

    // 5. Update session
    h.rdb.HSet(ctx, sessionKey, "chatwoot_conversation_id", cwConvID)
}
```

### Intent Summary Generation
```go
func (h *HandoffHandler) generateSummary(ctx context.Context, plID string, messages []Message) (string, error) {
    // Build conversation transcript
    transcript := buildTranscript(messages)

    // Call Dify with summarization prompt
    summaryPrompt := fmt.Sprintf(
        "请用1-2句话总结以下客服对话。重点说明：客户的需求是什么，AI已经尝试了什么，为什么需要转人工。\n\n对话记录：\n%s",
        transcript,
    )

    resp, err := h.difyClient.Chat(ctx, plConfig, summaryPrompt, "")
    return resp.Answer, err
}
```

### Chatwoot Integration
```go
// unica/router/internal/bridge/chatwoot.go

type ChatwootClient struct {
    httpClient *http.Client
    baseURL    string
    apiToken   string
}

// Create conversation in Chatwoot account (per product line)
func (c *ChatwootClient) CreateConversation(ctx context.Context, accountID int, contactID int, inboxID int) (*CWConversation, error)

// Send message to existing conversation
func (c *ChatwootClient) SendMessage(ctx context.Context, accountID int, convID int, content string, msgType string) error

// Add note (for intent summary)
func (c *ChatwootClient) AddNote(ctx context.Context, accountID int, convID int, content string) error

// Types
type CWConversation struct {
    ID        int    `json:"id"`
    AccountID int    `json:"account_id"`
    InboxID   int    `json:"inbox_id"`
    Status    string `json:"status"`
}
```

### Chatwoot API Endpoints Used
```
POST /api/v1/accounts/{account_id}/conversations
  → Create conversation

POST /api/v1/accounts/{account_id}/conversations/{conv_id}/messages
  → Send message (content, message_type: "incoming"/"outgoing"/"activity")

POST /api/v1/accounts/{account_id}/conversations/{conv_id}/notes  (or activity message)
  → Add intent summary as activity note
```

### Message Sync Format
When syncing AI conversation to Chatwoot:
```
[AI Intent Summary - Note]
客户咨询产品A的退款政策，AI回答了基本退款流程，但客户表示收到了损坏的商品，需要特殊处理。转人工原因：置信度低 (0.45)

[Message History]
[2026-03-05 10:20] 客户: 你好，我想问一下退款
[2026-03-05 10:20] AI: 您好！关于退款，我们的标准退款政策是...
[2026-03-05 10:21] 客户: 但是我收到的商品是坏的
[2026-03-05 10:21] AI: [置信度不足，已转人工]
```

### Metrics
```
router_handoff_total                 counter   {reason, product_line}
router_handoff_duration_seconds      histogram Time from trigger to Chatwoot ready
router_handoff_summary_duration_seconds histogram Time to generate intent summary
```

### Components
- `unica/router/internal/handoff/handler.go` — Handoff event processing
- `unica/router/internal/handoff/summary.go` — Intent summary generation
- `unica/router/internal/bridge/chatwoot.go` — Chatwoot API client
- `unica/router/cmd/router/main.go` — Register handoff stream consumer

---

## Dependencies

**Prerequisite:**
- STORY-015 (Conversation State Machine — state transitions)
- STORY-016 (Intelligent Routing — Router service, Dify client)
- STORY-021 (Router-Dify Integration — enhanced Dify client for summary)
- STORY-022 (Confidence Scoring — handoff event publisher)

**Blocks:**
- STORY-018 (Agent Scheduling — extends handoff with agent assignment)
- STORY-025 (Conversation History Sync — builds on Chatwoot integration)

---

## Definition of Done

- [ ] Handoff consumer reads from `unica:handoff` stream
- [ ] Full conversation history sent to Chatwoot
- [ ] AI-generated intent summary visible in Chatwoot conversation
- [ ] Conversation state transitions to `human_processing`
- [ ] Subsequent customer messages routed to Chatwoot
- [ ] Handoff latency < 2s verified
- [ ] Customer receives holding message during handoff
- [ ] Unit tests for handoff handler, summary generation (>=80% coverage)
- [ ] Integration test: trigger handoff → verify Chatwoot conversation created with history + summary
- [ ] Code committed to `unica/router/`

---

## Story Points Breakdown

- **Handoff consumer + handler:** 1.5 points
- **Intent summary generation:** 1 point
- **Chatwoot client + message sync:** 1.5 points
- **Testing:** 1 point
- **Total:** 5 points

**Rationale:** Moderate complexity — orchestrates multiple systems (Redis Streams, Dify summarization, Chatwoot API) and requires careful message formatting and state management. The Chatwoot client is new code that STORY-024 will expand on.

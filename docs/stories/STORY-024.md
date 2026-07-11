# STORY-024: Chatwoot Custom Channel Integration

**Epic:** EPIC-004 (Human Agent Workspace)
**Priority:** Must Have
**Story Points:** 8
**Status:** Not Started
**Sprint:** 3
**Created:** 2026-03-05

---

## User Story

As a human agent, I want all channel messages displayed in Chatwoot's unified inbox, so that I can handle conversations from one interface.

---

## Description

### Background
Chatwoot is UNICA's human agent workspace. For agents to receive and respond to customer messages from all 5 platforms, Chatwoot must be connected to the UNICA message pipeline. Chatwoot's "Custom Channel" (API Channel) feature allows external systems to push messages in and capture agent replies via webhooks. This story implements the bidirectional integration:

1. **Inbound:** UNICA pushes customer messages and AI handoffs into Chatwoot
2. **Outbound:** Agent replies in Chatwoot are captured and routed back through UNICA's outbound pipeline to the correct platform

This is the largest story in Sprint 3 (8 points) because it requires deep Chatwoot API integration, webhook handling, and bidirectional message flow.

### Scope
**In scope:**
- Create Chatwoot API Channel (custom inbox) per product line
- Push inbound customer messages to Chatwoot via API
- Capture agent replies via Chatwoot webhook
- Agent replies published to `unica:outbound` stream for platform delivery
- Real-time message updates (Chatwoot WebSocket for live status)
- Channel source indicator on each message (WeChat/Douyin/etc.)
- Contact (customer) creation and management in Chatwoot
- Conversation lifecycle sync (create, update, close)

**Out of scope:**
- AI conversation history sync on handoff (STORY-017 handles initial sync, STORY-025 deepens it)
- Agent scheduling and assignment (STORY-018)
- Quick reply templates (STORY-026)
- Satisfaction surveys (STORY-027)

### Bidirectional Flow
```
INBOUND (Customer → Chatwoot):
  1. Customer sends message on WeChat/Douyin/etc.
  2. Gateway receives, publishes to unica:inbound
  3. Router processes:
     - If human_processing state: forward to Chatwoot
     - If handoff triggered: STORY-017 creates Chatwoot conversation
  4. Push message to Chatwoot:
     POST /api/v1/accounts/{id}/conversations/{id}/messages
  5. Agent sees message in Chatwoot inbox

OUTBOUND (Chatwoot → Customer):
  1. Agent types reply in Chatwoot
  2. Chatwoot fires webhook to UNICA callback URL:
     POST /api/v1/webhook/chatwoot
     { event: "message_created", message: {...}, conversation: {...} }
  3. UNICA webhook handler:
     a. Verify webhook authenticity
     b. Extract agent reply content
     c. Map Chatwoot conversation → UNICA conversation_id → channel + customer
     d. Publish to unica:outbound stream with correct routing metadata
  4. Gateway picks up from outbound stream
  5. Correct adapter formats and sends to platform
```

---

## Acceptance Criteria

- [ ] Chatwoot API Channel (custom inbox) created per product line (Account)
- [ ] Customer messages forwarded to Chatwoot when conversation is in `human_processing` state
- [ ] Agent replies in Chatwoot captured via webhook callback
- [ ] Agent replies published to `unica:outbound` stream with correct channel routing info
- [ ] Reply delivered to customer on original platform (WeChat/Douyin/etc.)
- [ ] Real-time message delivery: customer message appears in Chatwoot within 1s
- [ ] Channel source indicator visible on each message (platform icon/label)
- [ ] Customer contact auto-created in Chatwoot with platform identity
- [ ] Conversation closed in Chatwoot → state transitions to `closed` in UNICA
- [ ] Conversation reopened (new customer message) → new conversation created in Chatwoot
- [ ] Webhook endpoint secured (token verification)
- [ ] Multiple product lines isolated (each uses separate Chatwoot Account)

---

## Technical Notes

### Chatwoot Setup (per product line)
```
1. Create Chatwoot Account per product line (maps to product_line_id)
2. Create API Channel inbox in each account
3. Configure webhook URL: https://{unica-host}/api/v1/webhook/chatwoot
4. Store Chatwoot account_id + inbox_id + api_token in product_lines.config_json
```

### Chatwoot Configuration in DB
```json
// product_lines.config_json
{
  "chatwoot": {
    "account_id": 1,
    "inbox_id": 1,
    "api_token": "encrypted_token",
    "webhook_token": "verification_token",
    "base_url": "http://chatwoot:3000"
  }
}
```

### Chatwoot API Client (expanded from STORY-017)
```go
// unica/router/internal/bridge/chatwoot.go

type ChatwootClient struct {
    httpClient *http.Client
    baseURL    string
}

// Contact Management
func (c *ChatwootClient) FindOrCreateContact(ctx context.Context, accountID int, identifier string, name string, metadata map[string]string) (*CWContact, error)

// Conversation Management
func (c *ChatwootClient) CreateConversation(ctx context.Context, accountID int, req CreateConversationReq) (*CWConversation, error)
func (c *ChatwootClient) GetConversation(ctx context.Context, accountID int, convID int) (*CWConversation, error)

// Message Management
func (c *ChatwootClient) SendMessage(ctx context.Context, accountID int, convID int, msg SendMessageReq) (*CWMessage, error)

// Types
type CreateConversationReq struct {
    SourceID    string            `json:"source_id"`    // UNICA conversation_id
    InboxID     int               `json:"inbox_id"`
    ContactID   int               `json:"contact_id"`
    Status      string            `json:"status"`
    CustomAttrs map[string]string `json:"custom_attributes"`
}

type SendMessageReq struct {
    Content     string `json:"content"`
    MessageType string `json:"message_type"` // "incoming" (customer) or "outgoing" (agent/AI)
    Private     bool   `json:"private"`       // true for notes
    ContentType string `json:"content_type"`  // "text", "input_select", etc.
    ContentAttrs map[string]interface{} `json:"content_attributes,omitempty"`
}
```

### Webhook Handler
```go
// unica/gateway/internal/webhook/chatwoot.go

type ChatwootWebhookHandler struct {
    rdb           *redis.Client
    db            *sql.DB
    streamPublisher *stream.Publisher
}

type ChatwootWebhookPayload struct {
    Event        string        `json:"event"`         // "message_created", "conversation_status_changed"
    MessageType  string        `json:"message_type"`  // "incoming", "outgoing"
    Content      string        `json:"content"`
    Conversation CWWebhookConv `json:"conversation"`
    Account      CWWebhookAcct `json:"account"`
}

func (h *ChatwootWebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
    // 1. Verify webhook token
    // 2. Parse payload
    // 3. Filter: only process "message_created" with message_type="outgoing" (agent replies)
    // 4. Map Chatwoot conversation → UNICA conversation_id
    // 5. Resolve channel + customer from UNICA conversation
    // 6. Publish to unica:outbound stream
}
```

### Conversation Mapping (Redis)
```
Key:   cw_conv:{chatwoot_conversation_id}
Value: {unica_conversation_id}

Key:   unica_conv_cw:{unica_conversation_id}
Value: {chatwoot_conversation_id}

TTL:   None (persist until conversation closed + cleanup)
```

### Message Source Indicator
Custom attributes on Chatwoot messages to show platform origin:
```json
{
  "custom_attributes": {
    "source_platform": "wechat",
    "source_channel": "品牌A微信公众号",
    "original_msg_type": "text"
  }
}
```

### Webhook Endpoint
```
POST /api/v1/webhook/chatwoot

Headers:
  X-Chatwoot-Webhook-Token: {verification_token}

Events to handle:
  - message_created (message_type=outgoing) → Agent reply → publish to outbound
  - conversation_status_changed (status=resolved) → Close UNICA conversation
  - conversation_status_changed (status=open) → Reopen if needed
```

### Metrics
```
chatwoot_messages_pushed_total        counter   {product_line, direction=inbound}
chatwoot_webhook_received_total       counter   {event_type}
chatwoot_agent_replies_total          counter   {product_line}
chatwoot_push_duration_seconds        histogram Message push latency
chatwoot_webhook_errors_total         counter   {error_type}
```

### Components
- `unica/router/internal/bridge/chatwoot.go` — Full Chatwoot API client
- `unica/gateway/internal/webhook/chatwoot.go` — Webhook receiver + outbound publisher
- `unica/router/internal/routing/chatwoot_forwarder.go` — Push inbound messages to Chatwoot
- `unica/scripts/setup_chatwoot_channels.go` — Setup script for API channels
- Database: conversation mapping stored in Redis

### Setup Script
```go
// unica/scripts/setup_chatwoot_channels.go
// For each product line:
// 1. Create Chatwoot Account (or use existing)
// 2. Create API Channel inbox
// 3. Configure webhook URL
// 4. Store credentials in product_lines.config_json
```

---

## Dependencies

**Prerequisite:**
- STORY-003 (Chatwoot deployed and accessible)
- STORY-015 (Conversation State Machine — state management)

**Blocks:**
- STORY-025 (Conversation History Sync — deepens Chatwoot message display)
- STORY-026 (Quick Reply Templates — uses Chatwoot canned responses)
- STORY-027 (Satisfaction Survey — dispatched via Chatwoot)

---

## Definition of Done

- [ ] Chatwoot API Channel created per product line
- [ ] Customer messages forwarded to Chatwoot in `human_processing` state
- [ ] Agent replies captured via webhook and published to outbound stream
- [ ] End-to-end verified: customer message on WeChat → agent sees in Chatwoot → agent replies → customer receives reply on WeChat
- [ ] Customer contacts auto-created in Chatwoot
- [ ] Platform source indicator visible on messages
- [ ] Webhook endpoint secured with token verification
- [ ] Conversation close in Chatwoot syncs to UNICA state
- [ ] Unit tests for Chatwoot client, webhook handler (>=80% coverage)
- [ ] Integration test: push message to Chatwoot → simulate webhook → verify outbound
- [ ] Setup script committed and documented
- [ ] Code committed to `unica/router/` and `unica/gateway/`

---

## Story Points Breakdown

- **Chatwoot API client (contacts, conversations, messages):** 2 points
- **Webhook handler + outbound publishing:** 2 points
- **Message forwarding + conversation mapping:** 2 points
- **Setup script + testing:** 2 points
- **Total:** 8 points

**Rationale:** High complexity — this is the largest story in Sprint 3. It involves bidirectional API integration (push + webhook), conversation lifecycle management, contact management, cross-system mapping, and careful state synchronization between UNICA and Chatwoot.

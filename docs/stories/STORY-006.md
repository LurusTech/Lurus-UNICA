# STORY-006: Standard Message Format + Adapter Interface

**Epic:** EPIC-001 (Multi-Channel Gateway)
**Priority:** Must Have
**Story Points:** 3
**Status:** Completed
**Sprint:** 1
**Created:** 2026-03-04

---

## User Story

As a developer, I want a well-defined standard message format and channel adapter interface, so that all platform adapters follow a consistent contract and are independently implementable.

---

## Description

### Background
UNICA connects 5 different platforms, each with its own message format (WeChat XML, Douyin JSON, etc.). The standard message format and adapter interface are the abstraction layer that makes the rest of the system platform-agnostic. This is a design + implementation story that defines the contracts all adapters must follow.

### Scope
**In scope:**
- StandardMessage JSON schema (detailed, with all message types)
- CloudEvents envelope for stream messages
- ChannelAdapter Go interface with full method signatures
- Message type definitions (text, image, video, link, event)
- Platform metadata preservation
- JSON serialization/deserialization with validation
- Unit tests for all message types
- Documentation of the adapter contract

**Out of scope:**
- Actual adapter implementations (STORY-007+)
- Message deduplication logic (STORY-009)

---

## Acceptance Criteria

- [x] `StandardMessage` struct supports text, image, video, link, and event content types
- [x] CloudEvents envelope includes: id, type, source, subject, time, data
- [x] `ChannelAdapter` interface defined with 4 methods: VerifyWebhook, ParseInbound, FormatOutbound, SendMessage
- [x] JSON serialization round-trip test passes for all content types
- [x] Platform metadata (original message ID, platform user ID, account info) preserved in message
- [x] Validation function rejects malformed messages with clear errors
- [x] Unit tests cover all message types and edge cases (empty content, oversized, Unicode)
- [x] Interface documented with Go doc comments explaining each method's contract

---

## Technical Notes

### StandardMessage (expanded from STORY-002 skeleton)
```go
package model

import "time"

// CloudEvents-compatible message envelope
type StandardMessage struct {
    ID      string      `json:"id"`                // UUID, unique per message
    Type    string      `json:"type"`              // "message.inbound" | "message.outbound" | "event.handoff"
    Source  string      `json:"source"`            // "adapter.wechat" | "adapter.douyin" | "router" | "chatwoot"
    Subject string     `json:"subject,omitempty"`  // "conversation:{id}"
    Time    time.Time   `json:"time"`
    Data    MessageData `json:"data"`
}

type MessageData struct {
    ConversationID string          `json:"conversation_id,omitempty"`
    ChannelID      string          `json:"channel_id"`
    ProductLineID  string          `json:"product_line_id,omitempty"`
    CustomerID     string          `json:"customer_id,omitempty"`
    Content        MessageContent  `json:"content"`
    PlatformMsgID  string          `json:"platform_msg_id"`
    PlatformMeta   PlatformMeta    `json:"platform_meta,omitempty"`
    CorrelationID  string          `json:"correlation_id"`
}

type MessageContent struct {
    Type  string `json:"type"`            // "text" | "image" | "video" | "link" | "event"
    Text  string `json:"text,omitempty"`
    URL   string `json:"url,omitempty"`   // For image/video/link
    Title string `json:"title,omitempty"` // For link type
    Desc  string `json:"desc,omitempty"`  // For link type
    Event string `json:"event,omitempty"` // For event type (subscribe, unsubscribe, etc.)
}

type PlatformMeta struct {
    PlatformUserID string `json:"platform_user_id"` // OpenID, UID, etc.
    AccountID      string `json:"account_id"`       // Which official account / store
    RawType        string `json:"raw_type"`         // Original platform message type
}
```

### ChannelAdapter Interface
```go
package adapter

import "net/http"

// ChannelAdapter defines the contract all platform adapters must implement.
// Each method handles one phase of the message lifecycle.
type ChannelAdapter interface {
    // VerifyWebhook validates the platform's webhook signature/token.
    // Returns nil if valid, error with details if invalid.
    VerifyWebhook(r *http.Request) error

    // ParseInbound extracts and normalizes an inbound platform message
    // into a StandardMessage. The original http.Request contains the raw payload.
    ParseInbound(r *http.Request) (*model.StandardMessage, error)

    // FormatOutbound converts a StandardMessage into platform-specific payload bytes.
    // The caller (gateway) will pass these bytes to SendMessage.
    FormatOutbound(msg *model.StandardMessage) ([]byte, error)

    // SendMessage delivers the platform-formatted payload to the platform API.
    // Returns nil on success, error on failure (gateway will retry).
    SendMessage(channelID string, payload []byte) error
}
```

### Validation
```go
func ValidateMessage(msg *StandardMessage) error {
    if msg.ID == "" { return errors.New("message ID required") }
    if msg.Data.ChannelID == "" { return errors.New("channel_id required") }
    if msg.Data.Content.Type == "" { return errors.New("content type required") }
    validTypes := map[string]bool{"text":true, "image":true, "video":true, "link":true, "event":true}
    if !validTypes[msg.Data.Content.Type] { return fmt.Errorf("invalid content type: %s", msg.Data.Content.Type) }
    if msg.Data.Content.Type == "text" && msg.Data.Content.Text == "" { return errors.New("text content required for text type") }
    return nil
}
```

### Test Cases
```
TestStandardMessage_TextRoundTrip
TestStandardMessage_ImageRoundTrip
TestStandardMessage_VideoRoundTrip
TestStandardMessage_LinkRoundTrip
TestStandardMessage_EventRoundTrip
TestStandardMessage_ValidationRejectsEmpty
TestStandardMessage_ValidationRejectsInvalidType
TestStandardMessage_UnicodeContent
TestStandardMessage_LargeContent
```

---

## Dependencies

**Prerequisite:**
- STORY-002 (Go scaffolding — this story expands the pkg/model package)

**Blocks:**
- STORY-005 (Gateway uses StandardMessage for stream publish/consume)
- STORY-007+ (All adapters implement ChannelAdapter interface)

**Note:** STORY-005 and STORY-006 can be developed in parallel since they share the same package.

---

## Definition of Done

- [x] StandardMessage fully defined with all content types
- [x] ChannelAdapter interface defined with doc comments
- [x] Validation function implemented
- [x] All unit tests passing (17 top-level tests, 23 with subtests)
- [x] Go doc comments on all exported types and methods
- [x] Code in `pkg/model/` and `gateway/internal/adapter/`

---

## Progress Tracking

**Status History:**
- 2026-03-04: Created
- 2026-03-05: Implemented and completed

**Actual Effort:** 3 points (matched estimate)

**Implementation Notes:**
- Expanded StandardMessage with full CloudEvents fields (ID, Type, Source, Subject, Time, Data)
- Added PlatformMeta struct for preserving platform-specific identifiers
- Added MessageContent with 5 content types: text, image, video, link, event
- Added type constants (MessageType* and ContentType*) for type safety
- ValidateMessage covers nil check, required fields, content type validation, and type-specific content constraints
- ChannelAdapter interface moved to gateway/internal/adapter with context.Context parameter and channelID on SendMessage
- 17 top-level test functions (23 subtests total) covering round-trips, validation, Unicode, large content, omitempty behavior
- All tests passing, gateway module compiles successfully

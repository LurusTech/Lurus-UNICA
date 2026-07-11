package model

import (
	"errors"
	"fmt"
	"time"
)

// --- Message Type Constants ---

// MessageType constants define the envelope-level event types (CloudEvents "type" field).
const (
	MessageTypeInbound  = "message.inbound"
	MessageTypeOutbound = "message.outbound"
	MessageTypeHandoff  = "event.handoff"
)

// ContentType constants define the payload content types carried inside MessageContent.
const (
	ContentTypeText  = "text"
	ContentTypeImage = "image"
	ContentTypeVideo = "video"
	ContentTypeLink  = "link"
	ContentTypeEvent = "event"
)

// validContentTypes is the set of accepted content types for validation.
var validContentTypes = map[string]bool{
	ContentTypeText:  true,
	ContentTypeImage: true,
	ContentTypeVideo: true,
	ContentTypeLink:  true,
	ContentTypeEvent: true,
}

// --- Core Message Types ---

// StandardMessage represents a CloudEvents-compatible envelope for all messages
// flowing through the UNICA system. Every inbound platform message is normalized
// into this format by a ChannelAdapter, and every outbound message is converted
// from this format into a platform-specific payload.
//
// Fields follow the CloudEvents v1.0 specification:
//   - ID:      unique message identifier (UUID)
//   - Type:    event type, e.g. "message.inbound", "message.outbound", "event.handoff"
//   - Source:  origin of the event, e.g. "adapter.wechat", "router", "chatwoot"
//   - Subject: optional context key, typically "conversation:{id}"
//   - Time:    timestamp when the event was produced
//   - Data:    the business payload
type StandardMessage struct {
	ID      string      `json:"id"`               // UUID, unique per message
	Type    string      `json:"type"`              // "message.inbound" | "message.outbound" | "event.handoff"
	Source  string      `json:"source"`            // "adapter.wechat" | "adapter.douyin" | "router" | "chatwoot"
	Subject string      `json:"subject,omitempty"` // "conversation:{id}"
	Time    time.Time   `json:"time"`
	Data    MessageData `json:"data"`
}

// MessageData contains the business payload of a message, including routing
// information, content, and platform-specific metadata.
type MessageData struct {
	ConversationID string         `json:"conversation_id,omitempty"` // Internal conversation identifier
	ChannelID      string         `json:"channel_id"`               // Channel through which the message flows
	ProductLineID  string         `json:"product_line_id,omitempty"`
	CustomerID     string         `json:"customer_id,omitempty"`
	Content        MessageContent `json:"content"`
	PlatformMsgID  string         `json:"platform_msg_id"`            // Original message ID from the platform
	PlatformMeta   PlatformMeta   `json:"platform_meta,omitempty"`    // Platform-specific metadata
	CorrelationID  string         `json:"correlation_id"`             // Trace / correlation identifier for distributed tracing
}

// MessageContent holds the actual user-facing content of a message.
// Exactly one of the optional fields should be populated based on Type.
type MessageContent struct {
	Type  string `json:"type"`            // "text" | "image" | "video" | "link" | "event"
	Text  string `json:"text,omitempty"`  // Populated when Type == "text"
	URL   string `json:"url,omitempty"`   // Populated when Type == "image" | "video" | "link"
	Title string `json:"title,omitempty"` // Populated when Type == "link"
	Desc  string `json:"desc,omitempty"`  // Populated when Type == "link"
	Event string `json:"event,omitempty"` // Populated when Type == "event" (e.g. "subscribe", "unsubscribe")
}

// PlatformMeta preserves platform-specific information that does not fit into
// the normalized message fields. This allows downstream consumers to access
// original platform identifiers when needed (e.g. for reply threading).
type PlatformMeta struct {
	PlatformUserID string `json:"platform_user_id"` // OpenID, UID, etc.
	AccountID      string `json:"account_id"`       // Which official account / store / app
	RawType        string `json:"raw_type"`         // Original platform message type string
}

// --- Validation ---

// ValidateMessage checks that a StandardMessage has all required fields set and
// that the content type is one of the allowed values. It also enforces
// type-specific content constraints (e.g. text messages must have non-empty Text).
//
// Returns nil if the message is valid, or an error describing the first
// validation failure encountered.
func ValidateMessage(msg *StandardMessage) error {
	if msg == nil {
		return errors.New("message must not be nil")
	}
	if msg.ID == "" {
		return errors.New("message ID required")
	}
	if msg.Type == "" {
		return errors.New("message type required")
	}
	if msg.Source == "" {
		return errors.New("message source required")
	}
	if msg.Time.IsZero() {
		return errors.New("message time required")
	}
	if msg.Data.ChannelID == "" {
		return errors.New("channel_id required")
	}
	if msg.Data.Content.Type == "" {
		return errors.New("content type required")
	}
	if !validContentTypes[msg.Data.Content.Type] {
		return fmt.Errorf("invalid content type: %s", msg.Data.Content.Type)
	}

	// Type-specific content validation
	switch msg.Data.Content.Type {
	case ContentTypeText:
		if msg.Data.Content.Text == "" {
			return errors.New("text content required for text type")
		}
	case ContentTypeImage, ContentTypeVideo:
		if msg.Data.Content.URL == "" {
			return errors.New("url required for " + msg.Data.Content.Type + " type")
		}
	case ContentTypeLink:
		if msg.Data.Content.URL == "" {
			return errors.New("url required for link type")
		}
		if msg.Data.Content.Title == "" {
			return errors.New("title required for link type")
		}
	case ContentTypeEvent:
		if msg.Data.Content.Event == "" {
			return errors.New("event name required for event type")
		}
	}

	return nil
}

package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// --- Helpers ---

// newValidMessage returns a minimal valid StandardMessage for testing.
func newValidMessage(contentType string) *StandardMessage {
	msg := &StandardMessage{
		ID:     "msg-001",
		Type:   MessageTypeInbound,
		Source: "adapter.wechat",
		Time:   time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC),
		Data: MessageData{
			ChannelID:     "ch-wechat-001",
			PlatformMsgID: "wx-msg-789",
			CorrelationID: "trace-abc",
			PlatformMeta: PlatformMeta{
				PlatformUserID: "openid-123",
				AccountID:      "gh_abc",
				RawType:        "text",
			},
		},
	}
	switch contentType {
	case ContentTypeText:
		msg.Data.Content = MessageContent{Type: ContentTypeText, Text: "Hello"}
	case ContentTypeImage:
		msg.Data.Content = MessageContent{Type: ContentTypeImage, URL: "https://example.com/img.png"}
	case ContentTypeVideo:
		msg.Data.Content = MessageContent{Type: ContentTypeVideo, URL: "https://example.com/vid.mp4"}
	case ContentTypeLink:
		msg.Data.Content = MessageContent{
			Type:  ContentTypeLink,
			URL:   "https://example.com/article",
			Title: "Breaking News",
			Desc:  "Read more about this",
		}
	case ContentTypeEvent:
		msg.Data.Content = MessageContent{Type: ContentTypeEvent, Event: "subscribe"}
	}
	return msg
}

// roundTrip marshals and unmarshals the message, returning the decoded copy.
func roundTrip(t *testing.T, msg *StandardMessage) StandardMessage {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded StandardMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	return decoded
}

// --- Round-trip serialization tests ---

func TestStandardMessage_TextRoundTrip(t *testing.T) {
	msg := newValidMessage(ContentTypeText)
	msg.Subject = "conversation:conv-123"
	msg.Data.ConversationID = "conv-123"
	msg.Data.ProductLineID = "pl-001"
	msg.Data.CustomerID = "cust-456"

	decoded := roundTrip(t, msg)

	if decoded.ID != msg.ID {
		t.Errorf("ID: got %q, want %q", decoded.ID, msg.ID)
	}
	if decoded.Type != msg.Type {
		t.Errorf("Type: got %q, want %q", decoded.Type, msg.Type)
	}
	if decoded.Source != msg.Source {
		t.Errorf("Source: got %q, want %q", decoded.Source, msg.Source)
	}
	if decoded.Subject != msg.Subject {
		t.Errorf("Subject: got %q, want %q", decoded.Subject, msg.Subject)
	}
	if !decoded.Time.Equal(msg.Time) {
		t.Errorf("Time: got %v, want %v", decoded.Time, msg.Time)
	}
	if decoded.Data.ConversationID != msg.Data.ConversationID {
		t.Errorf("ConversationID: got %q, want %q", decoded.Data.ConversationID, msg.Data.ConversationID)
	}
	if decoded.Data.ChannelID != msg.Data.ChannelID {
		t.Errorf("ChannelID: got %q, want %q", decoded.Data.ChannelID, msg.Data.ChannelID)
	}
	if decoded.Data.Content.Type != ContentTypeText {
		t.Errorf("Content.Type: got %q, want %q", decoded.Data.Content.Type, ContentTypeText)
	}
	if decoded.Data.Content.Text != "Hello" {
		t.Errorf("Content.Text: got %q, want %q", decoded.Data.Content.Text, "Hello")
	}
	if decoded.Data.PlatformMeta.PlatformUserID != "openid-123" {
		t.Errorf("PlatformMeta.PlatformUserID: got %q, want %q", decoded.Data.PlatformMeta.PlatformUserID, "openid-123")
	}
	if decoded.Data.CorrelationID != "trace-abc" {
		t.Errorf("CorrelationID: got %q, want %q", decoded.Data.CorrelationID, "trace-abc")
	}
}

func TestStandardMessage_ImageRoundTrip(t *testing.T) {
	msg := newValidMessage(ContentTypeImage)
	decoded := roundTrip(t, msg)

	if decoded.Data.Content.Type != ContentTypeImage {
		t.Errorf("Content.Type: got %q, want %q", decoded.Data.Content.Type, ContentTypeImage)
	}
	if decoded.Data.Content.URL != "https://example.com/img.png" {
		t.Errorf("Content.URL: got %q, want %q", decoded.Data.Content.URL, "https://example.com/img.png")
	}
	if decoded.Data.Content.Text != "" {
		t.Errorf("Content.Text should be empty, got %q", decoded.Data.Content.Text)
	}
}

func TestStandardMessage_VideoRoundTrip(t *testing.T) {
	msg := newValidMessage(ContentTypeVideo)
	decoded := roundTrip(t, msg)

	if decoded.Data.Content.Type != ContentTypeVideo {
		t.Errorf("Content.Type: got %q, want %q", decoded.Data.Content.Type, ContentTypeVideo)
	}
	if decoded.Data.Content.URL != "https://example.com/vid.mp4" {
		t.Errorf("Content.URL: got %q, want %q", decoded.Data.Content.URL, "https://example.com/vid.mp4")
	}
}

func TestStandardMessage_LinkRoundTrip(t *testing.T) {
	msg := newValidMessage(ContentTypeLink)
	decoded := roundTrip(t, msg)

	if decoded.Data.Content.Type != ContentTypeLink {
		t.Errorf("Content.Type: got %q, want %q", decoded.Data.Content.Type, ContentTypeLink)
	}
	if decoded.Data.Content.URL != "https://example.com/article" {
		t.Errorf("Content.URL mismatch")
	}
	if decoded.Data.Content.Title != "Breaking News" {
		t.Errorf("Content.Title: got %q, want %q", decoded.Data.Content.Title, "Breaking News")
	}
	if decoded.Data.Content.Desc != "Read more about this" {
		t.Errorf("Content.Desc mismatch")
	}
}

func TestStandardMessage_EventRoundTrip(t *testing.T) {
	msg := newValidMessage(ContentTypeEvent)
	decoded := roundTrip(t, msg)

	if decoded.Data.Content.Type != ContentTypeEvent {
		t.Errorf("Content.Type: got %q, want %q", decoded.Data.Content.Type, ContentTypeEvent)
	}
	if decoded.Data.Content.Event != "subscribe" {
		t.Errorf("Content.Event: got %q, want %q", decoded.Data.Content.Event, "subscribe")
	}
}

// --- OmitEmpty tests ---

func TestStandardMessage_OmitEmpty(t *testing.T) {
	msg := &StandardMessage{
		ID:   "msg-002",
		Type: MessageTypeOutbound,
		Time: time.Now(),
		Data: MessageData{
			ChannelID: "ch-001",
			Content: MessageContent{
				Type: ContentTypeImage,
				URL:  "https://example.com/image.png",
			},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	raw := string(data)
	// Subject should be omitted when empty
	if strings.Contains(raw, `"subject"`) {
		t.Error("expected 'subject' to be omitted when empty")
	}
	// Text should be omitted when empty
	if containsKey(raw, "text") {
		t.Error("expected 'text' field to be omitted when empty")
	}
	// conversation_id should be omitted when empty
	if strings.Contains(raw, `"conversation_id"`) {
		t.Error("expected 'conversation_id' to be omitted when empty")
	}
}

// --- Validation tests ---

func TestValidateMessage_ValidText(t *testing.T) {
	msg := newValidMessage(ContentTypeText)
	if err := ValidateMessage(msg); err != nil {
		t.Errorf("expected valid message, got error: %v", err)
	}
}

func TestValidateMessage_NilMessage(t *testing.T) {
	err := ValidateMessage(nil)
	if err == nil {
		t.Fatal("expected error for nil message")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("expected nil-related error, got: %v", err)
	}
}

func TestValidateMessage_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*StandardMessage)
		wantErr string
	}{
		{
			name:    "empty ID",
			modify:  func(m *StandardMessage) { m.ID = "" },
			wantErr: "message ID required",
		},
		{
			name:    "empty type",
			modify:  func(m *StandardMessage) { m.Type = "" },
			wantErr: "message type required",
		},
		{
			name:    "empty source",
			modify:  func(m *StandardMessage) { m.Source = "" },
			wantErr: "message source required",
		},
		{
			name:    "zero time",
			modify:  func(m *StandardMessage) { m.Time = time.Time{} },
			wantErr: "message time required",
		},
		{
			name:    "empty channel_id",
			modify:  func(m *StandardMessage) { m.Data.ChannelID = "" },
			wantErr: "channel_id required",
		},
		{
			name:    "empty content type",
			modify:  func(m *StandardMessage) { m.Data.Content.Type = "" },
			wantErr: "content type required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := newValidMessage(ContentTypeText)
			tt.modify(msg)
			err := ValidateMessage(msg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidateMessage_InvalidContentType(t *testing.T) {
	msg := newValidMessage(ContentTypeText)
	msg.Data.Content.Type = "audio"
	err := ValidateMessage(msg)
	if err == nil {
		t.Fatal("expected error for invalid content type")
	}
	if !strings.Contains(err.Error(), "invalid content type: audio") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateMessage_TextWithoutText(t *testing.T) {
	msg := newValidMessage(ContentTypeText)
	msg.Data.Content.Text = ""
	err := ValidateMessage(msg)
	if err == nil {
		t.Fatal("expected error for text type with empty text")
	}
	if !strings.Contains(err.Error(), "text content required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateMessage_ImageWithoutURL(t *testing.T) {
	msg := newValidMessage(ContentTypeImage)
	msg.Data.Content.URL = ""
	err := ValidateMessage(msg)
	if err == nil {
		t.Fatal("expected error for image type without URL")
	}
	if !strings.Contains(err.Error(), "url required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateMessage_VideoWithoutURL(t *testing.T) {
	msg := newValidMessage(ContentTypeVideo)
	msg.Data.Content.URL = ""
	err := ValidateMessage(msg)
	if err == nil {
		t.Fatal("expected error for video type without URL")
	}
}

func TestValidateMessage_LinkMissingFields(t *testing.T) {
	// Missing URL
	msg := newValidMessage(ContentTypeLink)
	msg.Data.Content.URL = ""
	if err := ValidateMessage(msg); err == nil {
		t.Error("expected error for link without URL")
	}

	// Missing Title
	msg2 := newValidMessage(ContentTypeLink)
	msg2.Data.Content.Title = ""
	if err := ValidateMessage(msg2); err == nil {
		t.Error("expected error for link without title")
	}
}

func TestValidateMessage_EventWithoutEventName(t *testing.T) {
	msg := newValidMessage(ContentTypeEvent)
	msg.Data.Content.Event = ""
	err := ValidateMessage(msg)
	if err == nil {
		t.Fatal("expected error for event type without event name")
	}
	if !strings.Contains(err.Error(), "event name required") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- Edge case tests ---

func TestStandardMessage_UnicodeContent(t *testing.T) {
	msg := newValidMessage(ContentTypeText)
	msg.Data.Content.Text = "你好世界 🌍 こんにちは мир"

	decoded := roundTrip(t, msg)
	if decoded.Data.Content.Text != msg.Data.Content.Text {
		t.Errorf("Unicode text mismatch: got %q, want %q", decoded.Data.Content.Text, msg.Data.Content.Text)
	}

	if err := ValidateMessage(msg); err != nil {
		t.Errorf("Unicode text should be valid: %v", err)
	}
}

func TestStandardMessage_LargeContent(t *testing.T) {
	msg := newValidMessage(ContentTypeText)
	// 100KB of text content
	msg.Data.Content.Text = strings.Repeat("A", 100*1024)

	decoded := roundTrip(t, msg)
	if len(decoded.Data.Content.Text) != 100*1024 {
		t.Errorf("large content length mismatch: got %d, want %d", len(decoded.Data.Content.Text), 100*1024)
	}

	if err := ValidateMessage(msg); err != nil {
		t.Errorf("large text should be valid: %v", err)
	}
}

func TestStandardMessage_AllContentTypesValid(t *testing.T) {
	types := []string{ContentTypeText, ContentTypeImage, ContentTypeVideo, ContentTypeLink, ContentTypeEvent}
	for _, ct := range types {
		t.Run(ct, func(t *testing.T) {
			msg := newValidMessage(ct)
			if err := ValidateMessage(msg); err != nil {
				t.Errorf("valid %s message should pass validation: %v", ct, err)
			}
		})
	}
}

func TestStandardMessage_PlatformMetaPreserved(t *testing.T) {
	msg := newValidMessage(ContentTypeText)
	msg.Data.PlatformMeta = PlatformMeta{
		PlatformUserID: "oABCD1234567890",
		AccountID:      "gh_official_001",
		RawType:        "text",
	}

	decoded := roundTrip(t, msg)

	meta := decoded.Data.PlatformMeta
	if meta.PlatformUserID != "oABCD1234567890" {
		t.Errorf("PlatformUserID: got %q, want %q", meta.PlatformUserID, "oABCD1234567890")
	}
	if meta.AccountID != "gh_official_001" {
		t.Errorf("AccountID: got %q, want %q", meta.AccountID, "gh_official_001")
	}
	if meta.RawType != "text" {
		t.Errorf("RawType: got %q, want %q", meta.RawType, "text")
	}
}

// --- Helpers ---

func containsKey(jsonStr, key string) bool {
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return false
	}
	return deepContainsKey(m, key)
}

func deepContainsKey(m map[string]interface{}, key string) bool {
	for k, v := range m {
		if k == key {
			return true
		}
		if sub, ok := v.(map[string]interface{}); ok {
			if deepContainsKey(sub, key) {
				return true
			}
		}
	}
	return false
}

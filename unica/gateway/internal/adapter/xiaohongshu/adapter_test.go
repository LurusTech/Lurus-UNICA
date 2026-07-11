package xiaohongshu

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/model"
)

const (
	testWebhookSecret = "xhs_test_secret"
	testChannelID     = "ch_xhs_001"
	testTimestamp      = "1680000000"
	testNonce          = "test_nonce"
)

func newTestAdapter() *Adapter {
	return NewAdapter(Config{
		AppID:         "xhs_test_appid",
		AppSecret:     "xhs_test_secret_key",
		WebhookSecret: testWebhookSecret,
	}, testChannelID, func(ctx context.Context) (string, error) {
		return "mock_xhs_token", nil
	})
}

// signBody computes the HMAC-SHA256 signature for the given body.
func signBody(secret, timestamp, nonce, body string) string {
	raw := timestamp + nonce + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

// newSignedRequest creates an HTTP request with valid XHS signature headers.
func newSignedRequest(t *testing.T, method, body string) *http.Request {
	t.Helper()
	sig := signBody(testWebhookSecret, testTimestamp, testNonce, body)

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/webhook/xiaohongshu", bodyReader)
	req.Header.Set("X-Signature", sig)
	req.Header.Set("X-Timestamp", testTimestamp)
	req.Header.Set("X-Nonce", testNonce)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// --- VerifyWebhook tests ---

func TestVerifyWebhook_Valid(t *testing.T) {
	a := newTestAdapter()
	body := `{"event":"im.message","msg_id":"m1"}`
	req := newSignedRequest(t, http.MethodPost, body)

	if err := a.VerifyWebhook(context.Background(), req); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestVerifyWebhook_Invalid(t *testing.T) {
	a := newTestAdapter()
	body := `{"event":"im.message"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/xiaohongshu", strings.NewReader(body))
	req.Header.Set("X-Signature", "bad_signature")
	req.Header.Set("X-Timestamp", testTimestamp)
	req.Header.Set("X-Nonce", testNonce)

	if err := a.VerifyWebhook(context.Background(), req); err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

// --- ParseInbound tests ---

func TestParseInbound_Text(t *testing.T) {
	a := newTestAdapter()
	body := `{
		"event": "im.message",
		"from_user_id": "user_xhs_id_xxx",
		"to_user_id": "app_xhs_id_xxx",
		"message_type": "text",
		"content": {"text": "Hello"},
		"msg_id": "msg_xhs_123456",
		"create_time": 1680000000
	}`
	req := newSignedRequest(t, http.MethodPost, body)
	// VerifyWebhook consumes and re-buffers the body
	if err := a.VerifyWebhook(context.Background(), req); err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeText {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeText)
	}
	if msg.Data.Content.Text != "Hello" {
		t.Errorf("content text = %q, want %q", msg.Data.Content.Text, "Hello")
	}
	if msg.Data.PlatformMeta.PlatformUserID != "user_xhs_id_xxx" {
		t.Errorf("platform_user_id = %q, want %q", msg.Data.PlatformMeta.PlatformUserID, "user_xhs_id_xxx")
	}
	if msg.Data.PlatformMsgID != "msg_xhs_123456" {
		t.Errorf("platform_msg_id = %q, want %q", msg.Data.PlatformMsgID, "msg_xhs_123456")
	}
	if msg.Type != model.MessageTypeInbound {
		t.Errorf("type = %q, want %q", msg.Type, model.MessageTypeInbound)
	}
	if msg.Source != "adapter.xiaohongshu" {
		t.Errorf("source = %q, want %q", msg.Source, "adapter.xiaohongshu")
	}
	if msg.Data.ChannelID != testChannelID {
		t.Errorf("channel_id = %q, want %q", msg.Data.ChannelID, testChannelID)
	}
}

func TestParseInbound_Image(t *testing.T) {
	a := newTestAdapter()
	body := `{
		"event": "im.message",
		"from_user_id": "user1",
		"to_user_id": "app1",
		"message_type": "image",
		"content": {"url": "https://example.com/pic.jpg"},
		"msg_id": "msg_img_001",
		"create_time": 1680000000
	}`
	req := newSignedRequest(t, http.MethodPost, body)
	a.VerifyWebhook(context.Background(), req)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeImage {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeImage)
	}
	if msg.Data.Content.URL != "https://example.com/pic.jpg" {
		t.Errorf("url = %q", msg.Data.Content.URL)
	}
}

func TestParseInbound_Video(t *testing.T) {
	a := newTestAdapter()
	body := `{
		"event": "im.message",
		"from_user_id": "user1",
		"to_user_id": "app1",
		"message_type": "video",
		"content": {"url": "https://example.com/video.mp4"},
		"msg_id": "msg_vid_001",
		"create_time": 1680000000
	}`
	req := newSignedRequest(t, http.MethodPost, body)
	a.VerifyWebhook(context.Background(), req)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeVideo {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeVideo)
	}
	if msg.Data.Content.URL != "https://example.com/video.mp4" {
		t.Errorf("url = %q", msg.Data.Content.URL)
	}
}

func TestParseInbound_NoteLink(t *testing.T) {
	a := newTestAdapter()
	body := `{
		"event": "im.message",
		"from_user_id": "user1",
		"to_user_id": "app1",
		"message_type": "note_link",
		"content": {"note_id": "note_abc123", "title": "My Note Title", "cover_url": "https://cover.jpg"},
		"msg_id": "msg_note_001",
		"create_time": 1680000000
	}`
	req := newSignedRequest(t, http.MethodPost, body)
	a.VerifyWebhook(context.Background(), req)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeLink {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeLink)
	}
	if msg.Data.Content.URL != "https://www.xiaohongshu.com/explore/note_abc123" {
		t.Errorf("url = %q", msg.Data.Content.URL)
	}
	if msg.Data.Content.Title != "My Note Title" {
		t.Errorf("title = %q", msg.Data.Content.Title)
	}
	if msg.Data.PlatformMeta.RawType != "note_link" {
		t.Errorf("raw_type = %q, want %q", msg.Data.PlatformMeta.RawType, "note_link")
	}
}

func TestParseInbound_UnsupportedType(t *testing.T) {
	a := newTestAdapter()
	body := `{
		"event": "im.message",
		"from_user_id": "user1",
		"to_user_id": "app1",
		"message_type": "sticker",
		"content": {},
		"msg_id": "msg_stk_001",
		"create_time": 1680000000
	}`
	req := newSignedRequest(t, http.MethodPost, body)
	a.VerifyWebhook(context.Background(), req)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeText {
		t.Errorf("content type = %q, want text", msg.Data.Content.Type)
	}
	if !strings.Contains(msg.Data.Content.Text, "unsupported") {
		t.Errorf("text = %q, want to contain 'unsupported'", msg.Data.Content.Text)
	}
}

func TestParseInbound_InvalidJSON(t *testing.T) {
	a := newTestAdapter()
	req := httptest.NewRequest(http.MethodPost, "/webhook/xiaohongshu", strings.NewReader("not json"))
	_, err := a.ParseInbound(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// --- FormatOutbound tests ---

func TestFormatOutbound_Text(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-001",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "Reply text",
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "target_user",
			},
		},
	}

	payload, err := a.FormatOutbound(context.Background(), msg)
	if err != nil {
		t.Fatalf("FormatOutbound: %v", err)
	}

	var out OutboundPayload
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ToUserID != "target_user" {
		t.Errorf("to_user_id = %q", out.ToUserID)
	}
	if out.MsgType != "text" {
		t.Errorf("msg_type = %q", out.MsgType)
	}
	if out.Content["text"] != "Reply text" {
		t.Errorf("content.text = %v", out.Content["text"])
	}
}

func TestFormatOutbound_Image(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-002",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeImage,
				URL:  "https://example.com/img.jpg",
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "target_user",
			},
		},
	}

	payload, err := a.FormatOutbound(context.Background(), msg)
	if err != nil {
		t.Fatalf("FormatOutbound: %v", err)
	}

	var out OutboundPayload
	json.Unmarshal(payload, &out)
	if out.MsgType != "image" {
		t.Errorf("msg_type = %q", out.MsgType)
	}
	if out.Content["url"] != "https://example.com/img.jpg" {
		t.Errorf("content.url = %v", out.Content["url"])
	}
}

func TestFormatOutbound_Video(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-003",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeVideo,
				URL:  "https://example.com/vid.mp4",
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "target_user",
			},
		},
	}

	payload, err := a.FormatOutbound(context.Background(), msg)
	if err != nil {
		t.Fatalf("FormatOutbound: %v", err)
	}

	var out OutboundPayload
	json.Unmarshal(payload, &out)
	if out.MsgType != "video" {
		t.Errorf("msg_type = %q", out.MsgType)
	}
}

func TestFormatOutbound_MissingPlatformUserID(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-004",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "Hi",
			},
		},
	}

	_, err := a.FormatOutbound(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for missing platform_user_id")
	}
	if !strings.Contains(err.Error(), "platform_user_id") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestFormatOutbound_FallbackUnsupported(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-005",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeLink,
				URL:  "https://example.com",
				Title: "Link",
				Desc: "A link description",
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "user",
			},
		},
	}

	payload, err := a.FormatOutbound(context.Background(), msg)
	if err != nil {
		t.Fatalf("FormatOutbound: %v", err)
	}

	var out OutboundPayload
	json.Unmarshal(payload, &out)
	if out.MsgType != "text" {
		t.Errorf("expected fallback to text, got %q", out.MsgType)
	}
}

func TestFormatOutbound_FallbackNoTextNoDesc(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-006",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeEvent,
				Event: "subscribe",
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "user",
			},
		},
	}

	payload, err := a.FormatOutbound(context.Background(), msg)
	if err != nil {
		t.Fatalf("FormatOutbound: %v", err)
	}

	var out OutboundPayload
	json.Unmarshal(payload, &out)
	if out.Content["text"] != "[unsupported message type]" {
		t.Errorf("content.text = %v", out.Content["text"])
	}
}

// --- SendMessage tests ---

func TestSendMessage_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Authorization header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer mock_xhs_token" {
			t.Errorf("Authorization = %q, want Bearer mock_xhs_token", auth)
		}
		// Verify Content-Type
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		// Read body
		body, _ := io.ReadAll(r.Body)
		var payload OutboundPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	defer server.Close()

	a := newTestAdapter()
	a.sendURL = server.URL
	a.httpClient = server.Client()

	payload, _ := json.Marshal(OutboundPayload{
		ToUserID: "user1",
		MsgType:  "text",
		Content:  map[string]interface{}{"text": "hello"},
	})

	err := a.SendMessage(context.Background(), testChannelID, payload)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

func TestSendMessage_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":40001,"msg":"invalid token"}`))
	}))
	defer server.Close()

	a := newTestAdapter()
	a.sendURL = server.URL
	a.httpClient = server.Client()

	err := a.SendMessage(context.Background(), testChannelID, []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for API error response")
	}
	if !strings.Contains(err.Error(), "40001") {
		t.Errorf("error = %q, want to contain '40001'", err.Error())
	}
}

func TestSendMessage_TokenError(t *testing.T) {
	a := &Adapter{
		cfg:        Config{WebhookSecret: "s"},
		channelID:  testChannelID,
		httpClient: &http.Client{},
		getToken: func(ctx context.Context) (string, error) {
			return "", fmt.Errorf("token refresh failed")
		},
		sendURL: "https://open.xiaohongshu.com/api/message/send",
	}

	err := a.SendMessage(context.Background(), testChannelID, []byte(`{}`))
	if err == nil {
		t.Fatal("expected error when token getter fails")
	}
	if !strings.Contains(err.Error(), "get token") {
		t.Errorf("error = %q", err.Error())
	}
}

// --- IsChallenge tests ---

func TestIsChallenge_Valid(t *testing.T) {
	body := []byte(`{"event":"challenge","challenge":"test_challenge_token"}`)
	challenge, ok := IsChallenge(body)
	if !ok {
		t.Fatal("expected challenge to be detected")
	}
	if challenge != "test_challenge_token" {
		t.Errorf("challenge = %q, want %q", challenge, "test_challenge_token")
	}
}

func TestIsChallenge_NotChallenge(t *testing.T) {
	body := []byte(`{"event":"im.message","msg_id":"m1"}`)
	_, ok := IsChallenge(body)
	if ok {
		t.Fatal("expected non-challenge event to return false")
	}
}

func TestIsChallenge_InvalidJSON(t *testing.T) {
	body := []byte(`not json`)
	_, ok := IsChallenge(body)
	if ok {
		t.Fatal("expected invalid JSON to return false")
	}
}

func TestIsChallenge_EmptyChallenge(t *testing.T) {
	body := []byte(`{"event":"challenge","challenge":""}`)
	_, ok := IsChallenge(body)
	if ok {
		t.Fatal("expected empty challenge to return false")
	}
}

// --- parseContent tests ---

func TestParseContent_AllTypes(t *testing.T) {
	tests := []struct {
		name        string
		msgType     string
		content     string
		wantType    string
		wantText    string
		wantURL     string
	}{
		{
			name:     "text",
			msgType:  "text",
			content:  `{"text":"Hello World"}`,
			wantType: model.ContentTypeText,
			wantText: "Hello World",
		},
		{
			name:     "image",
			msgType:  "image",
			content:  `{"url":"https://img.xhs.com/pic.jpg"}`,
			wantType: model.ContentTypeImage,
			wantURL:  "https://img.xhs.com/pic.jpg",
		},
		{
			name:     "video",
			msgType:  "video",
			content:  `{"url":"https://vid.xhs.com/vid.mp4"}`,
			wantType: model.ContentTypeVideo,
			wantURL:  "https://vid.xhs.com/vid.mp4",
		},
		{
			name:     "note_link",
			msgType:  "note_link",
			content:  `{"note_id":"n123","title":"My Note","cover_url":"https://cover.jpg"}`,
			wantType: model.ContentTypeLink,
			wantURL:  "https://www.xiaohongshu.com/explore/n123",
		},
		{
			name:     "unknown",
			msgType:  "location",
			content:  `{}`,
			wantType: model.ContentTypeText,
			wantText: "[unsupported: location]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &InboundMsg{
				MessageType: tt.msgType,
				Content:     json.RawMessage(tt.content),
			}
			c := parseContent(msg)
			if c.Type != tt.wantType {
				t.Errorf("type = %q, want %q", c.Type, tt.wantType)
			}
			if tt.wantText != "" && c.Text != tt.wantText {
				t.Errorf("text = %q, want %q", c.Text, tt.wantText)
			}
			if tt.wantURL != "" && c.URL != tt.wantURL {
				t.Errorf("url = %q, want %q", c.URL, tt.wantURL)
			}
		})
	}
}

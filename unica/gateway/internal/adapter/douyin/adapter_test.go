package douyin

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

// newSignedRequest creates an HTTP request with valid Douyin HMAC-SHA256 signature headers.
func newSignedRequest(t *testing.T, method, secret, body string) *http.Request {
	t.Helper()
	timestamp := "1680000000"
	nonce := "testnonce"

	raw := timestamp + nonce + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(raw))
	signature := hex.EncodeToString(mac.Sum(nil))

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/webhook/douyin", bodyReader)
	req.Header.Set("X-Douyin-Signature", signature)
	req.Header.Set("X-Douyin-Timestamp", timestamp)
	req.Header.Set("X-Douyin-Nonce", nonce)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func newTestAdapter() *Adapter {
	return NewAdapter(Config{
		ClientKey:     "test_client_key",
		ClientSecret:  "test_client_secret",
		WebhookSecret: "test_webhook_secret",
	}, "ch_douyin_001", func(ctx context.Context) (string, error) {
		return "mock_access_token", nil
	})
}

// --- VerifyWebhook tests ---

func TestVerifyWebhook_Valid(t *testing.T) {
	a := newTestAdapter()
	body := `{"event":"im","from_user_id":"user1"}`
	req := newSignedRequest(t, http.MethodPost, "test_webhook_secret", body)
	if err := a.VerifyWebhook(context.Background(), req); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestVerifyWebhook_Invalid(t *testing.T) {
	a := newTestAdapter()
	req := httptest.NewRequest(http.MethodPost, "/webhook/douyin", strings.NewReader("body"))
	req.Header.Set("X-Douyin-Signature", "bad_signature")
	req.Header.Set("X-Douyin-Timestamp", "123")
	req.Header.Set("X-Douyin-Nonce", "abc")
	if err := a.VerifyWebhook(context.Background(), req); err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

func TestVerifyWebhook_BodyRestoredAfterVerification(t *testing.T) {
	a := newTestAdapter()
	body := `{"event":"im","from_user_id":"user1"}`
	req := newSignedRequest(t, http.MethodPost, "test_webhook_secret", body)

	if err := a.VerifyWebhook(context.Background(), req); err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}

	// Body should still be readable after verification
	restoredBody, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(restoredBody) != body {
		t.Errorf("restored body = %q, want %q", string(restoredBody), body)
	}
}

// --- ParseInbound tests ---

func makeInboundJSON(event, fromUser, toUser, msgType, content, msgID string, createTime int64) string {
	msg := InboundMsg{
		Event:       event,
		FromUserID:  fromUser,
		ToUserID:    toUser,
		MessageType: msgType,
		Content:     content,
		MsgID:       msgID,
		CreateTime:  createTime,
	}
	data, _ := json.Marshal(msg)
	return string(data)
}

func TestParseInbound_Text(t *testing.T) {
	a := newTestAdapter()
	body := makeInboundJSON("im", "user_open_id_xxx", "app_user_id_xxx", "text", `{"text":"Hello"}`, "msg_123456", 1680000000)
	req := newSignedRequest(t, http.MethodPost, "test_webhook_secret", body)

	// Verify first (as handler would do)
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
	if msg.Data.PlatformMeta.PlatformUserID != "user_open_id_xxx" {
		t.Errorf("platform_user_id = %q, want %q", msg.Data.PlatformMeta.PlatformUserID, "user_open_id_xxx")
	}
	if msg.Data.PlatformMsgID != "msg_123456" {
		t.Errorf("platform_msg_id = %q, want %q", msg.Data.PlatformMsgID, "msg_123456")
	}
	if msg.Type != model.MessageTypeInbound {
		t.Errorf("type = %q, want %q", msg.Type, model.MessageTypeInbound)
	}
	if msg.Source != "adapter.douyin" {
		t.Errorf("source = %q, want %q", msg.Source, "adapter.douyin")
	}
}

func TestParseInbound_Image(t *testing.T) {
	a := newTestAdapter()
	body := makeInboundJSON("im", "user1", "app1", "image", `{"url":"https://example.com/pic.jpg"}`, "msg_img_001", 1680000000)
	req := newSignedRequest(t, http.MethodPost, "test_webhook_secret", body)
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
	body := makeInboundJSON("im", "user1", "app1", "video", `{"url":"https://example.com/video.mp4"}`, "msg_vid_001", 1680000000)
	req := newSignedRequest(t, http.MethodPost, "test_webhook_secret", body)
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

func TestParseInbound_Card(t *testing.T) {
	a := newTestAdapter()
	body := makeInboundJSON("im", "user1", "app1", "card", `{"title":"My Card","desc":"Card description","url":"https://example.com"}`, "msg_card_001", 1680000000)
	req := newSignedRequest(t, http.MethodPost, "test_webhook_secret", body)
	a.VerifyWebhook(context.Background(), req)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeLink {
		t.Errorf("content type = %q, want %q", msg.Data.Content.Type, model.ContentTypeLink)
	}
	if msg.Data.Content.URL != "https://example.com" {
		t.Errorf("url = %q", msg.Data.Content.URL)
	}
	if msg.Data.Content.Title != "My Card" {
		t.Errorf("title = %q", msg.Data.Content.Title)
	}
	if msg.Data.Content.Desc != "Card description" {
		t.Errorf("desc = %q", msg.Data.Content.Desc)
	}
}

func TestParseInbound_UnsupportedType(t *testing.T) {
	a := newTestAdapter()
	body := makeInboundJSON("im", "user1", "app1", "location", `{}`, "msg_loc_001", 1680000000)
	req := newSignedRequest(t, http.MethodPost, "test_webhook_secret", body)
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

func TestParseInbound_EmptyMsgID(t *testing.T) {
	a := newTestAdapter()
	body := makeInboundJSON("im", "user1", "app1", "text", `{"text":"Hi"}`, "", 1680000000)
	req := newSignedRequest(t, http.MethodPost, "test_webhook_secret", body)
	a.VerifyWebhook(context.Background(), req)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.PlatformMsgID != "user1:1680000000" {
		t.Errorf("platform_msg_id = %q, want %q", msg.Data.PlatformMsgID, "user1:1680000000")
	}
}

func TestParseInbound_InvalidJSON(t *testing.T) {
	a := newTestAdapter()
	req := httptest.NewRequest(http.MethodPost, "/webhook/douyin", strings.NewReader("not json"))
	_, err := a.ParseInbound(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseInbound_TextFallbackOnBadContent(t *testing.T) {
	a := newTestAdapter()
	// Content is not valid JSON - should fallback to using raw Content string
	body := makeInboundJSON("im", "user1", "app1", "text", "plain text not json", "msg_001", 1680000000)
	req := newSignedRequest(t, http.MethodPost, "test_webhook_secret", body)
	a.VerifyWebhook(context.Background(), req)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.Content.Type != model.ContentTypeText {
		t.Errorf("content type = %q, want text", msg.Data.Content.Type)
	}
	if msg.Data.Content.Text != "plain text not json" {
		t.Errorf("text = %q, want %q", msg.Data.Content.Text, "plain text not json")
	}
}

func TestParseInbound_AllFieldsPreserved(t *testing.T) {
	a := newTestAdapter()
	body := makeInboundJSON("im", "from_user", "to_account", "text", `{"text":"test msg"}`, "msg_55555", 1680000000)
	req := newSignedRequest(t, http.MethodPost, "test_webhook_secret", body)
	a.VerifyWebhook(context.Background(), req)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.PlatformMeta.AccountID != "to_account" {
		t.Errorf("account_id = %q, want %q", msg.Data.PlatformMeta.AccountID, "to_account")
	}
	if msg.Data.PlatformMeta.RawType != "text" {
		t.Errorf("raw_type = %q, want %q", msg.Data.PlatformMeta.RawType, "text")
	}
	if msg.Data.ChannelID != "ch_douyin_001" {
		t.Errorf("channel_id = %q", msg.Data.ChannelID)
	}
	if msg.Source != "adapter.douyin" {
		t.Errorf("source = %q", msg.Source)
	}
	if msg.ID == "" {
		t.Error("message ID should be non-empty UUID")
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

	var outMsg OutboundMsg
	if err := json.Unmarshal(payload, &outMsg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if outMsg.ToUserID != "target_user" {
		t.Errorf("to_user_id = %q", outMsg.ToUserID)
	}
	if outMsg.MsgType != "text" {
		t.Errorf("msg_type = %q", outMsg.MsgType)
	}

	// Verify nested JSON content
	var tc TextContent
	if err := json.Unmarshal([]byte(outMsg.Content), &tc); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if tc.Text != "Reply text" {
		t.Errorf("content text = %q, want %q", tc.Text, "Reply text")
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
				URL:  "https://example.com/pic.jpg",
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

	var outMsg OutboundMsg
	json.Unmarshal(payload, &outMsg)
	if outMsg.MsgType != "image" {
		t.Errorf("msg_type = %q", outMsg.MsgType)
	}

	var ic ImageContent
	json.Unmarshal([]byte(outMsg.Content), &ic)
	if ic.URL != "https://example.com/pic.jpg" {
		t.Errorf("url = %q", ic.URL)
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
				URL:  "https://example.com/video.mp4",
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

	var outMsg OutboundMsg
	json.Unmarshal(payload, &outMsg)
	if outMsg.MsgType != "video" {
		t.Errorf("msg_type = %q", outMsg.MsgType)
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

func TestFormatOutbound_FallbackUnsupportedType(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-005",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeLink,
				URL:  "https://example.com",
				Desc: "A link",
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

	var outMsg OutboundMsg
	json.Unmarshal(payload, &outMsg)
	if outMsg.MsgType != "text" {
		t.Errorf("expected fallback to text, got %q", outMsg.MsgType)
	}

	var tc TextContent
	json.Unmarshal([]byte(outMsg.Content), &tc)
	if tc.Text != "A link" {
		t.Errorf("fallback text = %q, want %q", tc.Text, "A link")
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

	var outMsg OutboundMsg
	json.Unmarshal(payload, &outMsg)

	var tc TextContent
	json.Unmarshal([]byte(outMsg.Content), &tc)
	if tc.Text != "[unsupported message type]" {
		t.Errorf("text = %q", tc.Text)
	}
}

// --- SendMessage tests ---

// roundTripFunc is an adapter to use ordinary functions as http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSendMessage_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Authorization header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer mock_access_token" {
			t.Errorf("Authorization = %q, want 'Bearer mock_access_token'", auth)
		}
		// Verify Content-Type
		ct := r.Header.Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"err_code":0,"err_msg":"ok"}`))
	}))
	defer server.Close()

	a := newTestAdapter()
	a.sendURL = server.URL

	payload, _ := json.Marshal(OutboundMsg{
		ToUserID: "user1",
		MsgType:  "text",
		Content:  `{"text":"hello"}`,
	})

	err := a.SendMessage(context.Background(), "ch_douyin_001", payload)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

func TestSendMessage_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"err_code":40001,"err_msg":"invalid credential"}`))
	}))
	defer server.Close()

	a := newTestAdapter()
	a.sendURL = server.URL

	err := a.SendMessage(context.Background(), "ch_douyin_001", []byte(`{}`))
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
		channelID:  "ch1",
		httpClient: &http.Client{},
		getToken: func(ctx context.Context) (string, error) {
			return "", fmt.Errorf("token refresh failed")
		},
		sendURL: "https://open.douyin.com/api/im/send/msg/",
	}

	err := a.SendMessage(context.Background(), "ch1", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error when token getter fails")
	}
	if !strings.Contains(err.Error(), "get token") {
		t.Errorf("error = %q", err.Error())
	}
}

// --- IsChallenge tests ---

func TestIsChallenge_Valid(t *testing.T) {
	body := []byte(`{"challenge":"challenge_string_abc"}`)
	challenge, ok := IsChallenge(body)
	if !ok {
		t.Fatal("expected IsChallenge to return true")
	}
	if challenge != "challenge_string_abc" {
		t.Errorf("challenge = %q, want %q", challenge, "challenge_string_abc")
	}
}

func TestIsChallenge_NotChallenge(t *testing.T) {
	body := []byte(`{"event":"im","from_user_id":"user1"}`)
	_, ok := IsChallenge(body)
	if ok {
		t.Fatal("expected IsChallenge to return false for non-challenge")
	}
}

func TestIsChallenge_InvalidJSON(t *testing.T) {
	body := []byte(`not json`)
	_, ok := IsChallenge(body)
	if ok {
		t.Fatal("expected IsChallenge to return false for invalid JSON")
	}
}

func TestIsChallenge_EmptyChallenge(t *testing.T) {
	body := []byte(`{"challenge":""}`)
	_, ok := IsChallenge(body)
	if ok {
		t.Fatal("expected IsChallenge to return false for empty challenge")
	}
}

// --- parseContent tests ---

func TestParseContent_TextBadJSON(t *testing.T) {
	msg := &InboundMsg{MessageType: "text", Content: "not json"}
	c := parseContent(msg)
	if c.Type != model.ContentTypeText {
		t.Errorf("type = %q, want text", c.Type)
	}
	// Fallback: raw content string
	if c.Text != "not json" {
		t.Errorf("text = %q, want %q", c.Text, "not json")
	}
}

func TestParseContent_ImageBadJSON(t *testing.T) {
	msg := &InboundMsg{MessageType: "image", Content: "not json"}
	c := parseContent(msg)
	if c.Type != model.ContentTypeImage {
		t.Errorf("type = %q, want image", c.Type)
	}
	// URL should be empty on parse failure
	if c.URL != "" {
		t.Errorf("url = %q, want empty", c.URL)
	}
}

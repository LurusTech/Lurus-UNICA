package taobao

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/model"
)

// newTestAdapter creates a test Taobao adapter with mock config.
func newTestAdapter() *Adapter {
	return NewAdapter(Config{
		AppKey:    "test_app_key",
		AppSecret: "test_app_secret",
	}, "ch_taobao_001", func(ctx context.Context) (string, error) {
		return "mock_access_token", nil
	})
}

// newSignedRequest creates an HTTP request with valid Taobao HMAC-MD5 signature query param.
func newSignedRequest(t *testing.T, secret, body string) *http.Request {
	t.Helper()

	params := map[string]string{
		"method":    "taobao.im.message",
		"app_key":   "test_app_key",
		"timestamp": "2026-03-06 10:00:00",
	}
	if body != "" {
		params["body"] = body
	}
	sig := ComputeSignature(secret, params)

	q := url.Values{}
	for k, v := range params {
		if k != "body" {
			q.Set(k, v)
		}
	}
	q.Set("sign", sig)

	reqURL := "/webhook/taobao?" + q.Encode()

	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}

	req := httptest.NewRequest(http.MethodPost, reqURL, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// --- VerifyWebhook tests ---

func TestVerifyWebhook_Valid(t *testing.T) {
	a := newTestAdapter()
	body := `{"msg_type":"text","from_user":"buyer1"}`
	req := newSignedRequest(t, "test_app_secret", body)
	if err := a.VerifyWebhook(context.Background(), req); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestVerifyWebhook_Invalid(t *testing.T) {
	a := newTestAdapter()
	req := httptest.NewRequest(http.MethodPost, "/webhook/taobao?sign=bad_signature", strings.NewReader("body"))
	if err := a.VerifyWebhook(context.Background(), req); err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

func TestVerifyWebhook_BodyRestoredAfterVerification(t *testing.T) {
	a := newTestAdapter()
	body := `{"msg_type":"text","from_user":"buyer1"}`
	req := newSignedRequest(t, "test_app_secret", body)

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

func makeInboundJSON(msgType, fromUser, toUser, content, msgID string, createTime int64) string {
	msg := InboundMsg{
		MsgType:    msgType,
		Content:    content,
		FromUser:   fromUser,
		ToUser:     toUser,
		MsgID:      msgID,
		CreateTime: createTime,
	}
	data, _ := json.Marshal(msg)
	return string(data)
}

func TestParseInbound_Text(t *testing.T) {
	a := newTestAdapter()
	body := makeInboundJSON("text", "buyer_nick_xxx", "seller_nick_xxx", `{"text":"Hello"}`, "msg_123456", 1680000000)
	req := newSignedRequest(t, "test_app_secret", body)

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
	if msg.Data.PlatformMeta.PlatformUserID != "buyer_nick_xxx" {
		t.Errorf("platform_user_id = %q, want %q", msg.Data.PlatformMeta.PlatformUserID, "buyer_nick_xxx")
	}
	if msg.Data.PlatformMsgID != "msg_123456" {
		t.Errorf("platform_msg_id = %q, want %q", msg.Data.PlatformMsgID, "msg_123456")
	}
	if msg.Type != model.MessageTypeInbound {
		t.Errorf("type = %q, want %q", msg.Type, model.MessageTypeInbound)
	}
	if msg.Source != "adapter.taobao" {
		t.Errorf("source = %q, want %q", msg.Source, "adapter.taobao")
	}
}

func TestParseInbound_Image(t *testing.T) {
	a := newTestAdapter()
	body := makeInboundJSON("image", "buyer1", "seller1", `{"url":"https://example.com/pic.jpg"}`, "msg_img_001", 1680000000)
	req := newSignedRequest(t, "test_app_secret", body)
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

func TestParseInbound_UnsupportedType(t *testing.T) {
	a := newTestAdapter()
	body := makeInboundJSON("trade_event", "buyer1", "seller1", `{}`, "msg_trade_001", 1680000000)
	req := newSignedRequest(t, "test_app_secret", body)
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
	body := makeInboundJSON("text", "buyer1", "seller1", `{"text":"Hi"}`, "", 1680000000)
	req := newSignedRequest(t, "test_app_secret", body)
	a.VerifyWebhook(context.Background(), req)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.PlatformMsgID != "buyer1:1680000000" {
		t.Errorf("platform_msg_id = %q, want %q", msg.Data.PlatformMsgID, "buyer1:1680000000")
	}
}

func TestParseInbound_InvalidJSON(t *testing.T) {
	a := newTestAdapter()
	req := httptest.NewRequest(http.MethodPost, "/webhook/taobao", strings.NewReader("not json"))
	_, err := a.ParseInbound(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseInbound_TextFallbackOnBadContent(t *testing.T) {
	a := newTestAdapter()
	body := makeInboundJSON("text", "buyer1", "seller1", "plain text not json", "msg_001", 1680000000)
	req := newSignedRequest(t, "test_app_secret", body)
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
	body := makeInboundJSON("text", "from_buyer", "to_seller", `{"text":"test msg"}`, "msg_55555", 1680000000)
	req := newSignedRequest(t, "test_app_secret", body)
	a.VerifyWebhook(context.Background(), req)

	msg, err := a.ParseInbound(context.Background(), req)
	if err != nil {
		t.Fatalf("ParseInbound: %v", err)
	}
	if msg.Data.PlatformMeta.AccountID != "to_seller" {
		t.Errorf("account_id = %q, want %q", msg.Data.PlatformMeta.AccountID, "to_seller")
	}
	if msg.Data.PlatformMeta.RawType != "text" {
		t.Errorf("raw_type = %q, want %q", msg.Data.PlatformMeta.RawType, "text")
	}
	if msg.Data.ChannelID != "ch_taobao_001" {
		t.Errorf("channel_id = %q", msg.Data.ChannelID)
	}
	if msg.Source != "adapter.taobao" {
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
				PlatformUserID: "target_buyer",
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
	if outMsg.ToUser != "target_buyer" {
		t.Errorf("to_user = %q", outMsg.ToUser)
	}
	if outMsg.MsgType != "text" {
		t.Errorf("msg_type = %q", outMsg.MsgType)
	}

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
				PlatformUserID: "target_buyer",
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

func TestFormatOutbound_MissingPlatformUserID(t *testing.T) {
	a := newTestAdapter()
	msg := &model.StandardMessage{
		ID:   "msg-003",
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
		ID:   "msg-004",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeLink,
				URL:  "https://example.com",
				Desc: "A link",
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "buyer",
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
		ID:   "msg-005",
		Type: model.MessageTypeOutbound,
		Data: model.MessageData{
			Content: model.MessageContent{
				Type: model.ContentTypeEvent,
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: "buyer",
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

func TestSendMessage_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer mock_access_token" {
			t.Errorf("Authorization = %q, want 'Bearer mock_access_token'", auth)
		}
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
		ToUser:  "buyer1",
		MsgType: "text",
		Content: `{"text":"hello"}`,
	})

	err := a.SendMessage(context.Background(), "ch_taobao_001", payload)
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

	err := a.SendMessage(context.Background(), "ch_taobao_001", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for API error response")
	}
	if !strings.Contains(err.Error(), "40001") {
		t.Errorf("error = %q, want to contain '40001'", err.Error())
	}
}

func TestSendMessage_TokenError(t *testing.T) {
	a := &Adapter{
		cfg:        Config{AppSecret: "s"},
		channelID:  "ch1",
		httpClient: &http.Client{},
		getToken: func(ctx context.Context) (string, error) {
			return "", fmt.Errorf("token refresh failed")
		},
		sendURL: "https://eco.taobao.com/router/rest",
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
	body := []byte(`{"msg_type":"text","from_user":"buyer1"}`)
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
	msg := &InboundMsg{MsgType: "text", Content: "not json"}
	c := parseContent(msg)
	if c.Type != model.ContentTypeText {
		t.Errorf("type = %q, want text", c.Type)
	}
	if c.Text != "not json" {
		t.Errorf("text = %q, want %q", c.Text, "not json")
	}
}

func TestParseContent_ImageBadJSON(t *testing.T) {
	msg := &InboundMsg{MsgType: "image", Content: "not json"}
	c := parseContent(msg)
	if c.Type != model.ContentTypeImage {
		t.Errorf("type = %q, want image", c.Type)
	}
	if c.URL != "" {
		t.Errorf("url = %q, want empty", c.URL)
	}
}

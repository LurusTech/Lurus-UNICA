package wechat

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/kefu/unica/pkg/model"
)

// mockProducer implements a minimal stream.Producer-like publish for testing.
// Since WebhookHandler depends on *stream.Producer which requires Redis,
// we test at the HTTP level using a real handler with a nil producer to
// verify routing and signature logic. For full integration, see handler
// integration tests.

// buildSignedURL returns a URL path with valid WeChat signature query params.
func buildSignedURL(token, echostr string) string {
	timestamp := "1709625600"
	nonce := "handler_nonce"

	params := []string{token, timestamp, nonce}
	sort.Strings(params)
	hash := sha1.Sum([]byte(strings.Join(params, "")))
	signature := fmt.Sprintf("%x", hash)

	url := fmt.Sprintf("/webhook/wechat?signature=%s&timestamp=%s&nonce=%s", signature, timestamp, nonce)
	if echostr != "" {
		url += "&echostr=" + echostr
	}
	return url
}

// newHandlerTestSetup creates an adapter and a mock stream producer for handler tests.
// We use a channel to capture published messages.
func newHandlerTestSetup() (*Adapter, *mockStreamProducer) {
	a := NewAdapter(Config{
		AppID:         "wx_handler_test",
		Token:         "handlertoken",
		EncryptedMode: false,
	}, "ch_handler_test", func(ctx context.Context) (string, error) {
		return "token", nil
	})

	return a, &mockStreamProducer{}
}

// mockStreamProducer captures published messages for verification.
type mockStreamProducer struct {
	published []*model.StandardMessage
	err       error
}

func (m *mockStreamProducer) Publish(ctx context.Context, msg *model.StandardMessage) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	m.published = append(m.published, msg)
	return "stream-id-001", nil
}

// Since WebhookHandler uses *stream.Producer directly, we cannot inject a mock easily.
// Instead, we test the HTTP handler behavior by creating a minimal test that exercises
// the adapter verification and message parsing logic through the ServeHTTP method.
// For the POST handler tests, we need to use the actual stream.Producer or skip the
// publish step. We'll test what we can at the handler level.

func TestHandler_GET_ValidSignature(t *testing.T) {
	a, _ := newHandlerTestSetup()

	// We need a real WebhookHandler; since it requires *stream.Producer (not interface),
	// we pass nil and only test GET (which doesn't use the producer).
	h := NewWebhookHandler(a, nil)

	url := buildSignedURL("handlertoken", "echo_challenge_str")
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "echo_challenge_str" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "echo_challenge_str")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

func TestHandler_GET_InvalidSignature(t *testing.T) {
	a, _ := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodGet, "/webhook/wechat?signature=bad&timestamp=1&nonce=2&echostr=test", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandler_POST_InvalidSignature(t *testing.T) {
	a, _ := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	body := `<xml><ToUserName>gh</ToUserName><FromUserName>u</FromUserName><CreateTime>1</CreateTime><MsgType>text</MsgType><Content>hi</Content><MsgId>1</MsgId></xml>`
	req := httptest.NewRequest(http.MethodPost, "/webhook/wechat?signature=bad&timestamp=1&nonce=2", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	a, _ := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodPut, "/webhook/wechat", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// TestHandler_POST_UnparseableBody tests that the handler returns 200 "success"
// for unparseable messages (to prevent WeChat from retrying).
// This test exercises the parse-error path. Since the producer is nil,
// we need to verify the handler reaches the parse step (valid signature, bad body).
func TestHandler_POST_UnparseableBody(t *testing.T) {
	a, _ := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	url := buildSignedURL("handlertoken", "")
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader("<<<not xml"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// Should return 200 with "success" to prevent WeChat retries
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "success" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "success")
	}
}

// TestCustomerServiceMsgJSON verifies JSON marshaling of outbound message types.
func TestCustomerServiceMsgJSON(t *testing.T) {
	msg := CustomerServiceMsg{
		ToUser:  "oUser123",
		MsgType: "text",
		Text:    &TextMsg{Content: "hello"},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	if parsed["touser"] != "oUser123" {
		t.Errorf("touser = %v", parsed["touser"])
	}
	if parsed["msgtype"] != "text" {
		t.Errorf("msgtype = %v", parsed["msgtype"])
	}
	textObj, ok := parsed["text"].(map[string]interface{})
	if !ok {
		t.Fatalf("text is not object: %T", parsed["text"])
	}
	if textObj["content"] != "hello" {
		t.Errorf("text.content = %v", textObj["content"])
	}

	// Omit empty fields
	if _, exists := parsed["image"]; exists {
		t.Error("image should be omitted when nil")
	}
	if _, exists := parsed["voice"]; exists {
		t.Error("voice should be omitted when nil")
	}
}

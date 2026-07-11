package douyin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// buildSignedDouyinRequest creates a POST request with valid Douyin signature headers.
func buildSignedDouyinRequest(t *testing.T, secret, body string) *http.Request {
	t.Helper()
	timestamp := "1680000000"
	nonce := "handler_nonce"

	raw := timestamp + nonce + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(raw))
	signature := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhook/douyin", strings.NewReader(body))
	req.Header.Set("X-Douyin-Signature", signature)
	req.Header.Set("X-Douyin-Timestamp", timestamp)
	req.Header.Set("X-Douyin-Nonce", nonce)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newHandlerTestSetup() *Adapter {
	return NewAdapter(Config{
		ClientKey:     "test_key",
		ClientSecret:  "test_secret",
		WebhookSecret: "handler_secret",
	}, "ch_handler_test", func(ctx context.Context) (string, error) {
		return "token", nil
	})
}

func TestHandler_POST_Challenge(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	body := `{"challenge":"verify_abc123"}`
	req := buildSignedDouyinRequest(t, "handler_secret", body)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["challenge"] != "verify_abc123" {
		t.Errorf("challenge = %q, want %q", resp["challenge"], "verify_abc123")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestHandler_POST_InvalidSignature(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodPost, "/webhook/douyin", strings.NewReader(`{"event":"im"}`))
	req.Header.Set("X-Douyin-Signature", "bad")
	req.Header.Set("X-Douyin-Timestamp", "1")
	req.Header.Set("X-Douyin-Nonce", "2")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodGet, "/webhook/douyin", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_PUT_MethodNotAllowed(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodPut, "/webhook/douyin", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_POST_Message_NilProducer(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	msg := InboundMsg{
		Event:       "im",
		FromUserID:  "user123",
		ToUserID:    "app456",
		MessageType: "text",
		Content:     `{"text":"Hello"}`,
		MsgID:       "msg_001",
		CreateTime:  1680000000,
	}
	bodyBytes, _ := json.Marshal(msg)
	body := string(bodyBytes)

	req := buildSignedDouyinRequest(t, "handler_secret", body)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// Should return 200 success even with nil producer
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "success" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "success")
	}
}

func TestHandler_POST_UnparseableBody(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	body := `<<<not json`
	req := buildSignedDouyinRequest(t, "handler_secret", body)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// Should return 200 with "success" to prevent Douyin retries
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "success" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "success")
	}
}

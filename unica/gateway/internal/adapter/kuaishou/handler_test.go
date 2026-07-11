package kuaishou

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

// buildSignedKuaishouRequest creates a POST request with valid Kuaishou signature headers.
func buildSignedKuaishouRequest(t *testing.T, secret, body string) *http.Request {
	t.Helper()
	timestamp := "1680000000"
	nonce := "handler_nonce"

	raw := timestamp + nonce + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(raw))
	signature := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhook/kuaishou", strings.NewReader(body))
	req.Header.Set("X-Kuaishou-Signature", signature)
	req.Header.Set("X-Kuaishou-Timestamp", timestamp)
	req.Header.Set("X-Kuaishou-Nonce", nonce)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newHandlerTestSetup() *Adapter {
	return NewAdapter(Config{
		AppKey:        "test_key",
		AppSecret:     "test_secret",
		WebhookSecret: "handler_secret",
	}, "ch_handler_test", func(ctx context.Context) (string, error) {
		return "token", nil
	})
}

func TestHandler_POST_Challenge(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	body := `{"challenge":"verify_abc123"}`
	req := buildSignedKuaishouRequest(t, "handler_secret", body)
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/kuaishou", strings.NewReader(`{"event":"im"}`))
	req.Header.Set("X-Kuaishou-Signature", "bad")
	req.Header.Set("X-Kuaishou-Timestamp", "1")
	req.Header.Set("X-Kuaishou-Nonce", "2")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodGet, "/webhook/kuaishou", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_PUT_MethodNotAllowed(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodPut, "/webhook/kuaishou", nil)
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

	req := buildSignedKuaishouRequest(t, "handler_secret", body)
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
	req := buildSignedKuaishouRequest(t, "handler_secret", body)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// Should return 200 with "success" to prevent Kuaishou retries
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "success" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "success")
	}
}

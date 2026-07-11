package xiaohongshu

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// buildSignedBody creates a POST request with valid XHS signature headers for handler tests.
func buildSignedBody(secret, body string) *http.Request {
	timestamp := "1680000000"
	nonce := "handler_nonce"
	raw := timestamp + nonce + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(raw))
	sig := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/webhook/xiaohongshu", strings.NewReader(body))
	req.Header.Set("X-Signature", sig)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newHandlerTestAdapter() *Adapter {
	return NewAdapter(Config{
		AppID:         "xhs_handler_test",
		AppSecret:     "handler_secret",
		WebhookSecret: "handler_webhook_secret",
	}, "ch_handler_test", func(ctx context.Context) (string, error) {
		return "token", nil
	})
}

func TestHandler_POST_Challenge(t *testing.T) {
	a := newHandlerTestAdapter()
	// Pass nil producer since challenge doesn't use it
	h := NewWebhookHandler(a, nil)

	body := `{"event":"challenge","challenge":"my_challenge_token"}`
	req := buildSignedBody("handler_webhook_secret", body)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	expected := `{"challenge":"my_challenge_token"}`
	if rec.Body.String() != expected {
		t.Errorf("body = %q, want %q", rec.Body.String(), expected)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestHandler_POST_InvalidSignature(t *testing.T) {
	a := newHandlerTestAdapter()
	h := NewWebhookHandler(a, nil)

	body := `{"event":"im.message"}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/xiaohongshu", strings.NewReader(body))
	req.Header.Set("X-Signature", "bad_signature")
	req.Header.Set("X-Timestamp", "1680000000")
	req.Header.Set("X-Nonce", "nonce")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	a := newHandlerTestAdapter()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodGet, "/webhook/xiaohongshu", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_POST_UnparseableBody(t *testing.T) {
	a := newHandlerTestAdapter()
	h := NewWebhookHandler(a, nil)

	body := `<<<not json`
	req := buildSignedBody("handler_webhook_secret", body)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// Should return 200 with code:0 to prevent retries
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandler_PUT_NotAllowed(t *testing.T) {
	a := newHandlerTestAdapter()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodPut, "/webhook/xiaohongshu", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

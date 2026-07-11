package taobao

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// buildSignedTaobaoRequest creates a POST request with valid Taobao signature query param.
func buildSignedTaobaoRequest(t *testing.T, secret, body string) *http.Request {
	t.Helper()

	params := map[string]string{
		"method":    "taobao.im.message",
		"app_key":   "test_key",
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
	req := httptest.NewRequest(http.MethodPost, reqURL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newHandlerTestSetup() *Adapter {
	return NewAdapter(Config{
		AppKey:    "test_key",
		AppSecret: "handler_secret",
	}, "ch_handler_test", func(ctx context.Context) (string, error) {
		return "token", nil
	})
}

func TestHandler_POST_Challenge(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	body := `{"challenge":"verify_abc123"}`
	req := buildSignedTaobaoRequest(t, "handler_secret", body)
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

	req := httptest.NewRequest(http.MethodPost, "/webhook/taobao?sign=bad", strings.NewReader(`{"msg_type":"text"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodGet, "/webhook/taobao", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandler_PUT_MethodNotAllowed(t *testing.T) {
	a := newHandlerTestSetup()
	h := NewWebhookHandler(a, nil)

	req := httptest.NewRequest(http.MethodPut, "/webhook/taobao", nil)
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
		MsgType:    "text",
		FromUser:   "buyer123",
		ToUser:     "seller456",
		Content:    `{"text":"Hello"}`,
		MsgID:      "msg_001",
		CreateTime: 1680000000,
	}
	bodyBytes, _ := json.Marshal(msg)
	body := string(bodyBytes)

	req := buildSignedTaobaoRequest(t, "handler_secret", body)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

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
	req := buildSignedTaobaoRequest(t, "handler_secret", body)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// Should return 200 with "success" to prevent Taobao retries
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "success" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "success")
	}
}

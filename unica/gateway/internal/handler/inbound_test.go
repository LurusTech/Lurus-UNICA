package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kefu/unica/pkg/model"
)

// mockProducer is a test double that records published messages.
// We test the handler logic in isolation from Redis by creating a thin
// wrapper that satisfies the handler's needs.

func validMessageJSON() []byte {
	msg := model.StandardMessage{
		ID:     "test-uuid-001",
		Type:   model.MessageTypeInbound,
		Source: "adapter.test",
		Time:   time.Now(),
		Data: model.MessageData{
			ChannelID:     "ch-001",
			PlatformMsgID: "plat-001",
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "hello world",
			},
		},
	}
	b, _ := json.Marshal(msg)
	return b
}

func TestInboundHandler_MethodNotAllowed(t *testing.T) {
	h := NewInboundHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/gateway/inbound", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("got status %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestInboundHandler_InvalidJSON(t *testing.T) {
	h := NewInboundHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/gateway/inbound",
		bytes.NewBufferString("{invalid json"))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestInboundHandler_ValidationFailure(t *testing.T) {
	h := NewInboundHandler(nil)
	// Empty message — validation should fail (missing ID, Type, etc.)
	body := `{"id":"","type":"","source":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/gateway/inbound",
		bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("got status %d, want %d", w.Code, http.StatusBadRequest)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("expected error message in response")
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, map[string]string{"key": "value"})

	if w.Code != http.StatusOK {
		t.Errorf("got status %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["key"] != "value" {
		t.Errorf("resp[key] = %q, want value", resp["key"])
	}
}

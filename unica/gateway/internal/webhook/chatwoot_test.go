package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return rdb, mr
}

func TestChatwootWebhook_MethodNotAllowed(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/webhook/chatwoot", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestChatwootWebhook_InvalidToken(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(`{}`))
	req.Header.Set("X-Chatwoot-Webhook-Token", "wrong-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestChatwootWebhook_NoTokenRequired(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: ""})
	body := `{"event":"unknown_event"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for no-token config, got %d", w.Code)
	}
}

func TestChatwootWebhook_InvalidJSON(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(`not json`))
	req.Header.Set("X-Chatwoot-Webhook-Token", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestChatwootWebhook_MessageCreated_AgentReply(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	// Set up conversation mapping
	rdb.Set(context.Background(), "cw_conv:100", "unica-conv-001", 0)

	// Create outbound stream
	rdb.XAdd(context.Background(), &redis.XAddArgs{
		Stream: outboundStream,
		ID:     "*",
		Values: map[string]interface{}{"init": "true"},
	})

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})

	payload := cwWebhookPayload{
		Event:       "message_created",
		ID:          200,
		Content:     "Hello, let me help you with that.",
		MessageType: 1, // outgoing
		Private:     false,
		Conversation: struct {
			ID         int               `json:"id"`
			Status     string            `json:"status,omitempty"`
			DisplayID  int               `json:"display_id,omitempty"`
			CustomAttr map[string]string `json:"custom_attributes,omitempty"`
		}{
			ID: 100,
			CustomAttr: map[string]string{"channel_id": "ch-wechat"},
		},
		Sender: struct {
			ID   int    `json:"id"`
			Name string `json:"name,omitempty"`
			Type string `json:"type,omitempty"`
		}{
			ID:   5,
			Name: "Agent Smith",
			Type: "user",
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(string(body)))
	req.Header.Set("X-Chatwoot-Webhook-Token", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Verify message was published to outbound stream
	result, err := rdb.XRange(context.Background(), outboundStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("failed to read outbound stream: %v", err)
	}

	// Find the agent reply (skip the init message)
	found := false
	for _, entry := range result {
		if source, ok := entry.Values["source"].(string); ok && source == "chatwoot" {
			payloadStr, ok := entry.Values["payload"].(string)
			if !ok {
				continue
			}
			var msg map[string]interface{}
			json.Unmarshal([]byte(payloadStr), &msg)
			if msg["type"] == "message.outbound" && msg["source"] == "chatwoot" {
				data := msg["data"].(map[string]interface{})
				if data["conversation_id"] == "unica-conv-001" {
					content := data["content"].(map[string]interface{})
					if content["text"] == "Hello, let me help you with that." {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Error("expected agent reply message in outbound stream")
	}
}

func TestChatwootWebhook_MessageCreated_IncomingSkipped(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})

	payload := cwWebhookPayload{
		Event:       "message_created",
		ID:          300,
		Content:     "Customer message",
		MessageType: 0, // incoming - should be skipped
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(string(body)))
	req.Header.Set("X-Chatwoot-Webhook-Token", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestChatwootWebhook_MessageCreated_PrivateSkipped(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})

	payload := cwWebhookPayload{
		Event:       "message_created",
		ID:          400,
		Content:     "Internal note",
		MessageType: 1, // outgoing
		Private:     true, // private - should be skipped
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(string(body)))
	req.Header.Set("X-Chatwoot-Webhook-Token", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestChatwootWebhook_ConversationResolved(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	// Set up conversation mapping
	rdb.Set(context.Background(), "cw_conv:150", "unica-conv-002", 0)

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})

	payload := cwWebhookPayload{
		Event: "conversation_status_changed",
		Conversation: struct {
			ID         int               `json:"id"`
			Status     string            `json:"status,omitempty"`
			DisplayID  int               `json:"display_id,omitempty"`
			CustomAttr map[string]string `json:"custom_attributes,omitempty"`
		}{
			ID:     150,
			Status: "resolved",
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(string(body)))
	req.Header.Set("X-Chatwoot-Webhook-Token", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	// Verify close event was published
	result, err := rdb.XRange(context.Background(), outboundStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("failed to read outbound stream: %v", err)
	}

	found := false
	for _, entry := range result {
		if source, ok := entry.Values["source"].(string); ok && source == "chatwoot" {
			if typ, ok := entry.Values["type"].(string); ok && typ == "event.conversation_closed" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected conversation_closed event in outbound stream")
	}
}

func TestChatwootWebhook_ConversationNotResolved_Ignored(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})

	payload := cwWebhookPayload{
		Event: "conversation_status_changed",
		Conversation: struct {
			ID         int               `json:"id"`
			Status     string            `json:"status,omitempty"`
			DisplayID  int               `json:"display_id,omitempty"`
			CustomAttr map[string]string `json:"custom_attributes,omitempty"`
		}{
			ID:     160,
			Status: "open", // Not resolved, should be ignored
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(string(body)))
	req.Header.Set("X-Chatwoot-Webhook-Token", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestChatwootWebhook_UnknownEvent(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})

	payload := `{"event":"some_other_event"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(payload))
	req.Header.Set("X-Chatwoot-Webhook-Token", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for unknown events, got %d", w.Code)
	}
}

func TestChatwootWebhook_MissingConvMapping(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	handler := NewChatwootWebhookHandler(rdb, ChatwootWebhookConfig{WebhookToken: "secret"})

	// No conversation mapping in Redis
	payload := cwWebhookPayload{
		Event:       "message_created",
		ID:          500,
		Content:     "Agent reply",
		MessageType: 1,
		Conversation: struct {
			ID         int               `json:"id"`
			Status     string            `json:"status,omitempty"`
			DisplayID  int               `json:"display_id,omitempty"`
			CustomAttr map[string]string `json:"custom_attributes,omitempty"`
		}{
			ID: 999, // No mapping exists for this
		},
		Sender: struct {
			ID   int    `json:"id"`
			Name string `json:"name,omitempty"`
			Type string `json:"type,omitempty"`
		}{
			ID:   1,
			Type: "user",
		},
	}
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/chatwoot", strings.NewReader(string(body)))
	req.Header.Set("X-Chatwoot-Webhook-Token", "secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should still return 200 to prevent retries, but log error internally
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

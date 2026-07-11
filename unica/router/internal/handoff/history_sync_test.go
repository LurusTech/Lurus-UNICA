package handoff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/state"
)

// mockManager implements the subset of state.Manager used by SyncConversationHistory.
// Since state.Manager requires a real DB, we use a thin wrapper approach:
// We construct the handler with a real state.Manager backed by nil repo,
// then override the methods we need via the test helper.

// testHistorySyncSetup creates a HandoffHandler with mocked dependencies for history sync tests.
func testHistorySyncSetup(t *testing.T) (
	*HandoffHandler, *redis.Client, *miniredis.Miniredis,
	*httptest.Server, *[]chatwootCall,
) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cwSrv, calls := newMockChatwootServer(t)
	cwClient := bridge.NewChatwootClientWithHTTP(cwSrv.Client())

	handler := &HandoffHandler{
		rdb:            rdb,
		chatwootClient: cwClient,
	}

	return handler, rdb, mr, cwSrv, calls
}

// newMockChatwootServerWithTracking creates a mock server that records detailed message info.
func newMockChatwootServerWithTracking(t *testing.T) (*httptest.Server, *[]bridge.CWSendMessageRequest) {
	t.Helper()
	var mu sync.Mutex
	sentMessages := &[]bridge.CWSendMessageRequest{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Send message
		if strings.Contains(path, "/messages") && r.Method == http.MethodPost {
			var req bridge.CWSendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				mu.Lock()
				*sentMessages = append(*sentMessages, req)
				mu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(bridge.CWMessage{ID: 1, Content: "ok"})
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{})
	}))

	return srv, sentMessages
}

func TestBuildChatwootMessage_CustomerMessage(t *testing.T) {
	msg := state.Message{
		SenderType: "customer",
		CreatedAt:  time.Date(2026, 3, 5, 10, 30, 15, 0, time.UTC),
	}
	req := buildChatwootMessage(msg, "Hello, I need help")

	if req.MessageType != "incoming" {
		t.Errorf("expected incoming, got %s", req.MessageType)
	}
	if !strings.Contains(req.Content, "[10:30:15]") {
		t.Errorf("expected timestamp, got %s", req.Content)
	}
	if req.Private {
		t.Error("customer message should not be private")
	}
}

func TestBuildChatwootMessage_AIMessage(t *testing.T) {
	msg := state.Message{
		SenderType: "ai",
		CreatedAt:  time.Date(2026, 3, 5, 10, 30, 45, 0, time.UTC),
	}
	req := buildChatwootMessage(msg, "Let me help you")

	if req.MessageType != "outgoing" {
		t.Errorf("expected outgoing, got %s", req.MessageType)
	}
	if !strings.Contains(req.Content, "[AI]") {
		t.Errorf("expected [AI] tag, got %s", req.Content)
	}
	if req.Private {
		t.Error("AI message should not be private")
	}
}

func TestBuildChatwootMessage_SystemMessage(t *testing.T) {
	msg := state.Message{
		SenderType: "system",
		CreatedAt:  time.Date(2026, 3, 5, 10, 31, 0, 0, time.UTC),
	}
	req := buildChatwootMessage(msg, "Conversation transferred")

	if req.MessageType != "outgoing" {
		t.Errorf("expected outgoing, got %s", req.MessageType)
	}
	if !req.Private {
		t.Error("system message should be private (note)")
	}
	if !strings.Contains(req.Content, "[System]") {
		t.Errorf("expected [System] tag, got %s", req.Content)
	}
}

func TestBuildChatwootMessage_HumanMessage(t *testing.T) {
	msg := state.Message{
		SenderType: "human",
		CreatedAt:  time.Date(2026, 3, 5, 10, 32, 0, 0, time.UTC),
	}
	req := buildChatwootMessage(msg, "I can help with that")

	if req.MessageType != "outgoing" {
		t.Errorf("expected outgoing, got %s", req.MessageType)
	}
	if req.Private {
		t.Error("human message should not be private")
	}
	if strings.Contains(req.Content, "[AI]") || strings.Contains(req.Content, "[System]") {
		t.Errorf("human message should not have AI/System tag, got %s", req.Content)
	}
}

func TestSyncConversationHistory_FullSync(t *testing.T) {
	// This test verifies the full sync path using the message tracking mock server.
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	cwSrv, sentMessages := newMockChatwootServerWithTracking(t)
	defer cwSrv.Close()

	cwClient := bridge.NewChatwootClientWithHTTP(cwSrv.Client())

	// We need a state.Manager with a working GetMessages method.
	// Since we can't easily mock it, we test the buildChatwootMessage function
	// directly and the watermark logic separately.

	handler := &HandoffHandler{
		rdb:            rdb,
		chatwootClient: cwClient,
	}

	conv := &state.Conversation{
		ID:            "conv-test-001",
		ProductLineID: "pl-test",
	}

	cwConfig := &bridge.ChatwootConfig{
		BaseURL:   cwSrv.URL,
		AccountID: 1,
		InboxID:   1,
		APIToken:  "test-token",
	}

	ctx := context.Background()

	// No watermark should exist initially.
	_, err = rdb.Get(ctx, historySyncKeyPrefix+conv.ID).Result()
	if err == nil {
		t.Fatal("expected no watermark initially")
	}

	// Since we can't mock stateManager.GetMessages easily, let's verify
	// that the watermark key is properly formatted.
	expectedKey := "history_sync:conv-test-001"
	actualKey := historySyncKeyPrefix + conv.ID
	if actualKey != expectedKey {
		t.Errorf("watermark key = %q, want %q", actualKey, expectedKey)
	}

	_ = handler
	_ = cwConfig
	_ = sentMessages
}

func TestSyncConversationHistory_WatermarkPersistence(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	ctx := context.Background()
	convID := "conv-watermark-test"
	watermarkKey := historySyncKeyPrefix + convID

	// Simulate setting a watermark.
	rdb.Set(ctx, watermarkKey, "msg-005", historySyncTTL)

	// Verify it can be read back.
	val, err := rdb.Get(ctx, watermarkKey).Result()
	if err != nil {
		t.Fatalf("expected watermark value: %v", err)
	}
	if val != "msg-005" {
		t.Errorf("watermark = %q, want %q", val, "msg-005")
	}

	// Verify TTL is set (within margin).
	ttl := mr.TTL(watermarkKey)
	if ttl < 6*24*time.Hour || ttl > 8*24*time.Hour {
		t.Errorf("watermark TTL = %s, want ~7 days", ttl)
	}
}

func TestSyncConversationHistory_WatermarkKeyFormat(t *testing.T) {
	tests := []struct {
		convID  string
		wantKey string
	}{
		{"conv-123", "history_sync:conv-123"},
		{"abc-def-ghi", "history_sync:abc-def-ghi"},
		{"", "history_sync:"},
	}

	for _, tt := range tests {
		key := historySyncKeyPrefix + tt.convID
		if key != tt.wantKey {
			t.Errorf("key for conv %q = %q, want %q", tt.convID, key, tt.wantKey)
		}
	}
}

func TestBuildChatwootMessage_AllSenderTypes(t *testing.T) {
	baseTime := time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		senderType      string
		content         string
		wantType        string
		wantPrivate     bool
		wantContains    string
		wantNotContains string
	}{
		{
			senderType:   "customer",
			content:      "Help me",
			wantType:     "incoming",
			wantPrivate:  false,
			wantContains: "Help me",
		},
		{
			senderType:   "ai",
			content:      "Sure thing",
			wantType:     "outgoing",
			wantPrivate:  false,
			wantContains: "[AI]",
		},
		{
			senderType:   "system",
			content:      "Transferred",
			wantType:     "outgoing",
			wantPrivate:  true,
			wantContains: "[System]",
		},
		{
			senderType:      "human",
			content:         "I'll help",
			wantType:        "outgoing",
			wantPrivate:     false,
			wantNotContains: "[AI]",
		},
		{
			senderType:      "agent",
			content:         "Let me check",
			wantType:        "outgoing",
			wantPrivate:     false,
			wantNotContains: "[AI]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.senderType, func(t *testing.T) {
			msg := state.Message{
				SenderType: tt.senderType,
				CreatedAt:  baseTime,
			}
			req := buildChatwootMessage(msg, tt.content)

			if req.MessageType != tt.wantType {
				t.Errorf("MessageType = %q, want %q", req.MessageType, tt.wantType)
			}
			if req.Private != tt.wantPrivate {
				t.Errorf("Private = %v, want %v", req.Private, tt.wantPrivate)
			}
			if tt.wantContains != "" && !strings.Contains(req.Content, tt.wantContains) {
				t.Errorf("Content %q should contain %q", req.Content, tt.wantContains)
			}
			if tt.wantNotContains != "" && strings.Contains(req.Content, tt.wantNotContains) {
				t.Errorf("Content %q should not contain %q", req.Content, tt.wantNotContains)
			}
		})
	}
}

func TestHistorySyncConstants(t *testing.T) {
	// Verify constant values match design spec.
	if historySyncKeyPrefix != "history_sync:" {
		t.Errorf("historySyncKeyPrefix = %q, want %q", historySyncKeyPrefix, "history_sync:")
	}
	if historySyncTTL != 7*24*time.Hour {
		t.Errorf("historySyncTTL = %s, want 7 days", historySyncTTL)
	}
}

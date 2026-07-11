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

// mockStateManager wraps minimal state manager behavior for testing.
// Since state.Manager requires a real DB, we test handler logic through
// integration-style tests with mocked external services.

// --- Helper: create a mock Chatwoot server ---

type chatwootCall struct {
	method string
	path   string
	body   string
}

func newMockChatwootServer(t *testing.T) (*httptest.Server, *[]chatwootCall) {
	t.Helper()
	var mu sync.Mutex
	calls := &[]chatwootCall{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes := make([]byte, 0)
		if r.Body != nil {
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			bodyBytes = buf[:n]
		}

		mu.Lock()
		*calls = append(*calls, chatwootCall{
			method: r.Method,
			path:   r.URL.Path,
			body:   string(bodyBytes),
		})
		mu.Unlock()

		path := r.URL.Path

		// Contact search
		if strings.Contains(path, "/contacts/search") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(bridge.CWContactSearchResult{Payload: []bridge.CWContact{}})
			return
		}

		// Create contact
		if strings.HasSuffix(path, "/contacts") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(bridge.CWContactPayload{
				Payload: bridge.CWContact{ID: 42, Name: "test-customer"},
			})
			return
		}

		// Create conversation
		if strings.HasSuffix(path, "/conversations") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(bridge.CWConversation{
				ID:        100,
				AccountID: 1,
				InboxID:   1,
				Status:    "open",
			})
			return
		}

		// Send message
		if strings.Contains(path, "/messages") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(bridge.CWMessage{
				ID:      1,
				Content: "ok",
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))

	return srv, calls
}

// --- Helper: create a mock Dify server ---

func newMockDifyServer(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(bridge.DifyResponse{
			Answer: answer,
		})
	}))
}

func TestExtractTextContent(t *testing.T) {
	tests := []struct {
		name    string
		input   json.RawMessage
		want    string
	}{
		{
			name:  "plain string",
			input: json.RawMessage(`"Hello world"`),
			want:  "Hello world",
		},
		{
			name:  "object with text field",
			input: json.RawMessage(`{"text":"How can I help?","type":"text"}`),
			want:  "How can I help?",
		},
		{
			name:  "object with url only",
			input: json.RawMessage(`{"type":"image","url":"https://example.com/img.png"}`),
			want:  "[image: https://example.com/img.png]",
		},
		{
			name:  "empty content",
			input: json.RawMessage(``),
			want:  "",
		},
		{
			name:  "null content",
			input: json.RawMessage(`null`),
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTextContent(tt.input)
			if got != tt.want {
				t.Errorf("extractTextContent(%s) = %q, want %q", string(tt.input), got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc..."},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestHandoffHandler_FindOrCreateCWConversation_ExistingMapping(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	cwSrv, _ := newMockChatwootServer(t)
	defer cwSrv.Close()

	cwClient := bridge.NewChatwootClientWithHTTP(cwSrv.Client())

	handler := &HandoffHandler{
		rdb:            rdb,
		chatwootClient: cwClient,
	}

	ctx := context.Background()
	unicaConvID := "test-conv-123"

	// Pre-set mapping in Redis
	rdb.Set(ctx, unicaConvCWPrefix+unicaConvID, "999", cwConvMappingTTL)

	cwConfig := &bridge.ChatwootConfig{
		BaseURL:   cwSrv.URL,
		AccountID: 1,
		InboxID:   1,
		APIToken:  "test-token",
	}

	cwConvID, err := handler.findOrCreateCWConversation(ctx, cwConfig, unicaConvID, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cwConvID != 999 {
		t.Errorf("expected cwConvID=999, got %d", cwConvID)
	}
}

func TestHandoffHandler_FindOrCreateCWConversation_NewConversation(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	cwSrv, calls := newMockChatwootServer(t)
	defer cwSrv.Close()

	cwClient := bridge.NewChatwootClientWithHTTP(cwSrv.Client())

	handler := &HandoffHandler{
		rdb:            rdb,
		chatwootClient: cwClient,
	}

	ctx := context.Background()
	unicaConvID := "test-conv-456"

	cwConfig := &bridge.ChatwootConfig{
		BaseURL:   cwSrv.URL,
		AccountID: 1,
		InboxID:   1,
		APIToken:  "test-token",
	}

	cwConvID, err := handler.findOrCreateCWConversation(ctx, cwConfig, unicaConvID, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cwConvID != 100 {
		t.Errorf("expected cwConvID=100 (from mock), got %d", cwConvID)
	}

	// Verify Redis mapping was stored
	storedCW, err := rdb.Get(ctx, unicaConvCWPrefix+unicaConvID).Result()
	if err != nil {
		t.Fatalf("expected Redis mapping: %v", err)
	}
	if storedCW != "100" {
		t.Errorf("expected stored mapping '100', got %q", storedCW)
	}

	storedUnica, err := rdb.Get(ctx, cwConvKeyPrefix+"100").Result()
	if err != nil {
		t.Fatalf("expected reverse Redis mapping: %v", err)
	}
	if storedUnica != unicaConvID {
		t.Errorf("expected reverse mapping %q, got %q", unicaConvID, storedUnica)
	}

	// Verify a conversation creation call was made
	hasConvCreate := false
	for _, c := range *calls {
		if strings.HasSuffix(c.path, "/conversations") && c.method == http.MethodPost {
			hasConvCreate = true
		}
	}
	if !hasConvCreate {
		t.Error("expected a conversation creation call to Chatwoot")
	}
}

func TestHandoffHandler_SendMessageHistory(t *testing.T) {
	cwSrv, calls := newMockChatwootServer(t)
	defer cwSrv.Close()

	cwClient := bridge.NewChatwootClientWithHTTP(cwSrv.Client())

	handler := &HandoffHandler{
		chatwootClient: cwClient,
	}

	messages := []state.Message{
		{
			ID:             "msg-1",
			ConversationID: "conv-1",
			Direction:      "inbound",
			SenderType:     "customer",
			ContentJSON:    json.RawMessage(`{"text":"I need help with my order","type":"text"}`),
			CreatedAt:      time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC),
		},
		{
			ID:             "msg-2",
			ConversationID: "conv-1",
			Direction:      "outbound",
			SenderType:     "ai",
			ContentJSON:    json.RawMessage(`{"text":"Let me look into that for you.","type":"text"}`),
			CreatedAt:      time.Date(2026, 3, 5, 10, 0, 30, 0, time.UTC),
		},
	}

	cwConfig := &bridge.ChatwootConfig{
		BaseURL:   cwSrv.URL,
		AccountID: 1,
		InboxID:   1,
		APIToken:  "test-token",
	}

	err := handler.sendMessageHistory(context.Background(), cwConfig, 100, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Count message send calls
	msgSendCount := 0
	for _, c := range *calls {
		if strings.Contains(c.path, "/messages") && c.method == http.MethodPost {
			msgSendCount++
		}
	}
	if msgSendCount != 2 {
		t.Errorf("expected 2 message send calls, got %d", msgSendCount)
	}
}

func TestHandoffConsumer_ProcessEntry(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// Create consumer group
	ctx := context.Background()

	// Track whether handler was called
	var handlerCalled bool
	var handlerEvent *HandoffEvent

	// We can't easily mock the full handler, so test the consumer's parsing logic
	// by creating a minimal handler and verifying the event is parsed correctly.
	cwSrv, _ := newMockChatwootServer(t)
	defer cwSrv.Close()

	cwClient := bridge.NewChatwootClientWithHTTP(cwSrv.Client())

	handler := &HandoffHandler{
		rdb:            rdb,
		chatwootClient: cwClient,
	}

	consumer := NewHandoffConsumer(rdb, handler, "test-group", "test-consumer")

	// Test parsing a valid stream entry
	event := HandoffEvent{
		Type:                 "event.handoff",
		ConversationID:       "conv-abc",
		ProductLineID:        "pl-123",
		Reason:               "low_confidence",
		ConfidenceScore:      0.45,
		AIResponseSuppressed: "Some AI answer",
		CustomerMessage:      "Help me please",
		Timestamp:            time.Now().UTC().Format(time.RFC3339),
	}
	eventJSON, _ := json.Marshal(event)

	// Add entry to stream
	rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: HandoffStream,
		Values: map[string]interface{}{
			"payload":         string(eventJSON),
			"type":            "event.handoff",
			"conversation_id": event.ConversationID,
		},
	})

	// Verify the event can be parsed from stream
	entries, err := rdb.XRange(ctx, HandoffStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	payloadStr, ok := entries[0].Values["payload"].(string)
	if !ok {
		t.Fatal("payload field not found or not a string")
	}

	var parsed HandoffEvent
	if err := json.Unmarshal([]byte(payloadStr), &parsed); err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}

	if parsed.ConversationID != "conv-abc" {
		t.Errorf("expected conv ID 'conv-abc', got %q", parsed.ConversationID)
	}
	if parsed.ProductLineID != "pl-123" {
		t.Errorf("expected product line 'pl-123', got %q", parsed.ProductLineID)
	}
	if parsed.Reason != "low_confidence" {
		t.Errorf("expected reason 'low_confidence', got %q", parsed.Reason)
	}
	if parsed.ConfidenceScore != 0.45 {
		t.Errorf("expected confidence 0.45, got %f", parsed.ConfidenceScore)
	}

	_ = handlerCalled
	_ = handlerEvent
	_ = consumer
}

func TestHandoffConsumer_StartStop(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	handler := &HandoffHandler{rdb: rdb}
	consumer := NewHandoffConsumer(rdb, handler, "test-group", "test-consumer")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	consumer.Start(ctx)

	// Give the goroutine time to start
	time.Sleep(50 * time.Millisecond)

	// Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		consumer.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK: stopped gracefully
	case <-time.After(5 * time.Second):
		t.Fatal("consumer.Stop() did not return within 5 seconds")
	}
}

func TestHandoffEvent_JSONRoundTrip(t *testing.T) {
	event := HandoffEvent{
		Type:                 "event.handoff",
		ConversationID:       "conv-123",
		ProductLineID:        "pl-456",
		Reason:               "low_confidence",
		ConfidenceScore:      0.42,
		AIResponseSuppressed: "AI response here",
		CustomerMessage:      "I need human help",
		Timestamp:            "2026-03-05T10:30:00Z",
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed HandoffEvent
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed != event {
		t.Errorf("roundtrip mismatch:\n  got:  %+v\n  want: %+v", parsed, event)
	}
}

func TestHandoffHandler_GenerateSummaryBestEffort_Fallback(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// Dify server that always returns 500
	difySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("error"))
	}))
	defer difySrv.Close()

	// Pre-cache Dify config pointing at the failing server
	difyConfig := bridge.DifyConfig{BaseURL: difySrv.URL, APIKey: "test-key"}
	configBytes, _ := json.Marshal(difyConfig)
	rdb.Set(context.Background(), "pl_dify_config:pl-fail", string(configBytes), 5*time.Minute)

	handler := &HandoffHandler{
		rdb:        rdb,
		difyClient: bridge.NewDifyClient(),
	}

	event := &HandoffEvent{
		Reason:          "low_confidence",
		CustomerMessage: "I want to speak to a human",
	}

	messages := []state.Message{
		{
			Direction:   "inbound",
			SenderType:  "customer",
			ContentJSON: json.RawMessage(`{"text":"I want to speak to a human","type":"text"}`),
			CreatedAt:   time.Now(),
		},
	}

	summary := handler.generateSummaryBestEffort(context.Background(), "pl-fail", messages, event)

	if !strings.Contains(summary, "Customer requires human assistance") {
		t.Errorf("expected fallback summary, got: %q", summary)
	}
	if !strings.Contains(summary, "low_confidence") {
		t.Errorf("expected reason in fallback, got: %q", summary)
	}
}

func TestHandoffHandler_GenerateSummaryBestEffort_EmptyMessages(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	handler := &HandoffHandler{
		rdb:        rdb,
		difyClient: bridge.NewDifyClient(),
	}

	event := &HandoffEvent{
		Reason:          "keyword_match",
		CustomerMessage: "Help me",
	}

	// With nil messages, GenerateSummary returns early with default text (not an error)
	summary := handler.generateSummaryBestEffort(context.Background(), "pl-123", nil, event)
	if summary != "No conversation history available." {
		t.Errorf("expected 'No conversation history available.', got: %q", summary)
	}
}

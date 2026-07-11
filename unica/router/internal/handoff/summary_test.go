package handoff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/state"
)

func TestBuildTranscript(t *testing.T) {
	messages := []state.Message{
		{
			Direction:   "inbound",
			SenderType:  "customer",
			ContentJSON: json.RawMessage(`{"text":"I have a problem with my order","type":"text"}`),
			CreatedAt:   time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC),
		},
		{
			Direction:   "outbound",
			SenderType:  "ai",
			ContentJSON: json.RawMessage(`{"text":"Could you provide your order number?","type":"text"}`),
			CreatedAt:   time.Date(2026, 3, 5, 10, 0, 15, 0, time.UTC),
		},
		{
			Direction:   "inbound",
			SenderType:  "customer",
			ContentJSON: json.RawMessage(`{"text":"Order #12345","type":"text"}`),
			CreatedAt:   time.Date(2026, 3, 5, 10, 0, 30, 0, time.UTC),
		},
	}

	transcript := buildTranscript(messages)

	if !strings.Contains(transcript, "Customer: I have a problem with my order") {
		t.Errorf("expected customer message in transcript, got:\n%s", transcript)
	}
	if !strings.Contains(transcript, "AI: Could you provide your order number?") {
		t.Errorf("expected AI message in transcript, got:\n%s", transcript)
	}
	if !strings.Contains(transcript, "Customer: Order #12345") {
		t.Errorf("expected second customer message in transcript, got:\n%s", transcript)
	}

	// Verify time prefixes
	if !strings.Contains(transcript, "[10:00]") {
		t.Errorf("expected time prefix [10:00] in transcript, got:\n%s", transcript)
	}
}

func TestBuildTranscript_EmptyMessages(t *testing.T) {
	transcript := buildTranscript(nil)
	if transcript != "" {
		t.Errorf("expected empty transcript for nil messages, got: %q", transcript)
	}
}

func TestBuildTranscript_SkipsEmptyContent(t *testing.T) {
	messages := []state.Message{
		{
			Direction:   "inbound",
			SenderType:  "customer",
			ContentJSON: json.RawMessage(`{"text":"Hello","type":"text"}`),
			CreatedAt:   time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC),
		},
		{
			Direction:   "inbound",
			SenderType:  "customer",
			ContentJSON: json.RawMessage(``), // Empty content
			CreatedAt:   time.Date(2026, 3, 5, 10, 1, 0, 0, time.UTC),
		},
	}

	transcript := buildTranscript(messages)
	lines := strings.Split(strings.TrimSpace(transcript), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line (skip empty content), got %d lines:\n%s", len(lines), transcript)
	}
}

func TestGenerateSummary_Success(t *testing.T) {
	// Create mock Dify server
	expectedSummary := "Customer is asking about order #12345 delivery status. AI could not find the order. Handoff needed for manual lookup."
	difySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it's a POST to /chat-messages
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/chat-messages") {
			t.Errorf("expected path ending in /chat-messages, got %s", r.URL.Path)
		}

		// Verify request body contains the transcript
		var reqBody struct {
			Query string `json:"query"`
			User  string `json:"user"`
		}
		json.NewDecoder(r.Body).Decode(&reqBody)

		if reqBody.User != summaryUserID {
			t.Errorf("expected user %q, got %q", summaryUserID, reqBody.User)
		}
		if !strings.Contains(reqBody.Query, "请用1-2句话总结") {
			t.Errorf("expected Chinese summary prompt, got: %s", reqBody.Query[:50])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(bridge.DifyResponse{
			Answer: expectedSummary,
		})
	}))
	defer difySrv.Close()

	// Create miniredis with Dify config
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// Pre-cache Dify config
	difyConfig := bridge.DifyConfig{
		BaseURL: difySrv.URL,
		APIKey:  "test-key",
	}
	configBytes, _ := json.Marshal(difyConfig)
	rdb.Set(context.Background(), "pl_dify_config:pl-123", string(configBytes), 5*time.Minute)

	// The DifyClient.Chat method uses its own http.Client, and the config
	// from Redis will point at our mock server URL.
	handler := &HandoffHandler{
		rdb:        rdb,
		difyClient: bridge.NewDifyClient(),
	}

	messages := []state.Message{
		{
			Direction:   "inbound",
			SenderType:  "customer",
			ContentJSON: json.RawMessage(`{"text":"Where is my order #12345?","type":"text"}`),
			CreatedAt:   time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC),
		},
		{
			Direction:   "outbound",
			SenderType:  "ai",
			ContentJSON: json.RawMessage(`{"text":"I could not find that order.","type":"text"}`),
			CreatedAt:   time.Date(2026, 3, 5, 10, 0, 15, 0, time.UTC),
		},
	}

	summary, err := handler.GenerateSummary(context.Background(), "pl-123", messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != expectedSummary {
		t.Errorf("expected summary %q, got %q", expectedSummary, summary)
	}
}

func TestGenerateSummary_EmptyMessages(t *testing.T) {
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

	summary, err := handler.GenerateSummary(context.Background(), "pl-123", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != "No conversation history available." {
		t.Errorf("expected default summary for empty messages, got: %q", summary)
	}
}

func TestGenerateSummary_DifyError_ReturnsError(t *testing.T) {
	// Dify server that always returns 500
	difySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer difySrv.Close()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// Cache Dify config pointing at the failing server
	difyConfig := bridge.DifyConfig{
		BaseURL: difySrv.URL,
		APIKey:  "test-key",
	}
	configBytes, _ := json.Marshal(difyConfig)
	rdb.Set(context.Background(), "pl_dify_config:pl-err", string(configBytes), 5*time.Minute)

	handler := &HandoffHandler{
		rdb:        rdb,
		difyClient: bridge.NewDifyClient(),
	}

	messages := []state.Message{
		{
			Direction:   "inbound",
			SenderType:  "customer",
			ContentJSON: json.RawMessage(`{"text":"Help","type":"text"}`),
			CreatedAt:   time.Now(),
		},
	}

	// Use a short context timeout to avoid waiting for retries
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = handler.GenerateSummary(ctx, "pl-err", messages)
	if err == nil {
		t.Error("expected error from Dify failure, got nil")
	}
}

func TestLoadDifyConfig_FromRedisCache(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	handler := &HandoffHandler{rdb: rdb}

	// Pre-cache config
	cfg := bridge.DifyConfig{BaseURL: "http://dify:3000", APIKey: "cached-key"}
	configBytes, _ := json.Marshal(cfg)
	rdb.Set(context.Background(), "pl_dify_config:pl-cached", string(configBytes), 5*time.Minute)

	loaded, err := handler.loadDifyConfig(context.Background(), "pl-cached")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loaded.BaseURL != "http://dify:3000" {
		t.Errorf("expected base URL 'http://dify:3000', got %q", loaded.BaseURL)
	}
	if loaded.APIKey != "cached-key" {
		t.Errorf("expected API key 'cached-key', got %q", loaded.APIKey)
	}
}

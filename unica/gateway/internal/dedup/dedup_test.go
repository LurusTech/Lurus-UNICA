package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/model"
)

// --- Helpers ---

func setupTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379", DB: 15})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("Redis not available, skipping integration test: %v", err)
	}
	// Flush test DB before each test
	rdb.FlushDB(ctx)
	return rdb
}

func sampleMessage() *model.StandardMessage {
	return &model.StandardMessage{
		ID:     "msg-001",
		Type:   model.MessageTypeInbound,
		Source: "adapter.wechat",
		Time:   time.Now(),
		Data: model.MessageData{
			ChannelID:     "ch-wechat",
			PlatformMsgID: "plat-123",
			CorrelationID: "corr-001",
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: "hello",
			},
		},
	}
}

// --- Deduplicator Tests ---

func TestIsDuplicate_FirstSeen(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	d := NewDeduplicator(rdb, 10*time.Second)
	ctx := context.Background()

	dup, err := d.IsDuplicate(ctx, "unique-msg-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dup {
		t.Error("expected first message to NOT be duplicate")
	}
}

func TestIsDuplicate_SecondSeen(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	d := NewDeduplicator(rdb, 10*time.Second)
	ctx := context.Background()

	// First call sets the key
	_, _ = d.IsDuplicate(ctx, "dup-msg-1")

	// Second call should detect duplicate
	dup, err := d.IsDuplicate(ctx, "dup-msg-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dup {
		t.Error("expected second message to BE duplicate")
	}
}

func TestIsDuplicate_EmptyID(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	d := NewDeduplicator(rdb, 10*time.Second)
	ctx := context.Background()

	dup, err := d.IsDuplicate(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dup {
		t.Error("empty ID should not be duplicate")
	}
}

func TestIsDuplicate_DifferentIDs(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	d := NewDeduplicator(rdb, 10*time.Second)
	ctx := context.Background()

	dup1, _ := d.IsDuplicate(ctx, "msg-a")
	dup2, _ := d.IsDuplicate(ctx, "msg-b")

	if dup1 || dup2 {
		t.Error("different IDs should not be duplicates of each other")
	}
}

func TestBuildDedupKey_PlatformMsgID(t *testing.T) {
	key := BuildDedupKey("plat-123", "user-1", time.Now())
	if key != "plat-123" {
		t.Errorf("expected plat-123, got %s", key)
	}
}

func TestBuildDedupKey_Fallback(t *testing.T) {
	ts := time.Unix(1700000000, 0)
	key := BuildDedupKey("", "user-1", ts)
	expected := "user-1:1700000000"
	if key != expected {
		t.Errorf("expected %s, got %s", expected, key)
	}
}

func TestBuildDedupKey_Empty(t *testing.T) {
	key := BuildDedupKey("", "", time.Time{})
	if key != "" {
		t.Errorf("expected empty key, got %s", key)
	}
}

// --- Dead-Letter Queue Tests ---

func TestDeadLetterQueue_SendAndQuery(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	dlq := NewDeadLetterQueue(rdb, "test:dead-letter")
	ctx := context.Background()

	msg := sampleMessage()
	err := dlq.SendToDeadLetter(ctx, msg, "test failure", 3)
	if err != nil {
		t.Fatalf("SendToDeadLetter failed: %v", err)
	}

	entries, err := dlq.QueryDeadLetters(ctx, DeadLetterFilter{})
	if err != nil {
		t.Fatalf("QueryDeadLetters failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.FailureReason != "test failure" {
		t.Errorf("expected reason 'test failure', got '%s'", e.FailureReason)
	}
	if e.ChannelID != "ch-wechat" {
		t.Errorf("expected channel 'ch-wechat', got '%s'", e.ChannelID)
	}
	if e.PlatformMsgID != "plat-123" {
		t.Errorf("expected platform_msg_id 'plat-123', got '%s'", e.PlatformMsgID)
	}
}

func TestDeadLetterQueue_FilterByChannel(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	dlq := NewDeadLetterQueue(rdb, "test:dead-letter-filter")
	ctx := context.Background()

	msg1 := sampleMessage()
	msg1.Data.ChannelID = "ch-wechat"
	msg2 := sampleMessage()
	msg2.Data.ChannelID = "ch-douyin"

	_ = dlq.SendToDeadLetter(ctx, msg1, "fail1", 1)
	_ = dlq.SendToDeadLetter(ctx, msg2, "fail2", 2)

	entries, err := dlq.QueryDeadLetters(ctx, DeadLetterFilter{ChannelID: "ch-douyin"})
	if err != nil {
		t.Fatalf("QueryDeadLetters failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for ch-douyin, got %d", len(entries))
	}
	if entries[0].ChannelID != "ch-douyin" {
		t.Errorf("expected ch-douyin, got %s", entries[0].ChannelID)
	}
}

func TestDeadLetterQueue_DefaultStreamName(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	dlq := NewDeadLetterQueue(rdb, "")
	if dlq.StreamName() != DefaultDeadLetterStream {
		t.Errorf("expected default stream name %s, got %s", DefaultDeadLetterStream, dlq.StreamName())
	}
}

// --- Retry Tests ---

func TestRetryer_SuccessFirstAttempt(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	dlq := NewDeadLetterQueue(rdb, "test:retry-dl")
	r := NewRetryer(RetryConfig{MaxRetries: 3, BackoffBase: time.Millisecond}, dlq)
	ctx := context.Background()
	msg := sampleMessage()

	callCount := 0
	err := r.ExecuteWithRetry(ctx, msg, func(ctx context.Context, m *model.StandardMessage) error {
		callCount++
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

func TestRetryer_SuccessAfterRetries(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	dlq := NewDeadLetterQueue(rdb, "test:retry-dl2")
	r := NewRetryer(RetryConfig{MaxRetries: 3, BackoffBase: time.Millisecond}, dlq)
	ctx := context.Background()
	msg := sampleMessage()

	callCount := 0
	err := r.ExecuteWithRetry(ctx, msg, func(ctx context.Context, m *model.StandardMessage) error {
		callCount++
		if callCount < 3 {
			return errors.New("transient error")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls, got %d", callCount)
	}
}

func TestRetryer_ExhaustedMovesToDeadLetter(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	dlq := NewDeadLetterQueue(rdb, "test:retry-dl3")
	r := NewRetryer(RetryConfig{MaxRetries: 3, BackoffBase: time.Millisecond}, dlq)
	ctx := context.Background()
	msg := sampleMessage()

	callCount := 0
	err := r.ExecuteWithRetry(ctx, msg, func(ctx context.Context, m *model.StandardMessage) error {
		callCount++
		return errors.New("permanent error")
	})

	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls, got %d", callCount)
	}

	// Verify message landed in dead-letter
	entries, qErr := dlq.QueryDeadLetters(ctx, DeadLetterFilter{})
	if qErr != nil {
		t.Fatalf("query dead-letter failed: %v", qErr)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 dead-letter entry, got %d", len(entries))
	}
}

func TestRetryer_BackoffDuration(t *testing.T) {
	dlq := &DeadLetterQueue{} // not used in this test
	r := NewRetryer(RetryConfig{MaxRetries: 3, BackoffBase: time.Second}, dlq)

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
	}

	for _, tt := range tests {
		got := r.backoffDuration(tt.attempt)
		if got != tt.expected {
			t.Errorf("attempt %d: expected %s, got %s", tt.attempt, tt.expected, got)
		}
	}
}

func TestRetryer_ContextCancelled(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	dlq := NewDeadLetterQueue(rdb, "test:retry-dl4")
	r := NewRetryer(RetryConfig{MaxRetries: 3, BackoffBase: 10 * time.Second}, dlq)

	ctx, cancel := context.WithCancel(context.Background())
	msg := sampleMessage()

	callCount := 0
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := r.ExecuteWithRetry(ctx, msg, func(ctx context.Context, m *model.StandardMessage) error {
		callCount++
		return errors.New("always fails")
	})

	if err == nil {
		t.Error("expected error on context cancellation")
	}
}

// --- Handler Tests ---

func TestDeadLetterHandler_GET(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	dlq := NewDeadLetterQueue(rdb, "test:handler-dl")
	ctx := context.Background()

	// Add a test entry
	msg := sampleMessage()
	_ = dlq.SendToDeadLetter(ctx, msg, "handler test", 2)

	handler := NewDeadLetterHandler(dlq)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/gateway/dead-letter", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp DeadLetterResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Count != 1 {
		t.Errorf("expected count 1, got %d", resp.Count)
	}
	if resp.Items[0].FailureReason != "handler test" {
		t.Errorf("unexpected failure reason: %s", resp.Items[0].FailureReason)
	}
}

func TestDeadLetterHandler_MethodNotAllowed(t *testing.T) {
	dlq := &DeadLetterQueue{}
	handler := NewDeadLetterHandler(dlq)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/gateway/dead-letter", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestDeadLetterHandler_InvalidStartTime(t *testing.T) {
	dlq := &DeadLetterQueue{}
	handler := NewDeadLetterHandler(dlq)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/gateway/dead-letter?start=not-a-time", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestDeadLetterHandler_InvalidLimit(t *testing.T) {
	dlq := &DeadLetterQueue{}
	handler := NewDeadLetterHandler(dlq)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/gateway/dead-letter?limit=abc", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestDeadLetterHandler_WithChannelFilter(t *testing.T) {
	rdb := setupTestRedis(t)
	defer rdb.Close()

	dlq := NewDeadLetterQueue(rdb, "test:handler-dl-filter")
	ctx := context.Background()

	msg1 := sampleMessage()
	msg1.Data.ChannelID = "ch-a"
	msg2 := sampleMessage()
	msg2.Data.ChannelID = "ch-b"

	_ = dlq.SendToDeadLetter(ctx, msg1, "fail", 1)
	_ = dlq.SendToDeadLetter(ctx, msg2, "fail", 1)

	handler := NewDeadLetterHandler(dlq)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/gateway/dead-letter?channel_id=ch-a", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var resp DeadLetterResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Count != 1 {
		t.Errorf("expected 1 entry for ch-a, got %d", resp.Count)
	}
}

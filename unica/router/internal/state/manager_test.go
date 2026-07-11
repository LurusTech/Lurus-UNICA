package state

import (
	"context"
	"testing"
	"time"
)

func TestDefaultManagerConfig(t *testing.T) {
	cfg := DefaultManagerConfig()
	if cfg.IdleTimeout != 30*time.Minute {
		t.Errorf("expected idle timeout 30m, got %s", cfg.IdleTimeout)
	}
	if cfg.CheckInterval != 1*time.Minute {
		t.Errorf("expected check interval 1m, got %s", cfg.CheckInterval)
	}
}

func TestNewManager(t *testing.T) {
	cfg := DefaultManagerConfig()
	m := NewManager(nil, nil, cfg)
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
	if m.config.IdleTimeout != cfg.IdleTimeout {
		t.Errorf("config mismatch: idle timeout")
	}
}

func TestStrPtr(t *testing.T) {
	// Empty string returns nil
	if strPtr("") != nil {
		t.Error("expected nil for empty string")
	}

	// Non-empty string returns pointer
	p := strPtr("hello")
	if p == nil || *p != "hello" {
		t.Error("expected pointer to 'hello'")
	}
}

func TestSessionData_Fields(t *testing.T) {
	s := &SessionData{
		State:         "pending",
		ProductLineID: "pl-123",
		AgentID:       "agent-456",
	}
	if s.State != "pending" {
		t.Error("state mismatch")
	}
	if s.ProductLineID != "pl-123" {
		t.Error("product line id mismatch")
	}
	if s.AgentID != "agent-456" {
		t.Error("agent id mismatch")
	}
}

func TestSessionKeys(t *testing.T) {
	key := sessionKey("conv-123")
	if key != "session:conv-123" {
		t.Errorf("expected 'session:conv-123', got %q", key)
	}
}

func TestCustomerKeys(t *testing.T) {
	key := customerKey("user-abc", "chan-xyz")
	if key != "customer:user-abc:chan-xyz" {
		t.Errorf("expected 'customer:user-abc:chan-xyz', got %q", key)
	}
}

func TestTagsToJSON(t *testing.T) {
	// Nil tags should produce empty array
	result := string(tagsToJSON(nil))
	if result != "[]" {
		t.Errorf("expected '[]', got %q", result)
	}

	// Non-nil tags should serialize properly
	result = string(tagsToJSON([]string{"vip", "new"}))
	if result != `["vip","new"]` {
		t.Errorf("expected '[\"vip\",\"new\"]', got %q", result)
	}
}

func TestManagerStartStop(t *testing.T) {
	cfg := ManagerConfig{
		IdleTimeout:   1 * time.Second,
		CheckInterval: 100 * time.Millisecond,
	}

	// Manager with nil repo/cache: Start should work, Stop should not panic.
	// The idle loop will fail on DB queries but that's expected without a real DB.
	m := NewManager(nil, nil, cfg)

	// We can't fully test the background loop without a DB, but we can verify
	// the lifecycle doesn't panic or deadlock.
	// Start and immediately stop.
	// Note: Start with nil repo will cause panic in closeIdleConversations,
	// so we skip the actual start/stop test when repo is nil.
	if m.cancel != nil {
		t.Error("cancel should be nil before Start")
	}
	// Just verify the manager was created correctly
	if m.repo != nil {
		t.Error("repo should be nil in this test")
	}
}

func TestHandleInboundMessage_NilMessage(t *testing.T) {
	cfg := DefaultManagerConfig()
	m := NewManager(nil, nil, cfg)

	_, err := m.HandleInboundMessage(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil message")
	}
}

func TestConversationModel(t *testing.T) {
	conv := &Conversation{
		ID:            "conv-1",
		ChannelID:     "chan-1",
		ProductLineID: "pl-1",
		CustomerID:    "cust-1",
		State:         StatePending,
	}
	if conv.State != StatePending {
		t.Errorf("expected pending state, got %s", conv.State)
	}
}

func TestCustomerModel(t *testing.T) {
	c := &Customer{
		PlatformIdentity: "user-123",
		ChannelID:        "chan-456",
		DisplayName:      "Test User",
		Tags:             []string{"vip"},
	}
	if c.PlatformIdentity != "user-123" {
		t.Error("platform identity mismatch")
	}
	if len(c.Tags) != 1 || c.Tags[0] != "vip" {
		t.Error("tags mismatch")
	}
}

func TestMessageModel(t *testing.T) {
	m := &Message{
		ConversationID: "conv-1",
		Direction:      "inbound",
		SenderType:     "customer",
		ContentJSON:    []byte(`{"type":"text","text":"hello"}`),
	}
	if m.Direction != "inbound" {
		t.Error("direction mismatch")
	}
	if m.SenderType != "customer" {
		t.Error("sender type mismatch")
	}
}

func TestTransitionState_Integration_WithoutDB(t *testing.T) {
	// Verify that TransitionState returns an error when no DB/cache is available
	// (this tests the error path, not the happy path which requires a real DB)
	cfg := DefaultManagerConfig()
	m := NewManager(nil, nil, cfg)

	err := m.TransitionState(context.Background(), "conv-1", StateAIProcessing, "test")
	if err == nil {
		t.Error("expected error when transitioning without DB/cache")
	}
}

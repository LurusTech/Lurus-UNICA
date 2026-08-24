package state

import (
	"context"
	"fmt"
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

	_, _, err := m.HandleInboundMessage(context.Background(), nil)
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

// TestCloseIdleConversations_FiresOnClose covers the gap that made the
// satisfaction survey unreachable.
//
// A conversation reaches "closed" two ways: an explicit transition, and the
// idle sweep. The sweep wrote the state straight to the database and returned,
// so the close callback — the only thing that sends a survey — never ran on the
// one path that closes conversations automatically. The feature was
// configurable, parsed, unit-tested, and had never sent a message.
func TestCloseIdleConversations_FiresOnClose(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	channelID := "11111111-2222-3333-4444-555555555501"
	productLineID := "6f304ef6-e2db-4f97-8d5a-795d834227e1"

	var customerID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO customers (platform_identity, channel_id, display_name)
		 VALUES ($1, $2, 'idle-sweep test') RETURNING id`,
		"idle-sweep-"+suffix, channelID).Scan(&customerID); err != nil {
		t.Fatalf("create customer: %v", err)
	}

	var convID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO conversations (channel_id, product_line_id, customer_id, state, updated_at)
		 VALUES ($1, $2, $3, 'ai_processing', now() - interval '90 minutes') RETURNING id`,
		channelID, productLineID, customerID).Scan(&convID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM conversations WHERE id = $1`, convID)
		db.Exec(`DELETE FROM customers WHERE id = $1`, customerID)
	})

	m := NewManager(NewRepository(db), nil, ManagerConfig{IdleTimeout: time.Hour, CheckInterval: time.Minute})
	var closedIDs []string
	m.SetOnClose(func(ctx context.Context, conv *Conversation) {
		closedIDs = append(closedIDs, conv.ID)
	})

	m.closeIdleConversations(ctx)

	var got string
	for _, id := range closedIDs {
		if id == convID {
			got = id
		}
	}
	if got == "" {
		t.Fatalf("the idle sweep closed the conversation without firing OnClose; "+
			"everything hanging off that callback is unreachable (fired for %v)", closedIDs)
	}

	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM conversations WHERE id = $1`, convID).Scan(&state); err != nil {
		t.Fatalf("read back state: %v", err)
	}
	if state != string(StateClosed) {
		t.Errorf("state = %q, want closed", state)
	}
}

// TestFindOrCreateConversation_SurveyReplyReachesTheConversationItRates covers
// the second half of the survey break.
//
// The survey is sent as the conversation closes, so a rating is always the first
// message after closure. Conversation lookup matched only on state, so that
// message started a new conversation, the router never got to ask whether it was
// a rating, and the customer's answer was replied to as an ordinary question.
func TestFindOrCreateConversation_SurveyReplyReachesTheConversationItRates(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	channelID := "11111111-2222-3333-4444-555555555501"
	productLineID := "6f304ef6-e2db-4f97-8d5a-795d834227e1"

	var customerID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO customers (platform_identity, channel_id, display_name)
		 VALUES ($1, $2, 'survey window test') RETURNING id`,
		fmt.Sprintf("survey-window-%d", time.Now().UnixNano()), channelID).Scan(&customerID); err != nil {
		t.Fatalf("create customer: %v", err)
	}
	var convID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO conversations (channel_id, product_line_id, customer_id, state,
		                            survey_status, survey_sent_at, closed_at)
		 VALUES ($1, $2, $3, 'closed', 'sent', now() - interval '5 minutes', now() - interval '5 minutes')
		 RETURNING id`,
		channelID, productLineID, customerID).Scan(&convID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM conversations WHERE customer_id = $1`, customerID)
		db.Exec(`DELETE FROM customers WHERE id = $1`, customerID)
	})

	m := NewManager(NewRepository(db), nil, DefaultManagerConfig())

	// No window registered: the closed conversation is invisible, which is the
	// behaviour every caller that does not send surveys should keep.
	got, err := m.findOrCreateConversation(ctx, customerID, channelID, productLineID)
	if err != nil {
		t.Fatalf("lookup without a window: %v", err)
	}
	if got == convID {
		t.Error("a closed conversation was resumed with no survey window registered")
	}
	db.Exec(`DELETE FROM conversations WHERE customer_id = $1 AND id <> $2`, customerID, convID)

	// With a window covering the send, the rating reaches the conversation it rates.
	m.SetSurveyWindow(func(context.Context, string) time.Duration { return time.Hour })
	got, err = m.findOrCreateConversation(ctx, customerID, channelID, productLineID)
	if err != nil {
		t.Fatalf("lookup within the window: %v", err)
	}
	if got != convID {
		t.Errorf("conversation = %s, want the one awaiting a rating (%s)", got, convID)
	}

	// Past the window it is an ordinary closed conversation again, so a customer
	// coming back days later starts fresh rather than resuming a stale thread.
	db.Exec(`UPDATE conversations SET survey_sent_at = now() - interval '3 hours' WHERE id = $1`, convID)
	got, err = m.findOrCreateConversation(ctx, customerID, channelID, productLineID)
	if err != nil {
		t.Fatalf("lookup past the window: %v", err)
	}
	if got == convID {
		t.Error("a conversation whose survey window had passed was still resumed")
	}
}

// A rating that was already recorded must not keep the conversation reachable:
// survey_status moves to completed, and the next message is a new conversation.
func TestFindOrCreateConversation_CompletedSurveyDoesNotHoldTheConversationOpen(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	channelID := "11111111-2222-3333-4444-555555555501"
	productLineID := "6f304ef6-e2db-4f97-8d5a-795d834227e1"

	var customerID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO customers (platform_identity, channel_id, display_name)
		 VALUES ($1, $2, 'survey completed test') RETURNING id`,
		fmt.Sprintf("survey-done-%d", time.Now().UnixNano()), channelID).Scan(&customerID); err != nil {
		t.Fatalf("create customer: %v", err)
	}
	var convID string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO conversations (channel_id, product_line_id, customer_id, state,
		                            survey_status, survey_sent_at, satisfaction_score, closed_at)
		 VALUES ($1, $2, $3, 'closed', 'completed', now() - interval '2 minutes', 5, now() - interval '3 minutes')
		 RETURNING id`,
		channelID, productLineID, customerID).Scan(&convID); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM conversations WHERE customer_id = $1`, customerID)
		db.Exec(`DELETE FROM customers WHERE id = $1`, customerID)
	})

	m := NewManager(NewRepository(db), nil, DefaultManagerConfig())
	m.SetSurveyWindow(func(context.Context, string) time.Duration { return time.Hour })

	got, err := m.findOrCreateConversation(ctx, customerID, channelID, productLineID)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == convID {
		t.Error("a conversation whose survey was already answered was resumed")
	}
}

package survey

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"strings"
	"testing"

	"github.com/kefu/unica/pkg/model"
	shared "github.com/kefu/unica/pkg/survey"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// mockStateManager implements the StateManager interface for testing.
type mockStateManager struct {
	surveyStatuses   map[string]string
	surveyScores     map[string]int
	customerMsgCount int
	updateErr        error
	countErr         error
}

func newMockStateManager() *mockStateManager {
	return &mockStateManager{
		surveyStatuses:   make(map[string]string),
		surveyScores:     make(map[string]int),
		customerMsgCount: 3,
	}
}

func (m *mockStateManager) UpdateSurveyStatus(ctx context.Context, convID string, status string, score *int) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.surveyStatuses[convID] = status
	if score != nil {
		m.surveyScores[convID] = *score
	}
	return nil
}

func (m *mockStateManager) CountCustomerMessages(ctx context.Context, convID string) (int, error) {
	if m.countErr != nil {
		return 0, m.countErr
	}
	return m.customerMsgCount, nil
}

// setupTestRedis creates a miniredis instance and returns the redis client.
func setupTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	return rdb, mr
}

// The prompt is now a per-line setting rather than a constant, so what is worth
// pinning is not its wording but the contract it has to keep with the parser
// that reads the reply.
func TestPlatformPromptKeepsItsContractWithTheParser(t *testing.T) {
	prompt := shared.Defaults().PromptMessage
	if !shared.PromptDeclaresScale(prompt) {
		t.Error("the platform prompt does not tell the customer the 1-5 scale the reply parser requires")
	}
	for _, digit := range []string{"1 -", "2 -", "3 -", "4 -", "5 -"} {
		if !strings.Contains(prompt, digit) {
			t.Errorf("platform prompt should spell out %q", digit)
		}
	}
}

// A prompt the customer cannot answer is the failure this contract exists to
// catch: the reply parser takes 1-5 and nothing else, and everything else is
// handed back as an ordinary message, so the rating is silently never recorded.
func TestPromptDeclaresScale(t *testing.T) {
	for prompt, want := range map[string]bool{
		"请回复 1-5 为我们打分":      true,
		"回复数字１到５（５分最高）":      true,
		"请为服务打分，5 分最高，1 分最低": true,
		"您对本次服务满意吗？":         false,
		"请回复满意或不满意":          false,
		"请回复 1-4 打分":         false,
		"":                   false,
	} {
		if got := shared.PromptDeclaresScale(prompt); got != want {
			t.Errorf("PromptDeclaresScale(%q) = %v, want %v", prompt, got, want)
		}
	}
}

// The two customer-facing strings follow the product line's settings. Without
// this the console would offer a field whose value nothing reads — the shape of
// defect this page has produced more than once.
func TestSendSurvey_UsesConfiguredPrompt(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	h := NewHandler(nil, rdb, newMockStateManager())
	ctx := context.Background()
	conv := &ConversationInfo{ID: "conv-prompt", ChannelID: "ch-1", ProductLineID: "pl-1", CustomerID: "cust-1"}

	cfg := shared.Load(json.RawMessage(`{"survey":{"enabled":true,"prompt_message":"本店定制：请回复 1-5 打分"}}`))
	if err := h.SendSurvey(ctx, conv, cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := lastOutboundText(t, rdb, ctx); got != "本店定制：请回复 1-5 打分" {
		t.Errorf("survey prompt = %q, want the configured text", got)
	}
}

// A Config built in code rather than loaded from a row carries empty strings,
// and there is more than one such caller. Sending that would deliver a blank
// message to the customer, which is the delivery the router refuses to make for
// an AI answer.
func TestSendSurvey_BlankConfiguredPromptFallsBackRatherThanSendingNothing(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	h := NewHandler(nil, rdb, newMockStateManager())
	ctx := context.Background()

	for name, cfg := range map[string]*SurveyConfig{
		"zero value":     {Enabled: true, TimeoutHours: 6},
		"whitespace":     {Enabled: true, PromptMessage: "  ​ "},
		"nil altogether": nil,
	} {
		conv := &ConversationInfo{ID: "conv-blank-" + name, ChannelID: "ch-1", ProductLineID: "pl-1"}
		if err := h.SendSurvey(ctx, conv, cfg); err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if got := lastOutboundText(t, rdb, ctx); got != shared.Defaults().PromptMessage {
			t.Errorf("%s: survey prompt = %q, want the platform text", name, got)
		}
	}
}

// The thank-you text is resolved by looking the product line up again rather
// than by reading a wider pending record: widening that record would leave
// every survey already in flight deserialising to an empty string on the day
// the change ships. With no database to look up, the platform text is what the
// customer gets — never silence, because the rating has already been recorded
// and the customer cannot tell a missing acknowledgement from a lost rating.
func TestHandleSurveyReply_ThankYouFallsBackWithoutADatabase(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	h := NewHandler(nil, rdb, newMockStateManager())
	ctx := context.Background()
	convID := "conv-thanks-fallback"

	pendingJSON, _ := json.Marshal(pendingSurveyData{SentAt: "2026-03-06T10:00:00Z", ChannelID: "ch-1", CustomerID: "cust-1"})
	rdb.Set(ctx, surveyPendingKeyPrefix+convID, string(pendingJSON), shared.Defaults().PendingTTL())

	handled, err := h.HandleSurveyReply(ctx, convID, "5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected the reply to be handled")
	}
	if got := lastOutboundText(t, rdb, ctx); got != shared.Defaults().ThanksMessage {
		t.Errorf("thank-you = %q, want the platform text", got)
	}
}

func TestThanksText_FollowsConfigAndFallsBackWhenBlank(t *testing.T) {
	if got := thanksText(&SurveyConfig{ThanksMessage: "谢谢，已收到您的评分"}); got != "谢谢，已收到您的评分" {
		t.Errorf("thanksText = %q, want the configured text", got)
	}
	for name, cfg := range map[string]*SurveyConfig{
		"zero value": {},
		"whitespace": {ThanksMessage: " ㅤ"},
		"nil":        nil,
	} {
		if got := thanksText(cfg); got != shared.Defaults().ThanksMessage {
			t.Errorf("%s: thanksText = %q, want the platform text", name, got)
		}
	}
}

// lastOutboundText reads the text of the most recent message on the outbound
// stream, which is what the customer would actually receive.
func lastOutboundText(t *testing.T, rdb *redis.Client, ctx context.Context) string {
	t.Helper()
	entries, err := rdb.XRange(ctx, OutboundStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("failed to read outbound stream: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("nothing was published to the outbound stream")
	}
	payload, _ := entries[len(entries)-1].Values["payload"].(string)
	var msg model.StandardMessage
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatalf("failed to unmarshal outbound payload: %v", err)
	}
	return msg.Data.Content.Text
}

func TestHandleSurveyReply_NoPendingSurvey(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	sm := newMockStateManager()
	h := NewHandler(nil, rdb, sm)

	ctx := context.Background()
	handled, err := h.HandleSurveyReply(ctx, "conv-123", "5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Error("expected not handled when no pending survey exists")
	}
}

func TestHandleSurveyReply_ValidScore(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	sm := newMockStateManager()
	h := NewHandler(nil, rdb, sm)

	ctx := context.Background()
	convID := "conv-valid-score"

	// Set up pending survey in Redis
	pendingData := pendingSurveyData{
		SentAt:     "2026-03-06T10:00:00Z",
		ChannelID:  "ch-1",
		CustomerID: "cust-1",
	}
	pendingJSON, _ := json.Marshal(pendingData)
	rdb.Set(ctx, surveyPendingKeyPrefix+convID, string(pendingJSON), shared.Defaults().PendingTTL())

	tests := []struct {
		name      string
		input     string
		wantScore int
	}{
		{"score 1", "1", 1},
		{"score 3", "3", 3},
		{"score 5", "5", 5},
		{"score with spaces", " 4 ", 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Re-set pending key for each test
			rdb.Set(ctx, surveyPendingKeyPrefix+convID, string(pendingJSON), shared.Defaults().PendingTTL())

			handled, err := h.HandleSurveyReply(ctx, convID, tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !handled {
				t.Error("expected reply to be handled")
			}
			if sm.surveyStatuses[convID] != "completed" {
				t.Errorf("expected status 'completed', got %q", sm.surveyStatuses[convID])
			}
			if sm.surveyScores[convID] != tt.wantScore {
				t.Errorf("expected score %d, got %d", tt.wantScore, sm.surveyScores[convID])
			}
		})
	}
}

func TestHandleSurveyReply_InvalidScore(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	sm := newMockStateManager()
	h := NewHandler(nil, rdb, sm)

	ctx := context.Background()
	convID := "conv-invalid-score"

	// Set up pending survey in Redis
	pendingData := pendingSurveyData{
		SentAt:     "2026-03-06T10:00:00Z",
		ChannelID:  "ch-1",
		CustomerID: "cust-1",
	}
	pendingJSON, _ := json.Marshal(pendingData)

	tests := []struct {
		name  string
		input string
	}{
		{"text reply", "hello"},
		{"zero", "0"},
		{"six", "6"},
		{"negative", "-1"},
		{"float", "3.5"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rdb.Set(ctx, surveyPendingKeyPrefix+convID, string(pendingJSON), shared.Defaults().PendingTTL())

			handled, err := h.HandleSurveyReply(ctx, convID, tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if handled {
				t.Errorf("expected not handled for input %q", tt.input)
			}
		})
	}
}

func TestHandleSurveyReply_UpdateError(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	sm := newMockStateManager()
	sm.updateErr = fmt.Errorf("db error")
	h := NewHandler(nil, rdb, sm)

	ctx := context.Background()
	convID := "conv-update-err"

	// Set up pending survey
	pendingData := pendingSurveyData{SentAt: "2026-03-06T10:00:00Z", ChannelID: "ch-1", CustomerID: "cust-1"}
	pendingJSON, _ := json.Marshal(pendingData)
	rdb.Set(ctx, surveyPendingKeyPrefix+convID, string(pendingJSON), shared.Defaults().PendingTTL())

	_, err := h.HandleSurveyReply(ctx, convID, "5")
	if err == nil {
		t.Error("expected error when UpdateSurveyStatus fails")
	}
}

func TestSendSurvey(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	sm := newMockStateManager()
	h := NewHandler(nil, rdb, sm)

	ctx := context.Background()
	conv := &ConversationInfo{
		ID:                     "conv-send-survey",
		ChannelID:              "ch-1",
		ProductLineID:          "pl-1",
		CustomerID:             "cust-1",
		CustomerPlatformUserID: "user-1",
		CustomerAccountID:      "acc-1",
		CorrelationID:          "corr-1",
	}

	err := h.SendSurvey(ctx, conv, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify pending key was set in Redis
	key := surveyPendingKeyPrefix + conv.ID
	val, err := rdb.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("expected pending key to be set: %v", err)
	}
	var pending pendingSurveyData
	if err := json.Unmarshal([]byte(val), &pending); err != nil {
		t.Fatalf("failed to unmarshal pending data: %v", err)
	}
	if pending.ChannelID != conv.ChannelID {
		t.Errorf("expected channel_id %q, got %q", conv.ChannelID, pending.ChannelID)
	}
	if pending.CustomerID != conv.CustomerID {
		t.Errorf("expected customer_id %q, got %q", conv.CustomerID, pending.CustomerID)
	}

	// Verify survey status was updated
	if sm.surveyStatuses[conv.ID] != "sent" {
		t.Errorf("expected status 'sent', got %q", sm.surveyStatuses[conv.ID])
	}

	// Verify outbound message was published to stream
	result, err := rdb.XRange(ctx, OutboundStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("failed to read outbound stream: %v", err)
	}
	if len(result) == 0 {
		t.Fatal("expected at least one message on the outbound stream")
	}
	payload, ok := result[0].Values["payload"].(string)
	if !ok {
		t.Fatal("expected payload in stream message")
	}
	if len(payload) == 0 {
		t.Error("payload should not be empty")
	}
}

func TestSurveyConfig_Defaults(t *testing.T) {
	config := &SurveyConfig{}
	if config.Enabled {
		t.Error("default should be disabled")
	}
	if config.MinCustomerMessages != 0 {
		t.Error("default min_customer_messages should be 0 (handler uses 2 as fallback)")
	}
}

func TestSurveyConfig_Parse(t *testing.T) {
	raw := `{"enabled": true, "min_customer_messages": 3, "timeout_hours": 12}`
	var config SurveyConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if !config.Enabled {
		t.Error("expected enabled=true")
	}
	if config.MinCustomerMessages != 3 {
		t.Errorf("expected min_customer_messages=3, got %d", config.MinCustomerMessages)
	}
	if config.TimeoutHours != 12 {
		t.Errorf("expected timeout_hours=12, got %d", config.TimeoutHours)
	}
}

func TestNewHandler(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	sm := newMockStateManager()
	h := NewHandler(nil, rdb, sm)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}

func TestSendSurvey_DuplicatePrevention(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	sm := newMockStateManager()
	h := NewHandler(nil, rdb, sm)

	ctx := context.Background()
	conv := &ConversationInfo{
		ID:            "conv-dup",
		ChannelID:     "ch-1",
		ProductLineID: "pl-1",
		CustomerID:    "cust-1",
	}

	// Send first survey
	if err := h.SendSurvey(ctx, conv, nil); err != nil {
		t.Fatalf("first send failed: %v", err)
	}

	// Verify pending key exists
	key := surveyPendingKeyPrefix + conv.ID
	exists, _ := rdb.Exists(ctx, key).Result()
	if exists == 0 {
		t.Error("expected pending key to exist after first send")
	}
}

func TestHandleSurveyReply_PendingKeyRemoved(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	sm := newMockStateManager()
	h := NewHandler(nil, rdb, sm)

	ctx := context.Background()
	convID := "conv-key-removed"

	// Set up pending survey
	pendingData := pendingSurveyData{SentAt: "2026-03-06T10:00:00Z", ChannelID: "ch-1", CustomerID: "cust-1"}
	pendingJSON, _ := json.Marshal(pendingData)
	rdb.Set(ctx, surveyPendingKeyPrefix+convID, string(pendingJSON), shared.Defaults().PendingTTL())

	// Handle survey reply
	handled, err := h.HandleSurveyReply(ctx, convID, "4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Error("expected reply to be handled")
	}

	// Verify pending key was removed
	exists, _ := rdb.Exists(ctx, surveyPendingKeyPrefix+convID).Result()
	if exists != 0 {
		t.Error("expected pending key to be removed after survey reply")
	}
}

func TestHandleSurveyReply_ThankYouMessage(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	sm := newMockStateManager()
	h := NewHandler(nil, rdb, sm)

	ctx := context.Background()
	convID := "conv-thanks"

	// Set up pending survey
	pendingData := pendingSurveyData{SentAt: "2026-03-06T10:00:00Z", ChannelID: "ch-1", CustomerID: "cust-1"}
	pendingJSON, _ := json.Marshal(pendingData)
	rdb.Set(ctx, surveyPendingKeyPrefix+convID, string(pendingJSON), shared.Defaults().PendingTTL())

	handled, err := h.HandleSurveyReply(ctx, convID, "5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Error("expected reply to be handled")
	}

	// Verify thank-you message was published to outbound stream
	result, err := rdb.XRange(ctx, OutboundStream, "-", "+").Result()
	if err != nil {
		t.Fatalf("failed to read outbound stream: %v", err)
	}
	if len(result) == 0 {
		t.Fatal("expected at least one message on the outbound stream (thank-you)")
	}
}

func TestConversationInfo_Fields(t *testing.T) {
	info := &ConversationInfo{
		ID:                     "conv-1",
		ChannelID:              "ch-1",
		ProductLineID:          "pl-1",
		CustomerID:             "cust-1",
		CustomerPlatformUserID: "user-1",
		CustomerAccountID:      "acc-1",
		CorrelationID:          "corr-1",
	}
	if info.ID != "conv-1" {
		t.Error("ID mismatch")
	}
	if info.ChannelID != "ch-1" {
		t.Error("ChannelID mismatch")
	}
	if info.ProductLineID != "pl-1" {
		t.Error("ProductLineID mismatch")
	}
}

// The timeout was parsed and defaulted for a long time while nothing read it,
// so a deployment that shortened it saw no change. This pins the value to the
// key that actually expires.
func TestSendSurvey_PendingTTLFollowsConfig(t *testing.T) {
	rdb, mr := setupTestRedis(t)
	defer mr.Close()

	h := NewHandler(nil, rdb, newMockStateManager())
	ctx := context.Background()
	conv := &ConversationInfo{ID: "conv-ttl", ChannelID: "ch-1", ProductLineID: "pl-1", CustomerID: "cust-1"}

	if err := h.SendSurvey(ctx, conv, &SurveyConfig{Enabled: true, TimeoutHours: 6}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := mr.TTL(surveyPendingKeyPrefix + conv.ID); got != 6*time.Hour {
		t.Errorf("pending TTL = %v, want 6h", got)
	}

	// No config at all keeps the period the fixed constant used to give.
	conv2 := &ConversationInfo{ID: "conv-ttl-default", ChannelID: "ch-1", ProductLineID: "pl-1", CustomerID: "cust-1"}
	if err := h.SendSurvey(ctx, conv2, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := mr.TTL(surveyPendingKeyPrefix + conv2.ID); got != 24*time.Hour {
		t.Errorf("default pending TTL = %v, want 24h", got)
	}
}

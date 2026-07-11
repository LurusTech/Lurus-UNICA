// Package survey implements post-conversation satisfaction survey logic.
package survey

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/router/internal/metrics"
)

const (
	// surveyPendingKeyPrefix is the Redis key prefix for pending survey tracking.
	surveyPendingKeyPrefix = "survey:pending:"
	// surveyPendingTTL is the TTL for pending survey keys.
	surveyPendingTTL = 24 * time.Hour
	// OutboundStream is the Redis Stream key for outbound messages.
	OutboundStream = "unica:outbound"
)

// surveyMessage is the template sent to customers after conversation closure.
const surveyMessage = "感谢您的咨询！请为本次服务评分：\n" +
	"回复数字 1-5（5分最高）\n" +
	"1 - 非常不满意\n" +
	"2 - 不满意\n" +
	"3 - 一般\n" +
	"4 - 满意\n" +
	"5 - 非常满意"

// surveyThanksMessage is sent after a valid survey reply is received.
const surveyThanksMessage = "感谢您的评价！我们会继续努力提升服务质量。"

// SurveyConfig holds survey configuration parsed from product_line config_json.
type SurveyConfig struct {
	Enabled            bool `json:"enabled"`
	MinCustomerMessages int  `json:"min_customer_messages"`
	TimeoutHours       int  `json:"timeout_hours"`
}

// pendingSurveyData is stored in Redis to track pending surveys.
type pendingSurveyData struct {
	SentAt     string `json:"sent_at"`
	ChannelID  string `json:"channel_id"`
	CustomerID string `json:"customer_id"`
}

// StateManager defines the interface for state management operations needed by survey handler.
type StateManager interface {
	UpdateSurveyStatus(ctx context.Context, convID string, status string, score *int) error
	CountCustomerMessages(ctx context.Context, convID string) (int, error)
}

// ConversationInfo holds the conversation data needed by the survey handler.
type ConversationInfo struct {
	ID            string
	ChannelID     string
	ProductLineID string
	CustomerID    string
	CustomerPlatformUserID string
	CustomerAccountID      string
	CorrelationID string
}

// Handler manages satisfaction survey lifecycle.
type Handler struct {
	db  *sql.DB
	rdb *redis.Client
	sm  StateManager
}

// NewHandler creates a new survey Handler.
func NewHandler(db *sql.DB, rdb *redis.Client, sm StateManager) *Handler {
	return &Handler{
		db:  db,
		rdb: rdb,
		sm:  sm,
	}
}

// ShouldSendSurvey checks if a survey should be sent for the closing conversation.
// Returns true if: survey enabled for product line, conv has >= min customer messages, survey not already sent.
func (h *Handler) ShouldSendSurvey(ctx context.Context, convID, productLineID string) (bool, error) {
	// Load survey config from product_lines
	config, err := h.loadSurveyConfig(ctx, productLineID)
	if err != nil {
		return false, fmt.Errorf("load survey config: %w", err)
	}
	if !config.Enabled {
		return false, nil
	}

	// Check if survey was already sent
	key := surveyPendingKeyPrefix + convID
	exists, err := h.rdb.Exists(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("check pending survey: %w", err)
	}
	if exists > 0 {
		return false, nil
	}

	// Check minimum customer messages
	minMsgs := config.MinCustomerMessages
	if minMsgs <= 0 {
		minMsgs = 2
	}
	count, err := h.sm.CountCustomerMessages(ctx, convID)
	if err != nil {
		return false, fmt.Errorf("count customer messages: %w", err)
	}
	if count < minMsgs {
		return false, nil
	}

	return true, nil
}

// SendSurvey creates the survey outbound message and publishes to unica:outbound.
// Also sets survey_sent_at and survey_status='sent' in DB.
// Stores Redis key survey:pending:{conversation_id} with TTL=24h.
func (h *Handler) SendSurvey(ctx context.Context, conv *ConversationInfo) error {
	// Build outbound survey message
	outMsg := model.StandardMessage{
		ID:      uuid.New().String(),
		Type:    model.MessageTypeOutbound,
		Source:  "router",
		Subject: "conversation:" + conv.ID,
		Time:    time.Now(),
		Data: model.MessageData{
			ConversationID: conv.ID,
			ChannelID:      conv.ChannelID,
			ProductLineID:  conv.ProductLineID,
			CustomerID:     conv.CustomerID,
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: surveyMessage,
			},
			PlatformMeta: model.PlatformMeta{
				PlatformUserID: conv.CustomerPlatformUserID,
				AccountID:      conv.CustomerAccountID,
			},
			CorrelationID: conv.CorrelationID,
		},
	}

	outJSON, err := json.Marshal(outMsg)
	if err != nil {
		return fmt.Errorf("marshal survey message: %w", err)
	}

	// Publish to outbound stream
	_, err = h.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: OutboundStream,
		Values: map[string]interface{}{
			"payload":        string(outJSON),
			"type":           outMsg.Type,
			"source":         "router",
			"correlation_id": conv.CorrelationID,
		},
	}).Result()
	if err != nil {
		return fmt.Errorf("publish survey message: %w", err)
	}

	// Store pending survey data in Redis
	pendingData := pendingSurveyData{
		SentAt:     time.Now().UTC().Format(time.RFC3339),
		ChannelID:  conv.ChannelID,
		CustomerID: conv.CustomerID,
	}
	pendingJSON, _ := json.Marshal(pendingData)
	key := surveyPendingKeyPrefix + conv.ID
	if err := h.rdb.Set(ctx, key, string(pendingJSON), surveyPendingTTL).Err(); err != nil {
		log.Printf("[survey] warning: failed to set pending survey key for %s: %v", conv.ID, err)
	}

	// Update DB survey status
	if err := h.sm.UpdateSurveyStatus(ctx, conv.ID, "sent", nil); err != nil {
		log.Printf("[survey] warning: failed to update survey status for %s: %v", conv.ID, err)
	}

	metrics.SurveySentTotal.WithLabelValues(conv.ProductLineID).Inc()
	log.Printf("[survey] survey sent for conversation %s", conv.ID)
	return nil
}

// HandleSurveyReply processes a potential survey reply message.
// Checks if survey:pending:{conversation_id} exists.
// Parses message as 1-5 rating. Stores in DB. Returns true if handled as survey.
func (h *Handler) HandleSurveyReply(ctx context.Context, convID, messageText string) (bool, error) {
	// Check if there's a pending survey
	key := surveyPendingKeyPrefix + convID
	val, err := h.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get pending survey: %w", err)
	}

	// Parse the message as a rating (1-5)
	text := strings.TrimSpace(messageText)
	score, err := strconv.Atoi(text)
	if err != nil || score < 1 || score > 5 {
		// Not a valid survey reply, let the caller decide (reopen conversation)
		return false, nil
	}

	// Valid survey reply - store score in DB
	if err := h.sm.UpdateSurveyStatus(ctx, convID, "completed", &score); err != nil {
		return false, fmt.Errorf("update survey score: %w", err)
	}

	// Remove pending key
	if err := h.rdb.Del(ctx, key).Err(); err != nil {
		log.Printf("[survey] warning: failed to delete pending survey key for %s: %v", convID, err)
	}

	// Extract product line for metrics from the pending data
	var pending pendingSurveyData
	if json.Unmarshal([]byte(val), &pending) == nil {
		// Get product line from conversation for metrics
		productLineID := h.getProductLineID(ctx, convID)
		metrics.SurveyCompletedTotal.WithLabelValues(productLineID, strconv.Itoa(score)).Inc()
	}

	// Send thank-you message
	h.sendThankYouMessage(ctx, convID, val)

	log.Printf("[survey] survey completed for conversation %s (score=%d)", convID, score)
	return true, nil
}

// loadSurveyConfig reads survey configuration from product_lines.config_json.
func (h *Handler) loadSurveyConfig(ctx context.Context, productLineID string) (*SurveyConfig, error) {
	var configJSONStr sql.NullString
	err := h.db.QueryRowContext(ctx,
		`SELECT config_json FROM product_lines WHERE id = $1`, productLineID,
	).Scan(&configJSONStr)
	if err != nil {
		return nil, fmt.Errorf("query product line config: %w", err)
	}

	config := &SurveyConfig{
		Enabled:            false,
		MinCustomerMessages: 2,
		TimeoutHours:       24,
	}

	if !configJSONStr.Valid || configJSONStr.String == "" {
		return config, nil
	}

	// Parse the full config JSON and extract the "survey" block
	var fullConfig map[string]json.RawMessage
	if err := json.Unmarshal([]byte(configJSONStr.String), &fullConfig); err != nil {
		return config, nil
	}

	surveyJSON, ok := fullConfig["survey"]
	if !ok {
		return config, nil
	}

	if err := json.Unmarshal(surveyJSON, config); err != nil {
		return config, nil
	}

	return config, nil
}

// getProductLineID retrieves the product line ID for a conversation.
func (h *Handler) getProductLineID(ctx context.Context, convID string) string {
	if h.db == nil {
		return "unknown"
	}
	var plID string
	err := h.db.QueryRowContext(ctx,
		`SELECT product_line_id FROM conversations WHERE id = $1`, convID,
	).Scan(&plID)
	if err != nil {
		return "unknown"
	}
	return plID
}

// sendThankYouMessage sends a thank-you response after survey completion.
func (h *Handler) sendThankYouMessage(ctx context.Context, convID, pendingDataJSON string) {
	var pending pendingSurveyData
	if err := json.Unmarshal([]byte(pendingDataJSON), &pending); err != nil {
		log.Printf("[survey] warning: failed to parse pending data for thank-you: %v", err)
		return
	}

	outMsg := model.StandardMessage{
		ID:      uuid.New().String(),
		Type:    model.MessageTypeOutbound,
		Source:  "router",
		Subject: "conversation:" + convID,
		Time:    time.Now(),
		Data: model.MessageData{
			ConversationID: convID,
			ChannelID:      pending.ChannelID,
			CustomerID:     pending.CustomerID,
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: surveyThanksMessage,
			},
		},
	}

	outJSON, err := json.Marshal(outMsg)
	if err != nil {
		log.Printf("[survey] warning: failed to marshal thank-you message: %v", err)
		return
	}

	_, err = h.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: OutboundStream,
		Values: map[string]interface{}{
			"payload": string(outJSON),
			"type":    outMsg.Type,
			"source":  "router",
		},
	}).Result()
	if err != nil {
		log.Printf("[survey] warning: failed to publish thank-you message: %v", err)
	}
}

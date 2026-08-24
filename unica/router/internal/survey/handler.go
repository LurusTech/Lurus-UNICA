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

	"github.com/kefu/unica/pkg/domain"
	"github.com/kefu/unica/pkg/model"
	shared "github.com/kefu/unica/pkg/survey"
	"github.com/kefu/unica/router/internal/metrics"
)

const (
	// surveyPendingKeyPrefix is the Redis key prefix for pending survey tracking.
	surveyPendingKeyPrefix = "survey:pending:"
	// OutboundStream is the Redis Stream key for outbound messages.
	OutboundStream = "unica:outbound"
)

// SurveyConfig is a product line's survey settings. It is an alias of the
// shared definition, not a copy: the admin console reads and writes the same
// block, and it persists what it displays.
type SurveyConfig = shared.Config

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
	ID                     string
	ChannelID              string
	ProductLineID          string
	CustomerID             string
	CustomerPlatformUserID string
	CustomerAccountID      string
	CorrelationID          string
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
//
// The settings it loaded are returned alongside the verdict so the send that
// follows can use them without reading the row again — and, more to the point,
// without SendSurvey needing a database at all. They are nil whenever the
// verdict is false.
func (h *Handler) ShouldSendSurvey(ctx context.Context, convID, productLineID string) (bool, *SurveyConfig, error) {
	// Load survey config from product_lines
	config, err := h.loadSurveyConfig(ctx, productLineID)
	if err != nil {
		return false, nil, fmt.Errorf("load survey config: %w", err)
	}
	if !config.Enabled {
		return false, nil, nil
	}

	// Check if survey was already sent
	key := surveyPendingKeyPrefix + convID
	exists, err := h.rdb.Exists(ctx, key).Result()
	if err != nil {
		return false, nil, fmt.Errorf("check pending survey: %w", err)
	}
	if exists > 0 {
		return false, nil, nil
	}

	// No clamp on the threshold here: Load back-fills a non-positive value from
	// the shared defaults, and repeating the number would put it in two places.
	minMsgs := config.MinCustomerMessages
	count, err := h.sm.CountCustomerMessages(ctx, convID)
	if err != nil {
		return false, nil, fmt.Errorf("count customer messages: %w", err)
	}
	if count < minMsgs {
		return false, nil, nil
	}

	return true, config, nil
}

// SendSurvey creates the survey outbound message and publishes to unica:outbound.
// Also sets survey_sent_at and survey_status='sent' in DB.
// Stores Redis key survey:pending:{conversation_id}, whose TTL is the product
// line's configured timeout rather than a fixed period — the setting existed
// and was parsed for a long time without anything reading it, so a deployment
// that changed it saw nothing happen.
//
// cfg is what ShouldSendSurvey already loaded; nil falls back to the platform
// defaults so a caller that has none is given the previous fixed behaviour
// rather than an immediate expiry.
func (h *Handler) SendSurvey(ctx context.Context, conv *ConversationInfo, cfg *SurveyConfig) error {
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
				Text: promptText(cfg),
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
	ttl := shared.Defaults().PendingTTL()
	if cfg != nil {
		ttl = cfg.PendingTTL()
	}
	if err := h.rdb.Set(ctx, key, string(pendingJSON), ttl).Err(); err != nil {
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

// PendingWindow reports how long a survey sent for this product line stays
// answerable. It is the same period the pending record is given, read from the
// same settings, so the conversation stops being reachable at the moment the
// record it would be matched against expires.
//
// A product line whose settings cannot be read yields zero rather than a guess:
// zero leaves conversation lookup exactly as it was before surveys existed.
func (h *Handler) PendingWindow(ctx context.Context, productLineID string) time.Duration {
	if h.db == nil || productLineID == "" {
		return 0
	}
	cfg, err := h.loadSurveyConfig(ctx, productLineID)
	if err != nil {
		log.Printf("[survey] warning: failed to load config for pending window on %s: %v", productLineID, err)
		return 0
	}
	if !cfg.Enabled {
		return 0
	}
	return cfg.PendingTTL()
}

// promptText and thanksText resolve the two customer-facing strings from a
// product line's settings.
//
// Both fall back to the platform text on a nil config and on a blank one, and
// the second case is the one that matters: shared.Load back-fills a blank
// value, but a Config assembled in code rather than loaded from a row carries
// empty strings, and there is more than one such caller. Publishing that would
// send the customer an empty message — the exact delivery the router now
// refuses to make for an AI answer, and there is no reason a survey should be
// held to a lower standard.
func promptText(cfg *SurveyConfig) string {
	if cfg == nil || domain.IsBlankAnswer(cfg.PromptMessage) {
		return shared.Defaults().PromptMessage
	}
	return cfg.PromptMessage
}

func thanksText(cfg *SurveyConfig) string {
	if cfg == nil || domain.IsBlankAnswer(cfg.ThanksMessage) {
		return shared.Defaults().ThanksMessage
	}
	return cfg.ThanksMessage
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

	// The product line is resolved from the conversation rather than read out of
	// the pending record. The record was written when the survey was sent, and
	// widening it would leave every survey already in flight deserialising to an
	// empty string on the day this ships — which is exactly the blank message
	// the router now refuses to deliver.
	productLineID := h.getProductLineID(ctx, convID)
	metrics.SurveyCompletedTotal.WithLabelValues(productLineID, strconv.Itoa(score)).Inc()

	// Send thank-you message
	h.sendThankYouMessage(ctx, convID, val, h.thankYouConfig(ctx, productLineID))

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

	// Read straight from the row on every call: unlike the guardrail block,
	// which the router caches with the channel route, these settings have no
	// cached copy, so a console write is met by the next conversation without
	// an invalidation step.
	return shared.Load(json.RawMessage(configJSONStr.String)), nil
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

// thankYouConfig loads the settings behind the thank-you message, degrading to
// the platform text rather than to silence: the rating has already been
// recorded at this point, and a customer who rated and heard nothing back has
// no way to tell that from a rating that was lost.
func (h *Handler) thankYouConfig(ctx context.Context, productLineID string) *SurveyConfig {
	if h.db == nil || productLineID == "" || productLineID == "unknown" {
		return nil
	}
	cfg, err := h.loadSurveyConfig(ctx, productLineID)
	if err != nil {
		log.Printf("[survey] warning: failed to load config for thank-you on %s: %v", productLineID, err)
		return nil
	}
	return cfg
}

// sendThankYouMessage sends a thank-you response after survey completion.
func (h *Handler) sendThankYouMessage(ctx context.Context, convID, pendingDataJSON string, cfg *SurveyConfig) {
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
				Text: thanksText(cfg),
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

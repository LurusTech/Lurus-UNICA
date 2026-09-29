// Package webhook provides HTTP webhook handlers for external service integrations.
package webhook

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/gateway/internal/metrics"
	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/pkg/platformsettings"
)

const (
	// Redis key prefixes for Chatwoot conversation mapping.
	cwConvKeyPrefix   = "cw_conv:"       // cw_conv:{chatwoot_conv_id} -> unica_conv_id
	unicaConvCWPrefix = "unica_conv_cw:" // unica_conv_cw:{unica_conv_id} -> chatwoot_conv_id

	// OutboundStream is the Redis Stream key for outbound messages.
	outboundStream = "unica:outbound"
)

// ChatwootWebhookConfig holds configuration for the Chatwoot webhook handler.
type ChatwootWebhookConfig struct {
	WebhookToken string // Token for verifying webhook requests
}

// ChatwootWebhookHandler handles incoming webhook events from Chatwoot.
type ChatwootWebhookHandler struct {
	rdb    *redis.Client
	db     *sql.DB // optional; when set, first agent reply timestamps are recorded
	config ChatwootWebhookConfig

	// capture and replies keep agent replies as reference answers for the
	// ontology evolution loop, only while evolve_capture is on. Both are set
	// by SetDB; without a database there is nowhere to keep them.
	capture captureFlag
	replies agentReplyRecorder
}

// captureFlag reports whether evolve_capture is on.
type captureFlag interface {
	On(ctx context.Context) bool
}

// agentReplyRecorder stores one human agent reply.
type agentReplyRecorder interface {
	RecordAgentReply(ctx context.Context, reply agentReply) error
}

// agentReply is a human agent's reply as stored in messages.
type agentReply struct {
	ConversationID string
	Content        model.MessageContent
	PlatformMsgID  string
	CorrelationID  string
}

// NewChatwootWebhookHandler creates a new Chatwoot webhook handler.
func NewChatwootWebhookHandler(rdb *redis.Client, config ChatwootWebhookConfig) *ChatwootWebhookHandler {
	return &ChatwootWebhookHandler{
		rdb:    rdb,
		config: config,
	}
}

// SetDB attaches an optional database connection used to stamp
// first_agent_reply_at for reporter response-time metrics. When nil, that
// recording is skipped.
func (h *ChatwootWebhookHandler) SetDB(db *sql.DB) {
	h.db = db
	if db != nil {
		h.capture = platformsettings.NewFlag(platformsettings.NewStore(db),
			platformsettings.KeyEvolveCapture, platformsettings.DefaultFlagTTL)
		h.replies = sqlReplyRecorder{db: db}
	}
}

// sqlReplyRecorder writes agent replies into messages.
type sqlReplyRecorder struct{ db *sql.DB }

// RecordAgentReply inserts the reply with sender_type 'human'. platform_msg_id
// is the Chatwoot message id, so a webhook redelivered by Chatwoot lands on
// the same partition's dedup index and is skipped rather than stored twice.
func (s sqlReplyRecorder) RecordAgentReply(ctx context.Context, reply agentReply) error {
	content, err := json.Marshal(reply.Content)
	if err != nil {
		return fmt.Errorf("marshal agent reply: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO messages (conversation_id, direction, sender_type, content_json, platform_msg_id, correlation_id)
		 VALUES ($1, 'outbound', 'human', $2, $3, $4)
		 ON CONFLICT DO NOTHING`,
		reply.ConversationID, content, reply.PlatformMsgID, reply.CorrelationID)
	if err != nil {
		return fmt.Errorf("insert agent reply: %w", err)
	}
	return nil
}

// cwWebhookPayload represents the top-level Chatwoot webhook event payload.
type cwWebhookPayload struct {
	Event string `json:"event"`

	// Fields for message_created events
	ID          int    `json:"id,omitempty"`
	Content     string `json:"content,omitempty"`
	MessageType int    `json:"message_type,omitempty"` // 0=incoming, 1=outgoing, 2=activity
	Private     bool   `json:"private,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Account     struct {
		ID int `json:"id"`
	} `json:"account,omitempty"`
	Conversation struct {
		ID            int    `json:"id"`
		Status        string `json:"status,omitempty"`
		DisplayID     int    `json:"display_id,omitempty"`
		CustomAttr    map[string]string `json:"custom_attributes,omitempty"`
	} `json:"conversation,omitempty"`
	Sender struct {
		ID   int    `json:"id"`
		Name string `json:"name,omitempty"`
		Type string `json:"type,omitempty"` // "user" for agent, "contact" for customer
	} `json:"sender,omitempty"`

	// Fields for conversation_status_changed events
	// Re-uses Conversation field above
}

// ServeHTTP handles incoming Chatwoot webhook HTTP requests.
func (h *ChatwootWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify webhook token
	token := r.Header.Get("X-Chatwoot-Webhook-Token")
	if h.config.WebhookToken != "" &&
		subtle.ConstantTimeCompare([]byte(token), []byte(h.config.WebhookToken)) != 1 {
		log.Printf("[chatwoot-webhook] invalid webhook token received")
		metrics.ChatwootWebhookErrorsTotal.WithLabelValues("auth_failed").Inc()
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[chatwoot-webhook] failed to read request body: %v", err)
		metrics.ChatwootWebhookErrorsTotal.WithLabelValues("read_body").Inc()
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Parse webhook payload
	var payload cwWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Printf("[chatwoot-webhook] failed to unmarshal payload: %v", err)
		metrics.ChatwootWebhookErrorsTotal.WithLabelValues("parse_payload").Inc()
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	metrics.ChatwootWebhookReceivedTotal.WithLabelValues(payload.Event).Inc()
	ctx := r.Context()

	switch payload.Event {
	case "message_created":
		if err := h.handleMessageCreated(ctx, &payload); err != nil {
			log.Printf("[chatwoot-webhook] error handling message_created: %v", err)
			metrics.ChatwootWebhookErrorsTotal.WithLabelValues("message_created").Inc()
			// Return 200 to prevent Chatwoot from retrying
		}

	case "conversation_status_changed":
		if err := h.handleConversationStatusChanged(ctx, &payload); err != nil {
			log.Printf("[chatwoot-webhook] error handling conversation_status_changed: %v", err)
			metrics.ChatwootWebhookErrorsTotal.WithLabelValues("conversation_status_changed").Inc()
		}

	default:
		log.Printf("[chatwoot-webhook] ignoring event type: %s", payload.Event)
	}

	// Always return 200 to prevent webhook retries
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// handleMessageCreated processes a message_created webhook event.
// Only outgoing (agent) messages that are not private are forwarded.
func (h *ChatwootWebhookHandler) handleMessageCreated(ctx context.Context, payload *cwWebhookPayload) error {
	// Filter: only process outgoing messages (message_type=1) from agents
	if payload.MessageType != 1 {
		log.Printf("[chatwoot-webhook] skipping non-outgoing message (type=%d)", payload.MessageType)
		return nil
	}

	// Skip private (internal) notes
	if payload.Private {
		log.Printf("[chatwoot-webhook] skipping private message %d", payload.ID)
		return nil
	}

	// Skip if sender is a contact (customer), only process agent messages
	if payload.Sender.Type == "contact" {
		log.Printf("[chatwoot-webhook] skipping contact message %d", payload.ID)
		return nil
	}

	// Look up UNICA conversation from Chatwoot conversation ID
	cwConvIDStr := strconv.Itoa(payload.Conversation.ID)
	unicaConvID, err := h.rdb.Get(ctx, cwConvKeyPrefix+cwConvIDStr).Result()
	if err != nil {
		return fmt.Errorf("lookup unica conversation for cw conv %d: %w", payload.Conversation.ID, err)
	}

	if payload.Content == "" {
		log.Printf("[chatwoot-webhook] skipping empty content message %d", payload.ID)
		return nil
	}

	// Build outbound StandardMessage for agent reply
	outMsg := model.StandardMessage{
		ID:      uuid.New().String(),
		Type:    model.MessageTypeOutbound,
		Source:  "chatwoot",
		Subject: "conversation:" + unicaConvID,
		Time:    time.Now(),
		Data: model.MessageData{
			ConversationID: unicaConvID,
			Content: model.MessageContent{
				Type: model.ContentTypeText,
				Text: payload.Content,
			},
			PlatformMsgID: fmt.Sprintf("cw-%d", payload.ID),
			CorrelationID: uuid.New().String(),
		},
	}

	// Try to enrich with channel/customer info from custom_attributes
	if payload.Conversation.CustomAttr != nil {
		if chID, ok := payload.Conversation.CustomAttr["channel_id"]; ok {
			outMsg.Data.ChannelID = chID
		}
	}

	// Publish to unica:outbound stream
	outJSON, err := json.Marshal(outMsg)
	if err != nil {
		return fmt.Errorf("marshal outbound message: %w", err)
	}

	_, err = h.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: outboundStream,
		Values: map[string]interface{}{
			"payload":        string(outJSON),
			"type":           outMsg.Type,
			"source":         "chatwoot",
			"correlation_id": outMsg.Data.CorrelationID,
		},
	}).Result()
	if err != nil {
		return fmt.Errorf("publish to outbound stream: %w", err)
	}

	metrics.ChatwootAgentRepliesTotal.WithLabelValues(outMsg.Data.ProductLineID).Inc()
	log.Printf("[chatwoot-webhook] agent reply published to outbound stream (cw_conv=%d, unica_conv=%s, msg_id=%s)",
		payload.Conversation.ID, unicaConvID, outMsg.ID)

	// Record the first human-agent reply time for reporter response-time metrics.
	// The WHERE guard makes this a no-op after the first reply, so it is safe to
	// run on every agent message.
	if h.db != nil {
		if _, err := h.db.ExecContext(ctx,
			`UPDATE conversations SET first_agent_reply_at = NOW(), updated_at = NOW()
			 WHERE id = $1 AND first_agent_reply_at IS NULL`,
			unicaConvID,
		); err != nil {
			log.Printf("[chatwoot-webhook] warning: failed to record first agent reply for %s: %v", unicaConvID, err)
		}
	}

	h.captureAgentReply(ctx, outMsg)
	return nil
}

// captureAgentReply keeps the reply as the reference answer the evolution loop
// compares the AI against, while evolve_capture is on. It runs after the reply
// has been forwarded and never fails the webhook: this is observation, and a
// customer's reply must not wait on it.
//
// Replies arriving after the Chatwoot-to-UNICA mapping has expired (72h) never
// get here — the lookup above fails, is logged, and the reply is dropped, as
// it always has been.
func (h *ChatwootWebhookHandler) captureAgentReply(ctx context.Context, outMsg model.StandardMessage) {
	if h.replies == nil || h.capture == nil || !h.capture.On(ctx) {
		return
	}
	err := h.replies.RecordAgentReply(ctx, agentReply{
		ConversationID: outMsg.Data.ConversationID,
		Content:        outMsg.Data.Content,
		PlatformMsgID:  outMsg.Data.PlatformMsgID,
		CorrelationID:  outMsg.Data.CorrelationID,
	})
	if err != nil {
		log.Printf("[chatwoot-webhook] warning: failed to capture agent reply for %s: %v",
			outMsg.Data.ConversationID, err)
	}
}

// handleConversationStatusChanged processes conversation status change events.
// When a Chatwoot conversation is resolved, the corresponding UNICA conversation is closed.
func (h *ChatwootWebhookHandler) handleConversationStatusChanged(ctx context.Context, payload *cwWebhookPayload) error {
	status := payload.Conversation.Status
	if status != "resolved" {
		log.Printf("[chatwoot-webhook] ignoring conversation status change to %s (conv=%d)", status, payload.Conversation.ID)
		return nil
	}

	// Look up UNICA conversation
	cwConvIDStr := strconv.Itoa(payload.Conversation.ID)
	unicaConvID, err := h.rdb.Get(ctx, cwConvKeyPrefix+cwConvIDStr).Result()
	if err != nil {
		return fmt.Errorf("lookup unica conversation for resolved cw conv %d: %w", payload.Conversation.ID, err)
	}

	// Publish a close event to the outbound stream so the router can handle it
	closeEvent := model.StandardMessage{
		ID:      uuid.New().String(),
		Type:    "event.conversation_closed",
		Source:  "chatwoot",
		Subject: "conversation:" + unicaConvID,
		Time:    time.Now(),
		Data: model.MessageData{
			ConversationID: unicaConvID,
			Content: model.MessageContent{
				Type:  model.ContentTypeEvent,
				Event: "conversation_resolved",
			},
			CorrelationID: uuid.New().String(),
		},
	}

	eventJSON, err := json.Marshal(closeEvent)
	if err != nil {
		return fmt.Errorf("marshal close event: %w", err)
	}

	_, err = h.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: outboundStream,
		Values: map[string]interface{}{
			"payload":        string(eventJSON),
			"type":           closeEvent.Type,
			"source":         "chatwoot",
			"correlation_id": closeEvent.Data.CorrelationID,
		},
	}).Result()
	if err != nil {
		return fmt.Errorf("publish close event: %w", err)
	}

	log.Printf("[chatwoot-webhook] conversation resolved event published (cw_conv=%d, unica_conv=%s)",
		payload.Conversation.ID, unicaConvID)

	return nil
}

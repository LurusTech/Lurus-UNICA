// Package webhook provides HTTP webhook handlers for external service integrations.
package webhook

import (
	"context"
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
	config ChatwootWebhookConfig
}

// NewChatwootWebhookHandler creates a new Chatwoot webhook handler.
func NewChatwootWebhookHandler(rdb *redis.Client, config ChatwootWebhookConfig) *ChatwootWebhookHandler {
	return &ChatwootWebhookHandler{
		rdb:    rdb,
		config: config,
	}
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
	if h.config.WebhookToken != "" && token != h.config.WebhookToken {
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

	return nil
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

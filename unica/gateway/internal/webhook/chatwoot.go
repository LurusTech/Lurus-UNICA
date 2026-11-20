// Package webhook provides HTTP webhook handlers for external service integrations.
package webhook

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/gateway/internal/metrics"
	"github.com/kefu/unica/pkg/model"
	"github.com/kefu/unica/pkg/platformsettings"
)

const (
	// The router still keeps cw_conv:/unica_conv_cw: mappings for its own use;
	// this handler identifies conversations by their attributes instead (see
	// unicaConversation).

	// OutboundStream is the Redis Stream key for outbound messages.
	outboundStream = "unica:outbound"

	// handoffStream is where the router's handoff consumer reads. A resolved
	// conversation is published here, not to outboundStream (D2): the outbound
	// consumer's only job is "find the channel adapter by ChannelID and send",
	// a close event has no channel and nothing to send, and every one of them
	// used to retry three times and land in the dead-letter stream while the
	// conversation itself stayed open until the idle sweep.
	handoffStream = "unica:handoff"

	// conversationClosedType is the event type the router's handoff consumer
	// recognises as "an agent resolved this conversation in Chatwoot". It is
	// mirrored in router/internal/handoff (EventConversationResolved); the two
	// modules share no package for it, so a test on each side pins the string.
	conversationClosedType = "event.conversation_closed"

	// seenKeyPrefix marks a Chatwoot event already handled. Chatwoot delivers
	// every event once per configured webhook — the account webhook and the
	// API inbox's own callback both fire (WebhookListener#deliver_webhook_payloads)
	// — so a deployment with both pointed here would otherwise send each agent
	// reply to the customer twice.
	seenKeyPrefix = "cw_seen:"
	seenTTL       = 24 * time.Hour
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

// cwWebhookPayload is a Chatwoot webhook body. The two events read here have
// different shapes (Chatwoot v3.15, app/listeners/webhook_listener.rb):
//
//   - message_created is Message#webhook_data: the message's own fields at the
//     top level, the conversation nested under "conversation";
//   - conversation_status_changed is Conversation#webhook_data itself, so the
//     conversation's id, status and custom attributes are top-level fields.
//
// In both, a conversation's "id" is its display id, which each Chatwoot
// account numbers from 1.
type cwWebhookPayload struct {
	Event string `json:"event"`

	// message_created: ID is the message id, unique across accounts; Account
	// is the Chatwoot account the message was written in.
	ID      int `json:"id,omitempty"`
	Account struct {
		ID int `json:"id"`
	} `json:"account,omitempty"`
	Content      string         `json:"content,omitempty"`
	MessageType  cwMessageType  `json:"message_type,omitempty"`
	Private      bool           `json:"private,omitempty"`
	ContentType  string         `json:"content_type,omitempty"`
	Conversation cwConversation `json:"conversation,omitempty"`
	Sender       struct {
		ID   int    `json:"id"`
		Name string `json:"name,omitempty"`
		Type string `json:"type,omitempty"` // "user" for agent, "contact" for customer
	} `json:"sender,omitempty"`

	// conversation_status_changed: ID above is the conversation's display id.
	// Timestamp is its last activity, the same in every delivery of one event.
	Status           string  `json:"status,omitempty"`
	CustomAttributes cwAttrs `json:"custom_attributes,omitempty"`
	Timestamp        int64   `json:"timestamp,omitempty"`
}

// cwConversation is the conversation nested in a message event.
type cwConversation struct {
	ID         int     `json:"id"`
	Status     string  `json:"status,omitempty"`
	CustomAttr cwAttrs `json:"custom_attributes,omitempty"`
}

// cwMessageType is Chatwoot's message_type. Webhooks carry the enum name
// ("incoming", "outgoing", "activity", "template": Message#webhook_data passes
// the Rails enum through), while the REST API's own responses carry the
// number. Both are read, so neither shape fails the whole payload — a strict
// int used to reject every real message_created with 400.
type cwMessageType int

const (
	cwIncoming cwMessageType = 0
	cwOutgoing cwMessageType = 1
)

var cwMessageTypeNames = map[string]cwMessageType{
	"incoming": cwIncoming, "outgoing": cwOutgoing, "activity": 2, "template": 3,
}

func (m *cwMessageType) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err == nil {
		v, ok := cwMessageTypeNames[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			v = -1
		}
		*m = v
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("message_type: %w", err)
	}
	*m = cwMessageType(n)
	return nil
}

// cwAttrs is a custom_attributes object. Its values are whatever an operator
// or an integration stored — strings, numbers, booleans — so it is decoded
// loosely; a map of strings would reject a payload over one numeric value.
type cwAttrs map[string]any

// str reads one attribute as a string, "" when absent or not a scalar.
func (a cwAttrs) str(key string) string {
	switch v := a[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

// ServeHTTP handles incoming Chatwoot webhook HTTP requests.
func (h *ChatwootWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify webhook token. Chatwoot sends no header of its own on a webhook
	// call (lib/webhooks/trigger.rb posts with content type and accept only),
	// so the token normally travels in the callback URL Chatwoot is given:
	// .../api/v1/webhook/chatwoot?token=<CHATWOOT_WEBHOOK_TOKEN>. The header is
	// still read for a proxy or a sender that sets it.
	token := r.Header.Get("X-Chatwoot-Webhook-Token")
	if token == "" {
		token = r.URL.Query().Get("token")
	}
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
	// Filter: only process outgoing messages from agents
	if payload.MessageType != cwOutgoing {
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

	unicaConvID, err := h.unicaConversation(payload.Conversation.ID, payload.Conversation.CustomAttr)
	if err != nil {
		return err
	}

	if payload.Content == "" {
		log.Printf("[chatwoot-webhook] skipping empty content message %d", payload.ID)
		return nil
	}

	recipient, err := h.recipientOf(ctx, unicaConvID, payload.Conversation.CustomAttr, payload.Account.ID)
	if err != nil {
		return err
	}

	seenKey := seenKeyPrefix + "msg:" + strconv.Itoa(payload.ID)
	if !h.firstDelivery(ctx, seenKey) {
		log.Printf("[chatwoot-webhook] message %d already forwarded, duplicate delivery skipped", payload.ID)
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

	outMsg.Data.ChannelID = recipient.channelID
	outMsg.Data.PlatformMeta.PlatformUserID = recipient.platformUserID

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
		// Not sent, so a second delivery of the same message may try again.
		// The request may already be cancelled; the unmark must not be.
		h.rdb.Del(context.WithoutCancel(ctx), seenKey)
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
// A reply that cannot be attributed to a UNICA conversation (no
// unica_conversation_id on the Chatwoot conversation) never gets here: the
// lookup fails, is logged, and the reply is dropped.
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
// When a Chatwoot conversation is resolved, the corresponding UNICA conversation
// is closed by the router: this publishes the close request to the handoff
// stream, whose consumer owns the conversation state machine and runs the same
// close path as the idle sweep (including the on-close hooks).
//
// The payload is shaped like a handoff event (type, conversation_id, reason,
// timestamp) because that consumer decodes every entry into one struct; the
// type is what tells it this is a close, not a handoff.
func (h *ChatwootWebhookHandler) handleConversationStatusChanged(ctx context.Context, payload *cwWebhookPayload) error {
	// The conversation is the payload itself: its display id is ID.
	cwConvID, status := payload.ID, payload.Status
	if status != "resolved" {
		log.Printf("[chatwoot-webhook] ignoring conversation status change to %s (conv=%d)", status, cwConvID)
		return nil
	}

	unicaConvID, err := h.unicaConversation(cwConvID, payload.CustomAttributes)
	if err != nil {
		return err
	}
	seenKey := ""
	if payload.Timestamp != 0 {
		seenKey = fmt.Sprintf("%sresolved:%s:%d", seenKeyPrefix, unicaConvID, payload.Timestamp)
		if !h.firstDelivery(ctx, seenKey) {
			log.Printf("[chatwoot-webhook] resolve of cw conv %d already published, duplicate delivery skipped", cwConvID)
			return nil
		}
	}

	closeEvent := resolvedEvent{
		Type:           conversationClosedType,
		ConversationID: unicaConvID,
		Reason:         "chatwoot_resolved",
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
	}
	eventJSON, err := json.Marshal(closeEvent)
	if err != nil {
		return fmt.Errorf("marshal close event: %w", err)
	}

	_, err = h.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: handoffStream,
		Values: map[string]interface{}{
			"payload":         string(eventJSON),
			"type":            conversationClosedType,
			"conversation_id": unicaConvID,
			"source":          "chatwoot",
		},
	}).Result()
	if err != nil {
		if seenKey != "" {
			h.rdb.Del(context.WithoutCancel(ctx), seenKey)
		}
		return fmt.Errorf("publish close event: %w", err)
	}

	log.Printf("[chatwoot-webhook] conversation resolved event published to %s (cw_conv=%d, unica_conv=%s)",
		handoffStream, cwConvID, unicaConvID)

	return nil
}

// unicaConversation finds the UNICA conversation a Chatwoot conversation
// mirrors. UNICA has written its own id into the custom attributes of every
// conversation it creates (router/internal/handoff and
// routing/chatwoot_forwarder, since the first version), and every webhook
// carries them back.
//
// There is deliberately no fallback to the cw_conv mapping. It is keyed by the
// display id alone, which every Chatwoot account numbers from 1, so with one
// account per tenant it names some other tenant's conversation 7 as readily as
// this one; and the conversations that reach it — no attribute — are exactly
// the ones UNICA did not create (a website widget, an e-mail inbox, an
// attribute an agent deleted). Following it could send an agent's words to
// another tenant's customer.
func (h *ChatwootWebhookHandler) unicaConversation(cwConvID int, attrs cwAttrs) (string, error) {
	if id := attrs.str("unica_conversation_id"); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("chatwoot conversation %d carries no unica_conversation_id: not a UNICA conversation", cwConvID)
}

// replyRecipient is where an agent's reply goes: the channel picks the
// adapter, the platform user id is who it is addressed to.
type replyRecipient struct {
	channelID      string
	platformUserID string
}

// recipientOf finds who an agent's reply is addressed to, and refuses one it
// cannot address. Without a platform user id no adapter can send — each
// refuses an outbound message that lacks one — so such a reply is reported
// here rather than published to fail three times and land in the dead-letter
// stream.
//
// With a database the conversation's own rows decide: custom attributes are
// editable by anyone who can edit the Chatwoot conversation, and the database
// also tells which Chatwoot account the conversation's product line uses, so
// a reply written in one account cannot be sent to another tenant's customer.
// Without one (or when the lookup fails) the attributes UNICA wrote onto the
// conversation are used; a conversation handed over before platform_user_id
// was written there cannot be answered that way.
func (h *ChatwootWebhookHandler) recipientOf(ctx context.Context, unicaConvID string, attrs cwAttrs, accountID int) (replyRecipient, error) {
	if h.db != nil {
		if _, err := uuid.Parse(unicaConvID); err != nil {
			return replyRecipient{}, fmt.Errorf("agent reply names conversation %q, which is not a UNICA conversation id", unicaConvID)
		}
		var r replyRecipient
		var plAccount sql.NullInt64
		err := h.db.QueryRowContext(ctx,
			`SELECT cu.platform_identity, cv.channel_id::text, pl.chatwoot_account_id
			   FROM conversations cv
			   JOIN customers cu ON cu.id = cv.customer_id
			   LEFT JOIN product_lines pl ON pl.id = cv.product_line_id
			  WHERE cv.id = $1`, unicaConvID).Scan(&r.platformUserID, &r.channelID, &plAccount)
		switch {
		case err == nil:
			if accountID != 0 && plAccount.Valid && int(plAccount.Int64) != accountID {
				return replyRecipient{}, fmt.Errorf("agent reply from chatwoot account %d names conversation %s of account %d: refused",
					accountID, unicaConvID, plAccount.Int64)
			}
			return r, nil
		case errors.Is(err, sql.ErrNoRows):
			return replyRecipient{}, fmt.Errorf("agent reply names conversation %s, which does not exist", unicaConvID)
		default:
			log.Printf("[chatwoot-webhook] warning: recipient lookup for %s failed, using the conversation's attributes: %v", unicaConvID, err)
		}
	}
	r := replyRecipient{channelID: attrs.str("channel_id"), platformUserID: attrs.str("platform_user_id")}
	if r.platformUserID == "" || r.channelID == "" {
		return r, fmt.Errorf("agent reply for conversation %s cannot be addressed (channel_id=%q, platform_user_id set=%t)",
			unicaConvID, r.channelID, r.platformUserID != "")
	}
	return r, nil
}

// firstDelivery reports whether key is seen for the first time, marking it.
// A Redis failure answers true: a reply sent twice is a nuisance, a reply
// never sent is a customer left waiting.
func (h *ChatwootWebhookHandler) firstDelivery(ctx context.Context, key string) bool {
	first, err := h.rdb.SetNX(ctx, key, 1, seenTTL).Result()
	if err != nil {
		log.Printf("[chatwoot-webhook] warning: duplicate check for %s failed: %v", key, err)
		return true
	}
	return first
}

// resolvedEvent is the close request as the router's handoff consumer decodes
// it. Only the fields that consumer reads for a close are set.
type resolvedEvent struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
	Reason         string `json:"reason"`
	Timestamp      string `json:"timestamp"`
}

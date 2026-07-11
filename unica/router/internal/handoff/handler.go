// Package handoff processes AI-to-human handoff events by creating Chatwoot
// conversations, forwarding message history, and generating intent summaries.
package handoff

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/metrics"
	"github.com/kefu/unica/router/internal/scheduling"
	"github.com/kefu/unica/router/internal/state"
)

const (
	// Redis key prefixes for Chatwoot conversation mapping (shared with ChatwootForwarder).
	cwConvKeyPrefix   = "cw_conv:"       // cw_conv:{chatwoot_conv_id} -> unica_conv_id
	unicaConvCWPrefix = "unica_conv_cw:" // unica_conv_cw:{unica_conv_id} -> chatwoot_conv_id
	cwConvMappingTTL  = 72 * time.Hour

	// maxHistoryMessages is the maximum number of messages to load for handoff.
	maxHistoryMessages = 50
)

// HandoffEvent is the deserialized payload from the unica:handoff stream.
type HandoffEvent struct {
	Type                 string  `json:"type"`
	ConversationID       string  `json:"conversation_id"`
	ProductLineID        string  `json:"product_line_id"`
	Reason               string  `json:"reason"`
	ConfidenceScore      float64 `json:"confidence_score"`
	AIResponseSuppressed string  `json:"ai_response_suppressed"`
	CustomerMessage      string  `json:"customer_message"`
	Timestamp            string  `json:"timestamp"`
}

// HandoffHandler processes individual handoff events.
type HandoffHandler struct {
	db             *sql.DB
	rdb            *redis.Client
	stateManager   *state.Manager
	difyClient     *bridge.DifyClient
	chatwootClient *bridge.ChatwootClient
	distributor    *scheduling.Distributor
}

// NewHandoffHandler creates a new HandoffHandler with all required dependencies.
func NewHandoffHandler(db *sql.DB, rdb *redis.Client, stateManager *state.Manager, difyClient *bridge.DifyClient, chatwootClient *bridge.ChatwootClient) *HandoffHandler {
	return &HandoffHandler{
		db:             db,
		rdb:            rdb,
		stateManager:   stateManager,
		difyClient:     difyClient,
		chatwootClient: chatwootClient,
		distributor:    scheduling.NewDistributor(db, rdb, chatwootClient),
	}
}

// Handle processes a single handoff event end-to-end:
// 1. Load conversation messages
// 2. Generate AI intent summary (best-effort)
// 3. Create/find Chatwoot contact and conversation
// 4. Send summary note and message history to Chatwoot
// 5. Update conversation state and intent summary in DB
// 6. Store Redis conversation mapping
func (h *HandoffHandler) Handle(ctx context.Context, event *HandoffEvent) error {
	start := time.Now()
	defer func() {
		metrics.HandoffDuration.Observe(time.Since(start).Seconds())
	}()

	convID := event.ConversationID
	log.Printf("[handoff] processing handoff for conversation %s (reason=%s, confidence=%.2f)",
		convID, event.Reason, event.ConfidenceScore)

	// Step 1: Load conversation details
	conv, err := h.stateManager.GetConversation(ctx, convID)
	if err != nil {
		metrics.HandoffErrorsTotal.WithLabelValues("conversation_load").Inc()
		return fmt.Errorf("get conversation %s: %w", convID, err)
	}

	// Step 2: Load message history
	messages, err := h.stateManager.GetMessages(ctx, convID, maxHistoryMessages)
	if err != nil {
		metrics.HandoffErrorsTotal.WithLabelValues("messages_load").Inc()
		return fmt.Errorf("get messages for conversation %s: %w", convID, err)
	}
	log.Printf("[handoff] loaded %d messages for conversation %s", len(messages), convID)

	// Step 3: Generate AI intent summary (best-effort, non-blocking with timeout)
	summary := h.generateSummaryBestEffort(ctx, event.ProductLineID, messages, event)

	// Step 4: Load Chatwoot config from product_lines
	cwConfig, err := h.loadChatwootConfig(ctx, event.ProductLineID)
	if err != nil {
		metrics.HandoffErrorsTotal.WithLabelValues("chatwoot_config").Inc()
		return fmt.Errorf("load chatwoot config for product line %s: %w", event.ProductLineID, err)
	}

	// Step 5: Find or create Chatwoot contact
	contactID, err := h.chatwootClient.FindOrCreateContact(ctx, *cwConfig, conv.CustomerID, conv.CustomerID, map[string]string{
		"channel_id":      conv.ChannelID,
		"unica_conv_id":   convID,
		"handoff_reason":  event.Reason,
	})
	if err != nil {
		metrics.HandoffErrorsTotal.WithLabelValues("contact_create").Inc()
		return fmt.Errorf("find/create chatwoot contact: %w", err)
	}

	// Step 5b: Enrich contact with customer metadata (best-effort)
	h.enrichContactMetadata(ctx, cwConfig, contactID, conv)

	// Step 6: Create or reuse Chatwoot conversation
	cwConvID, err := h.findOrCreateCWConversation(ctx, cwConfig, convID, contactID)
	if err != nil {
		metrics.HandoffErrorsTotal.WithLabelValues("conversation_create").Inc()
		return fmt.Errorf("find/create chatwoot conversation: %w", err)
	}

	// Step 6b: Assign conversation to an available agent
	if h.distributor != nil {
		assignment, err := h.distributor.Assign(ctx, convID, event.ProductLineID)
		if err != nil {
			log.Printf("[handoff] warning: agent assignment failed for %s: %v", convID, err)
		} else if assignment.Result == scheduling.ResultAssigned && assignment.ChatwootAgentID > 0 {
			if err := h.chatwootClient.AssignConversation(ctx, *cwConfig, cwConvID, assignment.ChatwootAgentID); err != nil {
				log.Printf("[handoff] warning: failed to assign Chatwoot conv %d to agent %d: %v",
					cwConvID, assignment.ChatwootAgentID, err)
			} else {
				log.Printf("[handoff] assigned Chatwoot conv %d to agent %s (chatwoot_id=%d)",
					cwConvID, assignment.AgentName, assignment.ChatwootAgentID)
			}
		} else {
			log.Printf("[handoff] conversation %s queued (no available agents)", convID)
		}
	}

	// Step 7: Send intent summary as a private note
	if summary != "" {
		noteContent := fmt.Sprintf("**AI Handoff Summary**\n\n%s\n\n---\nReason: %s | Confidence: %.2f",
			summary, event.Reason, event.ConfidenceScore)
		if err := h.chatwootClient.AddNote(ctx, *cwConfig, cwConvID, noteContent); err != nil {
			log.Printf("[handoff] warning: failed to add summary note to Chatwoot conv %d: %v", cwConvID, err)
			// Non-fatal: continue with message history
		}
	}

	// Step 8: Sync conversation history to Chatwoot (watermark-based dedup)
	if err := h.SyncConversationHistory(ctx, conv, cwConfig, cwConvID); err != nil {
		log.Printf("[handoff] warning: failed to sync conversation history: %v", err)
		// Non-fatal: the conversation is still created in Chatwoot
	}

	// Step 9: Update intent_summary in DB
	if summary != "" {
		if err := h.stateManager.UpdateIntentSummary(ctx, convID, summary); err != nil {
			log.Printf("[handoff] warning: failed to update intent summary in DB: %v", err)
		}
	}

	// Step 10: Ensure conversation state is human_processing
	if conv.State != state.StateHumanProcessing {
		if err := h.stateManager.TransitionState(ctx, convID, state.StateHumanProcessing, "handoff_handler"); err != nil {
			log.Printf("[handoff] warning: state transition to human_processing failed for %s: %v", convID, err)
			// The router may have already transitioned this; non-fatal.
		}
	}

	metrics.HandoffEventsProcessed.Inc()
	log.Printf("[handoff] completed handoff for conversation %s -> Chatwoot conv %d (duration=%s)",
		convID, cwConvID, time.Since(start))

	return nil
}

// generateSummaryBestEffort attempts to generate an AI summary with a timeout.
// On failure, returns a static fallback summary.
func (h *HandoffHandler) generateSummaryBestEffort(ctx context.Context, productLineID string, messages []state.Message, event *HandoffEvent) string {
	summaryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	summary, err := h.GenerateSummary(summaryCtx, productLineID, messages)
	if err != nil {
		log.Printf("[handoff] summary generation failed, using fallback: %v", err)
		return fmt.Sprintf("Customer requires human assistance. Reason: %s. Last message: %s",
			event.Reason, truncate(event.CustomerMessage, 200))
	}
	return summary
}

// findOrCreateCWConversation checks Redis for an existing mapping or creates a new one.
func (h *HandoffHandler) findOrCreateCWConversation(ctx context.Context, cwConfig *bridge.ChatwootConfig, unicaConvID string, contactID int) (int, error) {
	// Check Redis for existing mapping
	key := unicaConvCWPrefix + unicaConvID
	cwConvIDStr, err := h.rdb.Get(ctx, key).Result()
	if err == nil && cwConvIDStr != "" {
		cwConvID, err := strconv.Atoi(cwConvIDStr)
		if err == nil {
			log.Printf("[handoff] reusing existing Chatwoot conv %d for unica conv %s", cwConvID, unicaConvID)
			return cwConvID, nil
		}
	}

	// Create new conversation in Chatwoot
	conv, err := h.chatwootClient.CreateConversation(ctx, *cwConfig, bridge.CWCreateConversationRequest{
		SourceID:  unicaConvID,
		InboxID:   cwConfig.InboxID,
		ContactID: contactID,
		Status:    "open",
		CustomAttr: map[string]string{
			"unica_conversation_id": unicaConvID,
			"handoff":               "true",
		},
	})
	if err != nil {
		return 0, fmt.Errorf("create chatwoot conversation: %w", err)
	}

	// Store bidirectional mapping in Redis
	cwConvIDStr = strconv.Itoa(conv.ID)
	pipe := h.rdb.Pipeline()
	pipe.Set(ctx, key, cwConvIDStr, cwConvMappingTTL)
	pipe.Set(ctx, cwConvKeyPrefix+cwConvIDStr, unicaConvID, cwConvMappingTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("[handoff] warning: failed to store conversation mapping: %v", err)
	}

	log.Printf("[handoff] mapped unica conv %s <-> chatwoot conv %d", unicaConvID, conv.ID)
	return conv.ID, nil
}

// sendMessageHistory sends all conversation messages to Chatwoot with correct direction.
func (h *HandoffHandler) sendMessageHistory(ctx context.Context, cwConfig *bridge.ChatwootConfig, cwConvID int, messages []state.Message) error {
	for _, msg := range messages {
		content := extractTextContent(msg.ContentJSON)
		if content == "" {
			continue
		}

		// Determine message type based on direction/sender
		messageType := "incoming" // Default: customer message
		if msg.Direction == "outbound" || msg.SenderType == "ai" || msg.SenderType == "system" {
			messageType = "outgoing"
		}

		// Add timestamp prefix for context
		timeStr := msg.CreatedAt.Format("15:04:05")
		taggedContent := fmt.Sprintf("[%s] %s", timeStr, content)

		req := bridge.CWSendMessageRequest{
			Content:     taggedContent,
			MessageType: messageType,
		}
		if _, err := h.chatwootClient.SendMessage(ctx, *cwConfig, cwConvID, req); err != nil {
			log.Printf("[handoff] warning: failed to send message %s to Chatwoot: %v", msg.ID, err)
			// Continue sending remaining messages
		}
	}
	return nil
}

// loadChatwootConfig reads Chatwoot configuration from product_lines.config_json.
func (h *HandoffHandler) loadChatwootConfig(ctx context.Context, productLineID string) (*bridge.ChatwootConfig, error) {
	// Try Redis cache first
	cacheKey := "pl_cw_config:" + productLineID
	cached, err := h.rdb.Get(ctx, cacheKey).Result()
	if err == nil && cached != "" {
		var cwCfg bridge.ChatwootConfig
		if err := json.Unmarshal([]byte(cached), &cwCfg); err == nil {
			return &cwCfg, nil
		}
	}

	// Query DB for product_lines.config_json
	var configJSON sql.NullString
	err = h.db.QueryRowContext(ctx,
		`SELECT config_json FROM product_lines WHERE id = $1`, productLineID,
	).Scan(&configJSON)
	if err != nil {
		return nil, fmt.Errorf("query product line config: %w", err)
	}
	if !configJSON.Valid || configJSON.String == "" {
		return nil, fmt.Errorf("product line %s has no config_json", productLineID)
	}

	var fullConfig struct {
		Chatwoot bridge.ChatwootConfig `json:"chatwoot"`
	}
	if err := json.Unmarshal([]byte(configJSON.String), &fullConfig); err != nil {
		return nil, fmt.Errorf("unmarshal config_json: %w", err)
	}

	if fullConfig.Chatwoot.BaseURL == "" || fullConfig.Chatwoot.APIToken == "" {
		return nil, fmt.Errorf("product line %s has incomplete chatwoot config", productLineID)
	}

	// Cache for 5 minutes
	configBytes, _ := json.Marshal(fullConfig.Chatwoot)
	h.rdb.Set(ctx, cacheKey, string(configBytes), 5*time.Minute)

	return &fullConfig.Chatwoot, nil
}

// extractTextContent attempts to extract a text string from message content JSON.
// Content may be a simple string, or an object with a "text" field.
func extractTextContent(contentJSON json.RawMessage) string {
	if len(contentJSON) == 0 {
		return ""
	}

	// Try as plain string first
	var text string
	if err := json.Unmarshal(contentJSON, &text); err == nil {
		return text
	}

	// Try as object with text field
	var obj struct {
		Text string `json:"text"`
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(contentJSON, &obj); err == nil {
		if obj.Text != "" {
			return obj.Text
		}
		if obj.URL != "" {
			return fmt.Sprintf("[%s: %s]", obj.Type, obj.URL)
		}
	}

	return string(contentJSON)
}

// enrichContactMetadata enriches a Chatwoot contact with customer metadata.
// This is best-effort; errors are logged but do not fail the handoff.
func (h *HandoffHandler) enrichContactMetadata(ctx context.Context, cwConfig *bridge.ChatwootConfig, contactID int, conv *state.Conversation) {
	customer, err := h.stateManager.GetCustomer(ctx, conv.CustomerID)
	if err != nil {
		log.Printf("[handoff] warning: failed to load customer %s for enrichment: %v", conv.CustomerID, err)
		return
	}

	convCount, err := h.stateManager.CountConversations(ctx, conv.CustomerID)
	if err != nil {
		log.Printf("[handoff] warning: failed to count conversations for %s: %v", conv.CustomerID, err)
		convCount = 0
	}

	attrs := map[string]string{
		"platform":            customer.PlatformIdentity,
		"channel_id":          customer.ChannelID,
		"first_seen":          customer.FirstSeenAt.Format(time.RFC3339),
		"total_conversations":  strconv.Itoa(convCount),
	}
	if len(customer.Tags) > 0 {
		attrs["tags"] = strings.Join(customer.Tags, ", ")
	}

	update := bridge.ContactUpdate{
		Name:             customer.DisplayName,
		CustomAttributes: attrs,
	}

	if err := h.chatwootClient.UpdateContact(ctx, *cwConfig, contactID, update); err != nil {
		log.Printf("[handoff] warning: failed to update contact %d metadata: %v", contactID, err)
	}
}

// truncate shortens a string to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

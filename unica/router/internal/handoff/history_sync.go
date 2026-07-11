// Package handoff provides history synchronization for AI-to-human handoff.
// history_sync.go implements watermark-based deduplication for syncing
// conversation messages to Chatwoot.
package handoff

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/metrics"
	"github.com/kefu/unica/router/internal/state"
)

const (
	// historySyncKeyPrefix is the Redis key prefix for history sync watermarks.
	historySyncKeyPrefix = "history_sync:"

	// historySyncTTL is the TTL for history sync watermark keys.
	historySyncTTL = 7 * 24 * time.Hour
)

// SyncConversationHistory syncs conversation messages to Chatwoot using a Redis
// watermark to avoid sending duplicate messages. On first sync, all messages are
// sent. On subsequent syncs, only messages after the watermark are sent.
func (h *HandoffHandler) SyncConversationHistory(ctx context.Context, conv *state.Conversation, cwConfig *bridge.ChatwootConfig, cwConvID int) error {
	start := time.Now()
	defer func() {
		metrics.HistorySyncDuration.Observe(time.Since(start).Seconds())
	}()

	watermarkKey := historySyncKeyPrefix + conv.ID

	// Check for existing watermark (last synced message ID).
	lastSyncedID, err := h.rdb.Get(ctx, watermarkKey).Result()
	if err != nil && err.Error() != "redis: nil" {
		log.Printf("[history_sync] warning: failed to read watermark for %s: %v", conv.ID, err)
		// Continue without watermark — will send all messages.
		lastSyncedID = ""
	}

	// Load messages based on watermark.
	var messages []state.Message
	if lastSyncedID != "" {
		messages, err = h.stateManager.GetMessagesAfterID(ctx, conv.ID, lastSyncedID, maxHistoryMessages)
		if err != nil {
			metrics.HistorySyncErrorsTotal.WithLabelValues("messages_load").Inc()
			return fmt.Errorf("get messages after watermark for %s: %w", conv.ID, err)
		}
		log.Printf("[history_sync] incremental sync for %s: %d new messages (after %s)", conv.ID, len(messages), lastSyncedID)
	} else {
		messages, err = h.stateManager.GetMessages(ctx, conv.ID, maxHistoryMessages)
		if err != nil {
			metrics.HistorySyncErrorsTotal.WithLabelValues("messages_load").Inc()
			return fmt.Errorf("get messages for %s: %w", conv.ID, err)
		}
		log.Printf("[history_sync] full sync for %s: %d messages", conv.ID, len(messages))
	}

	if len(messages) == 0 {
		return nil
	}

	// Send each message to Chatwoot with appropriate tagging.
	var syncedCount int
	var lastMsgID string
	for _, msg := range messages {
		content := extractTextContent(msg.ContentJSON)
		if content == "" {
			continue
		}

		req := buildChatwootMessage(msg, content)
		if _, err := h.chatwootClient.SendMessage(ctx, *cwConfig, cwConvID, req); err != nil {
			log.Printf("[history_sync] warning: failed to send message %s: %v", msg.ID, err)
			metrics.HistorySyncErrorsTotal.WithLabelValues("send_message").Inc()
			// Continue sending remaining messages.
			continue
		}
		syncedCount++
		lastMsgID = msg.ID
	}

	// Update watermark to the last successfully synced message.
	if lastMsgID != "" {
		if err := h.rdb.Set(ctx, watermarkKey, lastMsgID, historySyncTTL).Err(); err != nil {
			log.Printf("[history_sync] warning: failed to update watermark for %s: %v", conv.ID, err)
			metrics.HistorySyncErrorsTotal.WithLabelValues("watermark_update").Inc()
		}
	}

	metrics.HistorySyncMessagesTotal.WithLabelValues(conv.ProductLineID).Add(float64(syncedCount))
	log.Printf("[history_sync] synced %d/%d messages for conversation %s (duration=%s)",
		syncedCount, len(messages), conv.ID, time.Since(start))

	return nil
}

// buildChatwootMessage creates a CWSendMessageRequest from a state.Message,
// applying sender-type-specific formatting rules.
func buildChatwootMessage(msg state.Message, content string) bridge.CWSendMessageRequest {
	timeStr := msg.CreatedAt.Format("15:04:05")

	switch msg.SenderType {
	case "customer":
		return bridge.CWSendMessageRequest{
			Content:     fmt.Sprintf("[%s] %s", timeStr, content),
			MessageType: "incoming",
		}
	case "ai":
		return bridge.CWSendMessageRequest{
			Content:     fmt.Sprintf("[%s] [AI] %s", timeStr, content),
			MessageType: "outgoing",
		}
	case "system":
		return bridge.CWSendMessageRequest{
			Content:     fmt.Sprintf("[%s] [System] %s", timeStr, content),
			MessageType: "outgoing",
			Private:     true,
		}
	default:
		// human/agent messages
		return bridge.CWSendMessageRequest{
			Content:     fmt.Sprintf("[%s] %s", timeStr, content),
			MessageType: "outgoing",
		}
	}
}

package marketing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/kefu/unica/router/internal/metrics"
)

// MetadataUpdater is the interface for updating conversation metadata.
// Satisfied by state.Manager.
type MetadataUpdater interface {
	UpdateConversationMetadata(ctx context.Context, convID string, metadataJSON json.RawMessage) error
}

// IntentMetadata represents the intent tracking data stored in conversation metadata.
type IntentMetadata struct {
	Intents          []string          `json:"intents"`
	IntentTimestamps map[string]string `json:"intent_timestamps"`
}

// Tracker stores detected intents in conversation metadata and emits metrics.
type Tracker struct {
	updater MetadataUpdater
}

// NewTracker creates a new intent Tracker with the given metadata updater.
func NewTracker(updater MetadataUpdater) *Tracker {
	return &Tracker{updater: updater}
}

// TrackIntents stores the detected intents in conversation metadata and
// increments Prometheus counters. It merges new intents with any previously
// stored intents for the conversation.
func (t *Tracker) TrackIntents(ctx context.Context, convID, productLineID string, intents []string) error {
	if len(intents) == 0 {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	meta := IntentMetadata{
		Intents:          intents,
		IntentTimestamps: make(map[string]string, len(intents)),
	}
	for _, intent := range intents {
		meta.IntentTimestamps[intent] = now
	}

	// Build the metadata JSON to merge into the conversation
	// Use PostgreSQL JSONB merge (||) so existing intents array gets updated
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal intent metadata: %w", err)
	}

	if err := t.updater.UpdateConversationMetadata(ctx, convID, metaJSON); err != nil {
		return fmt.Errorf("update conversation metadata: %w", err)
	}

	// Emit Prometheus metrics
	for _, intent := range intents {
		metrics.MarketingIntentDetectedTotal.WithLabelValues(intent, productLineID).Inc()
	}

	// If any purchase-related intent detected, count as proactive marketing
	for _, intent := range intents {
		if intent == "price_inquiry" || intent == "purchase_intent" || intent == "comparison" || intent == "feature_question" {
			metrics.MarketingProactiveMessagesTotal.WithLabelValues(productLineID).Inc()
			break
		}
	}

	log.Printf("[marketing] tracked %d intents for conversation %s: %v", len(intents), convID, intents)
	return nil
}

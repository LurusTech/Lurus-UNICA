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
	// MergeIntents unions the intents into metadata.intents and merges their
	// timestamps, preserving intents detected on earlier turns.
	MergeIntents(ctx context.Context, convID string, intents []string, timestamps json.RawMessage) error
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
	timestamps := make(map[string]string, len(intents))
	for _, intent := range intents {
		timestamps[intent] = now
	}
	tsJSON, err := json.Marshal(timestamps)
	if err != nil {
		return fmt.Errorf("marshal intent timestamps: %w", err)
	}

	// Union the new intents with any previously detected ones rather than
	// overwriting, so a conversation's full intent history is preserved.
	if err := t.updater.MergeIntents(ctx, convID, intents, tsJSON); err != nil {
		return fmt.Errorf("merge intents: %w", err)
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

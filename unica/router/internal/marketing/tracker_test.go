package marketing

import (
	"context"
	"encoding/json"
	"testing"
)

// mockMetadataUpdater is a test double for MetadataUpdater.
type mockMetadataUpdater struct {
	lastConvID string
	lastJSON   json.RawMessage
	callCount  int
	err        error
}

func (m *mockMetadataUpdater) UpdateConversationMetadata(_ context.Context, convID string, metadataJSON json.RawMessage) error {
	m.lastConvID = convID
	m.lastJSON = metadataJSON
	m.callCount++
	return m.err
}

func TestTracker_TrackIntents_Empty(t *testing.T) {
	mock := &mockMetadataUpdater{}
	tracker := NewTracker(mock)

	err := tracker.TrackIntents(context.Background(), "conv-1", "product-a", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.callCount != 0 {
		t.Error("expected no calls for empty intents")
	}
}

func TestTracker_TrackIntents_StoresMetadata(t *testing.T) {
	mock := &mockMetadataUpdater{}
	tracker := NewTracker(mock)

	intents := []string{"price_inquiry", "comparison"}
	err := tracker.TrackIntents(context.Background(), "conv-1", "product-a", intents)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.callCount != 1 {
		t.Errorf("expected 1 call, got %d", mock.callCount)
	}
	if mock.lastConvID != "conv-1" {
		t.Errorf("expected conv-1, got %s", mock.lastConvID)
	}

	// Verify the stored JSON structure
	var meta IntentMetadata
	if err := json.Unmarshal(mock.lastJSON, &meta); err != nil {
		t.Fatalf("failed to unmarshal stored metadata: %v", err)
	}
	if len(meta.Intents) != 2 {
		t.Errorf("expected 2 intents, got %d", len(meta.Intents))
	}
	if _, ok := meta.IntentTimestamps["price_inquiry"]; !ok {
		t.Error("missing timestamp for price_inquiry")
	}
	if _, ok := meta.IntentTimestamps["comparison"]; !ok {
		t.Error("missing timestamp for comparison")
	}
}

func TestTracker_TrackIntents_PropagatesError(t *testing.T) {
	mock := &mockMetadataUpdater{err: context.DeadlineExceeded}
	tracker := NewTracker(mock)

	err := tracker.TrackIntents(context.Background(), "conv-1", "product-a", []string{"complaint"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

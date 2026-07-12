package marketing

import (
	"context"
	"encoding/json"
	"testing"
)

// mockMetadataUpdater is a test double for MetadataUpdater.
type mockMetadataUpdater struct {
	lastConvID    string
	lastIntents   []string
	lastTimestamps json.RawMessage
	callCount     int
	err           error
}

func (m *mockMetadataUpdater) MergeIntents(_ context.Context, convID string, intents []string, timestamps json.RawMessage) error {
	m.lastConvID = convID
	m.lastIntents = intents
	m.lastTimestamps = timestamps
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

	// Verify the intents passed through and each got a timestamp
	if len(mock.lastIntents) != 2 {
		t.Errorf("expected 2 intents, got %d", len(mock.lastIntents))
	}
	var timestamps map[string]string
	if err := json.Unmarshal(mock.lastTimestamps, &timestamps); err != nil {
		t.Fatalf("failed to unmarshal timestamps: %v", err)
	}
	if _, ok := timestamps["price_inquiry"]; !ok {
		t.Error("missing timestamp for price_inquiry")
	}
	if _, ok := timestamps["comparison"]; !ok {
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

package audit

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// mockRepository is an in-memory audit repository for testing.
type mockRepository struct {
	mu      sync.Mutex
	entries []*AuditEntry
	failOn  int // fail after N successful inserts (-1 = never fail)
}

// newMockRepo creates a mock repository that captures inserted entries.
func newMockRepo() *mockRepository {
	return &mockRepository{failOn: -1}
}

func (m *mockRepository) getEntries() []*AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*AuditEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

func TestLogger_LogEvent(t *testing.T) {
	repo := NewRepository(nil) // will not actually be called
	logger := &Logger{
		repo:    repo,
		entries: make(chan *AuditEntry, 10),
		done:    make(chan struct{}),
	}

	// Don't start processLoop; just test Log enqueue behavior
	afterJSON := json.RawMessage(`{"name":"test"}`)
	entry := &AuditEntry{
		ActorID:      "user-1",
		ActorRole:    "super_admin",
		Action:       "create",
		ResourceType: "channel_config",
		ResourceID:   "ch-1",
		AfterState:   &afterJSON,
	}

	logger.Log(entry)

	select {
	case got := <-logger.entries:
		if got.ActorID != "user-1" {
			t.Errorf("expected actor_id user-1, got %s", got.ActorID)
		}
		if got.Action != "create" {
			t.Errorf("expected action create, got %s", got.Action)
		}
		if got.CreatedAt.IsZero() {
			t.Error("expected created_at to be set")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for entry on channel")
	}
}

func TestLogger_LogSetsTimestamp(t *testing.T) {
	logger := &Logger{
		entries: make(chan *AuditEntry, 10),
		done:    make(chan struct{}),
	}

	before := time.Now()
	entry := &AuditEntry{
		ActorID:      "user-1",
		ActorRole:    "admin",
		Action:       "update",
		ResourceType: "test",
		ResourceID:   "1",
	}
	logger.Log(entry)

	got := <-logger.entries
	if got.CreatedAt.Before(before) {
		t.Error("expected created_at to be >= before time")
	}
}

func TestLogger_LogDropsWhenBufferFull(t *testing.T) {
	logger := &Logger{
		entries: make(chan *AuditEntry, 1), // buffer of 1
		done:    make(chan struct{}),
	}

	// First entry should be enqueued
	logger.Log(&AuditEntry{ActorID: "1", ActorRole: "admin", Action: "create", ResourceType: "t", ResourceID: "1"})

	// Second entry should be dropped (buffer full)
	logger.Log(&AuditEntry{ActorID: "2", ActorRole: "admin", Action: "create", ResourceType: "t", ResourceID: "2"})

	// Only one entry should be in channel
	got := <-logger.entries
	if got.ActorID != "1" {
		t.Errorf("expected first entry, got actor_id %s", got.ActorID)
	}

	select {
	case extra := <-logger.entries:
		t.Errorf("expected no more entries, got actor_id %s", extra.ActorID)
	default:
		// expected: buffer empty
	}
}

func TestLogger_LogEventConvenience(t *testing.T) {
	logger := &Logger{
		entries: make(chan *AuditEntry, 10),
		done:    make(chan struct{}),
	}

	plID := "pl-1"
	before := map[string]string{"name": "old"}
	after := map[string]string{"name": "new"}

	logger.LogEvent("user-1", "product_admin", "update", "channel_config", "ch-1",
		&plID, before, after, "192.168.1.1")

	got := <-logger.entries
	if got.ActorID != "user-1" {
		t.Errorf("expected actor_id user-1, got %s", got.ActorID)
	}
	if got.ProductLineID == nil || *got.ProductLineID != "pl-1" {
		t.Error("expected product_line_id pl-1")
	}
	if got.BeforeState == nil {
		t.Error("expected before_state to be set")
	}
	if got.AfterState == nil {
		t.Error("expected after_state to be set")
	}
	if got.IPAddress == nil || *got.IPAddress != "192.168.1.1" {
		t.Error("expected ip_address 192.168.1.1")
	}
}

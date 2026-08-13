package audit

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAuditEntry_Fields(t *testing.T) {
	now := time.Now()
	before := json.RawMessage(`{"status":"active"}`)
	after := json.RawMessage(`{"status":"inactive"}`)
	plID := "pl-123"
	ip := "192.168.1.1"

	entry := &AuditEntry{
		ID:            1,
		ActorID:       "user-1",
		ActorRole:     "admin",
		Action:        "update",
		ResourceType:  "channel_config",
		ResourceID:    "ch-1",
		ProductLineID: &plID,
		BeforeState:   &before,
		AfterState:    &after,
		IPAddress:     &ip,
		CreatedAt:     now,
	}

	if entry.ActorID != "user-1" {
		t.Errorf("expected actor_id user-1, got %s", entry.ActorID)
	}
	if entry.Action != "update" {
		t.Errorf("expected action update, got %s", entry.Action)
	}
	if entry.ResourceType != "channel_config" {
		t.Errorf("expected resource_type channel_config, got %s", entry.ResourceType)
	}
	if *entry.ProductLineID != "pl-123" {
		t.Errorf("expected product_line_id pl-123, got %s", *entry.ProductLineID)
	}
}

func TestQueryFilter_Defaults(t *testing.T) {
	filter := QueryFilter{}
	if filter.Limit != 0 {
		t.Errorf("expected default limit 0, got %d", filter.Limit)
	}
	if filter.Offset != 0 {
		t.Errorf("expected default offset 0, got %d", filter.Offset)
	}
}

func TestQueryResult_EmptyEntries(t *testing.T) {
	result := &QueryResult{
		Entries: []AuditEntry{},
		Total:   0,
		Limit:   50,
		Offset:  0,
	}

	if len(result.Entries) != 0 {
		t.Errorf("expected empty entries, got %d", len(result.Entries))
	}

	// Verify JSON marshaling
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json marshal error: %v", err)
	}

	var decoded QueryResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json unmarshal error: %v", err)
	}
	if decoded.Total != 0 {
		t.Errorf("expected total 0, got %d", decoded.Total)
	}
}

func TestNewRepository(t *testing.T) {
	repo := NewRepository(nil)
	if repo == nil {
		t.Fatal("expected non-nil repository")
	}
	if repo.db != nil {
		t.Error("expected nil db for test repo")
	}
}

func TestAuditEntry_JSONMarshal(t *testing.T) {
	before := json.RawMessage(`{"key":"value"}`)
	entry := &AuditEntry{
		ID:           1,
		ActorID:      "user-1",
		ActorRole:    "admin",
		Action:       "create",
		ResourceType: "channel_config",
		ResourceID:   "ch-1",
		BeforeState:  &before,
		CreatedAt:    time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded["actor_id"] != "user-1" {
		t.Errorf("expected actor_id user-1 in JSON, got %v", decoded["actor_id"])
	}
	if decoded["action"] != "create" {
		t.Errorf("expected action create in JSON, got %v", decoded["action"])
	}
}

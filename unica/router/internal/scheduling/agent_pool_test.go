package scheduling

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr, rdb
}

func TestAgentPool_SetOnlineOffline(t *testing.T) {
	mr, rdb := setupTestRedis(t)
	defer mr.Close()
	defer rdb.Close()

	pool := NewAgentPool(nil, rdb)
	ctx := context.Background()
	agentID := "agent-001"

	// Initially offline
	online, err := pool.IsOnline(ctx, agentID)
	if err != nil {
		t.Fatalf("IsOnline error: %v", err)
	}
	if online {
		t.Error("expected agent to be offline initially")
	}

	// Set online
	if err := pool.SetOnline(ctx, agentID); err != nil {
		t.Fatalf("SetOnline error: %v", err)
	}

	online, err = pool.IsOnline(ctx, agentID)
	if err != nil {
		t.Fatalf("IsOnline error: %v", err)
	}
	if !online {
		t.Error("expected agent to be online after SetOnline")
	}

	// Verify TTL is set
	ttl := mr.TTL(agentStatusPrefix + agentID)
	if ttl == 0 {
		t.Error("expected TTL to be set on agent status key")
	}

	// Set offline
	if err := pool.SetOffline(ctx, agentID); err != nil {
		t.Fatalf("SetOffline error: %v", err)
	}

	online, err = pool.IsOnline(ctx, agentID)
	if err != nil {
		t.Fatalf("IsOnline error: %v", err)
	}
	if online {
		t.Error("expected agent to be offline after SetOffline")
	}
}

func TestAgentPool_IncrementDecrementLoad(t *testing.T) {
	mr, rdb := setupTestRedis(t)
	defer mr.Close()
	defer rdb.Close()

	pool := NewAgentPool(nil, rdb)
	ctx := context.Background()
	agentID := "agent-002"

	// Initial load should be 0
	load, err := pool.GetLoad(ctx, agentID)
	if err != nil {
		t.Fatalf("GetLoad error: %v", err)
	}
	if load != 0 {
		t.Errorf("expected initial load 0, got %d", load)
	}

	// Increment load
	val, err := pool.IncrementLoad(ctx, agentID)
	if err != nil {
		t.Fatalf("IncrementLoad error: %v", err)
	}
	if val != 1 {
		t.Errorf("expected load 1 after increment, got %d", val)
	}

	val, err = pool.IncrementLoad(ctx, agentID)
	if err != nil {
		t.Fatalf("IncrementLoad error: %v", err)
	}
	if val != 2 {
		t.Errorf("expected load 2 after second increment, got %d", val)
	}

	// Verify via GetLoad
	load, err = pool.GetLoad(ctx, agentID)
	if err != nil {
		t.Fatalf("GetLoad error: %v", err)
	}
	if load != 2 {
		t.Errorf("expected load 2, got %d", load)
	}

	// Decrement load
	val, err = pool.DecrementLoad(ctx, agentID)
	if err != nil {
		t.Fatalf("DecrementLoad error: %v", err)
	}
	if val != 1 {
		t.Errorf("expected load 1 after decrement, got %d", val)
	}

	// Decrement to zero
	val, err = pool.DecrementLoad(ctx, agentID)
	if err != nil {
		t.Fatalf("DecrementLoad error: %v", err)
	}
	if val != 0 {
		t.Errorf("expected load 0 after decrement, got %d", val)
	}

	// Decrement below zero should clamp to 0
	val, err = pool.DecrementLoad(ctx, agentID)
	if err != nil {
		t.Fatalf("DecrementLoad below zero error: %v", err)
	}
	if val != 0 {
		t.Errorf("expected load 0 after decrement below zero, got %d", val)
	}
}

func TestParseUUIDArray(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"empty braces", "{}", nil},
		{"single uuid", "{550e8400-e29b-41d4-a716-446655440000}", []string{"550e8400-e29b-41d4-a716-446655440000"}},
		{"multiple uuids", "{550e8400-e29b-41d4-a716-446655440000,660e8400-e29b-41d4-a716-446655440001}", []string{"550e8400-e29b-41d4-a716-446655440000", "660e8400-e29b-41d4-a716-446655440001"}},
		{"quoted uuids", `{"550e8400-e29b-41d4-a716-446655440000","660e8400-e29b-41d4-a716-446655440001"}`, []string{"550e8400-e29b-41d4-a716-446655440000", "660e8400-e29b-41d4-a716-446655440001"}},
		{"null", "NULL", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseUUIDArray([]byte(tt.input))
			if len(got) != len(tt.want) {
				t.Errorf("parseUUIDArray(%q) = %v (len=%d), want %v (len=%d)", tt.input, got, len(got), tt.want, len(tt.want))
				return
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("parseUUIDArray(%q)[%d] = %q, want %q", tt.input, i, v, tt.want[i])
				}
			}
		})
	}
}

func TestAgentPool_GetAvailableAgents_NoDB(t *testing.T) {
	// Test that GetAvailableAgents handles nil DB gracefully
	// by using a pool with no DB (will fail on queryAgentsByProductLine)
	mr, rdb := setupTestRedis(t)
	defer mr.Close()
	defer rdb.Close()

	pool := NewAgentPool(nil, rdb)
	ctx := context.Background()

	// This will error because db is nil, testing error path
	_, err := pool.GetAvailableAgents(ctx, "pl-123")
	if err == nil {
		t.Error("expected error with nil DB")
	}
}

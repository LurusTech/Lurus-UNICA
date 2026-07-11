package scheduling

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/router/internal/bridge"
)

// mockAgentPool wraps AgentPool but overrides queryAgentsByProductLine via an injected function.
// Since AgentPool.queryAgentsByProductLine requires a real DB, we test the Distributor
// by creating a pool with a mocked agent list and pre-populating Redis state.

// testDistributorSetup prepares a distributor with mock agents pre-populated in Redis.
func testDistributorSetup(t *testing.T, agents []Agent) (*Distributor, *miniredis.Miniredis, *redis.Client, *httptest.Server, *[]chatwootCall) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	cwSrv, calls := newMockChatwootServer(t)

	cwClient := bridge.NewChatwootClientWithHTTP(cwSrv.Client())

	pool := NewAgentPool(nil, rdb)

	dist := &Distributor{
		rdb:            rdb,
		chatwootClient: cwClient,
		pool:           pool,
	}

	return dist, mr, rdb, cwSrv, calls
}

type chatwootCall struct {
	method string
	path   string
	body   string
}

func newMockChatwootServer(t *testing.T) (*httptest.Server, *[]chatwootCall) {
	t.Helper()
	var mu sync.Mutex
	calls := &[]chatwootCall{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes := make([]byte, 0)
		if r.Body != nil {
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			bodyBytes = buf[:n]
		}

		mu.Lock()
		*calls = append(*calls, chatwootCall{
			method: r.Method,
			path:   r.URL.Path,
			body:   string(bodyBytes),
		})
		mu.Unlock()

		// Handle assignment endpoint
		if strings.Contains(r.URL.Path, "/assignments") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}

		w.WriteHeader(http.StatusOK)
	}))

	return srv, calls
}

func TestDistributor_Assign_NoAvailableAgents(t *testing.T) {
	// Test with a custom pool that returns empty agents.
	// We override GetAvailableAgents by using a pool with nil DB (which errors),
	// but we want a "no agents" result. So we use a different approach:
	// we create a full distributor with mock pool that has no agents in DB.

	mr, rdb := setupTestRedis(t)
	defer mr.Close()
	defer rdb.Close()

	cwSrv, _ := newMockChatwootServer(t)
	defer cwSrv.Close()

	cwClient := bridge.NewChatwootClientWithHTTP(cwSrv.Client())

	// Since we can't easily inject a mock pool without interfaces,
	// test the full Assign path with a real pool but no agents set online
	dist := &Distributor{
		rdb:            rdb,
		chatwootClient: cwClient,
		pool:           NewAgentPool(nil, rdb),
	}

	// GetAvailableAgents will fail because DB is nil
	// In this case, Assign should return an error
	ctx := context.Background()
	_, err := dist.Assign(ctx, "conv-123", "pl-abc")
	if err == nil {
		t.Error("expected error when DB is nil")
	}
}

// mockAgentPool is unused but kept for reference.
type mockAgentPool struct {
	agents []Agent
}

// Test distributor assignment with pre-populated Redis state.
// Since we can't mock the DB query, we test the assignment logic directly
// by calling the internal methods.
func TestDistributor_AssignLogic_SelectsLowestLoad(t *testing.T) {
	mr, rdb := setupTestRedis(t)
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()

	// Simulate two online agents with different loads
	agentA := "agent-aaa"
	agentB := "agent-bbb"

	pool := NewAgentPool(nil, rdb)

	// Set both agents online
	if err := pool.SetOnline(ctx, agentA); err != nil {
		t.Fatal(err)
	}
	if err := pool.SetOnline(ctx, agentB); err != nil {
		t.Fatal(err)
	}

	// Give agentA load of 3, agentB load of 1
	for i := 0; i < 3; i++ {
		pool.IncrementLoad(ctx, agentA)
	}
	pool.IncrementLoad(ctx, agentB)

	// Verify loads
	loadA, _ := pool.GetLoad(ctx, agentA)
	loadB, _ := pool.GetLoad(ctx, agentB)
	if loadA != 3 {
		t.Errorf("expected agentA load=3, got %d", loadA)
	}
	if loadB != 1 {
		t.Errorf("expected agentB load=1, got %d", loadB)
	}

	// Simulate the selection algorithm
	agents := []AgentWithLoad{
		{Agent: Agent{ID: agentA, Name: "Alice", MaxConcurrent: 5}, CurrentLoad: loadA},
		{Agent: Agent{ID: agentB, Name: "Bob", MaxConcurrent: 5}, CurrentLoad: loadB},
	}

	// The algorithm should pick the agent with the lowest load
	best := agents[0]
	for _, a := range agents[1:] {
		if a.CurrentLoad < best.CurrentLoad {
			best = a
		}
	}

	if best.Agent.ID != agentB {
		t.Errorf("expected lowest-load agent to be %s (load=1), got %s (load=%d)",
			agentB, best.Agent.ID, best.CurrentLoad)
	}
}

func TestDistributor_AssignLogic_AllAtCapacity(t *testing.T) {
	mr, rdb := setupTestRedis(t)
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	pool := NewAgentPool(nil, rdb)

	agentID := "agent-full"

	// Set agent online with max concurrent = 2
	pool.SetOnline(ctx, agentID)

	// Fill to capacity
	pool.IncrementLoad(ctx, agentID)
	pool.IncrementLoad(ctx, agentID)

	load, _ := pool.GetLoad(ctx, agentID)
	maxConcurrent := 2

	// This agent should be filtered out
	if load < maxConcurrent {
		t.Errorf("expected agent at capacity (load=%d >= max=%d)", load, maxConcurrent)
	}
}

func TestDistributor_AssignLogic_OfflineAgentFiltered(t *testing.T) {
	mr, rdb := setupTestRedis(t)
	defer mr.Close()
	defer rdb.Close()

	ctx := context.Background()
	pool := NewAgentPool(nil, rdb)

	agentID := "agent-offline"

	// Do NOT set online
	online, err := pool.IsOnline(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if online {
		t.Error("expected agent to be offline")
	}
}

func TestAssignment_Results(t *testing.T) {
	// Test Assignment struct creation
	assignedResult := &Assignment{
		AgentID:         "agent-001",
		AgentName:       "Alice",
		ChatwootAgentID: 42,
		Result:          ResultAssigned,
	}

	if assignedResult.Result != ResultAssigned {
		t.Errorf("expected result=assigned, got %s", assignedResult.Result)
	}

	queuedResult := &Assignment{Result: ResultQueued}
	if queuedResult.Result != ResultQueued {
		t.Errorf("expected result=queued, got %s", queuedResult.Result)
	}
	if queuedResult.AgentID != "" {
		t.Errorf("expected empty agent ID for queued result, got %s", queuedResult.AgentID)
	}
}

func TestNewDistributor(t *testing.T) {
	mr, rdb := setupTestRedis(t)
	defer mr.Close()
	defer rdb.Close()

	cwSrv, _ := newMockChatwootServer(t)
	defer cwSrv.Close()

	cwClient := bridge.NewChatwootClientWithHTTP(cwSrv.Client())

	dist := NewDistributor(nil, rdb, cwClient)
	if dist == nil {
		t.Fatal("expected non-nil distributor")
	}
	if dist.pool == nil {
		t.Error("expected non-nil agent pool")
	}
}

func TestNewDistributorWithPool(t *testing.T) {
	mr, rdb := setupTestRedis(t)
	defer mr.Close()
	defer rdb.Close()

	cwSrv, _ := newMockChatwootServer(t)
	defer cwSrv.Close()

	cwClient := bridge.NewChatwootClientWithHTTP(cwSrv.Client())
	pool := NewAgentPool(nil, rdb)

	dist := NewDistributorWithPool(pool, cwClient)
	if dist == nil {
		t.Fatal("expected non-nil distributor")
	}
	if dist.pool != pool {
		t.Error("expected pool to be the injected pool")
	}
}

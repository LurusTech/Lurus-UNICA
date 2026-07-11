package scheduling

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"

	"github.com/kefu/unica/router/internal/bridge"
	"github.com/kefu/unica/router/internal/metrics"
)

// AssignmentResult indicates the outcome of an assignment attempt.
type AssignmentResult string

const (
	ResultAssigned AssignmentResult = "assigned"
	ResultQueued   AssignmentResult = "queued"
)

// Assignment holds the result of a conversation assignment attempt.
type Assignment struct {
	AgentID         string           `json:"agent_id,omitempty"`
	AgentName       string           `json:"agent_name,omitempty"`
	ChatwootAgentID int              `json:"chatwoot_agent_id,omitempty"`
	Result          AssignmentResult `json:"result"`
}

// Distributor handles the logic of assigning conversations to available agents.
type Distributor struct {
	db             *sql.DB
	rdb            *redis.Client
	chatwootClient *bridge.ChatwootClient
	pool           *AgentPool
}

// NewDistributor creates a new Distributor with all required dependencies.
func NewDistributor(db *sql.DB, rdb *redis.Client, chatwootClient *bridge.ChatwootClient) *Distributor {
	return &Distributor{
		db:             db,
		rdb:            rdb,
		chatwootClient: chatwootClient,
		pool:           NewAgentPool(db, rdb),
	}
}

// NewDistributorWithPool creates a Distributor with an explicit AgentPool (for testing).
func NewDistributorWithPool(pool *AgentPool, chatwootClient *bridge.ChatwootClient) *Distributor {
	return &Distributor{
		chatwootClient: chatwootClient,
		pool:           pool,
	}
}

// Assign selects the best available agent for a conversation and returns the assignment.
// The algorithm finds online agents for the product line with the lowest current load.
// If no agents are available, it returns a queued result.
func (d *Distributor) Assign(ctx context.Context, conversationID, productLineID string) (*Assignment, error) {
	available, err := d.pool.GetAvailableAgents(ctx, productLineID)
	if err != nil {
		return nil, fmt.Errorf("get available agents: %w", err)
	}

	if len(available) == 0 {
		log.Printf("[distributor] no available agents for product line %s, conversation %s queued",
			productLineID, conversationID)
		metrics.AgentAssignmentsTotal.WithLabelValues(productLineID, string(ResultQueued)).Inc()
		return &Assignment{Result: ResultQueued}, nil
	}

	// Select agent with lowest current load
	best := available[0]
	for _, a := range available[1:] {
		if a.CurrentLoad < best.CurrentLoad {
			best = a
		}
	}

	// Increment the agent's load
	newLoad, err := d.pool.IncrementLoad(ctx, best.Agent.ID)
	if err != nil {
		return nil, fmt.Errorf("increment load for agent %s: %w", best.Agent.ID, err)
	}

	// Update DB: set assigned_agent_id on the conversation
	if d.db != nil {
		if _, err := d.db.ExecContext(ctx,
			`UPDATE conversations SET assigned_agent_id = $1, updated_at = NOW() WHERE id = $2`,
			best.Agent.ID, conversationID,
		); err != nil {
			log.Printf("[distributor] warning: failed to update assigned_agent_id in DB: %v", err)
		}
	}

	// Update metrics
	metrics.AgentAssignmentsTotal.WithLabelValues(productLineID, string(ResultAssigned)).Inc()
	metrics.AgentLoadCurrent.WithLabelValues(best.Agent.ID).Set(float64(newLoad))
	metrics.AgentPoolAvailable.WithLabelValues(productLineID).Set(float64(len(available) - 1))

	chatwootAgentID := 0
	if best.Agent.ChatwootAgentID != nil {
		chatwootAgentID = *best.Agent.ChatwootAgentID
	}

	log.Printf("[distributor] assigned conversation %s to agent %s (name=%s, load=%d/%d)",
		conversationID, best.Agent.ID, best.Agent.Name, newLoad, best.Agent.MaxConcurrent)

	return &Assignment{
		AgentID:         best.Agent.ID,
		AgentName:       best.Agent.Name,
		ChatwootAgentID: chatwootAgentID,
		Result:          ResultAssigned,
	}, nil
}

// Package scheduling implements agent availability tracking and conversation
// distribution logic for human handoff scenarios.
package scheduling

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// Redis key prefixes for agent status tracking.
	agentStatusPrefix = "agent:status:"  // agent:status:{agent_id} -> "online"
	agentLoadPrefix   = "agent:load:"    // agent:load:{agent_id} -> int (active conversations)
	plAgentsPrefix    = "pl:agents:"     // pl:agents:{product_line_id} -> SET of agent_ids

	// heartbeatTTL is the TTL for agent online status keys.
	heartbeatTTL = 300 * time.Second

	// plAgentsCacheTTL is the TTL for the product line agent set cache.
	plAgentsCacheTTL = 300 * time.Second
)

// Agent represents a human agent record from the database.
type Agent struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Email           string   `json:"email"`
	ChatwootAgentID *int     `json:"chatwoot_agent_id,omitempty"`
	MaxConcurrent   int      `json:"max_concurrent"`
	ProductLineIDs  []string `json:"product_line_ids"`
}

// AgentWithLoad pairs an agent with their current workload.
type AgentWithLoad struct {
	Agent       Agent
	CurrentLoad int
}

// AgentPool tracks agent availability and workload using Redis.
type AgentPool struct {
	db  *sql.DB
	rdb *redis.Client
}

// NewAgentPool creates a new AgentPool instance.
func NewAgentPool(db *sql.DB, rdb *redis.Client) *AgentPool {
	return &AgentPool{db: db, rdb: rdb}
}

// SetOnline marks an agent as online with a heartbeat TTL.
func (p *AgentPool) SetOnline(ctx context.Context, agentID string) error {
	key := agentStatusPrefix + agentID
	if err := p.rdb.Set(ctx, key, "online", heartbeatTTL).Err(); err != nil {
		return fmt.Errorf("set agent online %s: %w", agentID, err)
	}
	log.Printf("[agent_pool] agent %s is now online", agentID)
	return nil
}

// SetOffline removes the agent's online status.
func (p *AgentPool) SetOffline(ctx context.Context, agentID string) error {
	key := agentStatusPrefix + agentID
	if err := p.rdb.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("set agent offline %s: %w", agentID, err)
	}
	log.Printf("[agent_pool] agent %s is now offline", agentID)
	return nil
}

// IsOnline checks whether an agent is currently online.
func (p *AgentPool) IsOnline(ctx context.Context, agentID string) (bool, error) {
	key := agentStatusPrefix + agentID
	val, err := p.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check agent online %s: %w", agentID, err)
	}
	return val == "online", nil
}

// IncrementLoad atomically increases the active conversation count for an agent.
func (p *AgentPool) IncrementLoad(ctx context.Context, agentID string) (int64, error) {
	key := agentLoadPrefix + agentID
	val, err := p.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("increment load for agent %s: %w", agentID, err)
	}
	return val, nil
}

// DecrementLoad atomically decreases the active conversation count for an agent.
// It will not go below zero.
func (p *AgentPool) DecrementLoad(ctx context.Context, agentID string) (int64, error) {
	key := agentLoadPrefix + agentID
	val, err := p.rdb.Decr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("decrement load for agent %s: %w", agentID, err)
	}
	// Clamp to zero
	if val < 0 {
		p.rdb.Set(ctx, key, "0", 0)
		return 0, nil
	}
	return val, nil
}

// GetLoad returns the current conversation load for an agent.
func (p *AgentPool) GetLoad(ctx context.Context, agentID string) (int, error) {
	key := agentLoadPrefix + agentID
	val, err := p.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get load for agent %s: %w", agentID, err)
	}
	load, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("parse load for agent %s: %w", agentID, err)
	}
	return load, nil
}

// GetAvailableAgents returns online agents for a product line that have capacity.
// It queries the database for agents belonging to the product line, checks their
// online status via Redis, and filters by load capacity.
func (p *AgentPool) GetAvailableAgents(ctx context.Context, productLineID string) ([]AgentWithLoad, error) {
	// Query agents assigned to this product line
	agents, err := p.queryAgentsByProductLine(ctx, productLineID)
	if err != nil {
		return nil, fmt.Errorf("query agents for product line %s: %w", productLineID, err)
	}

	var available []AgentWithLoad
	for _, agent := range agents {
		// Check online status
		online, err := p.IsOnline(ctx, agent.ID)
		if err != nil {
			log.Printf("[agent_pool] warning: failed to check online status for %s: %v", agent.ID, err)
			continue
		}
		if !online {
			continue
		}

		// Check current load
		load, err := p.GetLoad(ctx, agent.ID)
		if err != nil {
			log.Printf("[agent_pool] warning: failed to get load for %s: %v", agent.ID, err)
			continue
		}

		// Filter out agents at or over capacity
		if load >= agent.MaxConcurrent {
			continue
		}

		available = append(available, AgentWithLoad{
			Agent:       agent,
			CurrentLoad: load,
		})
	}

	return available, nil
}

// queryAgentsByProductLine fetches agents from the DB whose product_line_ids contain the given ID.
func (p *AgentPool) queryAgentsByProductLine(ctx context.Context, productLineID string) ([]Agent, error) {
	if p.db == nil {
		return nil, fmt.Errorf("database connection is nil")
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, name, email, chatwoot_agent_id, max_concurrent, product_line_ids
		 FROM agents
		 WHERE $1 = ANY(product_line_ids)`,
		productLineID,
	)
	if err != nil {
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		var a Agent
		var chatwootID sql.NullInt64
		var plIDs []byte // UUID[] scanned as byte array
		if err := rows.Scan(&a.ID, &a.Name, &a.Email, &chatwootID, &a.MaxConcurrent, &plIDs); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		if chatwootID.Valid {
			id := int(chatwootID.Int64)
			a.ChatwootAgentID = &id
		}
		a.ProductLineIDs = parseUUIDArray(plIDs)
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// parseUUIDArray parses a PostgreSQL UUID[] text representation into a string slice.
// Format: {uuid1,uuid2,...}
func parseUUIDArray(data []byte) []string {
	s := string(data)
	if s == "" || s == "{}" || s == "NULL" {
		return nil
	}
	// Strip enclosing braces
	if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
		s = s[1 : len(s)-1]
	}
	if s == "" {
		return nil
	}
	var result []string
	for _, part := range splitComma(s) {
		trimmed := trimQuotes(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// splitComma splits a string by commas, respecting quotes.
func splitComma(s string) []string {
	var parts []string
	var current []byte
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			inQuote = !inQuote
			current = append(current, s[i])
		case s[i] == ',' && !inQuote:
			parts = append(parts, string(current))
			current = current[:0]
		default:
			current = append(current, s[i])
		}
	}
	if len(current) > 0 {
		parts = append(parts, string(current))
	}
	return parts
}

// trimQuotes removes surrounding double quotes from a string.
func trimQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

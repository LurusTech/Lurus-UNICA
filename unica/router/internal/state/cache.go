package state

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	sessionKeyPrefix  = "session:"
	customerKeyPrefix = "customer:"
	sessionTTL        = 24 * time.Hour
	customerCacheTTL  = 24 * time.Hour
)

// SessionData holds the cached state of an active conversation session.
type SessionData struct {
	State         string `json:"state" redis:"state"`
	ProductLineID string `json:"product_line_id" redis:"product_line_id"`
	AgentID       string `json:"agent_id" redis:"agent_id"`
}

// Cache provides Redis-backed session and customer lookup caching.
type Cache struct {
	rdb *redis.Client
}

// NewCache creates a new Cache backed by the given Redis client.
func NewCache(rdb *redis.Client) *Cache {
	return &Cache{rdb: rdb}
}

// sessionKey builds the Redis key for a conversation session.
func sessionKey(conversationID string) string {
	return sessionKeyPrefix + conversationID
}

// customerKey builds the Redis key for a customer lookup cache entry.
func customerKey(platformIdentity, channelID string) string {
	return customerKeyPrefix + platformIdentity + ":" + channelID
}

// GetSession retrieves cached session data for a conversation.
// Returns nil if the session is not cached.
func (c *Cache) GetSession(ctx context.Context, conversationID string) (*SessionData, error) {
	key := sessionKey(conversationID)
	result, err := c.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("get session %s: %w", conversationID, err)
	}
	if len(result) == 0 {
		return nil, nil
	}
	return &SessionData{
		State:         result["state"],
		ProductLineID: result["product_line_id"],
		AgentID:       result["agent_id"],
	}, nil
}

// SetSession stores session data in Redis with a 24h TTL.
func (c *Cache) SetSession(ctx context.Context, conversationID string, data *SessionData) error {
	key := sessionKey(conversationID)
	fields := map[string]interface{}{
		"state":           data.State,
		"product_line_id": data.ProductLineID,
		"agent_id":        data.AgentID,
	}
	pipe := c.rdb.Pipeline()
	pipe.HSet(ctx, key, fields)
	pipe.Expire(ctx, key, sessionTTL)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("set session %s: %w", conversationID, err)
	}
	return nil
}

// InvalidateSession removes the cached session for a conversation.
func (c *Cache) InvalidateSession(ctx context.Context, conversationID string) error {
	key := sessionKey(conversationID)
	if err := c.rdb.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("invalidate session %s: %w", conversationID, err)
	}
	return nil
}

// GetCustomerID retrieves a cached customer ID by platform identity and channel.
// Returns empty string if not cached.
func (c *Cache) GetCustomerID(ctx context.Context, platformIdentity, channelID string) (string, error) {
	key := customerKey(platformIdentity, channelID)
	val, err := c.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get customer cache %s: %w", key, err)
	}
	return val, nil
}

// SetCustomerID caches a customer ID lookup by platform identity and channel.
func (c *Cache) SetCustomerID(ctx context.Context, platformIdentity, channelID, customerID string) error {
	key := customerKey(platformIdentity, channelID)
	if err := c.rdb.Set(ctx, key, customerID, customerCacheTTL).Err(); err != nil {
		return fmt.Errorf("set customer cache %s: %w", key, err)
	}
	return nil
}

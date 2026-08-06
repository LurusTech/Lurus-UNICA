// Package routing provides route resolution and message dispatching for the router service.
package routing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	routeKeyPrefix = "channel_route:"
)

// RouteConfig holds the routing configuration for a channel.
type RouteConfig struct {
	ProductLineID string
	DifyAgentID   string
	DifyAPIKey    string
	DifyBaseURL   string
	ConfigJSON    json.RawMessage // Product-line config_json for guardrail and other settings
}

// RouteCache provides Redis-backed caching for channel-to-product-line route lookups.
type RouteCache struct {
	rdb *redis.Client
	db  *sql.DB
	ttl time.Duration
}

// NewRouteCache creates a new RouteCache backed by Redis with DB fallback.
func NewRouteCache(rdb *redis.Client, db *sql.DB, ttl time.Duration) *RouteCache {
	return &RouteCache{
		rdb: rdb,
		db:  db,
		ttl: ttl,
	}
}

// routeKey builds the Redis key for a channel route.
func routeKey(channelID string) string {
	return routeKeyPrefix + channelID
}

// GetRoute resolves the routing configuration for a channel.
// It checks Redis cache first and falls back to a DB query on cache miss.
func (rc *RouteCache) GetRoute(ctx context.Context, channelID string) (*RouteConfig, error) {
	// Try cache first
	key := routeKey(channelID)
	result, err := rc.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		log.Printf("[routing] cache lookup error for channel %s: %v", channelID, err)
	}
	if len(result) > 0 {
		var configJSON json.RawMessage
		if raw, ok := result["config_json"]; ok && raw != "" {
			configJSON = json.RawMessage(raw)
		}
		return &RouteConfig{
			ProductLineID: result["product_line_id"],
			DifyAgentID:   result["dify_agent_id"],
			DifyAPIKey:    result["dify_api_key"],
			DifyBaseURL:   result["dify_base_url"],
			ConfigJSON:    configJSON,
		}, nil
	}

	// Cache miss: query DB
	config, err := rc.queryRoute(ctx, channelID)
	if err != nil {
		return nil, err
	}

	// Populate cache
	if cacheErr := rc.setRouteCache(ctx, channelID, config); cacheErr != nil {
		log.Printf("[routing] warning: failed to cache route for channel %s: %v", channelID, cacheErr)
	}

	return config, nil
}

// queryRoute fetches routing config from the database.
//
// Two channel tables exist for historical reasons: `channels` (002) predates
// the admin service, and `channel_configs` (007) is what the admin API and
// portal actually write — nothing in the codebase inserts into `channels`
// anymore. Routing must therefore resolve against both, or every channel an
// operator creates through the portal receives webhooks that the router drops
// with "failed to resolve route". The legacy table is tried first so any
// hand-provisioned row keeps working; the admin-managed table is the one that
// makes portal-created channels real.
func (rc *RouteCache) queryRoute(ctx context.Context, channelID string) (*RouteConfig, error) {
	config, err := rc.scanRoute(ctx,
		`SELECT p.id, COALESCE(p.dify_agent_id, ''), COALESCE(p.dify_api_key, ''), COALESCE(p.dify_base_url, 'http://dify:5001/v1'), p.config_json
		 FROM channels c
		 JOIN product_lines p ON c.product_line_id = p.id
		 WHERE c.id = $1 AND c.enabled = true`, channelID)
	if err == nil {
		return config, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("query route for channel %s: %w", channelID, err)
	}

	config, err = rc.scanRoute(ctx,
		`SELECT p.id, COALESCE(p.dify_agent_id, ''), COALESCE(p.dify_api_key, ''), COALESCE(p.dify_base_url, 'http://dify:5001/v1'), p.config_json
		 FROM channel_configs c
		 JOIN product_lines p ON c.product_line_id = p.id
		 WHERE c.id = $1 AND c.is_enabled = true`, channelID)
	if err != nil {
		return nil, fmt.Errorf("query route for channel %s: %w", channelID, err)
	}
	return config, nil
}

func (rc *RouteCache) scanRoute(ctx context.Context, query, channelID string) (*RouteConfig, error) {
	var config RouteConfig
	var configJSONStr sql.NullString
	err := rc.db.QueryRowContext(ctx, query, channelID).
		Scan(&config.ProductLineID, &config.DifyAgentID, &config.DifyAPIKey, &config.DifyBaseURL, &configJSONStr)
	if err != nil {
		return nil, err
	}
	if configJSONStr.Valid && configJSONStr.String != "" {
		config.ConfigJSON = json.RawMessage(configJSONStr.String)
	}
	return &config, nil
}

// setRouteCache stores a route config in Redis with TTL.
func (rc *RouteCache) setRouteCache(ctx context.Context, channelID string, config *RouteConfig) error {
	key := routeKey(channelID)
	configJSONStr := ""
	if len(config.ConfigJSON) > 0 {
		configJSONStr = string(config.ConfigJSON)
	}
	fields := map[string]interface{}{
		"product_line_id": config.ProductLineID,
		"dify_agent_id":   config.DifyAgentID,
		"dify_api_key":    config.DifyAPIKey,
		"dify_base_url":   config.DifyBaseURL,
		"config_json":     configJSONStr,
	}
	pipe := rc.rdb.Pipeline()
	pipe.HSet(ctx, key, fields)
	pipe.Expire(ctx, key, rc.ttl)
	_, err := pipe.Exec(ctx)
	return err
}

# STORY-016: Intelligent Routing - Product Line Identification + AI Dispatch

**Epic:** EPIC-002 (Conversation Routing & Management)
**Priority:** Must Have
**Story Points:** 5
**Status:** Done
**Sprint:** 2
**Created:** 2026-03-05

---

## User Story

As a system, I want inbound messages automatically routed to the correct product line AI Agent, so that customers receive product-specific responses.

---

## Description

### Background
UNICA serves 7-8 product lines, each with its own AI Agent (Dify workspace) and knowledge base. When a customer message arrives, the Router must determine which product line it belongs to (based on the channel configuration) and route it to the correct AI Agent.

The Router Service is the brain of the system — it consumes from the inbound Redis Stream, identifies or creates conversations, and dispatches messages to either Dify (AI) or Chatwoot (human), depending on the conversation state.

### Scope
**In scope:**
- Router Service consuming from `unica:inbound` Redis Stream
- Channel-to-product-line mapping lookup (DB + Redis cache)
- New conversation creation (reuse STORY-015 state machine)
- Existing conversation lookup and continuation
- AI dispatch: call Dify chat API with message and conversation context
- AI response published to `unica:outbound` stream
- Unrecognized/unconfigured channels routed to default handler
- Prometheus metrics for routing decisions
- Routing latency target: <50ms for routing decision (excluding AI response time)

**Out of scope:**
- Dify workspace setup (STORY-019)
- RAG knowledge base (STORY-020)
- Confidence scoring / handoff logic (STORY-022)
- Human agent routing (STORY-017, STORY-018)

### Routing Flow
```
1. Consumer reads message from unica:inbound stream
2. Extract channel_id from message
3. Lookup product_line_id:
   - Redis cache: channel_route:{channel_id} → product_line_id, dify_agent_id
   - Cache miss: query DB, populate cache (TTL 5min)
4. Lookup existing conversation:
   - Redis: session:{customer_id}:{channel_id} → conversation_id
   - If no active conversation: create new one (state=pending → ai_processing)
   - If active conversation exists: continue
5. Store inbound message in messages table
6. Based on conversation state:
   - ai_processing: call Dify chat API
   - human_processing: forward to Chatwoot (STORY-017)
   - closed: reopen → ai_processing, call Dify
7. Receive AI response
8. Store AI response message
9. Publish to unica:outbound stream
10. XACK the inbound message
```

---

## Acceptance Criteria

- [ ] Router Service starts and consumes from `unica:inbound` stream via consumer group
- [ ] Channel-to-product-line mapping resolved from DB, cached in Redis (TTL 5min)
- [ ] New conversations created with state `ai_processing` for first-time customers
- [ ] Existing active conversations continued (same conversation_id)
- [ ] Closed conversations reopened on new customer message
- [ ] Dify chat API called with customer message and conversation_id
- [ ] AI response stored as outbound message in messages table
- [ ] AI response published to `unica:outbound` stream with correct channel routing info
- [ ] Unrecognized channel_id logged as error and message sent to dead-letter
- [ ] Routing decision latency < 50ms (excluding Dify API call time)
- [ ] Prometheus metrics: `router_messages_routed_total{product_line, route_type}`, `router_routing_duration_seconds`
- [ ] Consumer group handles multiple Router replicas (messages distributed, not duplicated)

---

## Technical Notes

### Router Service Architecture
```go
type Router struct {
    rdb           *redis.Client
    db            *sql.DB
    stateManager  *state.Manager      // from STORY-015
    difyClient    *bridge.DifyClient
    routeCache    *RouteCache
}

type RouteConfig struct {
    ProductLineID string
    DifyAgentID   string
    DifyAPIKey    string
    DifyBaseURL   string
}
```

### Channel Route Cache
```go
type RouteCache struct {
    rdb *redis.Client
    db  *sql.DB
    ttl time.Duration
}

func (rc *RouteCache) GetRoute(ctx context.Context, channelID string) (*RouteConfig, error) {
    key := fmt.Sprintf("channel_route:%s", channelID)
    // Try Redis first
    result, err := rc.rdb.HGetAll(ctx, key).Result()
    if err == nil && len(result) > 0 {
        return parseRouteConfig(result), nil
    }
    // Cache miss — query DB
    config, err := rc.queryDB(ctx, channelID)
    if err != nil {
        return nil, err
    }
    // Populate cache
    rc.rdb.HSet(ctx, key, configToMap(config))
    rc.rdb.Expire(ctx, key, rc.ttl)
    return config, nil
}
```

### Dify Chat API Call
```go
type DifyClient struct {
    httpClient *http.Client
}

func (d *DifyClient) Chat(ctx context.Context, config *RouteConfig, msg string, convID string) (*DifyResponse, error) {
    payload := map[string]interface{}{
        "inputs":          map[string]string{},
        "query":           msg,
        "user":            convID,
        "conversation_id": convID, // Dify conversation tracking
        "response_mode":   "blocking",
    }
    // POST to {config.DifyBaseURL}/v1/chat-messages
    // Header: Authorization: Bearer {config.DifyAPIKey}
}

type DifyResponse struct {
    Answer         string  `json:"answer"`
    ConversationID string  `json:"conversation_id"`
    Metadata       struct {
        Usage struct {
            TotalTokens int `json:"total_tokens"`
        } `json:"usage"`
    } `json:"metadata"`
}
```

### Product Line DB Table
```sql
-- This table should exist or be created as part of admin setup
CREATE TABLE IF NOT EXISTS product_lines (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    dify_agent_id VARCHAR(255),
    dify_api_key VARCHAR(255),
    dify_base_url VARCHAR(255) DEFAULT 'http://dify:5001/v1',
    config_json JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Channel-to-product-line mapping
CREATE TABLE IF NOT EXISTS channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    platform VARCHAR(50) NOT NULL,
    name VARCHAR(255) NOT NULL,
    product_line_id UUID NOT NULL REFERENCES product_lines(id),
    credentials_encrypted BYTEA,
    webhook_url VARCHAR(500),
    enabled BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_channels_product_line ON channels(product_line_id);
```

### Components
- `router/cmd/router/main.go` — Service entrypoint, stream consumer setup
- `router/internal/routing/router.go` — Core routing logic
- `router/internal/routing/cache.go` — Channel route cache
- `router/internal/bridge/dify.go` — Dify API client
- `router/migrations/002_product_lines_channels.sql` — DB migration

### Configuration
```
REDIS_URL=redis://:password@redis-master:6379/0
POSTGRES_URL=postgres://user:pass@postgresql:5432/unica_core
ROUTER_CONSUMER_GROUP=router-group
ROUTER_CONSUMER_NAME=router-1
ROUTER_WORKERS=4
ROUTE_CACHE_TTL=5m
DIFY_DEFAULT_BASE_URL=http://dify:5001/v1
```

### Metrics
```
router_messages_routed_total     counter    {product_line, route_type=ai|human|dead_letter}
router_routing_duration_seconds  histogram  Routing decision latency (excludes AI call)
router_dify_call_duration_seconds histogram AI response latency
router_conversations_created_total counter  New conversations created
router_active_conversations      gauge     Currently active conversations by state
```

### Edge Cases
- Channel not configured: log error, send to dead-letter, XACK to prevent retry loop
- Dify API unavailable: retry 2x, then keep message in pending state (don't XACK, will be reclaimed)
- Multiple messages from same customer in rapid succession: all should join same conversation
- Route cache invalidation: admin updates channel config → publish Redis Pub/Sub → clear cache
- Consumer crash recovery: pending messages auto-claimed after 60s (Gateway's consumer group config)

---

## Dependencies

**Prerequisite:**
- STORY-005 (Gateway Core — `unica:inbound` and `unica:outbound` streams)
- STORY-015 (Conversation State Machine — state management, DB tables)

**Blocks:**
- STORY-017 (AI→Human Handoff — extends routing with handoff path)
- STORY-021 (Router↔Dify Integration — deepens Dify integration with RAG)
- STORY-022 (Confidence Scoring — adds AI response evaluation)

---

## Definition of Done

- [ ] Router Service starts and consumes from `unica:inbound` stream
- [ ] Channel-to-product-line routing resolves correctly
- [ ] New conversations created for first-time customer messages
- [ ] Dify chat API called and AI response received
- [ ] AI response published to `unica:outbound` stream
- [ ] Messages stored in PostgreSQL messages table
- [ ] Redis route cache functioning with correct TTL
- [ ] Prometheus metrics exposed at `/metrics`
- [ ] Unit tests for routing logic, cache, state transitions (>=80% coverage)
- [ ] Integration test: publish inbound message → verify conversation created → verify Dify called → verify outbound message
- [ ] Code committed to `router/`

---

## Story Points Breakdown

- **Stream consumer + routing logic:** 2 points
- **Dify API client + response handling:** 1 point
- **Route cache + DB queries:** 1 point
- **Testing:** 1 point
- **Total:** 5 points

**Rationale:** Moderate complexity — combines stream consumption, database operations, external API calls, and caching. The routing logic itself is straightforward (channel → product line lookup), but the end-to-end message flow requires careful integration.

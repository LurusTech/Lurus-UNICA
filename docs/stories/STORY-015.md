# STORY-015: Conversation State Machine + DB Schema

**Epic:** EPIC-002 (Conversation Routing & Management)
**Priority:** Must Have
**Story Points:** 5
**Status:** Done
**Sprint:** 2
**Created:** 2026-03-05

---

## User Story

As a system, I want conversations tracked through defined lifecycle states with persistent storage, so that conversation flow is reliable and auditable.

---

## Description

### Background
Every customer interaction in UNICA is modeled as a "conversation" with a well-defined lifecycle. The conversation state determines how messages are routed: new conversations go to AI, handed-off conversations go to human agents, and closed conversations stop processing.

This story creates the core data model (PostgreSQL schema) and state machine logic that the Router Service (STORY-016) will use. It is foundational — every subsequent feature (routing, handoff, reporting) depends on this data model.

### Scope
**In scope:**
- PostgreSQL schema: conversations, messages, customers tables
- Conversation state machine: Pending → AI_Processing → Human_Processing → Closed
- State transition validation (reject invalid transitions)
- Idle timeout auto-closure (configurable per product line)
- State change audit logging (timestamps, actor)
- Redis session cache for fast state lookup
- Database migrations (versioned SQL files)

**Out of scope:**
- Routing logic (STORY-016)
- Handoff logic (STORY-017)
- Reporting queries (STORY-029)
- RBAC / Row-Level Security (STORY-031)

### State Machine
```
                    ┌─────────────┐
    New message     │   Pending   │
    ─────────────►  │  (created)  │
                    └──────┬──────┘
                           │ route
                    ┌──────▼──────┐
                    │     AI      │◄──── customer sends new message
                    │  Processing │      (continues AI conversation)
                    └──────┬──────┘
                           │ handoff (low confidence / keyword / manual)
                    ┌──────▼──────┐
                    │   Human     │◄──── agent picks up
                    │  Processing │
                    └──────┬──────┘
                           │ resolve / idle timeout
                    ┌──────▼──────┐
                    │   Closed    │
                    │             │
                    └─────────────┘

Valid transitions:
  Pending → AI_Processing
  Pending → Human_Processing (direct human route)
  AI_Processing → Human_Processing (handoff)
  AI_Processing → Closed (AI resolved)
  Human_Processing → Closed (agent resolved)
  Closed → AI_Processing (customer reopens with new message)
```

---

## Acceptance Criteria

- [ ] PostgreSQL migration creates `conversations`, `messages`, `customers` tables
- [ ] Conversation states: `pending`, `ai_processing`, `human_processing`, `closed`
- [ ] State machine rejects invalid transitions (e.g., Closed → Pending) with error
- [ ] Valid state transitions update state and record timestamp
- [ ] Idle timeout: conversations auto-close after configurable period (default 30 min)
- [ ] Idle timeout check runs as background goroutine in Router Service
- [ ] Customer record created on first message (upsert by platform_identity + channel_id)
- [ ] Messages stored with conversation_id, direction (inbound/outbound), sender_type (customer/ai/human)
- [ ] Session state cached in Redis hash: `session:{conversation_id}` → {state, product_line_id, agent_id}
- [ ] Redis cache updated on every state transition
- [ ] State changes logged with: previous_state, new_state, actor, timestamp
- [ ] All tables have proper indexes per architecture doc

---

## Technical Notes

### Database Schema
```sql
-- Customers table
CREATE TABLE customers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    platform_identity VARCHAR(255) NOT NULL,
    channel_id UUID NOT NULL,
    display_name VARCHAR(255),
    tags JSONB DEFAULT '[]',
    notes TEXT,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(platform_identity, channel_id)
);

-- Conversations table
CREATE TABLE conversations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id UUID NOT NULL,
    product_line_id UUID NOT NULL,
    customer_id UUID NOT NULL REFERENCES customers(id),
    state VARCHAR(20) NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'ai_processing', 'human_processing', 'closed')),
    ai_confidence REAL,
    assigned_agent_id UUID,
    intent_summary TEXT,
    metadata JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ
);

CREATE INDEX idx_conversations_product_state ON conversations(product_line_id, state);
CREATE INDEX idx_conversations_customer ON conversations(customer_id);
CREATE INDEX idx_conversations_agent_state ON conversations(assigned_agent_id, state);

-- Messages table (partitioned by month)
CREATE TABLE messages (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL,
    direction VARCHAR(10) NOT NULL CHECK (direction IN ('inbound', 'outbound')),
    sender_type VARCHAR(10) NOT NULL CHECK (sender_type IN ('customer', 'ai', 'human', 'system')),
    content_json JSONB NOT NULL,
    platform_msg_id VARCHAR(255),
    confidence_score REAL,
    correlation_id VARCHAR(36),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

-- Create initial partitions
CREATE TABLE messages_2026_03 PARTITION OF messages
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE messages_2026_04 PARTITION OF messages
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');

CREATE INDEX idx_messages_conversation ON messages(conversation_id, created_at);
CREATE UNIQUE INDEX idx_messages_platform_msg ON messages(platform_msg_id) WHERE platform_msg_id IS NOT NULL;
```

### State Machine Implementation
```go
type ConversationState string

const (
    StatePending         ConversationState = "pending"
    StateAIProcessing    ConversationState = "ai_processing"
    StateHumanProcessing ConversationState = "human_processing"
    StateClosed          ConversationState = "closed"
)

var validTransitions = map[ConversationState][]ConversationState{
    StatePending:         {StateAIProcessing, StateHumanProcessing},
    StateAIProcessing:    {StateHumanProcessing, StateClosed},
    StateHumanProcessing: {StateClosed},
    StateClosed:          {StateAIProcessing},
}

func (s ConversationState) CanTransitionTo(target ConversationState) bool {
    for _, valid := range validTransitions[s] {
        if valid == target {
            return true
        }
    }
    return false
}
```

### Redis Session Cache
```go
func cacheSession(rdb *redis.Client, convID string, state ConversationState, plID, agentID string) {
    key := fmt.Sprintf("session:%s", convID)
    rdb.HSet(ctx, key, map[string]interface{}{
        "state":           string(state),
        "product_line_id": plID,
        "agent_id":        agentID,
    })
    rdb.Expire(ctx, key, 24*time.Hour)
}
```

### Components
- `router/internal/state/machine.go` — State machine with transition validation
- `router/internal/state/repository.go` — PostgreSQL CRUD for conversations/messages/customers
- `router/internal/state/cache.go` — Redis session cache operations
- `router/migrations/001_core_schema.sql` — Database migration

### Configuration
```
POSTGRES_URL=postgres://user:pass@postgresql:5432/unica_core
IDLE_TIMEOUT=30m
SESSION_CACHE_TTL=24h
```

### Edge Cases
- Customer sends message to closed conversation — reopen (Closed → AI_Processing)
- Multiple messages arrive before first is processed — all reference same conversation
- Agent goes offline while assigned — conversation stays in human_processing, supervisor can reassign
- Database connection failure — return error, do not silently drop state changes
- Monthly partition creation — need a cron job or startup check to create future partitions

---

## Dependencies

**Prerequisite:**
- STORY-001 (PostgreSQL must be running with unica_core database)
- STORY-002 (Go monorepo scaffolding for router service)

**Blocks:**
- STORY-016 (Intelligent Routing — needs conversation state)
- STORY-017 (AI→Human Handoff — needs state transitions)
- STORY-024 (Chatwoot Integration — needs conversation records)
- STORY-029 (Reporting — queries against these tables)

---

## Definition of Done

- [ ] Database migration runs successfully, creates all tables with indexes
- [ ] State machine validates all transitions correctly
- [ ] Invalid transitions return descriptive error
- [ ] Customer upsert works (create if new, update last_seen if existing)
- [ ] Messages stored and retrievable by conversation_id
- [ ] Redis session cache updated on state change
- [ ] Idle timeout background job closes stale conversations
- [ ] Unit tests for state machine transitions (all valid + invalid paths)
- [ ] Integration test: create conversation → transition states → verify in DB and Redis
- [ ] Code committed to `router/internal/state/` and `router/migrations/`

---

## Story Points Breakdown

- **DB schema + migrations:** 1 point
- **State machine + repository:** 2 points
- **Redis cache + idle timeout:** 1 point
- **Testing:** 1 point
- **Total:** 5 points

**Rationale:** Core data model with moderate complexity. State machine logic is straightforward but touches both PostgreSQL and Redis, requiring integration testing.

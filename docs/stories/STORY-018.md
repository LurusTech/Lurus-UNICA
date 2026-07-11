# STORY-018: Agent Scheduling + Distribution

**Epic:** EPIC-002 (Conversation Routing & Management)
**Priority:** Should Have
**Story Points:** 5
**Status:** Not Started
**Sprint:** 4
**Created:** 2026-03-05

---

## User Story

As a supervisor, I want conversations distributed to agents based on product line, availability, and current load, so that workload is balanced.

---

## Description

### Background
When AI hands off a conversation to a human agent (STORY-017), the system currently creates the conversation in Chatwoot but does not intelligently assign it. This story implements the agent scheduling logic: matching conversations to the best available agent based on product line assignment, online status, and current workload.

This builds on top of the Chatwoot integration (STORY-024) and handoff logic (STORY-017). The scheduling logic lives in the Router service, and the assignment is reflected in Chatwoot via its API.

### Scope
**In scope:**
- Agent-to-product-line assignment (DB + Redis cache)
- Online/offline status tracking for agents
- Round-robin distribution within product line team
- Max concurrent conversations enforcement per agent
- Assignment reflected in Chatwoot (auto-assign conversation to agent)
- Fallback: unassigned queue when no agents available

**Out of scope:**
- Skill-based routing (future enhancement)
- Priority queuing (all conversations equal priority for now)
- Agent shift scheduling / calendar integration
- Real-time dashboard for supervisors (STORY-029)

### Distribution Flow
```
Handoff triggered (STORY-017)
  -> Router checks product_line_id from conversation
  -> Query available agents for this product line:
     - online_status = true
     - current_conversations < max_concurrent
  -> Sort by current_conversations ASC (least loaded first)
  -> Assign top agent
  -> Update Chatwoot conversation assignment via API
  -> Update agent's current_conversations count in Redis
  -> If no agents available: leave unassigned in Chatwoot queue
```

---

## Acceptance Criteria

- [ ] Agents tagged with product line assignments in DB
- [ ] Agent online/offline status tracked in Redis (TTL-based heartbeat)
- [ ] Round-robin distribution within product line team (least-loaded first)
- [ ] Offline agents excluded from routing
- [ ] Max concurrent conversations enforced per agent (configurable, default 5)
- [ ] When max reached, agent skipped in distribution
- [ ] Assignment reflected in Chatwoot (conversation assigned to correct agent)
- [ ] When no agents available, conversation enters unassigned queue
- [ ] When agent comes online, pending unassigned conversations can be picked up
- [ ] Agent closing a conversation decrements their current count
- [ ] Distribution handles edge case: all agents offline gracefully

---

## Technical Notes

### Database Schema
```sql
-- agents table (extend existing or create)
CREATE TABLE agents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(100) NOT NULL,
    email VARCHAR(255) UNIQUE NOT NULL,
    chatwoot_agent_id INTEGER,
    max_concurrent INTEGER DEFAULT 5,
    product_line_ids UUID[] NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_agents_product_lines ON agents USING GIN (product_line_ids);
```

### Redis State
```
Key:   agent:status:{agent_id}
Value: "online" | "offline"
TTL:   300s (heartbeat refresh every 60s)

Key:   agent:load:{agent_id}
Value: integer (current active conversation count)
TTL:   None (managed explicitly)

Key:   pl:agents:{product_line_id}
Value: SET of agent_ids assigned to this product line
TTL:   300s (refreshed from DB periodically)
```

### Components
- `router/internal/scheduling/distributor.go` — Core distribution logic
- `router/internal/scheduling/agent_pool.go` — Agent availability tracking
- `router/internal/scheduling/distributor_test.go` — Unit tests
- Update `router/internal/handoff/handler.go` — Integrate distributor into handoff flow

### Agent Status API
```
POST /api/v1/agents/{id}/status   — Agent heartbeat (online/offline)
GET  /api/v1/agents/{id}/load     — Current conversation count
```

### Metrics
```
agent_assignments_total           counter   {product_line, result=assigned|queued}
agent_pool_available              gauge     {product_line}
agent_load_current                gauge     {agent_id}
```

---

## Dependencies

**Prerequisite:**
- STORY-017 (AI-Human Handoff — triggers distribution)
- STORY-024 (Chatwoot Integration — assignment via API)

**Blocks:**
- None directly, but enhances STORY-025 (agents see assigned conversations)

**External Dependencies:**
- None

---

## Definition of Done

- [ ] Agent-product-line assignment stored in DB
- [ ] Online status tracked via Redis heartbeat
- [ ] Distribution logic selects least-loaded available agent
- [ ] Max concurrent limit enforced
- [ ] Assignment synced to Chatwoot
- [ ] Unassigned fallback works when no agents available
- [ ] Unit tests for distributor logic (>=80% coverage)
- [ ] Integration test: handoff triggers correct agent assignment
- [ ] Code committed to `router/internal/scheduling/`

---

## Story Points Breakdown

- **Agent pool + status tracking:** 1 point
- **Distribution algorithm:** 2 points
- **Chatwoot assignment sync:** 1 point
- **Testing:** 1 point
- **Total:** 5 points

**Rationale:** Moderate complexity. Core is the distribution algorithm and Redis state management. Chatwoot assignment is a single API call per conversation.

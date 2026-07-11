# STORY-033: AI Agent Configuration UI

**Epic:** EPIC-006 (System Management & Permissions)
**Priority:** Must Have
**Story Points:** 5
**Status:** Completed
**Assigned To:** Unassigned
**Created:** 2026-03-06
**Sprint:** 6

---

## User Story

As a knowledge admin,
I want to configure AI agent settings per product line,
So that I can tune AI behavior without developer help.

---

## Description

### Background
Currently, Dify workspace configuration (system prompts, confidence thresholds, handoff rules, knowledge base assignments) requires direct access to the Dify admin panel or API calls. This story adds admin API endpoints in the admin service that proxy configuration changes to Dify, combined with UNICA-specific settings (guardrail rules, handoff keywords) stored in PostgreSQL. Knowledge admins scoped to their product line can manage AI behavior through these APIs.

### Scope
**In scope:**
- Admin API endpoints for AI agent configuration per product line
- System prompt editor (read/update via Dify API)
- Confidence threshold configuration (stored in PostgreSQL, used by router guardrail)
- Handoff rules management: threshold, keywords, blocked topics
- Knowledge base document listing and assignment (link/unlink via Dify API)
- Preview/test endpoint: send test message and see AI response
- RBAC enforcement: KnowledgeAdmin or ProductAdmin for own product line

**Out of scope:**
- Frontend UI (admin API only, frontend is a future story)
- Dify workspace creation (STORY-019 handles that)
- Document upload (STORY-020 handles that via Dify directly)
- Training or fine-tuning models

### User Flow
1. KnowledgeAdmin authenticates via JWT
2. Selects product line (scoped by RBAC)
3. Views current AI agent config: system prompt, threshold, handoff rules
4. Edits system prompt, saves - admin service updates Dify workspace via API
5. Adjusts confidence threshold to 0.75 - saved to PostgreSQL
6. Adds handoff keyword "投诉" - saved to guardrail config
7. Uses "Test" button: sends sample question, sees AI response with confidence score
8. Verifies behavior is correct before going live

---

## Acceptance Criteria

- [ ] API endpoint: `GET /api/v1/ai-config/:product_line_id` returns current config
- [ ] API endpoint: `PUT /api/v1/ai-config/:product_line_id/prompt` updates system prompt via Dify API
- [ ] API endpoint: `PUT /api/v1/ai-config/:product_line_id/threshold` updates confidence threshold
- [ ] API endpoint: `PUT /api/v1/ai-config/:product_line_id/handoff-rules` updates handoff keywords, blocked topics, threshold
- [ ] API endpoint: `GET /api/v1/ai-config/:product_line_id/knowledge` lists linked knowledge base documents
- [ ] API endpoint: `POST /api/v1/ai-config/:product_line_id/test` sends test message, returns AI response + confidence
- [ ] Changes to system prompt applied to Dify workspace via API
- [ ] Changes to threshold/handoff rules stored in PostgreSQL and cached in Redis
- [ ] Router service picks up updated config without restart (Redis cache invalidation)
- [ ] RBAC enforced: only KnowledgeAdmin/ProductAdmin/SuperAdmin can access
- [ ] Product line isolation: admin can only configure own product lines

---

## Technical Notes

### Components
- **Admin service** (`admin/`): New handlers in `internal/handler/`
- **Database:** New table `ai_agent_configs` for UNICA-specific settings
- **Router service:** Read config from Redis cache, subscribe to invalidation
- **Dify API:** Proxy calls for prompt and knowledge management

### Database Schema
```sql
CREATE TABLE ai_agent_configs (
    product_line_id UUID PRIMARY KEY REFERENCES product_lines(id),
    confidence_threshold DECIMAL(3,2) DEFAULT 0.70,
    handoff_keywords TEXT[] DEFAULT '{"转人工","人工客服"}',
    blocked_topics TEXT[] DEFAULT '{}',
    max_ai_turns INTEGER DEFAULT 10,
    updated_at TIMESTAMP DEFAULT NOW(),
    updated_by UUID REFERENCES users(id)
);
```

### API Structure
```
admin/internal/
  handler/
    ai_config.go          -- HTTP handlers for AI config CRUD
    ai_config_test.go
  repository/
    ai_config.go          -- DB queries for ai_agent_configs
  bridge/
    dify.go               -- Dify API client (prompt, knowledge)
    dify_test.go
```

### Dify API Integration
- `GET /v1/apps` - List apps in workspace
- `PUT /v1/apps/:id` - Update app config (system prompt)
- `GET /v1/datasets` - List knowledge bases
- `POST /v1/chat-messages` - Test message (same as router uses)

### Config Cache Invalidation
- On config update: write to PostgreSQL, then `DEL` Redis key `ai_config:{product_line_id}`
- Router reads config: check Redis first, fallback to PostgreSQL, cache for 5 minutes
- Alternative: publish invalidation event to Redis Pub/Sub channel `unica:config_invalidation`

### Existing Integration Points
- `router/internal/guardrail/config.go` - Already has GuardrailConfig struct with Threshold, Keywords, BlockedTopics
- `router/internal/guardrail/evaluator.go` - Uses config for evaluation
- `router/internal/bridge/dify.go` - Dify API client already exists
- `admin/internal/rbac/` - RBAC middleware already implemented

---

## Dependencies

**Prerequisite Stories:**
- STORY-019: Dify Multi-Workspace Setup (workspaces must exist)
- STORY-031: RBAC Permission System (auth + product line scoping)

**Blocked Stories:**
- None

**External Dependencies:**
- Dify API access (already deployed and integrated)

---

## Definition of Done

- [ ] All 6 API endpoints implemented and tested
- [ ] Unit tests for handlers and repository (>80% coverage)
- [ ] Dify API integration tested (prompt update, knowledge listing, test message)
- [ ] Config changes propagate to router within 5 minutes (cache invalidation)
- [ ] RBAC enforcement validated (KnowledgeAdmin can only access own PLs)
- [ ] Integration test: update threshold → send test message → verify new threshold applied
- [ ] Admin service deploys to K3s with new endpoints
- [ ] API documentation updated

---

## Story Points Breakdown

- **Database schema + repository:** 1 point
- **API handlers (6 endpoints):** 1.5 points
- **Dify API bridge:** 1 point
- **Cache invalidation + router integration:** 1 point
- **Testing:** 0.5 points
- **Total:** 5 points

**Rationale:** Leverages existing Dify bridge code in router and RBAC in admin. Main work is wiring up the admin endpoints and cache invalidation.

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created
- 2026-03-06: Implemented - all 6 API endpoints, DB migration, Dify bridge, Redis cache invalidation, RBAC enforcement, unit tests passing

**Actual Effort:** 5 points (matched estimate)

**Implementation Notes:**
- Created ai_agent_configs table with UPSERT pattern for flexible config updates
- Dify bridge supports GetAppConfig, UpdateSystemPrompt, ListKnowledgeDocuments, SendTestMessage
- Redis cache invalidation via DEL + Pub/Sub on unica:config_invalidation channel
- RBAC enforced via existing PermManageAIConfig permission (KnowledgeAdmin, ProductAdmin, SuperAdmin)
- Product line isolation checked in handler before processing any request
- 18 unit tests passing across handler, bridge, and repository packages

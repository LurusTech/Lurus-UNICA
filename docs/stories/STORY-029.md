# STORY-029: AI Effectiveness + Agent Performance Reporting

**Epic:** EPIC-005 (Data Reports & Monitoring)
**Priority:** Must Have
**Story Points:** 5
**Status:** Completed
**Assigned To:** Unassigned
**Created:** 2026-03-06
**Sprint:** 6

---

## User Story

As a supervisor,
I want to see AI auto-resolution rate, handoff rate, and agent performance metrics,
So that I can optimize AI performance and manage agent workload.

---

## Description

### Background
UNICA processes customer conversations through AI and human agents across 7-8 product lines. Supervisors need data-driven insights: which product lines have high AI resolution rates, where knowledge gaps exist, which agents perform best, and what questions customers ask most. This story implements the reporter service that queries PostgreSQL conversation/message data and exposes it via Grafana dashboards and a REST API.

### Scope
**In scope:**
- Reporter service implementation (currently a stub in `reporter/`)
- AI effectiveness metrics: auto-resolution rate, handoff rate, knowledge hit rate, top questions
- Agent performance metrics: response time, conversation volume, satisfaction scores
- Per-product-line breakdowns for all metrics
- Daily/weekly trend charts in Grafana
- REST API for programmatic access to report data
- Materialized views or pre-aggregated tables for query performance

**Out of scope:**
- Real-time streaming analytics (use Prometheus metrics for real-time)
- Export to Excel/PDF (future enhancement)
- Custom report builder UI

### User Flow
1. Supervisor opens Grafana, selects "AI Effectiveness" dashboard
2. Selects product line filter and date range
3. Sees: auto-resolution rate %, handoff rate %, top 20 questions, knowledge hit rate
4. Switches to "Agent Performance" dashboard
5. Sees: per-agent response time, volume, satisfaction score, current load
6. Drills down to specific agent or time period

---

## Acceptance Criteria

- [ ] Auto-resolution rate calculated: (AI-only conversations / total conversations) per product line
- [ ] Handoff rate calculated: (handoff conversations / total) per product line
- [ ] Top 20 questions ranked by frequency per product line
- [ ] Knowledge base hit rate: (Dify retrieval hits / total AI calls) per product line
- [ ] Agent performance: average response time per agent
- [ ] Agent performance: conversation volume per agent (daily/weekly)
- [ ] Agent performance: average satisfaction score per agent
- [ ] Daily/weekly trend charts in Grafana for all metrics
- [ ] REST API endpoints: `GET /api/v1/reports/ai-effectiveness`, `GET /api/v1/reports/agent-performance`
- [ ] Product line filter on all reports
- [ ] Date range filter (default: last 7 days)
- [ ] Query performance: report generation < 2s for 30-day range

---

## Technical Notes

### Components
- **Reporter service** (`reporter/`): New Go service, queries PostgreSQL
- **Database:** Read from existing `conversations`, `messages`, `customers` tables
- **Grafana:** New dashboards with PostgreSQL datasource (or via reporter API)

### Database Queries

**Auto-resolution rate:**
```sql
SELECT product_line_id,
  COUNT(*) FILTER (WHERE state = 'closed' AND handoff_count = 0) AS ai_resolved,
  COUNT(*) AS total,
  ROUND(COUNT(*) FILTER (WHERE state = 'closed' AND handoff_count = 0)::numeric / NULLIF(COUNT(*), 0), 4) AS resolution_rate
FROM conversations
WHERE created_at >= :start AND created_at < :end
GROUP BY product_line_id;
```

**Handoff rate:**
```sql
SELECT product_line_id,
  COUNT(*) FILTER (WHERE handoff_count > 0) AS handoffs,
  COUNT(*) AS total,
  ROUND(COUNT(*) FILTER (WHERE handoff_count > 0)::numeric / NULLIF(COUNT(*), 0), 4) AS handoff_rate
FROM conversations
WHERE created_at >= :start AND created_at < :end
GROUP BY product_line_id;
```

**Top questions (requires message content analysis):**
- Query first customer messages per conversation
- Group by similarity or use Dify's intent classification stored in conversation metadata

**Agent response time:**
```sql
SELECT assigned_agent_id,
  AVG(EXTRACT(EPOCH FROM first_agent_reply_at - handoff_at)) AS avg_response_seconds
FROM conversations
WHERE state IN ('human_processing', 'closed') AND handoff_at IS NOT NULL
  AND created_at >= :start AND created_at < :end
GROUP BY assigned_agent_id;
```

### API Endpoints
- `GET /api/v1/reports/ai-effectiveness?product_line=&start=&end=`
- `GET /api/v1/reports/agent-performance?product_line=&start=&end=`
- `GET /api/v1/reports/top-questions?product_line=&limit=20`
- `GET /api/v1/reports/channel-traffic?start=&end=` (for FR-023)

### Performance Optimization
- Create materialized view `mv_daily_conversation_stats` refreshed hourly
- Add indexes: `conversations(product_line_id, created_at)`, `conversations(assigned_agent_id, created_at)`
- Partition `messages` table by month if not already done

### Reporter Service Structure
```
reporter/
  cmd/reporter/main.go        -- HTTP server startup
  internal/
    config/config.go           -- DB connection, listen address
    handler/routes.go          -- HTTP route registration
    handler/ai_effectiveness.go
    handler/agent_performance.go
    handler/top_questions.go
    handler/channel_traffic.go
    repository/conversation.go -- DB query layer
    repository/metrics.go      -- Aggregation queries
```

---

## Dependencies

**Prerequisite Stories:**
- STORY-015: Conversation State Machine + DB (provides conversations/messages tables)
- STORY-028: Grafana deployed (for dashboard creation)

**External Dependencies:**
- PostgreSQL with conversation data (populated by router service)

---

## Definition of Done

- [ ] Reporter service implemented with HTTP server and API endpoints
- [ ] All 4 API endpoints returning correct data
- [ ] Unit tests for repository query logic (>80% coverage)
- [ ] Grafana dashboard: AI Effectiveness (resolution rate, handoff rate, knowledge hit rate, top questions)
- [ ] Grafana dashboard: Agent Performance (response time, volume, satisfaction)
- [ ] Product line and date range filters working
- [ ] Query performance validated < 2s for 30-day range
- [ ] Materialized views or indexes created for performance
- [ ] Reporter service deploys to K3s
- [ ] Dashboard JSON committed to `deploy/grafana/dashboards/`

---

## Story Points Breakdown

- **Reporter service scaffold + config:** 0.5 points
- **Repository layer (SQL queries):** 1.5 points
- **API handlers:** 1 point
- **Grafana dashboards (2):** 1 point
- **Performance optimization + testing:** 1 point
- **Total:** 5 points

**Rationale:** Heavy on SQL query design and data modeling. Reporter is a new service but lightweight (read-only HTTP API).

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created
- 2026-03-06: Completed - reporter service implemented with 4 API endpoints, tests passing, Grafana dashboards created

**Actual Effort:** 5 points (matched estimate)

**Implementation Notes:**
- Added migration 010 for handoff_count, handoff_at, first_agent_reply_at columns + materialized view
- Reporter service: config, repository, handler layers with 4 REST endpoints
- 14 unit tests passing across handler and repository packages
- 2 Grafana dashboards (AI Effectiveness, Agent Performance) with PostgreSQL datasource
- Query performance optimized via indexes on (product_line_id, created_at) and (assigned_agent_id, created_at)
- Materialized view mv_daily_conversation_stats for pre-aggregated daily stats

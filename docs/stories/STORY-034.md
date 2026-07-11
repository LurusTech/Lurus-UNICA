# STORY-034: Audit Logging

**Epic:** EPIC-006 (System Management & Permissions)
**Priority:** Could Have
**Story Points:** 3
**Status:** Completed
**Assigned To:** Unassigned
**Created:** 2026-03-06
**Sprint:** 6

---

## User Story

As a system admin,
I want all configuration changes logged,
So that I can trace who changed what and when.

---

## Description

### Background
With multiple admins managing channels, AI configurations, and user permissions across product lines, it's critical to maintain an audit trail. When something breaks after a config change, admins need to quickly identify what changed and who made the change. This story adds structured audit logging for all admin API write operations.

### Scope
**In scope:**
- Audit log table in PostgreSQL with before/after snapshots
- Middleware that automatically captures audit events for admin API mutations
- Searchable by actor, action type, resource type, time range
- 90-day retention with partition-based cleanup
- REST API for querying audit logs
- RBAC: only SuperAdmin and ProductAdmin can view audit logs

**Out of scope:**
- Real-time audit event streaming
- Audit log export (CSV/PDF)
- Tamper-proof logging (blockchain/append-only)
- Audit of read operations (too noisy)

### User Flow
1. SuperAdmin opens audit log viewer
2. Filters by: last 7 days, action type "update", resource type "ai_config"
3. Sees list: "KnowledgeAdmin updated system prompt for ProductLine-A at 2026-03-05 14:30"
4. Clicks entry to see before/after JSON diff
5. Identifies the change that caused the issue
6. Reverts the configuration

---

## Acceptance Criteria

- [ ] Audit log captures: actor (user_id + role), timestamp, action (create/update/delete), resource type, resource ID, before JSON, after JSON
- [ ] All admin API write operations logged: channel config, AI config, user/role management
- [ ] Searchable by actor (user_id), action type, resource type, time range
- [ ] API endpoint: `GET /api/v1/audit-logs?actor=&action=&resource_type=&start=&end=`
- [ ] Pagination support (limit/offset)
- [ ] 90-day retention: partitioned by month, old partitions dropped automatically
- [ ] RBAC enforced: SuperAdmin sees all, ProductAdmin sees own product line only

---

## Technical Notes

### Database Schema

```sql
CREATE TABLE audit_logs (
    id BIGSERIAL,
    actor_id UUID NOT NULL,
    actor_role VARCHAR(50) NOT NULL,
    action VARCHAR(20) NOT NULL CHECK (action IN ('create', 'update', 'delete')),
    resource_type VARCHAR(50) NOT NULL,
    resource_id VARCHAR(255) NOT NULL,
    product_line_id UUID,
    before_state JSONB,
    after_state JSONB,
    ip_address INET,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
) PARTITION BY RANGE (created_at);

-- Create monthly partitions
CREATE TABLE audit_logs_2026_03 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE audit_logs_2026_04 PARTITION OF audit_logs
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
-- ... auto-create future partitions via cron job or pg_partman

CREATE INDEX idx_audit_actor ON audit_logs (actor_id, created_at);
CREATE INDEX idx_audit_resource ON audit_logs (resource_type, resource_id, created_at);
CREATE INDEX idx_audit_product_line ON audit_logs (product_line_id, created_at);
```

### Middleware Approach

```go
// AuditMiddleware wraps admin handlers to capture before/after state
func AuditMiddleware(resourceType string, getBeforeState func(r *http.Request) (json.RawMessage, error)) func(http.Handler) http.Handler
```

For each auditable endpoint:
1. Before handler: capture current state (before)
2. Execute handler
3. After handler: capture new state (after)
4. Write audit log entry asynchronously (goroutine with buffered channel)

### Admin Service Structure
```
admin/internal/
  audit/
    logger.go          -- AuditLogger with async write
    logger_test.go
    middleware.go       -- HTTP middleware for auto-capture
    middleware_test.go
    repository.go      -- DB queries for audit_logs
    repository_test.go
  handler/
    audit_logs.go      -- GET /api/v1/audit-logs endpoint
    audit_logs_test.go
```

### Auditable Operations
| Resource Type | Actions | Endpoint Pattern |
|---------------|---------|-----------------|
| channel_config | create, update, delete | `/api/v1/channels/*` |
| ai_config | update | `/api/v1/ai-config/*` |
| user | create, update, delete | `/api/v1/users/*` |
| role_assignment | create, delete | `/api/v1/users/*/roles` |

### Partition Maintenance
- Use `pg_partman` extension or a cron job
- Script to create next month's partition and drop partitions > 90 days
- Add to `scripts/maintain_partitions.sql`

---

## Dependencies

**Prerequisite Stories:**
- STORY-031: RBAC Permission System (user auth + product line context)
- STORY-032: Channel Configuration CRUD (auditable operations)
- STORY-033: AI Agent Configuration UI (auditable operations)

**External Dependencies:**
- PostgreSQL partitioning support (built-in since PG 10)

---

## Definition of Done

- [ ] Audit log table created with monthly partitioning
- [ ] Middleware captures audit events for all admin write endpoints
- [ ] Before/after state captured correctly for update operations
- [ ] Query API endpoint working with filters and pagination
- [ ] Unit tests for logger, middleware, and repository (>80% coverage)
- [ ] RBAC enforced on audit log queries
- [ ] Partition maintenance script created
- [ ] Integration test: perform config change → verify audit log entry exists with correct before/after
- [ ] Admin service deploys with audit logging active

---

## Story Points Breakdown

- **Database schema + partitioning:** 0.5 points
- **Audit logger + middleware:** 1.5 points
- **Repository + query API:** 0.5 points
- **Integration with existing endpoints:** 0.5 points
- **Total:** 3 points

**Rationale:** Straightforward CRUD + middleware pattern. Partitioning is built-in PostgreSQL. Async logging keeps it non-blocking.

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created
- 2026-03-06: Implementation completed

**Actual Effort:** 3 points (matched estimate)

**Implementation Notes:**
- Audit log table with PARTITION BY RANGE on created_at (monthly partitions)
- Async logging via buffered channel (256 entries) with graceful shutdown
- HTTP middleware captures before/after state for all write operations
- RBAC enforced: SuperAdmin sees all, ProductAdmin sees own product lines only
- Query API supports filters: actor, action, resource_type, start, end + limit/offset pagination
- Partition maintenance SQL script for cron-based cleanup (90 days)
- 21 unit tests passing across audit and handler packages
- Added PermViewAuditLogs permission to RBAC matrix

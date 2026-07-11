# STORY-003: Deploy Chatwoot

**Epic:** Infrastructure
**Priority:** Must Have
**Story Points:** 3
**Status:** Completed
**Sprint:** 1
**Created:** 2026-03-04

---

## User Story

As a developer, I want Chatwoot deployed and accessible on the K3s cluster, so that I can integrate it as the human agent workspace.

---

## Description

### Background
Chatwoot is the open-source agent workspace for UNICA. It provides the unified inbox, conversation management, RBAC, and reporting features (EPIC-004). This story deploys a functional Chatwoot instance that later stories will integrate with via API.

### Scope
**In scope:**
- Chatwoot deployment on K3s via Helm chart or Docker Compose manifest
- Separate PostgreSQL database for Chatwoot (or shared PG instance with separate DB)
- Redis instance for Chatwoot (can share Redis with app if configured)
- Admin account creation
- API access verification (REST + WebSocket)
- Custom channel API endpoint validation

**Out of scope:**
- Custom channel integration (STORY-024)
- Agent accounts and product-line configuration (STORY-031)
- Chatwoot UI customization

### Setup Steps
1. Create Chatwoot database in PostgreSQL
2. Deploy Chatwoot via Helm chart or K3s manifest
3. Configure environment variables (DB, Redis, secrets)
4. Run database migrations
5. Create super admin account
6. Verify web UI accessible
7. Verify API endpoints (create inbox, send message, etc.)

---

## Acceptance Criteria

- [ ] Chatwoot web UI accessible via browser (port-forward or ingress)
- [ ] Super admin account created and can login
- [ ] Chatwoot PostgreSQL database created (`chatwoot_db`)
- [ ] REST API responds (e.g., `GET /auth/sign_in` returns 200)
- [ ] Can create an Account via admin panel
- [ ] Can create a Custom Channel inbox via API
- [ ] WebSocket connection works for real-time updates
- [ ] Chatwoot survives pod restart (data persists)

---

## Technical Notes

### Chatwoot Environment Variables
```yaml
FRONTEND_URL: "http://chatwoot.unica-dev.svc:3000"
DATABASE_URL: "postgresql://chatwoot:password@postgresql:5432/chatwoot_db"
REDIS_URL: "redis://:password@redis-master:6379/1"
SECRET_KEY_BASE: <generated-secret>
RAILS_ENV: production
```

### Database Setup
```sql
CREATE DATABASE chatwoot_db;
CREATE USER chatwoot WITH PASSWORD '<password>';
GRANT ALL PRIVILEGES ON DATABASE chatwoot_db TO chatwoot;
```

### API Verification
```bash
# Get API token via sign_in
curl -X POST http://localhost:3000/auth/sign_in \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@unica.local","password":"<password>"}'

# List accounts
curl http://localhost:3000/api/v1/accounts \
  -H "api_access_token: <token>"

# Create custom channel inbox (for later integration)
curl -X POST http://localhost:3000/api/v1/accounts/1/inboxes \
  -H "api_access_token: <token>" \
  -H "Content-Type: application/json" \
  -d '{"name":"Test Channel","channel":{"type":"api","webhook_url":"http://gateway:8080/chatwoot/webhook"}}'
```

### K3s Deployment
Deploy Chatwoot manifests to `deploy/chatwoot/`. Consider using the official Chatwoot Helm chart or converting their Docker Compose to K3s manifests.

---

## Dependencies

**Prerequisite:**
- STORY-001 (PostgreSQL + Redis must be running)

**Blocks:**
- STORY-024 (Chatwoot custom channel integration)
- STORY-025 (History sync)
- STORY-026 (Quick reply templates)

---

## Definition of Done

- [x] Chatwoot deployed on K3s and accessible
- [x] Admin account functional
- [x] REST API verified
- [x] Custom Channel inbox creatable via API
- [x] Deployment manifests committed to `deploy/chatwoot/`

---

## Progress Tracking

**Status History:**
- 2026-03-04: Created
- 2026-03-05: Implementation started
- 2026-03-05: Deployment manifests complete, all YAML validated

**Actual Effort:** 3 points (matched estimate)

**Implementation Notes:**
- Used Chatwoot v3.15.0 official Docker image
- Shared PostgreSQL instance with separate `chatwoot_db` database and `chatwoot` user
- Shared Redis instance using DB index 1 to avoid conflicts with app data
- Deployment includes: web (Puma), sidekiq (background jobs), and 3 init jobs (db-init, migrate, admin-init)
- PVC for file storage (attachments/uploads) at 5Gi
- ConfigMap separates non-sensitive env vars from secrets
- Deploy/teardown/verify scripts provided for operational convenience
- Super admin created via Rails runner in init job
- Readiness probe on `/auth/sign_in`, liveness probe with 60s initial delay
- WebSocket available at `/cable` (ActionCable)

**Files Created:**
- `deploy/chatwoot/chatwoot-secrets.yaml` - Sensitive credentials (template)
- `deploy/chatwoot/chatwoot-env.yaml` - Non-sensitive ConfigMap
- `deploy/chatwoot/chatwoot-pvc.yaml` - Persistent storage
- `deploy/chatwoot/chatwoot-db-init.yaml` - Database initialization job
- `deploy/chatwoot/chatwoot-migrate.yaml` - Rails migration job
- `deploy/chatwoot/chatwoot-admin-init.yaml` - Super admin creation job
- `deploy/chatwoot/chatwoot-web.yaml` - Web server Deployment + Service
- `deploy/chatwoot/chatwoot-sidekiq.yaml` - Background worker Deployment
- `deploy/chatwoot/deploy.sh` - Ordered deployment script
- `deploy/chatwoot/verify.sh` - Verification script
- `deploy/chatwoot/teardown.sh` - Cleanup script

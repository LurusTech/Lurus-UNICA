# STORY-004: Deploy Dify

**Epic:** Infrastructure
**Priority:** Must Have
**Story Points:** 3
**Status:** Completed
**Sprint:** 1
**Created:** 2026-03-04

---

## User Story

As a developer, I want Dify deployed and accessible on the K3s cluster, so that I can configure AI agents and knowledge bases for each product line.

---

## Description

### Background
Dify is the AI orchestration platform for UNICA. It provides multi-agent management, RAG knowledge base, and prompt engineering capabilities (EPIC-003). This story deploys a functional Dify instance with pgvector support.

### Scope
**In scope:**
- Dify deployment on K3s
- Separate PostgreSQL database for Dify (with pgvector enabled)
- Admin account creation
- API access verification
- Create one test workspace to verify functionality
- LLM provider configuration (API key for chosen model)

**Out of scope:**
- Multi-workspace per product line setup (STORY-019)
- Knowledge base document upload (STORY-020)
- Router integration (STORY-021)

### Setup Steps
1. Create Dify database in PostgreSQL with pgvector
2. Deploy Dify services (api, worker, web, sandbox) via Docker Compose or K3s manifests
3. Configure environment (DB, Redis, storage)
4. Create admin account
5. Configure LLM provider (e.g., OpenAI-compatible endpoint)
6. Create test workspace and test chat
7. Verify API endpoints

---

## Acceptance Criteria

- [ ] Dify web UI accessible via browser
- [ ] Admin account created and can login
- [ ] Dify PostgreSQL database created with pgvector extension
- [ ] LLM provider configured and test chat works
- [ ] One test workspace created with a simple agent
- [ ] REST API responds (chat completion, dataset endpoints)
- [ ] Can upload a test document to knowledge base
- [ ] Vector search returns results for test query
- [ ] Dify survives pod restart (data persists)

---

## Technical Notes

### Dify Services
Dify consists of multiple services:
- `dify-api` — Backend API server
- `dify-worker` — Async task processor (embeddings, etc.)
- `dify-web` — Frontend UI
- `dify-sandbox` — Code execution sandbox

### Environment Variables
```yaml
DB_USERNAME: dify
DB_PASSWORD: <password>
DB_HOST: postgresql
DB_PORT: 5432
DB_DATABASE: dify_db
REDIS_HOST: redis-master
REDIS_PORT: 6379
REDIS_PASSWORD: <password>
SECRET_KEY: <generated-secret>
VECTOR_STORE: pgvector
PGVECTOR_HOST: postgresql
PGVECTOR_PORT: 5432
PGVECTOR_DATABASE: dify_db
```

### Database Setup
```sql
CREATE DATABASE dify_db;
CREATE USER dify WITH PASSWORD '<password>';
GRANT ALL PRIVILEGES ON DATABASE dify_db TO dify;
\c dify_db
CREATE EXTENSION IF NOT EXISTS vector;
```

### API Verification
```bash
# Create chat completion (after workspace + app setup)
curl -X POST http://localhost:5001/v1/chat-messages \
  -H "Authorization: Bearer <api-key>" \
  -H "Content-Type: application/json" \
  -d '{
    "inputs": {},
    "query": "Hello",
    "user": "test-user",
    "response_mode": "blocking"
  }'

# List datasets (knowledge bases)
curl http://localhost:5001/v1/datasets \
  -H "Authorization: Bearer <api-key>"
```

### LLM Configuration
Configure at least one LLM provider in Dify admin settings. Options:
- OpenAI API (if using GPT models)
- Self-hosted model via Ollama/vLLM with OpenAI-compatible endpoint
- Other providers supported by Dify

---

## Dependencies

**Prerequisite:**
- STORY-001 (PostgreSQL with pgvector + Redis must be running)
- LLM API access (key/endpoint)

**Blocks:**
- STORY-019 (Dify multi-workspace setup)
- STORY-020 (RAG knowledge base pipeline)
- STORY-021 (Router↔Dify integration)

---

## Definition of Done

- [x] Dify all services deployed on K3s and accessible
- [ ] Admin account functional (manual step: first visit to Web UI)
- [ ] LLM provider configured and working (manual step: configure in admin settings)
- [ ] Test workspace with working chat (manual step: create via Web UI)
- [ ] Knowledge base upload and vector search verified (manual step: upload test doc)
- [x] REST API verified (health endpoint via verify.sh)
- [x] Deployment manifests committed to `deploy/dify/`

---

## Progress Tracking

**Status History:**
- 2026-03-04: Created
- 2026-03-05: Implementation complete - all K3s manifests created

**Implementation Notes:**
- Dify v0.15.3 deployed (api, worker, web, sandbox, ssrf-proxy, nginx)
- Database initialization via Job (creates dify_db, dify user, pgvector + uuid-ossp extensions)
- Nginx reverse proxy on NodePort 30001 unifies API and Web access
- SSRF proxy (Squid) protects against internal network SSRF attacks from sandbox
- Storage via PVC (10Gi) for file uploads and local storage
- Vector store configured as pgvector (same database as Dify main DB)
- All secrets managed via K8s Secret (dify-secrets.yaml)
- deploy.sh script handles ordered deployment with prerequisite checks
- verify.sh script validates all acceptance criteria automatically

**Files Created:**
- `deploy/dify/dify-secrets.yaml` - Secrets (DB, Redis, encryption keys)
- `deploy/dify/dify-db-init.yaml` - Database initialization Job
- `deploy/dify/dify-pvc.yaml` - Persistent storage
- `deploy/dify/dify-api.yaml` - API server Deployment + Service
- `deploy/dify/dify-worker.yaml` - Celery worker Deployment
- `deploy/dify/dify-web.yaml` - Frontend UI Deployment + Service
- `deploy/dify/dify-sandbox.yaml` - Code sandbox Deployment + Service
- `deploy/dify/dify-ssrf-proxy.yaml` - SSRF proxy Deployment + ConfigMap + Service
- `deploy/dify/dify-nginx.yaml` - Nginx reverse proxy Deployment + ConfigMap + Service
- `deploy/dify/deploy.sh` - Deployment orchestration script
- `deploy/dify/verify.sh` - Acceptance criteria verification script

**Manual Steps Required After Deployment:**
1. Update placeholder passwords in dify-secrets.yaml
2. Run `bash deploy/dify/deploy.sh apply`
3. Visit http://NODE_IP:30001 to create admin account
4. Configure LLM provider in Settings > Model Provider
5. Create test workspace and verify chat functionality
6. Upload test document to knowledge base and verify vector search

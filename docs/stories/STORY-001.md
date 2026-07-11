# STORY-001: K3s Cluster + PostgreSQL + Redis Setup

**Epic:** Infrastructure
**Priority:** Must Have
**Story Points:** 5
**Status:** Completed
**Sprint:** 1
**Created:** 2026-03-04

---

## User Story

As a developer, I want the K3s cluster with PostgreSQL and Redis deployed and configured, so that all application services have their runtime dependencies available.

---

## Description

### Background
UNICA runs entirely on a K3s cluster with PostgreSQL (+ pgvector) as the primary database and Redis as the cache/message backbone. This is the foundational infrastructure that all other stories depend on. Nothing can be developed or tested without these services running.

### Scope
**In scope:**
- K3s cluster provisioning (single-node dev or multi-node)
- PostgreSQL 16 deployment with pgvector extension
- Redis 7 deployment with Streams enabled
- Persistent volume configuration
- K8s Secrets for connection strings
- Basic namespace setup (unica-dev)
- Network policies for inter-service communication

**Out of scope:**
- High-availability PostgreSQL (streaming replication) — production concern
- Redis Sentinel — production concern
- Monitoring stack (Prometheus/Grafana) — Sprint 6
- CI/CD pipeline — separate story

### Setup Steps
1. Provision K3s cluster (or configure existing nodes)
2. Create `unica-dev` namespace
3. Deploy PostgreSQL via Helm chart with pgvector extension
4. Deploy Redis via Helm chart with Streams enabled
5. Configure persistent volumes for both services
6. Store connection credentials as K8s Secrets
7. Verify connectivity from a test pod

---

## Acceptance Criteria

- [ ] K3s cluster is running and kubectl commands succeed
- [ ] `unica-dev` namespace created
- [ ] PostgreSQL 16 deployed and accepting connections
- [ ] pgvector extension installed (`CREATE EXTENSION vector;` succeeds)
- [ ] Redis 7 deployed and accepting connections
- [ ] Redis Streams functional (`XADD` / `XREAD` commands work)
- [ ] Persistent volumes configured — data survives pod restart
- [ ] Connection strings stored as K8s Secrets in `unica-dev` namespace
- [ ] Test pod can connect to both PostgreSQL and Redis using secrets

---

## Technical Notes

### Components
- K3s cluster (single-node for dev, multi-node for staging/prod)
- PostgreSQL 16 (Helm: bitnami/postgresql)
- Redis 7 (Helm: bitnami/redis)

### Helm Values (PostgreSQL)
```yaml
image:
  tag: "16"
auth:
  postgresPassword: <from-secret>
  database: unica_core
primary:
  persistence:
    size: 10Gi
  initdb:
    scripts:
      init-pgvector.sql: |
        CREATE EXTENSION IF NOT EXISTS vector;
```

### Helm Values (Redis)
```yaml
image:
  tag: "7"
auth:
  password: <from-secret>
master:
  persistence:
    size: 5Gi
  configuration: |
    maxmemory 512mb
    maxmemory-policy allkeys-lru
```

### Databases to Create
```sql
-- Main application database
CREATE DATABASE unica_core;
-- Chatwoot will use its own DB (created during Chatwoot deploy)
-- Dify will use its own DB (created during Dify deploy)
```

### Verification Commands
```bash
# PostgreSQL
kubectl exec -it postgresql-0 -n unica-dev -- psql -U postgres -d unica_core -c "SELECT 1;"
kubectl exec -it postgresql-0 -n unica-dev -- psql -U postgres -d unica_core -c "CREATE EXTENSION IF NOT EXISTS vector; SELECT extversion FROM pg_extension WHERE extname='vector';"

# Redis
kubectl exec -it redis-master-0 -n unica-dev -- redis-cli ping
kubectl exec -it redis-master-0 -n unica-dev -- redis-cli XADD test_stream '*' key value
kubectl exec -it redis-master-0 -n unica-dev -- redis-cli XREAD COUNT 1 STREAMS test_stream 0
```

---

## Dependencies

**Prerequisite:**
- K3s-compatible server nodes provisioned (cloud VMs or bare metal)
- Helm 3 installed
- kubectl configured

**Blocks:**
- STORY-002 (Go scaffolding needs DB connection config)
- STORY-003 (Chatwoot needs PostgreSQL)
- STORY-004 (Dify needs PostgreSQL + pgvector)
- STORY-005 (Gateway needs Redis Streams)
- All subsequent stories

---

## Definition of Done

- [ ] K3s cluster operational
- [ ] PostgreSQL deployed with pgvector, data persists across pod restarts
- [ ] Redis deployed with Streams, data persists across pod restarts
- [ ] Secrets created and verified
- [ ] Connectivity verified from test pod
- [ ] Helm values committed to `deploy/postgresql/` and `deploy/redis/`

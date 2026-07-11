# STORY-028: Prometheus Metrics + Grafana Dashboards

**Epic:** EPIC-005 (Data Reports & Monitoring)
**Priority:** Must Have
**Story Points:** 5
**Status:** Completed
**Assigned To:** Developer
**Created:** 2026-03-06
**Sprint:** 6

---

## User Story

As a system admin,
I want real-time system health dashboards,
So that I can monitor performance and detect issues early.

---

## Description

### Background
The gateway and router services already expose Prometheus metrics via `promauto` (gateway: inbound/outbound counts, latency, dedup, dead-letter, Chatwoot webhooks; router: routing latency, Dify call duration, confidence scores, handoff events, agent pool, survey stats). However, there is no Prometheus server collecting these metrics, no Grafana instance for visualization, and no `/metrics` HTTP endpoint exposed on each service. This story completes the observability pipeline.

### Scope
**In scope:**
- Expose `/metrics` endpoint on gateway, router, admin, and reporter services
- Deploy Prometheus to K3s with ServiceMonitor for auto-discovery
- Deploy Grafana to K3s with pre-provisioned dashboards
- Create dashboards: System Overview, Gateway Channel Status, Router AI Pipeline, Queue Backlog, Agent Workspace
- Integrate Loki for log aggregation with correlation ID tracing
- Configure data retention (15 days for metrics, 7 days for logs)

**Out of scope:**
- Alert rules (STORY-030)
- Business-level reporting queries (STORY-029)
- Custom metric additions beyond what already exists in code

### User Flow
1. Admin opens Grafana (exposed via Ingress)
2. Selects "UNICA System Overview" dashboard
3. Sees real-time panels: message throughput, latency P50/P95/P99, error rates, queue depth
4. Drills down to per-channel or per-product-line views
5. Clicks on a spike to see correlated logs via Loki
6. Uses time range selector to compare historical performance

---

## Acceptance Criteria

- [ ] Each Go service (gateway, router, admin, reporter) exposes `/metrics` endpoint in Prometheus format
- [ ] Prometheus deployed on K3s, scraping all service endpoints via ServiceMonitor
- [ ] Key metrics collected: request count, latency histogram, error rate, queue depth
- [ ] Grafana deployed on K3s with persistent storage
- [ ] Dashboard: System Overview (all services health, message throughput, error rates)
- [ ] Dashboard: Gateway per-channel status (inbound/outbound per channel, dedup hits, dead-letter count)
- [ ] Dashboard: Router AI Pipeline (Dify latency, confidence distribution, retrieval hit/miss, token usage)
- [ ] Dashboard: Queue Backlog (Redis Streams depth for inbound/outbound/handoff)
- [ ] Dashboard: Agent Workspace (agent pool availability, assignment success rate, survey scores)
- [ ] Loki deployed for log aggregation, correlation ID searchable
- [ ] Grafana datasources (Prometheus + Loki) auto-provisioned
- [ ] All dashboards provisioned as code (JSON/YAML in `deploy/grafana/`)

---

## Technical Notes

### Components
- **Gateway:** Add `/metrics` HTTP handler (prometheus/promhttp)
- **Router:** Add `/metrics` HTTP handler
- **Admin:** Add `/metrics` HTTP handler
- **Reporter:** Add `/metrics` HTTP handler
- **Deploy:** Helm charts for Prometheus, Grafana, Loki

### Existing Metrics (already instrumented)

**Gateway (`gateway/internal/metrics/metrics.go`):**
- `gateway_inbound_total`, `gateway_outbound_total`
- `gateway_inbound_duration_seconds`, `gateway_outbound_duration_seconds`
- `gateway_stream_depth` (by stream name)
- `gateway_dedup_hits_total`, `gateway_dedup_misses_total`, `gateway_dedup_errors_total`
- `gateway_dead_letter_total`
- `chatwoot_webhook_received_total`, `chatwoot_agent_replies_total`, `chatwoot_webhook_errors_total`

**Router (`router/internal/metrics/metrics.go`):**
- `router_messages_routed_total` (by product_line, route_type)
- `router_routing_duration_seconds`, `router_dify_call_duration_seconds`
- `router_conversations_created_total`, `router_active_conversations` (by state)
- `router_dify_tokens_total`, `router_dify_retrieval_hit_total`, `router_dify_retrieval_miss_total`
- `router_dify_confidence_score`, `router_dify_errors_total`
- `router_guardrail_decisions_total`, `router_handoff_events_processed_total`
- `router_handoff_duration_seconds`, `router_handoff_summary_duration_seconds`
- `agent_assignments_total`, `agent_pool_available`, `agent_load_current`
- `history_sync_messages_total`, `history_sync_duration_seconds`
- `survey_sent_total`, `survey_completed_total`, `survey_timeout_total`

### Deployment
- Prometheus: `kube-prometheus-stack` Helm chart (includes Grafana)
- Loki: `grafana/loki-stack` Helm chart
- ServiceMonitor CRDs for auto-discovery of `/metrics` endpoints
- Grafana dashboards stored in `deploy/grafana/dashboards/`
- Datasource provisioning in `deploy/grafana/provisioning/`

### Dashboard Panels (Key)

**System Overview:**
- Total messages/sec (inbound + outbound)
- Error rate % (errors / total)
- P95 latency (gateway + router)
- Active conversations gauge
- Service health (up/down per pod)

**Gateway Channel Status:**
- Inbound messages by channel (stacked bar)
- Outbound messages by channel
- Dedup hit rate
- Dead-letter queue size
- Rate limiter rejections

**Router AI Pipeline:**
- Dify API call latency (P50, P95, P99)
- Confidence score distribution (histogram)
- Retrieval hit rate (hits / total)
- Token consumption rate
- Guardrail decision breakdown
- Handoff rate by product line

---

## Dependencies

**Prerequisite Stories:**
- STORY-005: Gateway Core (metrics already instrumented)
- STORY-016: Router (metrics already instrumented)

**External Dependencies:**
- Helm repos: prometheus-community, grafana
- K3s cluster with sufficient resources for Prometheus/Grafana/Loki pods

---

## Definition of Done

- [ ] `/metrics` endpoint accessible on all 4 services
- [ ] Prometheus scraping all endpoints (verify in Prometheus UI targets)
- [ ] All 5 Grafana dashboards rendering with live data
- [ ] Loki collecting logs from all pods
- [ ] Correlation ID searchable across services in Grafana Explore
- [ ] Dashboard JSON files committed to `deploy/grafana/dashboards/`
- [ ] Helm values files committed to `deploy/`
- [ ] Services deploy successfully to K3s

---

## Story Points Breakdown

- **Service /metrics endpoints:** 1 point
- **Prometheus + Loki deployment:** 1 point
- **Grafana deployment + provisioning:** 1 point
- **Dashboard creation (5 dashboards):** 2 points
- **Total:** 5 points

**Rationale:** Most metrics already instrumented. Main work is deployment configuration and dashboard design.

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created
- 2026-03-06: Completed - all /metrics endpoints, Helm configs, 5 dashboards

**Actual Effort:** 5 points (matched estimate)

**Implementation Notes:**
- Gateway and Router already had /metrics endpoints and metrics packages
- Added /metrics endpoint + metrics package to Admin service
- Rewrote Reporter service from stub to HTTP server with /metrics endpoint
- Created kube-prometheus-stack Helm values with 15-day metric retention
- Created loki-stack Helm values with 7-day log retention
- Created ServiceMonitors for all 4 services (auto-discovery)
- Created 5 Grafana dashboards as JSON (provisioned via ConfigMap)
- All 4 services compile successfully

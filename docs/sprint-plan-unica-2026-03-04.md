# Sprint Plan: UNICA

**Date:** 2026-03-04
**Scrum Master:** BMAD
**Project Level:** 3
**Total Stories:** 34
**Total Points:** 155
**Planned Sprints:** 6 (12 weeks)
**Team:** 1 Senior Developer, 30 pts/sprint capacity

---

## Executive Summary

UNICA implementation is planned across 6 two-week sprints, progressing from infrastructure foundation through full multi-channel AI customer service capability. The plan follows a dependency-driven sequence: infrastructure → first channel (WeChat) → AI integration → channel expansion → admin/permissions → monitoring/reporting.

**Key Metrics:**
| Metric | Value |
|--------|-------|
| Total Stories | 34 |
| Total Points | 155 |
| Sprints | 6 |
| Capacity | 30 points/sprint |
| Buffer | 10-20% per sprint |
| Target Completion | ~12 weeks |

---

## Story Inventory

### Infrastructure Stories

#### STORY-001: K3s Cluster + PostgreSQL + Redis Setup
**Epic:** Infrastructure
**Priority:** Must Have
**Points:** 5

**User Story:**
As a developer, I want the K3s cluster with PostgreSQL and Redis deployed, so that all services have their runtime dependencies available.

**Acceptance Criteria:**
- [ ] K3s cluster running (multi-node or single-node dev)
- [ ] PostgreSQL 16 with pgvector extension deployed and accessible
- [ ] Redis 7 deployed with Streams enabled
- [ ] Persistent volumes configured for PG and Redis
- [ ] Connection strings documented and stored as K8s Secrets

**Technical Notes:** Use Helm charts for PG (bitnami/postgresql) and Redis (bitnami/redis). Enable pgvector extension post-install.

---

#### STORY-002: Go Monorepo Scaffolding
**Epic:** Infrastructure
**Priority:** Must Have
**Points:** 3

**User Story:**
As a developer, I want the Go monorepo structure with shared packages and build scripts, so that I can start implementing services.

**Acceptance Criteria:**
- [ ] Monorepo structure per architecture doc (gateway/, router/, admin/, reporter/)
- [ ] Shared model package (pkg/model/) with StandardMessage type
- [ ] Dockerfile template per service
- [ ] Makefile with build/test/lint targets
- [ ] go.work workspace configured

**Technical Notes:** Follow architecture code organization. Use Go 1.22+.

---

#### STORY-003: Deploy Chatwoot
**Epic:** Infrastructure
**Priority:** Must Have
**Points:** 3

**User Story:**
As a developer, I want Chatwoot deployed and accessible, so that I can integrate it as the agent workspace.

**Acceptance Criteria:**
- [ ] Chatwoot deployed on K3s with Helm chart
- [ ] Admin account created
- [ ] API access verified (REST + WebSocket)
- [ ] Connected to PostgreSQL (separate database)
- [ ] Custom channel API endpoint accessible

---

#### STORY-004: Deploy Dify
**Epic:** Infrastructure
**Priority:** Must Have
**Points:** 3

**User Story:**
As a developer, I want Dify deployed and accessible, so that I can configure AI agents and knowledge bases.

**Acceptance Criteria:**
- [ ] Dify deployed on K3s
- [ ] Admin account created
- [ ] API access verified
- [ ] Connected to PostgreSQL (pgvector enabled)
- [ ] At least one test workspace created

---

### EPIC-001: Multi-Channel Gateway

#### STORY-005: Gateway Core - Redis Streams Producer/Consumer
**Epic:** EPIC-001
**Priority:** Must Have
**Points:** 5

**User Story:**
As a system, I want the gateway to publish inbound messages to Redis Streams and consume outbound replies, so that message processing is asynchronous and resilient.

**Acceptance Criteria:**
- [ ] Gateway service starts and connects to Redis Streams
- [ ] Inbound messages published to `unica:inbound` stream
- [ ] Outbound messages consumed from `unica:outbound` stream
- [ ] Consumer group created with proper acknowledgment
- [ ] CloudEvents-compatible message envelope implemented
- [ ] Health check endpoint (/healthz) verifies Redis connectivity

**Technical Notes:** Use go-redis/v9 Streams API. Implement graceful shutdown with pending message drain.

**Dependencies:** STORY-001 (Redis), STORY-002 (scaffolding)

---

#### STORY-006: Standard Message Format + Adapter Interface
**Epic:** EPIC-001
**Priority:** Must Have
**Points:** 3

**User Story:**
As a developer, I want a defined standard message format and adapter interface, so that all channel adapters follow a consistent contract.

**Acceptance Criteria:**
- [ ] StandardMessage JSON schema defined (text, image, video, link types)
- [ ] ChannelAdapter Go interface defined (VerifyWebhook, ParseInbound, FormatOutbound, SendMessage)
- [ ] Platform metadata preserved in message envelope
- [ ] Unit tests for message serialization/deserialization

**Dependencies:** STORY-002 (scaffolding)

---

#### STORY-007: WeChat Adapter
**Epic:** EPIC-001
**Priority:** Must Have
**Points:** 8

**User Story:**
As a customer on WeChat, I want my messages received and replies delivered through the official account, so that I can get customer service on my preferred platform.

**Acceptance Criteria:**
- [ ] Webhook endpoint receives WeChat message push
- [ ] Signature verification (SHA1) passes
- [ ] XML message parsing for text/image/voice/video/link types
- [ ] Messages converted to StandardMessage format
- [ ] Outbound messages converted to WeChat XML format
- [ ] Reply sent via WeChat customer service API
- [ ] Integration test with WeChat sandbox

**Technical Notes:** WeChat uses XML format + SHA1 signature. Implement AES encryption/decryption for encrypted mode.

**Dependencies:** STORY-005, STORY-006

---

#### STORY-008: Douyin Adapter
**Epic:** EPIC-001
**Priority:** Must Have
**Points:** 5

**User Story:**
As a customer on Douyin, I want my messages received and replies delivered, so that I can get customer service on Douyin.

**Acceptance Criteria:**
- [ ] Webhook endpoint receives Douyin message push
- [ ] Signature verification passes per Douyin spec
- [ ] JSON message parsing for supported message types
- [ ] Messages converted to StandardMessage format
- [ ] Outbound reply via Douyin Open API
- [ ] Integration test with Douyin sandbox

**Dependencies:** STORY-005, STORY-006

---

#### STORY-009: Message Deduplication + Dead-Letter Queue
**Epic:** EPIC-001
**Priority:** Must Have
**Points:** 3

**User Story:**
As a system, I want duplicate messages discarded and permanently failed messages routed to a dead-letter queue, so that processing is idempotent and failures are recoverable.

**Acceptance Criteria:**
- [ ] Dedup via Redis SET NX with platform_msg_id key (TTL 24h)
- [ ] Duplicate messages logged and discarded
- [ ] Failed messages retried 3x with exponential backoff
- [ ] After 3 failures, message moved to dead-letter stream
- [ ] Dead-letter messages queryable for manual inspection

**Dependencies:** STORY-005

---

#### STORY-010: Token Auto-Management
**Epic:** EPIC-001
**Priority:** Must Have
**Points:** 3

**User Story:**
As a system, I want platform Access Tokens automatically refreshed before expiration, so that API calls never fail due to expired tokens.

**Acceptance Criteria:**
- [ ] Token stored in Redis with TTL = expiry - 5min buffer
- [ ] Refresh triggered when TTL < buffer threshold
- [ ] Concurrent refresh requests deduplicated (singleflight)
- [ ] Token refresh failure triggers alert event
- [ ] Works for WeChat, Douyin (extensible to other platforms)

**Dependencies:** STORY-001 (Redis)

---

#### STORY-011: Rate Limiting + Anti-Abuse
**Epic:** EPIC-001
**Priority:** Should Have
**Points:** 3

**User Story:**
As a system, I want per-channel API rate limiting and anti-abuse protection, so that platform rate limits are respected and malicious requests are blocked.

**Acceptance Criteria:**
- [ ] Sliding window rate limiter in Redis per channel
- [ ] Configurable limits per channel type
- [ ] 429 response with Retry-After header on exceeded
- [ ] Burst detection (abnormal request patterns) with throttling

**Dependencies:** STORY-005

---

#### STORY-012: Xiaohongshu Adapter
**Epic:** EPIC-001
**Priority:** Must Have
**Points:** 5

**User Story:**
As a customer on Xiaohongshu, I want my messages received and replies delivered, so that I can get customer service on Xiaohongshu.

**Acceptance Criteria:**
- [ ] Webhook endpoint receives XHS message push
- [ ] Signature verification per XHS spec
- [ ] Message parsing and StandardMessage conversion
- [ ] Outbound reply via XHS API
- [ ] Integration test

**Dependencies:** STORY-005, STORY-006

---

#### STORY-013: Taobao Adapter
**Epic:** EPIC-001
**Priority:** Must Have
**Points:** 5

**User Story:**
As a customer on Taobao, I want my messages received and replies delivered, so that I can get customer service on Taobao.

**Acceptance Criteria:**
- [ ] Webhook/polling for Taobao messages (per Taobao API spec)
- [ ] Signature verification per Taobao spec
- [ ] Message parsing and StandardMessage conversion
- [ ] Outbound reply via Taobao API
- [ ] Integration test

**Dependencies:** STORY-005, STORY-006

---

#### STORY-014: Kuaishou Adapter
**Epic:** EPIC-001
**Priority:** Must Have
**Points:** 5

**User Story:**
As a customer on Kuaishou, I want my messages received and replies delivered, so that I can get customer service on Kuaishou.

**Acceptance Criteria:**
- [ ] Webhook endpoint receives Kuaishou message push
- [ ] Signature verification per Kuaishou spec
- [ ] Message parsing and StandardMessage conversion
- [ ] Outbound reply via Kuaishou API
- [ ] Integration test

**Dependencies:** STORY-005, STORY-006

---

### EPIC-002: Conversation Routing & Management

#### STORY-015: Conversation State Machine + DB Schema
**Epic:** EPIC-002
**Priority:** Must Have
**Points:** 5

**User Story:**
As a system, I want conversations tracked through defined lifecycle states with persistent storage, so that conversation flow is reliable and auditable.

**Acceptance Criteria:**
- [ ] DB schema: conversations, messages, customers tables created
- [ ] State machine: Pending → AI_Processing → Human_Processing → Closed
- [ ] Invalid state transitions rejected
- [ ] Idle timeout auto-closure (configurable)
- [ ] State changes logged with timestamps
- [ ] Session state cached in Redis for fast lookup

**Dependencies:** STORY-001 (PostgreSQL), STORY-002

---

#### STORY-016: Intelligent Routing - Product Line Identification + AI Dispatch
**Epic:** EPIC-002
**Priority:** Must Have
**Points:** 5

**User Story:**
As a system, I want inbound messages automatically routed to the correct product line AI Agent, so that customers receive product-specific responses.

**Acceptance Criteria:**
- [ ] Channel-to-product-line mapping stored in DB, cached in Redis
- [ ] Router consumes from `unica:inbound` stream
- [ ] New conversations created and routed to correct Dify Agent
- [ ] Existing conversations continue with same agent
- [ ] Unrecognized conversations routed to default handler
- [ ] Routing latency < 50ms

**Dependencies:** STORY-005, STORY-015

---

#### STORY-017: AI→Human Handoff with Context
**Epic:** EPIC-002
**Priority:** Must Have
**Points:** 5

**User Story:**
As a human agent, I want to see the full AI conversation history and intent summary when a conversation is handed off, so that I can continue without asking the customer to repeat.

**Acceptance Criteria:**
- [ ] Handoff triggered when AI confidence < threshold
- [ ] Handoff triggered by user keyword ("转人工")
- [ ] Full conversation history sent to Chatwoot
- [ ] AI-generated intent summary attached
- [ ] Handoff event published to `unica:handoff` stream
- [ ] Conversation state transitions to Human_Processing

**Dependencies:** STORY-015, STORY-016, STORY-021

---

#### STORY-018: Agent Scheduling + Distribution
**Epic:** EPIC-002
**Priority:** Should Have
**Points:** 5

**User Story:**
As a supervisor, I want conversations distributed to agents based on product line, availability, and current load, so that workload is balanced.

**Acceptance Criteria:**
- [ ] Agents tagged with product line assignments
- [ ] Round-robin distribution within product line team
- [ ] Offline agents excluded from routing
- [ ] Max concurrent conversations enforced per agent
- [ ] Assignment reflected in Chatwoot

**Dependencies:** STORY-017

---

### EPIC-003: AI Smart Response & Knowledge Base

#### STORY-019: Dify Multi-Workspace Setup
**Epic:** EPIC-003
**Priority:** Must Have
**Points:** 3

**User Story:**
As a knowledge admin, I want each product line to have an isolated AI workspace in Dify, so that knowledge bases and prompts don't interfere between products.

**Acceptance Criteria:**
- [ ] One Dify workspace per product line (7-8 workspaces)
- [ ] Each workspace has independent system prompt
- [ ] Each workspace has isolated knowledge base (dataset)
- [ ] Cross-workspace data access verified blocked
- [ ] API keys generated per workspace

**Dependencies:** STORY-004 (Dify deployed)

---

#### STORY-020: RAG Knowledge Base + Document Upload Pipeline
**Epic:** EPIC-003
**Priority:** Must Have
**Points:** 5

**User Story:**
As a knowledge admin, I want to upload product documents and have them automatically vectorized for AI retrieval, so that the AI can answer product-specific questions.

**Acceptance Criteria:**
- [ ] Upload supports PDF, DOCX, TXT, MD formats via Dify API
- [ ] Documents auto-chunked and vectorized (pgvector)
- [ ] Update replaces vectors without affecting other docs
- [ ] Delete removes all associated vectors
- [ ] Verified: AI answers improve after document upload

**Dependencies:** STORY-019

---

#### STORY-021: Router↔Dify Integration - Message Flow
**Epic:** EPIC-003
**Priority:** Must Have
**Points:** 5

**User Story:**
As a customer, I want AI responses powered by product knowledge, so that I receive accurate answers to my product questions.

**Acceptance Criteria:**
- [ ] Router calls Dify chat API with customer message + conversation context
- [ ] Dify retrieves relevant knowledge chunks via RAG
- [ ] AI response returned with confidence score
- [ ] Response published to `unica:outbound` stream
- [ ] Full round-trip: customer message → AI response in <3s

**Dependencies:** STORY-016, STORY-019

---

#### STORY-022: Confidence Scoring + Auto-Handoff Logic
**Epic:** EPIC-003
**Priority:** Must Have
**Points:** 3

**User Story:**
As a system, I want AI responses evaluated for confidence and automatically handed off to humans when uncertain, so that customers never receive incorrect answers.

**Acceptance Criteria:**
- [ ] Confidence score extracted from Dify response metadata
- [ ] Configurable threshold per product line (default 0.7)
- [ ] Below threshold: auto-trigger handoff (FR-007)
- [ ] Sensitive topic detection blocks AI response
- [ ] Guardrail rules configurable per product line

**Dependencies:** STORY-021

---

#### STORY-023: Proactive Marketing Conversation Logic
**Epic:** EPIC-003
**Priority:** Should Have
**Points:** 5

**User Story:**
As a business, I want AI to identify acquisition intent and proactively recommend products, so that customer inquiries convert to leads.

**Acceptance Criteria:**
- [ ] Intent signals configurable (price inquiry, comparison, etc.)
- [ ] Proactive messages triggered by intent match
- [ ] Marketing templates configurable per product line in Dify prompt
- [ ] Conversion events tracked (tagged in conversation metadata)

**Dependencies:** STORY-021

---

### EPIC-004: Human Agent Workspace

#### STORY-024: Chatwoot Custom Channel Integration
**Epic:** EPIC-004
**Priority:** Must Have
**Points:** 8

**User Story:**
As a human agent, I want all channel messages displayed in Chatwoot's unified inbox, so that I can handle conversations from one interface.

**Acceptance Criteria:**
- [ ] Custom channel created in Chatwoot per product line (Account)
- [ ] Inbound messages forwarded to Chatwoot via API
- [ ] Agent replies in Chatwoot captured via webhook
- [ ] Replies published to `unica:outbound` stream for platform delivery
- [ ] Real-time message updates via WebSocket
- [ ] Channel source indicator visible on each message

**Dependencies:** STORY-003 (Chatwoot), STORY-015

---

#### STORY-025: Conversation History Sync + Context Display
**Epic:** EPIC-004
**Priority:** Must Have
**Points:** 5

**User Story:**
As a human agent, I want to see the customer's complete conversation history including AI interactions, so that I have full context.

**Acceptance Criteria:**
- [ ] AI conversation messages synced to Chatwoot on handoff
- [ ] AI vs. human messages visually distinguished
- [ ] Customer's previous conversations accessible
- [ ] Search within conversation history
- [ ] Intent summary displayed at conversation top

**Dependencies:** STORY-024, STORY-017

---

#### STORY-026: Quick Reply Templates
**Epic:** EPIC-004
**Priority:** Should Have
**Points:** 3

**User Story:**
As a human agent, I want to use pre-configured reply templates, so that I can respond faster to common questions.

**Acceptance Criteria:**
- [ ] Templates organized by product line and category
- [ ] Keyword search for templates
- [ ] One-click insert into reply (Chatwoot canned responses)
- [ ] Admins can create/edit/delete templates

**Dependencies:** STORY-003 (Chatwoot)

---

#### STORY-027: Satisfaction Survey Mechanism
**Epic:** EPIC-004
**Priority:** Should Have
**Points:** 3

**User Story:**
As a business, I want satisfaction surveys sent after conversations close, so that we can measure customer experience quality.

**Acceptance Criteria:**
- [ ] Survey message sent automatically after conversation closes
- [ ] Supports 1-5 star rating via platform message buttons/text
- [ ] Rating stored in conversation metadata
- [ ] Results aggregated for reports (STORY-029)

**Dependencies:** STORY-015, STORY-005

---

### EPIC-005: Data Reports & Monitoring

#### STORY-028: Prometheus Metrics + Grafana Dashboards
**Epic:** EPIC-005
**Priority:** Must Have
**Points:** 5

**User Story:**
As a system admin, I want real-time system health dashboards, so that I can monitor performance and detect issues early.

**Acceptance Criteria:**
- [ ] Each Go service exposes /metrics endpoint (Prometheus format)
- [ ] Key metrics: request count, latency histogram, error rate, queue depth
- [ ] Grafana dashboards: system overview, per-channel status, queue backlog
- [ ] Correlation ID visible in logs (Loki integration)

**Dependencies:** STORY-005 (gateway metrics), STORY-016 (router metrics)

---

#### STORY-029: AI Effectiveness Reporting
**Epic:** EPIC-005
**Priority:** Must Have
**Points:** 5

**User Story:**
As a supervisor, I want to see AI auto-resolution rate, handoff rate, and top questions, so that I can optimize AI performance.

**Acceptance Criteria:**
- [ ] Auto-resolution rate calculated (AI-only conversations / total)
- [ ] Handoff rate per product line
- [ ] Top 20 questions ranked by frequency
- [ ] Knowledge base hit rate per product line
- [ ] Daily/weekly trend charts in Grafana
- [ ] Agent performance metrics (response time, volume, satisfaction)

**Technical Notes:** Combines FR-021 (agent reports) and FR-022 (AI reports) into one reporting service.

**Dependencies:** STORY-015 (conversation data)

---

#### STORY-030: Alert Rules + Webhook Notifications
**Epic:** EPIC-005
**Priority:** Must Have
**Points:** 5

**User Story:**
As a system admin, I want alerts when channels fail or queues back up, so that I can respond before customers are affected.

**Acceptance Criteria:**
- [ ] Alert rules in Prometheus (channel error rate >5%, queue depth >1000, AI P95 >3s)
- [ ] AlertManager routes to webhook notification
- [ ] Notification supports DingTalk/WeCom/Feishu webhook
- [ ] Alert history with acknowledgment tracking
- [ ] Channel traffic reports visible in Grafana (FR-023)

**Dependencies:** STORY-028

---

### EPIC-006: System Management & Permissions

#### STORY-031: RBAC Permission System + Product Line Isolation
**Epic:** EPIC-006
**Priority:** Must Have
**Points:** 8

**User Story:**
As a system admin, I want role-based access control with product-line data isolation, so that each team only accesses their own data.

**Acceptance Criteria:**
- [ ] Roles: SuperAdmin, ProductAdmin, Supervisor, Agent, KnowledgeAdmin
- [ ] Each role scoped to product line(s)
- [ ] Cross-product-line data access blocked (API-level enforcement)
- [ ] Chatwoot Account per product line mirrors RBAC
- [ ] JWT claims include role + product_line_ids
- [ ] PostgreSQL RLS policies as defense-in-depth

**Dependencies:** STORY-001 (PostgreSQL)

---

#### STORY-032: Channel Configuration CRUD + Connection Test
**Epic:** EPIC-006
**Priority:** Must Have
**Points:** 5

**User Story:**
As a system admin, I want to configure channel API credentials and test connections, so that I can onboard and manage channels without code changes.

**Acceptance Criteria:**
- [ ] CRUD API for channel configurations
- [ ] Credentials encrypted at rest (AES-256-GCM)
- [ ] Connection test button verifies API credentials
- [ ] Enable/disable toggle per channel
- [ ] Webhook URL displayed for platform configuration

**Dependencies:** STORY-002, STORY-031

---

#### STORY-033: AI Agent Configuration UI
**Epic:** EPIC-006
**Priority:** Must Have
**Points:** 5

**User Story:**
As a knowledge admin, I want to configure AI agent settings per product line, so that I can tune AI behavior without developer help.

**Acceptance Criteria:**
- [ ] System prompt editor with preview/test
- [ ] Confidence threshold slider (0.0-1.0)
- [ ] Handoff rules: threshold, keywords, blocked topics
- [ ] Knowledge base assignment (link/unlink documents)
- [ ] Changes applied to Dify workspace via API

**Dependencies:** STORY-019, STORY-031

---

#### STORY-034: Audit Logging
**Epic:** EPIC-006
**Priority:** Could Have
**Points:** 3

**User Story:**
As a system admin, I want all configuration changes logged, so that I can trace who changed what and when.

**Acceptance Criteria:**
- [ ] Config changes logged: actor, timestamp, before/after JSON
- [ ] Searchable by actor, action type, time range
- [ ] Retention: 90 days (partition-based cleanup)

**Dependencies:** STORY-031

---

## Sprint Allocation

### Sprint 1 (Week 1-2): Foundation — 22/30 points — COMPLETED

**Goal:** Deploy all infrastructure and establish the gateway message pipeline with adapter contract.

| Story | Title | Points | Priority | Status |
|-------|-------|--------|----------|--------|
| STORY-001 | K3s + PostgreSQL + Redis Setup | 5 | Must | Done |
| STORY-002 | Go Monorepo Scaffolding | 3 | Must | Done |
| STORY-003 | Deploy Chatwoot | 3 | Must | Done |
| STORY-004 | Deploy Dify | 3 | Must | Done |
| STORY-006 | Standard Message Format + Adapter Interface | 3 | Must | Done |
| STORY-005 | Gateway Core - Redis Streams | 5 | Must | Done |
| **Total** | | **22** | | **6/6** |

**Risks:** K3s node provisioning may vary by cloud provider. Chatwoot/Dify Helm charts may need tuning.
**Deliverable:** Running infrastructure + gateway that can publish/consume messages via Redis Streams.
**Result:** All 6 stories completed. Infrastructure deployed, gateway core implemented with Redis Streams, StandardMessage format and ChannelAdapter interface defined.

---

### Sprint 2 (Week 3-4): First Channel + Routing Engine — 24/30 points — COMPLETED

**Goal:** Complete WeChat end-to-end message flow with intelligent routing to AI agents.

| Story | Title | Points | Priority | Status |
|-------|-------|--------|----------|--------|
| STORY-007 | WeChat Adapter | 8 | Must | Done |
| STORY-010 | Token Auto-Management | 3 | Must | Done |
| STORY-009 | Message Dedup + Dead-Letter | 3 | Must | Done |
| STORY-015 | Conversation State Machine + DB | 5 | Must | Done |
| STORY-016 | Intelligent Routing | 5 | Must | Done |
| **Total** | | **24** | | **5/5** |

**Risks:** WeChat sandbox may have limitations. Platform API approval pending.
**Deliverable:** WeChat messages flow from platform → gateway → router, conversations created and routed.
**Result:** All 5 stories completed. WeChat adapter with full test coverage (84.5%), token auto-management with singleflight, message deduplication with dead-letter queue, conversation state machine with DB schema, and intelligent routing service with Dify AI integration.

---

### Sprint 3 (Week 5-6): AI Integration + Human Handoff — 29/30 points — COMPLETED

**Goal:** Complete AI response pipeline and Chatwoot integration for human takeover.

| Story | Title | Points | Priority | Status |
|-------|-------|--------|----------|--------|
| STORY-019 | Dify Multi-Workspace Setup | 3 | Must | Done |
| STORY-020 | RAG Knowledge Base Pipeline | 5 | Must | Done |
| STORY-021 | Router↔Dify Integration | 5 | Must | Done |
| STORY-022 | Confidence Scoring + Auto-Handoff | 3 | Must | Done |
| STORY-017 | AI→Human Handoff with Context | 5 | Must | Done |
| STORY-024 | Chatwoot Custom Channel Integration | 8 | Must | Done |
| **Total** | | **29** | | **6/6** |

**Risks:** Dify API may have quirks. Chatwoot custom channel integration complexity. This is the highest-load sprint.
**Deliverable:** Full loop: customer message → AI response OR human handoff → reply delivered back to WeChat.
**Result:** All 6 stories completed. Dify multi-workspace setup with idempotent script, RAG knowledge base pipeline with document upload/update/delete, enhanced Router-Dify integration with RAG metadata and confidence scoring, guardrail evaluator with keyword/threshold/topic-based auto-handoff, AI-Human handoff with intent summary generation and Chatwoot context sync, bidirectional Chatwoot custom channel integration with webhook handling.

---

### Sprint 4 (Week 7-8): Channel Expansion + Workspace Polish — 26/30 points — COMPLETED

**Goal:** Add Douyin and Xiaohongshu channels, complete agent workspace features.

| Story | Title | Points | Priority | Status |
|-------|-------|--------|----------|--------|
| STORY-008 | Douyin Adapter | 5 | Must | Done |
| STORY-012 | Xiaohongshu Adapter | 5 | Must | Done |
| STORY-018 | Agent Scheduling + Distribution | 5 | Should | Done |
| STORY-025 | History Sync + Context Display | 5 | Must | Done |
| STORY-026 | Quick Reply Templates | 3 | Should | Done |
| STORY-027 | Satisfaction Survey | 3 | Should | Done |
| **Total** | | **26** | | **6/6** |

**Risks:** Platform API approval for Douyin/XHS may not be ready. Use mock adapters if needed.
**Deliverable:** 3 channels operational, human agents fully equipped with context/templates/surveys.
**Result:** All 6 stories completed. Douyin adapter with HMAC-SHA256 verification and mock testing (82.7% coverage), Xiaohongshu adapter with note_link support (48 tests), agent scheduling with least-loaded distribution and Chatwoot assignment sync, conversation history sync with watermark-based deduplication and contact metadata enrichment, quick reply templates with idempotent seeding script (3 brands x 12 templates), satisfaction survey with auto-send on close and 1-5 rating collection.

---

### Sprint 5 (Week 9-10): Full Channel Coverage + Admin — 26/30 points — COMPLETED

**Goal:** Complete all 5 channels and build the admin/permission system.

| Story | Title | Points | Priority | Status |
|-------|-------|--------|----------|--------|
| STORY-013 | Taobao Adapter | 5 | Must | Done |
| STORY-014 | Kuaishou Adapter | 5 | Must | Done |
| STORY-011 | Rate Limiting + Anti-Abuse | 3 | Should | Done |
| STORY-031 | RBAC Permission System | 8 | Must | Done |
| STORY-032 | Channel Configuration CRUD | 5 | Must | Done |
| **Total** | | **26** | | **5/5** |

**Risks:** Taobao API may use polling instead of webhooks (different pattern). RBAC complexity.
**Deliverable:** All 5 channels connected, admin can manage channels and permissions.
**Result:** All 5 stories completed. Taobao adapter with HMAC-MD5 signature and TOP protocol (84.3% coverage, 39 tests), Kuaishou adapter with HMAC-SHA256 webhook model (82.7% coverage, 38 tests), sliding window rate limiter with Redis sorted sets and burst detection (88.0% coverage, 14 tests), RBAC permission system with JWT auth, 5 roles, permission matrix, and product-line isolation (37 tests), channel configuration CRUD with AES-256-GCM encryption and per-platform connection testing (15 tests).

---

### Sprint 6 (Week 11-12): Monitoring, Reports, Polish — 28/30 points — COMPLETED

**Goal:** Complete observability, reporting, and remaining admin features. System production-ready.

| Story | Title | Points | Priority | Status |
|-------|-------|--------|----------|--------|
| STORY-028 | Prometheus + Grafana Dashboards | 5 | Must | Done |
| STORY-029 | AI Effectiveness + Agent Reports | 5 | Must | Done |
| STORY-030 | Alert Rules + Notifications | 5 | Must | Done |
| STORY-033 | AI Agent Configuration UI | 5 | Must | Done |
| STORY-023 | Proactive Marketing Logic | 5 | Should | Done |
| STORY-034 | Audit Logging | 3 | Could | Done |
| **Total** | | **28** | | **6/6** |

**Risks:** Report queries may need optimization for large datasets.
**Deliverable:** Full monitoring, dashboards, alerting, AI config UI. System ready for production.
**Result:** All 6 stories completed. Prometheus/Grafana/Loki deployed with 7 dashboards, reporter service with 4 API endpoints and materialized views, AlertManager with webhook adapter for DingTalk/WeCom/Feishu, AI agent configuration admin API with 6 endpoints and Dify bridge, proactive marketing intent detection integrated into router pipeline, audit logging with partitioned table and async middleware.

---

## Epic Traceability

| Epic | Name | Stories | Points | Sprints |
|------|------|---------|--------|---------|
| Infra | Infrastructure | 001, 002, 003, 004 | 14 | S1 |
| EPIC-001 | Multi-Channel Gateway | 005-014 | 45 | S1-S5 |
| EPIC-002 | Conversation Routing | 015-018 | 20 | S2-S4 |
| EPIC-003 | AI & Knowledge Base | 019-023 | 21 | S3, S6 |
| EPIC-004 | Agent Workspace | 024-027 | 19 | S3-S4 |
| EPIC-005 | Reports & Monitoring | 028-030 | 15 | S6 |
| EPIC-006 | System Management | 031-034 | 21 | S5-S6 |

---

## Requirements Coverage

| FR | Name | Story | Sprint |
|----|------|-------|--------|
| FR-001 | Multi-Channel Message Reception | 007, 008, 012, 013, 014 | S2, S4, S5 |
| FR-002 | Unified Message Format | 006, 005 | S1 |
| FR-003 | Message Dedup & Retry | 009 | S2 |
| FR-004 | Rate Limiting | 011 | S5 |
| FR-005 | Token Auto-Management | 010 | S2 |
| FR-006 | Intelligent Routing | 016 | S2 |
| FR-007 | AI→Human Handoff | 017, 022 | S3 |
| FR-008 | Human Takeover Context | 017, 025 | S3, S4 |
| FR-009 | Conversation State Mgmt | 015 | S2 |
| FR-010 | Agent Scheduling | 018 | S4 |
| FR-011 | Multi-Product Agents | 019 | S3 |
| FR-012 | RAG Knowledge Retrieval | 020, 021 | S3 |
| FR-013 | Knowledge Base Mgmt | 020, 033 | S3, S6 |
| FR-014 | Proactive Marketing | 023 | S6 |
| FR-015 | Confidence & Guardrails | 022 | S3 |
| FR-016 | Unified Inbox | 024 | S3 |
| FR-017 | History View | 025 | S4 |
| FR-018 | Quick Reply Templates | 026 | S4 |
| FR-019 | Customer Info Sidebar | 025 | S4 |
| FR-020 | Satisfaction Survey | 027 | S4 |
| FR-021 | Agent Reports | 029 | S6 |
| FR-022 | AI Effectiveness Reports | 029 | S6 |
| FR-023 | Channel Traffic Reports | 030 | S6 |
| FR-024 | Exception Alerts | 030 | S6 |
| FR-025 | Permission Isolation | 031 | S5 |
| FR-026 | Channel Config | 032 | S5 |
| FR-027 | AI Agent Config | 033 | S6 |
| FR-028 | Audit Log | 034 | S6 |

**Coverage: 28/28 FRs (100%)**

---

## Risks and Mitigation

### High Risk
| Risk | Impact | Mitigation |
|------|--------|-----------|
| Platform API approval delays | Blocks channel adapter development | Use mock adapters in dev; prioritize WeChat (most common); apply for all platforms in Sprint 1 |
| Chatwoot custom channel limitations | May not support all workspace features | Prototype integration in Sprint 3 early; evaluate API limitations before deep investment |
| Sprint 3 overload (29 pts) | Highest-load sprint with critical AI integration | All stories are Must Have; if needed, defer STORY-024 Chatwoot integration to Sprint 4 start |

### Medium Risk
| Risk | Impact | Mitigation |
|------|--------|-----------|
| Dify API quirks | RAG/confidence integration complexity | Spike test Dify API in Sprint 1 infrastructure phase |
| Taobao polling model | Different pattern from webhook-based adapters | Research Taobao API in Sprint 4, allocate extra time |

### Low Risk
| Risk | Impact | Mitigation |
|------|--------|-----------|
| PostgreSQL performance at scale | Report queries slow on large datasets | Add indexes early; partition messages table from day 1 |

---

## Definition of Done

For a story to be considered complete:
- [ ] Code implemented and committed
- [ ] Unit tests passing (target 80% coverage for business logic)
- [ ] Integration tests passing for critical paths
- [ ] Service deploys successfully to K3s dev environment
- [ ] Acceptance criteria validated
- [ ] No critical or high severity bugs

---

## Next Steps

**ALL 6 SPRINTS COMPLETED** - Project UNICA is feature-complete.

Sprint 6 COMPLETED (28/28 points, all 6 stories done). All 34 stories across 6 sprints delivered (155/155 points).

**Post-Sprint Actions:**
- Production deployment validation
- End-to-end integration testing across all 5 channels
- Performance/load testing
- Security audit
- Documentation finalization

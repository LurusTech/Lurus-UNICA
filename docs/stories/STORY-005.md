# STORY-005: Gateway Core - Redis Streams Producer/Consumer

**Epic:** EPIC-001 (Multi-Channel Gateway)
**Priority:** Must Have
**Story Points:** 5
**Status:** Completed
**Sprint:** 1
**Created:** 2026-03-04

---

## User Story

As a system, I want the gateway service to publish inbound messages to Redis Streams and consume outbound replies, so that message processing is asynchronous, resilient, and decoupled from downstream services.

---

## Description

### Background
The gateway is the central message hub of UNICA. It sits between channel adapters (inbound) and the router/AI services (outbound). Using Redis Streams as the message backbone enables:
- Fast webhook acknowledgment (platform requires <5s response)
- Fault isolation between channels
- Message persistence across service restarts
- Parallel consumption via consumer groups

This story implements the core gateway without any specific channel adapter — it provides the plumbing that all adapters will use.

### Scope
**In scope:**
- Gateway HTTP server accepting normalized messages from adapters
- Redis Streams producer: publish to `unica:inbound`
- Redis Streams consumer: read from `unica:outbound`
- Consumer group setup with automatic pending message recovery
- CloudEvents-compatible message envelope
- Health check endpoint (`/healthz`)
- Graceful shutdown with pending message drain
- Prometheus metrics endpoint (`/metrics`)
- Correlation ID generation and propagation

**Out of scope:**
- Channel-specific adapters (STORY-007+)
- Message deduplication (STORY-009)
- Rate limiting (STORY-011)
- Token management (STORY-010)

### Message Flow
```
Inbound:
  Adapter HTTP POST → Gateway /api/v1/gateway/inbound
    → Validate StandardMessage format
    → Generate correlation ID
    → XADD to unica:inbound stream
    → Return 202 Accepted

Outbound:
  Consumer reads from unica:outbound stream
    → Extract target channel from message
    → HTTP POST to adapter's outbound endpoint
    → XACK on success
    → On failure: retry or dead-letter
```

---

## Acceptance Criteria

- [x] Gateway starts and connects to Redis successfully
- [x] `POST /api/v1/gateway/inbound` accepts StandardMessage JSON, returns 202
- [x] Message published to `unica:inbound` Redis Stream with CloudEvents envelope
- [x] Consumer group `gateway-outbound` created on `unica:outbound` stream
- [x] Outbound messages consumed and dispatched to correct adapter endpoint
- [x] Messages acknowledged (XACK) only after successful dispatch
- [x] Pending messages auto-claimed after 60s timeout (crash recovery)
- [x] `/healthz` returns 200 when Redis connected, 503 when not
- [x] `/metrics` exposes Prometheus metrics (request count, latency, queue depth)
- [x] Correlation ID (`X-Correlation-ID`) generated on inbound, propagated on outbound
- [x] Graceful shutdown: stops accepting new messages, drains pending, then exits

---

## Technical Notes

### Dependencies (Go modules)
```
github.com/redis/go-redis/v9      # Redis client with Streams support
github.com/google/uuid             # Correlation ID generation
github.com/prometheus/client_golang # Metrics
net/http (stdlib)                   # HTTP server
```

### Redis Streams Setup
```go
// Create streams and consumer groups on startup
func setupStreams(rdb *redis.Client) error {
    // Create consumer groups (MKSTREAM creates stream if not exists)
    rdb.XGroupCreateMkStream(ctx, "unica:inbound", "router-group", "0")
    rdb.XGroupCreateMkStream(ctx, "unica:outbound", "gateway-outbound", "0")
    return nil
}
```

### Key Metrics
```
gateway_inbound_total          counter   Total inbound messages received
gateway_outbound_total         counter   Total outbound messages dispatched
gateway_inbound_duration_seconds  histogram  Inbound processing latency
gateway_outbound_duration_seconds histogram  Outbound dispatch latency
gateway_stream_depth           gauge     Current stream queue depth
```

### Configuration (environment variables)
```
REDIS_URL=redis://:password@redis-master:6379/0
GATEWAY_PORT=8080
GATEWAY_OUTBOUND_WORKERS=4
GATEWAY_PENDING_CLAIM_INTERVAL=60s
GATEWAY_SHUTDOWN_TIMEOUT=30s
```

### Error Handling
- Redis connection failure: `/healthz` returns 503, retry connection with backoff
- Outbound dispatch failure: retry 3x with exponential backoff, then NACK
- Malformed inbound message: return 400 Bad Request, do not publish to stream

---

## Dependencies

**Prerequisite:**
- STORY-001 (Redis must be running)
- STORY-002 (Go scaffolding with StandardMessage type)

**Blocks:**
- STORY-006 (adapter interface — can develop in parallel)
- STORY-007 (WeChat adapter)
- STORY-009 (dedup adds middleware to this gateway)
- All subsequent gateway features

---

## Definition of Done

- [x] Gateway service builds and runs
- [x] Inbound endpoint accepts messages and publishes to stream
- [x] Outbound consumer reads and dispatches messages
- [x] Health check and metrics endpoints functional
- [x] Unit tests for stream producer/consumer logic
- [x] Integration test: publish inbound → verify in stream → publish outbound → verify dispatch
- [ ] Dockerfile builds and runs in K3s (existing Dockerfile compatible, needs K3s deploy)
- [x] Code committed to `gateway/`

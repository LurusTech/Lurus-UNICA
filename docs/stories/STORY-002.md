# STORY-002: Go Monorepo Scaffolding

**Epic:** Infrastructure
**Priority:** Must Have
**Story Points:** 3
**Status:** Completed
**Sprint:** 1
**Created:** 2026-03-04

---

## User Story

As a developer, I want the Go monorepo structure with shared packages, build scripts, and Dockerfile templates, so that I can start implementing services immediately.

---

## Description

### Background
The architecture defines a Go monorepo with 4 services (gateway, router, admin, reporter) sharing common types and utilities. This story sets up the project skeleton so all subsequent service development has a consistent foundation.

### Scope
**In scope:**
- Go workspace (go.work) with 4 service modules
- Shared model package (pkg/model) with StandardMessage type
- Dockerfile template per service
- Makefile with build/test/lint/docker targets
- Basic project structure per architecture doc
- .gitignore for Go projects
- go.mod initialized for each module

**Out of scope:**
- Actual business logic (subsequent stories)
- CI/CD pipeline configuration
- Helm charts (created per-service in later stories)

### Directory Structure
```
unica/
├── gateway/
│   ├── cmd/gateway/main.go
│   ├── internal/
│   │   ├── adapter/adapter.go       # ChannelAdapter interface
│   │   ├── dedup/
│   │   ├── ratelimit/
│   │   ├── token/
│   │   └── stream/
│   ├── Dockerfile
│   └── go.mod
├── router/
│   ├── cmd/router/main.go
│   ├── internal/
│   │   ├── routing/
│   │   ├── handoff/
│   │   ├── state/
│   │   └── bridge/
│   ├── Dockerfile
│   └── go.mod
├── admin/
│   ├── cmd/admin/main.go
│   ├── internal/
│   ├── Dockerfile
│   └── go.mod
├── reporter/
│   ├── cmd/reporter/main.go
│   ├── internal/
│   ├── Dockerfile
│   └── go.mod
├── pkg/
│   └── model/
│       ├── message.go     # StandardMessage, CloudEvents envelope
│       └── message_test.go
├── deploy/
├── docs/
├── scripts/
├── go.work
├── Makefile
└── .gitignore
```

---

## Acceptance Criteria

- [ ] `go.work` workspace builds successfully (`go build ./...`)
- [ ] 4 service modules (gateway, router, admin, reporter) each with `cmd/*/main.go` placeholder
- [ ] `pkg/model` package defines `StandardMessage` struct with JSON tags
- [ ] `pkg/model` package defines `ChannelAdapter` interface (VerifyWebhook, ParseInbound, FormatOutbound, SendMessage)
- [ ] Each service has a multi-stage Dockerfile that compiles and runs
- [ ] Makefile targets: `build`, `test`, `lint`, `docker-build` work
- [ ] `go vet ./...` and `go test ./...` pass with zero errors
- [ ] .gitignore covers Go binaries, vendor, IDE files

---

## Technical Notes

### StandardMessage Schema (pkg/model/message.go)
```go
type StandardMessage struct {
    ID             string          `json:"id"`
    Type           string          `json:"type"`           // "message.inbound" | "message.outbound"
    Source         string          `json:"source"`         // "adapter.wechat" | "adapter.douyin" | ...
    Subject        string          `json:"subject"`        // "conversation:{id}"
    Time           time.Time       `json:"time"`
    Data           MessageData     `json:"data"`
}

type MessageData struct {
    ConversationID string          `json:"conversation_id"`
    ChannelID      string          `json:"channel_id"`
    ProductLineID  string          `json:"product_line_id"`
    CustomerID     string          `json:"customer_id"`
    Content        MessageContent  `json:"content"`
    PlatformMsgID  string          `json:"platform_msg_id"`
}

type MessageContent struct {
    Type string `json:"type"` // "text" | "image" | "video" | "link"
    Text string `json:"text,omitempty"`
    URL  string `json:"url,omitempty"`
}
```

### ChannelAdapter Interface
```go
type ChannelAdapter interface {
    VerifyWebhook(r *http.Request) error
    ParseInbound(r *http.Request) (*StandardMessage, error)
    FormatOutbound(msg *StandardMessage) ([]byte, error)
    SendMessage(payload []byte) error
}
```

### Dockerfile Template (multi-stage)
```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.work go.work.sum ./
COPY pkg/ pkg/
COPY gateway/ gateway/
RUN cd gateway && go build -o /bin/gateway ./cmd/gateway

FROM alpine:3.19
COPY --from=builder /bin/gateway /bin/gateway
ENTRYPOINT ["/bin/gateway"]
```

### Makefile Key Targets
```makefile
.PHONY: build test lint docker-build

build:
	@for svc in gateway router admin reporter; do \
		cd $$svc && go build ./... && cd ..; \
	done

test:
	go test ./...

lint:
	go vet ./...
```

---

## Dependencies

**Prerequisite:**
- Go 1.22+ installed
- Git repository initialized

**Blocks:**
- STORY-005 (Gateway core builds on this skeleton)
- STORY-006 (Message format defined here)
- All service implementation stories

---

## Definition of Done

- [ ] All 4 services build successfully
- [ ] `go test ./...` passes
- [ ] `go vet ./...` clean
- [ ] Dockerfiles build successfully
- [ ] StandardMessage and ChannelAdapter defined and tested
- [ ] Code committed to repository

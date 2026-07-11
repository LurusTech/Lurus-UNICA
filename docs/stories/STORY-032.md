# STORY-032: Channel Configuration CRUD + Connection Test

**Epic:** EPIC-006 (System Management & Permissions)
**Priority:** Must Have
**Story Points:** 5
**Status:** Not Started
**Assigned To:** Unassigned
**Created:** 2026-03-06
**Sprint:** 5

---

## User Story

As a system admin,
I want to configure channel API credentials and test connections,
So that I can onboard and manage channels without code changes.

---

## Description

### Background
Currently, channel configurations (API keys, secrets, webhook URLs) are managed through environment variables or config files, requiring developer intervention for changes. This story adds a proper admin interface for channel management, allowing admins to add/edit/disable channels and verify credentials work before going live.

### Scope
**In scope:**
- CRUD API for channel configurations (all 5 platforms)
- Credential encryption at rest (AES-256-GCM)
- Connection test endpoint that verifies API credentials per platform
- Enable/disable toggle per channel
- Webhook URL display for platform configuration
- Product line association for channels
- RBAC enforcement (SuperAdmin or ProductAdmin for own PL)

**Out of scope:**
- Channel configuration UI frontend (admin API only in this story)
- Automatic webhook registration on platforms
- Channel health monitoring (STORY-028)

### User Flow
1. Admin authenticates via JWT (STORY-031)
2. Admin creates new channel config: selects platform type, enters credentials
3. Admin clicks "Test Connection" - system verifies credentials against platform API
4. If test passes: channel saved with "verified" status
5. Admin enables channel - gateway starts accepting webhooks for it
6. Admin copies displayed webhook URL, configures it on platform dashboard
7. Messages start flowing

---

## Acceptance Criteria

- [ ] CRUD API for channel configurations (Create, Read, Update, Delete)
- [ ] Supports all 5 platform types: wechat, douyin, xiaohongshu, taobao, kuaishou
- [ ] Credentials encrypted at rest using AES-256-GCM
- [ ] Encryption key managed via K8s Secret (not hardcoded)
- [ ] Connection test endpoint verifies API credentials per platform type
- [ ] Connection test calls platform token/info API to validate credentials
- [ ] Channel enable/disable toggle (disabled channels reject webhooks)
- [ ] Webhook URL auto-generated and displayed per channel
- [ ] Channel associated with product line
- [ ] RBAC enforced: only SuperAdmin or ProductAdmin (own PL) can manage channels
- [ ] Sensitive fields (app_secret, tokens) never returned in API responses (masked)
- [ ] List channels filtered by product line scope
- [ ] Unit tests for encryption/decryption
- [ ] Integration tests for CRUD + connection test

---

## Technical Notes

### Database Schema

```sql
CREATE TABLE channel_configs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_line_id UUID NOT NULL REFERENCES product_lines(id),
    platform VARCHAR(50) NOT NULL,       -- wechat, douyin, xiaohongshu, taobao, kuaishou
    display_name VARCHAR(200) NOT NULL,
    app_id VARCHAR(255) NOT NULL,
    app_secret_encrypted BYTEA NOT NULL, -- AES-256-GCM encrypted
    extra_config_encrypted BYTEA,        -- platform-specific extra config (encrypted JSON)
    webhook_token VARCHAR(100),          -- verification token for webhook setup
    is_enabled BOOLEAN DEFAULT false,
    is_verified BOOLEAN DEFAULT false,
    last_test_at TIMESTAMPTZ,
    last_test_result TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(product_line_id, platform, app_id)
);

-- Enable RLS
ALTER TABLE channel_configs ENABLE ROW LEVEL SECURITY;
CREATE POLICY channel_pl_isolation ON channel_configs
    USING (product_line_id IN (
        SELECT product_line_id FROM user_roles
        WHERE user_id = current_setting('app.current_user_id')::UUID
    ));
```

### Components
- **New files:**
  - `unica/admin/internal/handler/channels.go` - Channel CRUD + test endpoints
  - `unica/admin/internal/repository/channel.go` - Channel DB operations
  - `unica/admin/internal/crypto/aes.go` - AES-256-GCM encrypt/decrypt
  - `unica/admin/internal/channel/tester.go` - Platform connection test logic
  - `unica/admin/internal/channel/tester_test.go`
  - `unica/admin/internal/crypto/aes_test.go`
  - `migrations/008_channel_configs.sql` - Schema migration
- **Modified files:**
  - `unica/gateway/cmd/gateway/main.go` - Load channel configs from DB instead of env vars
  - `unica/gateway/internal/adapter/registry.go` - Support dynamic adapter registration from DB configs

### API Endpoints

```
GET    /api/v1/channels                 - List channels (PL-scoped)
POST   /api/v1/channels                 - Create channel config
GET    /api/v1/channels/:id             - Get channel (secrets masked)
PUT    /api/v1/channels/:id             - Update channel config
DELETE /api/v1/channels/:id             - Delete channel config
POST   /api/v1/channels/:id/test        - Test connection
PUT    /api/v1/channels/:id/toggle      - Enable/disable channel
GET    /api/v1/channels/:id/webhook-url - Get webhook URL for platform setup
```

### Connection Test Logic per Platform

```go
type ConnectionTester interface {
    Test(ctx context.Context, cfg ChannelConfig) (*TestResult, error)
}

// Each platform tests by attempting to get an access token
// WeChat: GET https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid={}&secret={}
// Douyin: POST https://open.douyin.com/oauth/client_token/
// XHS: POST https://open.xiaohongshu.com/api/oauth/token
// Taobao: taobao.top.auth.token.create
// Kuaishou: POST https://open.kuaishou.com/oauth2/access_token
```

### Encryption Design
```go
// AES-256-GCM encryption
func Encrypt(plaintext []byte, key []byte) ([]byte, error) {
    block, _ := aes.NewCipher(key)
    gcm, _ := cipher.NewGCM(block)
    nonce := make([]byte, gcm.NonceSize())
    io.ReadFull(rand.Reader, nonce)
    return gcm.Seal(nonce, nonce, plaintext, nil), nil
}
```

### Gateway Integration
- Gateway periodically polls or subscribes to channel config changes
- On config change: re-initialize affected adapter with new credentials
- Disabled channels: webhook handler returns 404 (not 200 to avoid platform retries thinking it succeeded)

### Webhook URL Pattern
```
https://{gateway_host}/webhook/{platform}/{channel_id}
```

---

## Dependencies

**Prerequisite Stories:**
- STORY-002: Go Monorepo Scaffolding (Done)
- STORY-031: RBAC Permission System (Sprint 5 - must be implemented first)

**Blocked Stories:**
- None directly, but enables dynamic channel management for all adapters

**External Dependencies:**
- AES encryption key provisioned as K8s Secret

---

## Definition of Done

- [ ] Code implemented and committed to feature branch
- [ ] Database migration applied successfully
- [ ] Unit tests written and passing
  - [ ] AES-256-GCM encryption/decryption tests
  - [ ] Channel CRUD repository tests
  - [ ] Connection tester tests (mocked platform APIs)
  - [ ] RBAC enforcement tests
- [ ] Integration tests passing
  - [ ] Full CRUD lifecycle test
  - [ ] Connection test with mock platform
  - [ ] Enable/disable toggle affects webhook handling
  - [ ] Cross-PL access blocked
- [ ] Sensitive credentials never leaked in API responses
- [ ] Admin service deploys successfully to K3s dev environment
- [ ] Acceptance criteria validated
- [ ] No critical or high severity bugs

---

## Story Points Breakdown

- **DB schema + CRUD API**: 1.5 points
- **Credential encryption**: 1 point
- **Connection test per platform**: 1 point
- **Gateway integration (dynamic config)**: 1 point
- **Testing**: 0.5 points
- **Total:** 5 points

**Rationale:** Moderate complexity. The encryption and per-platform connection testing add some effort, but CRUD is straightforward. Gateway dynamic config reload is the most complex integration point.

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created

**Actual Effort:** TBD

---

**This story was created using BMAD Method v6 - Phase 4 (Implementation Planning)**

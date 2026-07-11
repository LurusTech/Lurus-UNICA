# STORY-026: Quick Reply Templates

**Epic:** EPIC-004 (Human Agent Workspace)
**Priority:** Should Have
**Story Points:** 3
**Status:** Not Started
**Sprint:** 4
**Created:** 2026-03-05

---

## User Story

As a human agent, I want to use pre-configured reply templates, so that I can respond faster to common questions.

---

## Description

### Background
Human agents frequently answer the same types of questions (shipping policy, return process, product specs, etc.). Quick reply templates allow agents to insert pre-written responses with one click, improving response time and consistency. Chatwoot has a built-in "Canned Responses" feature that supports this — this story leverages it via API.

This addresses FR-018 (Quick Reply Templates).

### Scope
**In scope:**
- Seed canned responses in Chatwoot per product line (Account)
- Templates organized by category (shortcode prefix)
- Keyword search for templates (Chatwoot native feature)
- One-click insert into reply (Chatwoot native UX)
- Admin CRUD for templates via Chatwoot API
- Setup script to seed initial templates from config

**Out of scope:**
- Custom template UI (use Chatwoot native canned responses)
- Template analytics (which templates are used most)
- Template variables / dynamic content (future enhancement)
- Multi-language templates

### How It Works
```
Chatwoot Canned Responses:
  - Agent types "/" in reply box -> template search popup
  - Agent searches by keyword or shortcode
  - Agent clicks template -> content inserted into reply
  - Agent can edit before sending

Template Organization:
  - Shortcode format: {category}_{name}
  - Examples: shipping_standard, return_policy, greeting_welcome
  - Each product line has its own set of templates (Chatwoot Account isolation)
```

---

## Acceptance Criteria

- [ ] Canned responses created in Chatwoot per product line
- [ ] Templates organized by category via shortcode prefix
- [ ] Agent can search templates by keyword in Chatwoot
- [ ] One-click insert into reply works (Chatwoot native)
- [ ] Admins can create/edit/delete templates via Chatwoot UI or API
- [ ] Setup script seeds initial templates from a config file
- [ ] Templates isolated per product line (Chatwoot Account boundary)
- [ ] At least 10 sample templates seeded per product line

---

## Technical Notes

### Chatwoot Canned Response API
```
POST   /api/v1/accounts/{account_id}/canned_responses
GET    /api/v1/accounts/{account_id}/canned_responses?search={query}
PUT    /api/v1/accounts/{account_id}/canned_responses/{id}
DELETE /api/v1/accounts/{account_id}/canned_responses/{id}

Body:
{
  "short_code": "shipping_standard",
  "content": "我们的标准快递一般3-5个工作日送达，下单后会有物流短信通知您。"
}
```

### Template Config File
```yaml
# deploy/config/canned_responses.yaml
product_lines:
  - name: "品牌A"
    templates:
      - short_code: "greeting_welcome"
        content: "您好！欢迎咨询品牌A客服，请问有什么可以帮您？"
      - short_code: "greeting_busy"
        content: "您好，当前咨询量较大，请稍等，我会尽快为您解答。"
      - short_code: "shipping_standard"
        content: "我们的标准快递一般3-5个工作日送达，下单后会有物流短信通知您。"
      - short_code: "return_policy"
        content: "我们支持7天无理由退换货，请在收到商品后7天内联系我们办理。"
      # ... more templates
```

### Setup Script
```go
// scripts/seed_canned_responses.go
// 1. Read canned_responses.yaml
// 2. For each product line, resolve Chatwoot account_id
// 3. For each template, create canned response via API
// 4. Skip if shortcode already exists (idempotent)
```

### Components
- `scripts/seed_canned_responses.go` — Seeding script
- `deploy/config/canned_responses.yaml` — Template config file
- Update `router/internal/bridge/chatwoot.go` — Add canned response CRUD methods

---

## Dependencies

**Prerequisite:**
- STORY-003 (Chatwoot deployed)
- STORY-024 (Chatwoot Custom Channel — accounts created per product line)

**Blocks:**
- None

**External Dependencies:**
- None

---

## Definition of Done

- [ ] Canned responses seeded in Chatwoot for each product line
- [ ] Agent can search and insert templates in Chatwoot
- [ ] Setup script is idempotent (re-run safe)
- [ ] Template config file committed with sample templates
- [ ] CRUD operations verified via Chatwoot API
- [ ] Templates isolated per product line verified
- [ ] Code committed to `scripts/` and `deploy/config/`

---

## Story Points Breakdown

- **Chatwoot API client extension:** 1 point
- **Setup script + config file:** 1 point
- **Sample templates + testing:** 1 point
- **Total:** 3 points

**Rationale:** Low complexity. Primarily leverages Chatwoot's built-in canned responses feature via API. Main work is the seeding script and organizing templates by category.

# STORY-019: Dify Multi-Workspace Setup

**Epic:** EPIC-003 (AI Smart Response & Knowledge Base)
**Priority:** Must Have
**Story Points:** 3
**Status:** Not Started
**Sprint:** 3
**Created:** 2026-03-05

---

## User Story

As a knowledge admin, I want each product line to have an isolated AI workspace in Dify, so that knowledge bases and prompts don't interfere between products.

---

## Description

### Background
UNICA serves 7-8 product lines, each requiring independent AI behavior: different system prompts, different knowledge bases, and different confidence thresholds. Dify's "Workspace" concept maps perfectly to this requirement — each workspace isolates datasets, prompts, and API keys. This story sets up the multi-workspace structure that all downstream AI stories depend on.

### Scope
**In scope:**
- Create one Dify workspace per product line (7-8 workspaces)
- Configure independent system prompt per workspace
- Create isolated knowledge base (dataset) per workspace
- Generate and store API keys per workspace
- Verify cross-workspace data isolation
- Store workspace configuration in `product_lines` table (dify_agent_id, dify_api_key, dify_base_url)

**Out of scope:**
- Document upload and RAG pipeline (STORY-020)
- Router integration with Dify API (STORY-021)
- Admin UI for AI configuration (STORY-033)

### Setup Flow
```
1. For each product line in product_lines table:
   a. Create Dify workspace via Dify Admin API
   b. Create a Chat App within the workspace
   c. Configure system prompt (default template)
   d. Create empty knowledge base (dataset) in workspace
   e. Generate API key for the Chat App
   f. Store dify_agent_id (app_id) + dify_api_key in product_lines table
2. Verify: Call chat API for workspace A, confirm it cannot access workspace B's data
3. Document workspace IDs and API keys in K8s Secrets
```

---

## Acceptance Criteria

- [ ] One Dify workspace created per product line (7-8 workspaces)
- [ ] Each workspace has an independent Chat App with configurable system prompt
- [ ] Each workspace has an isolated knowledge base (dataset)
- [ ] Cross-workspace data access verified blocked (workspace A cannot query workspace B's dataset)
- [ ] API keys generated per workspace and stored securely
- [ ] product_lines table updated with dify_agent_id, dify_api_key, dify_base_url
- [ ] Default system prompt template applied to all workspaces
- [ ] Setup script/migration is idempotent (can re-run safely)

---

## Technical Notes

### Dify Admin API Endpoints
```
POST /console/api/workspaces           — Create workspace
POST /console/api/apps                 — Create Chat App in workspace
PUT  /console/api/apps/{id}            — Update app config (system prompt)
POST /console/api/datasets             — Create dataset (knowledge base)
POST /console/api/apps/{id}/api-keys   — Generate API key
```

### Product Line Configuration
```sql
-- Update existing product_lines rows with Dify workspace info
UPDATE product_lines SET
    dify_agent_id = '{app_id}',         -- Dify Chat App ID
    dify_api_key = '{api_key}',         -- Encrypted API key
    dify_base_url = 'http://dify:5001/v1',
    config_json = jsonb_set(config_json, '{dify_workspace_id}', '"{workspace_id}"')
WHERE id = '{product_line_id}';
```

### Setup Script
- Location: `unica/scripts/setup_dify_workspaces.go`
- Reads product_lines from DB
- Creates workspaces + apps + datasets via Dify Admin API
- Updates product_lines with API credentials
- Idempotent: checks if workspace already exists before creating

### System Prompt Template (Default)
```
You are a customer service AI assistant for {product_line_name}.
Your role is to help customers with product inquiries, troubleshooting, and general questions.
Always be polite, concise, and accurate.
If you are unsure about an answer, indicate your uncertainty clearly.
Never make up product specifications or pricing.
```

### Security
- API keys encrypted at rest in PostgreSQL (AES-256-GCM, same as channel credentials)
- Dify Admin API access restricted to internal network only
- Workspace admin accounts use strong generated passwords

---

## Dependencies

**Prerequisite:**
- STORY-004 (Dify deployed and accessible)

**Blocks:**
- STORY-020 (RAG Knowledge Base — needs datasets created)
- STORY-021 (Router-Dify Integration — needs API keys and app IDs)
- STORY-022 (Confidence Scoring — extends Dify app config)

---

## Definition of Done

- [ ] 7-8 Dify workspaces created, each with Chat App and dataset
- [ ] API keys generated and stored in product_lines table
- [ ] Cross-workspace isolation verified
- [ ] Setup script committed to `unica/scripts/`
- [ ] Script is idempotent and documented
- [ ] System prompt template applied to all workspaces

---

## Story Points Breakdown

- **Dify API integration + setup script:** 1.5 points
- **DB updates + security:** 0.5 points
- **Verification + testing:** 1 point
- **Total:** 3 points

**Rationale:** Low-moderate complexity — mostly API calls to Dify and DB updates. The main challenge is understanding Dify's Admin API and ensuring idempotent setup.

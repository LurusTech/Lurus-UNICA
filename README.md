[中文](README.zh-CN.md) | English

# UNICA — Multi-Tenant Cross-Channel AI Customer Service Hub

UNICA is a self-hosted, multi-tenant AI customer-service system: one deployment serves several client companies at once. It unifies inbound messages from WeChat, Douyin, Taobao, Kuaishou and Xiaohongshu, lets a large language model answer pre-sales questions through Dify, and hands off to a human agent through Chatwoot whenever confidence is low or the answer would touch a policy fact the model can't be trusted to get right. The target workload is small merchants: one human agent backstops several companies' customer service at once.

```
Channel platforms ──► Gateway ──► Redis Streams ──► Router ──► Dify (LLM answers, one app + KB per tenant)
(WeChat/Douyin/Taobao/                                │  │
 Kuaishou/Xiaohongshu)                                │  └──► acest kb-server (optional dual knowledge base)
                                                       ▼
                                                Chatwoot (human agents, one account per tenant)
                                                       │
     Ops portal ──► Admin (back office / one-click onboarding)   Reporter (analytics) ──► Grafana
```

It is a real contract-delivered product (private-labeled per client, self-hosted on the client's infrastructure), not a public SaaS with self-serve signup. Several capabilities described below are implemented and unit-tested but have **not yet been exercised against real traffic or a real Chatwoot instance** — see [Honesty about maturity](#honesty-about-maturity).

## Core capabilities

- **Channel gateway with webhook verification, dedup and dead-letter handling** — signature verification, message normalization, Redis-based dedup with TTL fail-open, retry with backoff, and a dead-letter stream per channel (`unica/gateway/internal`).
- **Session state machine + LLM routing with fail-open dual knowledge base** — router calls Dify per conversation and, when `ACEST_KB_URL` is set, recalls both an "experience" store and external knowledge from an [acest](https://github.com/LurusTech/Lurus-acest) `kb-server` in parallel before the call, injecting `experience_context` / `knowledge_context`; unreachable kb-server only drops the injected context (`unica/router/internal/routing`, `unica/router/cmd/router/main.go:351-373`).
- **Domain ontology grounding** — per-tenant policy facts are injected before the model answers and validated after, with a circuit breaker that stops enforcing once the recent block rate exceeds 25% (`inject_facts` / `validation` off by default; `unica/pkg/domain`, `unica/router/cmd/router/main.go:343-349`).
- **Scene-aware response strategy (pre-sales vs. post-sales)** — router classifies each message and injects a matching tone/behavior template via `scene_context`; ships in `shadow` mode (metrics only, no behavior change) by default, and the mode is moved from the platform console without restarting the router (`SCENE_MODE` seeds it once, then `platform_settings`; `unica/router` `routing` package).
- **One-click tenant onboarding** — a single admin-only call creates the tenant, its Dify app + knowledge dataset + API key, a portal account, and (optionally) a Chatwoot account/inbox; every step is idempotent and resumable from a partial failure (`unica/admin/internal/identity/tenants_onboarding.go`).
- **Two-tier RBAC** — exactly two roles, `admin` (runs the platform) and `user` (owns exactly one tenant); tenant isolation is enforced server-side from the JWT, not just hidden in the UI (`unica/admin/internal/rbac/roles.go`).

## Quick start

```bash
# 1. Infrastructure: PostgreSQL + Redis (or use the manifests under deploy/)

# 2. Migrations (21 files, idempotent)
for f in unica/router/migrations/*.sql; do psql "$POSTGRES_URL" -v ON_ERROR_STOP=1 -f "$f"; done

# 3. Services — each module is an independent Go module
cd unica/gateway && go build ./... && ./gateway   # same for router / admin / reporter

# 4. Portal: any static host, with /api/ same-origin proxied to admin
#    (see deploy/chatwoot-preview/nginx.conf for a worked example)
```

Local Chatwoot/Dify preview stacks (Docker Compose, for development) live under `deploy/chatwoot-local/` and `deploy/dify-preview/`; copy the `.env.example` in each and fill in the generated secrets.

Build, vet and test any module:

```bash
cd unica/<module> && go build ./... && go vet ./... && go test ./...
```

CI (`.github/workflows/ci.yml`) runs `build` + `vet` + `test -race` across six modules: `unica/pkg`, `unica/router`, `unica/admin`, `unica/gateway`, `unica/reporter`, `deploy/alertmanager/webhook-adapter`.

Database-backed integration tests are skipped unless a writable **test** database is given explicitly (never reuses `POSTGRES_URL`):

```bash
ROUTER_TEST_POSTGRES_URL="postgres://...@localhost:5432/unica_test?sslmode=disable" \
  go test ./internal/state/ -count=1
```

## Architecture

| Module | Responsibility | Default port |
|---|---|---|
| `unica/gateway` | Channel webhook intake, signature verification, normalization, dedup/retry/dead-letter, token lifecycle | 8080 (`GATEWAY_PORT`) |
| `unica/router` | Session state machine, LLM + knowledge/ontology injection, judging chain, handoff, satisfaction survey | 8081 (`ROUTER_PORT`) |
| `unica/admin` | Back-office API: onboarding, tenants/channels/users/RBAC/audit, ontology publishing, knowledge proxy | 8081, moved to 8082 in practice when co-located with router (`ADMIN_PORT`) |
| `unica/reporter` | Analytics API: channel traffic, LLM effectiveness, agent performance, knowledge-base hit rate | 8083 (`REPORTER_PORT`) |
| `unica/pkg` | Shared, dependency-light libraries: `crypto` (AES), `difyapp` (Dify app/dataset contracts), `domain` (ontology/breaker), `guardrail`, `model`, `survey` | — |
| `portal/` | Ops portal (static HTML + JS): onboarding, knowledge base, channels, ontology, quality review | served by nginx, `/api/` proxied to admin |
| `deploy/` | K8s/Compose manifests for the platform itself plus self-hosted Chatwoot and Dify, Prometheus/Grafana/alerting | — |

```
2b-svc-unica/
├── unica/
│   ├── gateway/   # channel intake, one go.mod
│   ├── router/    # conversation engine + migrations/
│   ├── admin/     # back office API
│   ├── reporter/  # analytics API
│   ├── pkg/       # shared libraries (go.work member)
│   ├── scripts/   # one-off Go tools (Dify/Chatwoot bootstrap, partition maintenance SQL)
│   └── go.work    # workspace tying the five modules together
├── portal/        # static ops portal (8 pages)
├── deploy/        # K8s manifests + docker-compose previews for Chatwoot/Dify/monitoring
└── doc/           # ontology schema, unverified.md, known-defects.md
```

Stack: Go 1.23 microservices (`net/http`, no framework) + PostgreSQL + Redis Streams for the platform itself; the conversation workbench is self-hosted Chatwoot and LLM orchestration/RAG is self-hosted Dify — both deployed as separate services (see `deploy/chatwoot/`, `deploy/dify/`), not vendored into this codebase.

## Configuration

Environment variables actually read in code (`os.Getenv` / `envOrDefault`), grouped by module:

| Variable | Module | Default | Notes |
|---|---|---|---|
| `POSTGRES_URL` / `REDIS_URL` | all | — / `redis://localhost:6379/0` | storage connections |
| `GATEWAY_PORT` / `ROUTER_PORT` / `ADMIN_PORT` / `REPORTER_PORT` | each | `8080`/`8081`/`8081`/`8083` | listen port |
| `WECHAT_*` / `TAOBAO_*` / `KUAISHOU_*` | gateway | empty | static channel credentials |
| `DATABASE_URL` + `AES_ENCRYPTION_KEY` | gateway/admin | — | dynamic, portal-managed channel credentials |
| `CHATWOOT_WEBHOOK_TOKEN` | gateway | — | verifies Chatwoot agent-reply callbacks |
| `DIFY_ADMIN_URL` / `DIFY_ADMIN_EMAIL` / `DIFY_ADMIN_PASSWORD` | admin | — | Dify console credentials used for onboarding/provisioning |
| `DIFY_API_BASE_URL` | admin/router | `http://dify:5001/v1` | Dify service API root |
| `DIFY_DATASET_API_KEY` | admin | empty | dataset-scope key; self-serve knowledge base returns 503 without it |
| `DIFY_INDEXING_TECHNIQUE` | admin | `high_quality` | `economy` required when the model provider offers no embeddings. **Seed only** — see below |
| `CHATWOOT_BASE_URL` / `CHATWOOT_PLATFORM_TOKEN` / `CHATWOOT_WEBHOOK_URL` | admin | empty | Chatwoot step of onboarding; skips (not fails) when unset |
| `ACEST_KB_URL` / `ACEST_KB_TOKEN` | router | empty (disabled) | optional acest dual knowledge base |
| `ACEST_RECALL_TIMEOUT` / `ACEST_RECALL_TOP_K` | router | `2s` / `3` | recall budget / snippets injected per store |
| `INTENT_TRIAGE` | router | `shadow` | pre-LLM intent triage: `off` / `shadow` / `on`. **Seed only** — see below |
| `SCENE_MODE` | router | `shadow` | pre/post-sales tone injection: `off` / `shadow` / `on`. **Seed only** — see below |
| `SWITCH_POLL_INTERVAL` | router | `10s` | how often the router re-reads the stored switches, i.e. how long a console change takes to reach it |
| `ONTOLOGY_ENABLED` | router | `true` | ontology master switch; per-tenant opt-in still required |
| `PARTITION_MONTHS_AHEAD` / `PARTITION_CHECK_INTERVAL` | router | `3` / `24h` | monthly partition auto-provisioning |
| `JWT_SECRET` | admin | `change-me-in-production` | back office + portal auth signing key |

**Seed-only variables.** `INTENT_TRIAGE`, `SCENE_MODE` and `DIFY_INDEXING_TECHNIQUE` are read
from the environment exactly once, on a first start that finds nothing stored: the value seeds a
row in `platform_settings` (`unica/router/migrations/022_platform_settings.sql`) and the platform
console owns it from then on. Changing them later in a deployment file has no effect — the router
re-reads the table every `SWITCH_POLL_INTERVAL`, so a switch moves without a restart. A variable
left set to something the table disagrees with is not silently obeyed or silently dropped: the
router names it in `GET /configz` under `env_shadowed` and the console says to remove it
(`unica/pkg/platformsettings`, `unica/router/internal/routing/switches.go`).

## API overview

- **Gateway** (`unica/gateway/cmd/gateway/main.go`): `POST /api/v1/gateway/inbound`, `POST /webhook/{wechat,taobao,kuaishou}`, `POST /api/v1/webhook/chatwoot` (agent replies), `GET /api/v1/gateway/dead-letter`, `GET /healthz`, `GET /metrics`.
- **Admin** (`unica/admin/cmd/admin/main.go`): `POST/GET /api/v1/tenants` (onboarding + listing, admin-only), `GET/POST /api/v1/tenants/{id}/{knowledge,ai-settings,channels,ontology,violations,handoffs,workbench}`, `POST /api/v1/auth/{login,refresh}`, `GET /api/v1/audit-logs`, `GET/POST /api/v1/platform/{settings,models,prompts,knowledge}`. Older `/api/v1/product-lines/`, `/api/v1/channels/`, `/api/v1/violations/`, `/api/v1/handoff-events/` paths are kept as aliases onto the same handlers.
- **Router** (`unica/router/cmd/router/main.go`): `GET /healthz`, `GET /metrics`, `GET /configz`; conversation handling itself runs off the Redis Streams consumer, not an HTTP route.
- **Reporter** (`unica/reporter/internal/handler/routes.go`): `GET /api/v1/reports/{ai-effectiveness,agent-performance,top-questions,channel-traffic}`.
- **Ontology CLI** (CI/batch, `unica/router/cmd/ontology`): `go run ./cmd/ontology {validate,preview,publish} -dir <path>`.

## Honesty about maturity

Passing tests are not the same as verified behavior. [`doc/unverified.md`](doc/unverified.md) tracks capabilities that are implemented but still lack real-world evidence, and [`doc/known-defects.md`](doc/known-defects.md) tracks confirmed bugs. Notable examples as of this writing:

- Ontology grounding and the scene-strategy classifier have never seen a real customer message; both were validated only on a self-authored golden set.
- One-click Chatwoot onboarding (account/user/inbox provisioning, token capture, resumable retries) is verified only against an `httptest` mock server, not a real Chatwoot instance.
- The circuit-breaker thresholds (`trip_rate 0.25`, `min_samples 20`, `window 100`) are placeholders with no production traffic behind them.
- Deployment against a real Dify 0.15.3 instance surfaced a `400` from `UpdateAppConfig` that the original delivery only exercised against a mock; admin now degrades non-fatally on that path.

## Development conventions

- **Register, don't hide, unverified work.** New capabilities that can't be verified at merge time must get an entry in `doc/unverified.md` before or with the merge, and confirmed defects go in `doc/known-defects.md` with a `file:line` pointer — both files are pruned once the entry is actually verified or fixed, not left to rot.
- **Migrations are numbered and idempotent** (`unica/router/migrations/NNN_*.sql`); each must replay cleanly on an already-migrated database.
- **Flags default to off / shadow.** `SCENE_MODE`, `INTENT_TRIAGE`, ontology `validation` and the breaker all ship without changing observable behavior, so an upgrade never silently changes what customers see. The first two are platform-wide and live in `platform_settings`, changed from the console and picked up without a restart; ontology `validation` and the breaker are opted into per tenant.
- Every module has its own `go.mod`; `unica/go.work` ties gateway/router/admin/reporter/pkg together for local development, but CI builds each module standalone (see the matrix in `.github/workflows/ci.yml`).

## Related projects

- [**acest**](https://github.com/LurusTech/Lurus-acest) — optional dual knowledge base via its `kb-server` HTTP API (`/api/v1/search`, `/api/v1/kb/search`, `/api/v2/experiences`); router talks to it through `ACEST_KB_URL`/`ACEST_KB_TOKEN`, fail-open. See [`unica/doc/acest-kb-integration.md`](unica/doc/acest-kb-integration.md) for the data flow.
- **Chatwoot** (`chatwoot/chatwoot:v3.15.0`) — self-hosted human-agent workbench, deployed from `deploy/chatwoot/` and `deploy/chatwoot-local/`; not vendored into this repository.
- **Dify** (`langgenius/dify-api:0.15.3` / `dify-web:0.15.3`) — self-hosted LLM app orchestration and RAG, deployed from `deploy/dify/` and `deploy/dify-preview/`; not vendored into this repository.

No `LICENSE` file is present in this repository at the time of writing; treat licensing terms as unresolved and confirm with the repository owner before reuse.

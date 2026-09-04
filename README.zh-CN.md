中文 | [English](README.md)

# UNICA —— 跨渠道 AI 客服中枢（多租户）

UNICA 是一套自托管、多租户的跨渠道 AI 客服系统：一次部署同时服务多家公司。它统一接入微信、抖音、淘宝、快手、小红书等平台的客服消息，售前咨询由大模型经 Dify 直接应答，遇到低置信度或涉及"模型不该凭常识回答"的政策事实时自动转人工（Chatwoot）。目标工况是业务量不大的小公司：用一个人工坐席兜底多家公司的客服。

```
渠道平台 ──► Gateway ──► Redis Streams ──► Router ──► Dify（大模型应答，每租户一应用+知识库）
（微信/抖音/淘宝/                            │  │
 快手/小红书）                               │  └──► acest kb-server（可选，双知识库）
                                           ▼
                                    Chatwoot（人工坐席，每租户一 account）
                                           │
     运营门户 portal ──► Admin（管理后台/一键开户）     Reporter（报表）──► Grafana
```

它是一个真实的合同交付产品（按客户私有化部署，自托管在客户自己的基础设施上），不是自助注册的公开 SaaS。下文列出的部分能力已实现并有单元测试覆盖，但**尚未经过真实流量或真实 Chatwoot 实例验证**——见下方"成熟度诚实说明"。

## 核心能力

- **渠道网关：验签 + 去重 + 死信** —— webhook 验签、消息标准化、Redis 去重（fail-open + TTL）、带退避的重试、按渠道分流的死信队列（`unica/gateway/internal`）。
- **会话状态机 + 大模型路由，双知识库 fail-open 召回** —— router 每次调用 Dify 时，若设置了 `ACEST_KB_URL` 会并行召回 [acest](https://github.com/LurusTech/Lurus-acest) 的 `kb-server` 提供的"经验库"与外源知识库，注入 `experience_context`/`knowledge_context`；kb-server 不可达时仅缺注入内容，不影响主流程（`unica/router/internal/routing`、`unica/router/cmd/router/main.go:351-373`）。
- **领域本体校验** —— 每条产品线的政策事实在调用大模型前注入、回答后校验，熔断器在近期拦截比例超过 25% 时自动停止拦截（`inject_facts`/`validation` 默认全关；`unica/pkg/domain`、`unica/router/cmd/router/main.go:343-349`）。
- **场景化应答策略（售前/售后）** —— router 判定每条消息所处场景，注入对应语气/行为模板（`scene_context`），默认以 `shadow` 模式上线（只记指标不改行为）；换档在平台管理页上做，**不重启 router**（`SCENE_MODE` 只种一次，之后以 `platform_settings` 为准；`unica/router` 的 `routing` 包）。
- **一键开户** —— 一次超管专属调用即建好租户、其 Dify 应用+知识库数据集+API key、门户账号，以及可选的 Chatwoot 账号/收件箱；每一步幂等，失败后可从缺口续作（`unica/admin/internal/identity/tenants_onboarding.go`）。
- **两层 RBAC** —— 恰好两种角色：`admin`（运营整个平台）与 `user`（恰好拥有一个租户）；租户隔离由服务端按 JWT 强制，而不仅是界面隐藏（`unica/admin/internal/rbac/roles.go`）。

## 快速开始

```bash
# 1. 基础设施：PostgreSQL + Redis（或使用 deploy/ 下的清单）

# 2. 迁移（21 个文件，幂等）
for f in unica/router/migrations/*.sql; do psql "$POSTGRES_URL" -v ON_ERROR_STOP=1 -f "$f"; done

# 3. 各服务 —— 每个模块是独立的 Go module
cd unica/gateway && go build ./... && ./gateway   # router/admin/reporter 同理

# 4. portal：任意静态托管，把 /api/ 同源反代到 admin
#    （参考 deploy/chatwoot-preview/nginx.conf 的实际配置）
```

本地开发用的 Chatwoot/Dify 预览栈（Docker Compose）在 `deploy/chatwoot-local/` 与 `deploy/dify-preview/` 下；复制各自的 `.env.example` 并填入自行生成的密钥。

构建、静态检查、测试任一模块：

```bash
cd unica/<module> && go build ./... && go vet ./... && go test ./...
```

CI（`.github/workflows/ci.yml`）对六个模块跑 `build` + `vet` + `test -race`：`unica/pkg`、`unica/router`、`unica/admin`、`unica/gateway`、`unica/reporter`、`deploy/alertmanager/webhook-adapter`。

带数据库的集成测试默认跳过，需显式指定一个可写的**测试**库（不复用 `POSTGRES_URL`）：

```bash
ROUTER_TEST_POSTGRES_URL="postgres://...@localhost:5432/unica_test?sslmode=disable" \
  go test ./internal/state/ -count=1
```

## 架构

| 模块 | 职责 | 默认端口 |
|---|---|---|
| `unica/gateway` | 渠道 webhook 接入、验签、消息标准化、去重/重试/死信、令牌生命周期 | 8080（`GATEWAY_PORT`） |
| `unica/router` | 会话状态机、大模型调用与知识/本体注入、判定链路、转人工、满意度调研 | 8081（`ROUTER_PORT`） |
| `unica/admin` | 管理 API：一键开户、租户/渠道/用户/RBAC/审计、本体发布、知识库代理 | 8081，与 router 同机部署时实践中改用 8082（`ADMIN_PORT`） |
| `unica/reporter` | 报表 API：渠道流量、大模型效果、坐席绩效、知识库命中率 | 8083（`REPORTER_PORT`） |
| `unica/pkg` | 轻依赖共享库：`crypto`（AES）、`difyapp`（Dify 应用/数据集契约）、`domain`（本体/熔断）、`guardrail`、`model`、`survey` | — |
| `portal/` | 运营门户（纯静态 HTML+JS）：开户、知识库、渠道、本体、质量复核 | 由 nginx 托管，`/api/` 反代到 admin |
| `deploy/` | 平台本身及自托管 Chatwoot/Dify 的 K8s/Compose 清单，Prometheus/Grafana/告警 | — |

```
2b-svc-unica/
├── unica/
│   ├── gateway/   # 渠道接入，独立 go.mod
│   ├── router/    # 会话引擎 + migrations/
│   ├── admin/     # 管理后台 API
│   ├── reporter/  # 报表 API
│   ├── pkg/       # 共享库（go.work 成员）
│   ├── scripts/   # 一次性 Go 工具（Dify/Chatwoot 引导、分区维护 SQL）
│   └── go.work    # 把五个模块串成本地开发工作区
├── portal/        # 静态运营门户（8 个页面）
├── deploy/        # 平台 + Chatwoot/Dify/监控预览栈的 K8s 清单与 docker-compose
└── doc/           # 本体 schema、unverified.md、known-defects.md
```

技术栈：平台自身是 Go 1.23 微服务（`net/http`，无框架）+ PostgreSQL + Redis Streams；对话工作台用自托管 Chatwoot，大模型编排与 RAG 用自托管 Dify——两者都作为独立服务部署（见 `deploy/chatwoot/`、`deploy/dify/`），并未把源码纳入本仓库。

## 配置

代码里实际读取（`os.Getenv`/`envOrDefault`）的环境变量，按模块分组：

| 变量 | 模块 | 默认值 | 说明 |
|---|---|---|---|
| `POSTGRES_URL` / `REDIS_URL` | 全部 | — / `redis://localhost:6379/0` | 存储连接 |
| `GATEWAY_PORT` / `ROUTER_PORT` / `ADMIN_PORT` / `REPORTER_PORT` | 各自 | `8080`/`8081`/`8081`/`8083` | 监听端口 |
| `WECHAT_*` / `TAOBAO_*` / `KUAISHOU_*` | gateway | 空 | 静态渠道凭据 |
| `DATABASE_URL` + `AES_ENCRYPTION_KEY` | gateway/admin | — | 动态渠道凭据（portal 管理） |
| `CHATWOOT_WEBHOOK_TOKEN` | gateway | — | 验证 Chatwoot 坐席回复回调 |
| `DIFY_ADMIN_URL` / `DIFY_ADMIN_EMAIL` / `DIFY_ADMIN_PASSWORD` | admin | — | 开户/开通用的 Dify 控制台凭据 |
| `DIFY_API_BASE_URL` | admin/router | `http://dify:5001/v1` | Dify 服务 API 根 |
| `DIFY_DATASET_API_KEY` | admin | 空 | 数据集级 key；不配置则知识库自助返回 503 |
| `DIFY_INDEXING_TECHNIQUE` | admin | `high_quality` | 模型商不提供嵌入时必须用 `economy`。**仅首次种子**，见下 |
| `CHATWOOT_BASE_URL` / `CHATWOOT_PLATFORM_TOKEN` / `CHATWOOT_WEBHOOK_URL` | admin | 空 | 一键开户的 Chatwoot 步骤；不配置则跳过而非失败 |
| `ACEST_KB_URL` / `ACEST_KB_TOKEN` | router | 空（禁用） | 可选的 acest 双知识库 |
| `ACEST_RECALL_TIMEOUT` / `ACEST_RECALL_TOP_K` | router | `2s` / `3` | 召回总预算 / 每库注入片段数 |
| `INTENT_TRIAGE` | router | `shadow` | 调大模型前意图分诊：`off`/`shadow`/`on`。**仅首次种子**，见下 |
| `SCENE_MODE` | router | `shadow` | 售前/售后语气注入：`off`/`shadow`/`on`。**仅首次种子**，见下 |
| `SWITCH_POLL_INTERVAL` | router | `10s` | router 多久重读一次库里的开关，也就是控制台改动多久生效 |
| `ONTOLOGY_ENABLED` | router | `true` | 本体总开关，仍需逐租户单独开通 |
| `PARTITION_MONTHS_AHEAD` / `PARTITION_CHECK_INTERVAL` | router | `3` / `24h` | 月分区自动续期 |
| `JWT_SECRET` | admin | `change-me-in-production` | 后台与 portal 鉴权签名密钥 |

**只作首次种子的三个变量。** `INTENT_TRIAGE`、`SCENE_MODE`、`DIFY_INDEXING_TECHNIQUE`
只在「库里还没有这一行」的首次启动时被读一次：值种进 `platform_settings`
（`unica/router/migrations/022_platform_settings.sql`），之后由平台管理页说了算。
此后再改部署文件里的这几个变量不会有任何效果——router 每 `SWITCH_POLL_INTERVAL`
重读一次库，所以换档不用重启。变量留着不删也不会被静默服从或静默忽略：
router 会在 `GET /configz` 的 `env_shadowed` 里点名，控制台提示把它从部署配置里删掉
（`unica/pkg/platformsettings`、`unica/router/internal/routing/switches.go`）。

## 接口概览

- **Gateway**（`unica/gateway/cmd/gateway/main.go`）：`POST /api/v1/gateway/inbound`、`POST /webhook/{wechat,taobao,kuaishou}`、`POST /api/v1/webhook/chatwoot`（坐席回复回流）、`GET /api/v1/gateway/dead-letter`、`GET /healthz`、`GET /metrics`。
- **Admin**（`unica/admin/cmd/admin/main.go`）：`POST/GET /api/v1/tenants`（开户+列表，超管专属）、`GET/POST /api/v1/tenants/{id}/{knowledge,ai-settings,channels,ontology,violations,handoffs,workbench}`、`POST /api/v1/auth/{login,refresh}`、`GET /api/v1/audit-logs`、`GET/POST /api/v1/platform/{settings,models,prompts,knowledge}`。旧路径 `/api/v1/product-lines/`、`/api/v1/channels/`、`/api/v1/violations/`、`/api/v1/handoff-events/` 作为别名保留，映射到同一批 handler。
- **Router**（`unica/router/cmd/router/main.go`）：`GET /healthz`、`GET /metrics`、`GET /configz`；对话处理本身跑在 Redis Streams 消费者上，不经 HTTP 路由。
- **Reporter**（`unica/reporter/internal/handler/routes.go`）：`GET /api/v1/reports/{ai-effectiveness,agent-performance,top-questions,channel-traffic}`。
- **本体 CLI**（CI/批量，`unica/router/cmd/ontology`）：`go run ./cmd/ontology {validate,preview,publish} -dir <path>`。

## 成熟度诚实说明

测试全绿不等于行为已被验证。[`doc/unverified.md`](doc/unverified.md) 登记了已交付但仍缺真实证据的能力，[`doc/known-defects.md`](doc/known-defects.md) 登记已确认的缺陷。截至本文撰写时的几个例子：

- 本体校验与场景策略分类器从未见过一条真实客户消息，都只在自己写的黄金集上验证过。
- 一键开户里的 Chatwoot 步骤（账号/用户/收件箱开通、令牌捕获、断点续作）只对 `httptest` 假服务器验证过，没有对接过真实 Chatwoot 实例。
- 熔断阈值（`trip_rate 0.25`、`min_samples 20`、`window 100`）是没有生产流量支撑的占位值。
- 对接真实 Dify 0.15.3 时发现 `UpdateAppConfig` 返回 400，原交付只对假服务器测过；admin 现已在该路径上做非致命降级。

## 开发约定

- **登记而非隐藏未验证的工作**：合并时无法验证的新能力必须在合并前后写进 `doc/unverified.md`；已确认的缺陷登记进 `doc/known-defects.md` 并带 `file:line`——两份文件都在对应条目真正被验证/修复后才删除，不允许放着不管。
- **迁移编号且幂等**（`unica/router/migrations/NNN_*.sql`）：每条在已迁移过的数据库上重放必须成功。
- **开关默认关闭/影子模式**：`SCENE_MODE`、`INTENT_TRIAGE`、本体 `validation` 与熔断器上线时都不改变可观察行为（只记指标），确保升级不会静默改变客户看到的东西。前两个是平台级的，存在 `platform_settings` 里、在控制台改、不重启即生效；本体 `validation` 与熔断器则是逐租户开通。
- 每个模块有自己的 `go.mod`；`unica/go.work` 只把 gateway/router/admin/reporter/pkg 串成本地开发工作区，CI 按模块独立构建（见 `.github/workflows/ci.yml` 的 matrix）。

## 相关项目

- [**acest**](https://github.com/LurusTech/Lurus-acest) —— 通过其 `kb-server` HTTP API（`/api/v1/search`、`/api/v1/kb/search`、`/api/v2/experiences`）提供可选的双知识库；router 经 `ACEST_KB_URL`/`ACEST_KB_TOKEN` 接入，fail-open。数据流细节见 [`unica/doc/acest-kb-integration.md`](unica/doc/acest-kb-integration.md)。
- **Chatwoot**（`chatwoot/chatwoot:v3.15.0`）—— 自托管人工坐席工作台，由 `deploy/chatwoot/` 与 `deploy/chatwoot-local/` 部署；源码未纳入本仓库。
- **Dify**（`langgenius/dify-api:0.15.3` / `dify-web:0.15.3`）—— 自托管大模型应用编排与 RAG，由 `deploy/dify/` 与 `deploy/dify-preview/` 部署；源码未纳入本仓库。

本仓库撰写本文档时未发现 `LICENSE` 文件，许可条款视为未确定，复用前请与仓库所有者确认。

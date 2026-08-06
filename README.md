# UNICA — 跨渠道 AI 客服中枢（多租户）

UNICA 是一套自托管、多租户的跨渠道 AI 客服系统：**一次部署同时服务多家公司**。
统一接入微信、抖音、淘宝、快手、小红书等平台的客服消息，售前咨询尽可能由
AI（Dify）直接应答，低置信、敏感或违背业务事实的场景自动转人工（Chatwoot）。
目标工况：业务量不大的小公司，用**一个人工坐席兜底多家公司**的客服。

```
渠道平台 ──► Gateway ──► Redis Streams ──► Router ──► Dify (AI 应答，每客户一应用+知识库)
(微信/抖音/淘宝/                            │  │
 快手/小红书)                               │  └──► acest kb-server (可选，双知识库)
                                           ▼
                                    Chatwoot (人工坐席，每客户一 account)
                                           │
     运营门户 portal ──► Admin (管理后台/一键开户)     Reporter (报表) ──► Grafana
```

## 模块

| 模块 | 职责 | 默认端口 |
|---|---|---|
| `unica/gateway` | 渠道 webhook 接入、验签、消息标准化、去重/重试/死信、令牌生命周期 | 8080 |
| `unica/router` | 会话状态机、AI 调用与知识/本体注入、判定链路、转人工、满意度、营销意图 | 8081 |
| `unica/admin` | 管理 API：一键开户、产品线/渠道/用户/RBAC/审计、本体发布、知识库代理 | 8081（与 router 同机部署时错开，如 8082） |
| `unica/reporter` | 报表 API：渠道流量、AI 效果、坐席绩效、知识库命中率 | 8083 |
| `portal/` | 运营门户（纯静态 HTML+JS）：产品线/开户/知识库/渠道/本体/质量复核 | 由 nginx 托管，`/api/` 同源代理到 admin |
| `deploy/` | K8s/compose 清单、Chatwoot/Dify 部署、Prometheus/Grafana/告警适配器 | — |

技术栈：Go 微服务 + PostgreSQL + Redis Streams；对话工作台用 Chatwoot，AI 编排用 Dify。
portal 部署参考 `deploy/chatwoot-preview/nginx.conf`（静态文件 + `/api/` 反代 admin）。

## 多租户模型

租户单位是**产品线**（`product_lines`），一个客户公司对应一条产品线：

- **开户约定 1 账号 = 1 产品线**（表结构保持多对多，客户开第二条线就再开一个账号）；
- 一条产品线可绑**多个渠道**（如两个小红书账号），各自凭证独立，路由汇到同一 AI 应用；
- 每条线独立拥有：Dify 应用 + 知识库数据集、领域本体、护栏与熔断配置、Chatwoot account；
- **客户自助**：客户账号（`product_admin`，按线圈定）登录 portal 后自管知识库、渠道与
  质量复核，产品线选择器锁定为自己的线，首页隐藏运营专属入口（本体编辑不下放客户）；
- **多开**：登录态按浏览器标签页隔离（sessionStorage），同一浏览器两个标签页可同时
  登录两个客户账号并行运营；
- **人工兜底**：Chatwoot 按 account 隔离租户，一个坐席用户加入多个 account 即可
  同时接多家公司的转人工会话——这正是"一个人工兜底多家公司"的实现方式。

RBAC 越权（跨线读写）由服务端按 JWT 内的产品线范围强制，portal 的隐藏只是界面简化。

## 一键开户

`POST /api/v1/customers`（需同时持有 `manage_users` 与 `manage_product_lines`，
即超管专属；portal 产品线页有向导入口）。一次调用完成：

1. 建产品线（按名称 get-or-create，唯一索引兜并发）；
2. 开通 Dify：应用（含系统提示词与注入变量）+ 知识库数据集 + 应用 API key，写回绑定；
3. 建门户账号 + 按线赋 `product_admin` 角色（密码可传入，留空则生成强密码）；
4. Chatwoot 租户（可选）：Platform API 建 account → 坐席用户 → 管理员绑定 → API 收件箱，
   写 `config_json.chatwoot`。

**每一步幂等且进度即时落盘**：失败后重 POST 同一请求体即从缺口续作，不会重复建资源
（Chatwoot 的用户访问令牌只在创建时返回，因此分段持久化是收敛的前提）。生成的密码
**只在开户响应中出现一次**，审计快照统一脱敏。Chatwoot 未配置时该步如实降级
（`configured:false` + 原因），不影响其余步骤。

前置条件：`CHATWOOT_BASE_URL` + `CHATWOOT_PLATFORM_TOKEN`（平台应用需在 Chatwoot
Super Admin 后台**手工创建一次**换取 token）+ `CHATWOOT_WEBHOOK_URL`（指向 gateway）。

## 知识库自助

每条产品线的文档由客户在 portal 知识库页自管：上传文件/粘贴文本、删除、
按上传返回的 **batch** 轮询索引进度（Dify 的 indexing-status 以 batch 为键，
不是 document id）。服务端代理 Dify 数据集 API 并强制归属校验——数据集 ID
永远取自产品线配置，不接受请求指定。

两个部署级配置：

- `DIFY_DATASET_API_KEY`：**数据集级** API key（Dify 控制台"知识库 API"页铸造，
  工作区级）。Dify 按 token 类型校验，应用级 key 打数据集端点必被拒；
  不配置则知识库管理明确禁用（503），不会静默回退。
- `DIFY_INDEXING_TECHNIQUE`：`high_quality`（默认，需要工作区配有嵌入模型）或
  `economy`（关键词索引，零嵌入依赖）。模型商不提供嵌入（如 DeepSeek）时必须用
  `economy`，否则每次上传都被 `provider_not_initialize` 拒绝。

## 知识库架构：Dify 或 acest，如何选

UNICA 的 AI 应答支持两层知识来源，可以只用 Dify，也可以叠加 acest 双知识库。

### 方案 A：仅 Dify（默认）

Dify 应用自带 RAG：每条产品线绑定一个 Dify 数据集（`config_json.dify_dataset_id`），
文档由上文的自助页维护，Dify 负责切片、向量化和应答时检索。不设置 `ACEST_KB_URL`
即为此模式，零额外组件。适合知识全部来自静态文档、不需要系统"越用越聪明"的场景。

### 方案 B：Dify + acest 双知识库

[ACEST](../acest/acest) 的无头 `kb-server` 额外提供两套知识系统：外源知识库
（LocalDocs/Dify/RAGFlow/FastGPT/MCP/向量库的混合检索）与**经验知识库**
（客服交互自动蒸馏沉淀，越用越聪明）。router 在每次调用 Dify 前并行召回两库，
注入变量 `experience_context` / `knowledge_context`；应答结果异步回写成经验样本。
集成 **fail-open**：kb-server 不可达时仅缺注入上下文，主流程零影响。

配置（router 侧）：

```bash
ACEST_KB_URL=http://127.0.0.1:7423   # 不设置 = 禁用（方案 A）
ACEST_KB_TOKEN=<与 kb-server 一致>
ACEST_RECALL_TIMEOUT=2s              # 可选：召回总预算
ACEST_RECALL_TOP_K=3                 # 可选：每库注入片段数
```

Dify 应用侧需声明段落型变量 `experience_context` / `knowledge_context` 并在提示词中
引用（一键开户创建的应用已内置）。详细数据流与运维见
[`unica/doc/acest-kb-integration.md`](unica/doc/acest-kb-integration.md)。

## 领域本体：让 AI 不再靠常识补答案

RAG 只保证"检索到了相似内容"，保证不了"说的是对的"。不同客户的退货政策互相冲突
（7 天无理由 / 15 天限未拆封 / 根本没有无理由退货），检索分数区分不了它们。

领域本体把这类确定性事实从概率通道里拿出来：每条产品线声明自己的政策事实与
"本业务不提供什么"，router 调用 AI 前注入，回答后校验，违规回答按模式处置。
两个独立开关写在 `product_lines.config_json.ontology`：`inject_facts`（注入事实）与
`validation`（`off`/`shadow`/`enforce`）。**默认都关**，升级不改变任何现有行为。
本体在 portal 本体页编辑、校验、发布、回滚（运营侧能力，不下放客户）；
违规证据进质量复核队列人工定性（本体错/模型错/误报）。

`enforce` 带熔断：本体写错一条断言会让每一条碰到它的正确回答都被压下转人工，
所以最近窗口内被拦比例超过 25% 就自动停止拦截并告警（`breaker` 块可调，默认开）。
熔断态不是安全态，是拿"发出可能有误的回答"换"人工队列不被打爆"，
代价记在 `router_ontology_breaker_bypassed_total` 里。

命令行方式（CI/批量）：

```bash
cd unica/router
go run ./cmd/ontology validate -dir ../../deploy/config/ontology   # 检查，无需数据库
go run ./cmd/ontology preview  -dir ../../deploy/config/ontology   # 看注入内容
POSTGRES_URL=... go run ./cmd/ontology publish -dir ../../deploy/config/ontology
```

编写指南见 [`doc/ontology-schema.md`](doc/ontology-schema.md)。

## 快速开始

```bash
# 1. 基础设施（PostgreSQL + Redis），或使用 deploy/ 下的清单
# 2. 迁移（16 个，幂等）
for f in unica/router/migrations/*.sql; do psql $POSTGRES_URL -v ON_ERROR_STOP=1 -f $f; done

# 3. 各服务（每个模块独立 go.mod）
cd unica/gateway && go build ./... && ./gateway   # 同理 router/admin/reporter

# 4. portal：任意静态托管 + 把 /api/ 反代到 admin（同源），参考 deploy/chatwoot-preview/nginx.conf
```

核心环境变量（以各模块 config 为准）：

| 变量 | 模块 | 说明 |
|---|---|---|
| `POSTGRES_URL` / `REDIS_URL` | 全部 | 存储连接 |
| `WECHAT_*` / `DOUYIN_*` / `TAOBAO_*` / `KUAISHOU_*` | gateway | 渠道凭据（静态配置） |
| `DATABASE_URL` + `AES_ENCRYPTION_KEY` | gateway/admin | 动态渠道配置（portal 管理渠道凭据） |
| `CHATWOOT_WEBHOOK_TOKEN` | gateway | Chatwoot 坐席回复回流验签 |
| `DIFY_ADMIN_URL` / `DIFY_ADMIN_EMAIL` / `DIFY_ADMIN_PASSWORD` | admin | Dify 控制台凭据，开户/开通用 |
| `DIFY_API_BASE_URL` | admin/router | Dify 服务 API 根（`.../v1`） |
| `DIFY_DATASET_API_KEY` | admin | 数据集级 key，知识库自助的前提（见上文） |
| `DIFY_INDEXING_TECHNIQUE` | admin | `high_quality`（默认）/ `economy`（无嵌入模型必选） |
| `CHATWOOT_BASE_URL` / `CHATWOOT_PLATFORM_TOKEN` / `CHATWOOT_WEBHOOK_URL` | admin | 一键开户的 Chatwoot 步骤（不配则降级跳过） |
| `ACEST_KB_URL` / `ACEST_KB_TOKEN` | router | acest 双知识库（可选） |
| `INTENT_TRIAGE` | router | 调 AI 前意图分诊：`off` / `shadow`（默认，只记指标）/ `on` |
| `ONTOLOGY_ENABLED` | router | 领域本体总开关（默认 `true`），逐线在 `config_json.ontology` 开通 |
| `PARTITION_MONTHS_AHEAD` / `PARTITION_CHECK_INTERVAL` | router | 月分区自动续期（默认提前 3 个月、每 24h） |
| `JWT_SECRET` | admin | 后台与 portal 鉴权 |

## 分区与保留

`messages` 与 `audit_logs` 按月 RANGE 分区。**分区由 router 自己续期**（启动时一次 + 每 24h），
缺分区不是降级而是硬失败：插入直接报 `no partition of relation ... found for row`。
监控 `router_partition_maintenance_errors_total`，任何增长都要告警。
写 `audit_logs` 的是 admin，续期的是 router——只部署 admin 不部署 router 时，
需自行跑维护脚本：

```bash
psql $POSTGRES_URL -f unica/scripts/maintain_partitions.sql   # audit_logs 保留 90 天
```

入站去重的唯一索引建在每个叶子分区上，去重窗口是一个自然月；它是 gateway Redis
去重（fail-open + TTL）的持久化兜底，命中计入 `router_messages_duplicate_total`。

## 测试

```bash
cd unica/<module> && go build ./... && go vet ./... && go test ./...
```

CI 对六个模块（含 `deploy/alertmanager/webhook-adapter`）跑 build + vet + `test -race`。

**测试全绿不等于验证过。** 哪些能力已交付但还没有证据支持——零真实流量、
Chatwoot 平台开户只对假服务器验证过、熔断阈值没有数据支撑等——
登记在 [`doc/unverified.md`](doc/unverified.md)。先读那个再判断能上线到什么程度。

带数据库的集成测试默认跳过，需显式指定一个**可写的测试库**（不复用 `POSTGRES_URL`）：

```bash
ROUTER_TEST_POSTGRES_URL="postgres://...@localhost:5432/unica_test?sslmode=disable" \
  go test ./internal/state/ -count=1
```

## 监控

Prometheus 指标：各服务 `/metrics`。Grafana 仪表盘见 `deploy/grafana/dashboards/`
（渠道流量、AI 效果、坐席绩效、队列积压、回答质量等 9 块）。
告警经 `deploy/alertmanager/webhook-adapter` 推送钉钉/飞书/企微。

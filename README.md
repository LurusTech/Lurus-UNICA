# UNICA — 跨渠道 AI 客服中枢

UNICA 是一套自托管的跨渠道 AI 客服系统：统一接入微信、抖音、淘宝、快手、小红书等平台的客服消息，由 AI（Dify）自动应答，低置信/敏感场景自动转人工（Chatwoot），并配套完整的运营报表与监控。

```
渠道平台 ──► Gateway ──► Redis Streams ──► Router ──► Dify (AI 应答)
(微信/抖音/淘宝/                            │  │
 快手/小红书)                               │  └──► acest kb-server (可选，双知识库)
                                           ▼
                                    Chatwoot (人工坐席)
                                           │
              Admin (管理后台)      Reporter (报表 API) ──► Grafana
```

## 模块

| 模块 | 职责 | 端口 |
|---|---|---|
| `unica/gateway` | 渠道 webhook 接入、签名验签、消息标准化、去重/重试/死信、令牌生命周期 | 8080 |
| `unica/router` | 会话状态机、AI 调用与知识注入、护栏决策、转人工、满意度调查、营销意图 | 8081 |
| `unica/admin` | 管理后台 API：产品线/渠道/用户/RBAC/审计、一键开通 Dify 工作区 | 8082 |
| `unica/reporter` | 报表 API：渠道流量、AI 效果、坐席绩效、知识库命中率 | 8083 |
| `deploy/` | K8s 清单、Chatwoot/Dify 部署、Prometheus/Grafana/告警适配器 | — |

技术栈：Go 微服务 + PostgreSQL + Redis Streams；对话工作台用 Chatwoot，AI 编排用 Dify。

## 知识库架构：Dify 或 acest，如何选

UNICA 的 AI 应答支持两层知识来源，可以只用 Dify，也可以叠加 acest 双知识库。

### 方案 A：仅 Dify（默认）

Dify 应用自带 RAG：每条产品线绑定一个 Dify 数据集（`product_lines.config_json.dify_dataset_id`），文档上传后由 Dify 负责切片、向量化和应答时检索。不设置 `ACEST_KB_URL` 即为此模式，零额外组件。

适合：知识全部来自静态文档（产品手册、FAQ、政策），不需要系统"越用越聪明"。

### 方案 B：Dify + acest 双知识库（推荐）

[ACEST](../acest/acest)（Adaptive Context Engine for Smart Tasks）是一个 Rust 编写的 AI 终端助手，内含两套可独立使用的知识系统，通过其无头 `kb-server` 以 HTTP API 对外提供：

| | Dify 数据集 | acest 外源知识库 | acest 经验知识库 |
|---|---|---|---|
| 内容 | 静态文档 RAG | 多源文档检索 | **蒸馏后的经验条目**（Bullet） |
| 来源 | 手动上传 | LocalDocs / Dify / RAGFlow / FastGPT / MCP / 向量库 | **客服交互自动沉淀** |
| 检索 | Dify 内部 | 混合检索（向量+关键词） | 混合检索 + 召回计数 + 衰减 |
| 写入 | 上传文档 | acest 桌面端/Agent 技能注册 | **UNICA 自动回写**（经蒸馏管线） |
| 越用越聪明 | 否 | 否 | **是** |

双库叠加后的应答流程：

1. **召回**：router 在每次调用 Dify 前，并行查询 acest 经验库（`/api/v1/search`）与外源库（`/api/v1/kb/search`），命中片段注入 Dify 变量 `experience_context` / `knowledge_context`；
2. **应答**：Dify 结合自身 RAG + 注入的经验/知识生成回答，护栏评估置信度；
3. **回写**：AI 直接应答 → 成功样本；转人工 → 失败样本（含转接原因）。router 异步提交到 `/api/v2/experiences`，acest 侧经 reflector→评估→curator 去重后蒸馏成经验条目——**下次同类问题即可召回**，形成闭环。

整个集成 **fail-open**：kb-server 不可达时仅缺少注入上下文，客服主流程零影响。

选择建议：

- 起步/纯文档场景 → 方案 A；
- 希望积累"哪些回答有效、哪些问题必转人工"的运营经验，或已有 acest 管理的多源知识 → 方案 B；
- 从 A 升级到 B 只需设置两个环境变量并在 Dify 应用里声明两个变量，无迁移成本。

## acest 使用指南

### 1. 构建并启动 kb-server

acest 的 kb-server 已包含在默认构建中：

```bash
cd ../acest/acest/acest-rs
cargo build -q -p acest-cli --release

# 启动无头知识库服务（--allow-write 开启经验回写）
ACEST_KB_API_TOKEN=<你的token> ./target/release/acest kb-server --allow-write
# 默认监听 127.0.0.1:7423；跨机访问加 --bind 0.0.0.0（配合防火墙/Tailscale）
```

与 UNICA 同机部署时直接用 `http://127.0.0.1:7423`；跨机用 Tailscale IP。生产建议为客服域使用独立的 acest 数据目录（acest_home），与个人编码经验库分开。

经验蒸馏不强制要求 LLM：未配置模型时自动使用规则式评估管线（提交不会失败，只是蒸馏质量略低）。

### 2. 配置 UNICA router

```bash
ACEST_KB_URL=http://127.0.0.1:7423   # 不设置 = 禁用 acest 集成（方案 A）
ACEST_KB_TOKEN=<与 kb-server 一致>
# 可选：
ACEST_RECALL_TIMEOUT=2s              # 召回总预算（两库并行共用）
ACEST_RECALL_TOP_K=3                 # 每库最多注入片段数
ACEST_KB_SOURCES=docs,faq            # 外源库 source 过滤（默认全部）
ACEST_EXPERIENCE_QUEUE=256           # 经验回写队列容量
```

### 3. Dify 应用侧声明变量

在 Dify 应用编排中新增两个段落型变量并在提示词中引用（不声明则注入无效）：

- `experience_context` — 历史经验（如"退款需在订单页发起，直接给链接会被平台拦截"）
- `knowledge_context` — 外源知识片段

提示词示例：

```
你是客服助手。参考以下历史经验和知识回答，经验优先：
【历史经验】{{experience_context}}
【参考知识】{{knowledge_context}}
```

### 4. 外源知识源注册（acest 侧）

外源知识源通过 acest 桌面端（知识库页面）或 Agent 技能 `knowledge_action` 注册，支持 LocalDocs（本地文档导入）、Dify/RAGFlow/FastGPT（外部 RAG 平台）、MCP 服务、Qdrant 等向量库。HTTP API 仅提供检索与源的启停更新，不提供创建源——先在 acest 侧配好，UNICA 只消费。

### 5. 验证闭环

```bash
# kb-server 存活
curl http://127.0.0.1:7423/api/v1/health

# 发几条测试消息后，确认经验在积累（acest 侧）
acest ace status        # 条目总数/分区统计
acest ace search 退款   # 检索沉淀的经验

# UNICA 侧指标（router :8081/metrics）
# router_acest_recall_total{kind,status}      召回命中/错误
# router_experience_submitted_total{outcome}  经验提交量
# router_experience_dropped_total             队列满丢弃
```

详细数据流与运维说明见 [`unica/doc/acest-kb-integration.md`](unica/doc/acest-kb-integration.md)。

## 领域本体：让 AI 不再靠常识补答案

RAG 只保证"检索到了相似内容"，保证不了"说的是对的"。三条产品线的退货政策互相冲突
（7 天无理由 / 15 天限未拆封 / 根本没有无理由退货），检索分数区分不了它们。

领域本体把这类确定性事实从概率通道里拿出来：每条产品线声明自己的政策事实与"本业务不提供什么"，
router 在调用 AI 前注入，回答后校验。与行业无关——`deploy/config/ontology/examples/` 下有
留学中介与财务代理的完整样例，它们同时是保证机制通用性的测试夹具。

```bash
cd unica/router
go run ./cmd/ontology validate -dir ../../deploy/config/ontology   # 检查，无需数据库
go run ./cmd/ontology preview  -dir ../../deploy/config/ontology   # 看注入内容
POSTGRES_URL=... go run ./cmd/ontology publish -dir ../../deploy/config/ontology
```

两个独立开关写在 `product_lines.config_json.ontology`：`inject_facts`（注入事实）与
`validation`（`off`/`shadow`/`enforce`）。**默认都关**，升级不改变任何现有行为。

`enforce` 带熔断：本体写错一条断言会让每一条碰到它的正确回答都被压下转人工，
所以最近窗口内被拦比例超过 25% 就自动停止拦截并告警（`breaker` 块可调，默认开）。
熔断态不是安全态，是拿"发出可能有误的回答"换"人工队列不被打爆"，
代价记在 `router_ontology_breaker_bypassed_total` 里。

编写指南见 [`doc/ontology-schema.md`](doc/ontology-schema.md)。

## 快速开始

```bash
# 1. 基础设施（PostgreSQL + Redis），或使用 deploy/ 下的 K8s 清单
# 2. 迁移
for f in unica/router/migrations/*.sql; do psql $POSTGRES_URL -v ON_ERROR_STOP=1 -f $f; done

# 3. 各服务（每个模块独立 go.mod）
cd unica/gateway && go build ./... && ./gateway   # 同理 router/admin/reporter
```

核心环境变量（各模块 `cmd/*/main.go` 的 `loadConfig` 为准）：

| 变量 | 模块 | 说明 |
|---|---|---|
| `POSTGRES_URL` / `REDIS_URL` | 全部 | 存储连接 |
| `WECHAT_*` / `DOUYIN_*` / `TAOBAO_*` / `KUAISHOU_*` | gateway | 渠道凭据（静态配置） |
| `DATABASE_URL` + `AES_ENCRYPTION_KEY` | gateway | 启用动态渠道配置（后台管理渠道） |
| `CHATWOOT_WEBHOOK_TOKEN` | gateway | Chatwoot 坐席回复回流 |
| `ACEST_KB_URL` / `ACEST_KB_TOKEN` | router | acest 双知识库（可选，见上文） |
| `INTENT_TRIAGE` | router | 调用 AI 前的意图分诊：`off`（旧关键词行为）/ `shadow`（默认，只记录指标不改判定）/ `on`（分诊决定路由，关键词表退役） |
| `ONTOLOGY_ENABLED` | router | 领域本体总开关（默认 `true`）。逐产品线在 `config_json.ontology` 里开通，见 [`doc/ontology-schema.md`](doc/ontology-schema.md) |
| `PARTITION_MONTHS_AHEAD` / `PARTITION_CHECK_INTERVAL` | router | `messages`、`audit_logs` 月分区自动续期（默认提前 3 个月、每 24h 检查）。见下文 |
| `JWT_SECRET` | admin | 后台鉴权 |

## 分区与保留

`messages` 与 `audit_logs` 按月 RANGE 分区。**分区由 router 自己续期**（启动时一次 + 每 24h），
不再依赖外部 cron —— 缺分区不是降级而是硬失败：插入直接报
`no partition of relation ... found for row`，router 会 ack 并丢消息。
监控 `router_partition_maintenance_errors_total`，任何增长都要告警。
写 `audit_logs` 的是 admin，续期的是 router —— 只部署 admin 不部署 router 时，
需要自行跑下面的维护脚本。

入站去重的唯一索引建在**每个叶子分区**上（分区表的唯一约束必须含分区键，
而 `(platform_msg_id, created_at)` 恒不重复、等于没有约束），因此去重窗口是一个自然月。
它是 gateway Redis 去重（fail-open + TTL）的持久化兜底；命中时 router 跳过模型调用，
计入 `router_messages_duplicate_total`。

数据保留仍是显式操作，不放进服务：

```bash
psql $POSTGRES_URL -f unica/scripts/maintain_partitions.sql   # audit_logs 保留 90 天
```

## 测试

```bash
cd unica/<module> && go build ./... && go vet ./... && go test ./...
```

带数据库的集成测试默认跳过，需显式指定一个**可写的测试库**（不复用 `POSTGRES_URL`）：

```bash
ROUTER_TEST_POSTGRES_URL="postgres://...@localhost:5432/unica_test?sslmode=disable" \
  go test ./internal/state/ -count=1
```

## 监控

Prometheus 指标：各服务 `/metrics`。Grafana 仪表盘见 `deploy/grafana/dashboards/`（渠道流量、AI 效果、坐席绩效、队列积压等 8 块）。告警经 `deploy/alertmanager/webhook-adapter` 推送钉钉/飞书/企微。

# Active Task: 配置面重构 B 组 —— 运行开关落库热生效、索引方式可写、未启用能力清单、设置页四区重排

## Context

`INTENT_TRIAGE` / `SCENE_MODE` 是灰度开关却锁在 router 的 env 里，改一次重启一次；
`DIFY_INDEXING_TECHNIQUE` 同理，且平台页把它**硬编码显示成 `high_quality`**（已确认的缺陷，见决策 6）；
几类"为空即功能悄悄消失"的配置在界面上完全看不出来。

本组把两个开关和索引方式收进 `platform_settings` 表（env 只作首次种子），router 每 10 秒轮询热生效，
平台页可写并走审计，租户「设置」页按四区重排并新增只读的「平台运行状态」区。
落地计划文档第四、五、七条与验收 3、4。

## 已定的设计决策

1. **热读机制：router 定时轮询库（10s）写入原子快照。** 热路径每条消息只读进程内的
   `atomic.Pointer`，不碰库也不碰 Redis。推送端点在 router 重启、多实例、推送失败三种情况下
   都会制造"库里一个值、进程里另一个值"的静默分歧；Redis 键可被驱逐或 flush，最后仍要回落到库。
   轮询失败时保留上一次成功的快照，并把时效暴露到 `/configz`——让"陈旧"可见，而不是被默认值顶替。
   **端到端最坏延迟**：消息实际按新值路由 ≤ 10s；平台页（写入后主动清缓存）≈ 12s；
   租户页最坏 10s + 30s 缓存 = 40s。
2. **env 只在"库里没有这一行"时由进程启动种入，之后库为唯一权威。** 迁移不插行。
   行已存在而 env 不同时：库值生效，启动日志警告，`/configz` 用 `env_shadowed` 点名那个环境变量。
   分歧因此是一句可读的话，而不是一处静默。只有启动时读不到库才回落到 env，来源标 `env_fallback`。
3. **`/configz` 现有键语义不变**（仍是 router 此刻实际在按的值），只新增来源与时效字段——
   这样 admin 的 bridge 与租户页无需改动也仍然正确。
4. **三个设置共用一张 `platform_settings` 键值表**，key 与 value 的合法组合由 CHECK 封闭。
   三者都是平台级、无产线覆盖、无投影中间态的单值枚举，021 需要版本号与 `pushed_at` 的理由
   在这里一条都不成立。
5. **B5 明确不覆盖 `WECHAT_ENCRYPTED_MODE`。** 它配错时第一次回调验签就大声失败，
   不是"为空即静默消失"那一类；且对错取决于微信平台那侧，控制台看到布尔值也判断不了。
   为它给第三个进程加 `/configz`，投入与信息量不成比例。
6. **逐库索引方式：平台页显示全员清单（写控件在此），租户页显示自己那一个。**
   数据取自已有的 `platform/knowledge.go` roster（已逐库读过配置并区分"尚未确定"），
   不新调 Dify 的 `/datasets` 列表——那条路要 dataset key、要分页、且列的是全 workspace
   而非绑定到产线的库。索引方式写入**不做"先验证后提交"**：没有廉价可逆的探针，
   且选错的后果是大声的；改以 CHECK + 同屏清单 + 强制勾选"存量不迁移"把风险摊开。

### 开工前已核实的事实（2026-09-02）

- `GET {DIFY_API_BASE_URL}/datasets` 每库都带 `indexing_technique`，**但 `document_count == 0`
  的库该字段是 `null`**——Dify 要到第一篇文档索引完才定下索引方式。任何逐库展示都必须把
  "尚未确定"与真实技术分开，不能拿默认值顶上。
- 只读镜像已全通：router `/configz` → `bridge.RouterBridge`（30s 缓存）→ 平台页，
  **且租户页已经拿到一份收窄的 `runtime`**（`ai-settings.html` 用它把转人工关键词标成"已由意图分诊接管"）。
  B6 第四区缺的是排布，不是管道。
- `r.triageMode` / `r.sceneMode` 是普通字段，被 4 个 worker 协程并发读，热切换必须走原子快照。
- 模型配置那条路**不是先例**：它每请求重查库再推给外部 Dify，从没有过
  "运行中的 Go 进程被库里的值改掉内存配置"。B2 是头一遭。
- 预览环境现状：`intent_triage=shadow`、`scene_mode=on`、`ontology_enabled=true`、
  `acest_enabled=false`（最后一条正是 B5 的现成样本）。
- gateway 只有 `/healthz`，没有 `/configz`；admin 只认识它一个 `GATEWAY_HOST`（决策 5 的由来）。

## Critical Files

- `doc/plan-workbench-settings.md`（第四、五、七条与 B 组清单，完成后勾选）
- `unica/router/migrations/022_platform_settings.sql`（新建）
- `unica/pkg/platformsettings/store.go`、`store_test.go`（新建，router 与 admin 共用）
- `unica/router/internal/routing/switches.go`、`switches_test.go`（新建：原子快照 + 轮询 + 种子）
- `unica/router/internal/routing/router.go`（:97-98,155-170,542-566,616-625 改读快照）
- `unica/router/internal/routing/router_wiring_test.go`、`judge_test.go`（构造方式随之改）
- `unica/router/cmd/router/main.go`（:80-87 装配、:162-190 `/configz` 扩展、:318-338 env 变为种子）
- `unica/admin/internal/bridge/router.go`（`RuntimeSwitches` 加字段、`Invalidate()`）
- `unica/admin/internal/bridge/dify.go`（:39-43,340,409-441 索引方式改为实时读取）
- `unica/admin/internal/platform/platform.go`（:191-198,265-288 修硬编码缺陷；新增 `HandleSwitches`）
- `unica/admin/internal/platform/knowledge.go`（`knowledgeRow` 加结构化 `indexing`）
- `unica/admin/internal/capability/probe.go`、`probe_test.go`（新建：未启用能力探测）
- `unica/admin/internal/tenant/knowledge/knowledge.go`（:85-106,383,409 索引方式改为实时读取）
- `unica/admin/internal/tenant/aisettings/aisettings.go`（:285-297,705,1306-1311,1391-1404）
- `unica/admin/internal/config/config.go`（:29-33,74 注释改为"种子"）
- `unica/admin/cmd/admin/main.go`（:122-133,175-179,431-449,496-504 装配与路由）
- `portal/admin.html`（:1067-1214 开关可写、索引方式可写 + 全员清单、能力清单）
- `portal/ai-settings.html`（:3,361,388-690 改名与四区重排；:2285,2413-2418,2704-2720 复用 `cfg.runtime`）
- `portal/home.html`（:173,205）、`portal/knowledge.html`（:710）、`portal/admin.html`（:573,657,1218,1460）改名

## Step-by-Step Plan

- [x] 1. **事实核验**（只读，不改代码）：结果见文末「第 1 步核验结果」。
      (a) `aisettings.go:1306-1311` 的 `knowledgeStatus` 是否已含数据集实际 `IndexingTechnique`；
      (b) `bridge/dify.go` 中 `b.config.IndexingTechnique` 的全部读点（预期 :340 建库、:423-441 漂移校验，确认无第三处）；
      (c) 预览环境迁移的施加方式（021 当时怎么跑的，022 照做）；
      (d) 仓库内 compose / `.env.example` / README 里三个变量的出现位置，后续把注释改成"仅首次种子"；
      (e) 仓库里读迁移文件做断言的测试写法（如 `promptversions_test.go` / `audit/actions_test.go`），供第 3 步沿用。
      核验结果一行一条写回本文件 Current Status 下方。

- [x] 2. **新建 `unica/router/migrations/022_platform_settings.sql`**：
      表 `platform_settings(key TEXT PRIMARY KEY, value TEXT NOT NULL, source TEXT NOT NULL, note TEXT,
      updated_by UUID, updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`；
      `CHECK` 封闭 key 与 value 的合法组合：`intent_triage`/`scene_mode` ∈ {off, shadow, on}，
      `dify_indexing_technique` ∈ {high_quality, economy}；`source` ∈ {seed, console}。
      **不插任何行。** 头注释按 021 的体例说明：为什么是键值表而不是 021 那种版本表
      （无产线覆盖、无投影中间态、单值枚举）；为什么迁移不种行而由各自读 env 的进程在启动时种入；
      为什么 CHECK 是封闭的（014/020 同一理由）；`source='seed'` 意味着
      "这个值来自某次启动的环境变量，之后环境变量不再有发言权"。

- [x] 3. **新建 `unica/pkg/platformsettings`**（router 与 admin 共用）：
      常量 `KeyIntentTriage`、`KeySceneMode`、`KeyIndexingTechnique` 与每个 key 的 `AllowedValues`；
      `Store.Load(ctx, keys...) (map[string]Setting, error)`；
      `Store.Seed(ctx, key, value) (inserted bool, err error)`（`INSERT ... ON CONFLICT (key) DO NOTHING`，source=seed）；
      `Store.Set(ctx, key, value, source, updatedBy, note) error`（upsert，拒绝不在 `AllowedValues` 里的值）。
      测试：解析 022 的 CHECK，断言代码里的 key 集合与每个 key 的合法值集合与迁移**完全一致**；
      `Seed` 两次只插一行；`Set` 拒绝非法值。

- [x] 4. **router 热读**（`internal/routing/switches.go` + 装配）：
      (a) `Switches` 持有 `atomic.Pointer[snapshot]`，
      `snapshot{triage; scene; sources map[string]string; readAt; err string; envShadowed map[string]string}`；
      对外 `Triage()`、`Scene()`、`Snapshot()`；
      (b) `NewSwitchPoller(store, envTriage, envScene, interval)`：启动时先 `Seed` 两个 key
      （值来自 env 解析结果，空则用 `DefaultTriageMode`/`DefaultSceneMode`），再 `Load`；
      行已存在且与 env 不同 → 记入 `envShadowed` 并打警告日志；`Load` 失败 → 用 env 值起步、
      source 标 `env_fallback`、日志说明；之后每 10s `Load` 一次，失败时保留上一快照只更新 `err`，
      值变化时打一条 `intent_triage shadow -> on (source console)`；
      (c) `Router` 的 `triageMode`/`sceneMode` 字段替换为一个 `switchSource` 接口，
      `router.go:542,547,566,616,625` 经接口读取；`RouterConfig` 去掉两个字段改为传 `Switches`；
      提供 `StaticSwitches(triage, scene)` 供测试与无库场景，改 `router_wiring_test.go`、`judge_test.go`；
      (d) `main.go`：env 解析保留但注释改为"仅作首次种子"；装配 store → poller → router；
      `/configz` 现有键改从 `Snapshot()` 取（语义不变），新增 `switch_sources`、`switches_read_at`、
      `switch_poll_interval`、`env_shadowed`、`switches_error`；优雅停机时停掉 poller；
      (e) 第 1(d) 步查到的 env 示例注释改为"首次启动种入数据库，之后以平台管理页为准"。
      测试（假 loader，不连库）：改变返回值后下一次 tick 内 `Triage()` 变化；loader 报错时旧值保留
      且 `err` 非空；空表时 `Seed` 被调用且值等于 env；行存在且与 env 不同时 `envShadowed` 含该 env 名
      且生效值是库值；`Load` 失败的启动路径 source 为 `env_fallback`。

- [x] 5. **admin bridge**（`bridge/router.go`）：`RuntimeSwitches` 增加 `SwitchSources`、
      `SwitchesReadAt`、`SwitchPollInterval`、`EnvShadowed`、`SwitchesError`（JSON 名与 `/configz` 一致）；
      新增 `Invalidate()` 清空缓存；结构体注释里"They change only when the router restarts"改掉。
      测试：`Invalidate()` 后下一次 `Switches()` 重新请求。

- [x] 6. **admin 写路径**（`platform/platform.go` + `cmd/admin/main.go`）：
      新增 `PUT /api/v1/platform/switches`（管理员限定，检查在 handler 内，与 `HandleModel` 同式），
      请求体 `{intent_triage?, scene_mode?, dify_indexing_technique?, acknowledge_existing_not_migrated?, note?}`：
      至少一个 key；值不在 `AllowedValues` → 400 并原样列出合法值；带 `dify_indexing_technique`
      而未勾确认 → 400「存量文档不会自动迁移，请确认」；store 未装配 → 503（中文句式同 `:413`）；
      逐 key：读旧行 → `Set(source=console, updated_by=claims.UserID)` → 审计
      `LogEvent(action="update", resource_type="platform_setting", resource_id=key,
      before={value,source}, after={value,source,note})`，一 key 一行；写库失败也记一行 `after.ok=false`；
      成功后调 `routerBridge.Invalidate()`，响应 `{ok, changed, stored, effective, poll_interval}`。
      `SettingsConfig` 加 `Settings`；`GET /platform/settings` 新增 `stored_switches`、`switches_editable`。
      测试：非法值 400；缺确认 400 且不写库；两 key 一次提交写两条审计且 before/after 对；
      写库失败时审计 `ok=false`；成功后 bridge 缓存被清；
      `TestAuditActionsAreInTheMigrationVocabulary` 仍通过（`update` 在词表内）。

- [x] 7. **B4 读侧与硬编码缺陷修复**（admin）：
      (a) `cmd/admin/main.go` 启动时对 `KeyIndexingTechnique` 执行 `Seed`（值取 `cfg.DifyIndexingTechnique`），
      行存在且与 env 不同 → 警告日志；
      (b) `DifyBridgeConfig.IndexingTechnique string` 改为 `func(context.Context) string`
      （读 store，读不到时回落到 env 种子并记日志）。**核验修正**：全 bridge 只有
      `SetDatasetRetrievalWith` 一个函数读它（`dify.go:425,426,431,432,441`），建库与漂移校验
      是它的两个调用者而非两个读点——改这一个函数即可。
      `knowledge.NewHandler` 的 `indexingTechnique` 同样改为读取函数，`:383,409` 调用；
      `:93-100` 的 economy 警告移到读取函数里且只在值变化时打；
      (c) `platform.go:265-288`：`knowledgeDefaults.IndexingTechnique` 改为实时值，
      `RetrievalModel(...)` 用同一实时值派生 `search_method`/`top_k`——注释里
      "the technique this deployment actually creates datasets with"由此才成真；
      `compiledSection.Knowledge` 增加 `indexing_stored{value,source,updated_at}`；
      (d) `platform/knowledge.go` 的 `knowledgeRow` 新增
      `Indexing *{Decided bool; Technique, SearchMethod, Reason string}`：`retrievalStep` 读到的 cfg
      同时填入，"尚未确定" → `Decided=false`，读失败 → `Reason`；不改现有 `Steps` 语义；
      (e) 租户 `knowledgeStatus`（`aisettings.go:244-283`）**只补一个 `PlatformIndexingTechnique`**。
      **核验修正**：本库实际的 `IndexingTechnique` / `SearchMethod` / `TopK` 已在（:1333-1341 从 Dify 读回），
      "还没有文档所以索引方式未定"也已由 `Empty` + `RetrievalIndexPending` 分支表达并配了中文原因
      （:1343-1369）。缺的只是"平台给新文档用的是哪个"这一半，两者并排才构成对照。
      测试：store 为 economy 时 `GET /platform/settings` 的 `knowledge.indexing_technique` 为 economy
      且 `search_method` 为 keyword_search——**这是被确认的缺陷，测试必须先红后绿**；
      roster 对 `indexing_technique` 为空的数据集给出 `Decided=false` 而不是空串技术名；
      bridge 漂移校验用的是读取函数的返回值。

- [x] 8. **B5 未启用能力探测**（新建 `unica/admin/internal/capability`）：
      `Probe{DatasetAPIKeySet, RouterConfigured bool; Router SwitchReader}`，`List(ctx) []Capability`，
      `Capability{Key, Title, State("on"|"off"|"unknown"), Reason, Owner}`；
      三项：`knowledge_management`（`DIFY_DATASET_API_KEY` 为空 → off，原因写
      "全平台租户的知识库管理已禁用：上传、删除、查看分段均不可用"）、
      `router_runtime`（`ROUTER_INTERNAL_URL` 为空 → off）、
      `acest`（runtime 读不到 → unknown 并带原因；`acest_enabled=false` → off，
      原因"经验库召回与反馈未装配"）；
      平台 `settingsResponse` 与租户 aisettings 响应各加 `capabilities`（租户侧只带 key/title/state/reason）；
      两个 handler 共用同一个 Probe 实例。
      测试：key 为空 → knowledge_management off 且 reason 非空；runtime 不可达 → acest 为 unknown
      而非 off；全部配置齐 → 三项 on。

- [x] 9. **平台管理页**（`portal/admin.html:1067-1214`）：
      (a) 「运行开关」拆成两组：意图分诊 / 商业阶段策略变为可写（三态 select + 一句后果说明 +
      "库里：X · router 在用：Y"），标签改为"平台设置 · 保存后 router 最长 N 秒内生效"
      （N 取 `switch_poll_interval`）；其余行保持"router 环境变量 · 改完需重启 router"；
      (b) `env_shadowed` 非空时显示"环境变量 INTENT_TRIAGE=on 已被忽略，生效的是库里的值；
      请从部署配置里删掉它"；`switches_read_at` 距今超过两个轮询间隔或 `switches_error` 非空时
      显示"router 上次成功读库是 … 前，当前值可能陈旧"；
      (c) 保存后每 2s 重拉 `/platform/settings`，直到 `runtime.switches` 与 `stored_switches` 一致
      或超过 poll_interval+5s，超时则显示"已落库，但 router 尚未拾取"；
      (d) 「知识库检索」组：索引方式变为 select，提交前弹确认（必须勾选"我知道存量文档不会自动迁移"），
      旁边渲染从 roster 汇总的全员清单：每条产线"库 X：high_quality / economy / 尚未确定（还没有文档）"，
      所选值与任一已确定库不一致时醒目提示"改后新文档与该库现有索引不一致，检索恒空且不报错"；
      (e) 新增「本部署未启用的能力」组，逐条 state/reason，unknown 与 off 用不同样式；
      (f) `:573,657,1218,1460` 的「AI 设置」改为「设置」。

- [x] 10. **租户「设置」页**（`portal/ai-settings.html`）与入口：
      (a) `:3,361` 改名「设置」；`home.html:173,205`、`knowledge.html:710` 同步改名，
      `home.html:205` 卡片描述改为覆盖提示词版本、阈值与转人工、检索 top_k、知识库体检、
      满意度调研、平台运行状态；
      (b) 现有七张卡按四区加分区标题重排：1 回答行为（系统提示词、转人工与阈值、测试消息）、
      2 满意度调研、3 知识与检索（知识库绑定、生效模型）、4 平台运行状态（只读）；
      接通体检保留在页顶；**不改任何卡片内部逻辑与 id**；
      (c) 第 4 区新增：意图分诊（值 + "on 时转人工关键词整表停用"，与 `:2704-2720` 的禁用提示同源）、
      商业阶段策略、本体总闸（关闭时说明本租户事实注入不会生效）、
      索引方式（"平台新文档：X / 本知识库实际：Y 或 尚未确定"，不一致时红字说明后果）、
      未启用的能力清单；`runtime.available=false` 时整区显示"读不到平台运行状态（原因）"，
      不用默认值顶替（同 `admin.html:1172-1177` 口径）；
      (d) 第 4 区顶部一句话："这些由平台方决定，本页只读；能改的在上面三区。"

- [x] 11. **单元测试与静态检查**：`cd unica && go test ./pkg/... ./router/... ./admin/...`
      （含第 3、4、5、6、7、8 步新增测试与 `TestAuditActionsAreInTheMigrationVocabulary`）；
      `go vet ./...`；确认第 7(c) 步的缺陷测试**在修复前失败、修复后通过**。（Go 没有 `-q`。）

- [x] 12. **实机验收**（预览环境）：
      (a) **先 `pg_dump` 备份**（017/018 的先例，存 `~/unica-run/unica-pre022.dump`），
      再 `psql "$POSTGRES_URL" -v ON_ERROR_STOP=1 -f 022_platform_settings.sql`
      （本仓库没有迁移执行器，也没有 `schema_migrations` 台账，靠 `IF NOT EXISTS` 可重跑）；
      router 与 admin 各重启一次（**这是本组唯一一次重启**）；
      `/configz` 应显示 `switch_sources` 均为 `seed`、`switches_read_at` 在 10s 内、`env_shadowed` 为空；
      `SELECT * FROM platform_settings` 三行齐全、source=seed；
      (b) **验收 3**：平台页把意图分诊从 shadow 改到 on，不重启 router，router 日志出现
      `intent_triage shadow -> on`，`/configz` 在 10s 内变为 on，平台页显示"router 在用：on"；
      租户设置页第 4 区显示 on，转人工关键词框灰掉且提示与第 4 区一致；改回 shadow 后恢复；
      `audit_logs` 有两条 `platform_setting/update`，before/after 正确；
      (c) **分歧可见性**：给 router 的 env 设 `INTENT_TRIAGE=off` 并重启，日志应有警告，
      `/configz.env_shadowed` 含 `INTENT_TRIAGE`，平台页显示"已被忽略"，而生效值仍是库里的 shadow；
      验完把 env 删掉；
      (d) **验收 4**：把 admin 的 `DIFY_DATASET_API_KEY` 置空重启，租户设置页第 4 区
      「未启用的能力」列出知识库管理与原因，平台页同样列出；恢复 key 重启后该项变为 on；
      (e) **B4**：把索引方式改为 economy（确认框必须勾选才能提交），平台页显示 economy 与
      keyword_search（**修复前这里会显示 high_quality**），全员清单中已有文档的库显示各自真实技术、
      无文档的库显示"尚未确定"而不是空值；改回 high_quality；两次改动均有审计行；
      (f) 通过后在 `doc/plan-workbench-settings.md` 勾掉 B1–B6，在「当前状态」记录验收日期
      与"WECHAT_ENCRYPTED_MODE 明确未纳入 B5"的理由。

## 并行图

- **串行主干**：1 → 2 → 3。`platformsettings` 的 API（key 常量、`Load/Seed/Set`）是所有后续步骤的
  公共依赖，必须先定稿。
- **第 3 步后三条独立车道**：
  - 车道 R（router）：4。只依赖第 3 步，与 admin 侧无耦合。
  - 车道 A（admin 后端）：5 → 6；7 与 8 各自独立，可与 5/6 并行。7 与 6 唯一交点是
    `SettingsConfig` 多一个字段。
  - 车道 F（前端）：9、10 按本文列出的 JSON 契约先建，两者互相独立。
- **汇合点**：三车道合流后评审（重点：前端读的字段名与后端 JSON tag 完全一致；`RouterConfig`
  去掉两字段后 router 测试全部改过；审计动词只用了 `update`），然后 11 → 12 串行。
- **单一最高风险步：第 4 步。** 它是本组唯一触碰每条客户消息热路径的改动，同时承担种子/分歧语义——
  种子逻辑写错（行已存在时仍用 env 覆盖、或 `Load` 失败时回落到代码默认值而非 env）不会有任何报错，
  只会让某个部署静静地按错误的开关路由。这一步先做、先过测试、先在预览机上独立跑 12(a)(c)。

## Current Status

- [x] **步骤 1-12 全部完成，实机验收通过。未提交。**

### 第 4 步实机验证结果（2026-09-03，预览环境）

**先做的事**：`pg_dump` 备份到 `~/unica-run/unica-pre022.dump`（892K）；
旧 router 二进制备份为 `~/unica-run/router-scene-bin.bak-20260903-065224`。

- **迁移**：022 施加成功，表结构与预期一致；**两条 CHECK 实测都能拒**——
  `('intent_triage','high_quality')` 被 `platform_settings_key_value_check` 打回，
  `source='elsewhere'` 被 `platform_settings_source_check` 打回，拒后仍是 0 行；
  二次施加只报 `relation already exists, skipping`，可重跑成立。
- **种子**：router 重启后日志
  `intent_triage was not stored yet; seeded it with "shadow"` /
  `scene_mode ... seeded it with "on"`，两行 source=seed。
  **跨这次重启行为一字未变**——这正是迁移不预先插行的目的。
- **热生效（验收 3 的机器侧）**：直接改库把 `intent_triage` 置 `on`，**不重启**，
  **7 秒**后 `/configz` 变为 on、source 变为 console，日志出现
  `intent_triage shadow -> on (source console)`；改回同样 7-10 秒内生效。
- **分歧可见（验收 12c）**：router 的 env 里 `SCENE_MODE=on`。把库里改成 shadow 后，
  `/configz.env_shadowed = {"SCENE_MODE":"on"}`，而生效值是库里的 shadow——
  即被忽略的变量被点名、值以库为准。同时把 `intent_triage` 改成与默认不同的 `on`，
  `env_shadowed` **没有**多出 INTENT_TRIAGE（router.env 里根本没写这个变量）——
  「只报有人真写过的变量」这条成立。验完两个键都已还原成部署原本的值。
- **未能验证**：`-race` 跑不了（本机没有 gcc，`CGO_ENABLED=1` 起不来）。
  并发读写测试本身跑过且通过，但没有竞态检测器背书。快照替换用的是
  `atomic.Pointer` 且存入后不再改动其中的 map，按构造是安全的。

### 实现中改掉的一处自身缺陷

`SwitchPoller.Stop()` 原本等 `<-p.done`，而 `done` 只由 `Start()` 起的协程关闭——
**没调用过 `Start()` 的 poller 上 `Stop()` 会永久阻塞**。测试第一次跑就挂住暴露了它；
生产里若哪天在起协程前先走停机路径，停机同样会卡死。改成 `sync.WaitGroup`：
没启动过就 `Wait()` 立即返回。

### 第 1 步核验结果（2026-09-03）

- **(a) 租户侧已有一半。** `knowledgeStatus`（`aisettings.go:244-283`）已含 `IndexingTechnique` /
  `SearchMethod` / `TopK`，值是 `:1333` 从 Dify 读回的**本库实际值**；且已用 `Empty` +
  `difyapp.RetrievalIndexPending` 把"还没有文档、索引方式未定"单独表达，并配了中文原因
  （`:1343-1369`）。**第 7(e) 步因此缩为只加"平台给新文档用哪个"这一个字段。**
- **(b) 只有一个读点，不是两个。** `b.config.IndexingTechnique` 在整个 bridge 里只被
  `SetDatasetRetrievalWith` 读（`dify.go:425,426,431,432,441`）；建库（`:340`→`:359`）与
  漂移修复（`:377-379`）是它的两个**调用者**。`dify.go` 里其余的 `IndexingTechnique`
  （`:423,477,483,504,524`）读的是**数据集自己报的**技术，与配置无关，不要误改。
  租户 knowledge 侧确为两处（`:383` multipart、`:409` JSON）。
- **(c) 迁移靠手跑 psql，没有台账。** `README.md:170`：
  `for f in unica/router/migrations/*.sql; do psql $POSTGRES_URL -v ON_ERROR_STOP=1 -f $f; done`。
  没有迁移执行器，没有 `schema_migrations`，可重跑性全靠 `CREATE TABLE IF NOT EXISTS`。
  **021 怎么施加的没有记录**；有记录的先例是 017/018 各自先 `pg_dump`
  （`~/unica-run/unica-pre017.dump`）。022 照此先备份。预览机 `POSTGRES_URL` 在 `~/unica-run/router.env`。
- **(d) 要改注释的非 Go 文件三处**：`deploy/chatwoot-preview/docker-compose.yml:94`
  （`DIFY_INDEXING_TECHNIQUE` 的 `${...:-high_quality}` 默认）、`README.md:79,156,158,189,192,193`、
  `deploy/embeddings/README.md:91`（"改 admin 的这个变量再重启"——改完就不再成立）。
  `tmp/` 下的命中是 gitignore 的草稿，不动。
- **(e) 照 `audit/actions_test.go` 的写法。** 它 glob `../../../router/migrations/*.sql`、排序、
  用正则抓 `CHECK (... IN (...))`、取**最后一个**定义该约束的迁移为准，纯读文件不连库
  （`actions_test.go:142,146-176`）。从 `unica/pkg/platformsettings` 出发路径是 `../../router/migrations`。
- **(f) 审计不需要迁移。** `resource_type` 是 `VARCHAR(50)` **无 CHECK**（`009:9`），自由文本，
  `"platform_setting"` 直接可用；`action` 的封闭词表是 create/update/delete/publish/rollback/review/push
  （`020`），`update` 在内。`LogEvent(actorID, actorRole, action, resourceType, resourceID string,
  productLineID *string, beforeState, afterState interface{}, ipAddress string)`，各包各自声明同形的本地接口。
  roster 路由是 `GET /api/v1/platform/knowledge` → `platform.KnowledgeHandler.HandleList`（GET + admin）；
  `retrievalStep`（`knowledge.go:348-377`）已在调 `ClassifyRetrieval` 并把 `RetrievalIndexPending`
  当作正常态（`StepAlready`），加结构化字段是顺手的事。

### 步骤 5-12 结果（2026-09-03）

**测试**：pkg / router / admin / gateway / reporter 五个模块 `go vet` 全清、`go test` 全绿，
唯一例外是 `pkg/domain` 的 `TestSeedOntologiesMatchCannedResponses`——**本组一行未碰那个包**，
是既有失败，已登记为 D25。门户四个 HTML 的内联脚本 `node --check` 全过。

**第 7(c) 步的缺陷按要求先红后绿**：修之前测试报
`indexing_technique = "high_quality", want "economy"` 与
`search_method = "semantic_search", want keyword_search`——正是「注释说报实际值、代码读常量」
那条缺陷本身。修后通过。

### 并行实现后评审抓到、并已修掉的四条

1. **内网地址会漏给租户（高）**。`bridge/router.go` 把 `*url.Error` 整个包进错误里，
   文本含 `Get "http://<ROUTER_INTERNAL_URL>/configz": dial tcp ...`；能力探测把它拼进
   `Reason`，租户设置页原样渲染。修法不是把原因删掉——运维仍然需要它——而是拆成两个字段：
   `Reason` 是不含地址的说明（租户可见），`Detail` 是原始错误（只给平台页，
   租户侧的 `tenantCapability` 结构性地不拷贝它，和 `Owner` 同一处理）。
   同时修掉租户 `runtime.reason` 里同样的原样透传（这条是既有的，本组把它显示得更显眼了）。
   已加测试 `TestTheRouterAddressNeverReachesTheReason` 钉住。

2. **「读不到」被渲染成「还没有文档」（中）**。后端对「Dify 连不上」与「库里还没有文档」
   都返回 `decided:false`，前端一律显示「尚未确定（还没有文档）」——Dify 一挂，
   整张清单就会告诉运维「你的库都是空的」。修法是把区别做进类型：`knowledgeIndexing`
   增加 `Known`（照抄同文件里 `knowledgeDocuments.Known` 的先例），三种空各说各的。
   已加测试 `TestRosterKeepsUnreadableApartFromUndecided`。

3. **设置库读不出来时页面显示成「还没有落库」（中）**。读失败被吞掉、只留日志，
   响应里没有任何字段说「这次没读到」，`switches_editable` 照旧为 true。
   加 `stored_switches_error`：空表与读不到从此在线上是两个不同的响应，页面也分开渲染。
   已加测试 `TestHandle_AnUnreadableTableSaysSoRatherThanLookingUnset`。

4. **typed-nil 会把 503 变成 500（装配）**。`platformsettings.NewStore(nil)` 返回 nil `*Store`，
   直接塞进接口字段就不是 nil 接口了，没有数据库的部署会报告「开关可写」、写的时候才 500。
   `main.go` 里显式判空后再赋值，并在注释里写清楚原因。

另外顺手修了评审列的两条低危：平台页把索引方式的值与其来源行改成**一次读取**
（原本两次 `Load`，中间落一次保存就会自相矛盾），以及 `RuntimeSwitches` 那段
「这些值只有 router 重启才会变」的过时注释。

### 验收之外、被 B4 当场照出来的东西

知识库索引清单一上线就发现**三条演练线的库索引是 `economy`、检索是 `semantic_search`**——
上传正常、索引正常、检索恒空且不报错。这是存量数据的状态不是代码缺陷，已登记 D24。
清单原本把这种库渲染得和健康库一样平静（冲突判断只比对「和所选值一致否」，
不看库自身是否自洽），已补：自相矛盾的库标红并写明现在就该怎么修。

### 回滚

- 数据库：`~/unica-run/unica-pre022.dump`（施加 022 之前的 `pg_dump`）
- router：`~/unica-run/router-scene-bin.bak-20260903-065224`
- admin：`~/unica-run/admin-scene-bin.bak-20260903-073133`
- 门户是仓库目录的 bind mount，`git checkout` 即回滚

### 环境已还原

`intent_triage=shadow`、`scene_mode=on`、`dify_indexing_technique=high_quality`——
与本组开工前的运行值完全一致。`source` 如实记着 console（确实有人从控制台写过），
审计四行都在。

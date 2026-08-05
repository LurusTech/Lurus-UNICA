# Active Task: 领域本体（第 1 期）— 确定性事实注入 + 断言校验

## Context
UNICA 是面向多客户交付的产品，不同客户对事实严格度的要求不同（数码/生鲜/百货差异极大），
因此本体作为**可按产品线开关的能力**必须具备，而不是等验证结果再决定是否建设。
本期交付完整能力 + 两个独立开关，默认全关，对现有部署零行为变更。

## 设计决策
- **两个独立开关，不是一个 on/off**：`inject_facts`（把确定性事实注入 AI 上下文）与
  `validation`（off/shadow/enforce，对模型断言做本体校验）互不依赖。客户可以只要前者。
- **DB 是真相源，YAML 是导入格式**：多租户要求运行时可改本体，不能依赖重新部署。
  `ontology_versions` 表按产品线存生效版本；`deploy/config/ontology/*.yaml` 只是种子/导入格式。
- **提示词不含具体数字**：Dify 提示词只引用 `{{facts_context}}`，改本体不碰控制台——
  同时绕开 `dify_admin.go` UpdateAppConfig 的 400 遗留 bug。
- **闭世界假设**：本体未声明的属性 = 该产品线不存在该政策，并在 facts_context 里**显式否定**。
  这是刻意背离 OWL 开放世界语义，客服场景沉默等于放任幻觉。
- **断言协议用标签不用 JSON**：扩展现有 `[INTENT:xxx]` 约定为 `[FACT:k=v]`。提示词只加一行，
  模型不吐标签时 claims 为空、校验跳过、退回当前行为——与 acest 集成的 fail-open 哲学一致。
- **不做推理机**：层级两层、断言数十条，一致性用 `go test` 遍历即可。约束用于校验，不用于推理。

## Critical Files
- unica/router/migrations/011_domain_ontology.sql（新建：ontology_versions / claim_violations）
- unica/router/internal/domain/ontology.go（新建：TBox/ABox 类型 + 约束原语）
- unica/router/internal/domain/loader.go（新建：DB 加载 + 缓存，仿 RouteCache）
- unica/router/internal/domain/yaml.go（新建：YAML 导入/编译为 compiled JSONB）
- unica/router/internal/domain/render.go（新建：facts_context 渲染，含显式否定清单）
- unica/router/internal/domain/validator.go（新建：断言校验，五类约束）
- unica/router/internal/domain/claims.go（新建：`[FACT:k=v]` 解析与剥离）
- unica/router/internal/domain/config.go（新建：per-product-line 开关，读 config_json.ontology）
- unica/router/internal/routing/router.go（注入 facts_context；校验接入护栏与置信度）
- unica/router/internal/metrics/metrics.go（facts 注入率 / 违规计数 / 校验决策）
- deploy/config/ontology/{megastore,freshmart,techzone}.yaml（新建：种子本体）
- unica/router/cmd/ontology/main.go（新建：YAML → DB 导入/校验 CLI）

## Step-by-Step Plan
- [x] 1. 本体模型与 YAML 格式完成（`ontology.go` + `yaml.go`）。约束原语落地六类：
      domain / range / functional / disjoint / min_cardinality，外加 **requires**（伴随事实）——
      OWL 没有对应词汇，因为它约束的是"回答必须说什么"而非"世界是什么"。
      另加 `range.labels`：标识符保持机器可比，渲染与校验都接受中文措辞。
- [x] 1b. **作用域泛化（多行业适配）**：断言不再只按"类"分叉，而是按任意维度组合。
      `class` 降为内置维度（唯一带层级继承的那个），其余维度在 `dimensions` 下声明。
      理由：留学的服务阶段、财税的业务类型、房产的购房资格，都是"事实按某轴分叉"，
      而单一类层级已被产品类别占用。歧义检查随之泛化——"退费比例80%"不说阶段、
      "增值税率3%"不说纳税人类型，与"保修12个月"不分手机配件是同一条规则。
      值类型补齐 date / decimal / percent / money；标签语法 `[FACT:签约前.退费比例=80]`
      与 `[FACT:Phone.warranty_months=12]` 同形，正则放开中文。
- [x] 2. 三条产品线种子本体完成 + 自动漂移守卫：凡"只有唯一合法取值且非纯数字/非英文标识符"
      的事实，必须在该产品线的 canned response 中原文出现，否则测试失败。
      写守卫时即抓到一处真实漂移（TechZone 服务时间应为「周一至周日」）。
- [x] 2b. **非零售样例**（`deploy/config/ontology/examples/`）：留学中介、财务代理。
      不是文档，是**测试夹具**——通用性这种主张必须跑出来而不是嘴上说。
      留学中介压出了作用域泛化的需求（退费比例按服务阶段分三档）；
      财务代理压出了双轴交叉（纳税人类型 × 业务类型 → 3%/13%/6%/9%）。
- [x] 3. migration 011（`ontology_versions` 带 active 唯一索引 + `claim_violations`）
      + `store.go`（进程内缓存，重载失败时降级供旧版本而不是丢事实；发布/回滚走事务）
      + `cmd/ontology`（validate / preview / publish / versions / rollback）。
      `validate` 与 `preview` 刻意不依赖数据库，客户可离线自查。
- [x] 4. `render.go` 完成：facts_context = 事实清单 + 显式否定清单，带优先级声明头。
      子类分档（手机/配件保修）分别渲染，父类共有事实只渲染一次；输出确定性有测试钉住。
- [x] 5. `claims.go` + `validator.go` 完成。八类 Violation，其中 `denied_capability` 走答案原文，
      是模型完全不吐 `[FACT:]` 标签时唯一还起作用的网。
      句级否定判定从 `internal/eval` 上移到 `domain.IsAffirmed`，两处共用一份实现，杜绝漂移。
- [x] 6. `config.go` 两个独立开关（`inject_facts` / `validation`）+ router 全链路接入：
      注入 `facts_context`；`[FACT:]` 标签**无条件剥离**（标签漏给客户本身就是缺陷）；
      shadow 只记指标与 `claim_violations`，enforce 才覆盖护栏判定为 `claim_conflict` 并
      把违规说明带进 handoff 事件给坐席。违规入库放在回复发出**之后**，慢查询不拖延客户。
      部署级总开关 `ONTOLOGY_ENABLED`（默认 true，逐产品线仍需 opt-in）。
- [x] 7. 验证：`go build ./... && go vet ./... && go test ./...` 全绿。
      `TestDefaultConfigIsInert` 钉死"默认配置零行为变更"；
      非零售样例（留学/财税）作为通用性测试夹具随每次 `go test` 运行。
      待 Dify 就绪后补：evalset 注入前后对照。
- [x] 8. 客户文档 `doc/ontology-schema.md`：装什么不装什么的判据、作用域两根轴、
      值类型、约束一览、两个开关的上线路径、提示词改法、常见错误。

## Out of Scope
- OWL 文件 / RDF 三元组 / 推理机 / 传递闭包 / 本体自洽性推导
- 从本体反向生成 canned_responses.yaml 与知识库文档（降级为一致性测试）
- 置信度算法的完整重构（本期只在校验失败时压低，不改检索信号部分）
- 门户上的本体编辑界面（本期用 YAML + CLI 导入，UI 后续增量）

## 上期遗留（第 0 期）
- [x] Dify 已就绪（本地 WSL），意图分诊在实测中生效：8 条事务/情绪型全部在调模型前拦下
- [x] "事实进上下文"的疗效已直接量化，无需再做手写提示词的对照实验

### 校验半边的实测数据（evalset 补齐后）
`evalset` 原来只注入不校验，等于本体只验证了一半。补上后 `Grounding` 报告断言发射率、
违规分类，以及**黄金集与校验器的分歧**——两个独立裁判判同一批答案，分歧处是判断
"能不能开 enforce"的唯一证据。

| | 注入开 | 注入关 |
|---|---|---|
| 受检答案 | 52 | 52 |
| 带标签的答案 | **46（88%）** | **1（2%）** |
| 断言总数 | 101 | 1 |
| 违规 | **0** | 4（3 denied + 1 undeclared） |
| flagged-but-passed（误杀） | **0** | 0 |
| failed-but-clean | 0 | 0（但 52 条全被 handoff，内容没打过分） |

**结论一（正面）**：52 条真实正确答案上零误报。这比原来那 5 条手挑单测强得多，
是目前唯一的 enforce 安全性证据。

**结论二（负面，已修）**：**两个开关名义独立，实际上 validation 依赖 inject_facts。**
断言标签的词表随事实块一起注入，不注入就没有可抄的标签，发射率从 88% 掉到 2%，
精确检查基本空转，只剩文本级 `denies` 扫描。原文档建议"只开 validation 先观察错多少"
是错的——客户会看到一片"无违规"，那不是没问题，是没在查。
→ `LoadConfig` 加了这个组合的警告日志，`ontology-schema.md` 改了建议。

**仍未证明**：校验器的召回率。注入关闭时答案确实大量出错，但它们全被
`low_confidence` 拦在 handoff，内容从未被打分，无法构成召回样本。
报告里新增 `not-comparable` 计数，避免那个 0 被误读成"两个裁判完全一致"。

### 方差测量（同配置连跑 9 次）
单次运行的数字不可信——模型有非确定性。9 次分三批：

| 批次 | 通过率 | 标签率 | 误报 | 发现 |
|---|---|---|---|---|
| 1-3（修复前） | 60/60 ×3 | 88-92% | **1,1,0** | `missing_companion` 误杀正确答案，2/3 次发生 |
| 4-6 | 60/60, **59/60**, 60/60 | 90-92% | 0,0,0 | 误报消除；暴露一条黄金集自身的错误断言 |
| 7-9（全修后） | 60/60 ×3 | 88% ×3 | 0,0,0 | 稳定 |

**发现一：`requires` 查错了对象。** 答案「支持15天无理由退货，但需确保商品未拆封」
把两个事实都说清楚了，但模型只给了一个标签。`checkCompanions` 查的是"有没有声明"，
而这条约束的本意是"客户有没有看到"——客户读的是散文不是标签。
enforce 模式下这会压掉完美答案，三次里发生两次。
→ 改为标签或原文任一满足即可；permissive 方向是安全方向（漏查一次伴随事实，
远好过压掉一条正确答案）。加了正反两条回归测试。

**发现二：黄金集自己有错误断言。** run 5 唯一那条失败是 `tech-04`，
答案说「拆封后的产品**不支持**无理由退货」——完全正确，却撞上 `must_not_contain: ["无理由"]`。
这正是我给 `must_deny` 写过、却没给 `must_not_contain` 上的否定语境坑。
→ `tech-04` 与同形的 `tech-03` 改用 `must_deny`（句级否定感知）。
→ `failed-but-clean` 的文档也改了：它有三种读法，其中一种是**黄金集错了而校验器是对的**，
不能只数数量。

**结论**：修完后 9 次里最后 3 次全稳定。但诚实的说法是
「同配置下 100% 可复现，前提是断言本身没写错」——而前 6 次证明断言写错很容易。

### 追加修复（本次会话末，均已测试）
- **经验召回同样落进置信度陷阱**：`experience_context` 也是变量注入、同样不产生
  `retriever_resources`。只用 acest 经验库不用本体的产品线，召回再准也会全量转人工。
  → 新增 `confidenceExperience` 档（0.72），并用 `TestConfidenceTiersAreSeparable`
  钉死三档可被阈值分离。
- **模型不可用时客户静默**：Dify 报错或返回空回复时，原代码只记日志与计数就 `return`，
  既不回复也不转人工。护栏管的是"这条答案不该发"，管不了"根本没有答案"。
  → 新增 `handoffWithoutAnswer`，两条失败路径都走 `ai_unavailable` 转人工并发兜底话术。
  故障期间全部会话进人工队列——队列会忙，但这是客服系统的正确行为。

## 部署中发现的交付缺陷（不属本期范围，待单独增量修复）
- **迁移 001 最后一条语句必然失败**：`CREATE UNIQUE INDEX idx_messages_platform_msg`
  建在分区表上却不含分区键 `created_at`，PostgreSQL 拒绝。
  后果：**入站消息去重的唯一索引在任何环境都不存在**，gateway 的去重没有数据库兜底。
- **messages 分区只覆盖 2026-03 与 2026-04**：当前月份写入直接
  `no partition of relation "messages" found for row`，router 一收到真实消息就会失败。
- **`dify_admin.go` UpdateAppConfig 的正确端点已确认**：应为
  `POST /console/api/apps/{id}/model-config` 且必须提交完整 model_config 对象，
  `PUT /apps/{id}` 的部分更新不被接受。可照 `deploy/dify-preview/configure_apps.py` 修。

## 实测结果（2026-08-05，本地 WSL + Dify 0.15.3 + DeepSeek）
黄金集 60 条，同一部署跑 A/B：

| | 不注入事实 | 注入事实 |
|---|---|---|
| MegaStore | 4/20 | **20/20** |
| FreshMart | 2/20 | **20/20** |
| TechZone | 2/20 | **20/20** |
| 合计 | 8/60 (13.3%) | **60/60 (100%)** |

对照组只有意图分诊那 8 条通过——它们根本不经模型。所有内容题全挂，因为应用无知识库、无事实。

### 转人工的判定边界（回答"动态还是写死"）
全部是**写死 + 手工配置**，没有任何一处会根据结果自适应：

| | 位置 | 改动成本 |
|---|---|---|
| 写死 | 意图分诊全部词表、判定顺序、置信度四档（0.90/0.75/0.72/0.3） | 改代码发版 |
| 每产品线配置 | `confidence_threshold`、`blocked_topics`、`ontology.*` | 改 `config_json` |
| 部署级 | `INTENT_TRIAGE`、`ONTOLOGY_ENABLED` | 改环境变量 |
| 数据驱动 | 本体内容、Dify 知识库文档 | 改数据 |

置信度四档刻意拉开间距，让客户**只调一个阈值**就能表达风险偏好：

```
0.70（默认）  接受检索命中 / 经验召回 / 确定性事实
0.75          必须有确定性事实，经验召回不算数
0.80          必须有事实 + 校验通过的断言
```

**灌知识库只能降低 `low_confidence` 这一类转人工**（八条路径里的一条），
且它抬高的是检索相似度这个代理指标，不保证答案变对——可能把"低置信度转人工"
（安全）换成"高置信度发错答案"（危险）。真正安全的降法是扩本体覆盖面，
以及给事务型问题接订单查询工具。

### 实测暴露的三个问题（已全部修复）
1. **标签是编的不是抄的**：facts_context 只渲染中文标签，模型只好自造属性名，
   产出 `[FACT:退货.无理由窗口=15天]` 这种校验器认不出的标签。
   → `Render` 增加标签词表块，指令从"生成标识符"变成"复制其中一条"。
   新增 `TestRenderTags_MatchAssertions` 钉死"本体必须能验证它自己发出的标签"。
2. **置信度与本体正面冲突（严重）**：注入的事实不产生 `retriever_resources`，
   而 `CalculateConfidence` 只看它，于是全部落到 0.3 默认值、低于 0.7 阈值。
   **本体越是让模型答对，护栏越是把答案拦下来**——首轮注入实测 60 条全被 handoff。
   → 新增 `routing/grounding.go`：置信度改由检索信号与本体接地信号取高，
   校验失败直接归零。原计划第 2 期的事情，实测证明是本体能工作的前提。
3. **两处本体自身的缺陷**（黄金集正是为此存在）：
   FreshMart 加急时效存成 `120 分钟`，模型忠实照抄，而话术是「2小时」→ 改存 `2 小时`；
   MegaStore 没声明退货流程，模型答不出「联系客服」→ 补 `return_process_requirement`。

## Current Status
- [ ] Ready for Review — 全部步骤完成 + 真实 Dify 实测通过，
      `go build ./... && go vet ./... && go test ./...` 全绿。
      默认配置下与本期之前行为完全一致（测试钉住），可直接上线。

### 上线路径（每条产品线独立决定）
1. 写本体 → `go run ./cmd/ontology validate -file X.yaml` 自查 → `preview` 看注入内容
2. `publish` 入库
3. `config_json.ontology` 开 `inject_facts: true` + `validation: shadow`，跑一到两周
4. 看 `claim_violations` 表与 `router_claim_violations_total{kind,mode}`，人工抽样确认判定准确
5. 转 `validation: enforce`

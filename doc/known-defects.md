# 已确认缺陷登记

代码走查确认**已经坏掉**的部分，与 `doc/unverified.md` 区别开：
那份记的是"交付了但没有证据支持"，这份记的是"有证据表明它不工作"。

每条写清楚：坏在哪（带 file:line）、后果是什么、修的方向。修掉一条就从这里删掉。

最后更新：2026-08-10（场景化策略方案调研期间发现）

---

## D1 满意度调查从未真正发出

`OnClose` 回调只在 `state.Manager.TransitionState(..., StateClosed)` 内部触发
（`router/internal/state/manager.go:176-184`），但唯一真正关闭会话的
`closeIdleConversations` 直接调 `repo.UpdateConversationState`
（`state/manager.go:341`），**绕过了 `TransitionState`，也就绕过了回调**。

连带三处：
- `survey_status='no_response'` 全仓库无写入点，只在迁移注释里出现（`005_survey.sql:6`）。`sent` 状态会永久卡住。
- `SurveyConfig.TimeoutHours` 被解析（`survey/handler.go:46,256`）但从不使用，TTL 是写死常量（`handler.go:25`），配置项是摆设。
- 指标 `SurveyTimeoutTotal`（`metrics/metrics.go:209-211`）定义后从未 `Inc()`。

**后果**：`satisfaction_score` 是纸面字段。任何依赖"客户满不满意"的功能
（经验蒸馏筛选、坐席绩效、AI 质量度量）目前都没有数据源。
reporter 的 `avg_satisfaction` 恒为 0。

**修的方向**：`closeIdleConversations` 改走 `TransitionState`，
或在它内部显式触发 `OnClose`；补 `no_response` 的翻转任务；
`TimeoutHours` 要么接上要么删掉。

---

## D2 坐席在 Chatwoot 点"解决"不会关闭 UNICA 会话

`gateway/internal/webhook/chatwoot.go:247-302` 收到 `status=resolved` 后构造
`type: "event.conversation_closed"` 事件 XADD 进 **`unica:outbound`**——
和客户消息共用一条流。但该事件的 `Data.ChannelID` 从未填充，
而这条流唯一的消费者（`gateway/internal/stream/consumer.go` → `adapter/registry.go:56-73`）
的逻辑是"按 ChannelID 查渠道适配器并发送"。

ChannelID 为空 → `no adapter registered for channel` → 重试三次
（`dedup/retry.go:49-77`）→ 进死信队列 `unica:dead-letter`。

全仓库没有任何消费者把 `event.conversation_closed` 转成状态转移。

**后果**：会话只能靠空闲超时（默认 30 分钟）关闭。坐席的显式"已解决"信号
完全丢失，且每次都往死信队列里灌一条。

**修的方向**：该事件不该走出站流，应走 handoff 流或独立通道，
由 router 消费并调用 `TransitionState(..., StateClosed)`。修好后 D1 也一并有了触发源。

---

## D3 坐席回复文本不落 `messages` 表

`CreateMessage`（`state/repository.go:198`）只有两个调用点：
`manager.go:123`（客户入站，`sender_type='customer'`）和
`router.go:929`（AI 出站，`sender_type='ai'`）。
全仓库没有第三处，也没有任何 `sender_type='human'` 的写入路径——
尽管 schema 里声明了这个枚举值（`001_core_schema.sql:43`）。

坐席回复在 `gateway/internal/webhook/chatwoot.go:150-245` 被转发到出站流，
只留下一个时间戳：`first_agent_reply_at`（`chatwoot.go:234-241` 的裸 SQL）。

**后果**：凡是转过人工的会话，`messages` 表里没有坐席说过什么。
任何"从人工话术里蒸馏经验"的方案都被这一条卡死——
只能拿到客户消息、AI 草稿和摘要，拿不到真人实际怎么处理的。

**修的方向**：webhook 里补一次 `CreateMessage`，`direction='outbound'`、
`sender_type='human'`、`platform_msg_id='cw-<id>'`（现成的去重键）。

---

## D5 `[INTENT:]` 标签从未在任何提示词里被要求过

`marketing.DetectIntents`（`marketing/detector.go:34`）解析答复里的
`[INTENT:xxx]` 标签，`judge.go:94` 调用它，`router.go:654` 把结果
merge 进会话 metadata，`metrics.MarketingIntentDetectedTotal` 计数。
整条链路完整且有测试。

但 `DefaultSystemPrompt` 的五条规则里只有 `[FACT:]`（规则 5），
**从来没有一条要求模型输出 `[INTENT:]`**。git 全历史确认该指令在任何版本的
提示词或 `configure_apps.py` 里都不曾存在。

**后果**：营销意图追踪链路一直空转，`metadata.intents` 恒为空，
相关指标恒为 0。

**修的方向**：提示词加一条同构指令即可复活。但这会改变模型输出格式，
需独立验证对 claim 校验器（`pkg/domain/claims.go`）的影响——
`claims.go:20` 的注释说 `[FACT:]` 的约定"deliberately mirrors the existing
[INTENT:xxx] protocol"，而那个 protocol 其实一直没被启用过。

---

## D6 死代码三处

| 位置 | 说明 |
|---|---|
| `state/repository.go:444-455` `SetFirstAgentReply` | 从未被调用；gateway 用裸 SQL 实现了同样的事（`chatwoot.go:234-241`） |
| `handoff/handler.go:306-334` `sendMessageHistory` | 被 `history_sync.go` 的 `SyncConversationHistory` 取代，只在自身单测里出现 |
| `ai_agent_configs.max_ai_turns` | 迁移里定义，无任何读取方（见 D4） |

---

## D7 两套并行的 Dify provisioning 实现

| | `admin/internal/bridge/dify.go` | `router/internal/bridge/dify_admin.go` + `cmd/setup_dify_workspaces` |
|---|---|---|
| 用途 | 生产路径（一键开户） | 运维 CLI |
| 拓扑 | 默认工作区内，一线一 app + 一 dataset | 一线一 workspace |
| "已配置"判据 | `dify_agent_id` 非空 | `config_json.dify_workspace_id` 非空 |

两者判据不同，混用会对同一产品线产生两套互不一致的绑定。

**已定案（2026-08-13）**：`admin/internal/bridge/dify.go` 是**唯一的
provisioning 实现**——开户走 `POST /api/v1/tenants`，拓扑固定为"默认工作区内
一线一 app + 一 dataset"，"已配置"判据只认 `dify_agent_id`。
`router/cmd/setup_dify_workspaces` 降级为**一次性运维脚本**：它只用于给早于
本次重构、按 workspace 划分的存量环境做迁移引导，不参与任何生产路径，
也不再被视为一种可选拓扑。新环境不要跑它。

**剩余动作**：该脚本与 `router/internal/bridge/dify_admin.go` 的注释需写明
上述定位（避免下一个人把它当成"另一种开户方式"）；存量环境按 D8 的表回填后
即可停用。做完这两件即可删除本条。

---

## D8 存量产品线的知识库仍未接线、仍是关键词索引

原 D8「上传的知识库对回答无任何效果」已修复并验证（2026-08-12，见
`doc/test-ajyj-kb-import.md`），代码侧三处：开户时把 dataset 挂到 app
（`admin/internal/handler/product_lines.go` + `pkg/difyapp/datasetbinding.go`）、
建库时让检索方式与索引方式自洽（`pkg/difyapp/retrieval.go`）、
分段规则不再把记录与其标题切开（`ai_config.go` 的 `defaultProcessRule`）。

**但这些只对新开户生效。** 存量产品线（FreshMart / MegaStore / TechZone /
DrillCo…）三项全部还是旧状态：

| 项 | 存量现状 | 回填手段 |
|---|---|---|
| dataset 未挂到 app | `dataset_configs.datasets.datasets` 为空数组 | `POST /api/v1/tenants/{id}/ai-settings/dataset/bind`（已实现，幂等；`{id}` 可写 `me`，admin 可对任意租户执行） |
| 检索方式与索引不自洽 | 建库时默认 `semantic_search`，但文档按 economy 建的关键词索引 | 控制台 PATCH 数据集，见 `deploy/embeddings/README.md`「已有知识库的迁移」 |
| 文档按旧规则分段 | 每段约 250 字、单换行切分，标题与数据分离 | 删除并重新上传该库全部文档 |

**为什么必须回填**：这三项任何一项没做，客户在门户上传的资料就仍然
不参与回答，或参与了但答错。DrillCo3 是现成的反例——它此前"演练成功"
的观感来自模型的通用推理，不是知识库。

---

## D9 没有嵌入模型时，知识库静默降级为一个查不准的关键词库

`DIFY_INDEXING_TECHNIQUE=economy` 是工作区没有嵌入模型时的唯一选择
（`high_quality` 会被 Dify 直接拒绝）。但 economy **不是** high_quality 的
廉价等价物：它按段抽取有限个关键词建倒排，没被抽中的词无论出现多少次
都检索不到。中文里这包括绝大多数专有名词和复合词——它们不是单个 token。

2026-08-12 实测（房产知识库，16 篇文档，economy）：

```
查询「青溪墅园」→ 零命中   （该词在目标文档中出现 3 次）
查询「物业费」  → 零命中   （5 篇房源文档都含此词）
```

**后果**：上传成功、`indexing_status` 全部 completed、知识库各项体检正常，
但大量真实问题得到"我这边没有这个信息"，或者拿到**邻近记录的数据**
（把 A 楼盘的物业费当成 B 楼盘的答出来）。这与 D8 是同一种失败形态：
一个看起来健康、实际在产出错误答案的系统，没有任何一处报错。

**已做的缓解**：admin 启动时若为 economy 会打 WARN
（`ai_config.go:NewAIConfigHandler`），说明这是一个关于回答质量的决定，
不再只存在于设环境变量那个人的记忆里。

**根治**：配一个文本嵌入模型并切 `high_quality`。
`deploy/embeddings/` 提供了一个自带的本地方案（BGE 中文模型 +
OpenAI 兼容端点，无需外部厂商与额外 key），实测切换后上述零命中查询
全部命中且目标文档排第一。**当前只在演练环境手工起着，未纳入编排**——
生产部署需要把它放进 compose/k8s 并加鉴权，见该目录 README 的
「生产部署注意」。在那之前，任何新部署只要没人记得起这个服务，
就会重新落回 economy。

---

## D10 追问丢主语，检索返回别的记录，模型照单全收（长对话最严重）

2026-08-13 21 轮长对话实测（`doc/test-ajyj-kb-import.md` §七）。客户先锁定
「华亭书苑」聊了几轮，然后问：

> 物业费多少？车位怎么解决？

这句**不含主语**——真人对话里指代是常态。检索是无状态的，只看到这一句：

| 检索词 | 第 1 名 |
|---|---|
| `物业费多少？车位怎么解决？`（客户实际说的） | 苏河湾一号，**华亭书苑前 4 名都进不去** |
| `华亭书苑物业费多少？车位怎么解决？`（补上主语） | 华亭书苑 0.657 ✅ |

于是模型拿到的是**另一套房源**的段落。而它的对话上下文里明明白白写着
正在聊华亭书苑，它就把检索来的数据安到了华亭书苑头上，答出
「物业费3.6元/㎡/月、产权车位约60万元/个」——前者知识库里不存在（华亭书苑
是 1.5 元），后者是苏河湾一号的车位价。**置信度 0.9。**

**为什么这条最重要**：它与领域无关，与知识库质量无关，只要是多轮对话就会
发生。"那个多少钱""它保修多久""这款支持吗"——中文口语几乎不重复主语。
单轮测试永远测不出来，因为每个问题都自带主语。

**修的方向**（三选一，都是有代价的取舍，需决策）：
1. **检索前改写查询**：用会话历史把追问补成独立问题再检索。业界标准做法，
   效果最直接；代价是每轮多一次 LLM 调用（延迟+成本），或用小模型/规则降本。
2. **把已确立的主语作为检索约束**：会话状态里记住当前讨论对象，
   检索时拼进查询或作为元数据过滤。比 1 便宜，但要维护"当前对象"的状态机，
   且客户中途换对象时要能跟上。
3. **提示词兜底**：要求模型核对检索内容的主语是否与对话对象一致，不一致
   就不用。零成本但不可靠——正确数据根本没被检索到，模型最多只能改成
   "我不确定"，答不出正确答案。

单靠 3 不够。建议 1 或 2，1 更稳，2 更省。

---

## D11 知识库来源的内容没有任何事实校验

`pkg/domain` 的 claim 校验只覆盖 **ontology 属性**：解析回答里的
`[FACT:属性=值]` 标签，比对断言、range、伴生要求。知识库（Dify dataset）
来的内容**完全在校验范围之外**——模型不为它打标签，校验器也无从比对。

D10 那次对话里模型编造了物业费、错配了车位价、还凭常识虚构了一整张税费表
（见 D12），`claim_violations` 表记录数为 **0**。

**后果**：房源明细、商品参数、订单条款——凡是放在知识库而非 ontology 里的
内容，一旦被错误检索或被模型润色，没有任何一层会发现。而这恰恰是
最容易被 D10 污染的那部分内容。

**修的方向**：短期把最关键、最不能错的事实上移到 ontology（本次测试已按
此分工：政策进 ontology、房源明细留知识库，政策类回答全部正确）。
长期需要一种针对检索内容的校验——例如要求答案中的数值必须在检索片段里
逐字出现，否则标记。后者是新机制，需单独设计。

---

## D12 知识库明确说"要咨询"的事，模型会自己编出一张表

同一次长对话，客户问「税费大概多少？」。知识库对该房源只写了
「非满五唯一，涉及增值税及附加，具体税费需按最新政策计算，建议客户在
签约前咨询公司法务或税务部门」，ontology 里没有任何税率。模型输出了：

- 一张含契税 1.5%、个税 1%、名下有房 3% 的费率表——**这些税率全库皆无**
- 「增值税及附加：**免征**，房本满5年可免征」——**与知识库的"非满五唯一"直接矛盾**
- 「合计预估约13万元起」

置信度 0.9。这违反提示词规则 2（不得用行业常识替客户补答案）与规则 4
（没有的具体数值不要编造）。同一次对话第 20 轮它又正确说出"非满五唯一"，
即**同一会话内自相矛盾**。

**一个必须承认的关联**：2026-08-12 为治"该答不答"新增的规则 7
（"能答就直接答"）与规则 4 存在张力。规则 7 的措辞已限定"在以上约束内
能回答的问题"，但本例说明这个限定不足以压住模型在**部分检索到相关内容**
（它检索到了别的房源的税费说明）时的补全冲动。是否由规则 7 引入无法在
无 A/B 的情况下断定，但它必须列为规则 7 的已知风险并在下次改提示词时验证。

**修的方向**：给"应当拒答"的情形一条与规则 7 同等强度的正向指令——
例如"知识来源本身写明需要咨询/以核定为准的事项，只复述该指引，
不得给出任何自行推算的数值或税率"。改完需重跑本例验证。

---

## 记录规则

- 修掉并验证 → 从本文件删除，结论写进对应模块的注释。
- 判断为"不修，接受现状" → 移到 `doc/unverified.md` 并写清接受的理由。
- 新发现的确认缺陷，即使当期不修，也先登记再合并。

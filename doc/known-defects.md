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

## D4 后台 AI 配置对线上路由无任何效果（优先级最高）

存在两套互不相通的存储：

| | portal 能编辑的 | router 实际读的 |
|---|---|---|
| 位置 | `ai_agent_configs` 表（`008_ai_agent_configs.sql`） | `product_lines.config_json -> "guardrail"` |
| 写入方 | `PUT /api/v1/ai-config/:id/{threshold,handoff-rules}` | **无任何 handler 写这个键** |
| 读取方 | **router 从不读这张表** | `guardrail.LoadGuardrailConfig`，`routing/router.go:520,815` |

`SetConfigKey` 的调用点只有两处：`customers.go` 写 `"chatwoot"`、
`ontology.go:326` 写 `"ontology"`。没有人写 `"guardrail"`。

失效通知也是断的：`invalidateConfigCache`（`ai_config.go:753-766`）
publish 到 `unica:config_invalidation`，但订阅方只有
`gateway/internal/channelcfg/channelcfg.go:140-168`，且它只处理
`type=="channel_config"`，会直接忽略 `type=="ai_config"`。

**后果**：客户在门户「AI 参数」里调置信度阈值、转人工关键词、屏蔽话题，
保存成功、界面回显正常，**但线上行为一点不变**。所有产品线实际都跑
`DefaultGuardrailConfig()` 的硬编码值（阈值 0.70，固定关键词列表，空屏蔽话题）。
这是客户能直接感知的功能性欺骗。

对照组：ontology 那条线（`inject_facts` / `validation` / 断路器参数）
是打通的（`ontology.go:281-340` → `config_json.ontology` → router 读），
说明正确的做法在仓库里已有先例。

**修的方向**：二选一——让 AI 配置也写 `config_json.guardrail`（与 ontology 同构，
改动小、与现有读取方天然兼容），或让 router 改读 `ai_agent_configs` 表并接上失效通知。
推荐前者。另：`ai_agent_configs.max_ai_turns` 无任何读取方，一并处置。

2026-08-11 演练实证：手写 SQL 往 `config_json.guardrail` 塞 `confidence_threshold`
并清掉 `channel_route:*` 缓存后，router 立即按新阈值放行——**读取侧完全健康，
缺的只是写入路径**。修法就是给 `updateThreshold`/`updateHandoffRules` 加一行
`SetConfigKey(..., "guardrail", ...)` 并失效 route cache。

（同日已顺手修掉一条相邻缺陷：AI-config 的控制台调用全部依赖没人配置的静态
`DIFY_ADMIN_TOKEN`，portal 的「AI 参数」弹窗读/写提示词在真实部署里从来就是坏的。
bridge 现在会用邮箱密码按需铸造 token，见 3a913a7。）

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

**修的方向**：确认哪一套是长期形态，另一套降级为纯引导脚本或删除。

---

## 记录规则

- 修掉并验证 → 从本文件删除，结论写进对应模块的注释。
- 判断为"不修，接受现状" → 移到 `doc/unverified.md` 并写清接受的理由。
- 新发现的确认缺陷，即使当期不修，也先登记再合并。

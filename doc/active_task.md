# Active Task: AI 设置页收拢 S1–S7

## Context
`doc/plan-ai-settings-consolidation.md` 的前三步。三步全是**接线**：让配置真的可读、真的能校验、
真的立即生效，之后（S5 起）才往界面上放。分开做是这次的纪律——先证明它生效，再暴露它。
本增量**不改任何门户页面**，也不新增任何租户可写的设置。

## 为什么是这三步先
- **S1**：admin 的 Config 里**一个 router 的 env 都没有**。任何"展示平台总闸档位"的功能，
  照直觉去读 admin 自己的环境变量，会自信地显示相反的值。
- **S2**：检索设置的修复入口已经存在，但写入前不校验索引技术是否匹配——把 economy 索引的库
  改成语义检索，检索会恒空，而现有的回读校验只比对 `search_method`，发现不了。
- **S3**：ontology 的配置写入**不失效路由缓存**，关掉 enforce 最长 5 分钟不生效；
  更糟的是顺手保存一次 guardrail 就会冲掉缓存，导致该缺陷**间歇性消失**——
  在 S3 之前，任何现场复现都不可信。

## Critical Files
- `unica/router/cmd/router/main.go`（新增 /configz）
- `unica/admin/internal/config/config.go`（router 内网地址）
- `unica/admin/internal/bridge/dify.go`（数据集读写补全）
- `unica/admin/internal/routecache/`（新建：缓存失效的唯一实现）
- `unica/admin/internal/tenant/aisettings/aisettings.go`（改用共享实现，回报失效结果）
- `unica/admin/internal/tenant/facts/facts.go`（接上缓存失效）

## Step-by-Step Plan
- [x] 1. router 新增 `GET /configz`：返回运行时**实际生效**的行为开关
      （triage / scene / ontology 开关与缓存 TTL / route cache TTL / idle timeout / ACEST 是否启用）。
      **只出开关，不出任何凭据与连接串**——这个端点和 /healthz、/metrics 一样无鉴权。
- [x] 2. admin 侧新增 router 内网地址配置与取数客户端（短缓存），
      并暴露 `GET /api/v1/platform/runtime`（仅 admin）。
      完成标志：admin 读到的值与 router 容器里的 env 一致；改 env 重启 router 后 admin 读到新值。
- [x] 3. bridge 抽出"读数据集当前 retrieval_model + indexing_technique"的方法。
- [x] 4. `SetDatasetRetrieval` 写入前比对索引技术：不匹配则**拒写**并报"请先重建索引"，
      而不是写进去让检索恒空。
- [x] 5. 回读校验从只校 `search_method` 扩到 `top_k`（以及可得的 rerank 开关）。
      现在这条回读是唯一挡住"200 但没生效"的东西，覆盖面太窄。
- [x] 6. `want` 改成「平台默认 + 租户覆盖合并」的构造形态（当前租户覆盖恒为空）。
      现在就定好形状，否则 S11 加 top_k 时两个写入口会在同一张卡上互相静默回滚。
- [x] 7. 新建 `internal/routecache`，把失效逻辑抽成唯一实现；aisettings 改用它。
- [x] 8. facts 的 `putConfig` 接上失效（需改构造函数与 main 接线）。
- [x] 9. 失效失败不再静默 return：记 WARN，并在响应里返回 `cache_invalidated` 布尔。
      门户当前无条件写着"立即生效"，这个字段是后续让它说实话的依据。
- [x] 10. 验证：五模块 build+vet+test；`/configz` 与实机 env 逐项对拍；
      构造一次索引技术不匹配，确认**拒写**而非打印成功；
      改 ontology 配置后秒级观测到 router 行为变化。

## Current Status
- [x] S1–S7 完成（2026-08-24）

### S7 —— 调研文案：接线时发现整个功能从未真正跑通过

计划里 S7 是"两条客户能看到的文案还是 Go 常量，先接线再暴露"。接线做完去实机验收
——**发不出去**。查下来是两处独立的断点，两处都不在文案上：

**一、空闲清扫不触发 `OnClose`。** 会话有两条关闭路径：显式转移，和空闲清扫。
清扫直接写库然后 return（`state/manager.go:341`），绕过了回调；而**清扫是这套部署里
唯一会自动关闭会话的路径**。挂在这个回调上的东西因此全部够不着——调研可配置、
可解析、有单测，**从来没发出过一条**。已抽出 `fireOnClose`，两条路径都走它。

**二、就算发出去了，客户的回复也认不回来。** 调研是在会话关闭那一刻发出的，
所以评分永远是"关闭后的第一条消息"；而会话查找 `WHERE state != 'closed'`，
这条消息于是**新开了一个会话**，router 里那段"先问是不是评分再重开"的分支
（`routing/router.go:435`）根本走不到。客户的"5"被当成普通提问，AI 认真回答了它。
已把查找放宽到"处于调研待答期内的已关会话"，待答期由**产线自己的 `timeout_hours`**
经一个钩子传入；不注册钩子时窗口为 0，行为与从前完全一致——
`state` 包自己无权放宽会话查找。

这两条对应缺陷登记里的 **D1**（原文标题就是"满意度调查从未真正发出"，
且已正确诊断出第一处；第二处是新的）。D1 已改写为只剩"超时的调研没人翻成 `no_response`"。

**文案本身**：`pkg/survey` 加 `prompt_message` / `thanks_message`，平台默认值就是原来的
两个 Go 常量；两条一律走 `IsBlankAnswer` 回落，限长 500 字。感谢语按计划**没有扩
`pendingSurveyData`**，改用"回查产线再读配置"——扩了会让改动上线那一刻所有在途 pending
反序列化出空串，正撞上刚修过的"空答复不得投递"。

**提问语上加了一条契约校验。** 回复解析器只认 1–5 的裸数字，其余一律当普通消息。
所以一段被改成"您满意吗？请回复满意或不满意"的提问语，会产出一个**客户答不对的调研**：
评分不记录、会话重开、**任何地方都不报错**。写入时拒绝它，是这件事唯一能被看见的时刻。
门户在**编辑过程中**就标红，不等保存。

**实机验收（DrillCo3 演练线，改库不经门户）**：

```
自定义文案      客户收到「【S7验收】请回复 1-5 给本次服务打分…」
待答 TTL        7198s（配置 timeout_hours=2）
回复 "5"        收到「【S7验收】收到您的评分，多谢！」
                survey_status=completed，satisfaction_score=5，待答键已清除
两条文案置空    客户收到的是平台默认文案（回落生效）
```

接线验收通过后才上的表单。API 侧：缺 1-5 的提问语 400、超 500 字 400、
合规改写 200 且读回一致、清空后回答的是平台文案。
调研卡另补两项：等待时长注明"发出那一刻写死，只对此后的调研生效"，
以及只读的一行"平台设定：会话静默 30 分钟后自动结束，调研就在那一刻发出"——
租户能配这条消息的一切，唯独配不了它什么时候发。

**注意**：XDYX 线的调研本来就是开着的（`enabled=true`），此前从未真正发出过；
这次修复之后它**会开始真的给客户发调研**。演练用的一次性会话已清理。

### S6 —— 转人工卡片认账

**关键词表在 triage=on 时置灰**，并给出"已由意图分诊接管"的说明。
被置灰的字段保存时**不回写**——一个 disabled 的输入框里仍然有文本，
写回去等于把一份运行时根本不读的列表持久化，还顺手掩盖了它已被弃用这件事。

同时改了卡片开头那句介绍：它写着"客户说了转人工关键词→转人工"，
在 triage=on 下**是假的**。在一句假话下面挂一条警告并不能修好它——先被读到的是那句话。
现在这句介绍随模式切换。

**验收按计划做了真的开关**：把 `INTENT_TRIAGE` 改成 on、重启 router，
门户自动置灰，**前端没有为此改过任何判断逻辑**，全靠 S1 那条 `/configz` 链路。
验完已还原为默认（shadow）。

### 阈值阶梯：这里核减了调研的一条断言

调研称"置信度只有 0.3/0.72/0.75/0.90 等离散档，填 >0.90 即 100% 转人工"。
**不成立**：`GroundedConfidence` 返回的是 `max(检索平均分, 档位)`，
检索分是连续的（`CalculateConfidence`，无命中时 0.3）。档位是**下限**不是上限，
所以谈不上"可达的最高置信度"——那是一个我们无法诚实承诺的数字。

改为展示**阈值阶梯**，取自 `grounding.go` 自己的文档注释：

```
≥ 0.70  检索命中、历史经验、确定性事实，任一即可      本租户可达
≥ 0.75  必须有确定性事实，仅靠历史经验不够            事实注入未接通，设到这里会全部转人工
≥ 0.80  必须有事实，且答复的断言经过校验且无冲突      需先开启事实注入与校验
```

每一档标注本租户当前能不能踩上去，并高亮当前值所在的档。
这把"填一个数字"变成了"选一种取证要求"。

### 又拦下一个假红灯

用一次性租户验"事实未接通时上面两档应显示为不可达"时，发现**全新租户的知识库行
报红色"检索方式与索引不匹配（索引 空，检索 semantic_search）"**。
原因是新建的空数据集在 Dify 里 `indexing_technique` 尚未确定（要等第一篇文档索引），
而匹配判定把空值当成了不匹配——**每开一个新租户都会看到一条假告警**。

已区分为独立状态：`knowledge.empty`，前端显示为**黄色**"数据集为空"而非红色不匹配。
这不是故障，只是还没传文档。

验完的一次性租户已销户，五条产线状态不受影响。

### S5 —— 接通体检卡（本增量第一次动界面）

置于 AI 设置页**最顶部**，因为它的文案是"先看这里，再改设置"——
第一版我插在提示词卡之后，截图一看就不成立，已移到首位。

**设计上的一个判断：这些行是一条链路，不是一张清单，所以画成链路。**
状态点之间有一条细线串联，上游断了，它下面的行就是空谈——
这个依赖关系应该看得见，而不是写在文案里。行的顺序按消息经过的先后，
不按修改频率：这一页真正的失败模式不是"找不到常用项"，
而是"改了一个开关被上游作废却毫无提示"。

六行：上下文变量 · 知识库检索 · 确定性事实注入 · 应答策略注入 ·
转人工关键词 · 生效模型。每行只回答"这一环有没有在传东西"，
**不展示配置值**——页面此前完全可以把 inject_facts 显示成 on，
而事实早在两步之前就被丢弃了，没有任何地方说得出来。

服务端新增两个数据块（`GET /ai-settings`）：
- `knowledge`：数据集是否存在、检索方式与索引是否匹配（用 S2 的 `GetDatasetConfig`）
- `runtime`：**收窄后的**平台开关，只出 `ontology_enabled`/`intent_triage`/`scene_mode`
  三个行为开关。判据是"租户能用这条信息解释自己看到的行为"；
  TTL、worker 数、ACEST 端点解释不了租户能操作的任何事，不在此列。
  完整的一套仍只在 admin 专属的 `/api/v1/platform/runtime`。

### 过程中修掉的一个"假按钮"

第一版给"没有数据集"那一行配了「修复绑定」按钮。**它修不了这件事**——
`dataset/bind` 只能绑定已存在的数据集，而这三条线压根没有数据集（D8，要等阶段 3）。
点下去只会报错。已改为不给按钮，文案直说"补建数据集需要平台侧处理，本页暂时无法自助修复"。

一个修不了它旁边那件事的按钮，比没有按钮更糟：它耗掉一次点击、一个错误提示，
以及操作者对这一页上**其余每个按钮**的信任。

同时发现 S2 做的检索安全校验**根本没有接口暴露**，一并补上
`POST .../ai-settings/dataset/retrieval`（仅 admin）。

### 实机验收

体检卡截图（`tmp/diag-sound.png` / `tmp/diag-broken.png`）经真实登录渲染：
XDYX 全绿「全部接通」，MegaStore 报「1 处断开」并指出没有数据集——
**这张卡上线第一天就把 D8 摆到了台面上**，而它此前只存在于缺陷文档里。

```
检索修复 XDYX（有数据集且匹配）  -> 200，回报当前 knowledge 状态
检索修复 MegaStore（没有数据集）  -> 400 "no Dify dataset configured"（不假装成功）
租户 user 调两个修复接口          -> 均 403
```

### S4 —— 应用变量声明对拍与补齐

`pkg/difyapp` 新增 `DeclaredVariables` / `MissingContextVariables`；
bridge 的 `GetAppConfig` 把对拍结果一并带出（`AppInfo.Variables`），
并新增幂等的 `EnsureContextVariables`；
新接口 `POST /ai-settings/variables/repair`（仅 admin，与 `dataset/bind` 同类：
修的是被开出来的应用，不是租户的偏好）。

**为什么这个检查有价值**：Dify 会把应用未声明的输入**静默丢弃**——不报错、
答复里也看不出来。于是"本体没话说"和"本体压根没送到"这两件事，从答复上完全无法区分。
对拍是唯一能把它们分开的地方。

**构造性验收**（`tmp/verify_s4.py`）：五条线目前全是齐的，被动检查证明不了任何事，
所以人为造了一个缺失：

```
1. 开一次性租户 VARTEST
2. 开户后 complete=True                     ← 开户流程本身没问题
3. 从 Dify 里删掉 scene_context             ← 模拟手工改过/早期开户的应用
4. 门户报 complete=False missing=[scene_context]
5. 修复 added=[scene_context]
6. 复检 complete=True，declared=7 项
7. 再修一次 already_complete=True, added=None   ← 幂等
8. 销户，Dify 应用与 Chatwoot 账户随之清理
```

另有单测四条（`pkg/difyapp/prompt_variables_test.go`）覆盖：漏报、全齐、
空表单（全缺）、以及**修复必须是增量的**——运维自己加的变量不能被冲掉、重复执行不产生改动。

五条产线经新接口复查：全部 `complete=True`。

### S4 的定位（与调研报告的判断不同）

调研称这是"本次最大漏项"。**核减**：五条线的七个变量一直是齐的，
它是**预防性**体检，不是在修一个正在发生的故障。
相应地，报告称 S4 会让"一批租户第一次真的收到 facts_context"从而金标必然漂移——
对这五条线不成立，本次金标零影响。

### 实机验收

```
router /configz  {"intent_triage":"shadow","scene_mode":"on","ontology_enabled":true,
                  "ontology_cache_ttl":"5m0s","route_cache_ttl":"5m0s",
                  "idle_timeout":"30m0s","acest_enabled":false,"workers":4}
router.env       只有 SCENE_MODE=on —— 其余全部落默认值，与上面逐项对得上
admin 转述       available:true，八个字段与 router 完全一致
租户 user 访问   403
facts/config 写入   -> "cache_invalidated": true
ai-settings 写入    -> "cache_invalidated": true
```

### 顺带纠正一处我自己写错的事实

我此前说"当前 `INTENT_TRIAGE` 未设置，落到 **off**"——**错了，落到的是 shadow**
（`triage.go:36` `DefaultTriageMode = TriageShadow`）。结论不变：
`EvaluateWithMode` 只在 `TriageOn` 下弃用关键词表，shadow 下关键词照常生效。
`doc/plan-ai-settings-consolidation.md` 第 0.2 节已改。
这正好说明 S1 存在的理由——**这个值没有任何界面能看到，只能靠猜，而我就猜错了。**

### 与计划的差异

- **数据集覆盖字段的合并形态提前做了**（S2 第 6 步）：`DatasetConfig.Overrides`
  当前恒为空，但 PATCH 替换的是整个 `retrieval_model` 对象，等 S11 加 top_k 时
  才引入合并就晚了——两个写入口会在同一张卡上互相静默回滚。
- **`routeCacheKeyPrefix` 在测试里刻意重写了一份**而不是 import 共享常量：
  这个测试要证明的是"router 会读的那个 key 没了"，跟着被测代码改名会让测试
  在 key 改错时照样绿。

### 遗留

- 门户仍无条件显示"立即生效"，没有读 `cache_invalidated`。这是 S5 之后的事，
  但接口已经在说实话了。
- `/configz` 无鉴权（与 `/healthz`、`/metrics` 一致），因此只出开关、不出任何
  连接串与凭据；ACEST 只报 enabled 真假。

## 不在本增量内
门户任何改动、体检卡、提示词契约校验、调研文案、top_k 可写、admin.html 平台只读卡。
它们是 S4 之后的事，见 `doc/plan-ai-settings-consolidation.md`。

## 既存红灯
`pkg/domain` 的 `TestSeedOntologiesMatchCannedResponses` 先于阶段 0 就红，根治属阶段 3（D8）。

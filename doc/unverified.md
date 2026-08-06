# 未验证事项登记

已经交付、但**尚未被证据支持**的部分。单独列出来，是为了不被"测试全绿"这句话盖住。
每条写清楚：现状是什么、要验证需要什么。修掉一条就从这里删掉。

最后更新：2026-08-06（客户自助层四期后）

---

## 一、零真实流量

### 1.1 本体链路从未见过一条真实客户消息
`inject_facts` / `validation` / `enforce` 三种模式全部只在黄金集上跑过。
真实消息的口语化程度、多轮上下文、客户追问、跨产品线混问，一条都没测过。

**要验证需要**：一条产品线开 `inject_facts: true` + `validation: shadow` 跑一到两周，
看 `claim_violations` 表和 `router_claim_violations_total{kind,mode}`。

### 1.2 黄金集是循环的
本体和黄金集都是我写的，都从 `canned_responses.yaml` 抽出来，
而且实测过程中我根据测试失败**改了三次本体**。
共享的误解它一个都抓不到——只能证明"本体和黄金集彼此一致"。

**要验证需要**：本体由业务方写，黄金集由另一个人从真实对话日志里挑，两边不见面。
规模至少 200 条，含对抗性用例（客户反驳、诱导、提示词注入）。

### 1.3 事实注入的 token 成本未量化
`Render` 把整个本体渲染进每一次调用，不做与问题相关的裁剪——
刻意如此，避免裁剪逻辑本身成为召回失败的来源。
但代价是每条消息的 prompt token 都变大，**没有按真实消息量算过账**。

**要验证需要**：`router_dify_tokens_total{type="prompt"}` 开关本体前后对比。

---

## 二、判定链路（原 2.1/2.2 已修，遗留两条新的未验证）

原 2.1（主链路无测试）与 2.2（evalset 重实现判定链路且已漂移）已解决：
判定链路抽成了 `internal/routing/judge.go` 的共同入口，router 与 evalset
都走它；`internal/routing` 用 miniredis + Dify 打桩覆盖了重复投递跳过、
enforce 强制转人工、熔断打开后放行三条主链路。evalset 与 router 仅剩两处
**声明式**差异（无熔断、校验封顶 shadow），写在 `scoreCase` 的注释里。

修复过程中发现并修掉一处被证伪的设计：**违规曾把置信度直接压到 0**，
借 `low_confidence` 转人工——熔断拦不住这条通道（跳闸后违规回答照样被压下，
`bypassed` 指标记了"放行"实则没放）、shadow 模式因此并非只影子、
冲突还被误记为失败经验样本。现在冲突不降分，压制只发生在受熔断约束的
enforce 覆盖里（`grounding.go` / `judge.go` 注释与测试钉住新契约）。

### 2.1（新）语义修正只被推理与单元测试支撑，未经真实流量观察
熔断"跳闸后违规回答发给客户"的行为此前在线上从未真正发生过（旧通道拦着）。
修正后它会真的发生——这正是设计意图，但其真实代价从未被观察。

**要验证需要**：enforce + 熔断跳闸的真实场次，对照
`router_ontology_breaker_bypassed_total` 与客诉/改答率。

### 2.2（新）已解决：新基线已保存
2026-08-06 部署演练中黄金集对 deepseek-v4-flash 重跑 60/60（标签率 87%，
0 违规，执法器与黄金集 0 分歧），基线存于
`unica/router/testdata/golden/baseline-deepseek-v4-flash.json`。

---

## 三、熔断

### 3.1 阈值是猜的
`trip_rate 0.25 / min_samples 20 / window 100 / cooldown 15min` 背后没有数据。
唯一的测量是黄金集 52 条里 0 条触发——那说明不了上限该画在哪。

**要验证需要**：shadow 期间的真实违规率分布。在那之前，
把这四个值当成"需要按产品线调"的参数，不要当成经过验证的默认值。

### 3.2 从未在真实部署里触发过
行为由带假时钟的单元测试钉住，不是由一次真实事故钉住。

### 3.3 多副本行为未测
状态每进程独立，N 个副本各判各的。临界比例时可能有的副本在拦、有的没拦。
从未跑过一个以上的 router 实例。


---

## 三点五、本体运营面（第 2-3 期新增，第 5 期演练已大部销账）

2026-08-06 WSL 部署演练结果：全部 14 个迁移在全新 PG 16.14 干净重放；
013/014 在存量库上两遍幂等通过；admin 本体 API（含 `SetConfigKey` jsonb
合并——异键并存实证）、违规列表/复核/重开 SQL 全部真库过；portal 三页对
真实 admin 联调 19/20（唯一失败是环境数据缺口，且顺藤摸出 channels/
channel_configs 双表断裂，已修——见 3.5.6）。演练还抓出并修复两个真缺陷：
`audit_logs.ip_address` 是 inet 而直呼 LogEvent 传了带端口的 RemoteAddr
（所有本体/复核审计静默失败），以及 action CHECK 不认识
publish/rollback/review（迁移 014 扩展词汇）。原 3.5.1/3.5.2/3.5.3 销账。

### 3.5.4 告警阈值仍无数据支撑
`ClaimViolationSpike` 的 50/15min 是占位阈值（规则文件里已标注）。
configmap 半边已解决：生成命令改为目录形式（不再漏面板），kubectl
dry-run 验证 9 块全捕获。

**要验证需要**：shadow 真实流量标定阈值。

### 3.5.5 deepseek-v4-flash 经 openai_api_compatible 接入
Dify 0.15.3 原生 deepseek provider 的模型白名单没有 v4-flash，
演练改走 openai_api_compatible（endpoint https://api.deepseek.com/v1）。
行为等价但流式分隔/函数调用参数按保守值配置。

**要验证需要**：Dify 升级后回归原生 provider；或长会话/流式场景实测。

### 3.5.6 channels 表已成死架构，双表并存待清理
全仓库没有代码写 `channels`（002），admin/portal 写的是 `channel_configs`
（007），而 router 曾只按 `channels` 路由——portal 建的渠道收到消息会被
"failed to resolve route" 静默丢弃。已修：路由回退到 `channel_configs`
（两个分支演练中都真实路由成功）。遗留：`channels` 表与旧查询分支
应在确认无存量部署依赖后迁移移除。

**要验证需要**：确认没有环境仍靠 `channels` 行路由，然后出移除迁移。

---

## 三点七、客户自助层（2026-08-06 四期交付后新增）

2026-08-06 WSL 复原环境实测已覆盖：一键开户全链路（建线→Dify 应用+数据集+
应用密钥→门户账号+按线赋角色）、幂等续跑（重 POST 200、零重建、密码不复现）、
数据集级密钥上传文本/文件→按 batch 轮询到 completed→列表/删除、跨租户 403、
审计落库且密钥全部脱敏（[redacted] 实证）、portal 界面冒烟（单线账号选择器
锁定、角色卡片隐藏、界面上传到索引完成零 JS 报错）。以下仍无证据：

### 3.7.1 Chatwoot 平台 API 开户路径只对假服务器验证过
账号/用户/绑定/API 收件箱四步、"访问令牌只在创建时返回"的前提、
渐进落盘在真实故障下的续作行为，全部只有 httptest 假服务背书。
演练环境未部署 Chatwoot，实测走的是降级路径（如实报 configured:false）。

**要验证需要**：真实 Chatwoot + Super Admin 手工建一次平台应用拿 token，
跑一次完整开户，再故意在中间打断跑一次重试，对照 config_json.chatwoot
分段落盘与最终 configured:true。

### 3.7.2 high_quality 索引未在配了嵌入模型的工作区验过
演练工作区（DeepSeek 经 openai_api_compatible，无嵌入模型）实测
high_quality 被 Dify 以 provider_not_initialize 拒绝，economy 全通——
因此新增 DIFY_INDEXING_TECHNIQUE（默认 high_quality）。economy 的检索
质量对比、以及配了嵌入模型后 high_quality 的回归，都没跑过。

**要验证需要**：接入一个提供嵌入的模型商，同一批文档两种模式对比检索命中。

### 3.7.3 router 知识管道修复后仍是未接线的死代码
`internal/knowledge`（batch 修复、数据集密钥）有单测且与 admin 共用同一
个实测过的客户端，但 cmd/router 没有挂载它，无真实调用方。

**要验证需要**：接线或删除，二选一；接线后按 admin 同款清单实测。

---

## 四、环境单点

### 4.1 数据库只在 PostgreSQL 16.14 上验过
迁移 012 用到 `hashtext`、`pg_partitioned_table.partattrs`、
分区父表上的无目标 `ON CONFLICT DO NOTHING`。
按文档这些在 13+ 都可用（迁移 001 声明的下限），但只在 16.14 上真跑过。

### 4.2 Dify 只在 0.15.3 上验过
`POST /apps/{id}/model-config` 的形状随版本变化。compose 里刻意钉死了版本。
另外 advanced 提示词模式的拒绝路径**只对假服务器测过**，
没有真的建一个 advanced 模式的应用去撞。

### 4.3 多实例并发建分区未测
`ensure_month_partition` 里有建议锁 + `duplicate_table` 兜底，
但从没让两个 router 同时启动去撞过。

### 4.4 只部署 admin 不部署 router 时，audit_logs 无人续期
写 `audit_logs` 的是 admin，续期的是 router。这种部署组合没测过，
只在 README 里写了"需要自行跑维护脚本"。

---

## 五、行为已改变但没观察过

### 5.1 重复投递跳过，可能让客户永远收不到回答
上一次投递若在**存完消息、还没回答**时崩溃，重投会被当成重复直接跳过。
与现有 Redis 去重（也是先占坑再处理）完全同构，但这条路径没有被观察过。

### 5.2 去重窗口跨月边界会漏
唯一索引建在叶子分区上，唯一性窗口 = 一个自然月。
23:59 与次月 00:00 的重投逃得掉。平台重投窗口是秒到分钟级，所以接受了——
但没有任何平台的重投行为数据支持"秒到分钟级"这个前提。

---

## 记录规则

- 一条被真实数据或真实环境验证过 → 从本文件删除，把结论写进对应模块的注释或文档。
- 一条被证伪 → 修，并在这里留一行说明修的是什么，直到修复本身也被验证。
- **新增能力时如果验证不了，先在这里登记再合并**，不要等下一个人去发现。

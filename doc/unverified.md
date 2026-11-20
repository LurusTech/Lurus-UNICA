# 未验证事项登记

已经交付、但**尚未被证据支持**的部分。单独列出来，是为了不被"测试全绿"这句话盖住。
每条写清楚：现状是什么、要验证需要什么。修掉一条就从这里删掉。

最后更新：2026-11-20（并入验收候选后修正 J6 的 gateway 一句：该路径已同时接受字符串与整数 `message_type`，testdata 按 v3.15 源码形状整理，仍非生产抓包；10-16 新增 J7 镜像未构建；10-13 J5 补跑 `-race`，只余磁盘满与只读目录；10-10 J5 部分销账并新增 J6；J3 改为 `judge checkup`，新增 J5 多店事实目录；10-09 新增 J1–J4；10-08：3.7.3 销账、"2026-10-08 收口"一节）

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
2026-08-06 部署演练中黄金集对平台当时声明的模型重跑 60/60（标签率 87%，
0 违规，执法器与黄金集 0 分歧），基线存于
`unica/router/testdata/golden/` 下的基线文件。

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

### 3.5.5 平台模型经 Dify 的通用兼容型供应商接入
Dify 0.15.3 该模型供应商的原生 provider 白名单里没有平台声明的模型，
演练改走 Dify 的通用兼容型供应商（供应商自己的兼容端点）。
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
演练工作区（平台模型经 Dify 的通用兼容型供应商接入，无嵌入模型）实测
high_quality 被 Dify 以 provider_not_initialize 拒绝，economy 全通——
因此新增 DIFY_INDEXING_TECHNIQUE（默认 high_quality）。economy 的检索
质量对比、以及配了嵌入模型后 high_quality 的回归，都没跑过。

**要验证需要**：接入一个提供嵌入的模型商，同一批文档两种模式对比检索命中。

### 3.7.3（已销账，2026-10-08 核对）router 知识管道未接线
原文：`router/internal/knowledge` 有单测但 cmd/router 没有挂载，无真实调用方，需接线或删除。
**结论：已删除，删于 `d264528`**（提交说明："router/internal/knowledge deleted: no importers"）。
2026-10-08 核对：该目录已不存在，全仓 Go 代码无任何对它的引用；仍提到它的只有
`doc/plan-creator-ontology.md` 与 `docs/stories/STORY-020.md` 两份历史文档。

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

---

## 承接自"全局禁止 AI 承诺退款/赔付"增量（2026-08-22 结转）

该增量已交付并跑通五线全量，以下是它留下的、**尚无证据支持**的部分。
原计划文件已被配置面重构的计划覆盖，故在此登记，避免随文件一起消失。

### 其余四线的多轮采集验证未做
只有 XDYX 做过完整多轮（`tmp/run_xdyx_intake.py`，10/10，判据是 `handoff_events` 落表
而非答复措辞）。MegaStore / TechZone / FreshMart / AJYJ 只有单轮金标。
单轮测不出"必问项问齐之后是否真的转"、"会不会连环追问不收口"这两件事，
而它们正是采集规范的核心。

**要验证需要**：把 `run_xdyx_intake.py` 的六个场景按各线业务改写后重跑，看 `handoff_events`。

### 三条零售线的采集清单寄生在本体里
MegaStore / TechZone / FreshMart 没有知识库数据集（D8），采集清单只能写进本体，
靠 `facts_context` 无条件注入。这是权宜之计且有反作用：本体塞多了会淹没政策事实
（D17，实测 TechZone 注入量一度到 19195 字符、六条基础政策题回退，压到 7407 后恢复）。
提示词规则 9 写的是"按参考知识里的清单采集"，对这三条线是**悬空引用**。

**要验证需要**：D8 修复、数据集补建之后，把清单挪回知识库并复测这三条线。

### mega-02 的规则 6/7 回退未深查
A/B 只确认了 mega-17/19 的变化由规则 8/9 引起、与规则 6/7 无关；mega-02 的回退没查。

### 坐席侧的工单观感未验
转人工摘要读错字段的修复（D15）只跑了单测。人工在 Chatwoot 里实际看到的工单是否
真的"信息已齐、不必重复询问"——采集这套改造的全部意义所在——没有人看过。
2026-08-22 另查明坐席回复回传链路本身是断的（网关 webhook 端点未注册），
这条验证在链路修好之前做不了。

---

## 2026-10-08 收口：本轮改动中未经实机验证的部分

以下改动只有单测与 `go vet`/`go test` 支撑（详见 `doc/known-defects.md` 对应条目），
演练环境已拆，没有一条在真实部署或真实流量上跑过。

### D1 调研超时扫描
真实 PostgreSQL 上的两条 SQL（按 `survey_sent_at` 取 `sent` 行、带 `survey_status='sent'`
守卫的翻转）只对打桩存储测过；多副本同时扫描的去重靠 UPDATE 守卫，未实测。
**要验证需要**：开一条产线的调研，发出后不回复，等过 `timeout_hours`，
看该会话变为 `no_response`、`survey_timeout_total` 加一且只加一。

### D2 坐席"解决"关闭会话
真实 Chatwoot 的 `conversation_status_changed` 回调形状只按现有结构体与旧测试推断；
关闭后 `OnClose` 触发调研、客户再发消息回到 AI 这两段行为未在实机走过。
**要验证需要**：真实 Chatwoot 里点一次"解决"，对照 router 日志的关闭记录、
`unica:dead-letter` 无新增、会话状态为 closed。

### D20 全角标签
解析器覆盖了构造出的半角/全角/混合/空白写法；模型在真实答复里实际会写出哪些变体
没有采样过，不认识的写法（如尖括号）仍会原样到达客户。
**要验证需要**：有真实答复轨迹后，按标签名搜一遍残留在已投递答复里的标记。

### D23 token 类型
新旧 token 并存期（部署后一个 refresh 有效期内）的行为只由单测钉住：
旧 access 继续可用、旧 refresh 仍能换新。未在真实门户上走过一次"部署后不重新登录"的流程。
**要验证需要**：部署前登录并勾选"记住我"，部署后不登出，确认页面可用且续期成功。

---

## 2026-10-09 闸门形态：未经实机验证的部分

本轮把判定内核抽成 `pkg/judge`、新建独立二进制 `unica/judge`（含 `judge checkup`）、行业包升级；10-10 按 owner 授权定了开源边界与首个对象，体检搬进开放模块，事实支持按店分文件。
全部只有单测、`go vet`、本机冒烟（起二进制调全部接口；对 8 行 CSV 出报告）支撑。详见 `doc/judge-gate.md`。

### J1 Dify 插件骨架没有打包安装过
`unica/judge/adapters/dify-plugin/` 的 manifest、provider、tool 三件按官方插件结构手写，
没有在任何 Dify 实例上打包、安装、在工作流里跑过一次。HTTP 请求节点接法只写了步骤，同样未在实例上点过。
**要验证需要**：一台 Dify ≥ 1.0（插件机制）实例，按 `adapters/dify-plugin/README.md` 两种接法各走一遍，
看 `withheld=true` 时是否进入转人工分支、闸门关机时是否按"失败即转人工"处理。

### J2 Chatwoot webhook 载荷形状只按现有结构体推断
适配器读 `event`、`message_type`、`private`、`conversation.id`、`sender.type`；
`sender.type == "agent_bot"` 判为 AI 的依据是 Chatwoot 文档，未对真实回调抓包。
`conversation.messages` 回退路径对真实载荷是否存在不确定，所以主路径是适配器自己记住的上一条客户消息。
**要验证需要**：真实 Chatwoot 配一个 webhook 指向 `/adapters/chatwoot`，坐席与 agent bot 各发一条，
核对证据行的 `customer_message`、`speaker`，以及私信备注是否出现且客户侧不可见。

### J3 体检的 Chatwoot JSON 与 CSV 格式是约定，不是实测格式
Chatwoot 导入接受"对话数组 / `payload` / `data.payload`"三种包法，是按 API 文档写的；
CSV 要求带表头并由 `-csv-cols` 映射四列，千牛、抖店等卖家工具的真实导出一份都没拿到过。
**要验证需要**：各拿一份真实导出跑 `judge checkup`，看是否能在不改代码、只改 `-csv-cols` 的前提下读入。

### J4 没有任何外部影子部署
"若开启将扣下 N 条"这个数在真实流量上一次都没产生过；`judge.MinShadowSamples = 30` 是拍的下限，不是统计论证。
**要验证需要**：一家代运营机构的一条产线接影子两周，看 `GET /shadow/<line>` 的 `would_withhold` 与人工抽检的一致性。

### J5 多店事实目录：50 店加载、并发 PUT 与 `-race` 已验，磁盘满与只读目录未测
10-10 补了两条测试：50 个 `<line>.yaml` 的目录加载；两店各 20 次并发 PUT 与判定交错后事实互不串线、两条证据链各自连续。
第二条在修复前失败（同一店并发 PUT 的临时文件改名互相冲突，且盘上与内存可能落在不同版本），已在 `FactsStore.Replace` 加写锁串行化。
隔离检查已做：`GOWORK=off go build ./...` 通过，`go list -deps` 只拉到 `pkg` 与本模块自身。
10-13 在 WSL（带 C 工具链）对 `unica/judge`、`unica/pkg`、`unica/router` 跑 `go test -race -count=1 ./...`，全部通过。
**仍未验证**：磁盘满、只读目录下的行为没有测。
**要验证需要**：在一个小容量或只读挂载的目录上起 `judge serve -evidence`，确认写失败时返回错误、证据链回滚而不是留下半行。

### J6 Chatwoot webhook 顶层 `message_type` 的字符串形态，闸门适配器仍未对生产抓包验过
闸门适配器 10-10 改为同时接受整数与 `incoming`/`outgoing` 字符串，依据是 Chatwoot 文档与社区集成的通行写法，本仓库没有一份真实抓包。
保留模块 `gateway/internal/webhook/chatwoot.go` 在验收候选里已同时接受字符串与整数（`cwMessageType`）。旁边的 testdata 是按 Chatwoot v3.15 的 `Message#webhook_data` 与 `Conversations::EventDataPresenter#push_data` 整理的形状，不是从生产实例抓下来的原文。
**要验证需要**：接一个真实 Chatwoot 实例，抓一条 `message_created` 事件原文，对照闸门适配器与 gateway 两处解码。

### J7 `judge/Dockerfile` 未实际构建过
10-16 新增。本机 Windows 侧没有 docker，WSL 当时无响应，镜像一次都没建过；Dockerfile 只按其他服务的写法与 `GOWORK=off` 的独立编译推出来。
发布包脚本 `judge/scripts/dist.sh` 已在本机实跑：Windows 与 Linux 两个包产出，解压后 `judge.exe version` 与示例体检均正常；macOS 与 arm64 三个目标只是交叉编译，没在对应机器上运行过。
**要验证需要**：在带 docker 的环境 `docker build -f judge/Dockerfile .` 一次，带卷起 `serve`，PUT 一份事实、判一条、重启后确认证据链续号。

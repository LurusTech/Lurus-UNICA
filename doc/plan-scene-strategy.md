# 设计方案：场景化应答策略注入（售前 / 售后）

> 状态：设计稿，待评审。范围为 router + admin + Dify 提示词契约，不含经验回流与 acest。

## 1. 要解决的问题

每个产品线只有一套系统提示词（`pkg/difyapp/prompt.go:52`），五条规则全部是约束性的——
不得改写、不得推测、不要含糊带过、不要编造、必须打事实标签。这是一个防幻觉的
合规型客服人格，它做对了它要做的事，但它没有任何关于**怎么说**的指导：

| 场景 | 现在的表现 | 期望的表现 |
|---|---|---|
| 售前问价 | 直接报数字 | 先确认用途和使用频率，再用可比口径给价格 |
| 售前比较 | 罗列参数 | 用确定性事实里的具体数值和可比对象作结论式对比 |
| 售前犹豫 | 无动作 | 给一个低承诺的下一步，不催单 |
| 问题模糊 | 猜测意图后作答 | 用二选一封闭问题收敛，一次只问一个 |
| 售后故障 | 直接给流程 | 先复述客户的具体损失，再给方案和确定时间点 |
| 售后情绪 | 信息密度不变 | 降低单条信息密度，一次只给一个待执行动作 |

目标是让模型在不同场景下采用不同的应答**策略**（表达方式、提问顺序、信息密度），
而不改变它可以陈述的**内容**。**这条边界是整个方案的安全前提**：内容仍由本体和
知识库决定，策略只管形式。

## 2. 现状（均已在代码中验证）

| # | 事实 | 位置 | 对本方案的影响 |
|---|---|---|---|
| C1 | `intent.Classify` 只回答"要不要绕过 AI"，`Informational` 分支里结果打完指标即丢弃 | `routing/router.go:504-510` | 需要一个正交的场景轴，不能改造它 |
| C2 | Dify `inputs` 共 6 个 key，在 `prepareAIContext` 手写组装 | `routing/router.go:554-607` | 新变量需手动加一行，无自动生成 |
| C3 | `prepareAIContext` 开头已有一次 Redis 读（`getDifyConvID`），JSON 结构、TTL 24h | `routing/router.go:451,458,471` | 会话级粘性场景可搭这趟车，零新增往返 |
| C4 | **不存在任何自动漂移修复**。改 `DefaultSystemPrompt` 不会传播到已有 Dify 应用 | 全仓库仅三个写入点，见 §6 | 模板改动必须配主动回灌 |
| C5 | 模板在仓库里有三份拷贝，Python 那份无任何一致性保证 | `pkg/difyapp/prompt.go` / `deploy/dify-preview/configure_apps.py:39-46,52-69` / `router/internal/bridge/dify_admin.go:223`（别名，安全） | 改模板要同步改 Python，并补一条守卫 |
| C6 | `guardrail.TriageMode` 是本仓库"新行为分三档上线"的既有惯例 | `guardrail/triage.go` | 场景注入照抄这个形状 |

## 3. 设计

### 3.1 场景分类器（新增，与现有分类正交）

新文件 `router/internal/intent/stage.go`。`Classify` 回答"谁来接"，`ClassifyStage`
回答"在什么阶段"。两者互不调用、互不影响，`Classify` 一行不动。

```go
type Stage string
const (
    StagePresales  Stage = "presales"
    StagePostsales Stage = "postsales"
    StageUnknown   Stage = "unknown"
)

type StageResult struct {
    Stage   Stage
    Reason  string  // 指标标签，集合小而稳定
    Matched string
}

func ClassifyStage(message string) StageResult
func ResolveStage(prior Stage, message string) StageResult
```

判定规则严格沿用 `classifier.go` 已经打磨过的"双条件 + 否定守卫"风格，
并把它踩过的子串碰撞坑当作硬约束：

**售后**（优先级高于售前，信号更具体、歧义更少）
- 故障标记单独成立：`坏了/碎了/裂了/漏了/发霉/变质/过期/不亮/开不了机/连不上/少发/错发/漏发/没收到/迟迟未到`——这些词本身蕴含"已持有"。
- 持有标记 + 售后动作双条件才成立：持有（`我买的/我收到/收到的/已下单/已付款/买回来/拿到`）+ 动作（`退货/退款/换货/维修/返修/寄回/售后/三包/理赔/保修`）。
- **单独出现"退款/退货/保修"一律不算售后**。`退款政策是什么`、`支持7天无理由吗`、`保修多久` 都是典型售前咨询。这是本规则集最重要的约束，必须有测试守卫。

**售前**
- 价格：`多少钱/什么价/价格/贵不贵/能便宜/优惠/有券/包邮吗`
- 比较：`哪个好/哪款/区别/差别/对比/值得买/推荐/怎么样/好用吗`
- 适配：`适合/能不能用/支持吗/尺码/多大/几寸/容量/参数`
- 可得性：`有货吗/有现货/什么时候上新`

**都不命中 → `StageUnknown`**

### 3.2 粘性：售后是吸收态

`ResolveStage(prior, msg)` 优先级：

1. 本条判为售后 → 售后
2. `prior == 售后` → 保持售后（**吸收态**）
3. 本条判为售前 → 售前
4. 否则继承 `prior`

售后设为吸收态的理由：客户已经持有商品是**世界的状态**，不会在同一会话里逆转。
售后会话中出现的"换一个多少钱"应当继续用售后策略（先确认问题、给确定方案），
而不是切回推销语气。

粘性存储复用 §2-C3 那趟 Redis 车：`difyConvSession` 加 `Stage` 字段，
`getDifyConvID` 改为返回整个 session，`aiCallContext` 加 `stage`，
`recordJudgement` 里的 `setDifyConvID` 带上它回写。
**`setDifyConvID` 目前整体覆盖该 JSON，不把 stage 传进去就会每轮被抹掉——
这是本改动最容易写错的一处。**

粘性随 24h TTL 自然过期，与 Dify 自身多轮记忆的生命周期一致：记忆没了，
场景也重置，行为是一致的。

### 3.3 策略文本（平台内置）

新文件 `pkg/difyapp/strategy.go`，导出 `StrategyFor(stage string) string`。
放在 `pkg/difyapp` 而非 router 内部，因为该包已经是"router 和 admin 都要认的
Dify 契约"的所在地，策略文本属于同一契约。

三段文本（售前 / 售后 / 通用），每段自带标题（标题随场景变化）。要点见 §1 的表格，
外加三条 2026-08-11 目标核对后补上的（原稿写得比目标保守）：

- **售前·临门一脚**：客户表现出明确购买意向（问怎么下单、问发货、反复确认同一款）时，
  主动给一句基于事实的行动引导（如"今天下单当日发"），并用已有保障（退换政策）
  降低决策压力。**不得虚构紧迫感**——库存告急、限时优惠这类说法只有事实里有才能说。
- **售后·先归类再处理**：给方案前先把问题归入 质量问题 / 物流问题 / 漏发错发 /
  使用问题 四类之一，按类给对应路径；归不进时用一个封闭问题确认归类。
- **情绪分工边界**：强情绪消息（投诉/维权/曝光类词）由 `intent.Classify` 判为
  `Emotional`，`TriageOn` 下不进 AI、直接最高优先级转人工——这是既有设计且正确。
  售后策略的"安抚"只服务温和负面情绪这一段；真正激烈的客户归人，不归策略文本。

每段结尾统一附加同一句边界声明：

> 以上策略只决定表达方式，不改变可陈述的内容；数值、政策与承诺一律以"确定性事实"为准。

**没有这句，"展示漂亮数据"会直接撞上提示词规则 4（不要编造具体数值），
本体校验和断路器会开始拦截自家的销售话术。**

### 3.4 注入

- `difyapp.ContextVariables` 增加 `{Name: "scene_context", Label: "应答策略"}`。`WithContextVariables` 会自动补声明。
- 提示词模板在开场白之后、`【本业务确定性事实】` 之前插入一行 `{{scene_context}}`（不加固定标题，标题在策略文本里）。
- 回答规则追加第 6 条：*上述应答策略只影响表达方式与提问顺序，不改变可陈述的内容；任何数值、政策与承诺仍以"确定性事实"为准。*
- 位置理由：策略属于人格设定，放最前；事实优先级属于约束，留最后——模型对最后出现的约束遵守度更高，现有五条规则位置不动。
- `prepareAIContext` 按模式填 `ac.inputs["scene_context"]`。

### 3.5 模式开关

新增 `SCENE_MODE=off|shadow|on`，默认 `shadow`，解析函数与 `guardrail.ParseTriageMode`
同构（空值取默认，无法识别的值直接报错而不是静默回退）。

| 模式 | 分类 | 写粘性 | 注入 |
|---|---|---|---|
| `off` | 否 | 否 | 否 |
| `shadow` | 是 | 是 | **否** |
| `on` | 是 | 是 | 是 |

`shadow` 行为零变化，可安全开在任何环境，用来回答"如果开了会怎么分"。

新增指标 `router_scene_classified_total{stage, reason, source, mode}`，
`source ∈ {message, inherited}`，用来区分"这条消息自己判出来的"和"从会话继承的"。

## 4. 明确不做

- **不改经验回流**。`submitExperience` 的 success 判定仍是 guardrail 置信度。改它依赖满意度信号，而那条链路目前是断的（`doc/known-defects.md` D1）。
- **不碰 acest**。经验库当前无租户维度（`bridge.AcestConfig` 只有 BaseURL+Token），在"1 账号 = 1 客户"模型下开启即跨租户泄漏。默认 `ACEST_KB_URL` 不设，保持禁用。
- **不下放给客户配置**。策略是平台内置代码，portal 不暴露编辑入口。
- **不复活 `[INTENT:]`**。它是个现成的、零额外延迟的模型级场景信号（`price_inquiry`/`comparison`/`purchase_intent`/`complaint` 正好对应场景轴），提示词加一行即可复活；但它随答复返回，只能修正**下一轮**的粘性场景，且会改变模型输出格式、需要独立验证对 claim 校验器的影响。列为自然第二期。

## 5. 分步实施

| # | 步骤 | 产出 |
|---|---|---|
| 1 | 分类器 | `intent/stage.go` + `stage_test.go`，纯函数无依赖，先把词表和歧义守卫测通 |
| 2 | 策略文本 | `pkg/difyapp/strategy.go`，含统一边界声明 |
| 3 | 模式开关 | `routing/scene.go` + `cmd/router/main.go` 接线 |
| 4 | 注入接线 | `difyConvSession.Stage`、`getDifySession`、`aiCallContext.stage`、`prepareAIContext`、`setDifyConvID` 回写 |
| 5 | 提示词模板 | `ContextVariables` 加项、模板插占位符、追加规则 6 |
| 6 | 回灌端点 | admin 新增 + 路由注册（见 §6） |
| 7 | 黄金语料 | `eval.Case` 加 `Stage` 字段 + `TestClassifyStage_GoldenCorpus` |
| 8 | 第三份拷贝 | 同步 `deploy/dify-preview/configure_apps.py` |
| 9 | 防分叉守卫 | `pkg/difyapp` 新增测试读 Python 文件断言变量名齐全 |
| 10 | 文档 | README 环境变量表、回灌步骤与"改模板必须回灌"这条约束 |

步骤 1 的重点用例：`退款政策是什么` / `支持7天无理由退货吗` / `保修多久` 必须**不是**售后；
`我买的杯子摔坏了` / `收到的货发霉了` 必须是售后；
`3150的和2999的哪个更值得买` 必须是售前且不被 `315` 碰撞误伤。

步骤 7 按现有 `Expect.Handoff *bool` 的"不填即不校验"惯例处理未标注的 case，
先只标注明确无歧义的，其余留空。

步骤 9 的理由：`pkg/difyapp` 存在的全部意义就是"一份拷贝防止只改一边"（见其包注释），
而 Python 那份至今在这条防线之外。测试读 `../../../deploy/dify-preview/configure_apps.py`，
断言 `ContextVariables` 里每个变量名都出现；文件不存在时 skip 而非失败。

## 6. 存量应用回灌（本方案的关键运维步骤）

改了模板，已存在的 Dify 应用**不会**自动更新。全仓库只有三个写入点：

1. 产品线创建时（`admin/internal/bridge/dify.go:186`）——只影响此后新建的产品线；
2. `PUT /api/v1/ai-config/:id/prompt`（`admin/internal/handler/ai_config.go:254`）——写入调用方传来的文本；
3. `cmd/setup_dify_workspaces` CLI——**对已 provision 的产品线直接跳过**（`main.go:108-113`），无法用于回灌。

因此新增超管专用端点：

```
POST /api/v1/ai-config/{productLineID}/prompt/reset
```

用 `difyapp.DefaultSystemPrompt(pl.Name)` 回灌，不接收请求体里的提示词。
复用已有的 `h.difyBridge.UpdateSystemPrompt` 调用路径和权限中间件（要求
`PermManageAIConfig`，并因"客户不可改"的定位收紧到 super_admin）。幂等，可重复调用。

批量回灌用运维循环调用该端点，不做批量端点——批量写外部系统失败一半时的语义
太难说清，逐个调用可以直接看到哪个产品线失败。

### 6.1 一个必须知道的前端陷阱

`portal/product-lines.html:885` 的 `promptChanged = !els.fPrompt.disabled` 意味着：
只要产品线绑了 Dify 应用，保存「AI 参数」弹窗（**哪怕只改了置信度阈值**）
就一定会把文本框里的当前内容 PUT 一次；而文本框初值是 `GetAppConfig`
从 Dify 实时读回的**旧** `pre_prompt`，不是 `DefaultSystemPrompt()`。

结果：变量声明被 `WithContextVariables` 补齐了，提示词文本却被原样写回，
`{{scene_context}}` 永远不出现。router 照常传值，Dify 照常接收（因已声明），
**模型永远看不到——无报错、无日志**。

这正是回灌端点必须存在的理由，也是验证环节必须去 Dify 控制台肉眼确认占位符的理由。

## 7. 验证

- 六个模块各自 `go build -q ./... && go vet ./... && go test -q ./...`（`unica/scripts` 不在 go.work，按既有约定跳过）。
- `stage_test.go` 表驱动全绿，尤其歧义守卫那组。
- `routing/router_wiring_test.go` 复用 miniredis + `fakeDify` / `fakeRoutes` / `fakeConvStore`，新增三条：
  - `on` 模式下 `fakeDify` 收到的 `inputs["scene_context"]` 非空，且售前/售后消息拿到的文本不同；
  - `shadow` 模式下该 key 不存在；
  - 粘性：第一条售后消息之后，第二条纯价格问句仍解析为售后。
- 端到端（WSL 演练环境复原，`cd /data/unica-dify && docker compose up -d`）：
  1. 回灌一个产品线的提示词；
  2. 在 Dify 控制台确认 `scene_context` 已出现在变量表**且**提示词里含占位符；
  3. 分别发一条售前和一条售后消息，从 router 日志确认注入的策略文本不同；
  4. **人工读两条答复**，确认语气和结构确实有差异，且没有出现本体之外的数值。
- 回灌端点负路径：对处于 advanced prompt mode 的应用调用应返回明确错误而非静默成功（`updateSystemPromptWithToken` 已有该守卫，确认它透传到 HTTP 响应）。

## 8. 风险

| 风险 | 缓解 |
|---|---|
| **策略与合规规则打架**——售前策略鼓励主动、有说服力的表达，现有规则 2/4 抑制它 | 每段策略末尾的统一边界声明 + 新增规则 6；验证环节强制人工读答复确认没有出现本体之外的数值。这是本方案唯一需要人眼把关的地方 |
| **场景误判**——规则法必然有误判 | 默认 `shadow` 先量化分布，确认误判率可接受再开 `on`；误判代价有界，错的只是语气不是事实 |
| **模板回灌遗漏** | 验证环节强制在 Dify 控制台确认占位符存在；回灌端点幂等可重复执行 |
| **Python 拷贝分叉** | 步骤 9 的守卫测试 |
| **售前"展示漂亮数据"半空转**——本体现在只有政策类事实（退货窗口、保修期），没有卖点类事实（销量、好评率、可对比参数），策略只能展示防御性数据 | 不属于本期代码范围，属于供数运营：给本体加"卖点"属性类，或引导客户把卖点文档传入知识库。在客户引导文档里写明"售前效果取决于你喂了什么数据" |

## 9. 目标覆盖度核对（2026-08-11）

对照原始目标（"把客户处理用户问题的技巧拿来借鉴……让 AI 的回答更智能化"）逐项核验：

| 目标 | 本期覆盖 | 说明 |
|---|---|---|
| 售前情绪上推一把促成交 | ✅（核对后补强） | 见 §3.3"临门一脚"；紧迫感话术受事实约束 |
| 把握模糊问题的本质 | ✅ | 封闭问题收敛 |
| 展示产品的漂亮数据 | ⚠️ 管道通、水源空 | 见 §8 末行，供数是运营问题不是代码问题 |
| 售后安抚客户情绪 | ✅（限温和段） | 强情绪归人工，见 §3.3 情绪分工边界 |
| 售后问题分类处理 | ✅（核对后补上） | 见 §3.3"先归类再处理"，原稿遗漏 |
| **从真人客服身上学**（"借鉴"的字面义） | ❌ 本期没有 | 三段策略是通用方法论。真正的借鉴有硬依赖链：D3（坐席回复落库，学习素材）→ D1/D2（结果信号，好坏标签）→ acest 租户隔离（防跨客户泄漏）→ 蒸馏。顺序不可颠倒。本期的价值是把注入通道建好——蒸馏出的技巧将来走的就是这条 `scene_context` 管道 |

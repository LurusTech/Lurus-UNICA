# Active Task: 配置面重构 阶段 0 —— 收敛重复落点（不改行为）

## Context
配置面重构的地基工程：把"同一件事有两个落点、靠注释约束同步"的五处收敛掉。
本阶段**不新增任何功能、不改变任何运行时行为**——后续所有"门户显示生效值"的改动
都建立在两边一致之上，在不一致的地基上做上层功能只会把偏差固化。
完整设计见 `doc/plan-config-surface.md`（六个阶段，本文件是阶段 0）。
预期结果：guardrail 只剩一份定义，三处无人写却仍被读的旁路消失，
并有一个对拍脚本长期守住"门户所示 == 运行时所用"。

## Critical Files
- `unica/pkg/guardrail/`（新建：GuardrailConfig 与默认值的唯一权威）
- `unica/admin/internal/tenant/aisettings/guardrail.go`（删本地副本，改 import）
- `unica/router/internal/guardrail/config.go`（删本地副本，改 import）
- `unica/router/internal/knowledge/`（死包，零外部引用，整包删除）
- `unica/router/internal/handoff/summary.go`（删 config_json.dify 覆盖读取）
- `unica/admin/internal/identity/tenants_onboarding.go`（Chatwoot 绑定定权威后删回填逻辑）
- `unica/admin/go.mod` / `unica/router/go.mod`（新增对 pkg 的 require）
- `tmp/config_parity.py`（新建：五线对拍）

## Step-by-Step Plan
- [x] 1. 新建 `unica/pkg/guardrail`：把 `GuardrailConfig` 结构体、`DefaultGuardrailConfig()`、
      以及从 `product_lines.config_json` 解析与回填的共用逻辑收进去。默认值以 **router 那份为准**
      （它是运行时实际生效的一份），admin 的副本按字段逐条比对后丢弃。
- [x] 2. admin 与 router 改为 import 新包，删掉两处硬编码与两份重复 struct。
      注意两个模块的 `go.mod` 要加 require；`go.work` 已覆盖本地替换。
      完成标志：全仓库搜 `Must stay identical` 零命中。
- [x] 3. 删除死包 `unica/router/internal/knowledge`（已确认零外部 import）。
      它的分段规则用单换行、与 admin 实际生效的 `

` 矛盾，留着的唯一作用是
      被下一个人误接回主链路后静默破坏索引。
- [x] 4. 删除 `handoff/summary.go` 里 `config_json.dify.{base_url,api_key}` 的覆盖读取路径。
      该键**没有任何写入方**，但仍在被读——一旦有人手工改库就会静默覆盖列里的凭证，
      而门户与开户流程都看不见。`product_lines` 的列是唯一权威。
- [x] 5. Chatwoot 绑定定权威：`config_json.chatwoot` 为准。普查 `chatwoot_account_id` 列的
      全部读取方后二选一——改为派生 / 直接删列；随后删掉 `ensureChatwoot` 里为两者不一致
      而写的那段回填修复逻辑。
- [x] 6. 写 `tmp/config_parity.py`：对五条产线，比对**门户 `GET /ai-settings` 返回值**
      与**按新 pkg 逻辑从库内 config_json 计算出的生效值**，逐字段核对。
      这是本阶段的长期守卫，不是一次性脚本。
- [ ] 6b. **D18 的根治**（新增，优先于第 7 步）：判定层遇到 `answer == ""` 必须转人工，
      不得把空字符串投递给客户。参数调到 4096 只是把触发概率压低，
      问题更长、知识库更大时还会撞上，而它是**静默**失败。
      同时按 D19 给断言层加一条无条件前置检查：非转人工的用例，答复不得为空。
- [x] 7. 验证：`pkg` / `admin` / `router` / `gateway` 四模块 `go test ./...` 全绿；
      对拍 5/5；抽 XDYX 与 MegaStore 各跑一次金标，确认与阶段 0 之前的基线**无差异**
      （本阶段任何分数变化都是回归，不是改进）。

## Current Status
- [x] 阶段 0 完成（2026-08-23）。6b 未做，见下。

### 落点与计划的差异

**第 6 步改用 Go 而非 Python。** 计划写的是 `tmp/config_parity.py`，但 Python 版
必须重新实现一遍回填规则——那就是造出第三份副本，恰是本阶段要消灭的东西。
改成 `unica/router/cmd/configparity`：一侧直接调 `pkg/guardrail.Load`
（消息链路调的同一个函数），另一侧调门户 `GET /ai-settings`，中间不夹任何重写。
**10 个租户全部一致**（含 5 条历史 DrillCo）。

**第 5 步修正了判断：Chatwoot 那一列不删。** 盘点把它归成"冗余双写"，
但读代码后发现它有真实读取方——销户时用它定位要删除的 Chatwoot 账户，
且带 config_json 回退与偏移修复。删列要迁移、要改 INSERT，收益只是整洁。
**但里面有一个真问题已修**：`chatwootAccountOf` 原先**优先读列**，
而写入顺序是先块后列——会滞后的恰恰是列。列一旦陈旧，销户会删掉另一个
活着的租户的工作台，同时把该删的留成孤儿。已改为块优先、列兜底，
代码遵循的权威与设计声明的权威从此一致。

### 验收结果

| 项 | 结果 |
|---|---|
| 五模块 `go build` | 全过 |
| 四模块 `go test` | 全绿 |
| `pkg` 模块 | **1 条先于本阶段就红的用例**，见下 |
| 配置对拍 | 10/10 一致 |
| MegaStore 金标 | 24/24，0 回归（较基线 +1） |
| XDYX 金标 | 首跑 25/29「2 回归」，复跑 27/29 **0 回归** |

XDYX 首跑那 2 条（xdyx-02、xdyx-12）复跑即消失、且换成了另一条通过
（xdyx-08），失败集在漂移而非稳定回归——符合已记录的 XDYX 抖动区间。
本阶段对 router 判定路径是恒等变换（别名 + 同一份逻辑），唯一行为差异是
`blocked_topics` 由 nil 归一为 `[]`，对评估器无影响。

### 先于本阶段就存在的红灯（未处理）

`pkg/domain` 的 `TestSeedOntologiesMatchCannedResponses` 失败：上个增量把
采集清单写进了 `deploy/config/ontology/{freshmart,megastore,techzone}.yaml`
（D8 的权宜之计），而 `canned_responses.yaml` 没跟着更新，该测试把两者钉在一起。
已确认与本次改动无关——这些属性在 HEAD 版本的 YAML 里根本不存在，是工作树里新增的。
**不在阶段 0 范围内**：它的根治是 D8（给三条零售线补建数据集、把清单挪回知识库），
属阶段 3。在那之前这条会一直红。

## 后续阶段（不在本增量内，见 doc/plan-config-surface.md）
1. **阶段 1 补缺口**：`holding_message`、`survey.*` 补写入接口与表单；门户加"当前模型/供应商"只读卡片。
2. **阶段 2 提示词权威源回收**：本地版本表 + publish/rollback，Dify 降为投影，出"存量租户落后于模板"差异视图。解 D16。
3. **阶段 3 模型与检索权威源回收**：模型选择、temperature/max_tokens、检索方式、分段规则收进门户；顺带修 `provisionDifyLine` 的早退条件。解 D8。
4. **阶段 4（可不做）**：`INTENT_TRIAGE`/`SCENE_MODE`/`ONTOLOGY_ENABLED`/`DIFY_INDEXING_TECHNIQUE` 从 env 下放。
5. **阶段 5（可不做）**：租户自带模型 key（平台代持方案）。

## 已定的前置决策（2026-08-23）

**模型全平台唯一，租户不可选。** 不论租户在界面上设置什么，生效的只有一个模型。
当前定为 `openai_api_compatible / deepseek-v4-flash`，temperature 0.3、max_tokens 1024。
刻意选一个不强的模型：强模型会用推理能力盖住提示词、检索、本体这三层的缺陷、
让金标虚高，弱模型把缺陷逼到台面上。跑分是为了发现问题，不是拿好看的数字。
因此门户上模型相关的一切都是**只读展示**——看得见，改不了。

环境已拉平（`tmp/unify_model.py`，先 dry-run 后 `--push`）：XDYX 与 AJYJ 从
`deepseek/deepseek-chat` + Dify 默认参数改到目标模型，另三条线本已一致。
写后已核验提示词（1498~1503 字符、含 `[HANDOFF:]` 与规则 8/9）与数据集绑定均未被
`model-config` 的整体覆盖冲掉。回退值已知：`deepseek/deepseek-chat`、`{"stop":[]}`。

**但统一是个状态，不是一次操作。** 开户流程创建 Dify 应用时不写 model 字段，
新租户会落到工作空间默认值上、重新漂开。该强制项已排进阶段 1。

## 基线已重冻（2026-08-23，已完成）

新基线 `unica/router/testdata/golden/baseline-v4flash-unified.json`，
口径 `-inject-facts`（与历史基线一致）。旧的两份基线保留不覆盖。

**118 / 125（94.4%），空答复 0 条。**

| 产线 | 分数 |
|---|---|
| AJYJ | 24/24（100%）|
| FreshMart | 23/24 |
| MegaStore | 23/24 |
| TechZone | 22/24 |
| XDYX | 26/29 |

对旧基线（111/125，混合模型 + max_tokens 1024）：旧过新挂 3 条
（tech-01 / xdyx-01 / xdyx-08），旧挂新过 10 条。

### 过程中剥出两条缺陷，均已登记

第一次重跑得 109/125，其中 **18 条答复完全为空**。查明是
`max_tokens: 1024` 不够走完该模型的推理，生成在"还在想"时被截断，
Dify 返回空串而非半句话，链路把它当正常答复照发给客户（**D18**）。
实测真实需要 386~2021 tokens，2048 仍会掐掉最长的一条，故五线统一改为 4096
（`tmp/unify_model.py`）。改后同样的问题不但有答复，答案还是对的——
这批失败与模型能力无关。

那 18 条里有 **6 条被金标判为通过**：它们的断言只有否定式，
空答复天然满足（**D19**）。其中 ajyj-19 是 `handoff=false` 的纯静默通过。

### 剩下 7 条失败里有 2 条是断言误杀

`xdyx-08` 答"责任也**属于公司一方**"、`xdyx-11` 答"责任归属**在公司一方**"，
结论都对，但断言只收 `由公司/公司承担/我们承担/公司责任` 这几个字面量，
判失败。**裁定（2026-08-23）：断言不放宽。** 这两条记为**模型未达到口径**，
不是断言写错。责任归属是要发给客户、也要给坐席看的结论，措辞必须收敛到
一组确定说法，"属于公司一方"这种松散表述不算达标。放宽断言等于把口径要求
让渡给模型的即兴措辞，今天放宽一个词，明天就测不出东西。
两条在新基线里保持 false，要修就修提示词或知识库，不修测试。

另外 5 条是真漏：fresh-02 漏"拍照"、mega-19 该采集却只给了热线、
tech-01 漏"未拆封"、tech-13 漏"分期"、xdyx-01 漏"24小时"。

# 设计方案：本体约束驱动的应答事实化（Ontology Grounding）

> 状态：设计稿，待评审。`doc/active_task.md` 当前被"门户产品线管理"占用（第5步待实测），
> 本方案批准后再转正为 active_task。两者分属不同模块（admin/portal vs router），可并行。

## 1. 要解决的问题（均已在代码中验证）

| # | 缺陷 | 位置 | 后果 |
|---|---|---|---|
| P1 | 置信度 = 检索相似度均值，与回答正确性无关 | `internal/routing/confidence.go:13` | 幻觉零拦截能力 |
| P2 | 产品线政策互相冲突，无机制阻止串档 | `deploy/config/canned_responses.yaml` | 错误承诺 → 客诉 |
| P3 | 关键词子串匹配客户原文，且在调用 Dify **之后**评估 | `guardrail/evaluator.go:60`、`router.go:453→:528` | 咨询型问题被误转人工；白付一次 LLM |
| P4 | handoff 分支无差别以 `Success:false` 回写经验库 | `router.go:561-568` | 正确答案被当失败样本蒸馏，学习闭环反向 |
| P5 | 无任何真实答案正确率指标 | 全局 | 改动无法度量，优化靠感觉 |

P2 的具体形态：

| 产品线 | 无理由退货 | 前置条件 | 质保 |
|---|---|---|---|
| MegaStore | 7 天 | — | 1 年 |
| FreshMart | **不提供** | 仅质量问题 24h 内，需拍照 | — |
| TechZone | 15 天 | **未拆封** | 手机 12 月 / 配件 6 月 |

同一问句三种答案，其中一种是"该政策不存在"。检索分数无法区分。

## 2. 设计原则

1. **确定性事实不进概率通道**。政策/参数/质保这类有唯一正确答案的信息，由本体提供，
   LLM 只负责组织语言，不负责记忆数值。
2. **闭世界**。本体未声明的属性 = 该产品线不存在该政策，并**显式否定**。
   开放世界假设（"没说≠没有"）在客服场景等于放任幻觉。
3. **Fail-open 优先于 fail-safe**。沿用 acest 集成已确立的哲学：新机制故障时退回当前行为，
   绝不阻断主链路。
4. **提示词不含事实**。Dify 提示词里只有 `{{facts_context}}` 变量引用，
   具体数字全部来自 router 注入。改政策改本体，不碰 Dify 控制台。
5. **先度量后拦截**。任何拦截逻辑先跑 shadow mode。

## 3. 架构

```
入站消息
   │
   ├─① 前置分诊（router 侧，调 LLM 之前）─────────────────────
   │    事务型「我要退款」/ 情绪型「投诉曝光」→ 直接转人工，不调 LLM
   │    其余全部放行（宁可多调一次，不可误杀咨询）
   │
   ├─② 本体检索 → facts_context ──────────────────────────
   │    按产品线加载本体，抽取相关属性 + 显式否定清单
   │
   ├─③ Dify 调用（现有）───────────────────────────────
   │    inputs: experience_context / knowledge_context / facts_context
   │    答案内嵌 [FACT:property=value] 标签（复用现有 [INTENT:] 机制）
   │
   ├─④ 本体校验 ────────────────────────────────────
   │    逐条 claim 对本体检查：属性存在性 / 值域 / 单值性 / 类互斥
   │
   └─⑤ 护栏决策 ────────────────────────────────────
        置信度 = f(检索信号, 校验结果)；违规 → handoff(reason=claim_conflict)
```

### 3.1 为什么用标签而不是 JSON（重要决策）

原方案要求 Dify 返回 `{answer, claims[], ...}` JSON。**否决**，理由：

- `bridge/dify.go:97` 用 `response_mode: blocking`，返回的 `Answer` 是纯文本。改 JSON 要在
  Dify 应用编排里重配输出格式，逐条产品线改且无法回滚。
  （当时 `dify_admin.go` 的 `UpdateAppConfig` 对真实 Dify 0.15.3 还返回 400，连批量改都做不到；
  该缺陷后来已修，但这条决策的其余理由不受影响。）
- JSON 无优雅降级：模型吐坏 JSON → 整条消息作废，客户收不到任何东西。
- 团队已有 `[INTENT:xxx]` 约定（`marketing/detector.go:12`），提示词里加一行同构指令成本极低。

改为扩展标签词表：

```
[FACT:return_window_days=15]  [FACT:warranty_months=12]
```

- 提示词增量：一行。
- 降级路径：模型不吐标签 → claims 为空 → 校验跳过 → 完全退回当前行为。
- 剥离逻辑复用 `detector.go` 的 `ReplaceAllString` 模式，客户看不到标签。

### 3.2 砍掉：本体直答车道

原方案的第三车道"属性命中则本体直答，LLM 只做措辞"——**否决**。措辞仍需调 LLM，
省不下延迟；而一旦为了省延迟改成模板拼接，就退回关键词客服，牺牲自然语言能力换来的
正确性，本体注入 + 强制引用已经能拿到。

替代：facts_context 里的属性，提示词要求**必须原样引用、不得改写或推算**。

### 3.3 砍掉：本体作为 SSOT 生成 canned responses / 知识库文档

原方案主张从本体生成 `canned_responses.yaml` 与 Dify 知识库政策文档，防止三处漂移。
**推迟**：引入生成器与同步管线，且 Dify 文档上传本就是手工流程（README 明确不重造 Dify UI）。

替代（同等收益，5% 成本）：写一个 `go test` 做一致性断言——解析本体 + `canned_responses.yaml`，
两者事实不一致即测试失败。防漂移目的达成，不引入任何管线。

## 4. 分期

### 第 0 期：不引入新架构的止血（约 1 天，独立可上线）

目标：修 P3/P4，并用最低成本吃掉大部分 P2。

- [ ] 0.1 意图分层取代子串匹配。`guardrail` 新增 `intent.go`：
      - 情绪型（`投诉/曝光/315/工商/律师/差评`）→ 立即 handoff
      - 事务型（第一人称意愿动词 + 动作名词：`我要退款`/`帮我改地址`/`申请退货`）→ handoff
      - 咨询型（含疑问标记 `什么/几天/多久/怎么/如何/吗/?` 且无意愿动词）→ 放行给 AI
      - 判不准 → 放行（宁可多调一次 LLM，不可误杀）
- [ ] 0.2 纯客户侧规则前置到 Dify 调用之前（`router.go:428` 之前），命中直接 handoff，省一次 LLM。
- [ ] 0.3 `submitExperience` 按 `evalResult.Reason` 区分：仅 `low_confidence` 记 `Success:false`；
      `keyword_match`/`blocked_topic` 是策略拦截，**不回写**（不是质量信号）。
- [ ] 0.4 建立黄金测试集 `test/golden/{megastore,freshmart,techzone}.yaml`，
      每线 20 条（覆盖 7 个问题类目 + 跨线陷阱问题），标注期望事实。
      配 `go test` 离线跑分，输出正确率。**这是后续所有优化的唯一可信标尺。**
- [ ] 0.5 各产品线 Dify 提示词加一段固定政策事实（手工，3 次控制台编辑）。

验收：黄金集正确率基线建立；`router_guardrail_decisions_total{reason}` 中
`keyword_match` 占比显著下降；`DifyCallDuration` 在 handoff 路径上归零。

> 第 0 期做完若正确率已达标，第 1/2 期可以不做。这是刻意设计的止损点。

### 第 1 期：本体落地 + shadow mode（约 3 天）

- [ ] 1.1 `deploy/config/ontology/{megastore,freshmart,techzone}.yaml`，
      首期只覆盖 3 个属性：`return_window_days` / `warranty_months` / `return_precondition`。
      内容从 `canned_responses.yaml` 反向抽取，一次性人工核对。
- [ ] 1.2 `unica/router/internal/domain/`：`ontology.go`(类型) / `loader.go`(加载+缓存，仿 routeCache)
      / `render.go`(facts_context 渲染，含显式否定清单)。
- [ ] 1.3 migration `011_domain_ontology.sql`：`ontology_versions`(含 active 唯一索引)、`claim_violations`。
- [ ] 1.4 `router.go:437-445` 旁注入 `inputs["facts_context"]`；Dify 提示词引用该变量
      （替换第 0 期硬写的政策段，此后改政策不再碰控制台）。
- [ ] 1.5 `detector.go` 扩展 `[FACT:k=v]` 解析；`domain/validator.go` 实现校验；
      **shadow mode**：只写 `claim_violations` 表 + `router_claim_violations_total{property,kind}` 指标，
      **不改变 send/handoff 决策**。
- [ ] 1.6 一致性测试：本体 vs `canned_responses.yaml`。

验收：跑满一周，`claim_violations` 有数据，人工抽样确认违规判定准确率 >90%（误杀率可接受）；
黄金集正确率相对第 0 期基线提升。

### 第 2 期：开启拦截 + 置信度重构（约 2 天）

- [ ] 2.1 `calculateConfidence` 重构为组合信号：检索信号 × 本体校验结果，
      校验失败直接压至 0。
- [ ] 2.2 `guardrail.Evaluate` 增加 `claim_conflict` 决策路径；handoff 事件携带违规明细供坐席参考。
- [ ] 2.3 按产品线灰度开关（`product_lines.config_json.guardrail.enforce_ontology`），
      先开一条产品线观察 3 天再全开。

验收：客诉中"错误承诺"类目下降；`claim_violations` 增长率下降（模型被 facts_context 纠正）。

## 5. 本体约束原语（YAML → 校验语义）

| 原语 | YAML | 拦截的错误 |
|---|---|---|
| `rdfs:domain` | `properties.*.domain` | 张冠李戴的属性（FreshMart 谈"无理由退货"） |
| `rdfs:range` | `properties.*.range.{enum,min,max}` | 数值编错（TechZone 说 30 天） |
| `owl:FunctionalProperty` | `functional: true` | 同一答复自相矛盾（手机保修既 12 又 6 个月） |
| `owl:disjointWith` | `disjoint: [[Phone, Accessory]]` | 类混用（按配件规则回答手机） |
| `owl:minCardinality` | `min_cardinality` | 漏说前置条件（只说 15 天，不说未拆封） |
| `rdfs:subClassOf` | `subclass_of` | 层级属性继承 |
| **闭世界（反 OWL）** | `closed_world: true` | 沉默式幻觉 |

**明确不做**：推理机、传递闭包、等价类推导、本体自洽性检查。
层级两层、断言几十条，人肉可验；需要时用 `go test` 遍历，比挂 reasoner 便宜两个数量级。
**约束用于校验，不用于推理**——这是本方案与真 OWL 的分界线。

## 6. 度量

现有指标全是代理指标（检索命中率、置信度分布），**无法证明回答质量**。补三层：

1. **离线真值**：黄金测试集正确率（第 0 期建立）。唯一可信标尺，每次改动前后对比。
2. **在线代理**：`claim_violations` 表 → 按 property/kind 聚合的事实错误率。覆盖面受限于
   模型是否吐标签，只能看趋势不能看绝对值。
3. **免费人工标注**：handoff 之后坐席的首条回复，若与被抑制的 AI 答案矛盾，即为一条真值样本。
   可离线挖掘，用于扩充黄金集。

新增 Prometheus 指标：
```
router_intent_classified_total{intent}          # 分诊分布
router_claim_violations_total{property,kind}    # 事实违规
router_facts_injected_total{product_line}       # 本体命中率
```

## 7. 风险

| 风险 | 缓解 |
|---|---|
| 提示词只能手工改，3 线易漂移 | 提示词只放 `{{facts_context}}` 变量引用，不含具体数字；改本体不碰控制台 |
| 拦截误杀导致大面积转人工 | 第 1 期强制 shadow mode；第 2 期按产品线灰度 |
| 本体谁维护（运营不写 YAML） | 首期仅 3 个属性、9 条断言，从现有 canned responses 抽取；后续若需扩展再考虑门户编辑页 |
| 模型不遵守 `[FACT:]` 约定 | 无标签 = 无 claims = 退回当前行为，零损失；命中率作为指标观察 |
| 范围膨胀成"重写客服引擎" | 已砍本体直答车道与 SSOT 生成；每期独立可上线，第 0 期后可随时叫停 |

## 8. 明确不做

- OWL 文件 / 推理机 / RDF 三元组存储
- 为 pydantic 引入 Python 服务（本项目纯 Go，等价物是 struct tag + JSON Schema）
- 完整 JSON 结构化输出（见 3.1）
- 从本体生成 canned responses 与知识库文档（见 3.3，降级为一致性测试）
- 触碰 gateway / admin / portal（本方案全部落在 `unica/router`）

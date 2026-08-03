# Active Task: 回答质量标尺 + 分诊与学习闭环修复（本体化改造第 0 期）

## Context
当前系统没有任何能说明"回答对不对"的指标——置信度只是检索相似度均值（`confidence.go:13`），
调阈值、改提示词全靠感觉。本增量先建立可复跑的正确率标尺，再修两处已验证的链路缺陷
（咨询型问题被关键词误转人工、正确答案被当失败样本回写经验库），并用标尺证明改善。
**本期不引入本体、不改 Dify 提示词、不碰 gateway/admin/portal。**

## 设计决策
- **标尺优先**：先建黄金测试集并跑出基线，再动任何逻辑。没有基线的改动无法验收。
- **打分不用 LLM 评委**：每条用例标注 `must_contain_any` / `must_not_contain` 字符串断言即可
  自动判定，零额外成本、结果稳定。
- **分诊宁松勿严**：只拦截高置信度的事务型/情绪型意图，判不准一律放行给 AI。
  误杀咨询问题的代价（自动解决率下降）高于多调一次 LLM。
- **回写语义修正**：`keyword_match`/`blocked_topic` 是策略拦截，不是质量信号，不回写经验库；
  仅 `low_confidence` 记 `Success:false`。
- 跑分工具做成独立 CLI（需连真实 Dify），不进 `go test`，避免 CI 依赖外部服务。

## Critical Files
- unica/router/testdata/golden/{megastore,freshmart,techzone}.yaml（新建：每线 20 条用例）
- unica/router/cmd/evalset/main.go（新建：离线跑分 CLI，输出各线正确率）
- unica/router/internal/intent/classifier.go + classifier_test.go（新建：意图分层，纯离线可测）
- unica/router/internal/routing/router.go（分诊前置到 Dify 调用之前；`submitExperience` 按 reason 分流）
- unica/router/internal/guardrail/evaluator.go（接收 intent 结果，替代 HandoffKeywords 子串匹配）
- unica/router/internal/metrics/（新增 `router_intent_classified_total{intent}`）

## Step-by-Step Plan
- [x] 1. 黄金测试集完成：3 产品线 × 20 条 = 60 条，覆盖 7 类问题 + 跨线陷阱 + 防编造 + 意图边界对。
      新增字段 `must_deny`（闭世界否定）与 `must_not_match`（正则逃生舱）；每条带 `intent` 标注，
      供分类器离线复用。语料本身由 `internal/eval` 单测在每次 `go test` 时校验。
- [x] 2. `internal/eval`（断言引擎 + 报告聚合 + 基线对比）与 `cmd/evalset`（跑分 CLI）完成。
      CLI 复用线上判定链路：同一次 Dify 调用 → `routing.CalculateConfidence` →
      `guardrail.Evaluate` → `marketing.DetectIntents` 剥离标签后打分，测的是客户实际收到的文本。
- [ ] 3. **跑出基线并记录**（阻塞：需连真实 Dify + POSTGRES_URL，本机够不到）
- [x] 4. `internal/intent` 完成：三分类规则式分类器。60 条黄金用例意图标注全部命中；
      另有专项测试证明旧关键词表会误杀的 4 条咨询问题现已放行。
      **提前于第 3 步执行**：新包未接入主链路，不影响基线可比性。
- [x] 5. 已接入 router，并加 `INTENT_TRIAGE` 三档模式（`off`/`shadow`/`on`，默认 `shadow`）解除对第 3 步的依赖：
      `shadow` 只记录指标、不改任何判定，因此本次改动可以先上线；等 Dify 就绪后用
      `evalset -intent-triage off` 与 `-intent-triage on` 在同一部署上跑出前后对照，
      **基线不必抢在改动之前采集**。分诊命中时在调用 Dify 之前 handoff（`handoffBeforeAI`）；
      `on` 档下 `HandoffKeywords` 退役，`BlockedTopics` 与置信度阈值在所有档位保留。
- [x] 6. `submitExperience` 改由 `guardrail.IsQualitySignal(reason)` 把关：
      仅 `low_confidence` 记失败样本；关键词/屏蔽话题/分诊拦截一律不回写。
      分诊前置 handoff 也不提交（根本没有答案可评）。
- [ ] 7. 验证：`go build ./... && go vet ./... && go test ./...` **已全绿**；
      待 Dify 就绪后补：evalset 前后对照 + 观察
      `router_intent_classified_total{class,mode}` 与 `router_guardrail_decisions_total{reason}`。

## Out of Scope
- 本体 YAML / facts_context 注入 / claim 校验（第 1 期，见 `doc/plan-ontology-grounding.md`）
- 置信度算法重构（第 2 期）
- 修改 Dify 应用提示词（第 1 期统一改为引用 `{{facts_context}}` 变量，本期不动）
- `dify_admin.go` UpdateAppConfig 400 bug（第 1 期消息链路增量根修）

## 上一增量遗留（门户产品线管理，代码已交付部署）
- [ ] 用户实测门户流程；测试数据"默认产品线"已带真实绑定，可保留当样例
- 完整记录见 git 历史中本文件的上一版本

## Current Status
- [ ] Ready for Review — 代码部分（第 1/2/4/5/6 步）全部完成，`go build ./... && go vet ./... && go test ./...` 全绿。
      默认 `INTENT_TRIAGE=shadow`，对现有部署零行为变更，可以直接上线。

### 待 Dify 就绪后执行（第 3 + 7 步，工作目录 `unica/router`）
```
# A：旧行为基线
POSTGRES_URL=... go run ./cmd/evalset -intent-triage off -verbose \
    -save-baseline ../../doc/eval-baseline.json

# B：新行为对照
POSTGRES_URL=... go run ./cmd/evalset -intent-triage on -verbose \
    -baseline ../../doc/eval-baseline.json
```
前提：`product_lines.name` 必须与语料里的 MegaStore / FreshMart / TechZone 一致，
且三条产品线均已 provision（有 dify_api_key）。也可用
`-line TechZone -dify-base-url ... -dify-api-key ...` 单线试跑，绕开数据库。

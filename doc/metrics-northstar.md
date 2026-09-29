# 北极星指标记录

三个互相制衡的指标（见 doc/goal-evolution.md）：自助解决率守价值，合规误报率守信任，
事实过期数守真实。每轮迭代结束把变化追加到底部表格，不重写历史行。

## 口径定义与测量说明

看板接口：指标①②③按产线过滤后一次取数见 `GET /api/v1/tenants/{id}/quality/northstar?days=N`
（样本不足时对应字段返回 null，并置位 `self_service_insufficient` / `compliance_insufficient` /
`fact_stale_insufficient`，不返回一个假装精确的百分比）。每个本体版本发布后的质量、以及与上一版本
的对比见 `GET /api/v1/tenants/{id}/quality/versions?window_days=N`。两者均已在 portal 的
质量复核页（`portal/violations.html` 顶部）与确定性事实页（`portal/ontology.html` 版本历史）展示。

### 1. 自助解决率 ↑
**定义**：不转人工且用户未追问纠错的会话占比。

**当前可测近似**：`1 - (窗口内有转人工决策的会话数 / 窗口内会话数)`。
- 转人工决策数以 `handoff_events` 表为准（2026-08-13 起记录，决策点落库）。
  **不要用 `conversations.handoff_count`**：它由 handoff 消费端在 Chatwoot 建会话后回写，
  租户未接 Chatwoot 时整条链路静默丢失（实测 68 个历史会话该列全 0，而同期确有转人工发生）。
- "未追问纠错"暂不可测：需要会话内追问检测，属后续观测能力，本指标先用不转人工近似。

查询（按产线过滤；此前的版本漏了这一层，会把全平台混进单租户的数字里）：
```sql
select 1.0 - (select count(distinct conversation_id)::numeric from handoff_events
              where product_line_id = $1 and created_at >= now() - interval '30 days')
         / nullif((select count(*) from conversations
              where product_line_id = $1 and created_at >= now() - interval '30 days'), 0);
```

### 2. 合规误报率 ↓
**定义**：被拦内容中，人工复核判定为合法（`false_positive` 或 `ontology_wrong`）的占比。
分母只取 `enforced = true` 且已复核的违规行——shadow 行没有拦过任何人，不算误报也不算命中。

按产线过滤（同上，此前的版本没有）：
```sql
select count(*) filter (where review_status in ('false_positive','ontology_wrong'))::numeric
       / nullif(count(*), 0)
from claim_violations
where product_line_id = $1 and enforced and review_status <> 'pending';
```

辅助信号（`GET /tenants/{id}/violations/stats`）：
- `dead_constraints`：窗口内零命中的已声明约束——疑似死约束，但"安静"也可能因为它有效，
  需结合注入是否开启判断；
- `undeclared_properties`：模型宣称而本体未声明的属性——候选新约束，按命中频次排序。

### 3. 事实过期数 ↓
**定义**：超过 N 天未复核确认的租户事实条数。N = 90。

**当前可测近似**：激活版本发布超过 N 天的租户数（本体没有逐条事实的复核时间戳，
版本发布/回滚是目前唯一的"确认"动作）。逐条粒度留给后续轮次按需建设。

按产线过滤（同上）：
```sql
select count(*) from ontology_versions ov
where ov.product_line_id = $1 and ov.active and ov.created_at < now() - interval '90 days';
```

### 4. 本体自演化：可削减类转人工率 ↓（守门）
**定义**：留出集（按会话编号 SHA-256 冻结的 30%）上，以线上同一个 Dify 应用重答，
转人工原因属于可削减类（`low_confidence` / `empty_answer` / `model_requested` /
`claim_conflict`）的轮次占比。同时必须满足：强制类转人工逐条不少、错误作答（a）不上升。
口径与门控见 `unica/router/internal/replay`。

**数据前提**：客户机已跑迁移 023 并打开 `evolve_capture`，且经 owner 批准做了脱敏导出
（`doc/evolve-export.md`）。**演练数据只验链路，不报本指标。**

取数（只写命令，不写数值；数值只在真实导出的留出集上报告）：
```bash
evolve split -in export.jsonl -side holdout -out holdout.txt          # 冻结一次，提交 ID 列表
# 挖掘：只读训练侧（代码写死，不可选），产出候选本体、候选清单与聚类报告
POSTGRES_URL=... evolve propose -in export.jsonl -line <产品线> -ontology active \
    -top 1 -out candidates.json -cand cand.yaml -md mine.md
# 基线：当前激活版本（或 -ontology version:N 固定到导出窗口当时的版本）
POSTGRES_URL=... evolve eval -in export.jsonl -line <产品线> -ontology active \
    -split holdout -manifest holdout.txt -out base.json -md base.md
# 候选
POSTGRES_URL=... evolve eval -in export.jsonl -line <产品线> -ontology cand.yaml \
    -split holdout -manifest holdout.txt -out cand.json -md cand.md
evolve gate -baseline base.json -candidate cand.json -md gate.md -json gate.json   # 退出码 1 = 未过门
# 入队：A 级只有门过了、门判的是 candidates.json 这一版、且在留出集上，才写入待审队列；C 级工单直接入队
POSTGRES_URL=... evolve enqueue -candidates candidates.json -gate gate.json
```
基线报告里的 `可削减类转人工` 即本指标；（b）坐席参照不一致与（c）20 条人工抽检随报告输出。

**门的统计口径**（模型输出有随机性，单次"更低"不算数）：
- 成对精确符号检验：只看两边结论不同的轮次（基线可削减→候选不可削减记"改进"，反之记"退步"），
  单侧 p ≤ α（`-alpha`，默认 0.05）才算改善。5 改进 0 退步 p=1/32 过门；1 改进 0 退步 p=0.5 不过。
  报告另给成对自助法 95% 区间（只报告，不参与判定），可比轮次 < 30 时醒目提示样本量小。
- 强制类逐条不少；候选请求报错单列（不再混进"强制丢失"），有报错门就不过，重跑再判。
- 逐轮错误作答：候选答案在候选本体下有违规、而基线答案在同一本体下没有的轮次，一条就不过门
  （总数不变但违规换了位置也抓得到）；肯定否定项次数不得上升。
- 配置指纹：guardrail、校验模式、分诊、回放历史轮数、重复次数任一不同即不可比，门不过；
  确认后可 `-allow-config-mismatch` 放行。旧文件缺指纹只警告。
- `-repeat k`（默认 1）：每轮重答 k 次，启动前打印预计模型调用次数。可削减判定按多数（平局算可削减，
  两侧对称）；强制类要求 k 次全部转人工（pass^k）；报告给出"每次都交付且零违规"的 pass^k 估计。
  k>1 会成倍消耗模型调用，真实使用前先估算预算。

## 记录

| 轮次 | 日期 | 自助解决率 | 合规误报率 | 事实过期数 | 备注 |
|---|---|---|---|---|---|
| 0 | 2026-08-13 | 基线未定（handoff_events 自本日起累积；历史 68 会话无结构化转人工痕迹） | 样本不足（enforced 且已复核 n=1） | 0（四租户激活版本均 ≤8 天） | 观测层三件套上线：AJYJ 金标集 20 条入语料库；handoff_events 决策点落库+人工标注流；violations/stats 覆盖联结。下一轮起本表可给出真实数值 |

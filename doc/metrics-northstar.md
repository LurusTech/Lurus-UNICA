# 北极星指标记录

三个互相制衡的指标（见 doc/goal-evolution.md）：自助解决率守价值，合规误报率守信任，
事实过期数守真实。每轮迭代结束把变化追加到底部表格，不重写历史行。

## 口径定义与测量说明

### 1. 自助解决率 ↑
**定义**：不转人工且用户未追问纠错的会话占比。

**当前可测近似**：`1 - (窗口内有转人工决策的会话数 / 窗口内会话数)`。
- 转人工决策数以 `handoff_events` 表为准（2026-08-13 起记录，决策点落库）。
  **不要用 `conversations.handoff_count`**：它由 handoff 消费端在 Chatwoot 建会话后回写，
  租户未接 Chatwoot 时整条链路静默丢失（实测 68 个历史会话该列全 0，而同期确有转人工发生）。
- "未追问纠错"暂不可测：需要会话内追问检测，属后续观测能力，本指标先用不转人工近似。

查询：
```sql
select 1.0 - (select count(distinct conversation_id)::numeric from handoff_events
              where created_at >= now() - interval '30 days')
         / nullif((select count(*) from conversations
              where created_at >= now() - interval '30 days'), 0);
```

### 2. 合规误报率 ↓
**定义**：被拦内容中，人工复核判定为合法（`false_positive` 或 `ontology_wrong`）的占比。
分母只取 `enforced = true` 且已复核的违规行——shadow 行没有拦过任何人，不算误报也不算命中。

```sql
select count(*) filter (where review_status in ('false_positive','ontology_wrong'))::numeric
       / nullif(count(*), 0)
from claim_violations
where enforced and review_status <> 'pending';
```

辅助信号（`GET /tenants/{id}/violations/stats`）：
- `dead_constraints`：窗口内零命中的已声明约束——疑似死约束，但"安静"也可能因为它有效，
  需结合注入是否开启判断；
- `undeclared_properties`：模型宣称而本体未声明的属性——候选新约束，按命中频次排序。

### 3. 事实过期数 ↓
**定义**：超过 N 天未复核确认的租户事实条数。N = 90。

**当前可测近似**：激活版本发布超过 N 天的租户数（本体没有逐条事实的复核时间戳，
版本发布/回滚是目前唯一的"确认"动作）。逐条粒度留给后续轮次按需建设。

```sql
select count(*) from ontology_versions ov
where ov.active and ov.created_at < now() - interval '90 days';
```

## 记录

| 轮次 | 日期 | 自助解决率 | 合规误报率 | 事实过期数 | 备注 |
|---|---|---|---|---|---|
| 0 | 2026-08-13 | 基线未定（handoff_events 自本日起累积；历史 68 会话无结构化转人工痕迹） | 样本不足（enforced 且已复核 n=1） | 0（四租户激活版本均 ≤8 天） | 观测层三件套上线：AJYJ 金标集 20 条入语料库；handoff_events 决策点落库+人工标注流；violations/stats 覆盖联结。下一轮起本表可给出真实数值 |

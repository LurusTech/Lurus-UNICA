# Active Task: 产品进化第 0 轮 —— 观测层三件套

## Context
执行 doc/goal-evolution.md 第 0 轮：数据飞轮启动前先建观测层（金标回归集 / 约束命中分布 /
转人工原因落库与标注）。全部只读或旁路，不改动现有路由决策链路。探查结论：金标框架
（internal/eval + cmd/evalset）与违规复核流程已存在，缺 AJYJ 用例、聚合统计与转人工原因持久化。

## Critical Files
- unica/router/testdata/golden/ajyj.yaml（新增，第 4 份金标集）
- unica/router/internal/eval/goldenset.go、unica/router/cmd/evalset/main.go（只读确认，勿改断言语义）
- unica/router/migrations/018_handoff_events.sql（新增，旁路表）
- unica/router/internal/routing/router.go（publishHandoffEvent 处旁路落库）/ internal/state/repository.go
- unica/pkg/domain/store.go（violations 聚合查询）
- unica/admin/internal/tenant/quality/（stats 端点 + handoff 事件列表/标注端点）
- unica/admin/cmd/admin/main.go + router_test.go（路由注册与映射钉子）
- doc/metrics-northstar.md（新增，北极星指标记录，第 0 轮记基线）

## Step-by-Step Plan
- [x] 1. 读 goldenset.go / cmd/evalset/main.go：执行器无产线数量假设（凭 product_lines 表按名解析）；
       硬约束在测试侧——corpus 白名单（已加 AJYJ）+ intent 标签必须与分类器输出一致
- [x] 2. testdata/golden/ajyj.yaml 20 条已写：单轮事实/政策 + D10 单轮投影（轮6/7 负样本）+
       转人工 2 条 + 佣金五档陷阱 + denies 红线；eval/intent 单测一次通过
- [x] 3. migration 018 handoff_events 已写（含 annotated_* 三列与未标注部分索引）
- [x] 4. router publishHandoffEvent 旁路落库（handoffRecorder 接口，SetOntology 同 store 装配，
       nil 安全 + keyword 落 detail），wiring 单测 2 条通过；销户级联清单已补 handoff_events
- [x] 5. admin quality 新增 SignalsHandler：violations/stats（含 ontology 左连接 coverage、
       dead_constraints、undeclared_properties）、handoffs 列表/stats、annotate（audit action=review）；
       租户路由映射 + 钉子测试 4 条新增；signals_test 9 条全绿
- [ ] 6. 验证：五模块静默 build/vet/test 全绿；提交后备份业务库 → 应用 018 → 重启 router/admin →
       evalset 跑 ajyj.yaml 全绿 → 冒烟新端点（触发一次转人工，验证 handoff_events 落行、stats 可读）
- [ ] 7. doc/metrics-northstar.md 记第 0 轮基线（三指标当前口径与测量近似的诚实说明）；
       测试信息.md 登记新端点；清 active_task

## Current Status
- [x] In Progress —— 代码完成、五模块 vet/test 全绿；待提交后执行步骤 6 实机部署验证

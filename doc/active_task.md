# Active Task: 第 3 期——证据闭环与告警

## Context
违规证据可见、可复核、可回流；熔断/违规/令牌死信接入现有告警链路。

## Step-by-Step Plan
- [x] 1. 迁移 013（review_status 三态 + CHECK + 复核队列索引）+ pkg/domain
      违规读/复核方法（WHERE 构造纯函数化并单测；分页补 id 决胜键）
- [x] 2. admin violations handler（列表过滤分页 + 复核标注 + 审计 + scope）、
      mux 按子资源分派、/api/v1/violations/ 新入口、9 个测试
- [x] 3. portal/violations.html 复核队列 + index 卡片：待复核过滤、证据展开、
      一键三态标注、重开、分页、三种空态；mock 冒烟 51/51
- [x] 4. Grafana ontology-quality 面板（8 块，指标名逐一核对）+ 告警组
      unica-answer-quality（熔断跳闸 critical / 违规激增 warning 占位阈值 /
      令牌刷新耗尽 critical）+ gateway_token_refresh_exhausted_total 指标
- [ ] 5. 验证：`-race` 全绿；提交推送 CI；unverified 已登记（§3.5.3/3.5.4）
- [ ] 6. 汇报第 3 期

## Current Status
- [ ] In Progress（等 -race 与 CI）

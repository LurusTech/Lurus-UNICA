# Active Task: 第 1 期——判定链路整备（共同入口 / 拆分 / 测试 / 去重）

## Context
目标是把"能用、好用、单人兜底多公司"的产品化改造安全地铺开。本期不加新功能，
先消除三处会让后续每一期返工的结构性债：判定链路在 router 与 evalset 两处人肉同步
（已漂移：evalset 无熔断）、`callDifyAndPublish` 约 280 行且主链路零测试、
`config_json` 三个包各自解码无共享 schema、系统提示词两个二进制各存一份。

分期总览（后续各期）：2 本体与配置运营化（admin API + portal 页）；
3 违规证据闭环与告警；4 坐席附草稿/证据 + 售前分诊 + 渠道防呆（小红书优先）；
5 WSL 重部署 + DeepSeek 打通 + 黄金集演练。真实流量验证最后。

## Critical Files
- `unica/router/internal/routing/router.go`（callDifyAndPublish 拆分）
- `unica/router/internal/routing/judge.go`（新建：判定共同入口）
- `unica/router/cmd/evalset/main.go`（改用共同入口）
- `unica/router/internal/domain/config.go` / `internal/guardrail/config.go`（统一 config_json schema）
- `unica/router/internal/bridge/dify_admin.go` / `unica/admin/internal/bridge/dify.go`（提示词去重）
- `unica/router/internal/routing/router_test.go`（miniredis + Dify 打桩）
- `unica/router/internal/domain/ontology.go` / `store.go`（小重复清理）

## Step-by-Step Plan
- [x] 1. 抽出"判定一条回答"共同入口 `judge.go`，router 与 evalset 共用；
      evalset 两处差异（无熔断、校验封顶 shadow）在 `scoreCase` 显式声明。
      **过程中修掉一处被证伪的设计**：违规曾把置信度压到 0 走 `low_confidence`
      转人工，绕过熔断、破坏 shadow 契约、误记失败经验样本；
      现在冲突不降分，压制只走受熔断约束的 enforce 覆盖
- [x] 2. 拆 `callDifyAndPublish` → prepareAIContext / callDify / JudgeAnswer /
      recordJudgement / deliverJudgement；依赖字段接口化（含 typed-nil 防护）
- [x] 3. 主链路测试：miniredis + Dify 打桩，覆盖重复投递跳过、enforce 强制转人工
      （证据入 handoff 事件）、熔断跳闸后违规回答真的发出（bypassed 记账）
- [~] 4. `config_json` 统一类型 → **移到第 2 期**：schema 应由 admin 写路径的
      真实需求定形，现在做会二次返工
- [x] 5. 提示词模板去重 → `unica/pkg/difyapp`，router/admin 两个 bridge 共用
- [x] 6. 小清理：canonicalLabel 合并、sortedKeys 删除、store.go withTx 收敛
- [x] 7. 验证：build/vet/test 全绿；WSL 装 Go 首跑 `-race`
- [x] 8. CI：`.github/workflows/ci.yml`，Linux build+vet+`-race`（矩阵覆盖各模块）
- [ ] 9. 提交本期，向用户汇报

## Current Status
- [ ] In Progress（等 5/7/8 的执行结果回来后提交）

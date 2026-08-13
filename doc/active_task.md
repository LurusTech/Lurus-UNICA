# Active Task: 双层角色与租户自治架构（简化 RBAC + 消灭功能交叉调用）

## Context
现有 5 角色权限矩阵中 3 个从未使用，且功能间交叉调用严重（开户 handler 借调
产品线 handler 私有方法、一个 ai_config handler 管六个领域、同一配置双存储
即 D4、三套独立登录身份）。目标：恰好两层角色——admin（管用户与平台）与
user（自治管理自己租户的知识库/渠道/事实/AI设置/工作台/复核），并以分层
依赖规则从架构上禁止 L1 模块互调。详细设计见 `doc/plan-two-tier-tenancy.md`。

（此前的房产知识库测试与修复记录已沉淀至 `doc/test-ajyj-kb-import.md`、
`doc/known-defects.md`、`doc/测试信息.md`，本文件不再保留副本。）

## Critical Files
- doc/plan-two-tier-tenancy.md（完整方案：角色模型/分层规则/API 重塑/迁移/风险）
- unica/admin/internal/rbac/policy.go（矩阵收缩为 IsAdmin/OwnsTenant 两谓词）
- unica/admin/internal/auth/jwt.go（claims 收缩为 {user_id,email,role,tenant_id}）
- unica/admin/internal/handler/customers.go + product_lines.go（开户编排收进 L0 tenants 模块）
- unica/admin/internal/handler/ai_config.go（拆分为 tenant/{knowledge,aisettings,...} 六模块）
- unica/admin/cmd/admin/main.go（路由重排为 /api/v1/tenants/{id}/*，{id} 支持 "me"）
- router/migrations/017_two_tier_tenancy.sql（users 加 role/product_line_id/chatwoot_user_id；删 ai_agent_configs/user_roles/roles；RLS 重写）
- portal/（admin.html + home.html 按角色分流；AI 设置独立成页；工作台走 Chatwoot SSO）

## Step-by-Step Plan
- [x] 1. 迁移 017（含回填断言与 RLS 重写），全新库重放验证
- [x] 2. JWT claims 收缩，jwt_test 更新
- [x] 3. rbac 收缩为两谓词，policy_test 重写
- [x] 4. tenantAuth 中间件（含 "me" 解析），授权矩阵测试
- [x] 5. 路由重排 + audit 重挂（映射钉在 `cmd/admin/router_test.go`；curl 冒烟待部署后补）
- [x] 6. 开户/销户编排收进 tenants 模块（开户全链路已就位；`DELETE /tenants/{id}` 目前是 501 占位，级联清理待实现）
- [x] 7. ai-settings 模块：threshold/handoff 写 config_json.guardrail + 清缓存（= D4 修复代码侧完成，router 行为变化待实机复测）
- [x] 8. workbench SSO 端点（路由与授权已就位，处理器目前是 501 占位；免密实测待部署）
- [ ] 9. 门户重构（双首页/独立 AI 设置页/SSO 跳转），双角色全页面走查
- [x] 10. 收尾：tmp 脚本改挂新路由；`doc/测试信息.md` 页面表/账号表/API 速查更新；
      known-defects D4 标记为"代码侧已消除、待实测后删"、D7 定案、D8 回填端点改新路径

## Current Status
- [ ] Blocked / **In Progress** / Ready for Review

**待部署与实机验收**。代码侧步骤 1-8 已完成、五模块 build/vet/test 全绿，
但下面几项只有单元测试与走查支撑，必须在部署环境上实测后才算收口：

- D4 的实机复现：门户改阈值 → 发消息 → router 按新值放行（过了才删 D4 条目）
- 普通用户经 `/tenants/me/workbench/sso` 免密进 Chatwoot（当前 501 占位）
- `DELETE /tenants/{id}` 的级联清理（当前 501 占位）
- 全表 curl 冒烟（两种角色各跑一遍授权矩阵）
- 步骤 9 的门户重构尚未完成；`doc/测试信息.md` 第二节已按目标形态写好
  （index 分流 / admin.html / home.html / ai-settings.html / 删 product-lines.html），
  页面落地前该表是**计划态**，不是现状

前置事实（已验证）：Chatwoot 平台 API 可对平台应用创建的用户签发免密登录
链接（实测 200）；Dify 无同类接口（保持 admin 专用基础设施）；现网无人持
多产品线，迁移回填断言成立。

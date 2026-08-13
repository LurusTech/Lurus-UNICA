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
- [ ] 1. 迁移 017（含回填断言与 RLS 重写），全新库重放验证
- [ ] 2. JWT claims 收缩，jwt_test 更新
- [ ] 3. rbac 收缩为两谓词，policy_test 重写
- [ ] 4. tenantAuth 中间件（含 "me" 解析），授权矩阵测试
- [ ] 5. 路由重排 + audit 重挂，curl 冒烟全表
- [ ] 6. 开户/销户编排收进 tenants 模块（含 EnsureChatwootAgent 写回 chatwoot_user_id；销户级联清理 Chatwoot/Dify）
- [ ] 7. ai-settings 模块：threshold/handoff 写 config_json.guardrail + 清缓存（= D4 修复），实测 router 行为变化
- [ ] 8. workbench SSO 端点，user 账号实测免密进 Chatwoot
- [ ] 9. 门户重构（双首页/独立 AI 设置页/SSO 跳转），双角色全页面走查
- [ ] 10. 收尾：tmp 脚本与 doc/测试信息.md 更新；known-defects 删 D4 改 D7/D8；五模块全绿

## Current Status
- [ ] Blocked / **In Progress（方案已落盘，未开工）** / Ready for Review

前置事实（已验证）：Chatwoot 平台 API 可对平台应用创建的用户签发免密登录
链接（实测 200）；Dify 无同类接口（保持 admin 专用基础设施）；现网无人持
多产品线，迁移回填断言成立。

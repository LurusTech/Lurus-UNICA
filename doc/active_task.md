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
- [x] 9. 门户重构（双首页/独立 AI 设置页/SSO 跳转），已上线并页面级验证
- [x] 10. 收尾：tmp 脚本改挂新路由；`doc/测试信息.md` 页面表/账号表/API 速查更新；
      known-defects D4 标记为"代码侧已消除、待实测后删"、D7 定案、D8 回填端点改新路径

## Current Status
- [ ] Blocked / In Progress / **Ready for Review（已部署并实机验收通过）**

2026-08-13 部署与实机验收（全部通过）：
- 迁移 017 已应用活库（先 pg_dump 备份至 ~/unica-run/unica-pre017.dump）：
  rehearsal→admin，6 用户→user 各绑各租户，三表已删
- 授权矩阵冒烟 19/19：admin×任意、user×自己、user×他人 403、匿名 401、
  旧路由 404、user 侧 audit 自动过滤
- **D4 实机复现通过并已按记录规则从 known-defects 删除**：租户用户自己经
  `PUT /tenants/me/ai-settings/threshold` 改 0.95→下一条消息即转人工，
  改回 0.70→恢复正常回答（含 config_json 与缓存失效全链路）
- 生命周期演练通过：开临时租户（Chatwoot 自动配）→ 租户用户 SSO 免密
  登进 Chatwoot（会话实证）→ admin 销户 → 业务库/Chatwoot/Dify 三方零残留，
  审计行携带完整清理清单（终审【中1】已修复并有测试钉住）
- 门户九页面在线核对：三个新页面 200，product-lines.html 404

遗留（非阻断，来自终审清单）：已停用账号的 access token 在 2h TTL 内仍有效
（中3，known-defects 未立项，测试信息.md 已记坑）；ai-settings 审计快照只含
guardrail 块（低4）；SSO 发链无审计（低5，GET 天然跳过中间件）。

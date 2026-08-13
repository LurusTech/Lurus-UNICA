# 方案：双层角色与租户自治架构

> 状态：设计稿，待评审。范围：admin 服务的身份/授权/路由重塑 + 门户重构 +
> 一次数据迁移。**不动** router/gateway 的消息管道。
> 触发：2026-08-13 用户要求——"区分普通用户和管理员。普通用户具有管理自己
> 知识库、渠道等所有自己的功能权限；重新设计架构，不要出现各功能相互交叉
> 调用的情况。"

---

## 1. 现状问题（均已核实）

### 1.1 五个角色，三个从未使用

`unica/admin/internal/rbac/policy.go:35-69` 定义了 5 角色 × 9 权限的矩阵。
实际库里（2026-08-13 查询）只存在两种：`rehearsal@` 是 super_admin，
其余 6 个用户全是 product_admin，**每人恰好 1 条产品线**。
supervisor / agent / knowledge_admin 零使用。

而 `product_admin` 的权限行（policy.go:47-56）已经就是"普通用户"的定义：
管自己线的用户、渠道、AI 配置、知识库、会话、报表、审计。
**要做的不是设计新权限体系，是删掉死角色并把授权判断从矩阵降为两条规则。**

### 1.2 功能交叉调用（用户点名的问题），实例清单

| # | 交叉点 | 位置 | 症状 |
|---|---|---|---|
| C1 | 开户 handler 调用产品线 handler 的私有方法 | `customers.go:63,265` → `product_lines.go:242 provisionDifyLine` | 开户横跨用户/产品线/Dify/Chatwoot 四个领域，靠接口注入互相借方法 |
| C2 | ai_config 一个 handler 管六个不相干领域 | `ai_config.go:165-200` 路由 switch：prompt(Dify app)/threshold(DB)/handoff(DB)/knowledge(Dify dataset)/dataset-bind(Dify app)/test | 单文件 800+ 行，改一处怕碰五处 |
| C3 | 同一配置双存储 | known-defects **D4**：portal 写 `ai_agent_configs` 表，router 读 `config_json.guardrail`，互不相通 | 界面改了线上不变 |
| C4 | 同一外部系统双套 provisioning | known-defects **D7**：`admin/internal/bridge/dify.go` vs `router/cmd/setup_dify_workspaces` | 判据不同，混用产生不一致绑定 |
| C5 | 门户页面互相嵌功能 | `product-lines.html` 内嵌 AI-config 弹窗（含提示词回写陷阱，plan-scene-strategy §6.1） | UI 层重复 C2 的耦合 |
| C6 | 三套独立身份 | 门户 JWT / Chatwoot 密码 / Dify 密码 | 用户名义上"一个人"，实际三个账号 |

### 1.3 已验证的可行性前提

- Chatwoot 平台 API 可给**平台应用创建的**用户签发免密登录链接：
  `GET /platform/api/v1/users/{id}/login → {"url": ...sso_auth_token=...}`
  （2026-08-13 实测 200）。"一个账号"对客服工作台是可兑现的。
- Dify 控制台**没有**同类接口，只有共享管理员凭证（bridge 按需铸 token）。
  它是基础设施控制台，不应向普通用户暴露。
- JWT 现有 claims（`internal/auth/jwt.go:13-18`）：`role` + `roles[]` +
  `product_line_ids[]`——多角色多线的设计，现实是 1 人 1 角色 ≤1 线。

---

## 2. 目标架构

### 2.1 角色模型：恰好两层

| 角色 | 数据表示 | 能做什么 |
|---|---|---|
| **admin**（管理员） | `users.role='admin'`，`product_line_id IS NULL` | 用户与租户管理（建/删/停用）、平台运维（Dify 控制台、嵌入服务、审计全量）、可代任意租户操作 |
| **user**（普通用户） | `users.role='user'`，`product_line_id` 非空 | 自己租户的全部：知识库、渠道、确定性事实、AI 设置、客服工作台（SSO）、质量复核、审计（本租户） |

- **一个 user 恰属一个租户**（租户=product_line）。一个租户可以有多个 user
  （同事共管），这只是同 `product_line_id` 的多行，不需要矩阵。
- `users` 表加三列：`role`、`product_line_id`（FK）、`chatwoot_user_id`。
- JWT claims 收缩为 `{user_id, email, role, tenant_id}`；`roles[]` 与
  `product_line_ids[]` 删除。
- `rbac` 包从 9 权限矩阵收缩为两条谓词：`IsAdmin(claims)`、
  `OwnsTenant(claims, tenantID)`。`permissionMatrix`、五个
  `requireManageX` 中间件全部删除，换一个 `tenantAuth`。

### 2.2 分层与依赖规则（消灭交叉调用的机制）

```
┌────────────────────────────────────────────────────────┐
│ L0 身份与租户层（上层）                                    │
│    auth · users · tenants（开户/销户编排在这里，且只在这里） │
├────────────────────────────────────────────────────────┤
│ L1 租户资源模块（六个，互不相知，全部以 tenant_id 为作用域）  │
│  knowledge │ channels │ facts │ ai-settings │ workbench │ quality │
├────────────────────────────────────────────────────────┤
│ L2 外部桥（每个外部系统恰好一个实现）                        │
│    DifyBridge · ChatwootBridge                          │
├────────────────────────────────────────────────────────┤
│ 共享叶子库 pkg/difyapp（提示词/挂载/检索契约）               │
└────────────────────────────────────────────────────────┘
运行时面（router/gateway）：只读 product_lines.config_json 与 Redis，
永不调用 admin API。
```

依赖规则（评审时按 import 检查）：

| 规则 | 允许 | 禁止 |
|---|---|---|
| R1 | L0 → L1 的编排接口、L0 → L2 | L1 → L0、L1 → L1 |
| R2 | L1 模块 → 自己的 repo + 需要的 L2 桥 | handler 借用另一个 handler 的方法（现状 C1） |
| R3 | L2 → pkg/difyapp | 任何两处写同一外部资源（C4：setup CLI 降级为文档注明的一次性脚本） |
| R4 | 运行时 ← config_json（单向） | admin 与 router 各存一份配置（C3：`ai_agent_configs` 表废除，唯一存储是 `config_json`） |

Go 包落位：`internal/identity`（auth+users+tenants）、
`internal/tenant/{knowledge,channels,facts,aisettings,workbench,quality}`、
`internal/bridge`（不变）。现有 `internal/handler` 平铺文件按此拆走。

### 2.3 API 重塑：单一路由族，租户显式

```
/api/v1/auth/login | refresh

# L0，仅 admin
/api/v1/users                GET/POST        （POST 建用户：role + 所属租户）
/api/v1/users/{id}           GET/PUT/DELETE
/api/v1/tenants              GET/POST        （POST = 开户编排：PL+Dify+Chatwoot+首个用户）
/api/v1/tenants/{id}         GET/DELETE      （DELETE = 销户级联，见 §2.6）

# L1，{id} 接受 "me"；授权规则唯一：admin 任意 id，user 仅自己的
/api/v1/tenants/{id}/knowledge/...      （原 /ai-config/{id}/knowledge/*）
/api/v1/tenants/{id}/channels/...       （原 /channels?product_line=）
/api/v1/tenants/{id}/facts/...          （原 /product-lines/{id}/ontology*）
/api/v1/tenants/{id}/ai-settings/...    （原 prompt/threshold/handoff/dataset-bind）
/api/v1/tenants/{id}/workbench/sso      （新：换 Chatwoot 免密链接）
/api/v1/tenants/{id}/violations/...

/api/v1/audit-logs           admin 全量；user 自动过滤到本租户
```

旧路由（`/api/v1/product-lines*`、`/api/v1/customers`、`/api/v1/ai-config/*`）
**直接切除，不留兼容层**——演练环境唯一消费方是门户和 tmp/ 脚本，同步改。

### 2.4 门户重构：按角色分流，两种首页

- 登录后按 `role` 分流：
  - **admin → admin.html**：用户与租户管理（新页面，后端 API 全现成）+
    平台运维卡片（Dify 控制台【标注共享凭证】、嵌入服务状态、审计日志）。
  - **user → home.html**：我的知识库 / 渠道 / 确定性事实 / AI 设置 /
    客服工作台 / 质量复核。**没有产品线选择器**——租户来自 JWT。
- 「客服工作台」卡片 = 调 `/workbench/sso` 拿链接新窗口打开。
  **普通用户从此没有第二个密码。**
- AI 设置从 product-lines.html 的内嵌弹窗独立成页（消灭 C5 及其
  提示词回写陷阱）。
- index.html 保留为登录页/分流器。

### 2.5 外部系统定位（C6 的解法）

| 系统 | 定位 | 普通用户 | 管理员 |
|---|---|---|---|
| Chatwoot 工作台 | 租户功能 | SSO 免密进入 | SSO 或超管控制台 |
| Chatwoot 超管控制台 | 基础设施 | 不可见 | 密码（`Chatwoot-2026!`） |
| Dify 控制台 | 基础设施 | 不可见 | 共享凭证 |

原则：**租户功能必须单点登录；基础设施控制台保持独立凭证且仅 admin 可见。**

### 2.6 租户生命周期（新增销户，堵上已发现的漏洞）

删测试租户时已实证：删 `product_lines` 行不会清理 Chatwoot（留下孤儿坐席
cwtest*，现存 2 个）。销户编排（L0）定义为：停用本租户 users → 删 Chatwoot
账户（平台 API）→ 删 Dify app+dataset → 删业务数据（channels/conversations
级联）→ 删 PL 行。每步失败记 warning 继续，最后返回清理清单。

---

## 3. 数据迁移（`router/migrations/017_two_tier_tenancy.sql`）

1. `users` 加 `role text`、`product_line_id uuid REFERENCES product_lines`、
   `chatwoot_user_id integer`；
2. 回填：`user_roles` 中持 super_admin 者 → `role='admin'`；其余 →
   `role='user'` + 其唯一的 `product_line_id`（迁移前断言：无人持多线，
   查询已证实当前为真；若断言失败迁移中止）；
3. RLS 重写：`channel_configs`、`conversations` 等策略从
   `user_roles` 子查询改为 `users.product_line_id` 直查；
4. 删 `ai_agent_configs`（D4/D6：写入方无人读，`max_ai_turns` 无读取方）；
5. 删 `user_roles`、`roles`（演练环境，不留只读期）。

回滚：迁移事务化；失败自动回滚。已发布的 016 之前不动。

---

## 4. 分步实施（每步可独立验证）

| # | 步骤 | 主要文件 | 验证 |
|---|---|---|---|
| 1 | 迁移 017 | `router/migrations/` | 全新库重放 16+1 全过；回填结果手查 |
| 2 | JWT/claims 收缩 | `admin/internal/auth/jwt.go` | jwt_test 更新后绿 |
| 3 | rbac 收缩为两谓词 | `admin/internal/rbac/` | policy_test 重写：admin 任意 / user 仅自己 / 匿名全拒 |
| 4 | `tenantAuth` 中间件（解析 "me"） | `admin/internal/identity/` | 授权矩阵测试（admin×任意、user×自己、user×他人 403） |
| 5 | 路由重排 + audit 重挂 | `admin/cmd/admin/main.go` | curl 冒烟全表；旧路由 404 |
| 6 | 开户/销户编排收进 tenants 模块（吸收 customers.go；`provisionDifyLine` 变模块私有；Chatwoot agent 建号抽 `EnsureChatwootAgent` 并写 `users.chatwoot_user_id`） | `internal/identity/tenants*.go` | 实机开户：响应含 dify+chatwoot+首用户；销户后 Chatwoot/Dify 无孤儿 |
| 7 | ai-settings 模块：threshold/handoff 改写 `config_json.guardrail` + 清 route cache（**即 D4 修复**）；prompt/dataset-bind 迁入 | `internal/tenant/aisettings/` | 界面改阈值 → 查 config_json → 发测试消息证实 router 行为变化 |
| 8 | workbench 模块：SSO 端点 | `internal/tenant/workbench/` | user 账号实测免密进 Chatwoot |
| 9 | 门户重构（admin.html/home.html/独立 AI 设置页/SSO 跳转/六旧页改挂新路由） | `portal/` | 两种账号全页面点击走查；`tmp/check_portal.py` 更新 |
| 10 | 收尾：tmp 脚本改新路由；`doc/测试信息.md` 账号与页面表更新；known-defects 删 D4、改写 D7/D8 条目；Chatwoot 孤儿清理 | doc/tmp | 五模块 build/vet/test 全绿；`start-local-env.sh --status` 全绿 |

步骤 1-5 是一个可合并切片（身份层）；6-8 第二切片（模块化）；9-10 第三切片。

---

## 5. 明确不做

- **租户内细粒度角色**（supervisor/agent/knowledge_admin）：现网零使用，
  直接删除。将来真客户提出"客服只能回消息不能改配置"时，以 users 表上的
  布尔开关重引入，而不是恢复矩阵。
- **Dify 控制台 SSO**：无平台级接口；它是基础设施，不该做。
- **一个用户跨多租户**：现实为零，砍掉。真出现时是"再发一个账号"的问题。
- **旧 API 兼容层**：无外部消费方。
- **运行时管道改动**：router/gateway、D10-D12 全部正交，另行处理。
- **Chatwoot 超管的 SSO**：超管是手工建的、不属平台应用，保持密码。

## 6. 风险

| 风险 | 缓解 |
|---|---|
| RLS 重写破坏 router 的表访问 | router 以表 owner（dify）连接、天然 bypass RLS——迁移后仍需实测一条消息全链路 |
| 开户响应结构变化破坏依赖方 | 消费方只有门户与 tmp 脚本，同步改；响应保留 dify/chatwoot/warnings 字段名 |
| audit 中间件漏挂 | 步骤 5 验证表逐路由核对 audit_logs 有记录 |
| 门户六页改挂新路由回归 | 步骤 9 双角色全页面人工走查 + check_portal.py |
| 一次删三张表（ai_agent_configs/user_roles/roles）过激 | 迁移事务化；演练库有 pgdata 目录级备份可回退 |

## 7. 与既有缺陷的关系

| 缺陷 | 本方案的处置 |
|---|---|
| D4（AI 配置双存储） | **被消灭**：唯一存储 config_json，写入方在 ai-settings 模块（步骤 7） |
| D6（死代码） | `max_ai_turns` 随 `ai_agent_configs` 表删除 |
| D7（双套 provisioning） | admin 的 DifyBridge 定为唯一实现；`setup_dify_workspaces` 降级为注明用途的一次性脚本 |
| D8（存量回填） | 回填端点随路由改名（`/tenants/{id}/ai-settings/dataset/bind`），语义不变 |
| D9/D10-D12 | 正交，不在本方案范围 |

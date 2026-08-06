# Active Task: 客户自助层（单部署多客户的自助运营）

## Context
把"运营者集中管理"的后台升级为客户可自助的多租户控制台：开户约定 1 账号 = 1 产品线
（表结构保持多对多不动），渠道一线可绑多个；客户自管知识库与渠道；同浏览器多标签页
可双开两个账号（token 按标签页隔离，现状已满足）。后台开户、无公开注册。

## Critical Files
- unica/pkg/difyapp/（新增共享数据集客户端）
- unica/router/internal/bridge/dify_dataset.go、unica/router/internal/knowledge/manager.go、unica/router/migrations/
- unica/admin/internal/handler/ai_config.go、unica/admin/internal/bridge/dify.go、unica/admin/internal/config/config.go、unica/admin/cmd/admin/main.go
- unica/admin/internal/handler/（第 3 期：customers 编排 + chatwoot 平台客户端）
- portal/knowledge.html（新增）、portal/*.html（客户视图锁定）

## Step-by-Step Plan
- [x] 1. 知识库后端：pkg 共享数据集客户端（列表/传文件/传文本/删除/按 batch 查索引状态）；
      引入数据集级密钥（环境变量 DIFY_DATASET_API_KEY，缺失时 503 明示）；admin 的
      ai-config/knowledge 扩成完整增删查并补产品线越权校验与审计；修复 router 知识管道
      两处根因（数据集端点误用应用级 key；indexing-status 误用 document_id，实际按
      batch——迁移补 batch 列）；假服务单测全绿
- [x] 2. portal：knowledge.html 知识库页（列表/上传/删除/索引状态轮询）；客户视图
      （前端解 JWT，单线账号锁定产品线选择并隐藏切换，多开行为保持）
- [x] 3. 一键开户：POST /api/v1/customers 编排（建产品线→开通 Dify→建账号+按线赋角色→
      Chatwoot 平台 API 建 account/用户/绑定/API 收件箱→写 config_json.chatwoot；
      每步幂等，Chatwoot 未配置时降级并明示）；portal 开户入口；假服务单测
- [ ] 4. 验收：六模块 build/vet/test(-race) + CI 绿；WSL 复原 Dify 栈实测
      知识库上传→索引→问答接地 + portal 走查；结论写回 unverified 清单

## Current Status
- [ ] In Progress（第 1-3 期已完成并通过评审闸门；第 4 期（WSL 复原实测）进行中。
  分支 feat/customer-self-service）
- 第 3 期要点：POST /api/v1/customers 一键开户（super_admin 专属，需同时持有
  manage_users+manage_product_lines）；Chatwoot 三段式进度落盘（account→token→inbox
  各自成功即持久化，重试按缺口续作，不再孤儿化 account）；不完整绑定如实报
  configured:false 并附原因；赋角失败降级返回 201 保住一次性生成密码；
  迁移 016 product_lines.name 唯一索引堵并发开户竞态；生成密码只出现在开户
  响应一次，审计快照统一脱敏；portal 产品线页新增一键开户向导与结果报告；
  新环境变量 CHATWOOT_BASE_URL / CHATWOOT_PLATFORM_TOKEN / CHATWOOT_WEBHOOK_URL
  （平台应用 token 需在 Chatwoot Super Admin 后台手工创建一次）
- 第 2 期要点：portal 新增 knowledge.html（列表/搜索/分页、文件+文本上传、删除、
  按 batch 轮询索引进度带退避与代数守卫）；六页统一 JWT 解码助手，非超管单线账号
  锁定产品线选择；首页按角色隐藏 产品线管理/本体 卡（本体暂不下放为既定决策）；
  上传响应跨切线守卫、恰好 15MB 的边界预检、只读空状态文案已按评审修正
- 第 1 期要点：新增 DIFY_DATASET_API_KEY（数据集级密钥，缺失时 503/明确报错）；
  admin ai-config/knowledge 现为 列表/上传(文件+文本)/删除/按 batch 查状态；
  上传请求单独放宽读写期限（服务器全局 10s 超时对 15MB 上传不够）；
  router 知识管道改用共享客户端并修复两处根因（app key 误用、batch 误用），
  迁移 015 补 batch 列；首传默认 process_rule=automatic（新数据集无历史规则可回退）

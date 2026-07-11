# Active Task: 门户产品线管理 — 打通"产品线→知识库→渠道"配置链

## Context
用户指出配置断层：知识库在 Dify 设、渠道在门户设，但产品线本身没有设置入口，三者串不起来。本增量在门户新增"产品线管理"页 + admin 新增 Dify 一键开通接口，实现用户期望的流程：建产品线 → 一键初始化该产品线的 AI 应用与知识库（挂载）→ 渠道管理里选产品线+平台。知识库内容的上传编辑仍在 Dify 控制台（门户提供直达链接），不重造 Dify UI。

## 设计决策
- **不做 workspace-per-产品线**（Dify 社区版多工作区支持不可靠）：全部产品线的应用+知识库建在默认工作区内，命名前缀区分（如 "UNICA-{name} 应用/知识库"）。隔离靠 app/dataset 边界 + UNICA 数据库绑定，与交付脚本的差异记录在案。
- **Dify 凭证自动化**：admin 新增 DIFY_ADMIN_EMAIL/DIFY_ADMIN_PASSWORD 环境变量，开通时调 Dify console login 换临时 token，不再要求人工粘贴 DIFY_ADMIN_TOKEN（脚本旧方式的痛点）。
- 绑定回写沿用既有约定：product_lines.dify_agent_id / dify_api_key / dify_base_url / config_json{dify_dataset_id}（与 setup_dify_workspaces.go 一致，幂等：已绑定则跳过）。

## Critical Files
- unica/admin/internal/bridge/dify.go（扩展：login 换 token、CreateApp、CreateDataset、CreateAppAPIKey）
- unica/admin/internal/handler/product_lines.go（新增 POST /api/v1/product-lines/{id}/provision-dify；GET 响应补充绑定状态字段）
- unica/admin/internal/repository/product_line.go（绑定字段读写）
- unica/admin/internal/config/config.go（DIFY_ADMIN_EMAIL/PASSWORD）
- portal/product-lines.html（新建：产品线 CRUD + 一键开通 + AI 参数编辑(现有 ai-config API) + 知识库直达链接）
- portal/index.html（卡片改为：产品线管理/渠道接入/客服工作台/AI 与知识库）
- portal/channels.html（产品线为空时引导去产品线页）

## Step-by-Step Plan
- [x] 1. workflow 并行实现完成（另主会话补了 DELETE /api/v1/product-lines/{id}，带"仍有渠道时 409 拒绝"保护）
- [x] 2. workflow 验证全绿
- [x] 3. 已部署 ubuntu-1（admin 新二进制 + DIFY_ADMIN_* env + 三个门户页面）
- [x] 4. E2E 通过：provision-dify 真实 Dify 实测——登录/建应用/建知识库/发 API Key/回写绑定 全部成功，幂等重试正确返回 provisioned=false，产品线列表带 has_dify_binding/dify_dataset_id
- [ ] 5. 用户实测门户流程；测试数据"默认产品线"已带真实绑定，可保留当样例

## 新发现的交付遗留 bug（本次已绕开，待消息链路增量根修）
- router/internal/bridge/dify_admin.go UpdateAppConfig 用 `PUT /apps/{id}` 只传 pre_prompt，真实 Dify 0.15.3 返回 400（要求 name 参数；提示词实际应走 model-config 且依赖已配置模型供应商）。原交付仅对 mock 测试过。admin 侧开通流程已将该步骤改为非致命警告。

## Out of Scope
- 知识库文档上传/编辑（留在 Dify 控制台）
- Chatwoot 收件箱自动开通（后续增量，脚本已有）
- gateway/router 部署与消息链路

## Current Status
- [ ] In Progress — 计划待用户确认

# Active Task: 第 2 期——本体与配置运营化

## Context
第 1 期已交付。本期目标：本体的校验/预览/发布/回滚和 ontology 开关全部走
admin API + portal 页面，带 RBAC 与审计，"改政策、开 shadow"不再需要工程师连库。

## Step-by-Step Plan
- [x] 1. `router/internal/domain` → `pkg/domain`：16 文件纯 rename、8 处 import
      改写、3 处测试相对路径修正、pkg 增 yaml.v3；router+pkg 全绿
- [x] 2. admin 本体 handler：`internal/handler/ontology.go` 5 端点；
      子树入口按路径分派权限（ontology* 走 AI-config 权限，其余照旧）；
      审计 handler 内直写（子树无审计中间件）；`SetConfigKey` 用 SQL 端
      jsonb 合并（原子、保留其他键）；`Store.SourceYAML` 新增；
      9 个 handler 测试（校验/发布/回滚/配对硬约束/scope 403/404/往返）
- [x] 3. `portal/ontology.html` + index 卡片：编辑/校验/预览+token 估算/
      发布/版本回滚/开关面板（配对规则双端强制），对 mock 全流程冒烟通过
- [x] 4. guardrail schema 统一本期不做（已有写路径可用，无用户可见收益）
- [x] 5. 验证：五模块 build/vet/test 全绿；WSL `-race` 全绿
- [ ] 6. 提交推送，CI 全绿，汇报第 2 期

## Current Status
- [ ] In Progress（待提交与 CI）

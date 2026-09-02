# Active Task: AI 模型配置收回门户（配置面重构 阶段 4 · A 组）

## Context

`doc/plan-workbench-settings.md` 第八条。模型配置此前是**编译期常量**
（`pkg/difyapp/model.go` 的 `PlatformModel()`），不落库、无环境变量、无写入口，
改一次要重新编译发版。

现在：门户可改（平台默认 + 产线覆盖两层），改完先推一条产线验证、Dify 认了才落库，
漂移清单逐线比对 Dify 实况，勾选即可重推，全程写审计。

## Critical Files

- `unica/router/migrations/021_model_versions.sql` — 新建，已在实机应用
- `unica/admin/internal/repository/model_version.go` — 新建，仓储层
- `unica/pkg/difyapp/model.go` — `Validate()`、`MinMaxTokens`，`PlatformModel()` 语义改为回落默认
- `unica/admin/internal/bridge/dify.go` — `PinModel` / `PinModelWithToken` / `ErrModelWriteLanded`
- `unica/admin/internal/platform/models.go` — 漂移清单 + 批量重推
- `unica/admin/internal/platform/platform.go` — 平台写路径（先验证后提交）
- `unica/admin/internal/tenant/aisettings/modeloverride.go` — 产线覆盖
- `unica/admin/internal/identity/dify_line.go` — 开通时改用生效值
- `portal/admin.html` / `portal/ai-settings.html`

## Step-by-Step Plan

- [x] 1. 迁移 021：平台默认与产线覆盖同表，`product_line_id IS NULL` 为平台档
- [x] 2. 仓储层 + `PlatformModel()` 降为回落默认
- [x] 3. `PinModel(spec)` + 读回校验
- [x] 4. 漂移清单（三态）+ 批量重推 + 审计
- [x] 5. 写路径「先验证后提交」四步，含落库失败时把验证目标推回旧值
- [x] 6. `admin.html` 模型卡片可写 + 漂移清单
- [x] 7. `ai-settings.html` 显示档位与偏离提醒
- [x] 8. `MaxTokens` 下限 2048 与原因说明
- [x] 9. 编译 + 测试 + 实机验收

## 评审后补修（不在原计划内）

评审给出三条中危、九条低危，其中五条属于本仓库反复栽跟头的形态，已修：

- **写入已落地 vs 写入被拒混为一谈**。`pinModelWithToken` 在 POST 成功之后的所有失败
  （读回失败、读回不匹配）都被当成"Dify 拒绝了，什么都没写"，于是不回滚、不审计、
  告诉操作员没写——而应用可能已经在用新模型答客户了。新增 `ErrModelWriteLanded`
  把两者分开，落地未确认的走回滚 + 审计 + 如实措辞。
- **租户侧回滚二次失败对操作员不可见**。平台侧已把 `RevertError` 放进响应，租户侧只写日志，
  响应展示旧配置并称"未生效"——是反事实的答复。补齐字段并在门户渲染。
- **`set()` 的幂等早退**。存储值与请求相同即返回 OK 不重投影，是 D8 的同一形态：
  Dify 被手工改过时，"再保存一次相同的值"修不好。改为仍然投影、只是不新建版本。
- **读库失败静默回落内置值**。会让审计的 `before` 记成假的旧配置、回滚推错值。改为拒绝写入。
- **Dify 拒绝的写入尝试不写审计**。补上，实机已验证留痕。

配套测试四个：落地未确认时回滚并如实措辞（平台/租户各一）、读不到旧配置时拒写、
回滚二次失败时响应带 `revert_error`。改了一个既有测试的断言——
`SavingWhatIsAlreadyInForce` 原本断言"不写 Dify"，与上面第三条冲突，改为断言"重投影但不新建版本"。

## 实机验收结果（2026-08-26）

迁移 021 已应用到实机；两个 `COALESCE` 表达式唯一索引实测确认能管住平台默认档
（Postgres 默认 NULLS DISTINCT，普通 `UNIQUE` 管不住）。

1. **改温度不发版** — PUT 温度 0.3→0.5，落库 version 1、`pushed_at IS NULL`（已定版未生效），
   重推后 10 条线全部生效。验收后已还原为 0.3。
2. **不存在的模型名被拒且零落库** — 502，透传 Dify 原话
   "model.name must be in the specified model list"，`model_versions` 表 0 行。
3. **漂移清单认出手工改动** — 上线即抓出 **5 条演练线跑在 `deepseek-chat` 上**，
   且 `temperature`/`max_tokens` 双双为 0（从未写过 `completion_params`）。见 D18 补记。
4. **审计 before 记录旧配置** — 确认；被拒的那次写入也留痕（`ok:false` + Dify 错误原文）。

**顺带修掉的实机问题**：上述 5 条演练线已纠正到 `deepseek-v4-flash` / 4096。
清单现为 `{drifted: 0, in_effect: 10, unknown: 0}`。

## 已知遗留

- `pkg/domain` 的 `TestSeedOntologiesMatchCannedResponses` 仍红。既有失败，根在 ontology schema，
  `plan-config-surface.md` §7 明确排除，属另一个增量。
- `ModelSpec.Matches` 不比较 `mode`，手工只改 `mode` 的漂移认不出来。低危，未修。
- 批量重推时若推前读取应用配置失败，审计行的 `before` 为空。低危，未修。

## Current Status

- [x] Ready for Review —— 代码全绿、实机验收通过，等待提交

# Active Task: 配置面重构 阶段 1 —— 补缺口 + 锁死平台模型

## Context
把三处"根本没有地方配"的东西给个地方，并堵住模型漂移的产生源。
四件事里**只有开户锁死模型是强制项**——它防的是问题继续产生：
2026-08-23 把五条线拉平到 v4-flash 是一次操作，不是一个状态，
`provisionDifyLine` 建 Dify 应用时不写 model 字段，新租户会落到工作空间默认值上重新漂开。
其余三件（holding_message 入口、survey 入口、模型只读卡片）不改任何运行时行为。
完整设计见 `doc/plan-config-surface.md`。

## 摸底确认的两件事（都改变了做法）
1. **`timeout_hours` 是死字段**：被解析、有默认值、有单测，但没有任何逻辑读它——
   真正的 TTL 是硬编码的 `surveyPendingTTL = 24 * time.Hour`。
   直接暴露到门户等于交付一个**改了没反应**的开关，比没有更糟。故先接线再暴露。
2. **`AuditState` 只审计 guardrail 块**（`aisettings.go:508`，接线在 `main.go:247/251`）。
   新增 survey 写入而不扩展它，这些写操作不留任何审计痕迹。
   `holding_message` 在 guardrail 块内，天然已覆盖。

## 平台模型这个值放哪（已定）
代码里的常量是权威（`pkg/difyapp`，与 `DefaultSystemPrompt` 同处，符合既有惯例），
admin 环境变量可覆盖作为**紧急切换通道**，注释写明正式入口是后续阶段的平台配置页。
理由：常量保证任何部署都有确定值，env 不做成永久配置面。

## Critical Files
- `unica/pkg/survey/`（新建：SurveyConfig 的唯一权威，比照 pkg/guardrail）
- `unica/router/internal/survey/handler.go`（改为 import，并把 TTL 接到配置上）
- `unica/pkg/difyapp/prompt.go`（新增 PlatformModel 定义）
- `unica/admin/internal/bridge/dify.go`（AppInfo 补模型字段；新增写 model 的路径）
- `unica/admin/internal/identity/tenants_provision.go`（开户时写模型）
- `unica/admin/internal/tenant/aisettings/aisettings.go`（holding_message、survey、AuditState）
- `unica/admin/cmd/admin/main.go`（`aiSettingsPaths` 闭合路由表）
- `portal/ai-settings.html`（三处界面）

## Step-by-Step Plan
- [x] 1. 新建 `unica/pkg/survey`：`Config` + `Defaults()` + `Load()`，回填逐字段。
      router 的 survey 包改为 import。不这么做就会重新造出刚刚消灭掉的那种双份定义。
- [x] 2. `timeout_hours` 接线：`surveyPendingTTL` 由配置推导，默认仍是 24 小时，
      现有行为不变。零值与负值走默认。加单测钉住"配置 12 小时则 TTL 是 12 小时"。
- [x] 3. `holding_message` 写入：`updateHandoffRulesRequest` 加**可选**字段
      （指针类型，不传即不改，既有调用方不受影响）。校验用 `domain.IsBlankAnswer`——
      纯空白的转人工话术正是最后一条能把空白消息送到客户眼前的路径。
- [x] 4. `survey` 写入：新增 `PUT ai-settings/survey`，同时改 `aiSettingsPaths` 闭合表
      与 `Handle` 的 switch。读-改-写复用 `SetConfigKey` 的 jsonb 单键替换。
      **不需要失效路由缓存**（router 的 survey 每次现查库，与 guardrail 不同）——
      实现时要验证这一点，写进注释。
- [x] 5. 扩展 `AuditState`：把 survey 块一并纳入，否则 survey 的每一次修改都不留痕。
- [x] 6. 模型只读卡片的服务端：`AppInfo` 补 provider/name/completion_params，
      `GetAppConfig` 解析 `model_config.model`，`settingsResponse` 带出去。
- [x] 7. **开户锁死模型**（强制项）：`pkg/difyapp` 定义 `PlatformModel`，
      开户创建应用后按"读出整个 model_config、只改 model、整体写回"写入
      （与 `updateSystemPromptWithToken` 同一形状，Dify 无部分更新）。
      失败要**报告**而不是静默——静默正是 D8/D16 那一类缺陷的成因。
- [x] 8. 门户三处：转人工卡片加 holding_message 输入；新增 survey 卡片；
      新增模型只读卡片（复用现有 `.card` / `.hint` / `.mono` 样式，页面无框架无构建）。
- [x] 9. 验证：五模块 build+vet+test；**开一个全新租户，断言其 Dify 应用的 model
      等于平台设定**（这是本阶段唯一的强制验收）；从门户改 holding_message 与 survey，
      确认 router 侧立即读到新值。

## Current Status
- [x] 阶段 1 完成（2026-08-24）

### 实机验收（强制项已过）

开一个全新租户 `PINTEST`，其 Dify 应用被自动锁定：

```
model: {"provider":"openai_api_compatible","name":"deepseek-v4-flash",
        "temperature":0.3,"max_tokens":4096,"pinned":true}
```

开户日志留痕 `[dify-bridge] pinned app_id=... to openai_api_compatible/deepseek-v4-flash`。
存量租户 XDYX 同样报 `pinned: true`。验收后已销户，Dify 应用/数据集、
Chatwoot 账户 9、门户账号全部随之清理（顺带验证了阶段 0 那处"config_json 优先"的销户改动）。

其余三项：
- `holding_message` 可读可写；**纯空白被 400 拒绝**——用全角空格+零宽空格实测，
  `TrimSpace` 会放行，`domain.IsBlankAnswer` 拦下了。
- `survey` 可读可写，落库确认 `{"enabled":true,"timeout_hours":6,"min_customer_messages":3}`。
- 模型只读卡片在门户渲染，偏离平台设定时显示"已偏离平台设定"的告警胶囊。

### 与计划的差异

1. **`ShouldSendSurvey` 改了签名**，返回 `(bool, *SurveyConfig, error)`。
   原打算在 `SendSurvey` 里现查配置，写完发现测试里 `db` 为 nil 直接 panic——
   那说明这个做法给函数引入了它原本没有的依赖。改由已经加载过配置的调用方传下去，
   一次加载、无新依赖。
2. **`settingsResponse` 漏带 model 字段**，第一次验收拿到 `model: null`，
   而 Dify 侧查下来锁定其实成功了。教训是"日志说做了"和"接口能看到"是两件事，
   只读卡片的价值恰恰在后者。
3. **门户 JS 里的 `
` 在写入时被折叠成真实换行**，导致字符串字面量跨行、语法错误。
   现在用数组 `.join("
")` 组装，并在提交前用 `node --check` 校验抽出的脚本块。

### 遗留

- 平台模型目前是 `pkg/difyapp.PlatformModel()` 里的常量，**env 覆盖通道还没做**。
  当前没有它也能工作（改值重编译即可），正式入口是后续阶段的平台配置页。
  先不加 env，避免把临时通道做成事实上的永久配置面。
- `AuditState` 已含 survey 块，但**提示词仍不在审计范围内**（它不在 config_json 里，
  权威源在 Dify）。阶段 2 把提示词权威源收回本地后应一并纳入。

## 后续阶段（见 doc/plan-config-surface.md）
- 阶段 2：提示词权威源回收（本地版本表 + publish/rollback），解 D16
- 阶段 3：检索方式与分段规则回收；修 `provisionDifyLine` 早退条件，解 D8
- 阶段 4/5：env 业务开关下放、租户自带 key（均标注可不做）

## 未处理的既存红灯
`pkg/domain` 的 `TestSeedOntologiesMatchCannedResponses` 先于阶段 0 就红，根治在阶段 3。

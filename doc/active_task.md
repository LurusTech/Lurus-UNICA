# Active Task: 场景化应答策略注入（售前 / 售后）

## Context
每个产品线只有一套系统提示词，五条规则全是约束性的（不得编造、不得改写），
没有任何关于"怎么说"的指导——售前不会先确认用途再报价，售后不会先复述损失再给方案。
本任务按场景注入不同的应答**策略**（表达方式、提问顺序、信息密度），
但不改变可陈述的**内容**：数值与政策仍由本体决定。
完整设计见 `doc/plan-scene-strategy.md`。

三个已定前提：策略由平台内置、客户不可改；本期只做场景注入，不碰经验回流与 acest；
场景判定走关键词规则 + 会话粘性。

## Critical Files
- `unica/router/internal/intent/stage.go`（新增）
- `unica/pkg/difyapp/strategy.go`（新增）
- `unica/pkg/difyapp/prompt.go`（变量声明 + 模板占位符 + 规则 6）
- `unica/router/internal/routing/router.go`（`difyConvSession.Stage` / `prepareAIContext` 注入 / `setDifyConvID` 回写）
- `unica/router/internal/routing/scene.go`（新增，模式开关）
- `unica/admin/internal/handler/ai_config.go`（提示词回灌端点）
- `deploy/dify-preview/configure_apps.py`（模板的第三份拷贝，必须同步）

## Step-by-Step Plan
- [x] 1. 新增 `intent/stage.go`：`Stage` / `ClassifyStage` / `ResolveStage` + 词表，售后为吸收态
- [x] 2. 新增 `stage_test.go`：表驱动，重点守卫"退款政策是什么"不得判为售后、"3150的和2999的哪个更值得买"不得被 315 碰撞误伤
- [x] 3. 新增 `pkg/difyapp/strategy.go`：三段策略文本，每段结尾统一附边界声明
- [x] 4. 新增 `routing/scene.go`：`SceneMode` off/shadow/on，与 `guardrail/triage.go` 同构；`cmd/router/main.go` 读 `SCENE_MODE`
- [x] 5. 注入接线：`difyConvSession` 加 `Stage`、`getDifySession` 返回整个 session、`aiCallContext` 加 `stage`、`prepareAIContext` 填 `scene_context`、`setDifyConvID` 带上 stage 回写
- [x] 6. `prompt.go`：`ContextVariables` 加 `scene_context`、模板插占位符、回答规则追加第 6 条
- [x] 7. 新增指标 `router_scene_classified_total{stage, reason, source, mode}`
- [x] 8. admin 新增 `POST /api/v1/ai-config/{id}/prompt/reset`（super_admin，用默认模板回灌）+ 5 条 handler 测试（含 advanced 模式拒绝）
- [x] 9. `eval.Case` 加 `Stage` 字段 + `TestClassifyStage_GoldenCorpus`，60 条中标注 19 条无歧义 case
- [x] 10. 同步 `configure_apps.py` 的 `CONTEXT_VARS` 与 `PRE_PROMPT`
- [x] 11. `pkg/difyapp` 防分叉守卫测试：读 Python 文件断言变量名与占位符齐全，文件缺失则 skip
- [x] 12. `router_wiring_test.go` 加三条：on 模式注入且售前/售后文本不同、shadow 模式不注入、粘性吸收态（经真实 miniredis 会话）
- [x] 13. 六模块 build + vet + test 全绿（严格退出码验证）
- [x] 14. 端到端实测通过（2026-08-11，WSL 复原环境，真实 Dify + deepseek）：
  - 回灌端点首跑暴露真缺陷："dify admin token is empty"——AI-config 全部控制台调用
    依赖没人配置的静态 DIFY_ADMIN_TOKEN。已修：bridge 按 静态 token → 邮箱密码登录铸造
    （缓存 30 分钟 + 401 重铸一次）回退，提交 3a913a7
  - 修复后 DrillCo3 回灌成功，Dify 侧验证：占位符 `{{scene_context}}` ✓ 规则 6 ✓ 变量声明 ✓
  - 全链路（router SCENE_MODE=on，真实 Redis 流 + 真实模型）：
    售前"这两款榨汁机哪个更值得买"→ 反问二选一（出汁率 vs 清洗）；
    售后"我买的杯子碎了"→ 先共情复述 + 归类"物流问题" + 一次说清凭证（订单号+两张照片）;
    同会话追问"换一个新的多少钱"→ **保持售后语气**（免费补发，不推销）——吸收态经真实会话生效；
    指标 `router_scene_classified_total`：2 presales/comparison + 2 postsales/fault + 1 inherited，零误差
  - 边界确认：DrillCo3 未配本体，模型给出"免费补发"这类流程承诺无人校验——
    真实客户上线须配本体+validation，这不是场景策略的缺陷而是既有 per-line opt-in 设计
- [x] 15. README：环境变量表 + 「场景化应答策略」章节（含回灌约束）

## Current Status
- [x] Ready for Review：15 步全部完成，分支 feat/scene-strategy（f1f7a4d + 3a913a7），待合并

## 演练环境当前状态（2026-08-11 实测后仍在运行）
Dify 七件套 + unica-redis(:6380) + admin-scene-bin(:8081) + router-scene-bin(:8090) 全部在跑。
演练遗留数据：channels 表插入了一行 scene-drill 渠道（id 1111...5501 → DrillCo3）；
DrillCo3 的 config_json 加了 `guardrail.confidence_threshold=0.05`（放行答案用，
真实部署不要抄）且提示词已回灌为新模板。释放命令同上次（pkill + docker rm + compose down）。

---

## 附：演练环境（2026-08-06 已释放，数据保留可复原）
步骤 14 需要它。容器与进程全停，**数据目录 `/data/unica-dify`（85MB，业务库含
DrillCo3/4/5）与 `~/unica-run/`（admin.new + 含数据集密钥的 admin.env）保留**。

复原：`cd /data/unica-dify && docker compose up -d`，重建 unica-redis(:6380) 与
unica-portal（host 网络 :8791），`source ~/unica-run/admin.env` 后起 admin.new。
凭证：Dify 控制台 admin@unica.local / Rehearsal-Dify-2026!；
门户超管 rehearsal@unica.local / Rehearsal-2026!。
Windows 侧访问一律用 `wsl hostname -I` 的 IP，localhost 转发不通。
无嵌入模型的工作区上传知识库须设 `DIFY_INDEXING_TECHNIQUE=economy`。

## 附：本期不做但已登记
调研中确认了 7 条已经坏掉的东西，全部写入 `doc/known-defects.md`。
其中 **D4（后台 AI 配置对线上路由无任何效果）** 是客户能直接感知的，
优先级高于本任务，建议插队处理。

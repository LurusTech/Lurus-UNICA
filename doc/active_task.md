# Active Task: （无进行中任务——场景化应答策略已合并 main）

场景化应答策略（分支 feat/scene-strategy，2026-08-11）15 步全部完成并合并：
1. 场景轴 `intent.ClassifyStage`（售前/售后/未知，售后为吸收态），与路由分诊正交
2. 平台内置策略文本经 `scene_context` 变量注入，每段以"事实优先"边界收尾（提示词规则 6）
3. `SCENE_MODE=off/shadow/on` 三档，默认 shadow；指标 `router_scene_classified_total`
4. 超管回灌端点 `POST /api/v1/ai-config/{id}/prompt/reset`（幂等，advanced 模式明确报错）
5. WSL 全链路实测：真实 Dify + deepseek，售前反问二选一、售后共情+归类+一次索证、
   同会话追问价格保持售后语气（吸收态实证），指标 2/2/1 零误判
6. 演练顺手修掉真缺陷：AI-config 控制台调用依赖没人配的静态 token，
   bridge 现在按需登录铸 token（3a913a7）

main = c875282（含协作者的 portal 品牌 CSS 与 secret 扫描闸），CI 六模块绿 + Secret Scan 绿。

## 演练环境当前状态（2026-08-11 合并后仍在运行）
WSL(UbuntuE)：Dify 七件套 + unica-redis(:6380) + admin-scene-bin(:8081) +
router-scene-bin(:8090, SCENE_MODE=on)，二进制在 ~/unica-run/。
演练遗留：channels 表一行 scene-drill 渠道（1111...5501→DrillCo3）；
DrillCo3 阈值 0.05（放行答案用，真实部署不要抄）且提示词已回灌新模板。
凭证：Dify 控制台 admin@unica.local / Rehearsal-Dify-2026!；
门户超管 rehearsal@unica.local / Rehearsal-2026!。
Windows 侧访问用 `wsl hostname -I` 的 IP。释放：pkill 两个 *-bin 进程 +
`docker rm -f unica-redis` + `cd /data/unica-dify && docker compose down`。

## 建议下一步（按性价比排序）
1. **D4（known-defects.md）**：后台 AI 配置对线上路由无效——演练已证实读取侧健康，
   只差 `updateThreshold`/`updateHandoffRules` 写 `config_json.guardrail` + 清 route cache
2. D3：坐席回复落 `messages` 表（`sender_type='human'`），"借鉴真人技巧"依赖链第一块砖
3. `SCENE_MODE=on` 前先在真实流量上跑 shadow 看 stage 分布（现默认即 shadow，零操作）
4. D1/D2：满意度调查触发链与 Chatwoot resolve 事件消费（结果信号）

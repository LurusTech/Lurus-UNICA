# acest 双知识库接入说明

router 通过 acest kb-server 的 HTTP API 接入两类知识库，并回写客服经验形成闭环。
整个集成 fail-open：kb-server 不可达时仅缺少注入上下文，消息路由不受影响。

## 拓扑

kb-server 是部署级单实例（非每产品线一个）。两种部署形态均可：

- **同机部署**：kb-server 与 UNICA 跑在同一台机器，`ACEST_KB_URL=http://127.0.0.1:7423`。
- **跨机/Tailscale**：kb-server 以 `acest kb-server --bind 0.0.0.0 --allow-write` 启动
  （或 env `ACEST_KB_API_BIND=0.0.0.0`），UNICA 用 Tailscale IP 访问。网络隔离靠
  防火墙/NetworkPolicy，token 必须设置。

kb-server 侧启动（acest 项目）：

```
ACEST_KB_API_TOKEN=<token> acest kb-server --allow-write [--port 7423] [--bind <ip>]
```

注意：
- 经验提交（/api/v2/experiences）走 kb-server 内部的 LLM 蒸馏管线，acest_home 下需有
  可用的模型配置，否则提交任务会 failed。
- 客服经验域应使用独立的 acest_home 数据目录，与个人编码经验库分开；需要互通时用
  acest CLI 的 export/import 单向同步。
- 外源知识源（文档库）需在 acest 侧预先注册（CLI/桌面端），HTTP 无创建源端点。

## router 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `ACEST_KB_URL` | 空（禁用集成） | kb-server 地址，如 `http://127.0.0.1:7423` |
| `ACEST_KB_TOKEN` | 空 | Bearer token，与 kb-server 的 `ACEST_KB_API_TOKEN` 一致 |
| `ACEST_RECALL_TIMEOUT` | `2s` | 单次召回总预算（两库并行共用） |
| `ACEST_RECALL_TOP_K` | `3` | 每库最多注入的片段数 |
| `ACEST_KB_SOURCES` | 空（全部启用源） | 逗号分隔的外源库 source id 过滤 |
| `ACEST_EXPERIENCE_QUEUE` | `256` | 经验回写队列容量，满则丢弃并计数 |

## 数据流

1. **召回注入**：`callDifyAndPublish` 在调用 Dify 前并行查询
   `POST /api/v1/search`（经验库）与 `POST /api/v1/kb/search`（外源库），
   命中结果注入 Dify inputs：`experience_context` / `knowledge_context`。
   **Dify 应用编排中需声明这两个变量并在提示词中引用**，否则注入无效。
2. **经验回写**：guardrail 决策点异步提交 `POST /api/v2/experiences`：
   - AI 直接应答（send）→ `success=true`，Q/A 为客户消息与 AI 答复；
   - 转人工（handoff）→ `success=false`，`error` 带转接原因与置信度。
   `session_id` 为 UNICA 会话 ID，`tools_used` 为命中的 Dify RAG 数据集名。
   kb-server 侧经 reflector→评估→curator 去重后才入库，UNICA 不直接写成品经验。

## 指标

- `router_acest_recall_total{kind=playbook|kb, status=hit|empty|error}`
- `router_acest_recall_duration_seconds`
- `router_experience_submitted_total{outcome, status}`
- `router_experience_dropped_total`

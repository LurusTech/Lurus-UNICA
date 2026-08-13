# 方案：creator 广告文案生成接入 UNICA ontology 约束

状态：方案已定稿，未开工。涉及两个仓库（E:\kefu 与 E:\生成\lurus-creator），实施前需分别确认。

## 一、背景与前提纠正

creator（Lurus Creator Studio，Wails v2，Go 后端）最近用于生成广告文案，希望受本项目
ontology 的事实/合规约束，避免无约束生成（夸大承诺、声称业务不提供的服务等）。

两个关键事实（探查结论，2026-08-13）：

1. **creator 并没有直连 Dify 的 Postgres**。它走的是 Dify Service API
   （`internal/knowledge/dify.go`：`GET /v1/datasets` + `POST /v1/datasets/{id}/retrieve`），
   这是干净的 HTTP 只读链路，不需要"戒掉直连数据库"这一步。
2. **UNICA 的 ontology 权威数据源在业务库 `ontology_versions` 表**（按租户一版一行，
   带版本历史与 active 标记），YAML 只是导入格式。并且**已有现成租户 API**：
   - `GET  /api/v1/tenants/{id}/facts` → 当前激活版本 source_yaml + 版本列表
   - `POST /api/v1/tenants/{id}/facts/validate` → `{valid, rendered, estimated_tokens}`
     （rendered 即注入 Dify 的 facts_context 文本块：确定性事实 + 不提供的服务清单 + 事实标签词表）

所以接入方式就是：**creator 作为 UNICA 租户 API 的一个普通客户端**，不引入任何
跨仓库代码依赖，不做任何数据库直连。

## 二、方案总览（两条链路，可独立落地）

### 链路 A：生成前注入（约束进 prompt）

```
creator adgen 生成流程：
  取租户 ontology 渲染块（HTTP，带 TTL 缓存）
    → 作为第三段拼进 system prompt（现有：平台规则 + 营销风格）
    → LLM 生成文案
```

- UNICA 侧（小改，可选但推荐）：facts 模块新增
  `GET /api/v1/tenants/{id}/facts/rendered` —— 返回**当前激活版本**的渲染文本块。
  没有它 creator 也能用两步凑出来（GET facts 拿 source_yaml → POST validate 拿 rendered），
  但那是校验接口的副作用，语义不对且多一跳；加只读端点更干净。
  实现上就是 `Store.Active` + `Render`，facts handler 里十几行。
- creator 侧改动点（探查已定位）：
  - `internal/adgen/manager.go` `run()` 中 `BuildCopyPrompt` 调用前取约束块
    （对应 content 模块 `pipeline.go` 检索注入的同构位置）；
  - `internal/adgen/prompts.go` `BuildCopyPrompt` 增加第三段"事实与合规约束"参数，
    denies 渲染为明确的"禁止在文案中声称…"指令；
  - HTTP client 照 `internal/knowledge/dify.go` 的 `doJSON` 模板抄一份；
  - 设置层（`internal/settings`）加 UNICA BaseURL + 租户账号两个字段。

### 链路 B：生成后校验（合规兜底）

```
creator 生成文案后：
  POST /api/v1/tenants/{id}/facts/check-text  {text}
    → UNICA 用该租户激活 ontology 跑校验 → 返回 violations
  有违规 → creator 拒收/重生成/标红给用户
```

- UNICA 侧（新端点）：facts 模块加 `POST .../facts/check-text`，内部复用
  `pkg/domain` 现成能力：`ParseClaims`（若文本带事实标签）+ `Validate`
  （断言/域/值域校验 + **denials 文本扫描**）。denials 扫描不依赖标签，
  对广告文案最有价值——"学位保证、包过户、垫资"这类高风险话术直接命中。
  只读校验、不落 `claim_violations` 表（那张表语义是线上会话违规，别混）。
- creator 侧：Phase 1 出文案后调一次，violations 非空则带违规原因重试一轮
  （复用 `BuildRewritePrompt` 的改写机制），仍违规则标注给用户。

### 分工原则（与本项目既有教义一致）

| 数据 | 归属 | creator 获取方式 |
|---|---|---|
| 商品/房源明细（量大、常变） | Dify dataset | 已有 `internal/knowledge` 检索链路（content 模块模式，adgen 可复用） |
| 政策/合规/不提供的服务（少、稳定） | ontology | 链路 A 注入 + 链路 B 校验 |

## 三、鉴权

creator 用租户用户账号走 `POST /api/v1/auth/login` 换 JWT，请求带 Bearer。
TenantAuth 中间件天然限定"只能取自己租户的 ontology"，无需新权限模型。
token 过期按 401 重登。凭证存 creator 本地 settings（SQLite），不进任何仓库。

## 四、明确不做

- **不直连业务库/Dify 库**读 `ontology_versions`——绕过租户鉴权、复制 Decompile 逻辑、两库耦合。
- **不跨仓库 import `unica/pkg/domain`**——两个仓库发布节奏耦合，且 creator 拿不到 compiled
  数据源，本地解析 YAML 会与线上激活版本漂移。校验留在服务端，creator 只拿结论。
- **不为广告场景改 ontology schema**——denies/assertions 现有表达已够用；如某租户约束不足，
  是租户 ontology 内容问题，走门户编辑，不动引擎。

## 五、实施顺序建议

1. UNICA：`GET .../facts/rendered` + `POST .../facts/check-text`（同一 handler 文件，一次提交，含测试）
2. creator：链路 A（注入）—— 立刻能感知质量变化
3. creator：链路 B（校验兜底）—— 合规红线
4. 联调：用 AJYJ 租户实测一条"诱导违规"商品描述（如暗示学位、包过户），验证注入后不再出现、
   校验能兜住

## 六、风险

- ontology 渲染块 token 量（validate 接口已返回 estimated_tokens，AJYJ 量级在数百 token，可控；
  creator 缓存 TTL 建议 5 分钟，与服务端 Store 缓存一致）
- 租户未发布 ontology 时两个端点应返回明确的 404/空态，creator 降级为无约束生成并在 UI 提示
- check-text 是同步校验，denials 扫描为纯文本匹配，性能无虞；不设熔断（离线生成场景，
  不同于线上会话链路）

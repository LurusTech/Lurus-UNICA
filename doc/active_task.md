# Active Task: 配置面重构 阶段 4 · C 组（动线闭合）— 第一增量：文档分段只读（C3 + C4）

## Context

「我的知识库」缺"看某篇文档被切成了什么样"这一步，租户因此仍有一件事必须打开 Dify。
补上 admin 侧分段只读接口与门户「查看分段」后，租户日常动线在门户内闭合（验收第 2 条）。

C1 已实测定案（见下），**C2 拆为下一增量**：它要改运行中的 Dify nginx，并新增一条
平台级鉴权旁路，性质与 C3/C4 完全不同。C2 的设计结论已在本文 Deferred 一节定稿，
立项时直接照此执行，不必重议。

### C1 实测结论（2026-09-02，不必再验）

**子路径反代不可行。** Dify 0.15.3 的 web 是 Next.js，无 `basePath`。
`GET /` 返回 `307 → /apps`（绝对路径），HTML 里资源全是绝对的 `/_next/static/chunks/*.js`，
且 `/apps` `/datasets` `/signin` 等顶层路由会与门户自己的路由撞车。
除非重建 Dify 前端，否则这条路是死的——原 C2「同源反代」正是建在它上面。

**替代路径的三个前提均已验证：**
- Dify 自己那套 nginx 的配置在我们手里（`/data/unica-dify/nginx.conf`），
  现有 location 仅 `/console/api`、`/api`、`/v1`、`/files` 四条反代到 `api:5001`
  外加 `location /` 反代到 `web:3000`，可加新 location。
- `POST /console/api/login`（平台 Dify 管理员账号）实测返回
  `{"result":"success","data":{"access_token":"...","refresh_token":"..."}}`。
- Dify 前端从 **`localStorage` 的 `console_token` / `refresh_token`** 读凭据
  （web 容器 `/app/web/.next/static/chunks` 内直接搜到 `getItem("console_token")`，
  登出路径是 `removeItem("console_token")`）。

### 分段接口实测形状（本增量的地基）

读 `unica-dify-api` 容器内 `controllers/service_api/dataset/segment.py` 的 `SegmentApi.get` 定案：

- **不分页。** 只接受 `status`（可重复）与 `keyword` 两个 query 参数，
  没有 `limit` / `page` / `offset`；`total = query.count()` 之后把符合条件的段**全部返回**。
  所以一次请求拿到整篇文档的全部分段，前端必须自己控制渲染量。
- 响应顶层是 `{"data": [...], "doc_form": "text_model", "total": N}`——**三个键，`total` 不能漏**。
- 单段字段实测：`id, position, document_id, content, answer, word_count, tokens,
  keywords, index_node_id, index_node_hash, hit_count, enabled, disabled_at, disabled_by,
  status, created_by, created_at, updated_at, updated_by, indexing_at, completed_at, child_chunks`。
  `child_chunks` 是 0.15.3 的父子分块，当前数据里为空数组，本增量不展示但类型里要容得下。
- 服务端**已支持** `keyword` 过滤。本增量仍做客户端筛选：既然一次就取回全部，
  再为筛选发一轮请求只是多一次往返；但这是个选择而非唯一解，
  文档量级变大到单篇上千段时可以改走服务端。

## Critical Files

- `unica/pkg/difyapp/dataset.go`（新增 `Segment` / `SegmentList` 与 `ListSegments`）
- `unica/pkg/difyapp/dataset_test.go`
- `unica/admin/internal/tenant/knowledge/knowledge.go`（新增 `documents/{doc}/segments` 分支）
- `unica/admin/internal/tenant/knowledge/knowledge_test.go`
- `unica/admin/cmd/admin/router_test.go`（路由表加一行；`main.go` 的 `case "knowledge"`
  不设闭合清单，**无需改动**）
- `portal/knowledge.html`（行内「查看分段」按钮 + 分段弹窗）
- `doc/测试信息.md`（API 速查加一行）
- `doc/plan-workbench-settings.md`（C 组状态与 C1 结论回填）

## Step-by-Step Plan

- [x] 1. **客户端类型**：`pkg/difyapp/dataset.go` 新增 `Segment`
      （`ID, Position int, Content, Answer, WordCount, Tokens, HitCount int, Enabled bool,
      Status string, Keywords []string`，`answer` 可为 null，用指针或空串处理）
      与 `SegmentList{Data []Segment; DocForm string; Total int}`。
      JSON 标签对齐实测响应；多余字段（`index_node_*`、`child_chunks`、各时间戳）忽略，
      与既有 `Document` 同一风格。
- [x] 2. **客户端方法**：`func (c *DatasetClient) ListSegments(ctx, datasetID, documentID string) (*SegmentList, error)`。
      空参数走 `errors.New` 不发网络（同 `IndexingStatus`）；路径
      `/datasets/{ds}/documents/{doc}/segments`，两段都 `url.PathEscape`；经 `newRequest`/`do`。
      返回前按 `Position` 升序稳定排序（不依赖 Dify 的顺序）；`Data` 为 nil 时置空切片。
- [x] 3. **客户端测试**：`dataset_test.go` 加 `TestListSegmentsRequestAndDecode`
      （断言路径、Bearer、解码含 `hit_count`/`enabled=false`/`keywords`/`total`、乱序输入按
      position 排好）与 `TestListSegmentsArgumentValidation`（空 ID 不发请求）。
      `TestAPIErrorMapping` 补一条 404 分支覆盖该路径。
- [x] 4. **处理器路由**：`knowledge.go` 的 `Handle` switch 加分支——
      `rest[0]=="documents" && len(rest)==3 && rest[2]=="segments"`，仅 `GET`，
      调 `h.segments(w, r, pl, rest[1])`。文件头部注释的路由表同步加
      `GET knowledge/documents/{docID}/segments` 一行。
- [x] 5. **处理器实现**：`datasetFor(w, pl, true)`（未绑库 → 404 `noDatasetMessage`；
      无 key → 503，沿用现有语义）；docID 空白 → 400；调 `h.dataset.ListSegments`；
      上游错误经 `writeDatasetError`（Dify 对「文档不属于该数据集」回 404，原样映射为 404）。
      **租户隔离的最后一道**：dataset ID **只**来自路径租户的 `pl.DifyDatasetID`，
      请求里任何 dataset 参数一律不读。
      响应 `{product_line_id, document_id, doc_form, total, segments}`
      （`total` 用 Dify 返回的值而非 `len`——两者当前一致，但那边是 `query.count()`，
      让它自己说了算）。
- [x] 6. **处理器测试**：`knowledge_test.go` 加 `TestHandler_Segments`（路径
      `/v1/datasets/ds-1/documents/doc-1/segments`、字段回传、total）、
      `TestHandler_SegmentsIgnoresRequestDataset`（照抄 `TestHandler_ListIgnoresRequestDataset`，
      query 带别人的 dataset 仍打 `ds-1`）、`TestHandler_SegmentsUpstream404`（Dify 404 → 本端 404）、
      `TestHandler_SegmentsWithoutDataset`（未绑库 → 404）、
      `TestHandler_SegmentsMethodNotAllowed`（POST → 405）。
      `TestHandler_ScopeForbidden` 加该路径一例（403）。
- [x] 7. **路由表测试**：`cmd/admin/router_test.go` 加
      `{GET, "/api/v1/tenants/pl-1/knowledge/documents/doc-9/segments", "knowledge", 同路径}`。
      审计中间件对 GET 不记录（`audit/middleware_test.go:21`），无需处理。
- [x] 8. **门户入口**：`rowHtml` 的 `row-actions` 在「删除」前加
      `<button data-segments="<id>" data-name="<name>">查看分段</button>`；
      `pendingRowHtml` **不加**（尚在索引，没有稳定分段）。
      `tbody` 的 click 监听增加 `button[data-segments]` 分支。
- [x] 9. **分段弹窗**：新增 `#segments-modal`，复用现有 `.modal-backdrop`/`.modal` 样式，
      加 `.modal.wide`（宽度 `min(960px, 92vw)`、内容区 `max-height:70vh; overflow:auto`）。
      **弹窗而非行内展开**：单段最长 1000 token，一篇文档几十到上千段，
      展开在 5 列表格里会把其余文档推出视口，并与 `table-wrap` 自己的滚动打架。
      结构：标题行（文档名 · 共 N 段 · 字数合计）、客户端筛选框（按内容子串过滤，不再发请求）、
      列表区、关闭按钮；`Esc` 与点遮罩关闭，沿用现有 keydown 处理。
- [x] 10. **列表项渲染**：`#序号`（`position`）、正文（`white-space: pre-wrap` + `escapeHtml`）、
      右侧元信息「N 字 · 命中 M 次」；`enabled=false` 加 `pill muted 已停用`；
      `doc_form==="qa_model"` 时正文下另起「答：」行显示 `answer`。
      **分批渲染**：先 100 条，底部「显示更多」按钮追加 100——接口不分页，
      一次就把整篇文档的段全给了，不控制渲染量会在大文档上卡死。
      空结果按状态区分文案：文档 `indexing_status!=="completed"` → 「索引尚未完成，分段稍后可见」；
      已完成但为空 → 「该文档没有产出分段」。
- [x] 11. **请求接线**：走
      `UnicaAuth.api(knowledgePath("/documents/" + encodeURIComponent(id) + "/segments"))`；
      加载中弹窗先开、显示「加载中…」；`err.status===404` 用后端 `error` 文案；
      `unauthorized` 静默（与 `deleteDoc` 一致）。
- [x] 12. **文档**：`doc/测试信息.md` 第七节 API 速查「知识库」一行补
      `GET .../knowledge/documents/{doc}/segments`；
      `doc/plan-workbench-settings.md` 把 C1 标 `[x]` 并附结论（子路径不可行，改 Dify 同源种子页）、
      C3/C4 标 `[x]`、C2 标「下一增量，设计见本文 Deferred」。
- [x] 13. **Go 单测**：`unica/pkg` 下 `go test ./difyapp/`；
      `unica/admin` 下 `go test ./internal/tenant/knowledge/ ./cmd/admin/`。
      三包全绿，`go vet` 无告警。（注意 Go 没有 `-q`。）
- [x] 14. **实机验收**（WSL IP 用 `wsl hostname -I` 现查；换 admin 二进制先按端口取 PID kill、
      等 `kill -0` 失败再 cp）：
      1. `ajyj-admin@unica.local` 登门户 → 知识库 → 任一已完成文档「查看分段」：
         段数、首段正文与 Dify 控制台（`:3402`）该文档详情页**逐一对上**；
      2. **隔离**：用 AJYJ 的 access token 直接
         `GET /api/v1/tenants/me/knowledge/documents/<XDYX 的某个 doc id>/segments` → **404**，
         body 不含任何分段；用 XDYX token 打 `/tenants/<AJYJ id>/...` → 403；
      3. admin 账号带 `?tenant=<AJYJ id>` 打开 knowledge.html，同一按钮可用；
      4. 传一篇新文档，索引未完成时点「查看分段」看到「索引尚未完成」文案，完成后刷新可见分段。
      以上通过后清空本文件。

## Deferred（下一增量：C2 · Dify 入口，设计已定，可直接立项）

### 给谁：**只给 admin**

Dify 社区版只有一把共享管理员钥匙，拿到即全平台管理员：能读改**所有**租户的应用、
知识库正文与模型供应商凭据，能删掉别的产线的 app（`dify_agent_id` 悬空、该租户路由即断）。
而且在 Dify 里改提示词/模型/top_k 会被 UNICA 的权威源判为漂移、下次回推**静默冲掉**——
租户在那边做的任何事都是白做。「私用」消掉的是"恶意租户"，消不掉"共享钥匙没有个人留痕"。

所以 **C2 不是「工作台加入口」，而是「`portal/admin.html` 平台运维卡片里的 Dify 控制台
从裸外链升级为免密直达」**，`home.html` 不动；租户靠本增量的 C3/C4 做到"不用去"。

> 若决定仍要放给租户，改动本身很小（中间件从 `requireAdmin` 换成 `authMW`，
> home 加一张卡片），但**必须先**在 `plan-workbench-settings.md` 第一条
> 把上述后果写成一条已接受的决定，而不是默默放开。

### token 怎么到种子页：**URL fragment，一次性，只用于这一跳**

fragment 不上送服务器（nginx access log 不记）、不进 Referer；
种子页读完立刻 `history.replaceState(null,"",location.pathname)` 再 `location.replace("/apps")`，
带 token 的 URL 不留在历史栈。

不做「一次性 ticket 二次交换」：种子页在 `:3402`，回调 admin `:8081` 是跨源，
要么 admin 加 CORS 白名单和 preflight，要么 Dify nginx 加一条反代到宿主 admin
（容器到宿主的地址随 WSL IP 变，`extra_hosts: host-gateway` 需重建容器）——
两套新活动件，防的却是 fragment 已经不具备的泄露面；
token 最终本来就落在 localStorage 里，交换制不改变这一点。

### 后端

`bridge.DifyBridge.Login` 现在丢掉了 `refresh_token`（只解 `access_token`），
新增 `LoginPair` 返回两者。新端点 `GET /api/v1/platform/dify-console/session`
（`authMW(requireAdmin(...))`，与 `/platform/model` 同级），**每次新登录**而不是复用
30 分钟缓存（`consoleToken` 是服务自己的工作凭证，不把同一串交给浏览器），
响应 `{access_token, refresh_token}`，写审计 `resource=dify_console action=login`。

`DIFY_ADMIN_EMAIL/PASSWORD` 为空时 503 并说明原因（D21 口径）；
只配了静态 `DIFY_ADMIN_TOKEN` 也拒绝——那是长命凭证，不外发。
**D23 在此不放大**：refresh token 当 Bearer 时 role 为空，`RequireAdmin` 直接 403。

Dify 侧：新登录只写新键不删旧键，admin 自己的定期登录不会作废浏览器那份 refresh token；
浏览器在 Dify 点登出只删它自己那对。

### 前端（`admin.html`）

`#dify-link` 改为按钮：先 `window.open("about:blank")`（弹窗规则，同 `home.html` 的 SSO 卡片），
再取 session，再 `win.location.replace(DIFY_CONSOLE_BASE + "/unica/console-entry#a=<access>&r=<refresh>")`。

### Dify nginx

仓库副本 `deploy/dify-preview/nginx.conf`，运行副本 `/data/unica-dify/nginx.conf`
以 `:ro` 文件级 bind mount 进容器。在 `location /` **之前**加：

```
location = /unica/console-entry {
    default_type text/html;
    add_header Cache-Control "no-store";
    add_header Referrer-Policy "no-referrer";
    return 200 '<!doctype html>…内联不超过 20 行脚本：读 hash → 写 localStorage 的
                console_token / refresh_token → replaceState → location.replace("/apps")；
                hash 为空则显示"请从平台管理页进入"并链到 /signin…';
}
```

路径带 `/unica/` 前缀，与 `/apps` `/datasets` `/signin` 等 Next.js 顶层路由不撞。

**改法**：先 `cp nginx.conf nginx.conf.bak-<日期>`，两份副本同改；
写入运行副本必须 `cp` 或 `cat >` **覆盖原 inode**——`sed -i` 与编辑器的原子改名会生成新文件，
容器里看到的还是旧内容，改了等于没改；然后 `docker exec unica-dify-nginx nginx -t`
通过后 `nginx -s reload`。

**改坏了会怎样**：`nginx -t` 不通过则 reload 被拒、旧配置继续服务，Dify 不受影响；
reload 成功但 location 写错，只影响这一条新路径，`location /` 未动。
**回滚**：`cp nginx.conf.bak-<日期> nginx.conf && nginx -s reload`，一分钟内。

### C2 的验证

全部实机——admin 点卡片新窗口直落 `/apps` 且已登录；
地址栏与浏览器历史里搜不到 `console_token` 值；
`docker logs unica-dify-nginx` 里该请求行无 token；
用租户 token 打 session 端点 → 403；用 refresh token 当 Bearer → 403；
`audit_logs` 有 `dify_console/login` 一行；
把 `.env` 里 `DIFY_ADMIN_PASSWORD` 清空重启 admin → 卡片给出 503 文案而非静默失败。

## Current Status

- [x] **步骤 1-14 完成，实机验收通过（一项未能观察到，见下）。未提交。**

### 实机验收结果（2026-09-02）

`go vet` 与 `go test ./...` 在 `unica/pkg` 与 `unica/admin` 两个模块全绿；
`knowledge.html` 内联脚本通过 `node --check`，页面里 `getElementById` 引用的 id 全部存在。
admin 二进制已换（旧的留在 `~/unica-run/admin-scene-bin.bak`）。

**接口与隔离（curl 直打 :8081）**
- 自己的文档 → 200，字段齐（`doc_form` / `total` / `segments`）。
- **AJYJ 用自己的租户路径请求 XDYX 的文档 ID → 404**，响应体不含 `segments` 字段。
- XDYX 用户打 AJYJ 的租户路径 → 403。
- `POST` 该路径 → 405；不存在的文档 ID → 404。
- 同一文档，门户读到的段数与直读 Dify 一致。

**门户（Playwright，Chrome 扩展未连）**
- 文档行出现「查看分段」；弹窗标题显示「文档名 · 共 N 段 · M 字」；分段按 `#position` 编号。
- 筛选：命中词保留分段，未命中清空并给出「没有匹配…」。
- `Esc` 关闭并清空列表内容（在途响应落不到已关闭的视图上）。
- admin 带 `?tenant=<AJYJ>` 打开同一页，看到同样的分段。
- 一篇 **59 段**文档：59 项全部渲染、`position` 严格升序、字数 2,174 与筛选均正确、
  `显示更多` 正确隐藏（59 < 100）。全程零页面错误。

### 未能验证的一项（如实记录）

计划第 14 步第 4 条「索引未完成时点查看分段，应显示『索引尚未完成，分段稍后可见』」
**没能在实机观察到**。这台环境的索引比一次页面刷新还快：上传一篇 59 段的文本后，
第一次刷新时 `indexing_status` 已是 `completed`，窗口关闭。现有 30 篇文档也全部 completed。
该分支是纯客户端判断（`state.docs` 里该文档的 `indexing_status !== "completed"`），
错了的后果只是空状态文案不准，不涉及数据或权限。**记作未验证，不记作通过。**

### 遗留的测试数据（两份，删除请人工执行）

AJYJ 知识库里有两个探针文档，都是验收产物：
- `refresh-retry-probe.txt`（`7aa7c16e-c628-456f-95b9-19f5c2aa4c41`，D 组 multipart 验证留下）
- `indexing-state-probe.txt`（`7ced67c1-c27a-469d-9441-23732b36f4e6`，本次为抢索引窗口上传，59 段）

### 下一步

代码与文档就绪，等待提交。C2（Dify 免密入口）见上面的 Deferred，可直接立项。

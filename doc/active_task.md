# Active Task: 配置面重构 阶段 4 · C 组第二增量 — Dify 控制台免密直达（C2）

## Context

`portal/admin.html` 的平台运维卡片里，Dify 控制台是一条裸外链：点进去落在 Dify 的登录页，
管理员得再输一次密码。本增量把它换成免密直达。

**入口只给 admin，`home.html` 不动。** Dify 社区版只有一把共享管理员钥匙，拿到即全平台管理员：
能读改所有租户的应用、知识库正文与模型供应商凭据，能删掉别的产线的 app（`dify_agent_id` 悬空、
该租户路由即断）。而且租户在 Dify 里改提示词/模型/top_k 会被 UNICA 的权威源判为漂移、
下次回推静默冲掉——白改。租户要看的东西已由上一增量的「查看分段」在门户内解决。

### 形态（C1 已定案，不再验证）

子路径反代不可行（Next.js 无 `basePath`，`/` 是 `307 → /apps`、资源全绝对、顶层路由撞车）。
改走**同源种子页**：Dify 自己那套 nginx 供出一个极小页面，与 Dify 同源，
把后端取来的 token 写进 `localStorage` 的 `console_token` / `refresh_token`，然后跳 `/apps`。

**token 走 URL fragment**：fragment 不上送服务器（nginx access log 不记）、不进 Referer；
种子页读完立刻 `history.replaceState` 清掉再 `location.replace("/apps")`，不留在历史栈。
不做「一次性 ticket 二次交换」——种子页在 `:3402`、回调 admin `:8081` 是跨源，
要么加 CORS 白名单与 preflight，要么给 Dify nginx 加一条到宿主 admin 的反代
（容器到宿主的地址随 WSL IP 变），两套新活动件防的却是 fragment 已经不具备的泄露面；
token 最终本来就落在 localStorage 里，交换制不改变这一点。

### 已核实的现状

- `bridge.DifyBridge.Login`（`dify.go:213`）只解 `access_token`（`difyLoginResponse`，`dify.go:196-201`），
  `refresh_token` 被丢弃。
- `consoleToken()`（`dify.go:1106`）优先用静态 `AdminToken`，否则用配置的邮箱密码登录并缓存 30 分钟。
  **这两样都不能直接交给浏览器**：静态 token 是长命凭证；缓存的那串是服务自己的工作凭证。
- `config.go:19-23`：`DifyAdminURL` / `DifyAdminToken` / `DifyAdminEmail` / `DifyAdminPassword`。
- `platform.Handler` 已有 `requireAdmin`（`platform.go:687`）与 `h.audit`（`settingsAudit` 接口）。
- 路由挂载在 `cmd/admin/main.go:438-446`，形如 `mux.Handle("/api/v1/platform/model", authMW(...))`。
- `portal/admin.html:838` `DIFY_CONSOLE_BASE`，`:2975` `els.difyLink.href = DIFY_CONSOLE_BASE + "/"`。
- Dify nginx：仓库副本 `deploy/dify-preview/nginx.conf` 与运行副本 `/data/unica-dify/nginx.conf`
  **当前逐字节一致**（已 diff 确认）；运行副本以文件级 bind mount 进容器的
  `/etc/nginx/conf.d/default.conf`。现有 location 为 `/console/api`、`/api`、`/v1`、`/files`、`/`。

## Critical Files

- `unica/admin/internal/bridge/dify.go`（`loginPair` + `ConsoleSession`）
- `unica/admin/internal/bridge/dify_test.go`
- `unica/admin/internal/platform/platform.go`（新端点 + `consoleSessionMinter` 接口）
- `unica/admin/internal/platform/platform_test.go`
- `unica/admin/cmd/admin/main.go`（路由与装配）
- `deploy/dify-preview/nginx.conf` 与运行副本 `/data/unica-dify/nginx.conf`
- `portal/admin.html`
- `doc/测试信息.md`、`doc/plan-workbench-settings.md`

## Step-by-Step Plan

- [x] 1. **bridge · 取回 refresh_token**：`difyLoginResponse` 加 `RefreshToken`；抽
      `loginPair(ctx, email, password) (access, refresh string, err error)` 承载现有 HTTP 逻辑，
      `Login` 改为调它并只返回 access（**签名不变**，现有调用点不动）。
- [x] 2. **bridge · `ConsoleSession(ctx) (access, refresh string, err error)`**：
      每次都新登录，**不读缓存、不用静态 `AdminToken`**（长命凭证不外发；只配了它就报错）；
      未配置邮箱密码时返回可辨识的错误，供上层转 503。不写入 `b.cachedToken`——
      这一对是给浏览器的，不是服务自己的工作凭证。
- [x] 3. **bridge 测试**：`dify_test.go` 加——登录响应同时带两个 token 时都取到；
      只配 `AdminToken` 时 `ConsoleSession` 拒绝且不发请求；缺邮箱密码时同样拒绝；
      连续两次 `ConsoleSession` 发两次登录请求（不复用缓存）。
- [x] 4. **platform · 新端点**：`consoleSessionMinter` 接口（一个方法 `ConsoleSession`），
      `SettingsConfig` 加 `Console` 字段（可为 nil），`Handler` 加 `console` 字段。
      `HandleDifyConsoleSession`：仅 `GET`；`requireAdmin`；`h.console == nil` 或凭据缺失 → 503
      并说明是哪个环境变量；成功返回 `{access_token, refresh_token}`。
- [x] 5. **platform · 审计**：成功与失败都写一行，`action="login"`、
      `resource_type="dify_console"`，`after_state` 只记 `{"ok":bool}` 与失败原因，
      **绝不把 token 写进审计**。
- [x] 6. **platform 测试**：非 admin → 403；`Console` 为 nil → 503；成功 → 200 且响应体含两个 token；
      **审计行里不出现 token 字面量**；非 GET → 405。
- [x] 7. **装配**：`main.go` 加
      `mux.Handle("/api/v1/platform/dify-console/session", authMW(http.HandlerFunc(platformHandler.HandleDifyConsoleSession)))`，
      `SettingsConfig.Console` 传 dify bridge。
      **计划里「`router_test.go` 路由表加一行」不适用**：那张表只覆盖租户子路由
      （`TestTenantRouter_*`），没有 platform 路由表可加。新路由是 `main.go` 里一条普通
      `mux.Handle`，行为由处理器自己的测试覆盖。
- [x] 8. **Dify nginx 种子页**：在 `location /` **之前**加 `location = /unica/console-entry`，
      `default_type text/html`、`Cache-Control: no-store`、`Referrer-Policy: no-referrer`，
      `return 200` 一段内联 HTML：读 `location.hash` 的 `a` / `r` → 写两个 localStorage 键 →
      `history.replaceState` 清掉 hash → `location.replace("/apps")`；
      hash 为空则显示「请从平台管理页进入」并链到 `/signin`。
      路径带 `/unica/` 前缀，与 `/apps` `/datasets` `/signin` 等顶层路由不撞。
      **两份副本同改**；写运行副本必须 `cp` 或 `cat >` **覆盖原 inode**——`sed -i` 与编辑器的
      原子改名会生成新文件，容器里看到的还是旧内容，改了等于没改。
      改前 `cp nginx.conf nginx.conf.bak-<日期>`；`docker exec unica-dify-nginx nginx -t`
      通过后再 `nginx -s reload`。
- [x] 9. **admin.html**：`#dify-link` 改为按钮。先 `window.open("about:blank")`（弹窗规则），
      再取 session，再 `win.location.replace(DIFY_CONSOLE_BASE + "/unica/console-entry#a=…&r=…")`；
      取不到时关掉那个空窗并 toast 后端给的原因。token 只经 fragment，不写进任何 DOM 属性。
- [x] 10. **Go 验证**：`unica/admin` 下 `go build ./...`、`go vet ./...`、`go test ./...` 全绿。
      （Go 没有 `-q`。）
- [x] 11. **实机验收**：
      1. admin 点卡片 → 新窗口直落 `/apps` 且已登录；
      2. 地址栏与浏览器历史里搜不到 `console_token` 的值；
      3. `docker logs unica-dify-nginx` 里该请求行**不含 token**；
      4. 租户 token 打该端点 → 403；refresh token 当 Bearer 打 → 403（D23 不被放大）；
      5. `audit_logs` 有 `dify_console` / `login` 一行，且**不含 token 字面量**；
      6. 种子页不带 hash 直接打开 → 显示提示、不写 localStorage、不跳 `/apps`。
- [x] 12. **文档**：`doc/测试信息.md` 第二节页面表补种子页一行并说明只给 admin；
      第九节坑表补「改 Dify nginx 必须覆盖原 inode」；
      `doc/plan-workbench-settings.md` 把 C2 标 `[x]` 并记下「只给 admin」这条已接受的决定。

## 回滚

- **nginx**：`cp nginx.conf.bak-<日期> nginx.conf && docker exec unica-dify-nginx nginx -s reload`，一分钟内。
  `nginx -t` 不过则 reload 被拒、旧配置继续服务；写错也只影响这一条新路径，`location /` 未动。
- **admin**：旧二进制在 `~/unica-run/admin-scene-bin.bak`。

## Current Status

- [x] **步骤 1-12 完成，实机验收全部通过。未提交。**

### 实机验收结果（2026-09-02）

`go build ./...`、`go vet ./...`、`go test ./...`（unica/admin）全绿；`admin.html` 通过 `node --check`。
admin 二进制已换（旧的在 `~/unica-run/admin-scene-bin.bak`）；
Dify nginx 已改并 reload（备份 `/data/unica-dify/nginx.conf.bak-20260902-084614`，
宿主与容器 md5 一致，确认覆盖的是原 inode）。

**接口**：admin GET → 200 带两个 token；租户 → 403；refresh token 当 Bearer → 403（D23 未被放大）；
无凭证 → 401；POST → 405。签出的 token 实测能打 Dify 的 `/console/api/workspaces`（200）。

**浏览器**：admin 点卡片 → 新窗口直落 `/apps` 且已登录；地址栏与 history 里没有 token；
种子页不带 hash 直接打开时停在原地、给出提示、不写 localStorage；
租户工作台不出现 Dify 入口，且端点对租户 403；全程零页面错误。

**不泄露**：9 次种子页请求的 nginx 日志行**零条**含 token 特征（`a=` / `r=` / `eyJ`）——
fragment 从不上送服务器；审计有 `dify_console` / `create` / `{"ok":true}`，
整个审计响应里零个 `eyJ`。

### 实现过程中被拦下的三个问题

1. **审计动词会被数据库拒绝、审计行静默丢失**。仓库自带的
   `TestAuditActionsAreInTheMigrationVocabulary` 抓到：`audit_logs_action_check` 只允许
   `create/delete/publish/push/review/rollback/update`，我原本写的 `"login"` 会在 insert 时被拒、
   行直接丢掉。改用词表里的 `create`（语义准确：创建了一个控制台会话），不为一个动词做 schema 迁移。
2. **`window.open(..., "noopener")` 返回 null**，按钮会永远走「被拦截」分支、根本打不开。
   实测确认后去掉 `noopener`——句柄正是这里需要的东西，而目标是我们自己的 Dify 实例。
3. **落点是 `/install` 而不是 `/apps`**。Dify 前端在两个 token 之外还看第三个键 `setup_status`：
   它先查 localStorage，没有就异步问服务器，而那次异步在首屏渲染前没回来，于是先弹去 `/install`。
   服务端 `/console/api/setup` 返回 `{"step":"finished"}`。种子页补写这个键——
   理由是能签出会话就说明实例已安装；Dify 自己的登出也是把这三个键一起删的。

### 顺带测出的上游性质（不是缺陷，已写进坑表）

Dify 的控制台 JWT 只有 `iat`/`exp`、没有 `jti`，**同一秒内登录两次返回同一串 access_token**——
与我们刚修掉的 D22 同形态，长在上游。`refresh_token` 是随机串，每次都不同。
要紧的那一半（一次性、长命的 refresh token）是唯一的，access token 撞车无害：
它是有效期内的 bearer JWT，不是一次性凭证，两个浏览器拿到同一串不会互相作废。
据此把 bridge 测试里「两次的 access 必须不同」这条断言收窄成断言我们自己的行为
（确实发了第二次登录、refresh 不同）——原来那条是在断言 Dify 的行为，而桩代码恰好满足它。

### 回滚

- nginx：`cp /data/unica-dify/nginx.conf.bak-20260902-084614 /data/unica-dify/nginx.conf`
  再 `docker exec unica-dify-nginx nginx -s reload`。
- admin：`~/unica-run/admin-scene-bin.bak`。

### 下一步

代码与文档就绪，等待提交。C 组三条（C1/C2/C3/C4）至此全部完成，剩 B 组。

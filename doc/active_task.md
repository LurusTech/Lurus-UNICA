# Active Task: 配置面重构 阶段 4 · D 组（会话）—— 前端接 refresh、按账号索引的「记住我」、D22 签发唯一性

## Context

门户「老要重登」是两件事叠加：access token 2 小时到期，以及 token 只存 `sessionStorage`、
关标签页即丢。后端 `POST /auth/refresh` 早已实现（一次性、可撤销、哈希存 Redis），
门户从未调用过。本增量让前端接上这条流程并提供「记住我」，同时修掉 D22
（同秒签发的 token 字节级相同）——自动续期一旦上线，D22 会从「理论问题」变成真实的续期互废。

预期结果：验收第 5、6 条成立——两个标签页各登各的账号互不干扰、关浏览器重开两个账号都在；
会话空放 3 小时后继续操作不被踢回登录页。

**执行前必读的五个判断：**

1. **抽公共 `portal/auth.js`，不再八份内联。** 八个页面里 `getToken/clearToken/decodeClaims/api`
   已经是逐字复制的四份代码（`admin.html:999-1060`、`home.html:275-355`、`knowledge.html:435-547` 等），
   refresh 逻辑再抄八遍就是 D8/D16 的形态——修一处漏七处。`auth.js` 用 ES5 IIFE 挂
   `window.UnicaAuth`（与现有页面风格一致，不用 module），页面通过 `<script src="./auth.js?v=1">`
   引入，用 query 串做缓存失效。代价：每页多一次静态请求（门户 nginx 已开 gzip、
   `portal/` 整目录只读挂载，无部署改动）；页面级差异（401 后是跳 `index.html`
   还是显示内嵌登录表单）通过 `UnicaAuth.onSessionLost(fn)` 回调保留在页内，
   `auth.js` 不碰任何页面 DOM。**不**顺手把四个带内嵌登录表单的旧页改成跳转——那是另一个增量。

2. **并发 401 用模块级单飞 Promise 串行化。** `auth.js` 内维护 `refreshInFlight`：
   第一个 401 发起 `POST /auth/refresh` 并把 Promise 存下，同一时间窗内其余 401 全部
   await 这同一个 Promise，成功后各自用新 access token 重试**一次**
   （`options.__retried` 标记防死循环），失败则全部以 `unauthorized` 抛出并触发 `onSessionLost`。
   跨标签页同账号的竞态（两个标签页持同一个一次性 refresh token）用「先读表再刷、刷败再读表」处理：
   发 refresh 前先读 localStorage 会话表，若该账号条目里的 refresh token 已与本标签页手里的不同，
   说明别的标签页刚换过——直接采用表里的新对，不再调 refresh；refresh 返回 401 时再读一次表
   做同样判断，仍然相同才判定会话失效。

3. **D2 的存储边界：两层，各管一事。**
   - **标签页层（`sessionStorage`，不变）**：`unica_access_token`（沿用现有键名，所有读者不用改）
     加新增 `unica_refresh_token`。每个标签页各持一对，这一层就是今天「多账号互不干扰」的来源，
     本增量**原样保留**。当前标签页用的是哪个账号，由本页 access token 的 `user_id` claim 推出，
     不另设键。
   - **记住层（`localStorage["unica_sessions"]`）**：一张按 `user_id` 索引的表
     `{ "<user_id>": { email, role, tenant_id, access_token, refresh_token, saved_at } }`。
     **只在登录时勾了「记住我」才写入**；未勾的账号永远不进这张表，关标签页即消失，与今天完全一致。
     每次 refresh 成功后，若表里存在该 `user_id` 的条目就同步更新它
     （保证表里永远是最新的、未被消费的 refresh token）。
   - **选用规则**（`UnicaAuth.bootstrapTab()`，所有页面共用）：标签页已有 token 则用它；
     没有则读表并剔除 refresh token 已过期（解 `exp`）的条目；表里**恰好一个**账号则
     静默采用到本标签页；**两个以上**则返回空，页面按现有逻辑落到 `index.html`，
     `index.html` 在登录表单上方列出「已保存的账号」（邮箱 + 角色/租户）供点选，
     点选即把该条目复制进本标签页的 `sessionStorage` 再分流。
     因此 localStorage 不会破坏隔离：它只是账号仓库，标签页从不直接从它读 token 去发请求，
     永远先落到自己的 `sessionStorage`。
   - **登出**：清本标签页两个键，加删表中该 `user_id` 条目（登出即忘记）。
     服务端无 revoke 端点，不在本增量加。
   - 「在新标签页中打开」复制 sessionStorage 的浏览器行为不修；在 `index.html` 账号选择区加一行说明文字。

4. **`AccessTokenTTL` 不改成可配。** 理由三条：原方案第六条已明确否决「调长 access token」，
   加环境变量等于给这条否决留后门；D1 做完后这个数值对体验完全无感，没有任何消费者需要调它；
   阶段 4 的方向是配置从 env 走向库与门户（B 组），此时新增一个 env 开关是方向性倒退。
   2 小时作为安全常量硬编码是正确的。测试「到期后自动续期」不需要缩短 TTL——
   往 `sessionStorage` 塞一个格式合法但签名无效的 token，中间件走的是同一行 401
   （`middleware.go:96-99`），效果等价。

5. **验证分工**：D3 走 Go 单测（同秒两次签发必不同、access 与 refresh 的 `jti` 互异、旧测试全绿）；
   D1/D2 全部实机点，理由是它们的正确性完全在浏览器存储与网络时序里，Go 侧一行没改。

## Critical Files

- `portal/auth.js`（新建）
- `portal/index.html`
- `portal/admin.html`
- `portal/home.html`
- `portal/ai-settings.html`
- `portal/knowledge.html`
- `portal/channels.html`
- `portal/ontology.html`
- `portal/violations.html`
- `unica/admin/internal/auth/jwt.go`
- `unica/admin/internal/auth/jwt_test.go`
- `doc/known-defects.md`（D22 结案）
- `doc/plan-workbench-settings.md`（D1-D3 打勾）
- `doc/测试信息.md`（补「记住我」与新标签页复制坑）

## Step-by-Step Plan

- [x] 1. **D3 后端**：`unica/admin/internal/auth/jwt.go` 的 `GenerateTokenPair` 中，为 access 与 refresh
      两组 `RegisteredClaims` 各填一个独立的 `ID`（`jti`），值由 `crypto/rand` 取 16 字节转 hex
      （不引入新依赖）；抽一个 `newTokenID() (string, error)` 辅助函数，随机源失败时返回错误
      而非退化为空串。
- [x] 2. **D3 单测**：`jwt_test.go` 新增 `TestGenerateTokenPair_UniquePerIssue`——同一 manager、
      同一入参连续调用两次，断言两次的 `AccessToken`、`RefreshToken` 均不同，且解出的 `claims.ID`
      非空、access 与 refresh 的 `ID` 互异。运行 `go test ./internal/auth/...`（在 `unica/admin` 下），
      要求全绿。
- [x] 3. **新建 `portal/auth.js`**，ES5 IIFE 挂 `window.UnicaAuth`，导出：
      - 存储原语：`accessToken()` / `refreshToken()`（读 `sessionStorage`，try/catch 包裹）、
        `decodeClaims(token)`（从现有页面原样搬入）、`adoptPair(pair, {remember})`
        （写本标签页两键；`remember` 为真、或表中已有该 `user_id` 条目时，同步写 `localStorage["unica_sessions"]`）。
      - 会话表：`savedAccounts()`（读表、剔除 refresh token `exp` 已过的条目并回写）、`forgetAccount(userId)`。
      - `bootstrapTab()`：按判断 3 的选用规则返回 claims 或 `null`。
      - `login(email, password, remember)`：POST `/api/v1/auth/login`，成功后 `adoptPair`，返回 claims；
        错误信息沿用现有「邮箱或密码错误」兜底。
      - `logout()`：清两键，加 `forgetAccount(当前 user_id)`。
      - `onSessionLost(fn)`：注册页面级回调。
      - `api(path, options)`：现有八份 `api` 的**超集**——`FormData` 不加 JSON 头（`knowledge.html:522`）、
        错误对象带 `status` 与 `data`（`admin.html:1049-1054`）；401 分支改为：`options.__retried`
        为真则直接判失效；否则调 `refreshAccess()`（步骤 4），成功后以 `__retried=true` 重发原请求，
        失败则调 `onSessionLost` 回调并抛 `Error("unauthorized")`（`status=401`，
        保证各页 `err.message === "unauthorized"` 的静默分支继续成立）。
- [x] 4. **在 `auth.js` 内实现 `refreshAccess()`**（判断 2）：模块级 `refreshInFlight`；
      进入时先 `reconcileFromTable()`——读表中本账号条目，若其 refresh token 与本标签页不同
      则采用并 resolve，不发请求；否则若 `refreshInFlight` 已存在直接返回它；
      否则发起 `POST /api/v1/auth/refresh`，200 则 `adoptPair(新对)` 并 resolve；
      401 则再 `reconcileFromTable()` 一次，仍无新对才 reject；
      任何分支结束都把 `refreshInFlight` 置 `null`。
- [x] 5. **改 `portal/index.html`**：引入 `auth.js`；删本页 `getToken/setToken/clearToken/decodeClaims`；
      登录表单加「记住我（7 天内免登录）」复选框；
      提交改调 `UnicaAuth.login(email, password, remember)` 后 `dispatch(claims)`；
      Init 改为 `var claims = UnicaAuth.bootstrapTab()`：有则 `dispatch`；
      无且 `savedAccounts()` 不少于 2 个时，在登录表单上方渲染账号列表（邮箱 + 角色/租户名，
      每项一个按钮，点选后 `adoptPair(该条目)` 并 `dispatch`，附一个「忘记」小按钮调 `forgetAccount`），
      并加一行说明：从已登录页面右键「在新标签页中打开」会沿用原标签页账号，
      要切换账号请手动新开标签页粘地址；无且表空则 `showLogin()`。
- [x] 6. **改 `admin.html` / `home.html` / `ai-settings.html`**（跳转型页）：引入 `auth.js`；
      删本页四个重复函数，改为委托 `UnicaAuth.api` / `UnicaAuth.decodeClaims` / `UnicaAuth.accessToken`；
      `goLogin` 改为先 `UnicaAuth.logout()` 再 `window.location.replace(LOGIN_PAGE)`；
      页面启动处的 `getToken()` 判空改为 `UnicaAuth.bootstrapTab()` 判空；
      注册 `UnicaAuth.onSessionLost(...)`。
      **注意登出按钮与会话失效要区分**：登出按钮走 `logout()`（删表条目），
      会话失效回调只清本标签页两键再跳转，不删表——否则一次 refresh 失败会把「记住我」一起抹掉。
      为此 `auth.js` 额外导出 `dropTab()`（只清本标签页），`onSessionLost` 的页面回调用它。
- [x] 7. **改 `knowledge.html` / `channels.html` / `ontology.html` / `violations.html`**（内嵌登录型页）：
      引入 `auth.js`；同步骤 6 替换四个函数与 `getToken`；登录表单加同款「记住我」复选框，
      提交改调 `UnicaAuth.login(...)` 后沿用各页 `showApp(); bootstrap()`；
      `showLogin()` 内的 `clearToken()` 改为 `UnicaAuth.dropTab()`；
      登出按钮改调 `UnicaAuth.logout()` 再 `showLogin()`；
      注册 `onSessionLost` 回调显示「登录已过期，请重新登录」；
      Init 处 `getToken()` 判空改为 `UnicaAuth.bootstrapTab()`。
- [x] 8. **全仓核对**：`rg -n "sessionStorage|localStorage" portal/` 的命中应只剩 `portal/auth.js`；
      `rg -n "function api\(|function decodeClaims\(" portal/*.html` 应为零命中。
- [x] 9. **部署与实机验证 D3**：重编 admin（在 `unica/admin` 下 `go build ./cmd/admin`），
      按 `doc/测试信息.md` 第四节「先 kill 再 cp」换二进制（用端口取 PID，等 `kill -0` 失败再 cp）；
      `curl` 连续两次登录 `rehearsal@unica.local`，断言两次 `access_token` 与 `refresh_token` 均不同。
- [x] 10. **实机验证 D1**（浏览器门户地址与账号见 `doc/测试信息.md` 第三节）：
      - 不勾记住我登录 AJYJ 用户进 `home.html`；DevTools Console 里把 `unica_access_token`
        改成一个格式合法但签名无效的串；刷新页面。Network 面板断言：**恰好一次**
        `POST /auth/refresh` 200，随后各业务请求 200，页面不跳登录（并发 401 只刷一次）。
      - 再把 access 与 refresh 两个键都置坏，刷新：断言 refresh 401 后落回 `index.html`，
        且 `localStorage.unica_sessions` 未被写入（未勾记住我从不进表）。
      - `knowledge.html` 重复第一条，确认 `FormData` 上传路径在续期重试后仍成功。
- [x] 11. **实机验证 D2**（验收第 5 条）：标签页 A 勾记住我登录 AJYJ 用户，
      标签页 B 勾记住我登录 `rehearsal@unica.local`（admin）；各自操作（A 改阈值、B 看租户列表）
      互不干扰；`localStorage.unica_sessions` 含两个 `user_id` 键。
      关闭整个浏览器重开 `index.html`：出现两个账号的选择列表；分别在两个新标签页里点选，
      各自进入对应工作区。在 A 里手动把 access token 置坏触发续期，
      随后在 B 的 Console 读 `localStorage.unica_sessions`，断言 AJYJ 条目的 `refresh_token`
      已变而 admin 条目未变（表按账号隔离更新）。在 A 点登出：表中 AJYJ 条目消失、
      admin 条目保留；B 不受影响。
- [x] 12. **实机验证同账号双标签页竞态**：勾记住我登录 AJYJ 后，从该页右键「在新标签页中打开」
      任一链接（复制 sessionStorage），两个标签页都把 access token 置坏后先后刷新：
      第二个标签页不得被踢回登录（它应通过 `reconcileFromTable` 采用第一个标签页换到的新对）。
- [x] 13. **文档收尾**：`doc/known-defects.md` 的 D22 标题加「（已解，YYYY-MM-DD）」并写一句根因与修法；
      `doc/plan-workbench-settings.md` 的 D1-D3 打勾、「当前状态」更新；
      `doc/测试信息.md` 第九节坑表加「在新标签页中打开沿用原账号」一行，
      第三节账号表下补一句「记住我」存在 `localStorage.unica_sessions`、登出即删。
- [x] 14. **最终验证**：在 `unica/admin` 下 `go test ./...` 全绿；步骤 8 的两条 `rg` 为预期结果；
      步骤 10-12 全部通过后，把步骤 9-12 的结论逐条写回本文件 Current Status。

## Deferred（不在本增量）

- 服务端登出端点（撤销 refresh token）：目前登出只忘客户端，服务端 7 天内该 refresh token 仍可用。
- 四个内嵌登录页统一改为跳转 `index.html`，以及 `?next=` 登录后回到原页。
- **待登记的观察**：refresh token 无 `role` / `tenant_id`，却能通过 `AuthMiddleware` 的签名校验
  被当作 Bearer 使用；现有 `RequireAdmin` / `TenantAuth` 会 403，但只挂 `AuthMiddleware` 的路由
  （如 `/api/v1/audit-logs` 的用户自动租户过滤）在 `TenantID` 为空时的行为需要单独核实。
  建议登记为新缺陷，不在此修。

## Current Status

- [x] **步骤 1-14 全部完成，实机验收通过。未提交。**

### 实机验收结果（2026-09-02）

环境：本地 WSL（UbuntuE），WSL IP 172.17.158.157，门户 :3401，admin :8081。
admin 二进制从 Windows 交叉编译（WSL 里没装 Go），按坑表用端口取 PID、
等 `kill -0` 失败再 cp，旧二进制留在 `~/unica-run/admin-scene-bin.bak` 可回滚。
浏览器验证用 Playwright（Chrome 扩展未连）。

**步骤 9 · D3** — 连续两次登录，access 与 refresh 全不同；三个 `jti` 两两不同。

**步骤 10 · D1**
- access token 签名毁掉、refresh 完好：**恰好一次** `POST /auth/refresh` 200，
  `tenants/me` 走 `[401, 200]`，页面留在 `home.html`。
- 两个 token 都毁掉：refresh 401，落回 `index.html`，登录表单真的在屏幕上；
  未勾记住我的账号**没有**进金库；标签页带上了会话丢失标记。
- **六路并发 401**（`home.html` 只发一个业务请求，压不到并发，故直接发六个）：
  仍然只有**一次**续期，六个请求全部成功。
- multipart：`POST 401 → POST 201`，续期重试后边界没被破坏。

**步骤 11 · D2** — 两账号并存、各自工作区、金库两条按 `user_id` 索引；
A 续期后只有 A 的条目变、B 的没动；关掉整个浏览器重开，选择器列出两个账号，
分别点选各进各的工作区；A 登出只删 A 的条目，B 不受影响且仍能调 API。

**步骤 12 · 同账号双标签页竞态** — B 持已被 A 花掉的 refresh token 时
**一次续期请求都没发**，直接从金库取到 A 轮换出的新对；三个并发请求同样如此。

### 验收中发现并修掉的计划外缺口

计划假设「能攒出两个被记住的账号」，实机上这个前提不成立：只要有一个账号被记住，
任何新标签页打开 `index.html` 都会静默采用它并跳进工作台，登录表单到不了；
唯一出路「登出」又会忘掉这个账号。于是第二个账号永远加不进来，
账号选择器在真实使用中触发不到——而「一个人管两个账号」正是这一条最初的来由。
修法：`index.html` 认 `?switch=1` 时不做静默采用；`home.html` / `admin.html`
页眉加「切换账号」链接。静默采用本身没错，错在没留出口。

### 顺带定性的一条新缺陷

**D23（低危，未修）**：refresh token 能当 Bearer 用。`AuthMiddleware` 只验签名、
不区分 token 类型。实测 `/api/v1/tenants` 与 `/api/v1/platform/settings` 被
`RequireAdmin` 挡成 403，而只挂 `AuthMiddleware` 的 `/api/v1/audit-logs` 返回
**200 但零行**（refresh token 的 `tenant_id` 为空，按空租户过滤的结果是空而不是全部）。
**没有数据泄露**，是类型卫生问题。已登记进 `known-defects.md`。

### 遗留

- **AJYJ 知识库里留了一个探针文档** `refresh-retry-probe.txt`
  （`document_id` `7aa7c16e-c628-456f-95b9-19f5c2aa4c41`），是步骤 10 的 multipart
  验证产物。删除是不可逆操作，留给人工在知识库页处理。
- `adoptPair` 整表读改写在跨标签页下理论上可互相覆盖（低危）。读写是相邻语句、
  中间无 await，窗口已等于同步块本身，单键 localStorage 下无法再收窄。
- Deferred 三条不在本增量内。

### 下一步

代码与文档均已就绪，等待提交。B / C 两组见 `doc/plan-workbench-settings.md`。

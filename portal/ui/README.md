# portal/ui — 门户共享界面层

各页面原先各自内联的品牌变量、toast、对话框、筛选片，以及事件流需要的类别色板、全局顶栏、注意力信号，集中在这里一份。

- `unica-ui.css`：品牌变量（亮/暗）、组件样式、类别色板、顶栏、"新"标记与变化高亮、减少动效。
- `unica-ui.js`：IIFE，挂到 `window.UnicaUI`。无依赖、无打包，ES2017 以内。

## 引入

放在 `auth.js` 之后、页面自身脚本之前：

```html
<link rel="stylesheet" href="./ui/unica-ui.css?v=1">
...
<script src="./auth.js?v=1"></script>
<script src="./ui/unica-ui.js?v=1"></script>
<script>
  // 页面原有的 bootstrapTab / 登录跳转逻辑照旧，之后一行挂顶栏：
  UnicaUI.topbar.mount({ title: "质量复核" });
</script>
```

`auth.js` 不是硬依赖：没有它或没有会话时，顶栏只显示品牌与标题，事件流不连接，不报错。

## 默认不打扰

- 提示音默认**关**，在顶栏点"声音"才开；选择存在 `localStorage`。
- 浏览器通知默认**关**，在顶栏点"通知"并授权后才开；只在页面不在前台时、只对 `urgent` 事件发。
- 首屏回放的历史事件（`meta.initial === true`）从不发声、不发通知。
- 没调用 `topbar.mount` 的页面，除了按需调用的函数之外没有任何行为变化。

## API

### 基础

| 签名 | 说明 |
|---|---|
| `UnicaUI.tenant()` | 当前租户路径段。管理员取地址栏 `?tenant=`（没有则 `""`），其他账号为 `"me"`；无会话为 `""`。与各页 `tenantSegment()` 规则一致。 |
| `UnicaUI.withTenant(href)` | 管理员代管时给门户内链接补上 `?tenant=`。 |
| `UnicaUI.currentUserId()` | 当前账号 `user_id`（`auth.js` 未导出 `currentUserId`，这里读同一个 claim）。 |
| `UnicaUI.toast(message, kind?, link?, {duration?}?)` | `kind`：`success` / `error` / `warn` / `info`。`link` 为 `{href, text}`（violations/proposals 现用签名）或字符串 href。带链接停留 9 秒，错误 5.2 秒，其他 3.6 秒；`duration`（毫秒）只能延长、不会缩短。通过 live region 播报，错误用 assertive。 |
| `UnicaUI.modal.open(elOrId, {trigger?, onClose?, dismissible?, closeOnBackdrop?})` | 接受遮罩（`.uu-modal-backdrop` 或旧的 `.modal-backdrop`）、其中的对话框，或用 `hidden` 控制显隐的元素。焦点进框、Tab/Shift+Tab 循环、Esc 关闭、点遮罩关闭。`dismissible` / `closeOnBackdrop` 可以是布尔值，也可以是函数（关闭那一刻再问，返回 `false` 即拒绝；例如一次性密码显示时拒绝 Esc）。`onClose` 不论哪种方式关闭都会执行，清理逻辑放这里。返回句柄 `{close()}`。可叠加，Esc 与焦点陷阱只作用于最上层。 |
| `UnicaUI.modal.close(elOrHandle?)` | 不传则关最上层。焦点回到 `trigger`（默认是打开时的 `document.activeElement`）。 |
| `UnicaUI.chips(container, {multi?, onChange?(value, btn)})` | 容器内 `button.uu-chip` / `button.chip` / `button[data-chip]` 点击切换并维护 `aria-pressed`。值取 `data-value`，其次 `data-status`（现有页面写法），再次文本。单选时再点已选中的不取消。返回 `{value(), set(v), destroy()}`。 |
| `UnicaUI.liveRegion.announce(text, politeness?)` | `"polite"`（默认）或 `"assertive"`。 |

### 类别与标签

| 签名 | 说明 |
|---|---|
| `UnicaUI.categories[cat]` | `{label, glyph, color, sound, icon}`。`cat` 即 `Event.category`：`forced_handoff` 强制转人工 / `withheld` 答复已扣下 / `reducible_handoff` 可削减的转人工 / `violation` 事实冲突 / `ontology` 本体变更 / `config` 配置变更。`color` 是 `var(--cat-*)`，`icon` 是内联 SVG 字符串，每类形状不同。 |
| `UnicaUI.category(cat)` | 同上，未知类别给出中性兜底。 |
| `UnicaUI.icon(cat)` | 返回该类别的 SVG 元素（`aria-hidden`）。 |
| `UnicaUI.categoryPill(cat)` | 返回 `span.uu-pill.uu-cat`：图标 + 中文名 + 类别色。类别只用这一种方式展示，颜色从不单独出现。 |
| `UnicaUI.severities[sev]` | `{label, pill}`：`urgent` 紧急 / `attention` 需关注 / `info` 通知。 |
| `UnicaUI.reasonLabel(reason)` | `Event.reason` 的中文：转人工原因（原 evidence.html）、违规 kind（原 violations.html）、建议 kind 与决定（原 proposals.html）、审计动作（publish/rollback/review/create/update/delete）。未知原样返回。 |
| `UnicaUI.labels.{handoffReason, violationKind, proposalKind, proposalDecision, auditAction}` | 上面合并前的分表，页面迁移时用来替换本地 `REASON_LABELS` / `KIND_LABELS`。 |

### 声音与注意力

| 签名 | 说明 |
|---|---|
| `UnicaUI.sound.forEvent(ev)` | 该事件应发的音。只有两类事件会响：`severity` 为 `urgent` 的（`forced_handoff` 双音、`withheld` 低音），以及别人发布/回滚本体（`ontology` 且 `reason` 为 publish/rollback 且 `actor` 不是自己，轻提示；认不出自己时不响）。其余 `null`。 |
| `UnicaUI.sound.play(kind)` | WebAudio 合成；同一种音 10 秒内最多一次；静音或尚无用户交互时不发声（AudioContext 在首次 pointerdown/keydown 后才创建）。 |
| `UnicaUI.sound.isMuted()` / `setMuted(bool)` | 静音状态，存 `localStorage["unica_ui_sound"]`。 |
| `UnicaUI.attention.set(n)` | 标题前缀 `"(n) "` 与 favicon 数字角标；`0` 还原原标题与原 favicon。顶栏会自动调用。 |
| `UnicaUI.notify.{supported, enabled, enable, disable, maybe(ev, meta)}` | 浏览器通知。`enable()` 会请求权限，返回 Promise<bool>。 |

### 变化可视化

| 签名 | 说明 |
|---|---|
| `UnicaUI.countTo(el, n, {duration?, format?, instant?})` | 数字滚动到 `n`，涨/跌短暂着色。减少动效、页面隐藏或 `instant` 时直接落定。 |
| `UnicaUI.flashRow(el)` | 新行淡色高亮后淡出（约 2 秒）。减少动效时不做。 |
| `UnicaUI.seen.get(tenant, key)` / `set(tenant, key, value)` | "上次看过"的游标，存 `localStorage["unica_ui_seen:<tenant>:<key>"]`，值不透明（事件游标、时间戳均可）。 |
| `UnicaUI.seen.isNewer(tenant, key, isoTime)` | 时间晚于已存值才为真；从没存过则一律为假（首次访问不标"新"）。 |
| `UnicaUI.seen.badge()` | 返回 `span.uu-new`"新"。 |

### 事件流

| 签名 | 说明 |
|---|---|
| `UnicaUI.events.subscribe(tenant?, handler(ev, meta), {onStatus?, tail?})` | 返回取消函数。`tenant` 省略即 `UnicaUI.tenant()`。同一租户的多个订阅者共享一条连接。`meta = {initial, tenant}`；后加入的订阅者先收到已缓存的事件（`initial: true`）。`tail` 是首屏取回的条数（默认 100，上限 300），只由打开这条连接的第一个订阅者决定。 |
| `UnicaUI.events.unsubscribe(tenant, handler)` | 最后一个订阅者离开时断开。 |
| `UnicaUI.events.status(tenant?)` / `onStatus(tenant, fn)` | `idle` / `connecting` / `connected` / `reconnecting` / `polling` / `disconnected`。 |
| `UnicaUI.events.list(tenant, {cursor?, limit?, tail?})` | 列表接口一页，翻历史用。 |

连接过程：先 `GET /api/v1/tenants/{id}/events?tail=<tail>` 取最近事件并拿到游标；再用 `fetch` 读 `/events/stream`（`Authorization: Bearer` 头 + `Last-Event-ID`，令牌不进 URL），自己解析 SSE（`id` / `event` / `data` / `:` 心跳）。60 秒无任何字节视为断线。断线按 1s→2s→4s… 上限 30s 加抖动重连；连续 3 次失败，或服务端返回非 200 / 非 `text/event-stream`，降级为每 15 秒轮询列表接口，之后每 5 分钟再试一次流。事件按 `id` 去重，切换过程中不重复投递。流返回 401 时借 `UnicaAuth.api` 续期一次再重连，续期失败交给 `auth.js` 的 `onSessionLost`；续期成功后流仍返回 401，则降级为轮询。离开页面（`pagehide`）时立即中止所有流；页面从往返缓存恢复（`pageshow` 且 `persisted`）时按游标续上。

### 顶栏

`UnicaUI.topbar.mount({title, tail?, pendingFilter?})` → `{el, setTitle(text), pending(), recount(), openJump(), destroy()}`。重复调用返回同一个实例。

| 签名 | 说明 |
|---|---|
| `UnicaUI.topbar.refresh(opts?)` | 按当前会话与地址栏重建顶栏（登录后调用）；`opts` 省略沿用上次的。 |
| `UnicaUI.topbar.unmount()` | 退出登录时卸下顶栏并断开它的事件流。 |
| `UnicaUI.topbar.setTenant(id)` | 管理员在页面里切换租户：把 `?tenant=` 写回地址栏（`replaceState`）并重建顶栏，租户名、待办数、事件流、各链接随之切换。非管理员无操作。 |
| `UnicaUI.topbar.current()` | 当前顶栏句柄，没有则 `null`。页面保存的旧句柄在重建后调用 `destroy()` 不会拆掉新顶栏。 |
| `pendingFilter(ev)` | 返回 `false` 的事件不计入待办数（live.html 用它排除本浏览器"标为已读"的事件）；判断依据变了调用 `recount()`。 |
| `tail` | 传给事件流的首屏条数，让顶栏待办数与页面首屏统计同一批事件。 |

在 `<body>` 最前插入 `nav.uu-topbar`（不是 `<header>`，避免吃到各页的 `header {}` 样式）：

- 品牌 → `home.html`（管理员未带租户时 → `admin.html`）；管理员选了租户时另有"平台管理"链接（admin.html 本页除外）；租户名（`GET /api/v1/tenants/{id}` 的 `display_name`/`name`，失败显示 id）；当前位置。
- 待办计数：事件流中 `urgent` + `attention` 且未被后续事件 `resolves`、且未被 `pendingFilter` 排除的条数，点击去 `live.html`。统计范围是首屏取回的最近 `tail` 条加此后的实时事件。同时驱动标题前缀与 favicon 角标。没有租户（未登录、管理员未带 `?tenant=`）时不显示。
- 顶栏挂载期间给页面设 `scroll-padding-top`，页内跳转与聚焦定位不会被吸顶的顶栏挡住。
- 连接状态：实时 / 定时刷新（每 15 秒）/ 已断开，正在重连。圆点形状（实心/空心/虚线）与文字一起表达，不只靠颜色。
- 声音、通知开关（`aria-pressed`）。
- Ctrl+K（Mac 上 ⌘K）快速跳转：页面名模糊匹配；输入像会话号的串（一个不含空格且带数字的 token）时第一项是 `evidence.html?conversation=<id>`。

## 类名约定

- 所有组件类带 `uu-` 前缀：`uu-btn`、`uu-pill`、`uu-chip(s)`、`uu-toast(-host)`、`uu-modal(-backdrop/-actions/-sub)`、`uu-topbar`、`uu-new`、`uu-flash`、`uu-count`、`uu-sr-only`、`uu-icon`。未迁移页面的 `.toast` / `.modal` / `.chip` / `.pill` 不受影响。
- 修饰类沿用现有写法，不带前缀：`.primary` `.danger` `.ghost` `.sm`（按钮），`.ok` `.bad` `.warn` `.info` `.muted`（pill），`.success` `.error` `.warn` `.info`（toast），`.wide`（对话框），`.open`（遮罩打开态）。
- 设计变量沿用现有名字：`--lt-*`、`--bg` `--surface` `--text` `--text-muted` `--border` `--primary` `--primary-hover` `--row-hover` `--ok` `--bad` `--warn` `--info` `--shadow`；新增 `--scrim` `--shadow-modal` `--lt-font-mono` `--cat-<category>` `--uu-new` `--uu-topbar-h`。
- `:root[data-theme="dark"|"light"]` 的强制主题照旧生效。
- 本文件不给任何裸元素（`button`、`header`、`body`）加样式。唯一例外是减少动效：`prefers-reduced-motion: reduce` 下对所有元素压掉动画与过渡时长、循环只播一次，页面自己的加载转圈、脉冲、悬停上浮也一并停下。

## 迁移清单（每页）

1. `<head>` 引入 `ui/unica-ui.css`，`auth.js` 之后引入 `ui/unica-ui.js`。
2. 删除 `<style>` 里的品牌变量：两段 `:root {...}`、`@media (prefers-color-scheme: dark) {...}`、`:root[data-theme="dark"]`、`:root[data-theme="light"]`。页面独有的变量（如果有）留在页面里。
3. 删除 `#toast-host`、`.toast*`、`@keyframes slide-in` 样式，删除 `<div id="toast-host">` 与 `els.toastHost`；本地 `function toast(...)` 改为 `var toast = UnicaUI.toast;`，调用处不用改。打印样式里的 `#toast-host` 换成 `.uu-toast-host`（`unica-ui.css` 已对 `.uu-toast-host`、`.uu-topbar` 做了 `@media print` 隐藏）。
4. 对话框：`.modal-backdrop` → `.uu-modal-backdrop`，`.modal` → `.uu-modal`，`.modal-actions` → `.uu-modal-actions`，`.modal-sub` → `.uu-modal-sub`，删除本地样式；`classList.add("open")` / `remove("open")` 改为 `UnicaUI.modal.open(id, {trigger: btn})` / `UnicaUI.modal.close()`，删除页面自己的 Esc 与遮罩点击监听。过渡期也可以先只换调用、保留旧类名，`modal.open` 认识旧类名。
5. 筛选片：`.chips` → `.uu-chips`，`.chip` → `.uu-chip`，删本地样式；点击委托与 `aria-pressed` 维护改为 `UnicaUI.chips(container, {onChange})`（`data-status` 不用改）。
6. `.pill` → `.uu-pill`；按钮的 `.primary` 等可直接加 `uu-btn`，或保留页面自己的 `button {}` 规则。
7. 转人工原因、违规 kind 的本地中文表换成 `UnicaUI.labels.*` 或 `UnicaUI.reasonLabel`。
8. 页首加一行 `UnicaUI.topbar.mount({title: "..."})`。页面原 `<header>` 里与顶栏重复的部分（返回首页、租户名）可以删，账号与退出按钮留在页面。
9. 本地 `queryParam`/`requestedTenant`/`tenantSegment` 可改用 `UnicaUI.tenant()` / `UnicaUI.withTenant()`，规则相同。

## 已知缺口

- `auth.js` 未导出 `currentUserId`，这里从 access token 的 `user_id` claim 读取；若以后导出，会优先用导出的版本。
- 提示音按音色分别节流（同一种音 10 秒内最多一次），不同音色之间不互相压制。

// UNICA portal shared interface layer.
//
// What every page used to re-implement inline (toasts, modal focus handling,
// filter chips) plus what the event feed adds on top: one shared connection
// per tenant, the category table that decides colour, icon and sound, and the
// attention signals (title count, favicon badge, opt-in notifications).
//
// Rules this module keeps, so pages can rely on them:
//   * No dependencies, no build step. auth.js is used when present and never
//     required: without a session nothing connects and nothing breaks.
//   * Quiet by default. Sound starts muted and notifications start off; both
//     are switched on by the visitor in the top bar and remembered per browser.
//     Events replayed from history never make a sound or raise a notification.
//   * The access token never goes into a URL. The stream is read with fetch,
//     which can send the Authorization header that EventSource cannot.
//   * prefers-reduced-motion is honoured in script as well as in CSS: numbers
//     land at once and highlights are skipped.
(function (window, document) {
  "use strict";

  var TENANT_ROOT = "/api/v1/tenants/";
  var TENANT_ME = "me";
  var PREF_PREFIX = "unica_ui_";

  // ---------- Small helpers ----------
  function lsGet(key) {
    try { return window.localStorage.getItem(PREF_PREFIX + key); } catch (e) { return null; }
  }

  function lsSet(key, value) {
    try {
      if (value === null || value === undefined) window.localStorage.removeItem(PREF_PREFIX + key);
      else window.localStorage.setItem(PREF_PREFIX + key, String(value));
    } catch (e) { /* preferences are a convenience; the session still works */ }
  }

  function byId(target) {
    return typeof target === "string" ? document.getElementById(target) : target;
  }

  function reducedMotion() {
    return !!(window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches);
  }

  function queryParam(name) {
    var m = new RegExp("[?&]" + name + "=([^&]*)").exec(window.location.search);
    return m ? decodeURIComponent(m[1].replace(/\+/g, " ")) : "";
  }

  function auth() { return window.UnicaAuth || null; }

  function claims() {
    var a = auth();
    if (!a || !a.decodeClaims || !a.accessToken) return null;
    return a.decodeClaims(a.accessToken());
  }

  function isAdmin() {
    var c = claims();
    return !!c && c.role === "admin";
  }

  // auth.js keeps currentUserId private; the claim it reads is the same one.
  function currentUserId() {
    var a = auth();
    if (a && typeof a.currentUserId === "function") return a.currentUserId();
    var c = claims();
    return c && c.user_id ? String(c.user_id) : "";
  }

  // The tenant this page is about, resolved the way the pages already do it:
  // only an administrator names a tenant on the address; everyone else is
  // "me", which the server resolves from the token. An administrator with no
  // ?tenant= has no tenant at all and gets "", and so does a tab with no
  // session: there is nobody for "me" to resolve to.
  function tenant() {
    if (auth() && !claims()) return "";
    if (isAdmin()) return queryParam("tenant");
    return TENANT_ME;
  }

  // Carries ?tenant= across to another portal page, the way the home page
  // cards do. Only an administrator's tenant travels; "me" needs no naming.
  function withTenant(href) {
    var t = isAdmin() ? queryParam("tenant") : "";
    if (!t || /[?&]tenant=/.test(href)) return href;
    return href + (href.indexOf("?") < 0 ? "?" : "&") + "tenant=" + encodeURIComponent(t);
  }

  function apiGet(path) {
    var a = auth();
    if (a && a.api) return a.api(path);
    return window.fetch(path, { cache: "no-store" }).then(function (res) {
      if (!res.ok) {
        var err = new Error("请求失败 (" + res.status + ")");
        err.status = res.status;
        throw err;
      }
      return res.json();
    });
  }

  function h(tag, cls, text) {
    var el = document.createElement(tag);
    if (cls) el.className = cls;
    if (text !== undefined && text !== null) el.textContent = text;
    return el;
  }

  // Icons are fixed strings defined in this file, never data, so parsing them
  // as markup is safe.
  function svg(markup) {
    var holder = document.createElement("span");
    holder.innerHTML = markup;
    return holder.firstChild;
  }

  function onReady(fn) {
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", fn);
    else fn();
  }

  // ---------- Live region ----------
  // Two off-screen regions, created on first use. The text is cleared and set
  // again a tick later so that the same message twice is still announced.
  var liveEls = {};

  function liveEl(politeness) {
    var p = politeness === "assertive" ? "assertive" : "polite";
    if (liveEls[p] && document.body.contains(liveEls[p])) return liveEls[p];
    var el = h("div", "uu-sr-only");
    el.setAttribute("aria-live", p);
    el.setAttribute("aria-atomic", "true");
    if (p === "assertive") el.setAttribute("role", "alert");
    document.body.appendChild(el);
    liveEls[p] = el;
    return el;
  }

  function announce(text, politeness) {
    if (!text || !document.body) return;
    var el = liveEl(politeness);
    el.textContent = "";
    setTimeout(function () { el.textContent = String(text); }, 50);
  }

  // ---------- Toasts ----------
  // Both page signatures are accepted: toast(message, kind) and
  // toast(message, kind, {href, text}). A bare string link is taken as the
  // href with a generic label. The host has no aria-live of its own; each
  // toast is announced through the live region, errors assertively.
  // opts.duration (ms) lets a page keep a long warning up for longer than the
  // default; it never shortens a toast below what its link needs.
  var toastHost = null;

  function ensureToastHost() {
    if (toastHost && document.body.contains(toastHost)) return toastHost;
    toastHost = h("div", "uu-toast-host");
    document.body.appendChild(toastHost);
    return toastHost;
  }

  function toast(message, kind, link, opts) {
    if (typeof link === "string") link = { href: link, text: "查看 →" };
    var el = h("div", "uu-toast" + (kind ? " " + kind : ""));
    el.appendChild(h("div", null, message));
    if (link && link.href) {
      var a = h("a", null, link.text || "查看 →");
      a.href = link.href;
      el.appendChild(a);
    }
    ensureToastHost().appendChild(el);
    announce(message, kind === "error" ? "assertive" : "polite");
    // A toast that offers a link stays long enough to be reached.
    var ttl = link && link.href ? 9000 : (kind === "error" ? 5200 : 3600);
    if (opts && opts.duration > 0) ttl = Math.max(ttl, opts.duration);
    setTimeout(function () {
      if (reducedMotion()) { el.remove(); return; }
      el.classList.add("leaving");
      setTimeout(function () { el.remove(); }, 300);
    }, ttl);
    return el;
  }

  // ---------- Modals ----------
  // open() accepts the backdrop, the dialog inside it, or a bare element shown
  // with the hidden attribute — the shapes the pages already use. Opened modals
  // stack: Esc and the focus trap always belong to the top one.
  var FOCUSABLE = 'a[href], area[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), ' +
    'select:not([disabled]), textarea:not([disabled]), iframe, [tabindex]:not([tabindex="-1"]), [contenteditable="true"]';
  var modalStack = [];

  function isBackdrop(el) {
    return el.classList.contains("uu-modal-backdrop") || el.classList.contains("modal-backdrop");
  }

  function focusables(root) {
    var all = root.querySelectorAll(FOCUSABLE);
    var out = [];
    for (var i = 0; i < all.length; i++) {
      var el = all[i];
      if (el.offsetParent !== null || el.getClientRects().length) out.push(el);
    }
    return out;
  }

  function topModal() { return modalStack.length ? modalStack[modalStack.length - 1] : null; }

  // dismissible and closeOnBackdrop may be fixed values or functions asked
  // at the moment of closing: a dialog that is showing a one-time secret must
  // refuse Esc then, and only then. Anything but an explicit false allows.
  function allowed(opt) {
    if (typeof opt === "function") {
      try { return opt() !== false; } catch (e) { return true; }
    }
    return opt !== false;
  }

  function modalOpen(target, opts) {
    var el = byId(target);
    if (!el) return null;
    opts = opts || {};
    var backdrop = el;
    var dialog = el;
    if (isBackdrop(el)) {
      dialog = el.querySelector('[role="dialog"], [role="alertdialog"], .uu-modal, .modal') || el;
    } else if (el.parentElement && isBackdrop(el.parentElement)) {
      backdrop = el.parentElement;
    }
    for (var i = 0; i < modalStack.length; i++) {
      if (modalStack[i].backdrop === backdrop) return modalStack[i];
    }

    var entry = {
      backdrop: backdrop,
      dialog: dialog,
      trigger: opts.trigger || document.activeElement,
      opts: opts,
      wasHidden: backdrop.hidden,
      onBackdrop: null
    };
    backdrop.hidden = false;
    backdrop.classList.add("open");
    if (!dialog.hasAttribute("role")) dialog.setAttribute("role", "dialog");
    dialog.setAttribute("aria-modal", "true");
    if (!dialog.hasAttribute("tabindex")) dialog.setAttribute("tabindex", "-1");

    if (backdrop !== dialog && opts.closeOnBackdrop !== false) {
      // Only a press that both starts and ends on the scrim closes: a text
      // selection dragged out of the dialog must not throw the form away.
      var downOnScrim = false;
      entry.onBackdrop = {
        down: function (e) { downOnScrim = e.target === backdrop; },
        click: function (e) {
          if (downOnScrim && e.target === backdrop && allowed(entry.opts.closeOnBackdrop)) modalClose(entry);
        }
      };
      backdrop.addEventListener("mousedown", entry.onBackdrop.down);
      backdrop.addEventListener("click", entry.onBackdrop.click);
    }

    modalStack.push(entry);
    var first = dialog.querySelector("[autofocus]") || focusables(dialog)[0] || dialog;
    try { first.focus(); } catch (e) { /* element vanished; nothing to focus */ }
    entry.close = function () { modalClose(entry); };
    return entry;
  }

  function modalClose(target) {
    var entry = null;
    if (!target) entry = topModal();
    else if (target.backdrop) entry = target;
    else {
      var el = byId(target);
      for (var i = 0; i < modalStack.length; i++) {
        if (modalStack[i].backdrop === el || modalStack[i].dialog === el) entry = modalStack[i];
      }
    }
    if (!entry) return;
    var idx = modalStack.indexOf(entry);
    if (idx < 0) return;
    modalStack.splice(idx, 1);
    entry.backdrop.classList.remove("open");
    if (entry.wasHidden || !isBackdrop(entry.backdrop)) entry.backdrop.hidden = true;
    if (entry.onBackdrop) {
      entry.backdrop.removeEventListener("mousedown", entry.onBackdrop.down);
      entry.backdrop.removeEventListener("click", entry.onBackdrop.click);
    }
    var back = entry.trigger;
    if (back && back.focus && document.body.contains(back)) {
      try { back.focus(); } catch (e) { /* trigger is gone */ }
    }
    if (typeof entry.opts.onClose === "function") entry.opts.onClose();
  }

  document.addEventListener("keydown", function (e) {
    var top = topModal();
    if (!top) return;
    if (e.key === "Escape" || e.key === "Esc") {
      if (!allowed(top.opts.dismissible)) {
        // Swallowed rather than passed on: a page-level Esc handler must not
        // act behind a dialog that just refused to close.
        e.preventDefault();
        e.stopPropagation();
        return;
      }
      e.preventDefault();
      e.stopPropagation();
      modalClose(top);
      return;
    }
    if (e.key !== "Tab") return;
    var items = focusables(top.dialog);
    if (!items.length) {
      e.preventDefault();
      top.dialog.focus();
      return;
    }
    var first = items[0];
    var last = items[items.length - 1];
    var active = document.activeElement;
    var inside = top.dialog.contains(active);
    if (e.shiftKey && (active === first || !inside || active === top.dialog)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && (active === last || !inside)) {
      e.preventDefault();
      first.focus();
    }
  }, true);

  // ---------- Chips ----------
  // A row of toggle buttons. Single choice by default (the filter rows on the
  // review pages); { multi: true } lets several stay pressed. The value of a
  // chip is its data-value, falling back to the data-status the existing
  // pages use, then to its text.
  function chipValue(btn) {
    var d = btn.dataset;
    if (d.value !== undefined) return d.value;
    if (d.status !== undefined) return d.status;
    if (d.chip !== undefined) return d.chip;
    return (btn.textContent || "").trim();
  }

  function chips(container, opts) {
    var root = byId(container);
    if (!root) return null;
    opts = opts || {};
    var multi = !!opts.multi || root.getAttribute("data-multi") === "true";
    if (!root.hasAttribute("role")) root.setAttribute("role", "group");

    function buttons() {
      var all = root.querySelectorAll("button.uu-chip, button.chip, button[data-chip]");
      return Array.prototype.slice.call(all);
    }

    buttons().forEach(function (b) {
      if (!b.hasAttribute("type")) b.type = "button";
      if (!b.hasAttribute("aria-pressed")) b.setAttribute("aria-pressed", "false");
    });

    function value() {
      var pressed = buttons().filter(function (b) { return b.getAttribute("aria-pressed") === "true"; }).map(chipValue);
      return multi ? pressed : (pressed.length ? pressed[0] : null);
    }

    function set(v) {
      var want = multi ? (Array.isArray(v) ? v : [v]) : [v];
      buttons().forEach(function (b) {
        b.setAttribute("aria-pressed", want.indexOf(chipValue(b)) >= 0 ? "true" : "false");
      });
    }

    function onClick(e) {
      var btn = e.target.closest ? e.target.closest("button") : null;
      if (!btn || !root.contains(btn) || btn.disabled || buttons().indexOf(btn) < 0) return;
      var was = btn.getAttribute("aria-pressed") === "true";
      if (multi) {
        btn.setAttribute("aria-pressed", was ? "false" : "true");
      } else {
        // Pressing the chosen chip again keeps it chosen: a single-choice row
        // always has an answer.
        if (was) return;
        buttons().forEach(function (b) { b.setAttribute("aria-pressed", b === btn ? "true" : "false"); });
      }
      if (typeof opts.onChange === "function") opts.onChange(value(), btn);
    }

    root.addEventListener("click", onClick);
    return {
      value: value,
      set: set,
      destroy: function () { root.removeEventListener("click", onClick); }
    };
  }

  // ---------- Categories and labels ----------
  var ICON_ATTRS = 'class="uu-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" ' +
    'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"';

  // One entry per Event.category. Each has its own icon shape, so the
  // swimlanes stay apart for anyone who cannot tell the colours apart.
  // sound: which tone a live event of this category may make (see sound
  // engine); null means it never makes one.
  var CATEGORIES = {
    forced_handoff: {
      label: "强制转人工",
      glyph: "☎",
      color: "var(--cat-forced_handoff)",
      sound: "double",
      icon: '<svg ' + ICON_ATTRS + '><path d="M3 9V8a5 5 0 0 1 10 0v1"/><rect x="2" y="9" width="3" height="4.5" rx="1"/><rect x="11" y="9" width="3" height="4.5" rx="1"/></svg>'
    },
    withheld: {
      label: "答复已扣下",
      glyph: "⛔",
      color: "var(--cat-withheld)",
      sound: "low",
      icon: '<svg ' + ICON_ATTRS + '><path d="M5.5 1.5h5l4 4v5l-4 4h-5l-4-4v-5z"/><path d="M5 8h6"/></svg>'
    },
    reducible_handoff: {
      label: "可削减的转人工",
      glyph: "↘",
      color: "var(--cat-reducible_handoff)",
      sound: null,
      icon: '<svg ' + ICON_ATTRS + '><path d="M2 4l5 5 2-2 5 5"/><path d="M10 12h4V8"/></svg>'
    },
    violation: {
      label: "事实冲突",
      glyph: "⚠",
      color: "var(--cat-violation)",
      sound: null,
      icon: '<svg ' + ICON_ATTRS + '><path d="M8 1.8L15 14H1z"/><path d="M8 6.2v3.6"/><path d="M8 12h.01"/></svg>'
    },
    ontology: {
      label: "本体变更",
      glyph: "◈",
      color: "var(--cat-ontology)",
      sound: "soft",
      icon: '<svg ' + ICON_ATTRS + '><circle cx="4" cy="4" r="2"/><circle cx="12" cy="4" r="2"/><circle cx="8" cy="12.5" r="2"/><path d="M6 4h4M5 5.8l2 4.9M11 5.8l-2 4.9"/></svg>'
    },
    config: {
      label: "配置变更",
      glyph: "⚙",
      color: "var(--cat-config)",
      sound: null,
      icon: '<svg ' + ICON_ATTRS + '><path d="M2 4h7M13 4h1M2 12h1M7 12h7"/><circle cx="11" cy="4" r="2"/><circle cx="5" cy="12" r="2"/></svg>'
    }
  };

  var SEVERITIES = {
    urgent: { label: "紧急", pill: "bad" },
    attention: { label: "需关注", pill: "warn" },
    info: { label: "通知", pill: "muted" }
  };

  // Operator-facing wording for the machine reasons an Event carries. One
  // copy, gathered from the pages that used to own each map; an unknown code
  // is shown as it is rather than guessed at.
  var HANDOFF_REASONS = {
    confidence_ok: "置信度达标",
    low_confidence: "置信度不足",
    empty_answer: "答复为空",
    model_requested: "模型请求转人工",
    claim_conflict: "与本体事实冲突",
    commitment_unbacked: "承诺或数字无出处",
    human_quick_entry: "快捷入口要求人工",
    repeat_unresolved: "重复追问未解决",
    vulnerable_priority: "优先接待",
    ai_unavailable: "AI 服务不可用",
    keyword_match: "命中转人工关键词",
    blocked_topic: "禁答话题",
    safety_incident: "安全事件",
    funds_at_risk: "资金风险",
    payout_request: "赔付请求",
    liability_payout: "责任赔付",
    regulator_threat: "投诉或监管威胁"
  };

  var VIOLATION_KINDS = {
    undeclared_property: "未声明属性",
    unknown_scope: "未知场景",
    domain_mismatch: "类型不适用",
    range: "取值越界",
    contradicts_assertion: "与断言冲突",
    functional_conflict: "单值冲突",
    ambiguous_scope: "场景未限定",
    missing_companion: "缺少配套事实",
    denied_capability: "声称不提供的能力",
    unbacked_commitment: "承诺或金额无出处",
    deferred_figure: "应由人工核定的数字",
    redline_phrase: "红线表述（扣下）",
    redline_flagged: "红线表述（仅记录）",
    ungrounded_figure: "数字无出处"
  };

  var PROPOSAL_KINDS = {
    alias: "别名",
    dimension_value: "维度取值",
    requires: "伴随约束",
    missing_fact: "缺事实工单"
  };

  var PROPOSAL_DECISIONS = {
    approved: "已采纳",
    rejected: "已驳回",
    superseded: "已被取代",
    published: "已发布",
    resolved: "已解决"
  };

  var AUDIT_ACTIONS = {
    publish: "发布",
    rollback: "回滚",
    review: "复核",
    create: "新建",
    update: "修改",
    delete: "删除"
  };

  var LABEL_TABLES = [HANDOFF_REASONS, VIOLATION_KINDS, PROPOSAL_KINDS, PROPOSAL_DECISIONS, AUDIT_ACTIONS];

  function reasonLabel(reason) {
    if (!reason) return "";
    for (var i = 0; i < LABEL_TABLES.length; i++) {
      if (Object.prototype.hasOwnProperty.call(LABEL_TABLES[i], reason)) return LABEL_TABLES[i][reason];
    }
    return String(reason);
  }

  function category(name) {
    return CATEGORIES[name] || { label: name || "其他", glyph: "•", color: "var(--text-muted)", sound: null, icon: "" };
  }

  function icon(name) {
    var c = category(name);
    return c.icon ? svg(c.icon) : h("span", "uu-icon", c.glyph);
  }

  // Icon plus Chinese name in the category colour: the one way a category is
  // shown anywhere, so colour never stands alone.
  function categoryPill(name) {
    var c = category(name);
    var el = h("span", "uu-pill uu-cat");
    el.style.setProperty("--uu-cat", c.color);
    el.appendChild(icon(name));
    el.appendChild(h("span", null, c.label));
    return el;
  }

  // ---------- Sound ----------
  // Synthesised, so there is no file to fetch. The AudioContext is created on
  // the first pointer or key press after sound is switched on — browsers
  // refuse to start one earlier, and a page nobody touched has no business
  // making noise anyway. Sound starts muted; the choice is remembered.
  var SOUND_GAP_MS = 10000;
  var audioCtx = null;
  var lastPlayed = {};
  var soundListeners = [];

  function soundEnabled() { return lsGet("sound") === "on"; }

  function ensureAudio() {
    if (audioCtx || !soundEnabled()) return audioCtx;
    var Ctor = window.AudioContext || window.webkitAudioContext;
    if (!Ctor) return null;
    try { audioCtx = new Ctor(); } catch (e) { audioCtx = null; }
    return audioCtx;
  }

  function unlockAudio() {
    var ctx = ensureAudio();
    if (ctx && ctx.state === "suspended" && ctx.resume) ctx.resume();
  }
  document.addEventListener("pointerdown", unlockAudio, true);
  document.addEventListener("keydown", unlockAudio, true);

  function tone(ctx, freq, start, dur, type, peak) {
    var osc = ctx.createOscillator();
    var gain = ctx.createGain();
    osc.type = type;
    osc.frequency.value = freq;
    gain.gain.setValueAtTime(0.0001, start);
    gain.gain.exponentialRampToValueAtTime(peak, start + 0.015);
    gain.gain.exponentialRampToValueAtTime(0.0001, start + dur);
    osc.connect(gain);
    gain.connect(ctx.destination);
    osc.start(start);
    osc.stop(start + dur + 0.03);
  }

  var VOICES = {
    // Rising pair: someone must pick a conversation up.
    double: function (ctx, t) {
      tone(ctx, 880, t, 0.14, "sine", 0.18);
      tone(ctx, 1320, t + 0.18, 0.2, "sine", 0.18);
    },
    // One low note: an answer was held back.
    low: function (ctx, t) { tone(ctx, 196, t, 0.42, "triangle", 0.22); },
    // Barely there: somebody else changed the ontology.
    soft: function (ctx, t) { tone(ctx, 740, t, 0.26, "sine", 0.07); }
  };

  function playSound(kind) {
    if (!kind || !VOICES[kind] || !soundEnabled()) return false;
    var ctx = ensureAudio();
    if (!ctx || ctx.state !== "running") return false;
    var now = Date.now();
    if (lastPlayed[kind] && now - lastPlayed[kind] < SOUND_GAP_MS) return false;
    lastPlayed[kind] = now;
    try { VOICES[kind](ctx, ctx.currentTime + 0.01); } catch (e) { return false; }
    return true;
  }

  // Which tone a live event earns, or null. Only two kinds of event may make
  // a sound at all: urgent ones, and somebody else publishing or rolling back
  // the ontology (one's own action needs no echo). The category picks the
  // tone; the severity decides whether there is one, so a category that is
  // ever classified below urgent stays silent for those events.
  function soundForEvent(ev) {
    if (!ev) return null;
    var kind = category(ev.category).sound;
    if (!kind) return null;
    if (ev.category === "ontology") {
      if (ev.reason !== "publish" && ev.reason !== "rollback") return null;
      var me = currentUserId();
      if (!ev.actor || !me || String(ev.actor) === me) return null;
      return kind;
    }
    return ev.severity === "urgent" ? kind : null;
  }

  function setMuted(muted) {
    lsSet("sound", muted ? null : "on");
    if (!muted) unlockAudio();
    soundListeners.forEach(function (fn) { try { fn(!muted); } catch (e) { /* listener only */ } });
  }

  // ---------- Attention signals ----------
  // Title prefix and favicon badge show the same number, so a background tab
  // reads the same as the bar inside it. Zero restores both exactly.
  var TITLE_PREFIX = /^\(\d+\+?\) /;
  var faviconLink = null;
  var faviconBase = null;
  var faviconBaseTried = false;

  function setTitleCount(n) {
    var base = document.title.replace(TITLE_PREFIX, "");
    document.title = n > 0 ? "(" + (n > 99 ? "99+" : n) + ") " + base : base;
  }

  function cssVar(name, fallback) {
    var v = "";
    try { v = window.getComputedStyle(document.documentElement).getPropertyValue(name).trim(); } catch (e) { v = ""; }
    return v || fallback;
  }

  // The page's own icon, drawn under the badge when it can be loaded;
  // otherwise the brand mark. Loaded once.
  function loadFaviconBase(done) {
    if (faviconBaseTried) { done(); return; }
    faviconBaseTried = true;
    var links = document.querySelectorAll('link[rel~="icon"]');
    var href = "";
    for (var i = 0; i < links.length; i++) {
      if (links[i] !== faviconLink && links[i].href) href = links[i].href;
    }
    if (!href) { done(); return; }
    var img = new Image();
    img.onload = function () { faviconBase = img; done(); };
    img.onerror = function () { done(); };
    img.src = href;
  }

  function drawFavicon(n) {
    var size = 64;
    var canvas = document.createElement("canvas");
    canvas.width = size;
    canvas.height = size;
    var ctx = canvas.getContext && canvas.getContext("2d");
    if (!ctx) return null;
    if (faviconBase) {
      try { ctx.drawImage(faviconBase, 0, 0, size, size); } catch (e) { faviconBase = null; }
    }
    if (!faviconBase) {
      ctx.fillStyle = cssVar("--lt-accent", "#FF5D1F");
      ctx.beginPath();
      if (ctx.roundRect) ctx.roundRect(4, 4, 56, 56, 12); else ctx.rect(4, 4, 56, 56);
      ctx.fill();
      ctx.fillStyle = cssVar("--lt-on-accent", "#14130F");
      ctx.font = "bold 38px system-ui, sans-serif";
      ctx.textAlign = "center";
      ctx.textBaseline = "middle";
      ctx.fillText("U", 32, 35);
    }
    var label = n > 99 ? "99+" : String(n);
    var r = label.length > 2 ? 22 : 19;
    ctx.fillStyle = cssVar("--lt-err", "#B3392B");
    ctx.beginPath();
    ctx.arc(size - r, r, r, 0, Math.PI * 2);
    ctx.fill();
    ctx.fillStyle = "#fff";
    ctx.font = "bold " + (label.length > 2 ? 17 : label.length > 1 ? 22 : 26) + "px system-ui, sans-serif";
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.fillText(label, size - r, r + 1);
    try { return canvas.toDataURL("image/png"); } catch (e) { return null; }
  }

  // The badge lives in a link of its own, added last so it wins; the page's
  // original icon links are never touched and come back by removing it.
  function setFaviconCount(n) {
    if (!(n > 0)) {
      if (faviconLink && faviconLink.parentNode) faviconLink.parentNode.removeChild(faviconLink);
      faviconLink = null;
      return;
    }
    loadFaviconBase(function () {
      var url = drawFavicon(n);
      if (!url) return;
      if (!faviconLink) {
        faviconLink = document.createElement("link");
        faviconLink.rel = "icon";
        faviconLink.type = "image/png";
        faviconLink.setAttribute("data-uu", "badge");
      }
      faviconLink.href = url;
      (document.head || document.documentElement).appendChild(faviconLink);
    });
  }

  var attention = {
    count: 0,
    set: function (n) {
      n = Math.max(0, n | 0);
      attention.count = n;
      setTitleCount(n);
      setFaviconCount(n);
    }
  };

  // Browser notifications: off until the visitor switches them on in the top
  // bar, and even then only for urgent live events while this tab is hidden.
  var notify = {
    supported: function () { return "Notification" in window; },
    enabled: function () {
      return notify.supported() && lsGet("notify") === "on" && window.Notification.permission === "granted";
    },
    enable: function () {
      if (!notify.supported()) return Promise.resolve(false);
      var ask = window.Notification.permission === "granted"
        ? Promise.resolve("granted")
        : new Promise(function (resolve) {
          var r = window.Notification.requestPermission(resolve);
          if (r && r.then) r.then(resolve);
        });
      return ask.then(function (perm) {
        var ok = perm === "granted";
        lsSet("notify", ok ? "on" : null);
        return ok;
      });
    },
    disable: function () { lsSet("notify", null); },
    maybe: function (ev, meta) {
      if (!ev || (meta && meta.initial)) return false;
      if (ev.severity !== "urgent" || !document.hidden || !notify.enabled()) return false;
      try {
        var n = new window.Notification(category(ev.category).label + "：" + (ev.title || ""), {
          body: ev.detail || reasonLabel(ev.reason) || "",
          tag: ev.id || undefined
        });
        n.onclick = function () {
          window.focus();
          if (ev.link) window.location.href = ev.link;
          n.close();
        };
        return true;
      } catch (e) {
        return false;
      }
    }
  };

  // ---------- Change visualisation ----------
  function countTo(target, n, opts) {
    var el = byId(target);
    if (!el) return;
    opts = opts || {};
    var to = Number(n) || 0;
    var fmt = typeof opts.format === "function" ? opts.format : function (v) { return String(v); };
    var from = el.dataset.uuCount !== undefined ? Number(el.dataset.uuCount) : parseInt(el.textContent, 10);
    if (!isFinite(from)) from = 0;
    el.dataset.uuCount = String(to);
    el.classList.add("uu-count");
    if (el.__uuAnim) window.cancelAnimationFrame(el.__uuAnim);
    clearTimeout(el.__uuLand);
    // A hidden tab gets no animation frames at all, and a count that is
    // waiting for one would show a stale number in the title bar's shadow.
    if (from === to || reducedMotion() || opts.instant || document.hidden || !window.requestAnimationFrame) {
      el.textContent = fmt(to);
      return;
    }
    // Frames can also stop mid-roll (the tab is hidden halfway); the final
    // value lands regardless.
    el.__uuLand = setTimeout(function () {
      if (el.__uuAnim) window.cancelAnimationFrame(el.__uuAnim);
      el.__uuAnim = null;
      el.textContent = fmt(to);
    }, (opts.duration || 600) + 250);
    el.classList.remove("uu-count-up", "uu-count-down");
    el.classList.add(to > from ? "uu-count-up" : "uu-count-down");
    var dur = opts.duration || 600;
    var start = null;
    function step(ts) {
      if (start === null) start = ts;
      var p = Math.min(1, (ts - start) / dur);
      var eased = 1 - Math.pow(1 - p, 3);
      el.textContent = fmt(Math.round(from + (to - from) * eased));
      if (p < 1) {
        el.__uuAnim = window.requestAnimationFrame(step);
      } else {
        el.__uuAnim = null;
        clearTimeout(el.__uuLand);
        setTimeout(function () { el.classList.remove("uu-count-up", "uu-count-down"); }, 900);
      }
    }
    el.__uuAnim = window.requestAnimationFrame(step);
  }

  function flashRow(target) {
    var el = byId(target);
    if (!el || reducedMotion()) return;
    el.classList.remove("uu-flash");
    void el.offsetWidth; // restart the animation on a row flashed twice
    el.classList.add("uu-flash");
    el.addEventListener("animationend", function done() {
      el.classList.remove("uu-flash");
      el.removeEventListener("animationend", done);
    });
  }

  // "Seen" cursors: what the visitor had already looked at, per tenant and
  // per list, so a page can mark what arrived since. The value is opaque to
  // this module (an event cursor, a timestamp, an id).
  var seen = {
    get: function (tenantId, key) { return lsGet("seen:" + (tenantId || TENANT_ME) + ":" + key) || ""; },
    set: function (tenantId, key, value) { lsSet("seen:" + (tenantId || TENANT_ME) + ":" + key, value === "" ? null : value); },
    // True when an ISO timestamp is later than the stored one. Nothing
    // stored means nothing has been seen yet, which is not the same as all
    // of it being new: the first visit marks nothing.
    isNewer: function (tenantId, key, at) {
      var stored = seen.get(tenantId, key);
      if (!stored || !at) return false;
      var a = Date.parse(at);
      var b = Date.parse(stored);
      return isFinite(a) && isFinite(b) && a > b;
    },
    badge: function () {
      var el = h("span", "uu-new", "新");
      el.setAttribute("aria-label", "上次查看后新增");
      return el;
    }
  };

  // ---------- Event feed client ----------
  // One hub per tenant, shared by every subscriber on the page. The hub primes
  // itself with the most recent events from the list endpoint, then follows
  // the stream from that cursor. A stream that cannot be had — a non-stream
  // answer, or several failures in a row — gives way to polling the list
  // every 15 seconds, and the stream is tried again now and then.
  var EVENTS_TAIL = 100;
  var POLL_MS = 15000;
  var POLL_PAGE = 100;
  var MAX_STREAM_FAILURES = 3;
  var STREAM_RETRY_MS = 5 * 60 * 1000;
  // The server sends a comment every 25 seconds; twice that in silence means
  // the connection is dead even if nobody told the socket.
  var SILENCE_MS = 60000;
  var BACKLOG_MAX = 300;
  var SEEN_MAX = 2000;
  var hubs = {};

  function eventsPath(t, suffix) {
    return TENANT_ROOT + encodeURIComponent(t) + "/events" + (suffix || "");
  }

  function backoff(failures) {
    var base = Math.min(30000, 1000 * Math.pow(2, Math.max(0, failures - 1)));
    return Math.round(base * (0.8 + Math.random() * 0.4));
  }

  // A line-oriented reader for text/event-stream, following the HTML
  // standard's rules: CR, LF and CRLF all end a line, a leading colon is a
  // comment, one space after the field colon is dropped, and the last event
  // id carries over to later events that do not set their own.
  function SSEParser(onMessage, onComment) {
    this.buf = "";
    this.data = [];
    this.type = "";
    this.lastId = "";
    this.onMessage = onMessage;
    this.onComment = onComment;
  }

  SSEParser.prototype.feed = function (chunk) {
    this.buf += chunk;
    for (;;) {
      var cr = this.buf.indexOf("\r");
      var lf = this.buf.indexOf("\n");
      var end = cr < 0 ? lf : (lf < 0 ? cr : Math.min(cr, lf));
      if (end < 0) return;
      var skip = 1;
      if (this.buf.charAt(end) === "\r") {
        // A CR at the very end may be the first half of a CRLF still in flight.
        if (end === this.buf.length - 1) return;
        if (this.buf.charAt(end + 1) === "\n") skip = 2;
      }
      var line = this.buf.slice(0, end);
      this.buf = this.buf.slice(end + skip);
      this.line(line);
    }
  };

  SSEParser.prototype.line = function (line) {
    if (line === "") { this.dispatch(); return; }
    if (line.charAt(0) === ":") {
      if (this.onComment) this.onComment(line.slice(1).trim());
      return;
    }
    var idx = line.indexOf(":");
    var field = idx < 0 ? line : line.slice(0, idx);
    var value = idx < 0 ? "" : line.slice(idx + 1);
    if (value.charAt(0) === " ") value = value.slice(1);
    if (field === "event") this.type = value;
    else if (field === "data") this.data.push(value);
    else if (field === "id" && value.indexOf("\u0000") < 0) this.lastId = value;
    // "retry" is ignored: reconnect timing is this client's own backoff.
  };

  SSEParser.prototype.dispatch = function () {
    var type = this.type || "message";
    this.type = "";
    if (!this.data.length) return;
    var data = this.data.join("\n");
    this.data = [];
    this.onMessage({ type: type, data: data, id: this.lastId });
  };

  function Hub(t, tail) {
    this.tenant = t;
    this.tail = tail > 0 ? Math.min(tail | 0, BACKLOG_MAX) : EVENTS_TAIL;
    this.subs = [];
    this.statusSubs = [];
    this.state = "idle";
    this.cursor = "";
    this.backlog = [];
    this.seenIds = {};
    this.seenOrder = [];
    this.failures = 0;
    this.ctrl = null;
    this.timer = null;
    this.silence = null;
    this.stopped = false;
    this.pollingSince = 0;
    this.renewed = false;
    // Set once the priming list has answered. Until then there is no cursor,
    // and a stream opened without one would replay history as if it were new.
    this.primed = false;
  }

  Hub.prototype.setState = function (s) {
    if (this.state === s) return;
    this.state = s;
    this.statusSubs.slice().forEach(function (fn) {
      try { fn(s); } catch (e) { /* a status listener must not stop the feed */ }
    });
  };

  Hub.prototype.later = function (fn, ms) {
    var self = this;
    clearTimeout(this.timer);
    if (this.suspended) return;
    this.timer = setTimeout(function () {
      self.timer = null;
      if (!self.stopped && !self.suspended) fn.call(self);
    }, ms);
  };

  // Every event passes through here exactly once, however it arrived: the
  // priming list, the stream, or a poll that overlapped the stream.
  Hub.prototype.accept = function (ev, initial) {
    if (!ev || typeof ev !== "object") return;
    var key = ev.id || "";
    if (key) {
      if (this.seenIds[key]) return;
      this.seenIds[key] = true;
      this.seenOrder.push(key);
      if (this.seenOrder.length > SEEN_MAX) delete this.seenIds[this.seenOrder.shift()];
    }
    this.backlog.push(ev);
    if (this.backlog.length > BACKLOG_MAX) this.backlog.shift();
    var meta = { initial: !!initial, tenant: this.tenant };
    this.subs.slice().forEach(function (fn) {
      try { fn(ev, meta); } catch (e) {
        if (window.console && window.console.error) window.console.error("event handler failed", e);
      }
    });
  };

  Hub.prototype.start = function () {
    var self = this;
    this.setState(this.failures ? "reconnecting" : "connecting");
    apiGet(eventsPath(this.tenant, "?tail=" + this.tail)).then(function (data) {
      if (self.stopped) return;
      var list = data && Array.isArray(data.events) ? data.events : [];
      for (var i = 0; i < list.length; i++) self.accept(list[i], true);
      if (data && typeof data.cursor === "string" && data.cursor) self.cursor = data.cursor;
      self.primed = true;
      self.openStream();
    }, function (err) {
      if (self.stopped) return;
      // A lost session is auth.js's to announce; the page is about to leave.
      if (err && err.message === "unauthorized") { self.setState("disconnected"); return; }
      self.failures++;
      self.setState("reconnecting");
      self.later(self.start, backoff(self.failures));
    });
  };

  Hub.prototype.openStream = function () {
    var self = this;
    if (this.stopped || this.suspended) return;
    if (!window.fetch || typeof window.AbortController === "undefined" || typeof window.TextDecoder === "undefined" ||
        typeof window.ReadableStream === "undefined") {
      this.startPolling();
      return;
    }
    var ctrl = new window.AbortController();
    this.ctrl = ctrl;
    var headers = { "Accept": "text/event-stream" };
    var a = auth();
    var token = a && a.accessToken ? a.accessToken() : "";
    if (token) headers["Authorization"] = "Bearer " + token;
    if (this.cursor) headers["Last-Event-ID"] = this.cursor;
    if (this.state !== "polling") this.setState(this.failures ? "reconnecting" : "connecting");

    window.fetch(eventsPath(this.tenant, "/stream"), {
      headers: headers,
      signal: ctrl.signal,
      cache: "no-store",
      credentials: "same-origin"
    }).then(function (res) {
      if (self.stopped || ctrl.signal.aborted) return null;
      if (res.status === 401) return self.renewAndRetry(ctrl);
      var type = (res.headers.get("Content-Type") || "").toLowerCase();
      if (!res.ok || !res.body || type.indexOf("text/event-stream") < 0) {
        // Something answered, but not with a stream: the endpoint is missing
        // or a proxy in front of it buffers. Retrying will not change that.
        ctrl.abort();
        self.startPolling();
        return null;
      }
      self.renewed = false;
      self.pollingSince = 0;
      self.setState("connected");
      return self.read(res.body.getReader(), ctrl);
    }).catch(function () {
      if (self.stopped || (ctrl.signal.aborted && self.ctrl !== ctrl)) return;
      self.streamFailed(ctrl);
    });
  };

  Hub.prototype.read = function (reader, ctrl) {
    var self = this;
    var decoder = new window.TextDecoder("utf-8");
    var parser = new SSEParser(function (msg) {
      if (msg.type !== "event" && msg.type !== "message") return;
      var ev = null;
      try { ev = JSON.parse(msg.data); } catch (e) { return; }
      if (msg.id) self.cursor = msg.id;
      self.accept(ev, false);
    });
    var gotBytes = false;
    self.armSilence(ctrl);
    function pump() {
      return reader.read().then(function (r) {
        if (r.done) throw new Error("stream closed");
        if (!gotBytes) {
          // Only a stream that actually delivered something counts as a
          // recovery; one that is accepted and then dropped at once keeps
          // counting towards the switch to polling.
          gotBytes = true;
          self.failures = 0;
        }
        self.armSilence(ctrl);
        parser.feed(decoder.decode(r.value, { stream: true }));
        return pump();
      });
    }
    return pump();
  };

  Hub.prototype.armSilence = function (ctrl) {
    var self = this;
    clearTimeout(this.silence);
    this.silence = setTimeout(function () {
      if (self.ctrl === ctrl) ctrl.abort();
    }, SILENCE_MS);
  };

  // The stream cannot ask auth.js to renew a token: that only happens inside
  // UnicaAuth.api. A cheap authenticated read is sent through it instead, and
  // the stream is reopened once with whatever token the tab holds afterwards.
  Hub.prototype.renewAndRetry = function (ctrl) {
    var self = this;
    ctrl.abort();
    // A second refusal right after a renewal is not a lost session (that
    // would have failed the renewal itself); the stream is just not taking
    // this token. The list endpoint goes through auth.js and keeps working.
    if (this.renewed) { this.startPolling(); return null; }
    this.renewed = true;
    return apiGet(TENANT_ROOT + encodeURIComponent(this.tenant)).then(function () {
      if (!self.stopped) self.openStream();
    }, function (err) {
      if (self.stopped) return;
      if (err && err.message === "unauthorized") { self.setState("disconnected"); return; }
      self.streamFailed(ctrl);
    });
  };

  Hub.prototype.streamFailed = function (ctrl) {
    if (this.ctrl === ctrl) this.ctrl = null;
    clearTimeout(this.silence);
    if (this.stopped || this.suspended) return;
    this.failures++;
    if (this.pollingSince || this.failures >= MAX_STREAM_FAILURES) {
      this.startPolling();
      return;
    }
    this.setState("reconnecting");
    this.later(this.openStream, backoff(this.failures));
  };

  Hub.prototype.startPolling = function () {
    if (this.ctrl) { var c = this.ctrl; this.ctrl = null; c.abort(); }
    clearTimeout(this.silence);
    if (!this.pollingSince) this.pollingSince = Date.now();
    this.setState("polling");
    this.poll(0);
  };

  Hub.prototype.poll = function (round) {
    var self = this;
    var q = "?limit=" + POLL_PAGE + (this.cursor ? "&cursor=" + encodeURIComponent(this.cursor) : "");
    apiGet(eventsPath(this.tenant, q)).then(function (data) {
      if (self.stopped) return;
      var list = data && Array.isArray(data.events) ? data.events : [];
      for (var i = 0; i < list.length; i++) self.accept(list[i], false);
      if (data && typeof data.cursor === "string" && data.cursor) self.cursor = data.cursor;
      self.setState("polling");
      // Catch up a backlog straight away, but within reason.
      if (data && data.has_more && round < 10) { self.poll(round + 1); return; }
      if (Date.now() - self.pollingSince >= STREAM_RETRY_MS) {
        // Give the stream one more chance; a single failure returns here.
        self.pollingSince = Date.now();
        self.failures = MAX_STREAM_FAILURES - 1;
        self.openStream();
        return;
      }
      self.later(function () { self.poll(0); }, POLL_MS);
    }, function (err) {
      if (self.stopped) return;
      if (err && err.message === "unauthorized") { self.setState("disconnected"); return; }
      self.setState("disconnected");
      self.later(function () { self.poll(0); }, POLL_MS);
    });
  };

  // Called when the browser comes back online or the tab is shown again: a
  // hub waiting out a backoff tries at once instead.
  Hub.prototype.nudge = function () {
    if (this.stopped || !this.timer) return;
    if (this.state !== "reconnecting" && this.state !== "disconnected") return;
    clearTimeout(this.timer);
    this.timer = null;
    if (!this.primed) this.start();
    else if (this.state === "disconnected" && this.pollingSince) this.poll(0);
    else this.openStream();
  };

  Hub.prototype.stop = function () {
    this.stopped = true;
    clearTimeout(this.timer);
    clearTimeout(this.silence);
    if (this.ctrl) { var c = this.ctrl; this.ctrl = null; c.abort(); }
    this.setState("idle");
  };

  function nudgeAll() {
    for (var k in hubs) if (Object.prototype.hasOwnProperty.call(hubs, k)) hubs[k].nudge();
  }
  window.addEventListener("online", nudgeAll);
  document.addEventListener("visibilitychange", function () { if (!document.hidden) nudgeAll(); });

  // Leaving the page aborts every open stream at once rather than leaving it
  // to the browser: an open fetch keeps the page out of the back/forward
  // cache, and the server should see the reader go. A page restored from that
  // cache picks its feeds up again where they stopped, by cursor.
  Hub.prototype.suspend = function () {
    if (this.stopped) return;
    clearTimeout(this.timer);
    this.timer = null;
    clearTimeout(this.silence);
    if (this.ctrl) { var c = this.ctrl; this.ctrl = null; c.abort(); }
    this.suspended = true;
  };

  Hub.prototype.resume = function () {
    if (this.stopped || !this.suspended) return;
    this.suspended = false;
    if (!this.primed) this.start();
    else if (this.pollingSince) this.poll(0);
    else this.openStream();
  };

  window.addEventListener("pagehide", function () {
    for (var k in hubs) if (Object.prototype.hasOwnProperty.call(hubs, k)) hubs[k].suspend();
  });
  window.addEventListener("pageshow", function (e) {
    if (!e.persisted) return;
    for (var k in hubs) if (Object.prototype.hasOwnProperty.call(hubs, k)) hubs[k].resume();
  });

  // subscribe(tenantId, handler, { onStatus, tail }) → unsubscribe function.
  // handler(event, { initial, tenant }) is called once per event, in feed
  // order. A late subscriber first receives what the hub already holds, with
  // initial = true, so it can render history without asking again. tail is
  // how far back the hub primes itself (default 100, at most the backlog it
  // keeps); only the subscriber that opens the hub decides it.
  function subscribe(tenantId, handler, opts) {
    var t = tenantId || tenant();
    opts = opts || {};
    if (!t || typeof handler !== "function") return function () {};
    var hub = hubs[t];
    var fresh = !hub;
    if (fresh) hub = hubs[t] = new Hub(t, opts.tail);
    hub.subs.push(handler);
    if (typeof opts.onStatus === "function") {
      hub.statusSubs.push(opts.onStatus);
      try { opts.onStatus(hub.state); } catch (e) { /* listener only */ }
    }
    if (!fresh) {
      hub.backlog.slice().forEach(function (ev) {
        try { handler(ev, { initial: true, tenant: t }); } catch (e) { /* handler only */ }
      });
    } else {
      hub.start();
    }
    return function () { unsubscribe(t, handler, opts.onStatus); };
  }

  function unsubscribe(tenantId, handler, onStatus) {
    var t = tenantId || tenant();
    var hub = hubs[t];
    if (!hub) return;
    hub.subs = hub.subs.filter(function (fn) { return fn !== handler; });
    if (onStatus) hub.statusSubs = hub.statusSubs.filter(function (fn) { return fn !== onStatus; });
    if (!hub.subs.length) {
      hub.stop();
      delete hubs[t];
    }
  }

  var events = {
    subscribe: subscribe,
    unsubscribe: unsubscribe,
    // Connection state for a tenant: idle | connecting | connected |
    // reconnecting | polling | disconnected.
    status: function (tenantId) {
      var hub = hubs[tenantId || tenant()];
      return hub ? hub.state : "idle";
    },
    onStatus: function (tenantId, fn) {
      var hub = hubs[tenantId || tenant()];
      if (!hub || typeof fn !== "function") return function () {};
      hub.statusSubs.push(fn);
      return function () { hub.statusSubs = hub.statusSubs.filter(function (f) { return f !== fn; }); };
    },
    // One page of the list endpoint, for pages that browse history.
    // params: { cursor, limit, tail }.
    list: function (tenantId, params) {
      params = params || {};
      var q = [];
      if (params.cursor) q.push("cursor=" + encodeURIComponent(params.cursor));
      if (params.limit) q.push("limit=" + encodeURIComponent(params.limit));
      if (params.tail) q.push("tail=" + encodeURIComponent(params.tail));
      return apiGet(eventsPath(tenantId || tenant(), q.length ? "?" + q.join("&") : ""));
    },
    SSEParser: SSEParser
  };

  // ---------- Top bar ----------
  var STATUS_TEXT = {
    idle: "",
    connecting: "正在连接",
    connected: "实时",
    polling: "定时刷新（每 15 秒）",
    reconnecting: "已断开，正在重连",
    disconnected: "已断开，正在重连"
  };

  // Pages offered by the quick jump. keys are extra words a visitor might
  // type; matching is loose (substring first, then letters in order).
  var PAGES = [
    { file: "home.html", name: "我的工作台", keys: "首页 主页 工作台 home" },
    { file: "live.html", name: "实时事件", keys: "实时 事件 待办 收件箱 动态 live inbox" },
    { file: "knowledge.html", name: "我的知识库", keys: "知识库 文档 上传 knowledge" },
    { file: "channels.html", name: "渠道接入", keys: "渠道 接入 小红书 channels" },
    { file: "ontology.html", name: "确定性事实", keys: "本体 事实 发布 回滚 ontology facts" },
    { file: "ai-settings.html", name: "设置", keys: "设置 提示词 阈值 转人工 召回 settings" },
    { file: "violations.html", name: "质量复核", keys: "复核 冲突 违规 质量 violations" },
    { file: "proposals.html", name: "修改建议", keys: "建议 提案 别名 proposals" },
    { file: "compliance.html", name: "转人工与 AI 标识", keys: "转人工 标识 备案 自查 compliance" },
    { file: "evidence.html", name: "判定证据", keys: "证据 会话 留痕 evidence" },
    { file: "admin.html", name: "平台管理", keys: "平台 管理 租户 admin", admin: true }
  ];

  // A conversation id is one unbroken token with at least one digit; page
  // names and Chinese words never look like that.
  function looksLikeConversation(q) {
    return /^[A-Za-z0-9][A-Za-z0-9_\-:.]{5,}$/.test(q) && /\d/.test(q);
  }

  function fuzzyScore(q, hay) {
    if (!q) return 1;
    if (hay.indexOf(q) >= 0) return 3 + (hay.indexOf(q) === 0 ? 1 : 0);
    var j = 0;
    for (var i = 0; i < hay.length && j < q.length; i++) if (hay.charAt(i) === q.charAt(j)) j++;
    return j === q.length ? 1 : 0;
  }

  function currentFile() {
    var p = window.location.pathname;
    return p.slice(p.lastIndexOf("/") + 1) || "index.html";
  }

  var topbarState = { el: null, api: null, opts: null };

  // opts: { title, tail, pendingFilter }. pendingFilter(ev) → false keeps an
  // open event out of the pending count (the live page's "mark as read"); call
  // recount() on the returned handle when the answer for an event changes.
  function mountTopbar(opts) {
    if (topbarState.el) return topbarState.api;
    opts = opts || {};
    topbarState.opts = opts;
    var t = tenant();
    var c = claims();

    var nav = h("nav", "uu-topbar");
    nav.setAttribute("aria-label", "全局导航");

    var left = h("div", "uu-tb-left");
    var brand = h("a", "uu-brand");
    // An administrator with no tenant on the address has no workbench to go
    // back to; the platform pages are its home.
    if (c && c.role === "admin" && !t) {
      brand.href = "./admin.html";
      brand.title = "返回平台管理";
    } else {
      brand.href = withTenant("./home.html");
      brand.title = "返回我的工作台";
    }
    var mark = h("span", "uu-brand-mark", "U");
    mark.setAttribute("aria-hidden", "true");
    brand.appendChild(mark);
    brand.appendChild(h("span", null, "UNICA"));
    var tenantEl = h("span", "uu-tenant");
    tenantEl.title = "当前租户";
    var where = h("span", "uu-where", opts.title || document.title.replace(TITLE_PREFIX, "").split(" - ")[0]);
    where.setAttribute("aria-current", "page");
    left.appendChild(brand);
    left.appendChild(h("span", "uu-sep", "/"));
    left.appendChild(tenantEl);
    left.appendChild(h("span", "uu-sep", "/"));
    left.appendChild(where);

    var right = h("div", "uu-tb-right");

    // An administrator's way back to the platform pages, which the brand link
    // (the tenant's workbench) does not offer while a tenant is chosen.
    var adminLink = null;
    if (c && c.role === "admin" && t && currentFile() !== "admin.html") {
      adminLink = h("a", "uu-tb-btn", "平台管理");
      adminLink.href = "./admin.html";
    }

    var pending = h("a", "uu-tb-btn uu-pending");
    pending.href = withTenant("./live.html");
    pending.appendChild(h("span", "uu-hide-narrow", "待办"));
    var pendingN = h("span", "uu-pending-n uu-count", "0");
    pending.appendChild(pendingN);

    var conn = h("span", "uu-conn");
    conn.setAttribute("role", "status");
    var dot = h("span", "uu-conn-dot");
    dot.setAttribute("aria-hidden", "true");
    var connText = h("span", null, "");
    conn.appendChild(dot);
    conn.appendChild(connText);

    var muteBtn = h("button", "uu-tb-btn");
    muteBtn.type = "button";
    var notifyBtn = h("button", "uu-tb-btn uu-hide-narrow");
    notifyBtn.type = "button";
    var jumpBtn = h("button", "uu-tb-btn");
    jumpBtn.type = "button";
    jumpBtn.appendChild(h("span", null, "跳转"));
    var kbd = h("span", "uu-kbd uu-hide-narrow", "Ctrl K");
    kbd.setAttribute("aria-hidden", "true");
    jumpBtn.appendChild(kbd);
    jumpBtn.setAttribute("aria-keyshortcuts", "Control+K");

    // Without a tenant there is no feed, so a count would only ever say 0.
    if (c && t) right.appendChild(pending);
    if (adminLink) right.appendChild(adminLink);
    right.appendChild(conn);
    right.appendChild(muteBtn);
    if (notify.supported()) right.appendChild(notifyBtn);
    right.appendChild(jumpBtn);
    nav.appendChild(left);
    nav.appendChild(right);
    document.body.insertBefore(nav, document.body.firstChild);
    topbarState.el = nav;
    // The bar is sticky, so anything scrolled into view by an anchor or by
    // focus would otherwise land underneath it.
    var prevScrollPad = document.documentElement.style.scrollPaddingTop;
    document.documentElement.style.scrollPaddingTop = "calc(var(--uu-topbar-h) + 12px)";

    // -- tenant name
    if (!c) {
      tenantEl.textContent = "未登录";
    } else if (!t) {
      tenantEl.textContent = "平台管理员";
    } else {
      var fallback = t === TENANT_ME ? (c.tenant_id ? String(c.tenant_id) : TENANT_ME) : t;
      tenantEl.textContent = fallback;
      apiGet(TENANT_ROOT + encodeURIComponent(t)).then(function (info) {
        var name = info && (info.display_name || info.name);
        if (name) tenantEl.textContent = name;
      }, function () { /* the id stays */ });
    }

    // -- sound and notification toggles
    var SPEAKER = '<svg ' + ICON_ATTRS + '><path d="M2 6h3l4-3v10l-4-3H2z"/><path d="M11.5 5.5a3.5 3.5 0 0 1 0 5"/></svg>';
    var SPEAKER_OFF = '<svg ' + ICON_ATTRS + '><path d="M2 6h3l4-3v10l-4-3H2z"/><path d="M11 6l4 4M15 6l-4 4"/></svg>';
    var BELL = '<svg ' + ICON_ATTRS + '><path d="M4 11V7a4 4 0 0 1 8 0v4l1.5 1.5h-11z"/><path d="M6.5 14a1.5 1.5 0 0 0 3 0"/></svg>';

    function renderMute() {
      var on = soundEnabled();
      muteBtn.textContent = "";
      muteBtn.appendChild(svg(on ? SPEAKER : SPEAKER_OFF));
      muteBtn.appendChild(h("span", "uu-hide-narrow", on ? "声音：开" : "声音：关"));
      muteBtn.setAttribute("aria-pressed", on ? "true" : "false");
      muteBtn.setAttribute("aria-label", "提示音");
      muteBtn.title = on ? "关闭提示音" : "开启提示音（仅强制转人工、答复扣下与他人发布/回滚本体会发声）";
    }

    function renderNotify() {
      var on = notify.enabled();
      notifyBtn.textContent = "";
      notifyBtn.appendChild(svg(BELL));
      notifyBtn.appendChild(h("span", null, on ? "通知：开" : "通知：关"));
      notifyBtn.setAttribute("aria-pressed", on ? "true" : "false");
      notifyBtn.title = on ? "关闭浏览器通知" : "开启浏览器通知（仅紧急事件，且仅在本页不在前台时）";
    }

    muteBtn.addEventListener("click", function () {
      var turningOn = !soundEnabled();
      setMuted(!turningOn);
      renderMute();
      announce(turningOn ? "提示音已开启" : "提示音已关闭");
    });
    soundListeners.push(renderMute);

    notifyBtn.addEventListener("click", function () {
      if (notify.enabled()) {
        notify.disable();
        renderNotify();
        announce("浏览器通知已关闭");
        return;
      }
      notify.enable().then(function (ok) {
        renderNotify();
        if (ok) announce("浏览器通知已开启");
        else toast("浏览器拒绝了通知权限，可在地址栏左侧的网站设置里重新允许。", "warn");
      });
    });
    renderMute();
    renderNotify();

    // -- connection state
    function renderConn(state) {
      conn.setAttribute("data-state", state);
      connText.textContent = STATUS_TEXT[state] || "";
      conn.title = state === "polling" ? "实时连接不可用，已改为每 15 秒刷新一次" : "";
    }
    renderConn("idle");

    // -- pending count, fed by the event stream
    var open = {};
    var settled = {};
    var renderQueued = false;
    var firstRender = true;

    function renderPending() {
      renderQueued = false;
      var total = 0;
      var urgent = 0;
      var keep = typeof opts.pendingFilter === "function" ? opts.pendingFilter : null;
      for (var id in open) {
        if (!Object.prototype.hasOwnProperty.call(open, id)) continue;
        if (keep) {
          var counted = true;
          try { counted = keep(open[id]) !== false; } catch (e) { counted = true; }
          if (!counted) continue;
        }
        total++;
        if (open[id].severity === "urgent") urgent++;
      }
      countTo(pendingN, total, { instant: firstRender });
      firstRender = false;
      pending.classList.toggle("has-urgent", urgent > 0);
      pending.classList.toggle("has-attention", urgent === 0 && total > 0);
      pending.setAttribute("aria-label", "待处理事件 " + total + " 条" + (urgent ? "，其中紧急 " + urgent + " 条" : "") + "，查看实时事件");
      attention.set(total);
    }

    function queueRender() {
      if (renderQueued) return;
      renderQueued = true;
      setTimeout(renderPending, 0);
    }

    function onEvent(ev, meta) {
      if (ev.resolves) {
        settled[ev.resolves] = true;
        delete open[ev.resolves];
      }
      if ((ev.severity === "urgent" || ev.severity === "attention") && ev.id && !settled[ev.id]) open[ev.id] = ev;
      queueRender();
      if (meta.initial) return;
      playSound(soundForEvent(ev));
      notify.maybe(ev, meta);
      if (ev.severity === "urgent") announce("新的紧急事件：" + category(ev.category).label + "，" + (ev.title || ""));
    }

    var unsubscribeFeed = function () {};
    if (c && t) unsubscribeFeed = subscribe(t, onEvent, { onStatus: renderConn, tail: opts.tail });
    else renderPending();

    // -- quick jump
    var jump = buildJump();
    jumpBtn.addEventListener("click", function () { jump.open(jumpBtn); });
    function onKey(e) {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && (e.key === "k" || e.key === "K")) {
        e.preventDefault();
        jump.open(document.activeElement);
      }
    }
    document.addEventListener("keydown", onKey);

    var self = topbarState.api = {
      el: nav,
      setTitle: function (text) { where.textContent = text; },
      pending: function () { return attention.count; },
      recount: queueRender,
      openJump: function () { jump.open(document.activeElement); },
      destroy: function () {
        // A handle kept by a page outlives a refresh; destroying it then must
        // not take down the bar that replaced it.
        if (topbarState.api !== self) return;
        unsubscribeFeed();
        document.documentElement.style.scrollPaddingTop = prevScrollPad;
        document.removeEventListener("keydown", onKey);
        soundListeners = soundListeners.filter(function (fn) { return fn !== renderMute; });
        attention.set(0);
        if (nav.parentNode) nav.parentNode.removeChild(nav);
        if (jump.root.parentNode) jump.root.parentNode.removeChild(jump.root);
        topbarState.el = null;
        topbarState.api = null;
      }
    };
    return topbarState.api;
  }

  function unmountTopbar() {
    if (topbarState.api) topbarState.api.destroy();
  }

  // Rebuilds the bar from the session and address as they are now: after a
  // sign-in, or after the tenant on the address changed. opts default to the
  // ones it was last mounted with.
  function refreshTopbar(opts) {
    var o = opts || topbarState.opts || {};
    unmountTopbar();
    return mountTopbar(o);
  }

  // An administrator switching tenants on a page: the choice goes on the
  // address (where tenant(), withTenant() and the bar read it) and the bar
  // follows. For any other account the tenant is "me" and nothing changes.
  function setTopbarTenant(id) {
    if (!isAdmin()) return topbarState.api;
    var want = id === undefined || id === null ? "" : String(id);
    if (queryParam("tenant") === want) return topbarState.api;
    if (window.history && window.history.replaceState) {
      var url = new URL(window.location.href);
      if (want) url.searchParams.set("tenant", want);
      else url.searchParams.delete("tenant");
      window.history.replaceState(window.history.state, "", url.pathname + url.search + url.hash);
    }
    return topbarState.el ? refreshTopbar() : null;
  }

  function buildJump() {
    var root = h("div", "uu-modal-backdrop");
    var box = h("div", "uu-modal uu-jump");
    box.setAttribute("role", "dialog");
    box.setAttribute("aria-label", "快速跳转");
    var input = h("input");
    input.type = "text";
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-expanded", "true");
    input.setAttribute("aria-controls", "uu-jump-list");
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-label", "输入页面名称或会话号");
    input.placeholder = "页面名称，或粘贴会话号查看判定证据";
    input.autocomplete = "off";
    var list = h("ul");
    list.id = "uu-jump-list";
    list.setAttribute("role", "listbox");
    var hint = h("p", "uu-jump-hint", "↑↓ 选择 · Enter 打开 · Esc 关闭");
    box.appendChild(input);
    box.appendChild(list);
    box.appendChild(hint);
    root.appendChild(box);
    document.body.appendChild(root);

    var items = [];
    var active = 0;

    function options(q) {
      var query = q.trim();
      var lower = query.toLowerCase();
      var out = [];
      var admin = isAdmin();
      var here = currentFile();
      PAGES.forEach(function (p) {
        if (p.admin && !admin) return;
        var score = fuzzyScore(lower, (p.name + " " + p.keys + " " + p.file).toLowerCase());
        if (score > 0) out.push({ label: p.name + (p.file === here ? "（当前页）" : ""), file: p.file, href: withTenant("./" + p.file), score: score });
      });
      out.sort(function (a, b) { return b.score - a.score; });
      if (looksLikeConversation(query)) {
        out.unshift({
          label: "查看会话 " + query + " 的判定证据",
          file: "evidence.html",
          href: withTenant("./evidence.html?conversation=" + encodeURIComponent(query)),
          score: 9
        });
      }
      return out;
    }

    function render() {
      items = options(input.value);
      if (active >= items.length) active = 0;
      list.textContent = "";
      items.forEach(function (it, i) {
        var li = h("li");
        li.id = "uu-jump-opt-" + i;
        li.setAttribute("role", "option");
        li.setAttribute("aria-selected", i === active ? "true" : "false");
        li.appendChild(h("span", null, it.label));
        li.appendChild(h("span", "uu-jump-file", it.file));
        li.addEventListener("mousedown", function (e) { e.preventDefault(); go(i); });
        list.appendChild(li);
      });
      if (!items.length) list.appendChild(h("li", null, "没有匹配的页面"));
      if (items.length) input.setAttribute("aria-activedescendant", "uu-jump-opt-" + active);
      else input.removeAttribute("aria-activedescendant");
    }

    function go(i) {
      var it = items[i];
      if (!it) return;
      modalClose(root);
      window.location.href = it.href;
    }

    input.addEventListener("input", function () { active = 0; render(); });
    input.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown") { e.preventDefault(); active = items.length ? (active + 1) % items.length : 0; render(); }
      else if (e.key === "ArrowUp") { e.preventDefault(); active = items.length ? (active - 1 + items.length) % items.length : 0; render(); }
      else if (e.key === "Enter") { e.preventDefault(); go(active); }
    });

    return {
      root: root,
      open: function (trigger) {
        if (root.classList.contains("open")) return;
        input.value = "";
        active = 0;
        render();
        modalOpen(root, { trigger: trigger });
        input.focus();
      }
    };
  }

  // ---------- Export ----------
  window.UnicaUI = {
    version: "1",
    tenant: tenant,
    withTenant: withTenant,
    currentUserId: currentUserId,
    toast: toast,
    modal: { open: modalOpen, close: modalClose },
    chips: chips,
    liveRegion: { announce: announce },
    categories: CATEGORIES,
    severities: SEVERITIES,
    labels: {
      handoffReason: HANDOFF_REASONS,
      violationKind: VIOLATION_KINDS,
      proposalKind: PROPOSAL_KINDS,
      proposalDecision: PROPOSAL_DECISIONS,
      auditAction: AUDIT_ACTIONS
    },
    reasonLabel: reasonLabel,
    category: category,
    icon: icon,
    categoryPill: categoryPill,
    sound: {
      play: playSound,
      forEvent: soundForEvent,
      isMuted: function () { return !soundEnabled(); },
      setMuted: setMuted
    },
    attention: attention,
    notify: notify,
    countTo: countTo,
    flashRow: flashRow,
    seen: seen,
    events: events,
    topbar: {
      mount: mountTopbar,
      refresh: refreshTopbar,
      unmount: unmountTopbar,
      setTenant: setTopbarTenant,
      current: function () { return topbarState.api; }
    },
    reducedMotion: reducedMotion,
    onReady: onReady
  };
})(window, document);

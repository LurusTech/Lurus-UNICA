// UNICA portal session module.
//
// One copy of what used to be four near-identical inline blocks: token
// storage, claim decoding, the fetch wrapper, and — new here — silent access
// token renewal through /api/v1/auth/refresh.
//
// Two storage layers, deliberately separate:
//   * sessionStorage (per tab) holds the pair this tab actually uses. It is
//     the sole reason two accounts can be signed in side by side, so nothing
//     here ever reads a token from anywhere else to send a request.
//   * localStorage["unica_sessions"] is an account vault, written only when
//     the visitor ticked "remember me". A tab copies an entry into its own
//     sessionStorage before using it, never the other way round.
//
// This module touches no page DOM and never navigates. A page reacts to a
// lost session through the onSessionLost callback.
(function (window) {
  "use strict";

  var ACCESS_KEY = "unica_access_token";
  var REFRESH_KEY = "unica_refresh_token";
  var LOST_KEY = "unica_session_lost";
  var SESSIONS_KEY = "unica_sessions";
  var LOGIN_PATH = "/api/v1/auth/login";
  var REFRESH_PATH = "/api/v1/auth/refresh";

  // The single in-flight renewal. A refresh token is single-use, so several
  // requests failing at once must share one POST rather than race to spend it.
  var refreshInFlight = null;
  var sessionLostHandlers = [];
  // A lost session is announced once per episode, not once per failed
  // request, and rearms as soon as a new pair is adopted.
  var sessionLostFired = false;

  function assign(target) {
    for (var i = 1; i < arguments.length; i++) {
      var src = arguments[i];
      if (!src) continue;
      for (var k in src) {
        if (Object.prototype.hasOwnProperty.call(src, k)) target[k] = src[k];
      }
    }
    return target;
  }

  // ---------- Tab storage ----------
  // Storage access throws under restrictive privacy settings; a failure there
  // simply means this tab has no session, never a broken page.
  function accessToken() {
    try { return sessionStorage.getItem(ACCESS_KEY); } catch (e) { return null; }
  }

  function refreshToken() {
    try { return sessionStorage.getItem(REFRESH_KEY); } catch (e) { return null; }
  }

  function writeTabPair(access, refresh) {
    try {
      sessionStorage.setItem(ACCESS_KEY, access);
      if (refresh) sessionStorage.setItem(REFRESH_KEY, refresh);
      else sessionStorage.removeItem(REFRESH_KEY);
    } catch (e) { /* sign-in stays in memory for this page load */ }
  }

  // Clears this tab only. The vault is left alone: a transient renewal failure
  // must not erase the accounts the visitor asked to be remembered.
  function dropTab() {
    try {
      sessionStorage.removeItem(ACCESS_KEY);
      sessionStorage.removeItem(REFRESH_KEY);
    } catch (e) { /* nothing to clear */ }
  }

  // A tab that has just been thrown out of a session carries a mark across the
  // navigation to the login page. Without it the login page would silently
  // adopt the very account that was refused a moment ago and send the tab
  // straight back, so a backend that is down would be answered with an endless
  // bounce instead of a form. The mark lives in this tab only and is lifted the
  // moment a pair is adopted deliberately.
  function markSessionLost() {
    try { sessionStorage.setItem(LOST_KEY, "1"); } catch (e) { /* mark is advisory */ }
  }

  function wasSessionLost() {
    try { return !!sessionStorage.getItem(LOST_KEY); } catch (e) { return false; }
  }

  function clearSessionLostMark() {
    try { sessionStorage.removeItem(LOST_KEY); } catch (e) { /* nothing to clear */ }
  }

  // ---------- Claims ----------
  // Read-only decode of a token payload. The server remains the authority on
  // what an account may reach; this only decides what a page shows first. A
  // token that cannot be decoded yields null.
  function decodeClaims(token) {
    if (!token || typeof token !== "string") return null;
    var parts = token.split(".");
    if (parts.length !== 3) return null;
    try {
      var b64 = parts[1].replace(/-/g, "+").replace(/_/g, "/");
      while (b64.length % 4 !== 0) b64 += "=";
      var claims = JSON.parse(atob(b64));
      return claims && typeof claims === "object" ? claims : null;
    } catch (e) {
      return null;
    }
  }

  // Which account this tab is holding. The access token answers it; if that
  // one is unreadable the refresh token carries the same user_id.
  function currentUserId() {
    var claims = decodeClaims(accessToken());
    if (claims && claims.user_id) return String(claims.user_id);
    claims = decodeClaims(refreshToken());
    if (claims && claims.user_id) return String(claims.user_id);
    return "";
  }

  function isExpired(token) {
    var claims = decodeClaims(token);
    if (!claims || typeof claims.exp !== "number") return true;
    return claims.exp * 1000 <= Date.now();
  }

  // ---------- Account vault ----------
  function readTable() {
    var raw = null;
    try { raw = localStorage.getItem(SESSIONS_KEY); } catch (e) { return {}; }
    if (!raw) return {};
    try {
      var parsed = JSON.parse(raw);
      return parsed && typeof parsed === "object" && !(parsed instanceof Array) ? parsed : {};
    } catch (e) {
      return {};
    }
  }

  function writeTable(table) {
    var empty = true;
    for (var k in table) {
      if (Object.prototype.hasOwnProperty.call(table, k)) { empty = false; break; }
    }
    try {
      if (empty) localStorage.removeItem(SESSIONS_KEY);
      else localStorage.setItem(SESSIONS_KEY, JSON.stringify(table));
    } catch (e) { /* remembering is a convenience, not a requirement */ }
  }

  // The remembered accounts, newest first, with unusable entries removed and
  // the pruned table written back. An entry whose refresh token has expired
  // can no longer produce a session, so keeping it would only offer the
  // visitor a button that fails.
  function savedAccounts() {
    var table = readTable();
    var kept = {};
    var list = [];
    var changed = false;
    for (var uid in table) {
      if (!Object.prototype.hasOwnProperty.call(table, uid)) continue;
      var entry = table[uid];
      if (!entry || typeof entry !== "object" || !entry.access_token || !entry.refresh_token || isExpired(entry.refresh_token)) {
        changed = true;
        continue;
      }
      kept[uid] = entry;
      list.push(assign({}, entry, { user_id: uid }));
    }
    if (changed) writeTable(kept);
    list.sort(function (a, b) { return (b.saved_at || 0) - (a.saved_at || 0); });
    return list;
  }

  function forgetAccount(userId) {
    if (!userId) return;
    var table = readTable();
    if (!Object.prototype.hasOwnProperty.call(table, userId)) return;
    delete table[userId];
    writeTable(table);
  }

  function isRemembered(userId) {
    if (!userId) return false;
    return Object.prototype.hasOwnProperty.call(readTable(), userId);
  }

  // A 401 from the renewal endpoint is the server's final word on that refresh
  // token: revoked, signed with a secret that no longer exists, or belonging to
  // an account that has been disabled. Its exp says nothing about that, so the
  // entry would keep passing the expiry check and keep being adopted — every
  // page adopting it, failing, and bouncing back to the login page that adopts
  // it again. It is dropped, but only if the vault still holds exactly the
  // token that was refused: another tab may have replaced it in the meantime,
  // and a network failure or a 5xx is transient and never gets here.
  function forgetRefused(token) {
    if (!token) return;
    var uid = currentUserId();
    if (!uid) return;
    var table = readTable();
    var entry = table[uid];
    if (!entry || entry.refresh_token !== token) return;
    delete table[uid];
    writeTable(table);
  }

  // ---------- Adoption ----------
  // Installs a token pair into this tab and returns its claims. The vault is
  // written when the visitor asked to be remembered, and refreshed when an
  // entry for this account already exists — so a renewal keeps the stored
  // pair current — but an entry is never created behind the visitor's back.
  function adoptPair(pair, options) {
    if (!pair || !pair.access_token) return null;
    options = options || {};
    writeTabPair(pair.access_token, pair.refresh_token || "");
    sessionLostFired = false;
    clearSessionLostMark();

    var claims = decodeClaims(pair.access_token);
    var uid = claims && claims.user_id ? String(claims.user_id) : "";
    if (uid) {
      var table = readTable();
      var known = Object.prototype.hasOwnProperty.call(table, uid);
      if (options.remember || known) {
        table[uid] = {
          email: claims.email || (pair.email || ""),
          role: claims.role || (pair.role || ""),
          tenant_id: claims.tenant_id || (pair.tenant_id || ""),
          access_token: pair.access_token,
          refresh_token: pair.refresh_token || "",
          saved_at: Date.now()
        };
        writeTable(table);
      }
    }
    return claims;
  }

  // Picks the session this tab starts from. A tab that already holds a token
  // keeps it. Otherwise exactly one remembered account is adopted silently;
  // two or more means the page must ask, because choosing one would be a
  // guess about which account the visitor wants. A tab that was just thrown
  // out of a session asks as well, whatever the count: re-adopting on its own
  // is how a bounce becomes a loop.
  function bootstrapTab() {
    var token = accessToken();
    if (token) return decodeClaims(token);
    if (wasSessionLost()) return null;
    var list = savedAccounts();
    if (list.length === 1) return adoptPair(list[0], {});
    return null;
  }

  // ---------- Session loss ----------
  function onSessionLost(fn) {
    if (typeof fn === "function") sessionLostHandlers.push(fn);
  }

  function announceSessionLost() {
    if (sessionLostFired) return;
    sessionLostFired = true;
    markSessionLost();
    for (var i = 0; i < sessionLostHandlers.length; i++) {
      try {
        sessionLostHandlers[i]();
      } catch (e) {
        // One page's reaction failing must not stop the others, and must not
        // replace the unauthorized error the caller is about to see.
        if (window.console && window.console.error) window.console.error("session-lost handler failed", e);
      }
    }
  }

  function unauthorizedError() {
    var err = new Error("unauthorized");
    err.status = 401;
    return err;
  }

  // ---------- Renewal ----------
  // Another tab sharing this account may already have spent the refresh token
  // and stored the new pair. When the vault holds a pair this tab has not seen
  // yet, adopting it costs no network call and avoids burning a token that is
  // already gone.
  function reconcileFromTable() {
    var uid = currentUserId();
    if (!uid) return null;
    var table = readTable();
    var entry = table[uid];
    if (!entry || !entry.access_token || !entry.refresh_token) return null;
    if (entry.refresh_token === refreshToken()) return null;
    if (isExpired(entry.refresh_token)) return null;
    return adoptPair(entry, {});
  }

  function postRefresh(token) {
    return fetch(REFRESH_PATH, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refresh_token: token })
    }).then(function (res) {
      return res.text().then(function (text) {
        var data = null;
        if (text) {
          try { data = JSON.parse(text); } catch (e) { /* non-JSON response */ }
        }
        if (!res.ok || !data || !data.access_token) {
          var err = new Error((data && (data.error || data.message)) || "会话续期失败");
          err.status = res.status;
          err.data = data;
          throw err;
        }
        return data;
      });
    });
  }

  // True once the pair this tab now holds can carry a request again. A vault
  // entry is only as fresh as the last renewal that wrote it, so the pair it
  // hands over can have a live refresh token behind an access token that ran
  // out hours ago. Resolving on that one would send the retry into a second
  // 401 and cost the session a refresh token it never tried.
  function tabPairUsable() {
    return !isExpired(accessToken());
  }

  // Renews this tab's access token, at most one POST at a time. Every exit
  // path clears the in-flight promise, so a later failure can be retried.
  function refreshAccess() {
    var adopted = reconcileFromTable();
    if (adopted && tabPairUsable()) return Promise.resolve(adopted);

    if (refreshInFlight) return refreshInFlight;

    // Either this tab's own token, or the one just adopted from the vault when
    // the access token that came with it was already spent.
    var token = refreshToken();
    if (!token || isExpired(token)) return Promise.reject(unauthorizedError());
    // Whether this account is in the vault is read before the request, not
    // after it: a sibling tab losing the race for the same single-use token
    // drops the entry while this one is in flight, and the account the visitor
    // asked to be remembered must survive that.
    var remembered = isRemembered(currentUserId());

    refreshInFlight = postRefresh(token).then(function (pair) {
      refreshInFlight = null;
      var claims = adoptPair(pair, { remember: remembered });
      if (!claims) throw unauthorizedError();
      return claims;
    }, function (err) {
      refreshInFlight = null;
      // A second caller in this tab may have reconciled to a newer pair while
      // this POST was in flight. reconcileFromTable cannot see that: it asks
      // whether the vault differs from what the tab holds, and by now the two
      // have moved on together. Only the token actually posted tells them
      // apart, and throwing here would discard a pair this tab already holds.
      var held = refreshToken();
      if (held && held !== token) {
        if (tabPairUsable()) return decodeClaims(accessToken());
        return refreshAccess();
      }
      // The token may have been spent by another tab between the read above
      // and the response; the vault is the place that would show it.
      var late = reconcileFromTable();
      if (late && tabPairUsable()) return late;
      // What turned up is stale too, but its refresh token is one this tab has
      // never posted, so it is worth the single attempt this call makes.
      if (late) return refreshAccess();
      if (err && err.status === 401) forgetRefused(token);
      throw err;
    });
    return refreshInFlight;
  }

  // ---------- Requests ----------
  // Wraps fetch with the Authorization header, JSON parsing, and 401
  // handling. The HTTP status and the parsed body travel on the error, so a
  // caller can tell a refusal (403) or an explanation (409) from an ordinary
  // failure and can read a body that says more than one sentence.
  function api(path, options) {
    options = options || {};
    var headers = assign({}, options.headers || {});
    var sent = accessToken();
    if (sent) headers["Authorization"] = "Bearer " + sent;
    // FormData must set its own multipart boundary, so it is never labelled JSON.
    var isForm = (typeof FormData !== "undefined") && (options.body instanceof FormData);
    if (options.body && !isForm && !headers["Content-Type"]) headers["Content-Type"] = "application/json";

    return fetch(path, assign({}, options, { headers: headers })).then(function (res) {
      if (res.status === 401) return handleUnauthorized(path, options, sent);
      return res.text().then(function (text) {
        var data = null;
        if (text) {
          try { data = JSON.parse(text); } catch (e) { /* non-JSON response */ }
        }
        if (!res.ok) {
          var msg = (data && (data.error || data.message)) || ("请求失败 (" + res.status + ")");
          var err = new Error(msg);
          err.status = res.status;
          err.data = data;
          throw err;
        }
        return data;
      });
    });
  }

  function resend(path, options) {
    return api(path, assign({}, options, { __retried: true }));
  }

  function handleUnauthorized(path, options, sentToken) {
    // A retry that is refused again is a real refusal. Renewing once more
    // here would loop forever.
    if (options.__retried) {
      announceSessionLost();
      throw unauthorizedError();
    }
    // This request left before a renewal that has since landed; it only needs
    // to go again with the token this tab now holds.
    var current = accessToken();
    if (sentToken && current && current !== sentToken) return resend(path, options);

    return refreshAccess().then(function () {
      return resend(path, options);
    }, function () {
      announceSessionLost();
      throw unauthorizedError();
    });
  }

  // ---------- Sign in / out ----------
  // Resolves with the claims of the new session. The vault entry is written
  // only when remember is true.
  function login(email, password, remember) {
    return fetch(LOGIN_PATH, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: email, password: password })
    }).then(function (res) {
      return res.text().then(function (text) {
        var data = null;
        if (text) {
          try { data = JSON.parse(text); } catch (e) { /* non-JSON response */ }
        }
        if (!res.ok) {
          var err = new Error((data && (data.error || data.message)) || "邮箱或密码错误");
          err.status = res.status;
          err.data = data;
          throw err;
        }
        if (!data || !data.access_token) throw new Error("登录响应异常");
        return data;
      });
    }).then(function (data) {
      return adoptPair(data, { remember: !!remember });
    });
  }

  // Signing out means being forgotten: this tab is cleared and the account
  // leaves the vault. A session that merely failed to renew uses dropTab.
  function logout() {
    var uid = currentUserId();
    dropTab();
    if (uid) forgetAccount(uid);
  }

  window.UnicaAuth = {
    accessToken: accessToken,
    refreshToken: refreshToken,
    decodeClaims: decodeClaims,
    adoptPair: adoptPair,
    dropTab: dropTab,
    savedAccounts: savedAccounts,
    forgetAccount: forgetAccount,
    bootstrapTab: bootstrapTab,
    wasSessionLost: wasSessionLost,
    login: login,
    logout: logout,
    onSessionLost: onSessionLost,
    api: api
  };
})(window);

// Behaviour of the portal's shared event connection (portal/ui/unica-ui.js):
// one stream per visible tab, none for a tab left in the background, never two
// at once, and an approved-then-published proposal delivered twice.
//
// The portal has no build step and no test runner of its own; this loads the
// shipped script into a simulated browser. Run: cd scripts/portal-test &&
// bun install && bun test
import { expect, test } from "bun:test";
import { readFileSync } from "fs";
import { Window } from "happy-dom";
import { fileURLToPath } from "url";

const SRC = process.env.UNICA_UI_JS || fileURLToPath(new URL("../../portal/ui/unica-ui.js", import.meta.url));

type Timer = { id: number; at: number; fn: () => void };

function harness() {
  const win: any = new Window({ url: "http://portal.test/live.html" });
  const doc: any = win.document;
  let hidden = false;
  Object.defineProperty(doc, "hidden", { get: () => hidden, configurable: true });

  // A controllable clock for setTimeout: tests advance it explicitly.
  let now = 0;
  let seq = 0;
  let timers: Timer[] = [];
  const fakeSet = (fn: () => void, ms?: number) => {
    const t = { id: ++seq, at: now + (ms || 0), fn };
    timers.push(t);
    return t.id;
  };
  const fakeClear = (id?: number) => { timers = timers.filter((t) => t.id !== id); };
  const advance = async (ms: number) => {
    const until = now + ms;
    for (;;) {
      timers.sort((a, b) => a.at - b.at);
      const next = timers[0];
      if (!next || next.at > until) break;
      timers.shift();
      now = next.at;
      next.fn();
      await flush();
    }
    now = until;
  };

  const streams: { aborted: boolean; signal: AbortSignal }[] = [];
  const lists: string[] = [];
  win.fetch = (url: string, init: any) => {
    const rec = { aborted: false, signal: init.signal as AbortSignal };
    streams.push(rec);
    init.signal.addEventListener("abort", () => { rec.aborted = true; });
    const body = new ReadableStream({
      start(ctrl) {
        ctrl.enqueue(new TextEncoder().encode(": ping\n\n"));
        init.signal.addEventListener("abort", () => { try { ctrl.error(new Error("aborted")); } catch {} });
      },
    });
    return Promise.resolve(new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } }));
  };
  win.AbortController = AbortController;
  win.TextDecoder = TextDecoder;
  win.ReadableStream = ReadableStream;
  win.UnicaAuth = {
    accessToken: () => "tok",
    decodeClaims: () => ({ role: "user", user_id: "u1", tenant_id: "pl-1" }),
    api: (path: string) => {
      lists.push(path);
      return Promise.resolve({ events: [], cursor: "c1", has_more: false });
    },
  };

  const code = readFileSync(SRC, "utf8").replace(/\}\)\(window, document\);\s*$/, "})");
  const realSet = globalThis.setTimeout, realClear = globalThis.clearTimeout;
  (globalThis as any).setTimeout = fakeSet;
  (globalThis as any).clearTimeout = fakeClear;
  const factory = (0, eval)(code);
  factory(win, doc);

  const setHidden = (v: boolean) => {
    hidden = v;
    doc.dispatchEvent(new win.Event("visibilitychange"));
  };
  const restore = () => { (globalThis as any).setTimeout = realSet; (globalThis as any).clearTimeout = realClear; };
  const open = () => streams.filter((s) => !s.aborted).length;
  return { win, ui: win.UnicaUI, advance, setHidden, streams, lists, open, restore, setHiddenQuiet: (v: boolean) => { hidden = v; } };
}

async function flush() {
  for (let i = 0; i < 20; i++) await Promise.resolve();
}

test("a visible tab holds one stream; a tab hidden for 30s gives it up and polls; shown again it streams", async () => {
  const h = harness();
  try {
    const seen: any[] = [];
    h.ui.events.subscribe("pl-1", (ev: any) => seen.push(ev));
    await flush();
    expect(h.open()).toBe(1);
    expect(h.ui.events.status("pl-1")).toBe("connected");

    h.setHidden(true);
    await h.advance(29000);
    expect(h.open()).toBe(1); // a quick tab switch keeps the stream
    await h.advance(2000);
    expect(h.open()).toBe(0);
    expect(h.ui.events.status("pl-1")).toBe("polling");

    // Hidden, it keeps polling and never reopens a stream, even past the
    // five-minute retry that a failed stream would get.
    const polls = h.lists.length;
    await h.advance(6 * 60 * 1000);
    expect(h.lists.length).toBeGreaterThan(polls);
    expect(h.open()).toBe(0);

    h.setHidden(false);
    await flush();
    expect(h.open()).toBe(1);
    expect(h.ui.events.status("pl-1")).toBe("connected");
  } finally {
    h.restore();
  }
});

test("a tab opened in the background polls from the start", async () => {
  const h = harness();
  try {
    h.setHiddenQuiet(true);
    h.ui.events.subscribe("pl-1", () => {});
    await flush();
    expect(h.streams.length).toBe(0);
    expect(h.ui.events.status("pl-1")).toBe("polling");
    h.setHidden(false);
    await flush();
    expect(h.open()).toBe(1);
  } finally {
    h.restore();
  }
});

test("hiding and showing within the grace period never leaves two streams", async () => {
  const h = harness();
  try {
    h.ui.events.subscribe("pl-1", () => {});
    await flush();
    for (let i = 0; i < 5; i++) {
      h.setHidden(true);
      await h.advance(31000);
      h.setHidden(false);
      await flush();
      expect(h.open()).toBe(1);
    }
  } finally {
    h.restore();
  }
});

test("the same id at a new time is a new event; the same delivery twice is not", async () => {
  const h = harness();
  try {
    const got: string[] = [];
    // Feed events through the poll path: the list answers with them.
    let page: any[] = [];
    h.win.UnicaAuth.api = (path: string) => {
      h.lists.push(path);
      const out = { events: page, cursor: "c" + h.lists.length, has_more: false };
      page = [];
      return Promise.resolve(out);
    };
    h.setHiddenQuiet(true); // polling mode
    h.ui.events.subscribe("pl-1", (ev: any) => got.push(ev.id + "@" + ev.at));
    await flush();
    const approved = { id: "proposal_decision:7", at: "2026-10-12T08:00:00Z", severity: "info", category: "ontology" };
    const published = { id: "proposal_decision:7", at: "2026-10-12T08:05:00Z", severity: "info", category: "ontology" };
    page = [approved, approved];
    await h.advance(15000);
    page = [published];
    await h.advance(15000);
    expect(got).toEqual(["proposal_decision:7@2026-10-12T08:00:00Z", "proposal_decision:7@2026-10-12T08:05:00Z"]);
  } finally {
    h.restore();
  }
});

// JSDOM coverage for the ask_user awaiting-transition notification path
// (spec §11 item 4 of the ask-user/attention-status reconciliation): an
// ask-produced `SessionAwaiting` normalizes to attention level "needs_you"
// (hubcore's NormalizeState) exactly like any other awaiting/warning
// producer before it ever reaches the browser as a "serf/attention/changed"
// broadcast — there is no ask-specific wiring in notifications.js, and there
// should not be (it keys on the transition, not the producer). This mirrors
// the boot() harness in test-notifications-attention.js; see that file for
// the broader baseline/title/favicon/prefs coverage this one does not repeat.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const src = fs.readFileSync("../assets/notifications.js", "utf8");

function boot(opts) {
  const dom = new JSDOM(`<!DOCTYPE html><html><head><title>serf hub</title></head><body></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.document.hasFocus = () => !!(opts && opts.focused);
  let notifHandler = null;
  w.SerfAppwire = {
    onNotification(h) { notifHandler = notifHandler || h; },
    onConnectionRestored() {},
  };
  const fired = [];
  w.Notification = function (title, o) { fired.push({ title, body: (o || {}).body }); };
  w.Notification.permission = "granted";
  // Baseline fetch: /api/tree with an attentionSummary.
  w.fetch = () => Promise.resolve({
    json: () => Promise.resolve({ attentionSummary: (opts && opts.summary) || { needsYou: 0, error: 0, working: 0 } }),
  });
  w.localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: true, favicon: true, os: true, sound: false }));
  w.localStorage.setItem("serf-hub.notifications.v", "2");
  w.eval(src);
  return { w, fireNotif: (m, p) => notifHandler && notifHandler(m, p), fired };
}

(async () => {
  // 1) A session with a pending ask_user question settles `awaiting` at the
  // asking round's boundary. hubcore normalizes that raw state to attention
  // level "needs_you" before broadcasting, so the client sees an ordinary
  // transition from "working" (the asking turn was in flight) into
  // "needs_you" — it must fire the OS notification exactly like any other
  // needs_you producer (a plain inbox-semantics upgrade, a `warning`, etc.).
  const a = boot({ summary: { needsYou: 0, error: 0, working: 1 } });
  await new Promise((r) => setTimeout(r, 20));
  a.fireNotif("serf/attention/changed", {
    changed: [{ threadId: "01ASK", title: "which db?", project: "p", level: "needs_you", prevLevel: "working" }],
    summary: { needsYou: 1, error: 0, working: 0 },
  });
  await new Promise((r) => setTimeout(r, 5));
  if (a.fired.length !== 1) {
    throw new Error("ask-produced awaiting->needs_you transition should fire OS once, fired " + a.fired.length);
  }
  if (a.w.document.title.indexOf("(1)") === -1) {
    throw new Error("title should reflect the ask's needs_you count: " + a.w.document.title);
  }

  // 2) A repeat broadcast for the SAME still-pending ask (needs_you ->
  // needs_you — e.g. a job notification the entry gate held arriving while
  // the question is still unanswered, or a periodic re-seed) must not
  // re-fire: only a genuine into-transition alerts, not every broadcast
  // that merely reports the session is still needs_you.
  a.fireNotif("serf/attention/changed", {
    changed: [{ threadId: "01ASK", title: "which db?", project: "p", level: "needs_you", prevLevel: "needs_you" }],
    summary: { needsYou: 1, error: 0, working: 0 },
  });
  await new Promise((r) => setTimeout(r, 5));
  if (a.fired.length !== 1) {
    throw new Error("repeat needs_you broadcast (no transition) must not re-fire OS, fired " + a.fired.length);
  }

  console.log("ok");
})().catch((e) => { console.error(e); process.exit(1); });

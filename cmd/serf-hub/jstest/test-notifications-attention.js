// Attention-driven notifications: baseline-before-edge, summary-driven
// counts, transition-into needs_you/error fires, focused suppression.
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
  w.fetch = (url) => Promise.resolve({
    json: () => Promise.resolve({ attentionSummary: (opts && opts.summary) || { needsYou: 0, error: 0, working: 0 } }),
  });
  w.localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: true, favicon: true, os: true, sound: false }));
  w.localStorage.setItem("serf-hub.notifications.v", "2");
  w.eval(src);
  return { w, fireNotif: (m, p) => notifHandler && notifHandler(m, p), fired };
}

(async () => {
  // 1) Baseline populates the title count from the summary.
  const a = boot({ summary: { needsYou: 2, error: 0, working: 1 } });
  await new Promise((r) => setTimeout(r, 20));
  if (!a.w.document.title.startsWith("(2) ")) {
    throw new Error("title after baseline = " + a.w.document.title);
  }
  // 2) A changed event updates counts and fires OS on into-needs_you (unfocused).
  a.fireNotif("serf/attention/changed", {
    changed: [{ threadId: "01X", title: "T", project: "p", level: "needs_you", prevLevel: "working" }],
    summary: { needsYou: 3, error: 0, working: 0 },
  });
  await new Promise((r) => setTimeout(r, 5));
  if (!a.w.document.title.startsWith("(3) ")) throw new Error("title after delta = " + a.w.document.title);
  if (a.fired.length !== 1) throw new Error("OS fired " + a.fired.length + " times, want 1");
  // 3) Focused tab suppresses OS but still counts.
  const b = boot({ focused: true, summary: { needsYou: 0, error: 0, working: 0 } });
  await new Promise((r) => setTimeout(r, 20));
  b.fireNotif("serf/attention/changed", {
    changed: [{ threadId: "01Y", title: "U", project: "p", level: "error", prevLevel: "working" }],
    summary: { needsYou: 0, error: 1, working: 0 },
  });
  await new Promise((r) => setTimeout(r, 5));
  if (b.fired.length !== 0) throw new Error("focused tab must suppress OS");
  if (!b.w.document.title.startsWith("(1) ")) throw new Error("error counts in title: " + b.w.document.title);
  // 4) No baseline yet -> no edge firing (event before fetch resolves).
  const c = boot({ summary: { needsYou: 0, error: 0, working: 0 } });
  c.fireNotif("serf/attention/changed", {
    changed: [{ threadId: "01Z", title: "V", project: "p", level: "needs_you", prevLevel: "idle" }],
    summary: { needsYou: 1, error: 0, working: 0 },
  });
  if (c.fired.length !== 0) throw new Error("no-baseline event must not fire OS");
  // 5) Sound pref plays a tone on the same into-transition as step 2 (the
  // removed poll-based test-notifications.js carried an equivalent check;
  // this keeps that coverage alive under the event-driven model).
  const d = boot({ summary: { needsYou: 0, error: 0, working: 0 } });
  await new Promise((r) => setTimeout(r, 20));
  let toneStarted = false;
  d.w.AudioContext = function () {
    this.createOscillator = () => ({
      frequency: { value: 0 },
      connect: () => {},
      start: () => { toneStarted = true; },
      stop: () => {},
    });
    this.destination = {};
    this.close = () => {};
  };
  d.w.localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: true, favicon: true, os: false, sound: true }));
  d.fireNotif("serf/attention/changed", {
    changed: [{ threadId: "01W", title: "W", project: "p", level: "needs_you", prevLevel: "working" }],
    summary: { needsYou: 1, error: 0, working: 0 },
  });
  await new Promise((r) => setTimeout(r, 5));
  if (!toneStarted) throw new Error("sound pref should play a tone on into-transition");
  // 6) Prefs-change + afterSettle re-apply (coverage carried over from the
  // removed poll-based test-notifications.js scenarios 6/6b): dispatching
  // "serf-hub:notifications-changed" re-reads prefs and re-applies the
  // title from the current summary in both directions, and an
  // "htmx:afterSettle" on body re-applies a title the swap clobbered.
  const f = boot({ summary: { needsYou: 2, error: 0, working: 0 } });
  await new Promise((r) => setTimeout(r, 20));
  if (!f.w.document.title.startsWith("(2) ")) throw new Error("prefs scenario baseline = " + f.w.document.title);
  f.w.localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: false, favicon: true, os: true, sound: false }));
  f.w.document.dispatchEvent(new f.w.CustomEvent("serf-hub:notifications-changed", { detail: { key: "title", value: false } }));
  await new Promise((r) => setTimeout(r, 5));
  if (f.w.document.title.indexOf("(") !== -1) throw new Error("title OFF should strip the count: " + f.w.document.title);
  f.w.localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: true, favicon: true, os: true, sound: false }));
  f.w.document.dispatchEvent(new f.w.CustomEvent("serf-hub:notifications-changed", { detail: { key: "title", value: true } }));
  await new Promise((r) => setTimeout(r, 5));
  if (!f.w.document.title.startsWith("(2) ")) throw new Error("title ON should restore the count: " + f.w.document.title);
  f.w.document.title = "stale after swap";
  f.w.document.body.dispatchEvent(new f.w.CustomEvent("htmx:afterSettle", { bubbles: true }));
  await new Promise((r) => setTimeout(r, 5));
  if (!f.w.document.title.startsWith("(2) ")) throw new Error("afterSettle should re-apply the count: " + f.w.document.title);
  console.log("ok");
})().catch((e) => { console.error(e); process.exit(1); });

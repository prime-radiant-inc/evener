// loudScope preference (Track A §2 ask-tiering): default "asks" gates
// OS/sound to askPending/error transitions only, leaving title/favicon
// counts untouched. Covers the v2->v3 migration backfill and the
// onAttentionChanged edge-fire gate via the SerfNotificationsInternal test
// seam. Mirrors the boot() harness in test-notifications-migration.js /
// test-notifications-attention.js.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const src = fs.readFileSync("../assets/notifications.js", "utf8");

function boot(pre, version) {
  const dom = new JSDOM(`<!DOCTYPE html><html><head><title>t</title></head><body></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.document.hasFocus = () => false;
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.fetch = () => Promise.resolve({ json: () => Promise.resolve({ attentionSummary: { needsYou: 0, error: 0, working: 0 } }) });
  if (pre) w.localStorage.setItem("serf-hub.notifications", JSON.stringify(pre));
  if (version) w.localStorage.setItem("serf-hub.notifications.v", version);
  w.eval(src);
  return w;
}

// Migration backfills loudScope: "asks" for an existing v2 blob with no
// loudScope key, and bumps the version so migration does not re-run.
const legacy = boot({ title: true, favicon: true, os: false, sound: false }, "2");
let prefs = JSON.parse(legacy.localStorage.getItem("serf-hub.notifications"));
if (prefs.loudScope !== "asks") {
  throw new Error("existing v2 users must backfill to the asks default, not off: " + JSON.stringify(prefs));
}
if (legacy.localStorage.getItem("serf-hub.notifications.v") !== "3") {
  throw new Error("migration must bump the version so it does not re-run");
}

// A fresh install gets loudScope: "asks" outright.
const fresh = boot(null, null);
prefs = JSON.parse(fresh.localStorage.getItem("serf-hub.notifications"));
if (prefs.loudScope !== "asks") {
  throw new Error("fresh install must default loudScope to asks: " + JSON.stringify(prefs));
}

// Gating: under "asks" (default), a generic needs_you transition (no error,
// no askPending) must NOT fire OS/sound; an askPending or error transition
// must.
const w = boot({ title: true, favicon: true, os: true, sound: true, loudScope: "asks" }, "3");
let osNotified = 0;
let soundPlayed = 0;
w.SerfNotificationsInternal.setTestHooks({
  fireOsNotification: () => { osNotified++; },
  playTone: () => { soundPlayed++; },
});
w.SerfNotificationsInternal.setLeaderForTest(true);
w.SerfNotificationsInternal.setBaselineForTest({ needsYou: 0, error: 0, working: 0 });

w.SerfNotificationsInternal.onAttentionChanged({
  summary: { needsYou: 1, error: 0, working: 0 },
  changed: [{ id: "01A", level: "needs_you", prevLevel: "idle", askPending: false }],
});
if (osNotified !== 0) {
  throw new Error(`loudScope "asks" must suppress a generic needs_you settle, got ${osNotified} OS fires`);
}

w.SerfNotificationsInternal.onAttentionChanged({
  summary: { needsYou: 2, error: 0, working: 0 },
  changed: [{ id: "01B", level: "needs_you", prevLevel: "idle", askPending: true }],
});
if (osNotified !== 1) {
  throw new Error(`loudScope "asks" must still fire for an askPending transition, got ${osNotified}`);
}
if (soundPlayed !== 1) {
  throw new Error(`loudScope "asks" askPending transition must also play the tone, got ${soundPlayed}`);
}

// An error transition must fire regardless of askPending.
w.SerfNotificationsInternal.onAttentionChanged({
  summary: { needsYou: 2, error: 1, working: 0 },
  changed: [{ id: "01C", level: "error", prevLevel: "idle", askPending: false }],
});
if (osNotified !== 2) {
  throw new Error(`loudScope "asks" must still fire for an error transition, got ${osNotified}`);
}

console.log("ok");

// Versioned prefs migration: absent blob -> new defaults ON (title+favicon);
// existing partial blob -> absent keys backfilled explicit false.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync("../assets/notifications.js", "utf8");

function boot(pre) {
  const dom = new JSDOM(`<!DOCTYPE html><html><head><title>t</title></head><body></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.document.hasFocus = () => false;
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.fetch = () => Promise.resolve({ json: () => Promise.resolve({ attentionSummary: { needsYou: 0, error: 0, working: 0 } }) });
  if (pre) w.localStorage.setItem("serf-hub.notifications", JSON.stringify(pre));
  w.eval(src);
  return w;
}

const fresh = boot(null);
const freshPrefs = JSON.parse(fresh.localStorage.getItem("serf-hub.notifications"));
if (freshPrefs.title !== true || freshPrefs.favicon !== true || freshPrefs.os !== false || freshPrefs.sound !== false) {
  throw new Error("fresh defaults wrong: " + JSON.stringify(freshPrefs));
}
if (fresh.localStorage.getItem("serf-hub.notifications.v") !== "3") throw new Error("version stamp missing");

const legacy = boot({ os: true });
const legacyPrefs = JSON.parse(legacy.localStorage.getItem("serf-hub.notifications"));
if (legacyPrefs.title !== false || legacyPrefs.favicon !== false || legacyPrefs.os !== true) {
  throw new Error("legacy backfill wrong: " + JSON.stringify(legacyPrefs));
}
console.log("ok");

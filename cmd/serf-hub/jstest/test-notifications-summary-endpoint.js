// fetchBaseline must hit the lightweight summary-only endpoint
// (/api/tree?summary=1), never the full tree: the baseline only reads
// attentionSummary, and the full tree payload is megabytes on large hubs.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const src = fs.readFileSync("../assets/notifications.js", "utf8");

(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><head><title>serf hub</title></head><body></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.document.hasFocus = () => false;
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.Notification = function () {};
  w.Notification.permission = "granted";
  const urls = [];
  w.fetch = (url) => {
    urls.push(String(url));
    return Promise.resolve({ json: () => Promise.resolve({ attentionSummary: { needsYou: 1, error: 0, working: 0 } }) });
  };
  w.localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: true, favicon: false, os: false, sound: false }));
  w.localStorage.setItem("serf-hub.notifications.v", "3");
  w.eval(src);
  await new Promise((r) => setTimeout(r, 20));
  if (urls.length === 0) throw new Error("baseline fetch never issued");
  for (const u of urls) {
    if (u !== "/api/tree?summary=1") throw new Error("baseline fetched " + u + ", want /api/tree?summary=1");
  }
  if (!w.document.title.startsWith("(1) ")) throw new Error("summary baseline must still drive the title: " + w.document.title);
  console.log("ok");
})().catch((e) => { console.error(e); process.exit(1); });

// Sidebar should refresh from AppWire notifications instead of fixed polling.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SIDEBAR_PATH = "../assets/sidebar.js";
const sidebarSrc = fs.readFileSync(SIDEBAR_PATH, "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <aside id="sidebar"></aside>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
const handlers = [];
const triggered = [];
window.SerfAppwire = {
  onNotification(fn) {
    handlers.push(fn);
    return () => {};
  },
  onConnectionRestored(fn) {
    handlers.push(() => fn());
    return () => {};
  },
};
window.htmx = {
  trigger(target, name) {
    triggered.push({ target, name });
  },
};

window.eval(sidebarSrc);

setTimeout(() => {
  const failures = [];
  const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

  pass(handlers.length > 0, "sidebar should subscribe to AppWire notifications");
  pass(handlers.length === 2, "sidebar should register exactly 2 AppWire handlers (onNotification + onConnectionRestored)");
  handlers[0]("thread/status/changed", { threadId: "th_1" });

  setTimeout(() => {
    pass(
      triggered.some(t => t.name === "sidebar:refresh" && t.target === window.document.body),
      "thread notification should trigger sidebar:refresh on document.body"
    );
    if (failures.length === 0) {
      console.log("PASS: sidebar refreshes from AppWire notifications");
      process.exit(0);
    }
    for (const f of failures) console.log(" " + f);
    process.exit(1);
  }, 80);
}, 20);

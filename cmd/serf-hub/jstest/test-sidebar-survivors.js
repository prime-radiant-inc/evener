// Surviving contracts carried into the rewrite: rail toggle + persistence,
// and mobile drawer close API.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <button data-sidebar-rail-toggle></button>
  <aside id="sidebar"></aside></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
const w = dom.window;
w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve({ needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } }) });
w.htmx = { process() {} };
w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
w.eval(src);
w.document.querySelector("[data-sidebar-rail-toggle]").dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
if (!w.document.body.hasAttribute("data-sidebar-rail")) throw new Error("rail toggle must set body[data-sidebar-rail]");
if (w.localStorage.getItem("serf-hub.sidebar.rail") !== "true") throw new Error("rail must persist");
if (typeof w.SerfSidebar.close !== "function") throw new Error("drawer close API must survive");
console.log("ok survivors: rail toggle + persistence + close API");
process.exit(0);

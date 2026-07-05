// Surviving contracts carried into the rewrite: rail toggle + persistence,
// mobile drawer close API, and the subagent-row open-beside delegate.
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

// onSidebarOpenBeside (carried verbatim from the pre-rewrite sidebar.js) is a
// document-level delegated click listener, so it fires regardless of what
// currently lives inside #sidebar — inject a minimal .subagent-row-wrap
// fixture and confirm the click still reaches window.SerfPanes.open. This
// runs synchronously (no wait/setTimeout) so it lands before the async
// fetchTree() render pass would otherwise replace #sidebar's contents.
const openCalls = [];
w.SerfPanes = { open(href, title) { openCalls.push({ href, title }); } };
const wrap = w.document.createElement("div");
wrap.className = "subagent-row-wrap";
wrap.setAttribute("data-ref", "01SUBAGENT");
wrap.setAttribute("data-title", "do some work");
const besideBtn = w.document.createElement("span");
besideBtn.className = "open-beside-btn";
besideBtn.setAttribute("role", "button");
wrap.appendChild(besideBtn);
w.document.getElementById("sidebar").appendChild(wrap);
besideBtn.dispatchEvent(new w.MouseEvent("click", { bubbles: true, cancelable: true }));
if (openCalls.length !== 1) throw new Error("sidebar open-beside must call SerfPanes.open once, got " + openCalls.length);
if (openCalls[0].href !== "/thread/01SUBAGENT") throw new Error("sidebar open-beside wrong href: " + openCalls[0].href);
if (openCalls[0].title !== "do some work") throw new Error("sidebar open-beside wrong title: " + openCalls[0].title);

console.log("ok survivors: rail toggle + persistence + close API + subagent open-beside");
process.exit(0);

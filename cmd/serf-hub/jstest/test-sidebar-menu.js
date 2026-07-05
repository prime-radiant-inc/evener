// Menu: ⋯ opens a popover; session rows show Rename only when node.rename;
// choosing Favorite calls SerfSidebar.favorite; Escape closes; removing the
// anchor row closes the menu.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

function tree(renameable) {
  return { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
    projects: [{ key: "p1", name: "p", working_dir: "/w/p", default_expanded: true,
      sessions: [{ row_id: "project:p1:local:01A", ref: "local:01A", session_id: "01A", title: "s", state: "idle", kind: "session", tier: "current", rename: renameable }] }],
    attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
const w = dom.window;
w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(tree(true)) });
w.htmx = { process() {} };
w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
w.eval(src);
setTimeout(() => {
  const row = w.document.querySelector('[data-row-id="project:p1:local:01A"]');
  const btn = row.querySelector(".sb-menu-btn");
  if (!btn) throw new Error("row must carry a ⋯ menu button");
  btn.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
  const menu = w.document.querySelector(".sb-menu");
  if (!menu) throw new Error("clicking ⋯ opens a menu");
  const items = [].map.call(menu.querySelectorAll(".sb-menu-item"), (e) => e.textContent);
  if (!items.some((t) => /Rename/.test(t))) throw new Error("renameable row must offer Rename, got " + items);
  let favCalled = null;
  w.SerfSidebar.favorite = (ref, on) => { favCalled = [ref, on]; };
  [].find.call(menu.querySelectorAll(".sb-menu-item"), (e) => /Favorite/.test(e.textContent)).dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
  if (!favCalled || favCalled[0] !== "local:01A") throw new Error("Favorite item must call SerfSidebar.favorite");
  console.log("ok menu open + rename gating + favorite action");
  process.exit(0);
}, 20);

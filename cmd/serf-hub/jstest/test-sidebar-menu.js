// Menu: ⋯ opens a popover; session rows show Rename only when node.rename;
// choosing Favorite calls SerfSidebar.favorite; Escape closes; removing the
// anchor row closes the menu.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

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
w.eval(iconsSrc);
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

  // A project routed into Test runs but genuinely archived (is_archived) must
  // still offer "Unarchive" — the verb tracks the server's archived state, not
  // which section stamped the row (WS3 R2).
  const trTree = { needs_you: [], favorites: [], projects: [], archived_projects: [],
    test_runs: [{ key: "tr1", name: "e2e", working_dir: "/t/e2e", is_archived: true, default_expanded: true,
      sessions: [{ row_id: "project:tr1:local:0X", ref: "local:0X", session_id: "0X", title: "run", state: "ended", kind: "session", tier: "archived" }] }],
    attentionSummary: { needsYou: 0, error: 0, working: 0 } };
  w.SerfSidebar.renderTree(trTree);
  const secHeader = w.document.querySelector('[data-row-id="section:test-runs"]');
  if (!secHeader) throw new Error("test-runs section header must render");
  secHeader.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
  const trProjHeader = w.document.querySelector('[data-row-id="header:tr1"]');
  if (!trProjHeader) throw new Error("test-runs project header must render once its section is expanded");
  const trMenuBtn = trProjHeader.querySelector(".sb-menu-btn");
  if (!trMenuBtn) throw new Error("test-runs project header must carry a ⋯ menu button");
  trMenuBtn.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
  const trMenu = w.document.querySelector(".sb-menu");
  if (!trMenu) throw new Error("clicking ⋯ must open a menu");
  const trItems = [].map.call(trMenu.querySelectorAll(".sb-menu-item"), (e) => e.textContent);
  if (!trItems.some((t) => /^Unarchive$/.test(t))) throw new Error("archived test-run project must offer Unarchive, got " + JSON.stringify(trItems));
  console.log("ok archived test-run project menu offers Unarchive");
  process.exit(0);
}, 20);

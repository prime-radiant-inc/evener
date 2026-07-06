// Pin: projectMenuItems()'s "New session" (and "Settings") links must
// encodeURIComponent() the project's working_dir before putting it in the
// dir=/cwd= query param, so a working_dir containing space/&/# doesn't
// corrupt the query string or get silently truncated (WS3 T23).
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

function boot() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  const posts = [];
  w.fetch = (url, opts) => {
    if (opts && opts.method === "POST") { posts.push({ url, body: JSON.parse(opts.body) }); return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) }); }
    return Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  };
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.confirm = () => true; // confirmDeleteProject's window.confirm gate
  w.eval(iconsSrc);
  w.eval(src);
  return { w, posts };
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
function treeWithProject() {
  var t = emptyTree();
  t.projects = [{
    key: "p1", name: "p", working_dir: "/w/a b&c#d", default_expanded: true,
    sessions: [],
  }];
  return t;
}

// spyOnLocationHref: copied verbatim from test-renderer-fork-ref.js, with one
// required change — that version records "/" + url.path.join("/") (path
// only, dropping the query string, which the fork-ref test never needed);
// this test needs the query string too, so the capture is extended to
// append "?" + url.query when present (the whatwg-url URL record passed
// here carries a .query string field alongside .path).
function spyOnLocationHref(window) {
  const loc = window.location;
  const implSym = Object.getOwnPropertySymbols(loc)
    .find((s) => s.toString() === "Symbol(impl)");
  if (!implSym) throw new Error("jsdom location impl symbol not found — jsdom API changed?");
  const impl = loc[implSym];
  const navigations = [];
  const origNavigate = impl._locationObjectSetterNavigate.bind(impl);
  impl._locationObjectSetterNavigate = (url) => {
    navigations.push("/" + url.path.join("/") + (url.query ? "?" + url.query : ""));
  };
  return { navigations, restore: () => { impl._locationObjectSetterNavigate = origNavigate; } };
}

function openProjectMenu(w, key) {
  const header = w.document.querySelector('[data-row-id="header:' + key + '"]');
  if (!header) throw new Error("project header must render for key " + key);
  const menuBtn = header.querySelector(".sb-menu-btn");
  if (!menuBtn) throw new Error("project header must carry a ⋯ menu button");
  menuBtn.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
  const menu = w.document.querySelector(".sb-menu");
  if (!menu) throw new Error("clicking ⋯ must open a menu");
  return menu;
}

function clickMenuItem(w, menu, labelRe) {
  const item = [].find.call(menu.querySelectorAll(".sb-menu-item"), (e) => labelRe.test(e.textContent));
  if (!item) throw new Error("menu item matching " + labelRe + " not found");
  item.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
}

// 1. "New session" -> /new?dir=<escaped working_dir>
{
  const { w } = boot();
  w.SerfSidebar.renderTree(treeWithProject());
  const locationSpy = spyOnLocationHref(w);
  const menu = openProjectMenu(w, "p1");
  clickMenuItem(w, menu, /New session/);
  const nav = locationSpy.navigations[0];
  if (!/\/new\?dir=%2Fw%2Fa%20b%26c%23d/.test(nav)) {
    throw new Error("New session must navigate with encodeURIComponent-escaped working_dir, got " + JSON.stringify(nav));
  }
}

// 2. "Settings" -> /settings/project?cwd=<escaped working_dir> (double
// coverage: identical encodeURIComponent(p.working_dir) call, lower marginal
// value than case 1 but cheap to assert).
{
  const { w } = boot();
  w.SerfSidebar.renderTree(treeWithProject());
  const locationSpy = spyOnLocationHref(w);
  const menu = openProjectMenu(w, "p1");
  clickMenuItem(w, menu, /^Settings$/);
  const nav = locationSpy.navigations[0];
  if (!/\/settings\/project\?cwd=%2Fw%2Fa%20b%26c%23d/.test(nav)) {
    throw new Error("Settings must navigate with encodeURIComponent-escaped working_dir, got " + JSON.stringify(nav));
  }
}

console.log("ok sidebar working_dir escaping: New session + Settings hrefs are encodeURIComponent-escaped");
process.exit(0); // sidebar.js's 60s idle-resync interval keeps the event loop alive otherwise

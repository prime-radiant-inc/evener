// Desktop collapsed sidebar (issue #33): collapsed means FULLY collapsed.
// The 56px icon rail is gone — body.app[data-sidebar-rail] hides the entire
// sidebar (zero-width, not a strip). Reopen affordances, mirroring the phone /
// short-landscape drawer pattern: the app-shell .app-nav-toggle chip appears
// top-left and opens the sidebar as an overlay drawer
// (body[data-sidebar-rail][data-sidebar-open]); ⌘B and the in-header mode
// toggle still cycle back to pane. Session navigation from the drawer closes
// it (htmx:beforeRequest), so the drawer never hangs over a fresh session.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");
const sidebarSrc = fs.readFileSync(path.resolve(__dirname, "../assets/sidebar.js"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

function ruleBody(selector) {
  const i = css.indexOf(selector);
  if (i < 0) return null;
  const open = css.indexOf("{", i);
  const close = css.indexOf("}", open);
  return css.slice(open + 1, close);
}

// 1) Collapsed = hidden: the whole sidebar leaves the layout.
const hiddenBody = ruleBody("body.app[data-sidebar-rail] #sidebar");
pass(!!hiddenBody && /display:\s*none/.test(hiddenBody),
  "collapsed mode (data-sidebar-rail) must hide #sidebar entirely (display:none), not shrink it to a strip");

// 2) The 56px rail strip is gone: no rail-mode rule may set a 56px width.
pass(!/\[data-sidebar-rail\][^{}]*\{[^}]*56px/.test(css),
  "no data-sidebar-rail rule may size a 56px rail — the strip is retired");

// 3) No rail-mode per-element strip styling survives (the sidebar is hidden
// whole, so hiding .sb-row text columns etc. is dead weight).
pass(!/body\.app\[data-sidebar-rail\]\s+\.sb-row/.test(css),
  "collapsed mode must not restyle .sb-row (the whole sidebar is hidden)");

// 4) The drag resizer is a sibling of #sidebar, so it still needs its own hide.
const resizerBody = ruleBody("body.app[data-sidebar-rail] .sidebar-resizer");
pass(!!resizerBody && /display:\s*none/.test(resizerBody),
  "collapsed mode must hide the sidebar resizer");

// 5) Reopen affordance: the app-shell nav toggle chip appears when collapsed
// (it is display:none on desktop otherwise).
const chipBody = ruleBody("body.app[data-sidebar-rail] .app-nav-toggle");
pass(!!chipBody && /display:\s*inline-flex/.test(chipBody) && /position:\s*fixed/.test(chipBody),
  "collapsed mode must reveal the .app-nav-toggle chip (fixed) so the sidebar can be reopened");

// 6) The chip reopens the sidebar as an overlay drawer over the workspace.
const drawerBody = ruleBody("body.app[data-sidebar-rail][data-sidebar-open] #sidebar");
pass(!!drawerBody && /display:\s*block/.test(drawerBody) && /position:\s*fixed/.test(drawerBody),
  "collapsed + open must show #sidebar as a fixed overlay drawer");

// 7) The workspace header makes room for the floating chip.
pass(/body\.app\[data-sidebar-rail\]\s+\.workspace-header\s*\{[^}]*padding-left:\s*calc\(var\(--tap-min\)/.test(css),
  "collapsed mode must pad the workspace header clear of the floating nav chip");

// --- Behavioral: on a desktop viewport with a pinned collapsed mode, the
// chip opens the drawer and navigating from inside the sidebar closes it. ---
(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><body class="app">
    <button class="app-nav-toggle" data-sidebar-toggle>☰</button>
    <aside id="sidebar"><a class="sb-row" href="/thread/1">session</a></aside>
    <main id="workspace"></main>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
  const w = dom.window;
  w.localStorage.setItem("serf-hub.sidebar.rail", "rail");
  // Desktop viewport (≥1200px): explicit rail pins the collapsed state.
  w.matchMedia = (q) => ({
    matches: q === "(min-width: 1200px)",
    addEventListener() {}, addListener() {},
  });
  w.fetch = () => new Promise(() => {});
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(sidebarSrc);

  pass(w.document.body.hasAttribute("data-sidebar-rail"),
    "pinned rail pref sets data-sidebar-rail on desktop (collapsed)");
  pass(!w.document.body.hasAttribute("data-sidebar-open"), "drawer starts closed");

  w.document.querySelector(".app-nav-toggle").dispatchEvent(new w.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(w.document.body.hasAttribute("data-sidebar-open"),
    "clicking the nav chip opens the collapsed sidebar drawer");

  // Session navigation from inside the drawer closes it (htmx:beforeRequest).
  const row = w.document.querySelector("#sidebar .sb-row");
  const evt = new w.Event("htmx:beforeRequest", { bubbles: true, cancelable: true });
  Object.defineProperty(evt, "detail", { value: { elt: row } });
  w.document.dispatchEvent(evt);
  pass(!w.document.body.hasAttribute("data-sidebar-open"),
    "navigating to a session from the drawer closes it");

  // ⌘B cycles rail → pane, lifting the collapsed state entirely.
  w.document.dispatchEvent(new w.KeyboardEvent("keydown", { key: "b", metaKey: true, bubbles: true, cancelable: true }));
  pass(!w.document.body.hasAttribute("data-sidebar-rail"),
    "⌘B cycles collapsed → pane (sidebar re-pinned open)");
  pass(w.SerfSidebar.readSidebarMode() === "pane", "⌘B persists the pane mode");

  if (failures.length > 0) {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
  console.log("PASS: desktop collapsed sidebar — hidden, chip reopens drawer, ⌘B re-pins");
  process.exit(0);
})();

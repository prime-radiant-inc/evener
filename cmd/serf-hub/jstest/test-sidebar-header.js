// Regression guard for the WS3 sidebar rebuild (ba785b35) that deleted the
// server-rendered sidebar.html partial and, with it, the .sidebar-header block
// (rail-toggle, close, new-session, search, settings) — leaving users with no
// visible way to start a session, search, or reach settings. sidebar.js only
// ever builds the .sb-tree rows; the header must be restored as static chrome
// in app.html and must survive the client-rendered tree taking over #sidebar.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const appHtml = fs.readFileSync(path.resolve(__dirname, "../templates/app.html"), "utf8");
const sidebarSrc = fs.readFileSync(path.resolve(__dirname, "../assets/sidebar.js"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// --- Structural: app.html must define .sidebar-header inside <aside id="sidebar">. ---
const asideMatch = appHtml.match(/<aside id="sidebar"[^>]*>[\s\S]*?<\/aside>/);
pass(!!asideMatch, "app.html must contain <aside id=\"sidebar\">...</aside>");
const asideHtml = asideMatch ? asideMatch[0] : "";

pass(/class="sidebar-header"/.test(asideHtml), "#sidebar must contain a .sidebar-header block");
pass(/data-sidebar-rail-toggle/.test(asideHtml), "header must contain the rail-toggle control");
pass(/class="sidebar-close"[^>]*data-sidebar-toggle/.test(asideHtml), "header must contain a .sidebar-close wired to data-sidebar-toggle");
pass(/class="sidebar-action sidebar-action-new"/.test(asideHtml), "header must contain the new-session action");
pass(/class="sidebar-action sidebar-action-search"/.test(asideHtml) && /data-search-trigger/.test(asideHtml),
  "header must contain the search action wired to data-search-trigger");
pass(/class="sidebar-action settings-link"/.test(asideHtml), "header must contain the settings action");

// --- Wiring: new-session + settings must hit routes that are actually live today
// (not the WS3-deleted /_partials/workspace/spawn callers or old settings URLs —
// these two specific routes are still served by handleWorkspaceSpawn /
// renderSettingsPartial per cmd/serf-hub/web.go, so reusing them is correct). ---
pass(/sidebar-action-new"[^>]*href="\/new"/.test(asideHtml), "new-session action must link to /new");
pass(/sidebar-action-new"[^>]*hx-get="\/_partials\/workspace\/spawn"/.test(asideHtml),
  "new-session action must hx-get /_partials/workspace/spawn (still served by handleWorkspaceSpawn)");
pass(/settings-link"[^>]*href="\/settings"/.test(asideHtml), "settings action must link to /settings");
pass(/settings-link"[^>]*hx-get="\/_partials\/settings\/general"/.test(asideHtml),
  "settings action must hx-get /_partials/settings/general (still served by renderSettingsPartial)");
const newActionTag = (asideHtml.match(/<a class="sidebar-action sidebar-action-new"[^>]*>/) || [""])[0];
pass(/hx-target="#workspace"/.test(newActionTag),
  "new-session action must target #workspace like every other workspace navigation");

// --- Behavioral: the header must survive sidebar.js's async skeleton -> tree
// takeover of #sidebar, and its controls must be live (rail-toggle, close). ---
function boot() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body class="app">${asideHtml}</body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(sidebarSrc);
  return w;
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}

const w = boot();

pass(!!w.document.querySelector(".sidebar-header"), "header must be present right after sidebar.js loads (skeleton paint)");

w.SerfSidebar.renderTree(emptyTree());
pass(!!w.document.querySelector(".sidebar-header"), "header must survive renderTree() taking over #sidebar for the .sb-tree");
pass(!!w.document.querySelector(".sb-tree"), "renderTree must still produce the .sb-tree nav alongside the header");

// Render twice more (simulating live-update refreshes) to guard against the
// header being re-added/duplicated or dropped on subsequent renders.
w.SerfSidebar.renderTree(emptyTree());
w.SerfSidebar.renderTree(emptyTree());
pass(w.document.querySelectorAll(".sidebar-header").length === 1, "header must not be duplicated across repeated renders");

// Rail-toggle: delegated document click handler flips body[data-sidebar-rail].
const railBtn = w.document.querySelector("[data-sidebar-rail-toggle]");
pass(!!railBtn, "rail-toggle button must exist in the DOM");
if (railBtn) {
  pass(!w.document.body.hasAttribute("data-sidebar-rail"), "rail starts disabled");
  railBtn.dispatchEvent(new w.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(w.document.body.hasAttribute("data-sidebar-rail"), "clicking rail-toggle enables rail mode");
}

// Close button: shares the data-sidebar-toggle wiring with the app-shell hamburger.
const closeBtn = w.document.querySelector(".sidebar-close[data-sidebar-toggle]");
pass(!!closeBtn, "close button must exist and carry data-sidebar-toggle");
if (closeBtn) {
  pass(!w.document.body.hasAttribute("data-sidebar-open"), "drawer starts closed");
  closeBtn.dispatchEvent(new w.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(w.document.body.hasAttribute("data-sidebar-open"), "clicking close toggles the drawer open (same data-sidebar-toggle wiring as the hamburger)");
}

if (failures.length === 0) {
  console.log("PASS: sidebar header restored — rail-toggle, close, new, search, settings all present and wired");
  process.exit(0);
}
for (const f of failures) console.log(" " + f);
process.exit(1);

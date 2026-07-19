// #27 — Mobile ⋯ menu modal. Under matchMedia "(max-width: 767px)" the
// sidebar row/project ⋯ menu must open as a full-width modal sheet on a
// tap-to-dismiss backdrop scrim, not as the anchored desktop mini-popover.
// Desktop behavior must stay exactly as-is.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const sidebarSrc = fs.readFileSync(path.resolve(__dirname, "../assets/sidebar.js"), "utf8");
const iconsSrc = fs.readFileSync(path.resolve(__dirname, "../assets/icons.js"), "utf8");
const focusTrapSrc = fs.readFileSync(path.resolve(__dirname, "../assets/focus-trap.js"), "utf8");

const failures = [];
const pass = (c, m) => { if (!c) failures.push("FAIL: " + m); };
const flush = () => new Promise((r) => setTimeout(r, 20));

function tree() {
  return { needs_you: [], favorites: [], archived_projects: [], test_runs: [],
    projects: [{ key: "p1", name: "p", working_dir: "/w/p", default_expanded: true,
      sessions: [{ row_id: "project:p1:local:01A", ref: "local:01A", session_id: "01A", title: "s", state: "idle", kind: "session", tier: "current", rename: true }] }],
    attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
const emptyTree = { needs_you: [], favorites: [], archived_projects: [], test_runs: [], projects: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };

function boot(isPhone) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
  const w = dom.window;
  w.matchMedia = (q) => ({
    matches: isPhone && /max-width:\s*767px/.test(q),
    media: q, onchange: null,
    addListener() {}, removeListener() {}, addEventListener() {}, removeEventListener() {}, dispatchEvent() { return false; },
  });
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(tree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(iconsSrc);
  w.eval(focusTrapSrc);
  w.eval(sidebarSrc);
  return w;
}

function openRowMenu(w) {
  const row = w.document.querySelector('[data-row-id="project:p1:local:01A"]');
  const btn = row && row.querySelector(".sb-menu-btn");
  if (!btn) throw new Error("row must carry a ⋯ menu button");
  btn.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
  return btn;
}

(async () => {
  // ── Mobile: full-width modal sheet on a scrim ──────────────────────────
  {
    const w = boot(true);
    await flush();
    const btn = openRowMenu(w);
    const menu = w.document.querySelector(".sb-menu");
    const scrim = w.document.querySelector(".sb-menu-scrim");
    pass(menu, "mobile: clicking ⋯ opens a menu");
    pass(scrim, "mobile: menu opens with a backdrop scrim");
    pass(menu && menu.classList.contains("sb-menu-modal"), "mobile: menu carries the modal sheet class");
    pass(scrim && menu && menu.parentNode === scrim, "mobile: sheet lives inside the scrim overlay");
    pass(menu && !menu.style.top && !menu.style.left, "mobile: sheet is not anchored under the button (no inline top/left)");
    pass(menu && menu.getAttribute("role") === "menu", "mobile: menu keeps role=menu");
    const items = menu ? menu.querySelectorAll(".sb-menu-item") : [];
    pass(items.length > 0 && items[0].getAttribute("role") === "menuitem", "mobile: items keep role=menuitem");
    pass(items.length > 0 && w.document.activeElement === items[0], "mobile: first menu item is focused on open");
    pass(w.document.getElementById("sidebar").hasAttribute("inert"), "mobile: background is inert while the modal is open");
    await flush(); // let openMenu install its document click listener

    // Arrow-key navigation still works.
    w.document.dispatchEvent(new w.KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    pass(items.length > 1 && w.document.activeElement === items[1], "mobile: ArrowDown moves focus to the next item");
    w.document.dispatchEvent(new w.KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }));
    pass(w.document.activeElement === items[0], "mobile: ArrowUp moves focus back to the first item");

    // Escape closes, tears down the scrim, and refocuses the anchor.
    w.document.dispatchEvent(new w.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    pass(!w.document.querySelector(".sb-menu"), "mobile: Escape closes the menu");
    pass(!w.document.querySelector(".sb-menu-scrim"), "mobile: Escape removes the scrim");
    pass(w.document.activeElement === btn, "mobile: Escape refocuses the ⋯ anchor");
    pass(!w.document.getElementById("sidebar").hasAttribute("inert"), "mobile: inert is lifted after close");

    // Backdrop tap dismisses (click lands on the scrim, outside the sheet).
    openRowMenu(w);
    await flush();
    const scrim2 = w.document.querySelector(".sb-menu-scrim");
    pass(scrim2, "mobile: menu reopens with a scrim");
    if (scrim2) scrim2.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
    pass(!w.document.querySelector(".sb-menu"), "mobile: tapping the scrim dismisses the menu");
    pass(!w.document.querySelector(".sb-menu-scrim"), "mobile: tapping the scrim removes it");

    // Anchor-row removal (keyed reconcile) tears down the scrim too.
    openRowMenu(w);
    await flush();
    pass(!!w.document.querySelector(".sb-menu-scrim"), "mobile: menu open before rerender");
    w.SerfSidebar.renderTree(emptyTree);
    pass(!w.document.querySelector(".sb-menu"), "mobile: removing the anchor row closes the menu");
    pass(!w.document.querySelector(".sb-menu-scrim"), "mobile: removing the anchor row removes the scrim");
  }

  // ── Desktop: anchored mini-popover, no scrim (unchanged) ───────────────
  {
    const w = boot(false);
    await flush();
    const btn = openRowMenu(w);
    const menu = w.document.querySelector(".sb-menu");
    pass(menu, "desktop: clicking ⋯ opens a menu");
    pass(!w.document.querySelector(".sb-menu-scrim"), "desktop: no scrim is created");
    pass(menu && !menu.classList.contains("sb-menu-modal"), "desktop: menu keeps popover styling (no modal class)");
    pass(menu && menu.parentNode === w.document.body, "desktop: menu is appended to <body>");
    pass(menu && menu.style.position === "absolute" && menu.style.top !== "", "desktop: menu is anchored under the button");
    const items = menu ? menu.querySelectorAll(".sb-menu-item") : [];
    pass(items.length > 0 && w.document.activeElement === items[0], "desktop: first menu item is focused on open");
    pass(!w.document.getElementById("sidebar").hasAttribute("inert"), "desktop: background is not inert (popover, not modal)");
    await flush();
    w.document.dispatchEvent(new w.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    pass(!w.document.querySelector(".sb-menu"), "desktop: Escape closes the menu");
    pass(w.document.activeElement === btn, "desktop: Escape refocuses the anchor");
  }

  if (failures.length) {
    failures.forEach((f) => console.log(f));
    process.exit(1);
  }
  console.log("ok mobile ⋯ menu opens as full-width modal sheet; desktop popover unchanged");
  process.exit(0);
})().catch((e) => { console.error(e); process.exit(1); });

// Mobile nav reachability: the sidebar toggle must live in the app shell, not a
// per-page header, so EVERY page rendered into #workspace (session, new,
// settings, welcome) can open the off-canvas sidebar. Regression guard for the
// refactor that replaced the per-page .header-hamburger with one persistent
// .app-nav-toggle — and for the drawer wiring it depends on.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const appHtml = fs.readFileSync(path.resolve(__dirname, "../templates/app.html"), "utf8");
const workspaceHtml = fs.readFileSync(path.resolve(__dirname, "../templates/partials/workspace.html"), "utf8");
const sidebarSrc = fs.readFileSync(path.resolve(__dirname, "../assets/sidebar.js"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const wait = () => new Promise((r) => setTimeout(r, 30));

// --- Structural invariant: the toggle is in the shell, not per-page. ---
pass(/class="app-nav-toggle"/.test(appHtml), "app shell must contain the persistent .app-nav-toggle");
pass(/app-nav-toggle[^>]*data-sidebar-toggle/.test(appHtml), ".app-nav-toggle must carry data-sidebar-toggle");
pass(!/header-hamburger/.test(workspaceHtml),
  "workspace header must NOT carry its own hamburger (nav lives in the shell, so all pages have it)");

// --- Behavioral: clicking a [data-sidebar-toggle] opens/closes the drawer. ---
(async () => {
  const dom = new JSDOM(`<!DOCTYPE html><html><body class="app">
    <button class="app-nav-toggle" data-sidebar-toggle>☰</button>
    <aside id="sidebar"></aside>
    <main id="workspace"></main>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
  const { window } = dom;
  window.eval(sidebarSrc);
  window.document.dispatchEvent(new window.Event("DOMContentLoaded", { bubbles: true }));
  await wait();

  const toggle = window.document.querySelector(".app-nav-toggle");
  pass(!window.document.body.hasAttribute("data-sidebar-open"), "drawer starts closed");

  toggle.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(window.document.body.hasAttribute("data-sidebar-open"), "clicking the toggle opens the drawer");

  toggle.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(!window.document.body.hasAttribute("data-sidebar-open"), "clicking the toggle again closes the drawer");

  if (failures.length === 0) {
    console.log("PASS: nav toggle — shell placement + drawer open/close");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})();

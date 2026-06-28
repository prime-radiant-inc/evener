// Verify skeleton.js sets data-loading on htmx swap targets at the start of
// a request and removes it after the swap completes. Targets are inferred
// from event.detail.target (when present) or fall back to the element that
// initiated the request.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SKELETON_PATH = "../assets/skeleton.js";
const skeletonSrc = fs.readFileSync(SKELETON_PATH, "utf8");

const dom = new JSDOM(
  `<!DOCTYPE html><html><body>
     <aside id="sidebar"></aside>
     <main id="workspace"></main>
     <div id="settings-content"></div>
   </body></html>`,
  { runScripts: "outside-only", pretendToBeVisual: true }
);
const { window } = dom;
window.eval(skeletonSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

function dispatch(name, target, detail) {
  const ev = new window.CustomEvent(name, { bubbles: true, detail: detail || {} });
  Object.defineProperty(ev, "target", { value: target, enumerable: true });
  window.document.body.dispatchEvent(ev);
}

// Sidebar refreshes are background navigation updates. They should not flip
// the whole sidebar into loading/skeleton chrome.
const sidebar = window.document.getElementById("sidebar");
dispatch("htmx:beforeRequest", sidebar, { target: sidebar });
pass(!sidebar.hasAttribute("data-loading"), "sidebar should not get data-loading on refresh");

// htmx:afterSwap remains harmless.
dispatch("htmx:afterSwap", sidebar, { target: sidebar });
pass(!sidebar.hasAttribute("data-loading"), "sidebar data-loading should be cleared after htmx:afterSwap");

// htmx:responseError no-op guard for sidebar: sidebar never receives
// data-loading (set() skips it), so this pair confirms the handler does not
// accidentally set the attribute on error paths. It is NOT meaningful coverage
// of clear() — see the workspace case below for that.
dispatch("htmx:beforeRequest", sidebar, { target: sidebar });
dispatch("htmx:responseError", sidebar, { target: sidebar });
pass(!sidebar.hasAttribute("data-loading"), "sidebar data-loading should remain absent after htmx:responseError (no-op guard)");

// Workspace target — happy path via afterSwap.
const workspace = window.document.getElementById("workspace");
dispatch("htmx:beforeRequest", workspace, { target: workspace });
pass(workspace.hasAttribute("data-loading"), "workspace should have data-loading");
dispatch("htmx:afterSwap", workspace, { target: workspace });
pass(!workspace.hasAttribute("data-loading"), "workspace data-loading cleared after htmx:afterSwap");

// Workspace target — error path via responseError. This is the real coverage
// of clear(): data-loading is set first, then an error clears it. Breaking
// clear() would leave data-loading on workspace and this assertion would fail.
dispatch("htmx:beforeRequest", workspace, { target: workspace });
pass(workspace.hasAttribute("data-loading"), "workspace should have data-loading before error");
dispatch("htmx:responseError", workspace, { target: workspace });
pass(!workspace.hasAttribute("data-loading"), "workspace data-loading cleared after htmx:responseError");

if (failures.length === 0) {
  console.log("PASS: skeleton data-loading toggle");
  process.exit(0);
} else {
  for (const f of failures) console.log(" " + f);
  process.exit(1);
}

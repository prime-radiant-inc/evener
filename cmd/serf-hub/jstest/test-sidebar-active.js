// Verify that sidebar.js listens for htmx:afterSwap on #workspace and
// applies [data-active] to the .sb-row whose href matches the URL the
// swap was triggered for. Also verifies that the marker clears from all
// other rows.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SIDEBAR_JS = "../assets/sidebar.js";
const sidebarSrc = fs.readFileSync(SIDEBAR_JS, "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body class="app">
  <aside id="sidebar">
    <nav class="sidebar">
      <a class="sb-row" data-state="awaiting" href="/s/01ABC"
         hx-get="/_partials/s/01ABC/workspace" hx-target="#workspace"
         hx-push-url="/s/01ABC">
        <div class="dot-col"></div>
        <div class="text-col"><div class="title">A</div></div>
      </a>
      <a class="sb-row" data-state="active" href="/s/01DEF"
         hx-get="/_partials/s/01DEF/workspace" hx-target="#workspace"
         hx-push-url="/s/01DEF">
        <div class="dot-col"></div>
        <div class="text-col"><div class="title">B</div></div>
      </a>
      <a class="sb-row" data-state="idle" href="/s/01GHI"
         hx-get="/_partials/s/01GHI/workspace" hx-target="#workspace"
         hx-push-url="/s/01GHI">
        <div class="dot-col"></div>
        <div class="text-col"><div class="title">C</div></div>
      </a>
    </nav>
  </aside>
  <main id="workspace"></main>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });

const { window } = dom;
window.eval(sidebarSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const wait = () => new Promise(r => setTimeout(r, 30));

(async () => {
  await wait();

  const doc = window.document;
  const workspace = doc.getElementById("workspace");
  const rowA = doc.querySelector('.sb-row[href="/s/01ABC"]');
  const rowB = doc.querySelector('.sb-row[href="/s/01DEF"]');
  const rowC = doc.querySelector('.sb-row[href="/s/01GHI"]');

  // 1. Simulate htmx swapping in /s/01DEF — pushState first so location
  //    reflects the new URL, then fire htmx:afterSwap on #workspace.
  window.history.pushState({}, "", "/s/01DEF");
  const evt = new window.CustomEvent("htmx:afterSwap", { bubbles: true });
  workspace.dispatchEvent(evt);

  pass(rowB.hasAttribute("data-active"), "rowB should be marked active after swap to /s/01DEF");
  pass(!rowA.hasAttribute("data-active"), "rowA should NOT be active");
  pass(!rowC.hasAttribute("data-active"), "rowC should NOT be active");

  // 2. Swap to /s/01ABC — marker moves, prior marker clears.
  window.history.pushState({}, "", "/s/01ABC");
  workspace.dispatchEvent(new window.CustomEvent("htmx:afterSwap", { bubbles: true }));
  pass(rowA.hasAttribute("data-active"), "rowA should be marked active after swap to /s/01ABC");
  pass(!rowB.hasAttribute("data-active"), "rowB marker should clear");
  pass(!rowC.hasAttribute("data-active"), "rowC should remain unmarked");

  // 3. Swap to a non-session URL like /new — all rows should clear.
  window.history.pushState({}, "", "/new");
  workspace.dispatchEvent(new window.CustomEvent("htmx:afterSwap", { bubbles: true }));
  pass(!rowA.hasAttribute("data-active"), "no row should be active after /new swap");
  pass(!rowB.hasAttribute("data-active"), "no row should be active after /new swap");
  pass(!rowC.hasAttribute("data-active"), "no row should be active after /new swap");

  if (failures.length === 0) {
    console.log("PASS: sidebar data-active wiring on htmx:afterSwap");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();

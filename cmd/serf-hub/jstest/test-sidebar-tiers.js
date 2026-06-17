// Verify the tiered sidebar's two interactive behaviors:
//   1. Active-tier projects (data-default-expanded) start expanded and stay
//      expanded across re-init, while still honoring an explicit user collapse.
//   2. The "+N subagents" fold toggle reveals/hides the overflow subagent rows
//      and updates its own label + aria-expanded.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SIDEBAR_JS = "../assets/sidebar.js";
const sidebarSrc = fs.readFileSync(SIDEBAR_JS, "utf8");

function buildDom() {
  return new JSDOM(`<!DOCTYPE html><html><body>
    <nav class="sidebar">
      <section class="sidebar-tier" data-tier="active">
        <header class="sidebar-section-header tier-header"><span>active</span></header>
        <section class="sidebar-section project-section" data-project-key="alpha" data-default-expanded="true">
          <header class="project-header">
            <button class="project-chevron" aria-expanded="true">▾</button>
            <span class="project-name">alpha</span>
          </header>
          <div class="project-children">
            <a class="sb-row" href="/s/01P">parent</a>
            <a class="sb-row sub subagent-row" href="/s/01S1"><span class="subagent-glyph">✓</span><span class="subagent-title">s1</span></a>
            <a class="sb-row sub subagent-row" href="/s/01S2"><span class="subagent-glyph">✓</span><span class="subagent-title">s2</span></a>
            <a class="sb-row sub subagent-row" href="/s/01S3"><span class="subagent-glyph">✓</span><span class="subagent-title">s3</span></a>
            <a class="sb-row sub subagent-row subagent-overflow" href="/s/01S4"><span class="subagent-glyph">✓</span><span class="subagent-title">s4</span></a>
            <a class="sb-row sub subagent-row subagent-overflow" href="/s/01S5"><span class="subagent-glyph">✓</span><span class="subagent-title">s5</span></a>
            <button class="subagent-toggle" aria-expanded="false">+2 subagents</button>
          </div>
        </section>
      </section>
      <section class="sidebar-tier" data-tier="recent">
        <header class="sidebar-section-header tier-header"><span>recent</span></header>
        <section class="sidebar-section project-section collapsed" data-project-key="bravo">
          <header class="project-header">
            <button class="project-chevron" aria-expanded="false">▸</button>
            <span class="project-name">bravo</span>
          </header>
          <div class="project-children"><a class="sb-row" href="/s/01B">b</a></div>
        </section>
      </section>
    </nav>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
}

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const wait = () => new Promise(r => setTimeout(r, 30));

(async () => {
  // --- Round 1: active-tier project defaults expanded; recent stays collapsed.
  let dom = buildDom();
  let { window } = dom;
  window.eval(sidebarSrc);
  await wait();

  let alpha = window.document.querySelector('[data-project-key="alpha"]');
  let bravo = window.document.querySelector('[data-project-key="bravo"]');
  pass(!alpha.classList.contains("collapsed"), "active-tier project should default expanded");
  pass(bravo.classList.contains("collapsed"), "recent-tier project should default collapsed");

  // --- Round 2: explicit collapse of the active project persists across re-init.
  let chevron = alpha.querySelector(".project-chevron");
  chevron.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(alpha.classList.contains("collapsed"), "active project collapses on click");

  // Re-init with the same storage — explicit collapse should win over the
  // default-expanded hint.
  const storage = window.localStorage;
  const collapsedVal = storage.getItem("serf-hub.sidebar.expanded.alpha");
  dom = buildDom();
  window = dom.window;
  if (collapsedVal !== null) window.localStorage.setItem("serf-hub.sidebar.expanded.alpha", collapsedVal);
  else window.localStorage.setItem("serf-hub.sidebar.expanded.alpha", "false");
  window.eval(sidebarSrc);
  await wait();
  alpha = window.document.querySelector('[data-project-key="alpha"]');
  pass(alpha.classList.contains("collapsed"), "explicit collapse of active project should persist across re-init");

  // --- Round 3: "+N subagents" toggle reveals overflow rows.
  dom = buildDom();
  window = dom.window;
  window.eval(sidebarSrc);
  await wait();

  const children = window.document.querySelector('[data-project-key="alpha"] .project-children');
  const toggle = children.querySelector(".subagent-toggle");
  pass(toggle !== null, "subagent toggle should exist");
  pass(!children.hasAttribute("data-subagents-expanded"), "overflow hidden initially");

  toggle.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(children.hasAttribute("data-subagents-expanded"), "overflow revealed after toggle click");
  pass(toggle.getAttribute("aria-expanded") === "true", "toggle aria-expanded flips to true");

  toggle.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(!children.hasAttribute("data-subagents-expanded"), "overflow hidden again after second click");
  pass(toggle.getAttribute("aria-expanded") === "false", "toggle aria-expanded flips back to false");
  pass(/\+2 subagents/.test(toggle.textContent), "collapsed toggle label restores +N subagents");

  if (failures.length === 0) {
    console.log("PASS: sidebar tiers — default-expand + subagent fold");
    process.exit(0);
  } else {
    for (const f of failures) console.log(" " + f);
    process.exit(1);
  }
})();

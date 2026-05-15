// Verify that project sections default collapsed, clicking a project chevron
// expands the project's children, persists state to localStorage, flips the
// chevron glyph, and restores from storage on re-init.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SIDEBAR_JS = "../assets/sidebar.js";
const sidebarSrc = fs.readFileSync(SIDEBAR_JS, "utf8");

function buildDom() {
  return new JSDOM(`<!DOCTYPE html><html><body>
    <nav class="sidebar">
      <section class="sidebar-section project-section" data-project-key="serf-hub">
        <header class="project-header">
          <span class="project-chevron">▾</span>
          <span class="project-folder">📁</span>
          <span class="project-name">serf-hub</span>
          <span class="row-meta project-count">3</span>
          <span class="project-rollup-dot" data-state="awaiting"></span>
        </header>
        <div class="project-children">
          <a class="session-row">a</a>
          <a class="session-row">b</a>
          <a class="session-row">c</a>
        </div>
      </section>
      <section class="sidebar-section project-section" data-project-key="other-proj">
        <header class="project-header">
          <span class="project-chevron">▾</span>
          <span class="project-folder">📁</span>
          <span class="project-name">other-proj</span>
          <span class="row-meta project-count">1</span>
          <span class="project-rollup-dot" data-state=""></span>
        </header>
        <div class="project-children">
          <a class="session-row">x</a>
        </div>
      </section>
    </nav>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
}

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const PERSISTED_KEY = "serf-hub.sidebar.expanded.serf-hub";

// JSDOM fires DOMContentLoaded asynchronously after script eval. Each round
// awaits a microtask flush before asserting.
const wait = () => new Promise(resolve => setTimeout(resolve, 30));

(async () => {
  // --- Round 1: fresh load, click chevron, verify expand + storage write.
  let dom = buildDom();
  let { window } = dom;
  window.eval(sidebarSrc);
  await wait();

  let section = window.document.querySelector('[data-project-key="serf-hub"]');
  let chevron = section.querySelector(".project-chevron");

  pass(section.classList.contains("collapsed"), "initially collapsed");
  pass(chevron.textContent === "▸", "initial glyph should be ▸, got " + JSON.stringify(chevron.textContent));

  chevron.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));

  pass(!section.classList.contains("collapsed"), "after click, section should be expanded");
  pass(chevron.textContent === "▾", "after click glyph should be ▾, got " + JSON.stringify(chevron.textContent));

  const stored = window.localStorage.getItem(PERSISTED_KEY);
  pass(stored === "true", `localStorage should be "true", got ${JSON.stringify(stored)}`);

  // --- Round 2: rebuild DOM, seed localStorage, re-run script, verify restore.
  dom = buildDom();
  window = dom.window;
  window.localStorage.setItem(PERSISTED_KEY, "true");
  window.eval(sidebarSrc);
  await wait();

  section = window.document.querySelector('[data-project-key="serf-hub"]');
  chevron = section.querySelector(".project-chevron");
  pass(!section.classList.contains("collapsed"), "restored: section should be expanded from localStorage");
  pass(chevron.textContent === "▾", "restored glyph should be ▾, got " + JSON.stringify(chevron.textContent));

  const other = window.document.querySelector('[data-project-key="other-proj"]');
  pass(other.classList.contains("collapsed"), "untouched project should default collapsed");
  pass(other.querySelector(".project-chevron").textContent === "▸", "untouched chevron stays ▸");

  // --- Round 3: click again to collapse, localStorage cleared.
  chevron.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(section.classList.contains("collapsed"), "after second click, collapsed again");
  pass(chevron.textContent === "▸", "collapsed glyph should be ▸, got " + JSON.stringify(chevron.textContent));
  pass(window.localStorage.getItem(PERSISTED_KEY) === null, "localStorage entry should be cleared on collapse");

  if (failures.length === 0) {
    console.log("PASS: sidebar collapse — toggle, persist, restore");
    process.exit(0);
  } else {
    for (const f of failures) console.log(" " + f);
    process.exit(1);
  }
})();

// Verify the repeated-title cluster (mockup #10/#C) expand/collapse:
// clicking the cluster header toggles data-cluster-expanded on the
// .session-cluster (CSS reveals the member runs), flips the chevron glyph,
// and updates aria-expanded. The member rows must stay real session links.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SIDEBAR_JS = "../assets/sidebar.js";
const sidebarSrc = fs.readFileSync(SIDEBAR_JS, "utf8");

function buildDom() {
  return new JSDOM(`<!DOCTYPE html><html><body>
    <nav class="sidebar">
      <section class="sidebar-tier" data-tier="recent">
        <header class="sidebar-section-header tier-header"><span>recent</span></header>
        <section class="sidebar-section project-section" data-project-key="serf-docs" data-default-expanded="true">
          <header class="project-header">
            <button class="project-chevron" aria-expanded="true">▾</button>
            <span class="project-name">serf-docs</span>
          </header>
          <div class="project-children">
            <div class="session-cluster" data-cluster-key="serf-docs:describe this image">
              <button class="sb-row cluster-header" aria-expanded="false">
                <div class="dot-col"><span class="cluster-chevron">▸</span></div>
                <div class="text-col"><div class="title">describe this image<span class="cluster-count">×3</span></div></div>
              </button>
              <div class="cluster-members">
                <a class="sb-row cluster-member" href="/s/01A"><div class="text-col"><div class="title">run</div></div></a>
                <a class="sb-row cluster-member" href="/s/01B"><div class="text-col"><div class="title">run</div></div></a>
                <a class="sb-row cluster-member" href="/s/01C"><div class="text-col"><div class="title">run</div></div></a>
              </div>
            </div>
          </div>
        </section>
      </section>
    </nav>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
}

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const wait = () => new Promise(r => setTimeout(r, 30));

(async () => {
  const dom = buildDom();
  const { window } = dom;
  window.eval(sidebarSrc);
  await wait();

  const cluster = window.document.querySelector(".session-cluster");
  const header = cluster.querySelector(".cluster-header");
  const chevron = cluster.querySelector(".cluster-chevron");

  // Member runs are real session links, present in the DOM (CSS hides them).
  pass(cluster.querySelectorAll(".cluster-member").length === 3, "cluster holds its 3 member runs");
  pass(!cluster.hasAttribute("data-cluster-expanded"), "cluster starts collapsed");

  // Click the header → expands.
  header.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(cluster.hasAttribute("data-cluster-expanded"), "cluster expands on header click");
  pass(header.getAttribute("aria-expanded") === "true", "header aria-expanded flips to true");
  pass(chevron.textContent === "▾", "chevron flips to ▾ when expanded");

  // Click again → collapses.
  header.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  pass(!cluster.hasAttribute("data-cluster-expanded"), "cluster collapses on second click");
  pass(header.getAttribute("aria-expanded") === "false", "header aria-expanded flips back to false");
  pass(chevron.textContent === "▸", "chevron flips back to ▸ when collapsed");

  if (failures.length === 0) {
    console.log("PASS: sidebar repeated-title cluster expand/collapse");
    process.exit(0);
  } else {
    for (const f of failures) console.log(" " + f);
    process.exit(1);
  }
})();

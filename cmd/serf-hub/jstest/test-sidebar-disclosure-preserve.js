// Verify that sidebar.js preserves ephemeral disclosure state across a full
// sidebar swap. The sidebar re-renders by replacing #sidebar's innerHTML
// (hx-swap="innerHTML") on every archive action and on every notification-
// driven refresh. Native <details> and JS-toggled folds carry no server-side
// "open" marker, so without a snapshot/restore pass they snap shut on every
// swap. sidebar.js snapshots the open disclosures on htmx:beforeSwap and
// reapplies them on htmx:afterSwap.
//
// Covered disclosures:
//   1. Per-project Archived <details class="session-tier archived">.
//   2. The global Archived-projects <details>.
//   3. Repeated-title cluster fold (data-cluster-expanded).
//   4. "Completed (N)" subagent fold (data-subagents-expanded).
const fs = require("fs");
const { JSDOM } = require("jsdom");

const sidebarSrc = fs.readFileSync("../assets/sidebar.js", "utf8");

// markup builds the inner HTML of #sidebar. open=true opens every disclosure;
// open=false leaves them all collapsed (the fresh-from-server default).
function markup(open) {
  const det = open ? " open" : "";
  const subAttr = open ? " data-subagents-expanded" : "";
  const cluAttr = open ? " data-cluster-expanded" : "";
  const cluAria = open ? "true" : "false";
  const cluGlyph = open ? "▾" : "▸";
  const subLabel = open ? "− hide" : "Completed (2)";
  const subAria = open ? "true" : "false";
  return `
    <section class="sidebar-section project-section" data-project-key="myproject" data-default-expanded="true">
      <header class="project-header"><span class="project-name">myproject</span></header>
      <div class="project-children"${subAttr}>
        <div class="session-cluster" data-cluster-key="myproject:dup"${cluAttr}>
          <button type="button" class="sb-row cluster-header" aria-expanded="${cluAria}">
            <span class="cluster-chevron">${cluGlyph}</span>
          </button>
          <div class="cluster-members"><a class="sb-row cluster-member" href="/s/01CLU">run</a></div>
        </div>
        <button type="button" class="subagent-toggle" aria-expanded="${subAria}">${subLabel}</button>
        <div class="subagent-overflow">done worker</div>
        <details class="session-tier archived" data-tier="archived"${det}>
          <summary class="session-tier-label">Archived <span class="count">1</span></summary>
          <div class="sb-row-wrap"><a class="sb-row" href="/s/01OLD">old session</a></div>
        </details>
      </div>
    </section>
    <section class="sidebar-tier archived-projects" data-tier="archived-projects">
      <details${det}>
        <summary class="sidebar-section-header tier-header">Archived projects <span class="count">1</span></summary>
        <section class="sidebar-section project-section" data-project-key="oldproject">
          <header class="project-header"><span class="project-name">oldproject</span></header>
        </section>
      </details>
    </section>`;
}

const dom = new JSDOM(
  `<!DOCTYPE html><html><body><aside id="sidebar">${markup(true)}</aside></body></html>`,
  { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" }
);
const { window } = dom;
window.htmx = { trigger: function () {}, process: function () {} };
window.fetch = function () { return Promise.resolve({ ok: true, text: function () { return Promise.resolve(""); } }); };
window.eval(sidebarSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const sidebar = window.document.getElementById("sidebar");

(async () => {
  // Snapshot the open state, then swap in a fresh (all-collapsed) render — the
  // exact sequence htmx runs on a sidebar refresh.
  sidebar.dispatchEvent(new window.CustomEvent("htmx:beforeSwap", { bubbles: true }));
  sidebar.innerHTML = markup(false);
  sidebar.dispatchEvent(new window.CustomEvent("htmx:afterSwap", { bubbles: true }));
  await new Promise(r => setTimeout(r, 10));

  const archivedTier = sidebar.querySelector("details.session-tier.archived");
  pass(archivedTier && archivedTier.open, "per-project Archived details should reopen after swap");

  const archivedProjects = sidebar.querySelector(".archived-projects details");
  pass(archivedProjects && archivedProjects.open, "Archived-projects details should reopen after swap");

  const cluster = sidebar.querySelector(".session-cluster");
  pass(cluster && cluster.hasAttribute("data-cluster-expanded"), "expanded cluster should reopen after swap");
  const clusterHeader = sidebar.querySelector(".cluster-header");
  pass(clusterHeader && clusterHeader.getAttribute("aria-expanded") === "true", "reopened cluster header should be aria-expanded");

  const children = sidebar.querySelector(".project-children");
  pass(children && children.hasAttribute("data-subagents-expanded"), "expanded subagent fold should reopen after swap");
  const toggle = sidebar.querySelector(".subagent-toggle");
  pass(toggle && toggle.getAttribute("aria-expanded") === "true", "reopened subagent toggle should be aria-expanded");

  // A disclosure the user left CLOSED must stay closed across a swap — restore
  // only reopens what was open, it never force-opens.
  {
    const dom2 = new JSDOM(
      `<!DOCTYPE html><html><body><aside id="sidebar">${markup(false)}</aside></body></html>`,
      { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" }
    );
    const w2 = dom2.window;
    w2.htmx = { trigger: function () {}, process: function () {} };
    w2.fetch = function () { return Promise.resolve({ ok: true, text: function () { return Promise.resolve(""); } }); };
    w2.eval(sidebarSrc);
    const sb2 = w2.document.getElementById("sidebar");
    sb2.dispatchEvent(new w2.CustomEvent("htmx:beforeSwap", { bubbles: true }));
    sb2.innerHTML = markup(false);
    sb2.dispatchEvent(new w2.CustomEvent("htmx:afterSwap", { bubbles: true }));
    await new Promise(r => setTimeout(r, 10));
    const at2 = sb2.querySelector("details.session-tier.archived");
    pass(at2 && !at2.open, "a collapsed Archived details must stay collapsed across a swap");
  }

  if (failures.length === 0) {
    console.log("PASS: sidebar disclosure state survives a full sidebar swap");
    process.exit(0);
  } else {
    for (const f of failures) console.log(" " + f);
    process.exit(1);
  }
})();

// Project rollup badges (R4): the project-header's dead .project-rollup span
// (buildProjectHeader) is wired to the server-computed magnitude counts —
// "⟳N · ◆M" (mockup #10 rec A) — via a shared setProjectRollup(el, p) used by
// both buildProjectHeader and patchProjectHeader, so the reconcile patch path
// updates counts/tint in place instead of rebuilding the header node.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

function boot() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(src);
  return w;
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
// project builds a fixture TreeProject carrying the T4 rollup wire fields
// (rollup_live/rollup_attn/rollup_state); default_expanded avoids the
// lazy-children fetch path since these tests only care about the header.
function project(rollup) {
  return Object.assign({
    key: "p1", name: "proj", working_dir: "/w/p1", default_expanded: true, sessions: [],
  }, rollup);
}
function treeWithProject(rollup) {
  var t = emptyTree();
  t.projects = [project(rollup)];
  return t;
}
function headerRollup(w) {
  var header = w.document.querySelector('[data-row-id="header:p1"]');
  if (!header) throw new Error("project header must render");
  var rollup = header.querySelector(".project-rollup");
  if (!rollup) throw new Error("project header must carry a .project-rollup element");
  return rollup;
}

// 1. attn-only: needs-you badge only, not the live badge.
{
  const w = boot();
  w.SerfSidebar.renderTree(treeWithProject({ rollup_live: 0, rollup_attn: 2, rollup_state: "awaiting" }));
  const rollup = headerRollup(w);
  const attn = rollup.querySelector(".rollup-badge.rollup-attn");
  if (!attn) throw new Error("attn-only project must show the needs-you badge");
  if (attn.textContent !== "◆2") throw new Error('expected needs-you badge text "◆2", got ' + JSON.stringify(attn.textContent));
  if (rollup.querySelector(".rollup-badge.rollup-live")) throw new Error("attn-only project must NOT show the live badge");
  if (rollup.getAttribute("data-state") !== "awaiting") throw new Error('data-state must reflect rollup_state, got ' + JSON.stringify(rollup.getAttribute("data-state")));
  console.log("ok attn-only shows the needs-you badge only");
}

// 2. live-only: working badge only, not the needs-you badge.
{
  const w = boot();
  w.SerfSidebar.renderTree(treeWithProject({ rollup_live: 3, rollup_attn: 0, rollup_state: "active" }));
  const rollup = headerRollup(w);
  const live = rollup.querySelector(".rollup-badge.rollup-live");
  if (!live) throw new Error("live-only project must show the working badge");
  if (live.textContent !== "⟳3") throw new Error('expected working badge text "⟳3", got ' + JSON.stringify(live.textContent));
  if (rollup.querySelector(".rollup-badge.rollup-attn")) throw new Error("live-only project must NOT show the needs-you badge");
  console.log("ok live-only shows the working badge only");
}

// 3. both: both segments render in the precedent's order (live then attn,
// separated by "·"), and data-state reflects the attention-ranked winner
// (rollup_state is server-computed via rollupRank — awaiting outranks active).
{
  const w = boot();
  w.SerfSidebar.renderTree(treeWithProject({ rollup_live: 2, rollup_attn: 1, rollup_state: "awaiting" }));
  const rollup = headerRollup(w);
  const badges = rollup.querySelectorAll(".rollup-badge");
  if (badges.length !== 2) throw new Error("expected exactly 2 badges (never a third), got " + badges.length);
  if (!badges[0].classList.contains("rollup-live") || badges[0].textContent !== "⟳2") {
    throw new Error("expected the live badge (⟳2) first, got " + JSON.stringify(badges[0].outerHTML));
  }
  if (!badges[1].classList.contains("rollup-attn") || badges[1].textContent !== "◆1") {
    throw new Error("expected the needs-you badge (◆1) second, got " + JSON.stringify(badges[1].outerHTML));
  }
  if (!rollup.querySelector(".rollup-sep")) throw new Error("both-nonzero rollup must render a separator between segments");
  if (rollup.getAttribute("data-state") !== "awaiting") {
    throw new Error('data-state must reflect the attention-ranked rollup_state ("awaiting" outranks "active"), got ' + JSON.stringify(rollup.getAttribute("data-state")));
  }
  console.log("ok both segments render in precedent order with the attention-ranked data-state");
}

// 4. both-zero: the rollup stays genuinely empty — no stray separator, no
// third fallback category (e.g. no dot resurrected for an idle-only project).
{
  const w = boot();
  w.SerfSidebar.renderTree(treeWithProject({ rollup_live: 0, rollup_attn: 0, rollup_state: "" }));
  const rollup = headerRollup(w);
  if (rollup.children.length !== 0) throw new Error("both-zero rollup must be empty, got " + rollup.innerHTML);
  console.log("ok both-zero rollup is empty with no stray separator");
}

// 5. patch path: a re-render with changed counts updates the SAME header DOM
// node in place (keyed reconcile), with the badges reflecting the new counts.
{
  const w = boot();
  const tree = treeWithProject({ rollup_live: 1, rollup_attn: 0, rollup_state: "active" });
  w.SerfSidebar.renderTree(tree);
  const header = w.document.querySelector('[data-row-id="header:p1"]');
  header.__probe = true;
  tree.projects[0].rollup_live = 5;
  tree.projects[0].rollup_attn = 2;
  tree.projects[0].rollup_state = "awaiting";
  w.SerfSidebar.renderTree(tree);
  const header2 = w.document.querySelector('[data-row-id="header:p1"]');
  if (!header2 || header2.__probe !== true) throw new Error("patch path must keep the SAME project-header DOM node");
  const rollup2 = header2.querySelector(".project-rollup");
  const live2 = rollup2.querySelector(".rollup-badge.rollup-live");
  const attn2 = rollup2.querySelector(".rollup-badge.rollup-attn");
  if (!live2 || live2.textContent !== "⟳5") throw new Error("patched live badge must update to ⟳5, got " + (live2 && live2.textContent));
  if (!attn2 || attn2.textContent !== "◆2") throw new Error("patched attn badge must update to ◆2, got " + (attn2 && attn2.textContent));
  if (rollup2.getAttribute("data-state") !== "awaiting") throw new Error("patched data-state must update to awaiting");
  console.log("ok patch path updates rollup counts/tint on the same DOM node");
}

console.log("ok sidebar rollup badges: needs-you outranks active, two segments only");
process.exit(0); // sidebar.js's 60s idle-resync interval keeps the event loop alive otherwise

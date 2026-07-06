// Cluster folds: a T5 synthetic `kind:"cluster"` node (same-titled repeated
// runs folded into one row server-side — hubcore.clusterRepeatedTitles, e.g.
// "describe this image x5") renders as a non-navigable fold row — title +
// member count, collapsed by default — whose `children` become plain session
// rows once expanded. Also covers the T22 auto-reveal through a deeper
// section + project + cluster chain.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

function boot(url) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: url || "http://localhost/",
  });
  const w = dom.window;
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(iconsSrc);
  w.eval(src);
  return w;
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
function treeWithCluster() {
  var t = emptyTree();
  t.projects = [{
    key: "p1", name: "proj", working_dir: "/w/p1", default_expanded: true,
    sessions: [{
      row_id: "project:p1:cluster:ab12", ref: "cluster:ab12", session_id: "",
      title: "describe this image", state: "ended", kind: "cluster", cluster_count: 3,
      children: [
        { row_id: "project:p1:local:01M1", ref: "local:01M1", session_id: "01M1", title: "describe this image", state: "ended", kind: "session", tier: "current" },
        { row_id: "project:p1:local:01M2", ref: "local:01M2", session_id: "01M2", title: "describe this image", state: "idle", kind: "session", tier: "current" },
        { row_id: "project:p1:local:01M3", ref: "local:01M3", session_id: "01M3", title: "describe this image", state: "ended", kind: "session", tier: "current" },
      ],
    }],
  }];
  return t;
}

const w = boot();
const tree = treeWithCluster();

// 1. Collapsed by default: the fold row renders (no href/hx-*), title + count
// visible; members absent.
w.SerfSidebar.renderTree(tree);
const fold = w.document.querySelector('[data-row-id="project:p1:cluster:ab12"]');
if (!fold) throw new Error("a kind:cluster node must render a fold row");
if (fold.hasAttribute("href")) throw new Error("a cluster fold row must not carry an href — it has no real session");
if (fold.hasAttribute("hx-get")) throw new Error("a cluster fold row must not carry hx-* attributes");
if (fold.tagName !== "BUTTON") throw new Error("a cluster fold row must be a button-like element, got " + fold.tagName);
if (fold.textContent.indexOf("describe this image") === -1) throw new Error("fold row must show the cluster's title");
if (fold.textContent.indexOf("3") === -1) throw new Error('fold row must show the member count, got ' + JSON.stringify(fold.textContent));
if (fold.getAttribute("aria-expanded") !== "false") {
  throw new Error('collapsed cluster fold must have aria-expanded="false", got ' + JSON.stringify(fold.getAttribute("aria-expanded")));
}
if (w.document.querySelector('[data-row-id="project:p1:local:01M1"]')) {
  throw new Error("cluster members must NOT render while the fold is collapsed");
}

// 2. Expand -> members appear as plain session rows (NOT the subagent-row
// indent treatment — members are normal rows per spec).
fold.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
const fold2 = w.document.querySelector('[data-row-id="project:p1:cluster:ab12"]');
if (fold2.getAttribute("aria-expanded") !== "true") {
  throw new Error('expanded cluster fold must have aria-expanded="true", got ' + JSON.stringify(fold2.getAttribute("aria-expanded")));
}
const m1 = w.document.querySelector('[data-row-id="project:p1:local:01M1"]');
const m2 = w.document.querySelector('[data-row-id="project:p1:local:01M2"]');
const m3 = w.document.querySelector('[data-row-id="project:p1:local:01M3"]');
if (!m1 || !m2 || !m3) throw new Error("expanding the fold must render all 3 members as keyed rows");
if (m1.classList.contains("subagent-row")) throw new Error("cluster members are normal session rows, not indented subagent rows");
if (m1.getAttribute("href") !== "/s/01M1") throw new Error("cluster members must be normal navigable session rows");
if (w.localStorage.getItem("serf-hub.sidebar.expanded.project:p1:cluster:ab12") !== "true") {
  throw new Error("cluster expansion must persist under the cluster's OWN row_id");
}

// 3. Identity probe: a member row keeps DOM node identity across a re-render.
m1.__probe = true;
w.SerfSidebar.renderTree(tree);
const m1b = w.document.querySelector('[data-row-id="project:p1:local:01M1"]');
if (!m1b || m1b.__probe !== true) throw new Error("a cluster member row must keep DOM node identity across re-renders");

// 4. Collapse again -> members removed, aria flips back to false.
const fold3 = w.document.querySelector('[data-row-id="project:p1:cluster:ab12"]');
fold3.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
if (w.document.querySelector('[data-row-id="project:p1:local:01M1"]')) {
  throw new Error("collapsing the fold again must remove its member rows");
}
if (w.document.querySelector('[data-row-id="project:p1:cluster:ab12"]').getAttribute("aria-expanded") !== "false") {
  throw new Error("re-collapsed fold must flip aria-expanded back to false");
}

console.log("ok sidebar clusters: fold row (no href, button) + title/count + expand/collapse + identity");

// ---------------------------------------------------------------------------
// Auto-reveal through a deeper chain: the active session is a cluster MEMBER,
// nested inside a collapsed cluster, nested inside a collapsed project, inside
// the collapsed Archived SECTION — three levels, all must expand + persist.
// ---------------------------------------------------------------------------
function treeWithDeepClusterMember() {
  var t = emptyTree();
  t.archived_projects = [{
    key: "apk-deep", name: "arch-deep", working_dir: "/a/deep", default_expanded: false,
    sessions: [{
      row_id: "project:apk-deep:cluster:cc99", ref: "cluster:cc99", session_id: "",
      title: "flaky test run", state: "ended", kind: "cluster", cluster_count: 3,
      children: [
        { row_id: "project:apk-deep:local:01DEEP1", ref: "local:01DEEP1", session_id: "01DEEP1", title: "flaky test run", state: "ended", kind: "session", tier: "archived" },
        { row_id: "project:apk-deep:local:01DEEP2", ref: "local:01DEEP2", session_id: "01DEEP2", title: "flaky test run", state: "ended", kind: "session", tier: "archived" },
        { row_id: "project:apk-deep:local:01DEEP3", ref: "local:01DEEP3", session_id: "01DEEP3", title: "flaky test run", state: "ended", kind: "session", tier: "archived" },
      ],
    }],
  }];
  return t;
}

const w2 = boot("http://localhost/s/01DEEP2"); // a middle member — proves it's a real search, not "first wins"
const deepTree = treeWithDeepClusterMember();
w2.SerfSidebar.renderTree(deepTree);
const revealed = w2.document.querySelector('[data-row-id="project:apk-deep:local:01DEEP2"]');
if (!revealed) throw new Error("auto-reveal must expand the section + project + cluster chain so the deep member renders");
if (!revealed.hasAttribute("data-active")) throw new Error("the auto-revealed cluster member must be marked data-active");
if (w2.localStorage.getItem("serf-hub.sidebar.expanded.section:archived") !== "true") {
  throw new Error("auto-reveal must persist the Archived section's expansion key");
}
if (w2.localStorage.getItem("serf-hub.sidebar.expanded.apk-deep") !== "true") {
  throw new Error("auto-reveal must persist the enclosing project's expansion key");
}
if (w2.localStorage.getItem("serf-hub.sidebar.expanded.project:apk-deep:cluster:cc99") !== "true") {
  throw new Error("auto-reveal must persist the cluster's own row_id as its expansion key");
}

console.log("ok sidebar auto-reveal: deep-link through collapsed section + project + cluster chain");
process.exit(0); // sidebar.js's 60s idle-resync interval keeps the event loop alive otherwise

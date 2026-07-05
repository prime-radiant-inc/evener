// Subagent children disclosure: a session node whose wire data carries
// `children` (subagent threads run under it — hubapi.TreeNode.Children,
// T6-capped at 50 server-side) gains a collapsed-by-default toggle on its own
// row; expanding reveals the children as keyed session rows (buildRow +
// .subagent-row indent), reusing the SAME reconcile + pending-overlay
// machinery as any other row. Also covers the T22 auto-reveal: a deep-link
// whose target is nested behind a collapsed disclosure/project must still
// end up visible + data-active after the initial render.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

function boot(url) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: url || "http://localhost/",
  });
  const w = dom.window;
  const posts = [];
  w.fetch = (u, opts) => {
    if (opts && opts.method === "POST") { posts.push({ url: u, body: JSON.parse(opts.body) }); return Promise.resolve({ ok: true, json: () => Promise.resolve({ ok: true }) }); }
    return Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  };
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(src);
  return { w, posts };
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
function treeWithChildren() {
  var t = emptyTree();
  t.projects = [{
    key: "p1", name: "proj", working_dir: "/w/p1", default_expanded: true,
    sessions: [{
      row_id: "project:p1:local:01PARENT", ref: "local:01PARENT", session_id: "01PARENT",
      title: "parent task", state: "active", kind: "session", tier: "current",
      children: [
        { row_id: "project:p1:local:01CHILD1", ref: "local:01CHILD1", session_id: "01CHILD1", title: "child one", state: "ended", kind: "session", tier: "current" },
        { row_id: "project:p1:local:01CHILD2", ref: "local:01CHILD2", session_id: "01CHILD2", title: "child two", state: "active", kind: "session", tier: "current" },
      ],
    }],
  }];
  return t;
}

const { w, posts } = boot();
const tree = treeWithChildren();

// 1. Collapsed by default: the parent row renders a disclosure toggle
// showing the child count; no child rows are in the DOM yet.
w.SerfSidebar.renderTree(tree);
const parentRow = w.document.querySelector('[data-row-id="project:p1:local:01PARENT"]');
if (!parentRow) throw new Error("parent row must render");
const toggle = parentRow.querySelector(".sb-children-toggle");
if (!toggle) throw new Error("a parent row with children must carry a disclosure toggle");
if (toggle.textContent.indexOf("2") === -1) {
  throw new Error('toggle must show the child count "2", got ' + JSON.stringify(toggle.textContent));
}
if (toggle.getAttribute("aria-expanded") !== "false") {
  throw new Error('collapsed children toggle must have aria-expanded="false", got ' + JSON.stringify(toggle.getAttribute("aria-expanded")));
}
if (w.document.querySelector('[data-row-id="project:p1:local:01CHILD1"]')) {
  throw new Error("child rows must NOT render while the parent's disclosure is collapsed");
}

// A row with no children must NOT carry the toggle at all (only the
// project's own session row, "parent task", has children in this fixture —
// nothing else to check here directly, but guard against a stray toggle on
// the project header itself).
const projHeader = w.document.querySelector('[data-row-id="header:p1"]');
if (projHeader && projHeader.querySelector(".sb-children-toggle")) {
  throw new Error("a project header must never carry a children disclosure toggle");
}

// 2. Click the toggle -> children appear as keyed rows; aria flips to true.
toggle.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
const toggle2 = w.document.querySelector('[data-row-id="project:p1:local:01PARENT"] .sb-children-toggle');
if (!toggle2) throw new Error("toggle must survive the re-render (reconcile identity)");
if (toggle2.getAttribute("aria-expanded") !== "true") {
  throw new Error('expanded children toggle must have aria-expanded="true", got ' + JSON.stringify(toggle2.getAttribute("aria-expanded")));
}
const child1 = w.document.querySelector('[data-row-id="project:p1:local:01CHILD1"]');
const child2 = w.document.querySelector('[data-row-id="project:p1:local:01CHILD2"]');
if (!child1 || !child2) throw new Error("expanding the parent's disclosure must render both children as keyed rows");
if (!child1.classList.contains("subagent-row")) throw new Error("a child row must carry the subagent-row indent class (style.css reuse)");
if (!child1.classList.contains("sb-row")) throw new Error("a child row must still be a normal .sb-row (keyed reconcile, hx-*, menu, etc.)");
if (child1.getAttribute("href") !== "/s/01CHILD1") throw new Error("a child row must be a normal navigable session row");

// 3. localStorage persistence under a "children:<row_id>" key that cannot
// collide with a project slug ("p1") or a "section:*"/cluster row_id.
if (w.localStorage.getItem("serf-hub.sidebar.expanded.children:project:p1:local:01PARENT") !== "true") {
  throw new Error("children disclosure must persist under a children:<row_id> localStorage key");
}

// 4. Identity probe: a child row keeps DOM node identity across a re-render
// (same reconcile machinery as every other row).
child1.__probe = true;
w.SerfSidebar.renderTree(tree);
const child1b = w.document.querySelector('[data-row-id="project:p1:local:01CHILD1"]');
if (!child1b || child1b.__probe !== true) throw new Error("a child row must keep DOM node identity across re-renders");

// 5. Collapse again -> children removed, aria flips back to false.
const toggle3 = w.document.querySelector('[data-row-id="project:p1:local:01PARENT"] .sb-children-toggle');
toggle3.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
if (w.document.querySelector('[data-row-id="project:p1:local:01CHILD1"]')) {
  throw new Error("collapsing the disclosure again must remove the child rows");
}
if (w.document.querySelector('[data-row-id="project:p1:local:01PARENT"] .sb-children-toggle').getAttribute("aria-expanded") !== "false") {
  throw new Error("re-collapsed toggle must flip aria-expanded back to false");
}
// Re-expand for the remaining assertions below (overlay recursion needs the
// child rendered so we can observe its data-favorite attribute).
w.document.querySelector('[data-row-id="project:p1:local:01PARENT"] .sb-children-toggle')
  .dispatchEvent(new w.MouseEvent("click", { bubbles: true }));

// 6. Pending overlay recursion: a favorite op on a CHILD ref must apply to
// the child's row even though it lives inside another row's children[]
// (forEachNode must recurse into .children, not just top-level lists).
w.SerfSidebar.favorite("local:01CHILD1", true);
const child1c = w.document.querySelector('[data-row-id="project:p1:local:01CHILD1"]');
if (!child1c.hasAttribute("data-favorite")) {
  throw new Error("a pending favorite op on a child ref must apply to the child's row (forEachNode must recurse into children)");
}
if (!posts.some((p) => p.url === "/api/favorite" && p.body.id === "01CHILD1" && p.body.favorited === true)) {
  throw new Error("favoriting a child must POST /api/favorite with the child's session id");
}

console.log("ok sidebar children: disclosure collapsed-by-default + count + expand/collapse + identity + overlay recursion");

// ---------------------------------------------------------------------------
// Auto-reveal (closes the T22 known-issue): a deep-link whose session is
// nested behind a collapsed PROJECT and a collapsed children disclosure must
// still be revealed + marked data-active after the initial render, with both
// expansion keys persisted to localStorage.
// ---------------------------------------------------------------------------
function treeWithDeepChild() {
  var t = emptyTree();
  t.projects = [{
    key: "p2", name: "deep-proj", working_dir: "/w/p2", default_expanded: false,
    sessions: [{
      row_id: "project:p2:local:01PARENT2", ref: "local:01PARENT2", session_id: "01PARENT2",
      title: "deep parent", state: "idle", kind: "session", tier: "current",
      children: [
        { row_id: "project:p2:local:01CHILDDEEP", ref: "local:01CHILDDEEP", session_id: "01CHILDDEEP", title: "deep child", state: "ended", kind: "session", tier: "current" },
      ],
    }],
  }];
  return t;
}

const { w: w2 } = boot("http://localhost/s/01CHILDDEEP");
const deepTree = treeWithDeepChild();
w2.SerfSidebar.renderTree(deepTree);
const revealed = w2.document.querySelector('[data-row-id="project:p2:local:01CHILDDEEP"]');
if (!revealed) throw new Error("auto-reveal must expand the project + children chain so the deep-linked child renders");
if (!revealed.hasAttribute("data-active")) throw new Error("the auto-revealed row must be marked data-active");
if (w2.localStorage.getItem("serf-hub.sidebar.expanded.p2") !== "true") {
  throw new Error("auto-reveal must persist the enclosing project's expansion key");
}
if (w2.localStorage.getItem("serf-hub.sidebar.expanded.children:project:p2:local:01PARENT2") !== "true") {
  throw new Error("auto-reveal must persist the parent's children disclosure key");
}

// Guard: a second render for the SAME pathname (e.g. a periodic resync) must
// not redo the reveal — no additional expansion-key writes. This proves the
// "attempt at most once per distinct pathname" guard is actually gating
// re-attempts, not merely happening not to loop.
let expandWrites = 0;
const realSetItem = w2.localStorage.setItem.bind(w2.localStorage);
w2.localStorage.setItem = function (k, v) {
  if (k.indexOf("serf-hub.sidebar.expanded.") === 0) expandWrites++;
  return realSetItem(k, v);
};
w2.SerfSidebar.renderTree(deepTree);
if (expandWrites !== 0) {
  throw new Error("a second render for the same pathname must not re-run the reveal (loop guard), got " + expandWrites + " new expansion writes");
}

console.log("ok sidebar auto-reveal: deep-link through collapsed project + children chain, loop-guarded");

// Guard: a pathname pointing at a nonexistent session must not hang or blow
// the call stack — the reveal search finds nothing and gives up cleanly.
const { w: w3 } = boot("http://localhost/s/01DOES-NOT-EXIST");
let threw = null;
try {
  w3.SerfSidebar.renderTree(treeWithDeepChild());
} catch (e) {
  threw = e;
}
if (threw) throw new Error("auto-reveal for a nonexistent session must not throw (guard missing?): " + threw);
if (w3.document.querySelector("#sidebar .sb-row[data-active]")) {
  throw new Error("a nonexistent session must not mark any row active");
}

console.log("ok sidebar auto-reveal: nonexistent session does not loop or throw");
process.exit(0); // sidebar.js's 60s idle-resync interval keeps the event loop alive otherwise

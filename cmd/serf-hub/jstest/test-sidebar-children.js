// Recursive subagent projection: current children are always visible directly
// beneath their parent; terminal children live behind that parent's independent
// inactive disclosure. This fixture exercises nested current + inactive states,
// keyed reconciliation, and resync persistence against the real sidebar.js.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");

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
  w.eval(iconsSrc);
  w.eval(src);
  return { w, posts };
}
function emptyTree() {
  return { needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [], attentionSummary: { needsYou: 0, error: 0, working: 0 } };
}
function node(id, title, state, children) {
  return { row_id: "project:p1:local:" + id, ref: "local:" + id, session_id: id, title, state, kind: "session", tier: "current", children: children || [] };
}
function treeWithChildren() {
  var t = emptyTree();
  var runningGrandchild = node("GRAND-IDLE", "idle grandchild", "idle");
  var endedGrandchild = node("GRAND-ENDED", "ended grandchild", "ended");
  var runningChild = node("CHILD-RUNNING", "running child", "active", [runningGrandchild, endedGrandchild]);
  t.projects = [{
    key: "p1", name: "main", working_dir: "/w/p1", default_expanded: true,
    sessions: [{
      row_id: "project:p1:local:MAIN", ref: "local:MAIN", session_id: "MAIN",
      title: "main", state: "active", kind: "session", tier: "current",
      children: [runningChild, node("CHILD-IDLE", "idle retained child", "idle"), node("CHILD-UNKNOWN", "unknown retained child", "mystery"), node("CHILD-ERROR", "errored child", "errored"), node("CHILD-ENDED", "ended child", "ended")],
    }],
  }];
  return t;
}

const { w, posts } = boot();
const tree = treeWithChildren();
w.SerfPanes = { calls: [], state: [], openHrefs: function () { return this.state.slice(); }, openAfter: function (href, title, afterHref) {
  this.calls.push({ href, title, afterHref });
  if (href.indexOf("CHILD-ERROR") !== -1) return null;
  if (this.state.indexOf(href) === -1) this.state.push(href);
  return { href: href };
} };
w.SerfSidebar.renderTree(tree);
const mainRow = w.document.querySelector('[data-row-id="project:p1:local:MAIN"]');
if (!mainRow) throw new Error("main row must render");
const running = w.document.querySelector('[data-row-id="project:p1:local:CHILD-RUNNING"]');
const retained = w.document.querySelector('[data-row-id="project:p1:local:CHILD-IDLE"]');
if (!running || !retained) throw new Error("running and idle direct children must render automatically");
const unknownSource = tree.projects[0].sessions[0].children[2];
const unknownRow = w.document.querySelector('[data-row-id="project:p1:local:CHILD-UNKNOWN"]');
if (!unknownRow || !unknownRow.classList.contains("subagent-row")) throw new Error("unknown child state must remain visible as a current child");
if (unknownRow.getAttribute("data-state") !== "notLoaded") throw new Error("unknown child state must use neutral notLoaded presentation");
if (unknownRow.querySelector(".status-icon").getAttribute("title") !== "Not loaded") throw new Error("unknown child state must use neutral text");
if (w.SerfSidebarInternal.stateIconKey("notLoaded") !== "idle") throw new Error("unknown child state must use neutral icon");
if (unknownSource.state !== "mystery") throw new Error("unknown state normalization must not mutate the source node");
if (unknownSource.row_id !== "project:p1:local:CHILD-UNKNOWN" || unknownSource.ref !== "local:CHILD-UNKNOWN") throw new Error("unknown state normalization must not mutate server identity fields");
if (!running.classList.contains("subagent-row") || !retained.classList.contains("subagent-row")) {
  throw new Error("current direct children must be indented subagent rows");
}
if (!w.document.querySelector('[data-row-id="project:p1:local:GRAND-IDLE"]')) {
  throw new Error("current grandchild must render under its direct parent");
}
const ordinaryClick = new w.MouseEvent("click", { bubbles: true, cancelable: true });
mainRow.dispatchEvent(ordinaryClick);
if (ordinaryClick.defaultPrevented) throw new Error("ordinary main-session rows must retain HTMX navigation");
const runningClick = new w.MouseEvent("click", { bubbles: true, cancelable: true });
running.dispatchEvent(runningClick);
if (!runningClick.defaultPrevented) throw new Error("current child activation must prevent ordinary navigation");
if (w.SerfPanes.calls.length !== 1 ||
    w.SerfPanes.calls[0].href !== "/thread/local%3ACHILD-RUNNING" || w.SerfPanes.calls[0].afterHref !== null) {
  throw new Error("direct child activation must open only the child as the first side pane: " + JSON.stringify(w.SerfPanes.calls));
}
// Existing ancestors are skipped; duplicate target activation makes one
// openAfter call and leaves the pane order to the pane manager.
w.SerfPanes.calls.length = 0;
w.SerfPanes.state = [];
running.dispatchEvent(new w.MouseEvent("click", { bubbles: true, cancelable: true }));
if (w.SerfPanes.calls.length !== 1 ||
    w.SerfPanes.calls[0].href !== "/thread/local%3ACHILD-RUNNING" || w.SerfPanes.calls[0].afterHref !== null) {
  throw new Error("direct child activation must open only the child as the first side pane: " + JSON.stringify(w.SerfPanes.calls));
}
// Existing ancestors are skipped; duplicate target activation makes one openAfter call and leaves the pane order to the pane manager.
w.SerfPanes.calls.length = 0;
w.SerfPanes.state = ["/thread/local%3ACHILD-RUNNING"];
running.dispatchEvent(new w.MouseEvent("click", { bubbles: true, cancelable: true }));
if (w.SerfPanes.calls.length !== 1 || w.SerfPanes.calls[0].href !== "/thread/local%3ACHILD-RUNNING") throw new Error("existing ancestors must be skipped and duplicate target must use one openAfter call");
const grandchildCurrent = w.document.querySelector('[data-row-id="project:p1:local:GRAND-IDLE"]');
w.SerfPanes.calls.length = 0; w.SerfPanes.state = [];
grandchildCurrent.dispatchEvent(new w.MouseEvent("click", { bubbles: true, cancelable: true }));
if (w.SerfPanes.calls.length !== 2 ||
    w.SerfPanes.calls[0].href !== "/thread/local%3ACHILD-RUNNING" || w.SerfPanes.calls[0].afterHref !== null ||
    w.SerfPanes.calls[1].href !== "/thread/local%3AGRAND-IDLE" || w.SerfPanes.calls[1].afterHref !== "/thread/local%3ACHILD-RUNNING") {
  throw new Error("nested child activation must use source-qualified parent-to-child calls: " + JSON.stringify(w.SerfPanes.calls));
}
// Keyed rows must retain identity while transitioning child -> ordinary and
// ordinary -> child; only the current descriptor controls interception.
const promoted = w.document.querySelector('[data-row-id="project:p1:local:CHILD-IDLE"]');
promoted.__identity = true;
const promotedNode = JSON.parse(JSON.stringify(tree));
promotedNode.projects[0].sessions[0].children = promotedNode.projects[0].sessions[0].children.filter(function (n) { return n.row_id !== "project:p1:local:CHILD-IDLE"; });
promotedNode.projects[0].sessions.push({ row_id: "project:p1:local:CHILD-IDLE", ref: "local:CHILD-IDLE", session_id: "CHILD-IDLE", title: "idle retained child", state: "active", kind: "session", tier: "current" });
w.SerfSidebar.renderTree(promotedNode);
const promotedAfter = w.document.querySelector('[data-row-id="project:p1:local:CHILD-IDLE"]');
if (promotedAfter !== promoted || promotedAfter.__identity !== true || promotedAfter.classList.contains("subagent-row") || promotedAfter.getAttribute("data-subagent-depth") !== null) throw new Error("child -> ordinary patch must preserve row identity and clear child stamping");
const ordinaryTransitionClick = new w.MouseEvent("click", { bubbles: true, cancelable: true });
promotedAfter.dispatchEvent(ordinaryTransitionClick);
if (ordinaryTransitionClick.defaultPrevented) throw new Error("demoted row must retain ordinary navigation");
const demotedNode = JSON.parse(JSON.stringify(tree));
demotedNode.projects[0].sessions = demotedNode.projects[0].sessions.filter(function (n) { return n.row_id !== "project:p1:local:CHILD-IDLE"; });
demotedNode.projects[0].sessions[0].children.push({ row_id: "project:p1:local:CHILD-IDLE", ref: "local:CHILD-IDLE", session_id: "CHILD-IDLE", title: "idle retained child", state: "idle", kind: "session", tier: "current" });
w.SerfSidebar.renderTree(demotedNode);
const demotedAfter = w.document.querySelector('[data-row-id="project:p1:local:CHILD-IDLE"]');
if (demotedAfter !== promoted || !demotedAfter.classList.contains("subagent-row")) throw new Error("ordinary -> child patch must preserve identity and child styling");
const promotedClick = new w.MouseEvent("click", { bubbles: true, cancelable: true });
demotedAfter.dispatchEvent(promotedClick);
if (!promotedClick.defaultPrevented) throw new Error("promoted child must intercept activation");
const mainInactive = w.document.querySelector('[data-row-id="inactive:project:p1:local:MAIN"]');
if (!mainInactive) throw new Error("main must have an inactive disclosure row");
if (mainInactive.textContent.indexOf("Inactive subagents (2)") === -1) {
  throw new Error("main inactive disclosure must count direct terminal children only");
}
if (mainInactive.getAttribute("aria-expanded") !== "false") {
  throw new Error("main inactive disclosure must start collapsed");
}
const childInactive = w.document.querySelector('[data-row-id="inactive:project:p1:local:CHILD-RUNNING"]');
if (!childInactive || childInactive.textContent.indexOf("Inactive subagents (1)") === -1) {
  throw new Error("running child must control its own ended grandchild disclosure");
}
if (w.document.querySelector('[data-row-id="project:p1:local:GRAND-ENDED"]')) {
  throw new Error("ended grandchild must stay behind the child's inactive disclosure");
}
if (w.document.querySelector('[data-row-id="project:p1:local:CHILD-ERROR"]')) {
  throw new Error("errored direct child must stay behind main inactive disclosure");
}
const controlled = w.document.getElementById(mainInactive.getAttribute("aria-controls"));
if (!controlled) throw new Error("inactive disclosure must expose a stable aria-controls target");
if (mainInactive.getAttribute("aria-controls") !== "inactive-rows-project:p1:local:MAIN") {
  throw new Error("inactive disclosure must expose a stable aria-controls target");
}

// Expanding main only reveals its direct terminal children; it must not flatten
// the nested ended grandchild out of the running child's own disclosure.
mainInactive.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
if (!w.document.querySelector('[data-row-id="project:p1:local:CHILD-ERROR"]') || !w.document.querySelector('[data-row-id="project:p1:local:CHILD-ENDED"]')) {
  throw new Error("expanding main inactive must reveal its two direct terminal children");
}
const expandedControlled = w.document.getElementById(mainInactive.getAttribute("aria-controls"));
if (!expandedControlled || !expandedControlled.contains(w.document.querySelector('[data-row-id="project:p1:local:CHILD-ERROR"]')) ||
    !expandedControlled.contains(w.document.querySelector('[data-row-id="project:p1:local:CHILD-ENDED"]'))) {
  throw new Error("inactive disclosure controlled region must contain its inactive direct child rows");
}
if (w.document.querySelector('[data-row-id="project:p1:local:GRAND-ENDED"]')) {
  throw new Error("expanding main inactive must not flatten grandchildren");
}
if (w.localStorage.getItem("serf-hub.sidebar.expanded.inactive:project:p1:local:MAIN") !== "true") {
  throw new Error("main inactive expansion must persist under inactive:<row_id>");
}
const inactiveChild = w.document.querySelector('[data-row-id="project:p1:local:CHILD-ERROR"]');
w.SerfPanes.calls.length = 0; w.SerfPanes.state = [];
inactiveChild.dispatchEvent(new w.MouseEvent("click", { bubbles: true, cancelable: true }));
if (w.SerfPanes.calls.length !== 1 ||
    w.SerfPanes.calls[0].href !== "/thread/local%3ACHILD-ERROR" || w.SerfPanes.calls[0].afterHref !== null) {
  throw new Error("revealed inactive child activation must open ancestry in order: " + JSON.stringify(w.SerfPanes.calls));
}

// Expand the child's independent disclosure, then prove all state survives a
// resync while keyed rows retain their existing DOM identity.
childInactive.dispatchEvent(new w.MouseEvent("click", { bubbles: true }));
const grandEnded = w.document.querySelector('[data-row-id="project:p1:local:GRAND-ENDED"]');
if (!grandEnded) throw new Error("child inactive disclosure must reveal only its ended grandchild");
if (!grandEnded.classList.contains("subagent-row")) throw new Error("inactive rows retain normal indented row styling");
if (grandEnded.getAttribute("data-state") !== "ended") throw new Error("inactive rows retain their normal status state");
const retainedBefore = w.document.querySelector('[data-row-id="project:p1:local:CHILD-IDLE"]');
retainedBefore.__probe = true;
w.SerfSidebar.renderTree(tree);
const retainedAfter = w.document.querySelector('[data-row-id="project:p1:local:CHILD-IDLE"]');
if (!retainedAfter || retainedAfter.__probe !== true) throw new Error("resync must preserve keyed current child row identity");
if (w.document.querySelector('[data-row-id="project:p1:local:GRAND-ENDED"]') !== grandEnded) {
  throw new Error("resync must preserve keyed inactive child row identity");
}
if (w.document.querySelector('[data-row-id="inactive:project:p1:local:MAIN"]').getAttribute("aria-expanded") !== "true" ||
    w.document.querySelector('[data-row-id="inactive:project:p1:local:CHILD-RUNNING"]').getAttribute("aria-expanded") !== "true") {
  throw new Error("resync must preserve each parent's inactive expansion state");
}

// The visual contract is indentation + spacing only: no lineage rail and no
// left-edge active/selected stripe for subagent rows.
const stampedRule = /\.sb-row\[data-subagent-depth\]\s*\{([^}]*)\}/.exec(css);
const stampedBody = stampedRule && stampedRule[1];
const stampedBorderColor = stampedBody && /(?:^|;)\s*border-left-color\s*:\s*([^;}]*)/.exec(stampedBody);
const stampedBorder = stampedBody && /(?:^|;)\s*border-left\s*:\s*([^;}]*)/.exec(stampedBody);
if (!stampedRule || (stampedBorder && stampedBorder[1].trim() !== "none") ||
    !stampedBorderColor || stampedBorderColor[1].trim() !== "transparent" ||
    /\.sb-row\[data-subagent-depth\]\s*::?(?:before|after)/.test(css)) {
  throw new Error("subagent styling must not add a lineage rail or left-edge pseudo stripe");
}
running.setAttribute("data-active", "");
if (!stampedBorderColor || stampedBorderColor[1].trim() !== "transparent") throw new Error("active child must retain transparent left border while selected background remains generic");

// Existing recursive pending overlay and ordinary row navigation remain intact.
w.SerfSidebar.favorite("local:GRAND-ENDED", true);
if (!w.document.querySelector('[data-row-id="project:p1:local:GRAND-ENDED"]').hasAttribute("data-favorite")) {
  throw new Error("pending favorite must recurse through inactive descendants");
}
if (!posts.some((p) => p.url === "/api/favorite" && p.body.id === "GRAND-ENDED" && p.body.favorited === true)) {
  throw new Error("favoriting a nested child must POST its source-qualified session id");
}

console.log("ok sidebar children: recursive current/inactive projection + independent expansion + keyed resync");

// ---------------------------------------------------------------------------
// Auto-reveal: a deep-link behind a collapsed project and inactive/current
// disclosures must still reveal the exact target without flattening siblings.
// ---------------------------------------------------------------------------
function treeWithDeepChild() {
  var t = emptyTree();
  t.projects = [{
    key: "p2", name: "deep-proj", working_dir: "/w/p2", default_expanded: false,
    sessions: [{
      row_id: "project:p2:local:01PARENT2", ref: "local:01PARENT2", session_id: "01PARENT2",
      title: "deep parent", state: "idle", kind: "session", tier: "current",
      children: [node("CHILDDEEP", "deep child", "ended")],
    }],
  }];
  // node() uses p1's row prefix; make this fixture's source-qualified identity explicit.
  t.projects[0].sessions[0].children[0].row_id = "project:p2:local:CHILDDEEP";
  t.projects[0].sessions[0].children[0].ref = "local:CHILDDEEP";
  t.projects[0].sessions[0].children[0].session_id = "CHILDDEEP";
  return t;
}

const { w: w2 } = boot("http://localhost/s/CHILDDEEP");
const deepTree = treeWithDeepChild();
w2.SerfSidebar.renderTree(deepTree);
const revealed = w2.document.querySelector('[data-row-id="project:p2:local:CHILDDEEP"]');
if (!revealed) throw new Error("auto-reveal must expand the project + current/inactive chain so the deep-linked child renders");
if (!revealed.hasAttribute("data-active")) throw new Error("the auto-revealed row must be marked data-active");
if (w2.localStorage.getItem("serf-hub.sidebar.expanded.p2") !== "true") throw new Error("auto-reveal must persist the enclosing project's expansion key");
if (w2.localStorage.getItem("serf-hub.sidebar.expanded.inactive:project:p2:local:01PARENT2") !== "true") throw new Error("auto-reveal must persist the parent's inactive disclosure key");

let expandWrites = 0;
const realSetItem = w2.localStorage.setItem.bind(w2.localStorage);
w2.localStorage.setItem = function (k, v) {
  if (k.indexOf("serf-hub.sidebar.expanded.") === 0) expandWrites++;
  return realSetItem(k, v);
};
w2.SerfSidebar.renderTree(deepTree);
if (expandWrites !== 0) throw new Error("a second render for the same pathname must not re-run the reveal, got " + expandWrites + " new expansion writes");
console.log("ok sidebar auto-reveal: deep-link through collapsed project + inactive chain, loop-guarded");

const { w: w3 } = boot("http://localhost/s/01DOES-NOT-EXIST");
let threw = null;
try { w3.SerfSidebar.renderTree(treeWithDeepChild()); } catch (e) { threw = e; }
if (threw) throw new Error("auto-reveal for a nonexistent session must not throw: " + threw);
if (w3.document.querySelector("#sidebar .sb-row[data-active]")) throw new Error("a nonexistent session must not mark any row active");
console.log("ok sidebar auto-reveal: nonexistent session does not loop or throw");
process.exit(0);

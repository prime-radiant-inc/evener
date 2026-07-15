# Web UI Subagent Sidebar Mockup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a standalone interactive mockup that shows current and inactive subagents under their direct parents and opens each subagent immediately to the right of its direct parent pane.

**Architecture:** Add one self-contained HTML document under the existing Web UI mockups. A normalized fixture store drives a recursive sidebar renderer, a horizontal pane controller, deterministic lifecycle scenarios, and embedded self-checks. The file imports only the existing mockup token sheet and never calls production JavaScript or APIs.

**Tech Stack:** HTML, CSS, vanilla JavaScript, existing `docs/web-ui/mockups/tokens.css`, Node.js, temporary jsdom validation outside the repository.

## Global Constraints

- Create only `docs/web-ui/mockups/23-subagent-sidebar.html` for the mockup implementation.
- Do not modify production code or assets.
- The mockup must work from a direct `file://` open with no build, server, API, or production JavaScript.
- Current statuses are exactly `running`, `awaiting`, `retained-idle`, and `unknown`.
- Terminal statuses are exactly `completed`, `failed`, `cancelled`, and `stopped`.
- Render current direct children without a wrapper disclosure.
- Render terminal direct children only inside their parent's initially collapsed `Inactive subagents (N)` disclosure.
- Preserve recursive direct-parent relationships, including descendants of terminal children.
- Insert a newly opened child pane immediately to the right of its direct parent pane.
- Focus an existing pane instead of creating a duplicate.
- Closing a pane closes its open descendants and restores focus to the originating sidebar row.
- Convey status in text and color; use semantic buttons, `aria-expanded`, `aria-current`, labelled pane roots, visible focus, and keyboard activation.
- Use deterministic scenario controls; do not use timers, network requests, or ambient state.
- Test with temporary tooling under `/tmp`; do not add a package manifest, lockfile, or vendored dependency.

## File Structure

- Create `docs/web-ui/mockups/23-subagent-sidebar.html`: owns fixture data, visual structure, recursive sidebar rendering, inactive disclosure state, pane ordering, lifecycle scenarios, accessibility behavior, and embedded self-checks.
- Reuse `docs/web-ui/mockups/tokens.css`: supplies the existing mockup color, typography, spacing, and status token vocabulary. Do not modify it.
- Create `/tmp/serf-subagent-sidebar-check.js` only as an ephemeral jsdom harness during verification; remove it before completion.

## Shared Interfaces

The single HTML script exposes this inspection surface for embedded and jsdom checks:

```javascript
window.SubagentSidebarMockup = {
  STATUS_META,
  CURRENT_STATUSES,
  TERMINAL_STATUSES,
  state,
  childrenOf,
  partitionChildren,
  render,
  activateSession,
  closePane,
  applyScenario,
  runSelfChecks,
};
```

Use these exact data shapes:

```javascript
const sessions = new Map([
  ["main", { id: "main", parentId: null, title: "Restore subagent navigation", status: "running" }],
  ["audit", { id: "audit", parentId: "main", title: "Audit sidebar data flow", status: "running" }],
]);

const state = {
  sessions,
  inactiveExpanded: new Set(),
  openPaneIds: ["main"],
  activePaneId: "main",
  returnFocusByPane: new Map(),
};
```

Every renderer and controller reads from this state. Do not duplicate session records into current, inactive, or pane arrays.

---

### Task 1: Render the recursive current and inactive sidebar

**Files:**
- Create: `docs/web-ui/mockups/23-subagent-sidebar.html`

**Interfaces:**
- Consumes: existing `tokens.css` and the normalized `state.sessions` map.
- Produces: `STATUS_META`, `CURRENT_STATUSES`, `TERMINAL_STATUSES`, `childrenOf(parentId)`, `partitionChildren(parentId)`, `renderSidebar()`, `renderSessionBranch(sessionId, depth, container)`, `renderInactiveGroup(parentId, terminalChildren, depth, container)`, `toggleInactive(parentId)`, `render()`, and the first embedded self-checks.
- DOM contract: session rows use `[data-session-row="<id>"]`; inactive controls use `[data-inactive-toggle="<parentId>"]`; inactive regions use `[data-inactive-region="<parentId>"]`; status text uses `[data-status-text]`.

- [ ] **Step 1: Create the document shell and failing embedded sidebar checks**

Create `docs/web-ui/mockups/23-subagent-sidebar.html` with a complete HTML document. Link the existing tokens through:

```html
<link rel="stylesheet" href="tokens.css">
```

Add this top-level structure:

```html
<main class="mockup-page">
  <header class="mockup-intro">
    <p class="eyebrow">Web UI navigation prototype</p>
    <h1>Subagents stay attached to their session</h1>
    <p>Current work remains visible. Terminal work folds into each parent's inactive disclosure.</p>
  </header>

  <section class="scenario-bar" aria-label="Mockup scenarios">
    <button type="button" data-scenario="baseline" aria-pressed="true">Mixed status tree</button>
    <button type="button" data-scenario="complete-audit" aria-pressed="false">Audit completes</button>
    <button type="button" data-scenario="resume-audit" aria-pressed="false">Audit resumes</button>
  </section>

  <section class="app-frame" aria-label="Subagent sidebar and pane workspace">
    <aside class="prototype-sidebar" aria-label="Sessions">
      <div class="sidebar-project"><span aria-hidden="true">⌄</span><span>serf</span></div>
      <nav id="session-tree" aria-label="Session tree"></nav>
    </aside>
    <section id="pane-strip" class="pane-strip" aria-label="Open agent sessions"></section>
  </section>

  <section class="self-checks" aria-labelledby="self-check-title">
    <h2 id="self-check-title">Prototype checks</h2>
    <p id="self-check-summary" aria-live="polite">Not run</p>
    <ol id="self-check-list"></ol>
  </section>
</main>
```

Define `runSelfChecks()` first with sidebar assertions that call missing renderer functions. Use an assertion collector rather than throwing on the first failure:

```javascript
function runSelfChecks() {
  const results = [];
  function check(name, condition) {
    results.push({ name, pass: Boolean(condition) });
  }

  const current = partitionChildren("main").current.map((item) => item.status);
  const terminal = partitionChildren("main").terminal.map((item) => item.status);
  check("current status set is exact", sameSet(current, CURRENT_STATUSES));
  check("terminal status set is exact", sameSet(terminal, TERMINAL_STATUSES));
  check("current children render without a wrapper", document.querySelectorAll('[data-session-row="audit"]').length === 1);
  check("inactive disclosure starts collapsed", document.querySelector('[data-inactive-toggle="main"]')?.getAttribute("aria-expanded") === "false");
  check("inactive count uses direct terminal children", document.querySelector('[data-inactive-toggle="main"]')?.textContent.includes("4"));
  check("terminal descendants keep direct parentage", document.querySelector('[data-inactive-region="main"] [data-session-row="archive"] [data-session-row="archive-child"]') !== null);

  renderCheckResults(results);
  return results;
}
```

Include helpers with exact signatures:

```javascript
function sameSet(values, expectedSet) {
  return values.length === expectedSet.size && values.every((value) => expectedSet.has(value));
}

function renderCheckResults(results) {
  const list = document.getElementById("self-check-list");
  list.replaceChildren(...results.map(({ name, pass }) => {
    const item = document.createElement("li");
    item.className = pass ? "pass" : "fail";
    item.textContent = `${pass ? "PASS" : "FAIL"}: ${name}`;
    return item;
  }));
  const passed = results.filter((result) => result.pass).length;
  document.getElementById("self-check-summary").textContent = `${passed}/${results.length} checks passing`;
}
```

Call `render(); runSelfChecks();` at the end. Leave `partitionChildren` and `render` as explicit throwing stubs for the red state:

```javascript
function partitionChildren() { throw new Error("sidebar partition not implemented"); }
function render() { throw new Error("sidebar render not implemented"); }
```

- [ ] **Step 2: Open the mockup and confirm the red state**

Run:

```bash
open docs/web-ui/mockups/23-subagent-sidebar.html
```

Expected: the browser console reports `sidebar render not implemented`, and the prototype does not render the session tree.

- [ ] **Step 3: Add the exact fixture matrix and status classifier**

Replace the stubs with the normalized fixture store. Include all eight statuses and a terminal child that owns both current and terminal children:

```javascript
const STATUS_META = Object.freeze({
  running:       { label: "Running", tone: "live", glyph: "⟳", terminal: false },
  awaiting:      { label: "Awaiting input", tone: "attention", glyph: "!", terminal: false },
  "retained-idle": { label: "Idle · resumable", tone: "neutral", glyph: "•", terminal: false },
  unknown:       { label: "Unknown", tone: "neutral", glyph: "?", terminal: false },
  completed:     { label: "Completed", tone: "done", glyph: "✓", terminal: true },
  failed:        { label: "Failed", tone: "error", glyph: "×", terminal: true },
  cancelled:     { label: "Cancelled", tone: "neutral", glyph: "–", terminal: true },
  stopped:       { label: "Stopped", tone: "neutral", glyph: "■", terminal: true },
});
const CURRENT_STATUSES = new Set(["running", "awaiting", "retained-idle", "unknown"]);
const TERMINAL_STATUSES = new Set(["completed", "failed", "cancelled", "stopped"]);

const BASELINE_SESSIONS = [
  { id: "main", parentId: null, title: "Restore subagent navigation", status: "running" },
  { id: "audit", parentId: "main", title: "Audit sidebar data flow", status: "running" },
  { id: "review", parentId: "main", title: "Review interaction model", status: "awaiting" },
  { id: "retained", parentId: "main", title: "Retained implementation agent", status: "retained-idle" },
  { id: "mystery", parentId: "main", title: "Recover remote worker", status: "unknown" },
  { id: "archive", parentId: "main", title: "Inspect session history", status: "completed" },
  { id: "failed", parentId: "main", title: "Probe stale roster", status: "failed" },
  { id: "cancelled", parentId: "main", title: "Compare discarded direction", status: "cancelled" },
  { id: "stopped", parentId: "main", title: "Stop redundant scan", status: "stopped" },
  { id: "audit-live", parentId: "audit", title: "Trace live roster", status: "running" },
  { id: "audit-done", parentId: "audit", title: "Read sidebar renderer", status: "completed" },
  { id: "archive-child", parentId: "archive", title: "Summarize prior behavior", status: "running" },
  { id: "archive-child-done", parentId: "archive", title: "Catalog old tests", status: "completed" },
];

const sessions = new Map(BASELINE_SESSIONS.map((session) => [session.id, { ...session }]));
const state = {
  sessions,
  inactiveExpanded: new Set(),
  openPaneIds: ["main"],
  activePaneId: "main",
  returnFocusByPane: new Map(),
};

function childrenOf(parentId) {
  return Array.from(state.sessions.values()).filter((session) => session.parentId === parentId);
}

function partitionChildren(parentId) {
  return childrenOf(parentId).reduce((groups, session) => {
    const meta = STATUS_META[session.status];
    if (!meta) throw new Error(`Unknown fixture status: ${session.status}`);
    groups[meta.terminal ? "terminal" : "current"].push(session);
    return groups;
  }, { current: [], terminal: [] });
}
```

- [ ] **Step 4: Implement recursive direct-child rendering**

Implement the renderer with DOM APIs and no interpolated `innerHTML` for fixture text:

```javascript
function render() {
  renderSidebar();
  renderPanes();
  syncScenarioButtons();
}

function renderSidebar() {
  const tree = document.getElementById("session-tree");
  tree.replaceChildren();
  renderSessionBranch("main", 0, tree);
}

function renderSessionBranch(sessionId, depth, container) {
  const session = state.sessions.get(sessionId);
  const branch = document.createElement("div");
  branch.className = "session-branch";
  branch.dataset.sessionBranch = session.id;
  branch.style.setProperty("--depth", depth);
  branch.append(buildSessionRow(session, depth));

  const { current, terminal } = partitionChildren(session.id);
  current.forEach((child) => renderSessionBranch(child.id, depth + 1, branch));
  if (terminal.length) renderInactiveGroup(session.id, terminal, depth + 1, branch);
  container.append(branch);
}

function renderInactiveGroup(parentId, terminalChildren, depth, container) {
  const group = document.createElement("div");
  group.className = "inactive-group";
  group.style.setProperty("--depth", depth);

  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "inactive-toggle";
  toggle.dataset.inactiveToggle = parentId;
  toggle.setAttribute("aria-expanded", String(state.inactiveExpanded.has(parentId)));
  toggle.setAttribute("aria-controls", `inactive-${parentId}`);
  toggle.textContent = `Inactive subagents (${terminalChildren.length})`;
  toggle.addEventListener("click", () => toggleInactive(parentId));

  const region = document.createElement("div");
  region.id = `inactive-${parentId}`;
  region.dataset.inactiveRegion = parentId;
  region.hidden = !state.inactiveExpanded.has(parentId);
  terminalChildren.forEach((child) => renderSessionBranch(child.id, depth, region));
  group.append(toggle, region);
  container.append(group);
}

function toggleInactive(parentId) {
  if (state.inactiveExpanded.has(parentId)) state.inactiveExpanded.delete(parentId);
  else state.inactiveExpanded.add(parentId);
  renderSidebar();
  document.querySelector(`[data-inactive-toggle="${CSS.escape(parentId)}"]`)?.focus();
}
```

`buildSessionRow(session, depth)` must return a `<button type="button">` with:

```javascript
row.dataset.sessionRow = session.id;
row.dataset.status = session.status;
row.style.setProperty("--depth", depth);
row.setAttribute("aria-current", state.activePaneId === session.id ? "true" : "false");
row.addEventListener("click", () => activateSession(session.id, row));
```

Append separate glyph, title, and status spans. Put the exact `STATUS_META[session.status].label` into a span carrying `data-status-text`.

- [ ] **Step 5: Add production-shaped sidebar styling**

Add scoped CSS that uses existing tokens and these structural rules:

```css
.mockup-page { max-width: 1500px; margin: 0 auto; padding: var(--s5); }
.app-frame { display: grid; grid-template-columns: 292px minmax(0, 1fr); min-height: 620px; border: 1px solid var(--line); border-radius: var(--r); overflow: hidden; background: var(--bg); }
.prototype-sidebar { border-right: 1px solid var(--line); background: var(--bg-1); padding: var(--s3) var(--s2); overflow: auto; }
.session-row, .inactive-toggle { width: calc(100% - (var(--depth) * 14px)); margin-left: calc(var(--depth) * 14px); }
.session-row { display: grid; grid-template-columns: 18px minmax(0, 1fr) auto; gap: var(--s2); align-items: center; min-height: 34px; padding: 0 var(--s2); border-radius: var(--r); text-align: left; }
.session-row[aria-current="true"] { background: var(--surface); box-shadow: inset 2px 0 0 var(--accent); }
.session-row:focus-visible, .inactive-toggle:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.inactive-toggle { color: var(--ink-3); font-size: var(--fs-xs); padding: var(--s2); text-align: left; }
[data-tone="live"] { color: var(--accent); }
[data-tone="attention"] { color: var(--attention); }
[data-tone="error"] { color: var(--error); }
[data-tone="done"], [data-tone="neutral"] { color: var(--ink-3); }
```

Use a subtle branch line with `.session-branch > .session-branch::before` or equivalent. Do not introduce a new palette or use green for completed work.

- [ ] **Step 6: Run the mockup and confirm the sidebar checks turn green**

Run:

```bash
open docs/web-ui/mockups/23-subagent-sidebar.html
```

Expected: the sidebar renders four current children under the main row; `Inactive subagents (4)` starts collapsed; after expanding it, the completed `archive` row contains its current and inactive descendants; the self-check panel reports all Task 1 checks passing.

- [ ] **Step 7: Commit the recursive sidebar slice**

```bash
git add docs/web-ui/mockups/23-subagent-sidebar.html
git commit -m "mockup(web): model recursive subagent sidebar"
```

---

### Task 2: Add parent-relative panes and stable pane identity

**Files:**
- Modify: `docs/web-ui/mockups/23-subagent-sidebar.html`

**Interfaces:**
- Consumes: `state.sessions`, `state.openPaneIds`, `state.activePaneId`, sidebar row IDs, and direct `parentId` relationships from Task 1.
- Produces: `renderPanes()`, `buildPane(session)`, `activateSession(sessionId, originRow)`, `ensurePane(sessionId)`, `closePane(sessionId)`, `isDescendantOf(sessionId, ancestorId)`, `focusPane(sessionId)`, and pane self-checks.
- DOM contract: pane roots use `[data-pane-id="<id>"]`; pane roots have `tabindex="-1"`; pane titles have `id="pane-title-<id>"`; non-main panes have `[data-close-pane="<id>"]`.

- [ ] **Step 1: Add failing pane-order and identity self-checks**

Append these checks inside `runSelfChecks()` after the sidebar checks. Save and restore state so the checks do not alter the visible starting scenario:

```javascript
const paneSnapshot = {
  openPaneIds: [...state.openPaneIds],
  activePaneId: state.activePaneId,
  expanded: new Set(state.inactiveExpanded),
};

activateSession("audit-live", document.querySelector('[data-session-row="audit-live"]'));
check("missing ancestor opens first", state.openPaneIds.includes("audit"));
check("nested child sits immediately right of direct parent", state.openPaneIds.indexOf("audit-live") === state.openPaneIds.indexOf("audit") + 1);
const countBeforeReopen = state.openPaneIds.filter((id) => id === "audit-live").length;
activateSession("audit-live", document.querySelector('[data-session-row="audit-live"]'));
check("reopening creates no duplicate", countBeforeReopen === 1 && state.openPaneIds.filter((id) => id === "audit-live").length === 1);
check("existing pane root receives focus", document.activeElement?.dataset.paneId === "audit-live");

state.openPaneIds = paneSnapshot.openPaneIds;
state.activePaneId = paneSnapshot.activePaneId;
state.inactiveExpanded = paneSnapshot.expanded;
render();
```

Temporarily define `activateSession()` as:

```javascript
function activateSession() { throw new Error("pane controller not implemented"); }
```

- [ ] **Step 2: Open the mockup and confirm the pane checks fail**

Run:

```bash
open docs/web-ui/mockups/23-subagent-sidebar.html
```

Expected: the console reports `pane controller not implemented`, or the pane checks report failures if the error is caught by the self-check collector.

- [ ] **Step 3: Implement immediate-right pane insertion and focus-existing behavior**

Implement the controller with these exact rules:

```javascript
function activateSession(sessionId, originRow) {
  if (originRow) state.returnFocusByPane.set(sessionId, originRow.dataset.sessionRow);
  ensurePane(sessionId);
  state.activePaneId = sessionId;
  render();
  focusPane(sessionId);
}

function ensurePane(sessionId) {
  if (state.openPaneIds.includes(sessionId)) return;
  const session = state.sessions.get(sessionId);
  if (!session) throw new Error(`Missing session: ${sessionId}`);
  if (session.parentId && !state.openPaneIds.includes(session.parentId)) ensurePane(session.parentId);
  if (!session.parentId) return;
  const parentIndex = state.openPaneIds.indexOf(session.parentId);
  state.openPaneIds.splice(parentIndex + 1, 0, sessionId);
}

function focusPane(sessionId) {
  const pane = document.querySelector(`[data-pane-id="${CSS.escape(sessionId)}"]`);
  pane?.scrollIntoView({ behavior: "smooth", block: "nearest", inline: "nearest" });
  pane?.focus({ preventScroll: true });
}
```

The insertion algorithm intentionally shifts every later pane right. It does not append after the parent's descendant block.

- [ ] **Step 4: Render labelled, closable pane roots**

Implement:

```javascript
function renderPanes() {
  const strip = document.getElementById("pane-strip");
  strip.replaceChildren(...state.openPaneIds.map((id) => buildPane(state.sessions.get(id))));
}

function buildPane(session) {
  const pane = document.createElement("article");
  pane.className = "thread-pane";
  pane.dataset.paneId = session.id;
  pane.tabIndex = -1;
  pane.setAttribute("aria-labelledby", `pane-title-${session.id}`);
  if (state.activePaneId === session.id) pane.dataset.active = "true";

  const header = document.createElement("header");
  const heading = document.createElement("h2");
  heading.id = `pane-title-${session.id}`;
  heading.textContent = session.title;
  const status = document.createElement("span");
  status.textContent = STATUS_META[session.status].label;
  status.dataset.tone = STATUS_META[session.status].tone;
  header.append(heading, status);

  if (session.id !== "main") {
    const close = document.createElement("button");
    close.type = "button";
    close.dataset.closePane = session.id;
    close.setAttribute("aria-label", `Close ${session.title}`);
    close.textContent = "×";
    close.addEventListener("click", () => closePane(session.id));
    header.append(close);
  }

  const body = document.createElement("div");
  body.className = "pane-body";
  body.innerHTML = '<p class="fixture-label">Fixture transcript</p><p>This pane represents the selected agent session. Transcript streaming is outside this mockup.</p>';
  pane.append(header, body);
  return pane;
}
```

The fixed body string contains no fixture input. All session-derived text still uses `textContent`.

- [ ] **Step 5: Implement close-cascade and focus restoration**

Implement:

```javascript
function isDescendantOf(sessionId, ancestorId) {
  let current = state.sessions.get(sessionId);
  while (current?.parentId) {
    if (current.parentId === ancestorId) return true;
    current = state.sessions.get(current.parentId);
  }
  return false;
}

function closePane(sessionId) {
  if (sessionId === "main") return;
  const closedIds = new Set(state.openPaneIds.filter((id) => id === sessionId || isDescendantOf(id, sessionId)));
  state.openPaneIds = state.openPaneIds.filter((id) => !closedIds.has(id));
  state.activePaneId = state.sessions.get(sessionId)?.parentId || "main";

  const session = state.sessions.get(sessionId);
  if (session && STATUS_META[session.status].terminal && session.parentId) {
    state.inactiveExpanded.add(session.parentId);
  }
  render();

  const rowId = state.returnFocusByPane.get(sessionId) || sessionId;
  document.querySelector(`[data-session-row="${CSS.escape(rowId)}"]`)?.focus();
}
```

Before `render()` in `closePane`, add two self-check assertions by opening `audit` and `audit-live`, closing `audit`, then confirming both pane IDs are absent and an unrelated sibling pane remains when present.

- [ ] **Step 6: Add horizontal pane styling and narrow-width behavior**

Use an actual horizontal strip rather than shrinking panes into unreadable columns:

```css
.pane-strip { display: flex; min-width: 0; overflow-x: auto; scroll-snap-type: x proximity; background: var(--bg); }
.thread-pane { flex: 0 0 clamp(360px, 48vw, 680px); min-height: 620px; border-right: 1px solid var(--line); scroll-snap-align: start; outline: none; }
.thread-pane[data-active="true"] { box-shadow: inset 0 2px 0 var(--accent); }
.thread-pane:focus-visible { box-shadow: inset 0 0 0 2px var(--accent); }
.thread-pane > header { display: grid; grid-template-columns: minmax(0, 1fr) auto auto; gap: var(--s3); align-items: center; min-height: 48px; padding: 0 var(--s4); border-bottom: 1px solid var(--line); }
.pane-body { max-width: 68ch; padding: var(--s5); color: var(--ink-2); line-height: 1.65; }
@media (max-width: 700px) {
  .mockup-page { padding: 0; }
  .app-frame { grid-template-columns: minmax(210px, 44vw) minmax(0, 1fr); border-radius: 0; }
  .thread-pane { flex-basis: calc(100vw - min(44vw, 292px)); }
}
```

- [ ] **Step 7: Verify pane ordering, duplicate prevention, close cascade, and focus**

Open the file and perform this exact sequence:

1. Click `Trace live roster`; confirm panes read main → audit → audit-live.
2. Click `Review interaction model`; confirm it inserts immediately right of main and shifts the audit subtree right.
3. Click `Trace live roster` again; confirm no duplicate pane appears and its pane root gains focus.
4. Close `Audit sidebar data flow`; confirm both audit panes close while the review pane remains.
5. Expand the main inactive group, open `Inspect session history`, then close it; confirm focus returns to its sidebar row and the inactive disclosure remains expanded.

Expected: every Task 2 self-check reports PASS.

- [ ] **Step 8: Commit the pane interaction slice**

```bash
git add docs/web-ui/mockups/23-subagent-sidebar.html
git commit -m "mockup(web): open subagents beside parent panes"
```

---

### Task 3: Add deterministic lifecycle scenarios and finish accessibility checks

**Files:**
- Modify: `docs/web-ui/mockups/23-subagent-sidebar.html`

**Interfaces:**
- Consumes: the fixture store and render/controller interfaces from Tasks 1 and 2.
- Produces: `SCENARIOS`, `applyScenario(name)`, `resetSessions(statusOverrides)`, `syncScenarioButtons()`, complete `runSelfChecks()`, and final responsive/accessibility presentation.
- Scenario contract: `baseline`, `complete-audit`, and `resume-audit` are the only scenario names.

- [ ] **Step 1: Add failing lifecycle and disclosure-state checks**

Add these checks to `runSelfChecks()` before implementing scenarios:

```javascript
state.inactiveExpanded.delete("audit");
applyScenario("complete-audit");
check("running to completed moves the row into inactive", !document.querySelector('[data-session-row="audit"]') && document.querySelector('[data-inactive-toggle="main"]')?.textContent.includes("5"));
check("new inactive disclosure starts collapsed", document.querySelector('[data-inactive-toggle="audit"]') === null || document.querySelector('[data-inactive-toggle="audit"]')?.getAttribute("aria-expanded") === "false");

state.inactiveExpanded.add("main");
applyScenario("resume-audit");
check("completed to retained-idle returns the row to current", document.querySelector('[data-session-row="audit"][data-status="retained-idle"]') !== null);
check("nonempty disclosure preserves expansion", document.querySelector('[data-inactive-toggle="main"]')?.getAttribute("aria-expanded") === "true");

applyScenario("baseline");
```

Temporarily define:

```javascript
function applyScenario() { throw new Error("scenarios not implemented"); }
```

- [ ] **Step 2: Open the mockup and confirm the lifecycle checks fail**

Run:

```bash
open docs/web-ui/mockups/23-subagent-sidebar.html
```

Expected: the lifecycle checks fail or the console reports `scenarios not implemented`.

- [ ] **Step 3: Implement deterministic scenario resets**

Use exact status overrides and preserve pane identity across transitions:

```javascript
const SCENARIOS = Object.freeze({
  baseline: {},
  "complete-audit": { audit: "completed" },
  "resume-audit": { audit: "retained-idle" },
});

let activeScenario = "baseline";

function resetSessions(statusOverrides) {
  state.sessions = new Map(BASELINE_SESSIONS.map((session) => [
    session.id,
    { ...session, status: statusOverrides[session.id] || session.status },
  ]));
}

function applyScenario(name) {
  const overrides = SCENARIOS[name];
  if (!overrides) throw new Error(`Unknown scenario: ${name}`);
  const beforeCounts = new Map(Array.from(state.sessions.keys(), (id) => [id, partitionChildren(id).terminal.length]));
  resetSessions(overrides);

  state.sessions.forEach((session) => {
    const before = beforeCounts.get(session.id) || 0;
    const after = partitionChildren(session.id).terminal.length;
    if (before === 0 && after > 0) state.inactiveExpanded.delete(session.id);
    if (after === 0) state.inactiveExpanded.delete(session.id);
  });

  activeScenario = name;
  render();
  runSelfChecks({ preserveScenario: true });
}
```

Update `runSelfChecks(options = {})` so it snapshots and restores `activeScenario`, session records, disclosure state, open panes, active pane, and focus bookkeeping. When `options.preserveScenario` is true, render the caller's scenario again after the test matrix. This prevents recursive self-check calls: add a `checking` boolean guard and do not call `runSelfChecks()` from `applyScenario` while `checking` is true.

- [ ] **Step 4: Wire scenario buttons and pressed state**

Add:

```javascript
document.querySelectorAll("[data-scenario]").forEach((button) => {
  button.addEventListener("click", () => applyScenario(button.dataset.scenario));
});

function syncScenarioButtons() {
  document.querySelectorAll("[data-scenario]").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.scenario === activeScenario));
  });
}
```

Scenario changes must not clear `state.openPaneIds`; an open audit pane stays open and updates its status when audit moves between current and inactive.

- [ ] **Step 5: Complete accessibility and identity self-checks**

Add deterministic checks for:

```javascript
check("status always has visible text", Array.from(document.querySelectorAll("[data-status-text]")).every((node) => node.textContent.trim().length > 0));
check("disclosures expose aria-expanded", Array.from(document.querySelectorAll("[data-inactive-toggle]")).every((node) => ["true", "false"].includes(node.getAttribute("aria-expanded"))));
check("one pane per session ID", new Set(state.openPaneIds).size === state.openPaneIds.length);
check("pane IDs are unique", document.querySelectorAll("[data-pane-id]").length === new Set(Array.from(document.querySelectorAll("[data-pane-id]"), (node) => node.dataset.paneId)).size);
check("pane roots are labelled", Array.from(document.querySelectorAll("[data-pane-id]")).every((pane) => pane.getAttribute("aria-labelledby") && document.getElementById(pane.getAttribute("aria-labelledby"))));
check("active row exposes aria-current", document.querySelectorAll('[data-session-row][aria-current="true"]').length === 1);
```

Add a visible note below the scenario buttons: `All content is deterministic fixture data. No live sessions or APIs are used.`

- [ ] **Step 6: Add a reduced-motion rule and finish the visual hierarchy**

Add:

```css
.scenario-bar { display: flex; flex-wrap: wrap; gap: var(--s2); align-items: center; margin: var(--s4) 0; }
.scenario-bar button[aria-pressed="true"] { color: var(--bg); background: var(--accent); }
.self-checks { margin-top: var(--s5); border: 1px solid var(--line); border-radius: var(--r); padding: var(--s4); }
.self-checks .pass { color: var(--done); }
.self-checks .fail { color: var(--error); }
.fixture-label { color: var(--ink-3); font-size: var(--fs-xs); letter-spacing: .04em; text-transform: uppercase; }
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { scroll-behavior: auto !important; transition-duration: 0.01ms !important; animation-duration: 0.01ms !important; }
}
```

Keep the main session visually dominant through weight and selected treatment. Do not add a second containment card around current children.

- [ ] **Step 7: Create the temporary jsdom harness**

If `/tmp/serf-jstest-jsdom/node_modules/jsdom` is absent, install it outside the repository:

```bash
mkdir -p /tmp/serf-jstest-jsdom
npm install --prefix /tmp/serf-jstest-jsdom jsdom@26
```

Create `/tmp/serf-subagent-sidebar-check.js` with:

```javascript
"use strict";
const assert = require("assert");
const fs = require("fs");
const path = require("path");
const { JSDOM, VirtualConsole } = require("jsdom");

const file = path.resolve(process.argv[2]);
const virtualConsole = new VirtualConsole();
const consoleErrors = [];
virtualConsole.on("jsdomError", (error) => consoleErrors.push(String(error)));
virtualConsole.on("error", (error) => consoleErrors.push(String(error)));

const dom = new JSDOM(fs.readFileSync(file, "utf8"), {
  runScripts: "dangerously",
  resources: "usable",
  pretendToBeVisual: true,
  url: `file://${file}`,
  virtualConsole,
  beforeParse(window) {
    window.HTMLElement.prototype.scrollIntoView = function () {};
    if (!window.CSS) window.CSS = {};
    if (!window.CSS.escape) window.CSS.escape = (value) => String(value).replace(/[^a-zA-Z0-9_-]/g, "\\$&");
  },
});

setTimeout(() => {
  const api = dom.window.SubagentSidebarMockup;
  assert.ok(api, "inspection surface must exist");
  const results = api.runSelfChecks();
  assert.ok(results.length >= 18, `expected at least 18 checks, got ${results.length}`);
  assert.deepStrictEqual(results.filter((result) => !result.pass), []);
  assert.deepStrictEqual(consoleErrors, []);
  assert.strictEqual(dom.window.document.querySelectorAll('[data-pane-id="main"]').length, 1);
  console.log(`PASS: ${results.length} embedded mockup checks`);
  dom.window.close();
}, 100);
```

The test harness is temporary and must never be staged.

- [ ] **Step 8: Run automated checks**

Run:

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  node /tmp/serf-subagent-sidebar-check.js \
  docs/web-ui/mockups/23-subagent-sidebar.html
```

Expected: `PASS: <N> embedded mockup checks`, where `N >= 18`, with no console errors.

Run the repository's mockup hygiene checks:

```bash
git diff --check
git status --short
```

Expected: no whitespace errors; only `docs/web-ui/mockups/23-subagent-sidebar.html` is modified since the prior mockup commit.

- [ ] **Step 9: Perform the browser acceptance pass**

Open the file directly:

```bash
open docs/web-ui/mockups/23-subagent-sidebar.html
```

At approximately 1280×800 CSS pixels, verify:

1. the main row shows all four current statuses directly;
2. `Inactive subagents (4)` starts collapsed;
3. nested inactive disclosures open independently;
4. the terminal `archive` row preserves its current and terminal descendants;
5. child panes insert immediately right of direct parents;
6. reopening focuses without duplication;
7. closing a parent closes its descendants;
8. all scenario transitions preserve open-pane identity; and
9. the self-check panel reports every check passing.

Resize to approximately 390×844 CSS pixels and verify the sidebar remains usable, panes scroll horizontally, no controls overlap, and keyboard focus remains visible.

- [ ] **Step 10: Remove temporary artifacts and confirm the production diff is empty**

```bash
rm -f /tmp/serf-subagent-sidebar-check.js
git diff --name-only HEAD~2..HEAD -- cmd/serf-hub agent internal hubapi appwire
test -z "$(git diff --name-only HEAD~2..HEAD -- cmd/serf-hub agent internal hubapi appwire)"
```

Expected: the production-path diff is empty.

- [ ] **Step 11: Commit the completed mockup**

```bash
git add docs/web-ui/mockups/23-subagent-sidebar.html
git commit -m "mockup(web): exercise subagent lifecycle states"
```

- [ ] **Step 12: Run final branch verification**

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  node /tmp/serf-subagent-sidebar-check.js \
  docs/web-ui/mockups/23-subagent-sidebar.html
```

If Step 10 removed the harness, recreate it with the exact Step 7 content before this command, then remove it again.

Run:

```bash
git diff --check HEAD~3..HEAD
git status --short --branch
git log -3 --oneline
```

Expected:

- all embedded checks pass;
- no diff-check errors;
- the branch is clean;
- the last three implementation commits correspond to recursive sidebar rendering, parent-relative panes, and lifecycle scenarios; and
- no production code or asset changed.

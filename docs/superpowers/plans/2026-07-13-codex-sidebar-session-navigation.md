# Codex Sidebar Session Navigation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve source-qualified AppWire identity when a user opens a Codex session from the Web UI sidebar while keeping local Serf session URLs unchanged.

**Architecture:** Add one client-side route-identity helper to `sidebar.js`. Every sidebar navigation and active-row lookup derives its URL from that helper, so external refs remain qualified and local refs retain the existing short URL. Exercise the real sidebar code in a deterministic jsdom regression test; rely on existing Go tests for source-qualified workspace dispatch and Codex controls.

**Tech Stack:** Vanilla JavaScript, jsdom, HTMX attributes, Go `net/http`, AppWire, Go test.

## Global Constraints

- Do not change the Codex AppWire adapter, launcher, session capabilities, or controls.
- Do not search sources by an unqualified session ID.
- Do not add a Codex-specific branch; the rule applies to every non-local AppWire source.
- Preserve `/s/<session-id>` for valid `local:<session-id>` refs.
- Missing or malformed refs fall back to the existing bare `session_id` behavior and existing not-found handling.
- Tests must not require credentials, network access, a live Codex binary, wall-clock sleeps, or ambient developer state.
- Do not add `package.json`, `package-lock.json`, or a checked-in jsdom dependency; use `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules`.

## File Structure

- Modify `cmd/serf-hub/assets/sidebar.js`: own canonical sidebar route derivation and use it in row links, menu navigation, active-row matching, and hidden-row reveal.
- Create `cmd/serf-hub/jstest/test-sidebar-session-routes.js`: boot the real sidebar in jsdom and lock local, external, malformed-ref, menu-target, active-row, and reveal behavior.
- No Go production files change; existing handlers already accept source-qualified route IDs.

---

### Task 1: Route sidebar sessions by canonical AppWire identity

**Files:**
- Create: `cmd/serf-hub/jstest/test-sidebar-session-routes.js`
- Modify: `cmd/serf-hub/assets/sidebar.js:14-68, 218-225, 767-839`

**Interfaces:**
- Consumes: tree nodes shaped as `{ ref: string, session_id: string, row_id: string, ... }` from `/api/tree`.
- Produces: `sessionRouteID(node) -> string` and `sessionHref(node) -> string`; local nodes produce `session_id`, valid non-local nodes produce `ref`, and malformed refs produce `session_id`.
- Produces: `window.SerfSidebarInternal.sessionRouteID`, `sessionHref`, `sessionMenuItems`, and `findRevealChain` as deterministic test/inspection surfaces alongside the existing `buildRow` export.

- [ ] **Step 1: Create the failing jsdom regression test**

Create `cmd/serf-hub/jstest/test-sidebar-session-routes.js` with:

```javascript
"use strict";
const assert = require("assert");
const fs = require("fs");
const { JSDOM } = require("jsdom");

const sidebarSrc = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const iconsSrc = fs.readFileSync(__dirname + "/../assets/icons.js", "utf8");

function emptyTree() {
  return {
    needs_you: [], favorites: [], projects: [], archived_projects: [], test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
  };
}

function boot(pathname) {
  const dom = new JSDOM(
    '<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>',
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost" + pathname },
  );
  const w = dom.window;
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(emptyTree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(iconsSrc);
  w.eval(sidebarSrc);
  return w;
}

function node(ref, sessionID, rowID) {
  return {
    row_id: rowID,
    ref,
    session_id: sessionID,
    title: ref,
    state: "idle",
    kind: "session",
    tier: "current",
    updated_at: "2026-07-13T00:00:00Z",
  };
}

const w = boot("/s/codex-local:th_codex");
const I = w.SerfSidebarInternal;
const local = node("local:01LOCAL", "01LOCAL", "project:p:local:01LOCAL");
const codex = node("codex-local:th_codex", "codex-session", "project:p:codex-local:th_codex");
const malformed = node("not a ref", "fallback-id", "project:p:local:fallback-id");

assert.strictEqual(I.sessionRouteID(local), "01LOCAL");
assert.strictEqual(I.sessionRouteID(codex), "codex-local:th_codex");
assert.strictEqual(I.sessionRouteID(malformed), "fallback-id");
assert.strictEqual(I.sessionHref(local), "/s/01LOCAL");
assert.strictEqual(I.sessionHref(codex), "/s/codex-local:th_codex");

const localRow = I.buildRow(local);
assert.strictEqual(localRow.getAttribute("href"), "/s/01LOCAL");
assert.strictEqual(localRow.getAttribute("hx-get"), "/_partials/s/01LOCAL/workspace");
assert.strictEqual(localRow.getAttribute("hx-push-url"), "/s/01LOCAL");

const codexRow = I.buildRow(codex);
assert.strictEqual(codexRow.getAttribute("href"), "/s/codex-local:th_codex");
assert.strictEqual(codexRow.getAttribute("hx-get"), "/_partials/s/codex-local:th_codex/workspace");
assert.strictEqual(codexRow.getAttribute("hx-push-url"), "/s/codex-local:th_codex");

const openItem = I.sessionMenuItems(codex).find((item) => item.label === "Open");
assert.ok(openItem, "Codex row menu must contain Open");
assert.strictEqual(openItem.href, "/s/codex-local:th_codex");

const tree = {
  needs_you: [], favorites: [], archived_projects: [], test_runs: [],
  projects: [{ key: "p", name: "p", working_dir: "/work/p", default_expanded: true, sessions: [codex] }],
  attentionSummary: { needsYou: 0, error: 0, working: 0 },
};
w.SerfSidebar.renderTree(tree);
const rendered = w.document.querySelector('[data-row-id="project:p:codex-local:th_codex"]');
assert.ok(rendered, "Codex row must render");
assert.ok(rendered.hasAttribute("data-active"), "qualified Codex URL must mark its row active");
assert.deepStrictEqual(Array.from(I.findRevealChain(tree, "codex-local:th_codex")), ["p"]);
assert.strictEqual(I.findRevealChain(tree, "th_codex"), null, "bare external thread ID must not match");

console.log("PASS: sidebar session routes preserve source identity");
process.exit(0);
```

The menu descriptor's `href` is an intentional inspectable value; its `run` callback will use that same value in Step 3.

- [ ] **Step 2: Run the focused test and confirm the red state**

Run:

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  node cmd/serf-hub/jstest/test-sidebar-session-routes.js
```

Expected: FAIL because `SerfSidebarInternal.sessionRouteID` and the other new inspection surfaces do not exist; current Codex row URLs also use `/s/codex-session`.

- [ ] **Step 3: Add the minimal canonical-route implementation**

In `cmd/serf-hub/assets/sidebar.js`, replace the current `SerfSidebarInternal` assignment and add these helpers near `rowKey`:

```javascript
  window.SerfSidebarInternal = {
    buildRow: buildRow,
    stateIconKey: stateIconKey,
    stateWord: stateWord,
    buildRollupBadge: buildRollupBadge,
    sessionRouteID: sessionRouteID,
    sessionHref: sessionHref,
    sessionMenuItems: sessionMenuItems,
    findRevealChain: findRevealChain,
  }; // test/inspection surface

  function rowKey(n) { return n.row_id; }

  function parseSessionRef(raw) {
    if (typeof raw !== "string" || !/^[A-Za-z0-9._~:-]+$/.test(raw)) return null;
    var split = raw.indexOf(":");
    if (split <= 0 || split === raw.length - 1) return null;
    var sessionID = raw.slice(split + 1);
    if (sessionID.indexOf("..") !== -1) return null;
    return { hostID: raw.slice(0, split), sessionID: sessionID };
  }

  function sessionRouteID(n) {
    var fallback = n && typeof n.session_id === "string" ? n.session_id : "";
    var parsed = parseSessionRef(n && n.ref);
    if (!parsed || parsed.hostID === "local") return fallback;
    return n.ref;
  }

  function sessionHref(n) { return "/s/" + sessionRouteID(n); }
  function sessionWorkspaceHref(n) { return "/_partials/s/" + sessionRouteID(n) + "/workspace"; }
```

Update `buildRow` so all three navigation attributes share the helper:

```javascript
    var href = sessionHref(n);
    a.setAttribute("href", href);
    a.setAttribute("hx-get", sessionWorkspaceHref(n));
    a.setAttribute("hx-target", "#workspace");
    a.setAttribute("hx-swap", "innerHTML");
    a.setAttribute("hx-push-url", href);
```

Update `sessionMenuItems` so **Open** exposes and uses the same computed target:

```javascript
  function sessionMenuItems(n) {
    var openHref = sessionHref(n);
    return [
      { label: "Open", href: openHref, run: function () { window.location.href = openHref; } },
      { label: "Open beside", run: function () { if (window.SerfPanes) window.SerfPanes.open("/thread/" + encodeURIComponent(n.ref), n.title); } },
      { label: n.favorite ? "Unfavorite" : "Favorite", run: function () { window.SerfSidebar.favorite(n.ref, !n.favorite); } },
      { label: "Rename", hidden: !n.rename, run: function () { startInlineRename(n); } },
      { label: n.tier === "archived" ? "Unarchive" : "Archive", run: function () { window.SerfSidebar.archive(n.ref, n.tier !== "archived"); } },
    ];
  }
```

Update active-row reveal to pass the route ID, not an assumed bare session ID:

```javascript
    var chain = findRevealChain(model.tree, m[1]);
```

Keep that call unchanged, but rename the lookup parameters and compare through `sessionRouteID`:

```javascript
  function findRevealChain(tree, routeID) {
    return (
      searchNodes(tree.needs_you, routeID, []) ||
      searchNodes(tree.favorites, routeID, []) ||
      searchProjects(tree.projects, routeID, []) ||
      searchProjects(tree.archived_projects, routeID, [SECTION_ARCHIVED]) ||
      searchProjects(tree.test_runs, routeID, [SECTION_TEST_RUNS]) ||
      null
    );
  }
  function searchProjects(projects, routeID, prefix) {
    for (var i = 0; i < (projects || []).length; i++) {
      var p = projects[i];
      var found = searchNodes(p.sessions, routeID, prefix.concat([p.key]));
      if (found) return found;
    }
    return null;
  }
  function searchNodes(nodes, routeID, chain) {
    for (var i = 0; i < (nodes || []).length; i++) {
      var n = nodes[i];
      if (sessionRouteID(n) === routeID) return chain;
      if (n.kind === "cluster") {
        var cm = searchNodes(n.children, routeID, chain.concat([n.row_id]));
        if (cm) return cm;
      } else if (n.children && n.children.length) {
        var cc = searchNodes(n.children, routeID, chain.concat([childrenKey(n)]));
        if (cc) return cc;
      }
    }
    return null;
  }
```

Update the nearby comments from `session_id`/`sessionId` terminology to `canonical route ID`/`routeID` so they describe both local and external rows.

- [ ] **Step 4: Run the focused test and confirm the green state**

Run:

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  node cmd/serf-hub/jstest/test-sidebar-session-routes.js
```

Expected:

```text
PASS: sidebar session routes preserve source identity
```

- [ ] **Step 5: Run adjacent sidebar regressions**

Run:

```bash
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  node cmd/serf-hub/jstest/test-sidebar-row-layout.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  node cmd/serf-hub/jstest/test-sidebar-menu.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules \
  node cmd/serf-hub/jstest/test-sidebar-model.js
```

Expected: all three exit 0. `test-sidebar-model.js` specifically preserves the existing local deep-link, HTMX, and active-row behavior while the new test covers the qualified external path.

- [ ] **Step 6: Commit the test-first implementation**

```bash
git add cmd/serf-hub/assets/sidebar.js \
  cmd/serf-hub/jstest/test-sidebar-session-routes.js
git commit -m "fix(web): preserve Codex sidebar session refs"
```

Expected: one commit containing only the sidebar implementation and its regression test.

---

### Task 2: Verify source-qualified workspace dispatch and the full hub surface

**Files:**
- Verify only: `cmd/serf-hub/web_test.go`
- Verify only: `cmd/serf-hub/jstest/run-all.sh`

**Interfaces:**
- Consumes: Task 1's `/s/<source>:<thread-id>` and `/_partials/s/<source>:<thread-id>/workspace` URLs.
- Verifies: existing `handleSessionPartial`, workspace loading, configured/managed Codex source dispatch, and AppWire capability rendering accept those URLs without production changes.
- Produces: recorded command output for final completion evidence; no code changes are expected.

- [ ] **Step 1: Run focused source-qualified Go tests**

Run:

```bash
go test ./cmd/serf-hub \
  -run 'TestWeb_(CodexSessionRouteReadsConfiguredSource|APITreeIncludesConfiguredCodexSourceThreads|ManagedCodexLiveWorkspaceCapabilitiesEnsureSource)$' \
  -count=1
```

Expected: `ok primeradiant.com/serf/cmd/serf-hub`.

- [ ] **Step 2: Run the complete JavaScript UI suite**

Run:

```bash
cd cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules JSTEST_TIMEOUT=90 sh run-all.sh
cd ../../..
```

Expected: every `test-*.js` row reports `OK` and the suite ends with `jstest: all tests passed`.

- [ ] **Step 3: Run the complete hub package tests**

Run:

```bash
go test ./cmd/serf-hub -count=1
```

Expected: `ok primeradiant.com/serf/cmd/serf-hub`.

- [ ] **Step 4: Inspect repository state and committed diff**

Run:

```bash
git status --short
git show --stat --oneline HEAD
git diff HEAD^ -- cmd/serf-hub/assets/sidebar.js \
  cmd/serf-hub/jstest/test-sidebar-session-routes.js
```

Expected: the implementation commit touches only the two Task 1 files. Pre-existing untracked `.private-journal` files and `docs/superpowers/plans/2026-05-07-serf-daemon-prereqs.md` remain untouched.

- [ ] **Step 5: Record verification without an empty commit**

Do not create a verification-only commit when no files changed. Report the exact commands and pass/fail results in the completion summary.

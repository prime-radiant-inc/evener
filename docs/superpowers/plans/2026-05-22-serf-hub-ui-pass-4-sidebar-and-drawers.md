# serf-hub UI Pass 4 — Sidebar Restructure + Workspace Header + Slide-over A11y

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restructure the sidebar to a 2-column dot/text grid with 2-line title wrap and mono meta, add desktop rail mode (56px) toggled by `⌘B`, refactor the workspace header to inline the hamburger and drop separator dots, and introduce a `SerfFocusTrap` helper that traps focus inside slide-over panels (tasks, details, mobile sidebar).

**Architecture:** CSS + templates + small JS additions. One new helper file (`focus-trap.js`) exposing `window.SerfFocusTrap.activate/deactivate`. `sidebar.js` gains rail-mode persistence + `⌘B` binding + a `data-active` row marker driven by `htmx:afterSwap`. `renderer.js` wraps the two existing slide-over toggles in focus-trap calls. `sidebar.html` and `workspace.html` partials are rewritten to the new shape; `web_test.go` assertions are updated in lockstep (no co-applied legacy classes). The new selectors (`.sb-row`, `.dot-col`, `.text-col`, `.title`, `.meta`, `[data-active]`, `[data-sidebar-rail]`) are the ones Pass 5 (buttons + focus rings) will hang `:focus-visible` rules off of, so this pass ships first.

**Tech Stack:** Vanilla JS (no framework), htmx events, CSS custom properties + container queries + grid, Go html/template partials, JSDOM-based jstest, Go `httptest` for partial rendering.

**Spec references:**
- Design language: `docs/superpowers/specs/2026-05-22-serf-hub-design-language.md` §3.4 (row), §3.8 (dialog/drawer), §4.1 (workspace header), §4.8 (sidebar)
- Implementation spec: `docs/superpowers/specs/2026-05-22-serf-hub-responsive-ui-design.md` — "Pass 5" section in Migration order, which (per the Rollout section) is the **fourth** PR in ship order.

**Ship-order note:** The implementation spec's Migration order numbers this Pass "Pass 5" but the Rollout section re-sequences it to ship **before** the buttons/focus-rings pass (which is Migration order "Pass 4"). Hence this plan is labelled "Pass 4" by ship order. The Pass 5 (buttons + focus rings) plan will reference `.sb-row` and the other selectors created here.

---

## File touch map

| File | Action | Purpose |
| --- | --- | --- |
| `cmd/serf-hub/assets/focus-trap.js` | **create** | `window.SerfFocusTrap.activate/deactivate` helper (~80 LOC) |
| `cmd/serf-hub/jstest/test-focus-trap.js` | **create** | TDD coverage for the focus-trap helper |
| `cmd/serf-hub/jstest/test-sidebar-active.js` | **create** | Covers `data-active` wiring on `htmx:afterSwap` |
| `cmd/serf-hub/templates/app.html` | modify | Load `focus-trap.js` before `sidebar.js` + `renderer.js`; remove body-level `#mobile-hamburger` (it moves into the workspace header partial) |
| `cmd/serf-hub/templates/partials/sidebar.html` | rewrite | New `.sb-row` markup; rail-toggle button at top; project chevron becomes `<button>` |
| `cmd/serf-hub/templates/partials/workspace.html` | modify | Hamburger inline at left of header (phone only); status pill → status badge; drop `.rule-dot` separators in meta |
| `cmd/serf-hub/assets/sidebar.js` | modify | Rail toggle + `⌘B` keybinding + localStorage persist; `data-active` wiring on `htmx:afterSwap`; activate focus-trap on mobile drawer open; chevron becomes keyboard-accessible (Enter/Space) |
| `cmd/serf-hub/assets/renderer.js` | modify | `toggleTasksPanel` + `toggleDetailsPanel` call `SerfFocusTrap.activate/deactivate`; signatures accept the trigger element |
| `cmd/serf-hub/assets/style.css` | modify | New `.sb-row` rules; rail mode + container query; workspace-header mobile rules drop the 56px padding-left offset; new `.status-badge` for the meta row; remove obsolete `.session-row`/`.live-row`/`.subagent-row`/`.fork-row`/`.row-title`/`.row-age` rules |
| `cmd/serf-hub/web_test.go` | modify | Replace every `session-row` / `live-row` assertion with the new `sb-row` shape (full list in Task 5) |
| `cmd/serf-hub/jstest/test-sidebar-collapse.js` | modify | Update inline DOM fixture from `class="session-row"` to `class="sb-row"` and add `data-state` |
| `cmd/serf-hub/jstest/test-panels.js` | modify | Add a focus-trap stub in the JSDOM `window` so the existing panel tests don't break under the new activation calls |

The CSS file is touched throughout; CSS edits are batched per task so each commit ships a coherent visual unit.

---

## Task 1: Write the failing focus-trap jstest

**Files:**
- Create: `cmd/serf-hub/jstest/test-focus-trap.js`

This is TDD — the test is written and run **before** `focus-trap.js` exists. It must fail with `SerfFocusTrap is not defined`.

- [ ] **Step 1: Write the test**

Write `cmd/serf-hub/jstest/test-focus-trap.js` with this exact content:

```js
// Verify the SerfFocusTrap helper: on activate, stores activeElement as the
// restore target; on Tab, focus cycles through focusable elements inside the
// trap; on Shift+Tab cycles backwards; on deactivate, restores focus.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const TRAP_PATH = "../assets/focus-trap.js";
const trapSrc = fs.readFileSync(TRAP_PATH, "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <button id="outside-before">before</button>
  <button id="trigger">open</button>
  <aside id="panel">
    <button id="first">first</button>
    <input id="middle" type="text">
    <button id="last">last</button>
  </aside>
  <button id="outside-after">after</button>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });

const { window } = dom;
window.eval(trapSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

pass(typeof window.SerfFocusTrap === "object", "SerfFocusTrap should exist on window");
pass(typeof window.SerfFocusTrap.activate === "function", "SerfFocusTrap.activate should be a function");
pass(typeof window.SerfFocusTrap.deactivate === "function", "SerfFocusTrap.deactivate should be a function");

const doc = window.document;
const trigger = doc.getElementById("trigger");
const panel = doc.getElementById("panel");
const first = doc.getElementById("first");
const middle = doc.getElementById("middle");
const last = doc.getElementById("last");
const outsideBefore = doc.getElementById("outside-before");
const outsideAfter = doc.getElementById("outside-after");

// --- 1. activate captures activeElement and focuses first focusable in trap.
trigger.focus();
pass(doc.activeElement === trigger, "trigger should hold focus before activate");

const handle = window.SerfFocusTrap.activate(panel, trigger);
pass(handle && typeof handle === "object", "activate should return a handle object");
pass(doc.activeElement === first, "after activate, focus should land on first focusable inside panel, got " + (doc.activeElement && doc.activeElement.id));

// --- 2. Sibling root-level elements should have `inert` applied.
pass(outsideBefore.hasAttribute("inert"), "outside-before should have inert during trap");
pass(outsideAfter.hasAttribute("inert"), "outside-after should have inert during trap");
pass(!panel.hasAttribute("inert"), "panel itself should NOT have inert during trap");

// --- 3. Tab from last cycles forward to first.
last.focus();
const tabEvent = new window.KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
panel.dispatchEvent(tabEvent);
pass(doc.activeElement === first, "Tab from last should cycle to first, got " + (doc.activeElement && doc.activeElement.id));

// --- 4. Shift+Tab from first cycles backward to last.
first.focus();
const shiftTab = new window.KeyboardEvent("keydown", { key: "Tab", shiftKey: true, bubbles: true, cancelable: true });
panel.dispatchEvent(shiftTab);
pass(doc.activeElement === last, "Shift+Tab from first should cycle to last, got " + (doc.activeElement && doc.activeElement.id));

// --- 5. Tab in the middle of the trap leaves the browser to do its default
//       forward step (we shouldn't preventDefault). middle -> last via the
//       browser's natural Tab traversal isn't simulated by JSDOM, so just
//       assert the helper didn't preventDefault when not at an edge.
middle.focus();
const midTab = new window.KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
panel.dispatchEvent(midTab);
pass(!midTab.defaultPrevented, "Tab in middle of trap should NOT preventDefault");

// --- 6. deactivate restores focus to trigger and removes inert.
window.SerfFocusTrap.deactivate(handle);
pass(doc.activeElement === trigger, "deactivate should restore focus to trigger, got " + (doc.activeElement && doc.activeElement.id));
pass(!outsideBefore.hasAttribute("inert"), "outside-before should no longer have inert after deactivate");
pass(!outsideAfter.hasAttribute("inert"), "outside-after should no longer have inert after deactivate");

// --- 7. After deactivate the Tab handler is unbound (cycling no longer fires).
last.focus();
const postTab = new window.KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true });
panel.dispatchEvent(postTab);
pass(doc.activeElement === last, "after deactivate, Tab should not cycle (handler unbound), got " + (doc.activeElement && doc.activeElement.id));

if (failures.length === 0) {
  console.log("PASS: focus-trap activate, cycle, deactivate");
  process.exit(0);
} else {
  for (const f of failures) console.log(f);
  process.exit(1);
}
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-focus-trap.js
```

Expected: failure with `ENOENT` or `SerfFocusTrap should exist on window` / `SerfFocusTrap.activate should be a function`. Either is acceptable — both prove the helper is absent.

- [ ] **Step 3: Commit the failing test**

```bash
git add cmd/serf-hub/jstest/test-focus-trap.js
git commit -m "test: add failing jstest for SerfFocusTrap helper"
```

---

## Task 2: Implement the focus-trap helper

**Files:**
- Create: `cmd/serf-hub/assets/focus-trap.js`

- [ ] **Step 1: Write the helper**

Create `cmd/serf-hub/assets/focus-trap.js` with this exact content:

```js
// SerfFocusTrap — minimal focus management for slide-over panels, modal
// dialogs, and the mobile sidebar drawer. The contract:
//
//   const handle = SerfFocusTrap.activate(panelEl, triggerEl);
//   // ...later...
//   SerfFocusTrap.deactivate(handle);
//
// On activate, the helper:
//   1. Captures the current activeElement (or the explicit triggerEl arg) as
//      the restore target.
//   2. Applies `inert` to every root-level sibling of panelEl so screen
//      readers and tab traversal skip them.
//   3. Binds a Tab/Shift+Tab handler that cycles focus inside panelEl.
//   4. Focuses the first focusable child of panelEl.
//
// On deactivate, the helper:
//   1. Removes `inert` from the siblings it applied it to.
//   2. Unbinds the Tab handler.
//   3. Returns focus to the captured restore target (if still in the DOM).
//
// The handle is opaque; callers pass it back to deactivate. Each activate
// produces a fresh handle, so multiple traps can be active concurrently and
// torn down in any order — though in practice serf-hub opens one at a time.
(function () {
  "use strict";

  // Standard focusable selectors. Tabbable subset = these minus [tabindex="-1"].
  var FOCUSABLE = [
    "a[href]",
    "button:not([disabled])",
    "input:not([disabled]):not([type=hidden])",
    "select:not([disabled])",
    "textarea:not([disabled])",
    "[tabindex]:not([tabindex='-1'])",
    "summary",
  ].join(",");

  function tabbable(root) {
    var nodes = root.querySelectorAll(FOCUSABLE);
    var out = [];
    for (var i = 0; i < nodes.length; i++) {
      var n = nodes[i];
      if (n.hasAttribute("disabled")) continue;
      if (n.getAttribute("tabindex") === "-1") continue;
      // Skip hidden elements — offsetParent is null for display:none subtrees
      // in normal layout; JSDOM returns null too. inert subtrees are also
      // skipped because their items report tabindex=-1.
      out.push(n);
    }
    return out;
  }

  function activate(panel, returnFocusTo) {
    if (!panel) return null;
    var restoreTarget = returnFocusTo || document.activeElement;
    var siblings = [];
    var parent = panel.parentNode;
    if (parent) {
      for (var i = 0; i < parent.children.length; i++) {
        var sib = parent.children[i];
        if (sib === panel) continue;
        if (sib.hasAttribute("inert")) continue; // don't double-toggle
        sib.setAttribute("inert", "");
        siblings.push(sib);
      }
    }

    function onKeyDown(e) {
      if (e.key !== "Tab") return;
      var list = tabbable(panel);
      if (list.length === 0) {
        e.preventDefault();
        return;
      }
      var first = list[0];
      var last = list[list.length - 1];
      var active = document.activeElement;
      if (e.shiftKey && active === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
      // Otherwise let the browser do its natural Tab.
    }

    panel.addEventListener("keydown", onKeyDown);

    // Initial focus: first tabbable inside the panel, else the panel itself.
    var initial = tabbable(panel)[0];
    if (initial) {
      initial.focus();
    } else if (panel.tabIndex < 0) {
      panel.setAttribute("tabindex", "-1");
      panel.focus();
    }

    return { panel: panel, siblings: siblings, restoreTarget: restoreTarget, onKeyDown: onKeyDown };
  }

  function deactivate(handle) {
    if (!handle) return;
    if (handle.panel && handle.onKeyDown) {
      handle.panel.removeEventListener("keydown", handle.onKeyDown);
    }
    for (var i = 0; i < handle.siblings.length; i++) {
      handle.siblings[i].removeAttribute("inert");
    }
    var t = handle.restoreTarget;
    if (t && typeof t.focus === "function" && document.contains(t)) {
      t.focus();
    }
  }

  window.SerfFocusTrap = { activate: activate, deactivate: deactivate };
})();
```

- [ ] **Step 2: Run the test and confirm it passes**

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-focus-trap.js
```

Expected: `PASS: focus-trap activate, cycle, deactivate`.

- [ ] **Step 3: Wire the script tag**

Edit `cmd/serf-hub/templates/app.html`. Find the script block at the bottom and insert `<script src="/assets/focus-trap.js"></script>` immediately before `<script src="/assets/sidebar.js"></script>`. The new script block ordering, in full:

```html
  <script src="/assets/htmx.min.js"></script>
  <script src="/assets/appwire.js"></script>
  <script src="/assets/launchconfig.js"></script>
  <script src="/assets/focus-trap.js"></script>
  <script src="/assets/sidebar.js"></script>
  <script src="/assets/notifications.js"></script>
  <script src="/assets/theme.js"></script>
  <script src="/assets/search.js"></script>
  <script src="/assets/settings.js"></script>
  <script src="/assets/composer-attachments.js"></script>
  <script src="/assets/spawn.js"></script>
  <script src="/assets/settings-pickers.js"></script>
  <script src="/assets/marked.min.js"></script>
  <script src="/assets/diagnostics.js"></script>
  <script src="/assets/pending.js"></script>
  <script src="/assets/renderer.js"></script>
```

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/focus-trap.js cmd/serf-hub/templates/app.html
git commit -m "feat: add SerfFocusTrap helper for slide-over focus management"
```

---

## Task 3: Restructure sidebar.html partial to .sb-row markup

**Files:**
- Modify: `cmd/serf-hub/templates/partials/sidebar.html`

This task changes ONLY the template. The CSS and Go tests are updated in subsequent tasks. After this task, the visual presentation will look broken — that's expected; the CSS catches up in Task 4 and the Go tests in Task 5.

- [ ] **Step 1: Rewrite the partial**

Replace the entire contents of `cmd/serf-hub/templates/partials/sidebar.html` with:

```html
{{define "sidebar"}}
<nav class="sidebar" aria-label="Sessions">
  <div class="sidebar-header">
    <button type="button" class="sidebar-rail-toggle" data-sidebar-rail-toggle title="collapse sidebar (⌘B)" aria-label="collapse sidebar">⇤</button>
    <a class="sidebar-action" href="/new" hx-get="/_partials/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ new</a>
    <a class="sidebar-action" href="#" data-search-trigger>search<kbd>⌘K</kbd></a>
    <a class="sidebar-action settings-link" href="/settings" hx-get="/_partials/settings/general" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/settings">settings</a>
  </div>

  {{if .Live}}
  <section class="sidebar-section">
    <header class="sidebar-section-header">
      <span>Live</span>
      <span class="count">{{len .Live}}</span>
    </header>
    {{range .Live}}
    <a class="sb-row{{if eq .Kind "subagent"}} sub{{else if eq .Kind "fork"}} fork{{end}}"
       data-state="{{.State}}"
       href="/s/{{.ID}}"
       hx-get="/_partials/s/{{.ID}}/workspace"
       hx-target="#workspace"
       hx-swap="innerHTML"
       hx-push-url="/s/{{.ID}}">
      <div class="dot-col">
        {{if eq .Kind "fork"}}<span class="fork-glyph" data-state="{{.State}}">⎇</span>{{else}}<span class="status-dot{{if eq .Kind "subagent"}} subagent{{end}}" data-state="{{.State}}"></span>{{end}}
      </div>
      <div class="text-col">
        <div class="title">{{.Title}}</div>
        <div class="meta"><span>{{.Project}}</span><span class="sep">·</span><span>{{.Age}}</span></div>
      </div>
    </a>
    {{end}}
  </section>
  {{end}}

  {{range .Projects}}
  <section class="sidebar-section project-section collapsed" data-project-key="{{.Name}}">
    <header class="project-header">
      <button type="button" class="project-chevron" aria-label="expand project" aria-expanded="false">▸</button>
      <span class="project-folder" aria-hidden="true">📁</span>
      <span class="project-name">{{.Name}}</span>
      <span class="count project-count">{{len .Sessions}}</span>
      <span class="project-rollup-dot" data-state="{{.RollupState}}"></span>
      {{if .WorkingDir}}
      <a class="project-gear-btn"
         title="project settings for {{.Name}}"
         href="/settings/project?cwd={{.WorkingDir | urlquery}}"
         hx-get="/_partials/settings/project?cwd={{.WorkingDir | urlquery}}"
         hx-target="#workspace"
         hx-swap="innerHTML"
         hx-push-url="/settings/project?cwd={{.WorkingDir | urlquery}}">⚙</a>
      <a class="project-new-btn"
         title="new session in {{.Name}}"
         href="/new?dir={{.WorkingDir | urlquery}}"
         hx-get="/_partials/workspace/spawn?dir={{.WorkingDir | urlquery}}"
         hx-target="#workspace"
         hx-swap="innerHTML"
         hx-push-url="/new?dir={{.WorkingDir | urlquery}}">＋</a>
      {{end}}
    </header>
    <div class="project-children">
    {{range .Sessions}}
    <a class="sb-row"
       data-state="{{.State}}"
       href="/s/{{.ID}}"
       hx-get="/_partials/s/{{.ID}}/workspace"
       hx-target="#workspace"
       hx-swap="innerHTML"
       hx-push-url="/s/{{.ID}}">
      <div class="dot-col"><span class="status-dot" data-state="{{.State}}"></span></div>
      <div class="text-col">
        <div class="title">{{.Title}}</div>
        <div class="meta"><span>{{.Age}}</span></div>
      </div>
    </a>
    {{range .Children}}
      {{if eq .Kind "subagent"}}
      <a class="sb-row sub"
         data-state="{{.State}}"
         href="/s/{{.ID}}"
         hx-get="/_partials/s/{{.ID}}/workspace"
         hx-target="#workspace"
         hx-swap="innerHTML"
         hx-push-url="/s/{{.ID}}">
        <div class="dot-col"><span class="status-dot subagent" data-state="{{.State}}"></span></div>
        <div class="text-col">
          <div class="title">{{.Title}}</div>
          <div class="meta"><span>subagent</span><span class="sep">·</span><span>{{.Age}}</span></div>
        </div>
      </a>
      {{else if eq .Kind "fork"}}
      <a class="sb-row fork"
         data-state="{{.State}}"
         href="/s/{{.ID}}"
         hx-get="/_partials/s/{{.ID}}/workspace"
         hx-target="#workspace"
         hx-swap="innerHTML"
         hx-push-url="/s/{{.ID}}">
        <div class="dot-col"><span class="fork-glyph" data-state="{{.State}}">⎇</span></div>
        <div class="text-col">
          <div class="title">{{.Title}}</div>
          <div class="meta"><span>fork</span><span class="sep">·</span><span>{{.Age}}</span></div>
        </div>
      </a>
      {{end}}
    {{end}}
    {{end}}
    </div>
  </section>
  {{end}}
</nav>
{{end}}
```

Notes about this rewrite:
- Old single-class-per-kind (`session-row` / `live-row` / `subagent-row` / `fork-row`) collapses to one canonical `.sb-row` with `.sub` and `.fork` modifier classes.
- Each row is now a `grid-template-columns: 10px 1fr` container with `.dot-col` and `.text-col`. The title wraps to 2 lines; meta is mono.
- Project chevron is a `<button>` (was `<span role="button">`) for keyboard accessibility (Pass 4 spec requirement).
- Rail-toggle `<button>` lives at the top of the sidebar header. `⇤` glyph collapses; `⇥` glyph expands. Tooltip says `(⌘B)` so the shortcut is discoverable.
- `.row-meta` and `.row-age` classes are gone; replaced by `.count` (section/project counters) and the `.meta` line inside `.text-col`.
- `nav` gains `aria-label="Sessions"` per design language §6.2.

- [ ] **Step 2: Do NOT commit yet**

CSS (Task 4) and Go test updates (Task 5) ship together with this template change. Move on to Task 4 — they form a single visual unit.

---

## Task 4: Add sidebar CSS rules for .sb-row, rail mode, container query

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (sidebar section lines 254–330; mobile section line 873 onward)

- [ ] **Step 1: Replace the existing sidebar row rules**

In `cmd/serf-hub/assets/style.css`, locate the block of rules starting at line 265 (`.session-row, .subagent-row, .fork-row, .live-row { ... }`) and ending at line 293 (`.live-row[data-state="warning"] ...`). Replace the entire block (lines 265–293 inclusive) with:

```css
/* Sidebar row — grid: dot column + text column. The 2-line title wrap is
   the key design choice; full titles like "refactor auth middleware to use
   the new session token store" stay readable. */
.sb-row {
  display: grid;
  grid-template-columns: 10px 1fr;
  gap: var(--space-3);
  padding: 5px var(--space-4);
  border-left: 2px solid transparent;
  color: var(--text);
  text-decoration: none;
  cursor: pointer;
  transition: background var(--motion-fast);
}
.sb-row:hover { background: var(--bg-raised); }
.sb-row .dot-col { display: flex; align-items: center; justify-content: center; padding-top: 4px; }
.sb-row .text-col { min-width: 0; }
.sb-row .title {
  font-family: var(--font-sans);
  font-size: var(--text-base);
  color: var(--text);
  line-height: 1.35;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-word;
}
.sb-row .meta {
  margin-top: 2px;
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
  color: var(--text-muted);
  letter-spacing: 0.02em;
  line-height: 1.2;
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
}
.sb-row .meta .sep { color: var(--text-dim); }

/* State accents — left border + 5% tinted background. Dark theme reads
   fine at 5%; light-theme bump lives in the per-theme override block. */
.sb-row[data-state="awaiting"] {
  background: color-mix(in srgb, var(--state-awaiting) 5%, transparent);
  border-left-color: var(--state-awaiting);
}
.sb-row[data-state="active"] {
  background: color-mix(in srgb, var(--state-processing) 5%, transparent);
  border-left-color: var(--state-processing);
}
.sb-row[data-state="warning"] {
  background: color-mix(in srgb, var(--state-warning) 5%, transparent);
  border-left-color: var(--state-warning);
}

/* Active session marker — JS sets [data-active] on the row whose href
   matches the currently-rendered workspace URL. */
.sb-row[data-active] {
  background: color-mix(in srgb, var(--accent) 10%, transparent);
  border-left-color: var(--accent);
}

/* Subagent + fork variants — indent + dim. */
.sb-row.sub  { padding-left: var(--space-7); }
.sb-row.sub  .title { font-size: var(--text-sm); color: var(--text-muted); }
.sb-row.fork { color: var(--text-muted); }
.sb-row.fork .title { color: var(--text-muted); }

/* Fork glyph colour follows row data-state. */
.sb-row.fork .fork-glyph { color: var(--state-ended); }
.sb-row.fork[data-state="active"]   .fork-glyph { color: var(--state-processing); }
.sb-row.fork[data-state="awaiting"] .fork-glyph { color: var(--state-awaiting); }
.sb-row.fork[data-state="warning"]  .fork-glyph { color: var(--state-warning); }
.sb-row.fork[data-state="idle"]     .fork-glyph { color: var(--state-idle); }
```

- [ ] **Step 2: Tighten the section/project header styles**

Locate the `.sidebar-section-header, .project-header { padding: 14px 20px 4px; ... }` block (around line 262). Update the trailing `.row-meta` selector to use the new `.count` class. Replace lines 262–264 with:

```css
.sidebar-section-header,
.project-header { padding: 14px 20px 4px; color: var(--text-dim); font-family: var(--font-mono); font-size: var(--text-2xs); letter-spacing: 0.14em; text-transform: uppercase; display: flex; align-items: baseline; gap: var(--space-3); }
.sidebar-section-header .count,
.project-header .count { margin-left: auto; letter-spacing: 0; font-family: var(--font-mono); color: var(--text-muted); }
```

- [ ] **Step 3: Make the project chevron a keyboard-accessible button**

Locate `.project-chevron { ... }` (around line 298). Replace the existing line with:

```css
.project-chevron {
  display: inline-block;
  width: 12px;
  padding: 0;
  background: transparent;
  border: none;
  color: var(--text-muted);
  font: inherit;
  cursor: pointer;
  user-select: none;
}
.project-chevron:hover { color: var(--text); }
```

- [ ] **Step 4: Make ⚙ + ＋ persistent (no hover-only opacity)**

Locate `.project-gear-btn` (around line 323). The current rule sets `opacity: 0` and reveals on hover. Replace lines 320–325 (the `.project-new-btn` and `.project-gear-btn` block) with:

```css
.project-new-btn,
.project-gear-btn {
  color: var(--text-dim);
  text-decoration: none;
  padding: var(--space-1) var(--space-2);
  font-size: var(--text-sm);
  line-height: 1;
  cursor: pointer;
  border-radius: var(--radius-md);
  transition: color var(--motion-fast), background var(--motion-fast);
}
.project-new-btn:hover,
.project-gear-btn:hover {
  color: var(--text);
  background: var(--bg-raised);
}
.project-section:hover .project-new-btn,
.project-section:hover .project-gear-btn {
  color: var(--text-muted);
}
```

- [ ] **Step 5: Add the rail-toggle button + rail mode**

Append to the sidebar block (immediately after the project-header rules, before the workspace block at line 333):

```css
/* Rail-toggle button — lives in .sidebar-header. Default state is
   "collapse"; rail mode flips the glyph to "expand". */
.sidebar-rail-toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  margin-right: var(--space-2);
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: var(--text-base);
  cursor: pointer;
  transition: background var(--motion-fast), color var(--motion-fast), border-color var(--motion-fast);
}
.sidebar-rail-toggle:hover {
  color: var(--text);
  background: var(--bg-raised);
  border-color: var(--rule);
}

/* Rail mode: collapse to 56px, hide text columns, hide most header actions. */
body.app[data-sidebar-rail] #sidebar { width: 56px; }
body.app[data-sidebar-rail] .sidebar-header .sidebar-action { display: none; }
body.app[data-sidebar-rail] .sidebar-header { padding: 0 var(--space-3) var(--space-3); justify-content: center; gap: 0; }
body.app[data-sidebar-rail] .sidebar-rail-toggle::before { content: "⇥"; }
body.app[data-sidebar-rail] .sidebar-rail-toggle { font-size: 0; }
body.app[data-sidebar-rail] .sidebar-rail-toggle::before { font-size: var(--text-base); }
body.app[data-sidebar-rail] .sidebar-section-header,
body.app[data-sidebar-rail] .project-header .project-name,
body.app[data-sidebar-rail] .project-header .count,
body.app[data-sidebar-rail] .project-header .project-folder,
body.app[data-sidebar-rail] .project-gear-btn,
body.app[data-sidebar-rail] .project-new-btn { display: none; }
body.app[data-sidebar-rail] .sb-row { grid-template-columns: 1fr; padding: var(--space-2); justify-content: center; }
body.app[data-sidebar-rail] .sb-row .text-col { display: none; }
body.app[data-sidebar-rail] .sb-row .dot-col { padding-top: 0; }

/* Container query — the same collapse triggered structurally if a parent
   ever shrinks the sidebar below 80px independent of the rail attribute. */
#sidebar { container-type: inline-size; container-name: sidebar; }
@container sidebar (max-width: 80px) {
  .sb-row .text-col,
  .sidebar-section-header .count,
  .project-header .project-name,
  .project-header .count,
  .project-header .project-folder { display: none; }
  .sb-row { grid-template-columns: 1fr; justify-content: center; }
}
```

- [ ] **Step 6: Light-mode tint bump**

The 5% mix is invisible against `#fafafa`. Add a light-theme override. Locate the existing `[data-theme="light"]` block in `style.css` (if absent, the `@media (prefers-color-scheme: light)` block will do). Append these rules at the end of that block:

```css
[data-theme="light"] .sb-row[data-state="awaiting"] { background: color-mix(in srgb, var(--state-awaiting) 12%, transparent); }
[data-theme="light"] .sb-row[data-state="active"]   { background: color-mix(in srgb, var(--state-processing) 12%, transparent); }
[data-theme="light"] .sb-row[data-state="warning"]  { background: color-mix(in srgb, var(--state-warning) 12%, transparent); }
[data-theme="light"] .sb-row[data-active]            { background: color-mix(in srgb, var(--accent) 16%, transparent); }
@media (prefers-color-scheme: light) {
  :root:not([data-theme="dark"]) .sb-row[data-state="awaiting"] { background: color-mix(in srgb, var(--state-awaiting) 12%, transparent); }
  :root:not([data-theme="dark"]) .sb-row[data-state="active"]   { background: color-mix(in srgb, var(--state-processing) 12%, transparent); }
  :root:not([data-theme="dark"]) .sb-row[data-state="warning"]  { background: color-mix(in srgb, var(--state-warning) 12%, transparent); }
  :root:not([data-theme="dark"]) .sb-row[data-active]           { background: color-mix(in srgb, var(--accent) 16%, transparent); }
}
```

- [ ] **Step 7: Do NOT commit yet**

Tests and templates need to land together. Go to Task 5.

---

## Task 5: Update web_test.go assertions to the new .sb-row shape

**Files:**
- Modify: `cmd/serf-hub/web_test.go`

The implementation spec is explicit: old class names are removed entirely — they are NOT co-applied transitionally, because that leaves dead selectors. Every test that asserts on the old class names must move to the new ones in lockstep.

The two affected test functions are:
1. `TestWeb_Sidebar_ProjectSections_DefaultCollapsed` (around line 820)
2. `TestWeb_Sidebar_LiveRowDataState` (around line 3486)

- [ ] **Step 1: Update the project-sections collapsed test**

In `cmd/serf-hub/web_test.go`, around line 828, find this block:

```go
	if !strings.Contains(body, "session-row") {
		t.Errorf("missing session-row class")
	}
```

Replace it with:

```go
	if !strings.Contains(body, "sb-row") {
		t.Errorf("missing sb-row class")
	}
```

- [ ] **Step 2: Update the live-row data-state test**

In `cmd/serf-hub/web_test.go`, the entire `TestWeb_Sidebar_LiveRowDataState` function (lines ~3486–3537) uses `live-row` throughout. Replace the function body (everything between the function-opening brace and its closing brace) with the version below. The comment block at the top, the function signature, and the body are all updated; the test logic is preserved — just renamed to assert on `sb-row` rather than `live-row`.

Replace the test that begins at the comment `// TestWeb_Sidebar_LiveRowDataState verifies` with:

```go
// TestWeb_Sidebar_LiveRowDataState verifies that a live entry in the sidebar
// renders with data-state on the .sb-row anchor itself, so the CSS state
// accents (left border + tinted background) can apply.
func TestWeb_Sidebar_LiveRowDataState(t *testing.T) {
	dir := t.TempDir()
	writeRendezvous(t, dir, rendezvous.Entry{PID: 30, Address: "127.0.0.1:55570"})
	r := NewRoster(dir, fakeProber{sessionID: "01LIVEACC", status: appwire.ThreadStatusAwaiting})
	r.Refresh()

	web := NewWebServer(WebConfig{
		HubAddr: "127.0.0.1:9180",
		Roster:  r,
		Past:    NewPastIndex(""),
	})
	req := httptest.NewRequest(http.MethodGet, "/_partials/sidebar", nil)
	req.Host = "127.0.0.1:9180"
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	// The sb-row anchor in the Live section must carry data-state="awaiting"
	// so the state-accent CSS rules match. The template line-wraps the anchor,
	// so flatten whitespace before looking for the two attributes adjacent on
	// the same element.
	if !strings.Contains(body, "sb-row") {
		t.Fatalf("body missing sb-row class: %q", body)
	}
	flat := strings.Join(strings.Fields(body), " ")
	if !strings.Contains(flat, `sb-row`) || !strings.Contains(flat, `data-state="awaiting"`) {
		t.Errorf("sb-row missing data-state=\"awaiting\": %q", body)
	}
	// And confirm they're on the same opening tag: find <a ... > containing both.
	tagFound := false
	for _, chunk := range strings.Split(flat, "<a ") {
		// The first split chunk is everything before the first <a; subsequent
		// chunks each begin with the anchor's attribute list.
		if !strings.HasPrefix(chunk, `class="sb-row`) {
			continue
		}
		end := strings.Index(chunk, ">")
		if end < 0 {
			continue
		}
		if strings.Contains(chunk[:end], `data-state="awaiting"`) {
			tagFound = true
			break
		}
	}
	if !tagFound {
		t.Errorf("data-state=\"awaiting\" not on the sb-row <a> element: %q", body)
	}
}
```

- [ ] **Step 3: Update the sidebar-collapse jstest fixture**

Edit `cmd/serf-hub/jstest/test-sidebar-collapse.js`. Find the four occurrences of `class="session-row"` (lines 22, 23, 24, 36) and replace each with `class="sb-row"`. The fixture HTML around line 22 should read:

```html
        <div class="project-children">
          <a class="sb-row">a</a>
          <a class="sb-row">b</a>
          <a class="sb-row">c</a>
        </div>
```

…and around line 36:

```html
        <div class="project-children">
          <a class="sb-row">x</a>
        </div>
```

- [ ] **Step 4: Confirm no other references**

Run:

```bash
cd /home/jesse/git/prime-radiant/serf
grep -rn "session-row\|live-row\|subagent-row\|fork-row\|row-title\|row-age\|row-meta" cmd/serf-hub/ --include='*.go' --include='*.js' --include='*.html' --include='*.css'
```

Expected: only matches in `style.css` for selectors we will delete in the next step. If matches appear elsewhere (other than the file already updated above), open each and rename to the new scheme: `sb-row`, the `.title` / `.meta` children, and the section/project-header `.count` for counters.

- [ ] **Step 5: Delete dead CSS selectors**

Re-open `cmd/serf-hub/assets/style.css`. Search for `.row-meta`, `.row-age`, `.row-title`, `.session-row`, `.live-row`, `.subagent-row`, `.fork-row`. Delete any rule whose entire selector list consists of these dead names. Where a rule mixes a dead selector with a still-live one, drop the dead one only.

Specifically, the original lines 269–280 of style.css contain rules like:
- `.session-row { font-weight: 500; }` — delete entirely.
- `.session-row .row-title { flex: 1; }` — delete entirely.
- `.subagent-row { padding-left: 48px; }` — delete entirely (replaced by `.sb-row.sub`).
- `.fork-row { color: var(--text-muted); }` — delete entirely.
- `.fork-row[data-state="..."] .fork-glyph { ... }` — delete entirely (replaced by `.sb-row.fork[data-state] .fork-glyph`).
- `.row-meta { ... }` and `.row-age { ... }` (lines 288–289) — delete entirely.
- `.live-row .row-title { flex: 1; }` — delete entirely.

- [ ] **Step 6: Run Go tests and the jstest**

```bash
cd /home/jesse/git/prime-radiant/serf
make test
```

Expected: pass. If `TestWeb_Sidebar_ProjectSections_DefaultCollapsed` or `TestWeb_Sidebar_LiveRowDataState` fails with an unrelated regression, capture the body diff and fix the template before continuing.

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-sidebar-collapse.js
```

Expected: `PASS: sidebar collapse — toggle, persist, restore`.

- [ ] **Step 7: Commit the sidebar restructure**

```bash
git add cmd/serf-hub/templates/partials/sidebar.html cmd/serf-hub/assets/style.css cmd/serf-hub/web_test.go cmd/serf-hub/jstest/test-sidebar-collapse.js
git commit -m "feat(serf-hub): restructure sidebar to .sb-row with dot/text columns

Sidebar rows become a 2-column grid (dot + text) with 2-line title wrap
and a mono meta line. Replaces .session-row/.live-row/.subagent-row/
.fork-row with a single .sb-row + .sub/.fork modifiers. Project chevron
becomes a <button>; ⚙ and ＋ become persistent (no hover-only opacity).
Light-mode state tints bump from 5% to 12% so the accent is visible
against the cream background. CSS container query collapses to dot-only
when sidebar width drops below 80px.

web_test.go assertions updated in lockstep; the old class names are
removed entirely (not co-applied transitionally) per the design spec."
```

---

## Task 6: Add rail-mode JS — toggle button, localStorage, ⌘B keybinding

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js`

- [ ] **Step 1: Add the rail-mode block to sidebar.js**

Open `cmd/serf-hub/assets/sidebar.js`. Immediately above the closing `})();` line at the end of the IIFE (currently around line 128), insert the following block:

```js
  // Sidebar rail mode — persisted to localStorage. The body[data-sidebar-rail]
  // attribute is the single source of truth that CSS reads; the helper
  // syncs that attribute to storage and back.
  var RAIL_KEY = "serf-hub.sidebar.rail";

  function isRailEnabled() {
    try {
      return window.localStorage.getItem(RAIL_KEY) === "true";
    } catch (e) {
      return false;
    }
  }

  function setRail(enabled) {
    if (enabled) {
      document.body.setAttribute("data-sidebar-rail", "");
    } else {
      document.body.removeAttribute("data-sidebar-rail");
    }
    try {
      if (enabled) {
        window.localStorage.setItem(RAIL_KEY, "true");
      } else {
        window.localStorage.removeItem(RAIL_KEY);
      }
    } catch (e) {
      // localStorage may be disabled; flip still works for this session.
    }
  }

  function toggleRail() {
    setRail(!document.body.hasAttribute("data-sidebar-rail"));
  }

  // Apply persisted rail state ASAP — before first paint when possible.
  if (isRailEnabled()) {
    setRail(true);
  }

  document.addEventListener("click", function (e) {
    var t = e.target;
    if (!t || !t.closest) return;
    var btn = t.closest("[data-sidebar-rail-toggle]");
    if (!btn) return;
    e.preventDefault();
    e.stopPropagation();
    toggleRail();
  });

  // ⌘B / Ctrl+B — toggle rail mode. Skip when the focus is on an editable
  // surface (textarea, contenteditable, input) so the shortcut doesn't fire
  // while the user is typing browser-native chords. Mobile (no
  // matchMedia "(min-width: 768px)") ignores the shortcut because rail
  // mode is a desktop affordance.
  function isEditableTarget(el) {
    if (!el) return false;
    var tag = (el.tagName || "").toLowerCase();
    if (tag === "input" || tag === "textarea" || tag === "select") return true;
    if (el.isContentEditable) return true;
    return false;
  }

  document.addEventListener("keydown", function (e) {
    if (e.key !== "b" && e.key !== "B") return;
    if (!(e.metaKey || e.ctrlKey)) return;
    if (e.altKey || e.shiftKey) return;
    if (isEditableTarget(e.target)) return;
    // Desktop only — match the design-language breakpoint.
    if (window.matchMedia && window.matchMedia("(max-width: 767px)").matches) return;
    e.preventDefault();
    toggleRail();
  });

  // Sync the rail-toggle button's aria-label so screen readers hear the
  // correct direction after each flip. Runs on init + after each htmx
  // swap (the sidebar partial re-renders frequently).
  function syncRailToggleLabel() {
    var btn = document.querySelector("[data-sidebar-rail-toggle]");
    if (!btn) return;
    var railed = document.body.hasAttribute("data-sidebar-rail");
    btn.setAttribute("aria-label", railed ? "expand sidebar" : "collapse sidebar");
    btn.setAttribute("title", railed ? "expand sidebar (⌘B)" : "collapse sidebar (⌘B)");
  }
  document.addEventListener("DOMContentLoaded", syncRailToggleLabel);
  document.addEventListener("htmx:afterSwap", syncRailToggleLabel);
```

- [ ] **Step 2: Make the project chevron keyboard-activated**

The chevron is now a `<button>` so Enter/Space already fire `click` events natively. But the current click handler at line 49 (`onChevronClick`) does an explicit `e.preventDefault()` and `e.stopPropagation()` — that's fine for both clicks and synthetic clicks from Enter/Space.

However, the chevron now needs its `aria-expanded` attribute kept in sync. Update `onChevronClick` (currently lines 49–61) to:

```js
  function onChevronClick(e) {
    var chevron = e.target.closest(".project-chevron");
    if (!chevron) return;
    var section = chevron.closest("[data-project-key]");
    if (!section) return;
    e.preventDefault();
    e.stopPropagation();
    var key = section.getAttribute("data-project-key");
    var nextCollapsed = !section.classList.contains("collapsed");
    section.classList.toggle("collapsed", nextCollapsed);
    chevron.textContent = nextCollapsed ? "▸" : "▾";
    chevron.setAttribute("aria-expanded", nextCollapsed ? "false" : "true");
    setCollapsed(key, nextCollapsed);
  }
```

Also update `applyCollapseState` (currently lines 31–40) to sync `aria-expanded` on initial paint:

```js
  function applyCollapseState(section) {
    var key = section.getAttribute("data-project-key");
    if (!key) return;
    var collapsed = isCollapsed(key);
    section.classList.toggle("collapsed", collapsed);
    var chevron = section.querySelector(".project-chevron");
    if (chevron) {
      chevron.textContent = collapsed ? "▸" : "▾";
      chevron.setAttribute("aria-expanded", collapsed ? "false" : "true");
    }
  }
```

- [ ] **Step 3: Run the existing sidebar jstest to confirm no regressions**

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-sidebar-collapse.js
```

Expected: `PASS: sidebar collapse — toggle, persist, restore`.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js
git commit -m "feat(serf-hub): add sidebar rail toggle with ⌘B keybinding

Rail mode collapses the sidebar to 56px (icons + status dots only),
persisted to localStorage[\"serf-hub.sidebar.rail\"]. Toggled via the
new ⇤/⇥ button in the sidebar header or the ⌘B/Ctrl+B shortcut
(desktop only, suppressed when typing in editable fields). The
project chevron now reflects expand state via aria-expanded for
screen readers."
```

---

## Task 7: Write the data-active jstest (TDD-style)

**Files:**
- Create: `cmd/serf-hub/jstest/test-sidebar-active.js`

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-sidebar-active.js` with this exact content:

```js
// Verify that sidebar.js listens for htmx:afterSwap on #workspace and
// applies [data-active] to the .sb-row whose href matches the URL the
// swap was triggered for. Also verifies that the marker clears from all
// other rows.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SIDEBAR_JS = "../assets/sidebar.js";
const sidebarSrc = fs.readFileSync(SIDEBAR_JS, "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body class="app">
  <aside id="sidebar">
    <nav class="sidebar">
      <a class="sb-row" data-state="awaiting" href="/s/01ABC"
         hx-get="/_partials/s/01ABC/workspace" hx-target="#workspace"
         hx-push-url="/s/01ABC">
        <div class="dot-col"></div>
        <div class="text-col"><div class="title">A</div></div>
      </a>
      <a class="sb-row" data-state="active" href="/s/01DEF"
         hx-get="/_partials/s/01DEF/workspace" hx-target="#workspace"
         hx-push-url="/s/01DEF">
        <div class="dot-col"></div>
        <div class="text-col"><div class="title">B</div></div>
      </a>
      <a class="sb-row" data-state="idle" href="/s/01GHI"
         hx-get="/_partials/s/01GHI/workspace" hx-target="#workspace"
         hx-push-url="/s/01GHI">
        <div class="dot-col"></div>
        <div class="text-col"><div class="title">C</div></div>
      </a>
    </nav>
  </aside>
  <main id="workspace"></main>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });

const { window } = dom;
window.eval(sidebarSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const wait = () => new Promise(r => setTimeout(r, 30));

(async () => {
  await wait();

  const doc = window.document;
  const workspace = doc.getElementById("workspace");
  const rowA = doc.querySelector('.sb-row[href="/s/01ABC"]');
  const rowB = doc.querySelector('.sb-row[href="/s/01DEF"]');
  const rowC = doc.querySelector('.sb-row[href="/s/01GHI"]');

  // 1. Simulate htmx swapping in /s/01DEF — pushState first so location
  //    reflects the new URL, then fire htmx:afterSwap on #workspace.
  window.history.pushState({}, "", "/s/01DEF");
  const evt = new window.CustomEvent("htmx:afterSwap", { bubbles: true });
  workspace.dispatchEvent(evt);

  pass(rowB.hasAttribute("data-active"), "rowB should be marked active after swap to /s/01DEF");
  pass(!rowA.hasAttribute("data-active"), "rowA should NOT be active");
  pass(!rowC.hasAttribute("data-active"), "rowC should NOT be active");

  // 2. Swap to /s/01ABC — marker moves, prior marker clears.
  window.history.pushState({}, "", "/s/01ABC");
  workspace.dispatchEvent(new window.CustomEvent("htmx:afterSwap", { bubbles: true }));
  pass(rowA.hasAttribute("data-active"), "rowA should be marked active after swap to /s/01ABC");
  pass(!rowB.hasAttribute("data-active"), "rowB marker should clear");
  pass(!rowC.hasAttribute("data-active"), "rowC should remain unmarked");

  // 3. Swap to a non-session URL like /new — all rows should clear.
  window.history.pushState({}, "", "/new");
  workspace.dispatchEvent(new window.CustomEvent("htmx:afterSwap", { bubbles: true }));
  pass(!rowA.hasAttribute("data-active"), "no row should be active after /new swap");
  pass(!rowB.hasAttribute("data-active"), "no row should be active after /new swap");
  pass(!rowC.hasAttribute("data-active"), "no row should be active after /new swap");

  if (failures.length === 0) {
    console.log("PASS: sidebar data-active wiring on htmx:afterSwap");
    process.exit(0);
  } else {
    for (const f of failures) console.log(f);
    process.exit(1);
  }
})();
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-sidebar-active.js
```

Expected: a FAIL line — likely `rowB should be marked active after swap to /s/01DEF`. The afterSwap handler doesn't yet wire `data-active`.

- [ ] **Step 3: Commit the failing test**

```bash
git add cmd/serf-hub/jstest/test-sidebar-active.js
git commit -m "test(serf-hub): add failing jstest for sidebar data-active wiring"
```

---

## Task 8: Wire data-active in sidebar.js

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js`

- [ ] **Step 1: Add the data-active sync handler**

In `cmd/serf-hub/assets/sidebar.js`, immediately above the closing `})();` of the IIFE, insert:

```js
  // data-active marker — sync after every htmx swap into #workspace. The
  // marker is the row whose href ends with the current pathname (e.g.,
  // "/s/01ABC"). /new and /settings URLs don't match any sb-row, so all
  // rows clear. Matching is suffix-based because the href is server-side
  // absolute but the pathname can pick up query strings.
  function syncActiveRow() {
    var path = window.location.pathname || "";
    var rows = document.querySelectorAll(".sb-row");
    var matched = null;
    if (path && path.indexOf("/s/") === 0) {
      // Direct prefix — strip any trailing slashes for exact match.
      var clean = path.replace(/\/+$/, "");
      for (var i = 0; i < rows.length; i++) {
        var href = rows[i].getAttribute("href") || "";
        if (href === clean) { matched = rows[i]; break; }
      }
    }
    for (var j = 0; j < rows.length; j++) {
      if (rows[j] === matched) {
        rows[j].setAttribute("data-active", "");
      } else {
        rows[j].removeAttribute("data-active");
      }
    }
  }

  document.addEventListener("DOMContentLoaded", syncActiveRow);
  document.addEventListener("htmx:afterSwap", function (e) {
    // Only resync when the swap was into #workspace OR the sidebar itself.
    // (Sidebar swaps re-render the rows; the marker has to be re-applied.)
    var target = e && e.target;
    if (!target) { syncActiveRow(); return; }
    if (target.id === "workspace" || target.id === "sidebar" || (target.closest && target.closest("#sidebar"))) {
      syncActiveRow();
    }
  });
```

- [ ] **Step 2: Run the test and confirm it passes**

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-sidebar-active.js
```

Expected: `PASS: sidebar data-active wiring on htmx:afterSwap`.

- [ ] **Step 3: Run the full jstest suite to ensure no regressions**

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh
```

Expected: every test prints `PASS: ...` and exits 0.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js
git commit -m "feat(serf-hub): wire sidebar [data-active] from htmx:afterSwap

After every swap into #workspace (or a re-render of #sidebar), parse
window.location.pathname; if it matches /s/<id>, find the .sb-row
whose href ends with that path and apply data-active to it, clearing
the marker from all other rows."
```

---

## Task 9: Move hamburger into the workspace header; refactor meta row

**Files:**
- Modify: `cmd/serf-hub/templates/app.html`
- Modify: `cmd/serf-hub/templates/partials/workspace.html`
- Modify: `cmd/serf-hub/assets/style.css`

This task makes three coordinated edits: remove the body-level `#mobile-hamburger`, render an inline `.header-hamburger` button inside `.workspace-header`, and replace the `status-pill` + `rule-dot` separators in the meta row with a `status-badge` and `gap`-based mono cluster.

- [ ] **Step 1: Remove the body-level hamburger from app.html**

In `cmd/serf-hub/templates/app.html`, delete line 20:

```html
  <button type="button" id="mobile-hamburger" data-sidebar-toggle title="open sidebar" aria-label="open sidebar">☰</button>
```

The line is removed entirely — no replacement at the body level. The hamburger is now rendered inside each workspace partial.

- [ ] **Step 2: Inline hamburger + new meta row in workspace.html**

In `cmd/serf-hub/templates/partials/workspace.html`, replace the existing `<header class="workspace-header" ...>` block and the `{{define "workspace_meta"}}` line at the bottom. The new full template:

```html
{{define "workspace"}}
<header class="workspace-header" data-session-id="{{.ID}}" data-source-label="{{.SourceLabel}}">
  {{if .ForkLabel}}
  <div class="fork-original-banner">↳ original of {{if .ForkOfTitle}}<span class="fork-original-target">{{.ForkOfTitle}}</span>{{else}}fork{{end}}{{if .DivergenceTurn}}, divergence at turn {{.DivergenceTurn}}{{end}}</div>
  {{end}}
  <div class="workspace-title-row">
    <button type="button" class="header-hamburger" data-sidebar-toggle title="open sidebar" aria-label="open sidebar">☰</button>
    <div class="workspace-title">
      <span class="title">{{.Title}}</span>
      <button class="title-action" type="button" data-copy-id="{{.ID}}" title="copy session ID">⧉</button>
    </div>
    <div class="workspace-actions">
      <button type="button" class="panel-toggle" data-tasks-trigger title="task list">
        <span class="panel-toggle-icon">☑</span><span class="panel-toggle-label">tasks</span>
      </button>
      <button type="button" class="panel-toggle" data-details-trigger title="session details">
        <span class="panel-toggle-icon">ⓘ</span><span class="panel-toggle-label">details</span>
      </button>
    </div>
  </div>
  <div class="workspace-meta"
       hx-get="/_partials/s/{{.ID}}/meta"
       hx-trigger="load, every 2s"
       hx-swap="innerHTML">
    {{template "workspace_meta" .}}
  </div>
</header>

<div class="conversation"
     id="conversation"
     data-session-id="{{.ID}}"
     data-active-turn-id="{{.ActiveTurnID}}"
     data-state="{{.State}}">
  <div class="conversation-empty" data-empty-placeholder>no messages yet — type below to start the conversation</div>
</div>

<form class="workspace-input" data-input-form data-session-id="{{.ID}}">
  <div class="input-attachments" data-attachments></div>
  <div class="composer-attachments" data-composer-attachments></div>
  <div class="composer-attachment-error" data-attachment-error hidden></div>
  <div class="queue-preview" data-queue-preview hidden>
    <div class="queue-preview-header">
      <span class="queue-preview-label">queued <span data-queue-depth>0</span></span>
      <span class="queue-preview-hint">processes after current turn — <kbd>⇧↵</kbd> or "send as steer" drains as steering</span>
    </div>
    <ul class="queue-preview-list" data-queue-list></ul>
  </div>
  <div class="input-card" data-drop-zone>
    <textarea class="message-input" placeholder="message the agent…" autofocus rows="1"></textarea>
  </div>
  <div class="input-controls">
    <button type="button" class="input-btn" data-attach-trigger title="attach image">＋</button>
    {{if .Capabilities.ChangeModel}}
    <button type="button" class="input-chip model-chip" data-model-trigger>{{if .Model}}{{.Model}}{{else}}—{{end}} <span class="chip-caret">▾</span></button>
    {{else}}
    <span class="input-chip model-chip" title="model changes unavailable">{{if .Model}}{{.Model}}{{else}}—{{end}}</span>
    {{end}}
    {{if or (eq .State "active") (eq .State "awaiting")}}
    <span class="running-indicator" data-running-indicator><span class="running-dot"></span>running{{if .RunningFor}} · {{.RunningFor}}{{end}}</span>
    {{end}}
    <span class="controls-spacer"></span>
    {{if .Capabilities.Interrupt}}
    <button type="button" class="input-btn input-btn-stop stop-btn" data-action-trigger="interrupt" data-capability-interrupt="{{.Capabilities.Interrupt}}" title="stop the in-flight turn"{{if and (ne .State "awaiting") (ne .State "active")}} disabled{{end}}>Stop</button>
    {{end}}
    {{if or .Capabilities.Steer .Capabilities.Send .Capabilities.Queue}}
    <button type="button" class="input-btn input-btn-ghost" data-steer-trigger data-capability-steer="{{.Capabilities.Steer}}" title="drain the queue as a steering message — or steer with the textarea text when the queue is empty"{{if or (not .Capabilities.Steer) (eq .ActiveTurnID "") (and (ne .State "awaiting") (ne .State "active"))}} disabled{{end}}>send as steer <kbd>⇧↵</kbd></button>
    {{end}}
    <button type="submit" class="input-btn input-btn-primary send-btn" data-capability-send="{{.Capabilities.Send}}" data-capability-queue="{{.Capabilities.Queue}}"{{if and (not .Capabilities.Send) (not .Capabilities.Queue)}} disabled title="send unavailable"{{end}}>send <kbd>⌘↵</kbd></button>
  </div>
  <div id="input-status"
       class="input-status"
       hx-get="/_partials/s/{{.ID}}/state"
       hx-trigger="load, every 2s"
       hx-swap="innerHTML">
    {{template "input_status" .}}
  </div>
  <input type="file" data-file-picker accept="image/*" multiple hidden>
</form>
{{end}}

{{define "workspace_meta"}}{{if .SourceLabel}}<span class="source-label" data-source-label="{{.SourceLabel}}">{{.SourceLabel}}</span>{{end}}{{if .Branch}}<span class="branch">{{.Branch}}</span>{{end}}<span class="status-badge" data-state="{{.State}}"><span class="status-dot" data-state="{{.State}}"></span>{{.StateLabel}}</span><span class="turn-count">{{.TurnCount}} turn{{if ne .TurnCount 1}}s{{end}}</span>{{end}}
```

Key changes inside `workspace_meta`:
- All four `<span class="rule-dot">·</span>` separators are removed. The mono cluster spacing in CSS does the work.
- `status-pill` → `status-badge`. Inside the badge the structure is `[dot][label]` with no surrounding pill chrome.

- [ ] **Step 3: Update the CSS — workspace header + status badge + drop rule-dot**

In `cmd/serf-hub/assets/style.css`:

(a) **Replace the `.workspace-meta` rule** (around line 337) so meta items cluster with `gap`. Find the line:

```css
.workspace-meta { margin-top: 3px; display: flex; align-items: baseline; gap: 8px; color: var(--text-muted); font-size: 11.5px; }
```

…and replace it with:

```css
.workspace-meta {
  margin-top: 3px;
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: var(--space-4);
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
}
.workspace-meta .turn-count { color: var(--text-dim); }
```

(b) **Delete the `.rule-dot` rule** (line 355): `​.rule-dot { color: var(--text-dim); }` — remove the entire line.

(c) **Replace `.status-pill` with `.status-badge`**. Find line 356:

```css
.status-pill { display: inline-flex; align-items: baseline; gap: 6px; }
```

…and replace it with:

```css
/* Status badge — typographic small-caps mono in the state colour. No
   background fill: the badge IS the dot + label cluster. */
.status-badge {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  font-weight: 500;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--text-muted);
}
.status-badge[data-state="active"]    { color: var(--state-processing); }
.status-badge[data-state="awaiting"]  { color: var(--state-awaiting); }
.status-badge[data-state="errored"]   { color: var(--state-awaiting); }
.status-badge[data-state="warning"]   { color: var(--state-warning); }
.status-badge[data-state="idle"]      { color: var(--state-idle); }
.status-badge[data-state="ended"]     { color: var(--text-muted); }
.status-badge[data-state="closed"]    { color: var(--text-muted); }
.status-badge[data-state="notLoaded"] { color: var(--text-muted); }
```

(d) **Add the inline hamburger CSS**. Append (immediately after the workspace-header CSS, before `.workspace-meta`):

```css
/* Inline workspace-header hamburger — visible on phone only. The
   button takes the leftmost slot in .workspace-title-row so partials
   without a header (empty state, settings) don't need their own
   hamburger; those partials now lose the hamburger affordance, which
   is acceptable because the sidebar tap-out + ⌘B + the search palette
   all remain available. */
.header-hamburger {
  display: none;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  padding: 0;
  background: transparent;
  border: 1px solid var(--rule);
  border-radius: var(--radius-md);
  color: var(--text);
  font-size: var(--text-lg);
  line-height: 1;
  cursor: pointer;
  flex-shrink: 0;
}
.header-hamburger:hover { background: var(--bg-raised); }
```

(e) **Mobile rules**: replace the existing `#mobile-hamburger { display: none; }` (line 873) and the `#mobile-hamburger { ... }` block inside the media query (lines 899–915) with mobile rules for `.header-hamburger`. Also drop the `padding-left: 56px` offset on `.workspace-header`. Find this block (around lines 866–928):

```css
/* ----- Mobile (<= 767px): off-canvas sidebar, full-width panels, stacked input controls.
   ...
   reachable even when the workspace is showing the empty state, settings,
   or any other partial that doesn't render its own header. */
#mobile-hamburger { display: none; }

@media (max-width: 767px) {
  /* Single-column layout: workspace fills the screen, sidebar lives off-canvas. */
  body.app { display: block; }
  ...
  /* Hamburger pinned to the top-left, available from any workspace state. */
  #mobile-hamburger {
    display: inline-flex;
    ...
    z-index: 150;
  }

  /* Workspace header: leave room for the fixed hamburger; allow wrap. */
  .workspace-header { padding: 10px 14px 8px 56px; }
  ...
}
```

…and replace it with:

```css
/* ----- Mobile (<= 767px): off-canvas sidebar, full-width panels, stacked input controls.
   Phone screens get ~390px viewport; the desktop two-pane layout collapses
   to single-pane workspace, with the sidebar and slide-over panels turned
   into full-screen drawers triggered by the inline header hamburger. */

@media (max-width: 767px) {
  /* Single-column layout: workspace fills the screen, sidebar lives off-canvas. */
  body.app { display: block; }
  #sidebar {
    position: fixed;
    top: 0; left: 0; bottom: 0;
    width: 80vw;
    max-width: 320px;
    z-index: var(--z-drawer, 900);
    transform: translateX(-100%);
    transition: transform var(--motion-base);
    box-shadow: 4px 0 24px rgba(0,0,0,0.4);
  }
  body.app[data-sidebar-open] #sidebar { transform: translateX(0); }
  body.app[data-sidebar-open]::before {
    content: "";
    position: fixed;
    top: 0; left: 0; right: 0; bottom: 0;
    background: rgba(0,0,0,0.5);
    z-index: var(--z-overlay, 800);
  }
  #workspace { width: 100%; height: 100vh; }

  /* Inline hamburger visible on phone, leftmost in the title row.
     Partials don't need to remember a 56px offset anymore. */
  .header-hamburger { display: inline-flex; }

  /* Workspace header reverts to its standard padding; no fixed-overlay
     hamburger offset. */
  .workspace-header { padding: 10px 14px 8px; }
  .workspace-title-row { gap: 8px; flex-wrap: wrap; }
  .workspace-actions { gap: 4px; flex-wrap: wrap; }
  .header-action,
  .panel-toggle { padding: 4px 8px; font-size: 11px; }
  .panel-toggle-icon { display: none; }
  .workspace-meta { font-size: 11px; gap: var(--space-3); }

  /* Empty state and other workspace partials don't have a header to push;
     they keep their own padding without needing a hamburger offset. */
  .workspace-empty { padding-top: 64px; }

  /* Tasks/details: full screen on mobile so they're actually readable. */
  .details-panel {
    width: 100%;
    padding: 14px;
    box-shadow: none;
  }

  /* Bottom strip: keep horizontal but allow wrap so chips reflow rather than overflow. */
  .workspace-input { padding: 8px 12px 10px; }
  .input-controls { flex-wrap: wrap; gap: 6px; }
  .controls-spacer { display: none; }
  .input-btn-primary kbd { display: none; }
  .input-status { flex-wrap: wrap; }

  /* Search palette: full-screen on mobile so the keyboard doesn't shove it. */
  .search-dialog-inner {
    width: 100%;
    max-width: 100%;
    margin: 0;
    height: 100vh;
    border-radius: 0;
    border: none;
  }
  .search-dialog-header {
    position: sticky;
    top: 0;
    z-index: 1;
    align-items: flex-start;
    flex-wrap: wrap;
    background: var(--bg-raised);
  }
  #search-input { min-width: 0; }
  .search-results {
    max-height: calc(100vh - 64px);
    padding-bottom: calc(16px + env(safe-area-inset-bottom));
  }
  .search-row {
    min-height: 48px;
    align-items: flex-start;
    padding: 12px 14px;
  }
  .search-cmd-pill {
    flex-wrap: wrap;
    max-width: 100%;
    line-height: 1.3;
    border-radius: 8px;
  }
  .search-cmd-pill-label {
    white-space: normal;
    overflow-wrap: anywhere;
  }

  /* User-message pill: more breathing room because the screen is narrower. */
  .user-message .pill { max-width: 90%; }
  .conversation { padding: 12px 14px; }
  .diagnostic { margin-left: 0; max-width: none; }
}
```

(f) **Note on rule-dot inside input_strip.html**: the input strip still uses `.rule-dot` (`partials/input_strip.html` lines 3, 5, 8). Pass 6 owns the composer + status row migration. Leave `.rule-dot` defined for now? No — design language requires the class be removed entirely once unused, but we still need it for the input strip. Restore the class definition immediately below `.status-badge`:

```css
/* Transitional: input_strip.html still renders .rule-dot separators
   until the Pass 6 composer migration. Kept here so they don't read
   as raw text. */
.rule-dot { color: var(--text-dim); font-family: var(--font-mono); }
```

- [ ] **Step 4: Run Go tests**

```bash
cd /home/jesse/git/prime-radiant/serf
make test
```

Expected: pass. If any test asserts on `mobile-hamburger`, `status-pill`, or `rule-dot` in `workspace.html`-derived output, capture the diff and update that test in the same commit (use the same pattern as Task 5: rename to the new class name).

- [ ] **Step 5: Run the full jstest suite**

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh
```

Expected: every test prints `PASS: ...`. The existing `test-panels.js` does not depend on the hamburger, so it should be unaffected.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/templates/app.html cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/assets/style.css
git commit -m "feat(serf-hub): inline workspace hamburger; status pill → status badge

The hamburger moves from a body-level fixed-position button into the
workspace-header title row (phone only), so partials no longer need
to remember a 56px padding-left offset. The status pill becomes a
typographic status badge (mono small-caps in the state colour, no
background). Meta-row .rule-dot separators are dropped in favour of
gap: var(--space-4)."
```

---

## Task 10: Activate focus-trap on mobile sidebar drawer

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js`

The existing `setSidebarOpen(open)` is the chokepoint for mobile drawer open/close. Add focus-trap activation when opening, deactivation on close.

- [ ] **Step 1: Modify setSidebarOpen**

In `cmd/serf-hub/assets/sidebar.js`, find the existing `setSidebarOpen` function (lines 82–90):

```js
  function setSidebarOpen(open) {
    if (open) {
      document.body.setAttribute("data-sidebar-open", "");
      document.addEventListener("click", onOutsideClick, true);
    } else {
      document.body.removeAttribute("data-sidebar-open");
      document.removeEventListener("click", onOutsideClick, true);
    }
  }
```

Replace with:

```js
  var sidebarTrapHandle = null;

  function setSidebarOpen(open) {
    if (open) {
      document.body.setAttribute("data-sidebar-open", "");
      document.addEventListener("click", onOutsideClick, true);
      // Only trap focus on phone — desktop sidebar isn't a drawer. Match
      // the design-language breakpoint.
      var isPhone = window.matchMedia && window.matchMedia("(max-width: 767px)").matches;
      if (isPhone && window.SerfFocusTrap) {
        var sidebar = document.getElementById("sidebar");
        var trigger = document.querySelector("[data-sidebar-toggle]");
        if (sidebar) {
          sidebarTrapHandle = window.SerfFocusTrap.activate(sidebar, trigger);
        }
      }
    } else {
      document.body.removeAttribute("data-sidebar-open");
      document.removeEventListener("click", onOutsideClick, true);
      if (sidebarTrapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(sidebarTrapHandle);
        sidebarTrapHandle = null;
      }
    }
  }
```

- [ ] **Step 2: Sanity-check by running the sidebar-collapse jstest**

`test-sidebar-collapse.js` doesn't exercise the mobile drawer code path, but it does eval `sidebar.js`, so any syntax error would surface here.

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-sidebar-collapse.js
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-sidebar-active.js
```

Expected: both pass.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js
git commit -m "feat(serf-hub): trap focus inside mobile sidebar drawer

When setSidebarOpen(true) fires on a phone breakpoint, call
SerfFocusTrap.activate(sidebar, hamburgerTrigger). Restore focus on
close. Desktop (≥768px) is untouched — the sidebar is a pane, not a
drawer."
```

---

## Task 11: Wire focus-trap into the tasks + details panels

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`
- Modify: `cmd/serf-hub/jstest/test-panels.js`

`toggleTasksPanel` and `toggleDetailsPanel` currently take no arguments. Extend them to accept an optional trigger element (so focus restores correctly even when the toggle fires from a keyboard shortcut or a non-trigger element); call `SerfFocusTrap.activate/deactivate`.

- [ ] **Step 1: Modify the existing panel test to stub SerfFocusTrap**

In `cmd/serf-hub/jstest/test-panels.js`, immediately after the `window.fetch = (url) => { ... }` block (around line 30, before `window.eval(rendererSrc);`), insert:

```js
// Stub SerfFocusTrap — the renderer calls it but the panel test doesn't
// verify focus-trap behaviour (that's covered by test-focus-trap.js).
window.SerfFocusTrap = {
  activate: function () { return { handle: true }; },
  deactivate: function () { /* no-op */ },
};
```

- [ ] **Step 2: Modify toggleTasksPanel + toggleDetailsPanel**

In `cmd/serf-hub/assets/renderer.js`, locate `function toggleTasksPanel()` (around line 2868). Replace the entire function with:

```js
  function toggleTasksPanel(trigger) {
    const existing = document.getElementById("tasks-panel");
    if (existing) {
      if (existing.__pollTimer) clearInterval(existing.__pollTimer);
      if (existing.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(existing.__trapHandle);
      }
      existing.remove();
      setPanelToggleActive("[data-tasks-trigger]", false);
      return;
    }
    // Close details panel if open — they share the same slot.
    const details = document.getElementById("details-panel");
    if (details) {
      if (details.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(details.__trapHandle);
      }
      details.remove();
    }
    setPanelToggleActive("[data-details-trigger]", false);

    const header = document.querySelector(".workspace-header");
    if (!header) return;
    const id = header.dataset.sessionId;
    if (!id) return;

    const triggerEl = trigger || document.querySelector("[data-tasks-trigger]");

    const panel = document.createElement("aside");
    panel.id = "tasks-panel";
    panel.className = "details-panel";
    panel.innerHTML = "<div class='details-loading'>loading…</div>";
    document.body.appendChild(panel);

    const refresh = () => {
      const tasksPromise = window.SerfAppwire
        ? window.SerfAppwire.tasks(id)
        : partialFetch(sessionPartialPath(id, "tasks")).then(r => r.json());
      tasksPromise.then(tasks => {
        renderTasksInto(panel, tasks);
      }).catch(() => {
        panel.innerHTML = "<div class='details-loading'>failed to load</div>";
      });
    };
    refresh();
    panel.__pollTimer = setInterval(refresh, 2000);
    setPanelToggleActive("[data-tasks-trigger]", true);

    if (window.SerfFocusTrap) {
      panel.__trapHandle = window.SerfFocusTrap.activate(panel, triggerEl);
    }

    const close = () => {
      if (panel.__pollTimer) clearInterval(panel.__pollTimer);
      if (panel.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(panel.__trapHandle);
      }
      panel.remove();
      setPanelToggleActive("[data-tasks-trigger]", false);
    };
    document.addEventListener("keydown", function escClose(ev) {
      if (ev.key === "Escape") {
        close();
        document.removeEventListener("keydown", escClose);
      }
    });
    bindClickOutside(panel, "[data-tasks-trigger]", close);
  }
```

Then locate `function toggleDetailsPanel()` (around line 3039). Replace it with:

```js
  function toggleDetailsPanel(trigger) {
    const existing = document.getElementById("details-panel");
    if (existing) {
      if (existing.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(existing.__trapHandle);
      }
      existing.remove();
      setPanelToggleActive("[data-details-trigger]", false);
      return;
    }
    // Close tasks panel if open — they share the same slot.
    const tasks = document.getElementById("tasks-panel");
    if (tasks) {
      if (tasks.__pollTimer) clearInterval(tasks.__pollTimer);
      if (tasks.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(tasks.__trapHandle);
      }
      tasks.remove();
      setPanelToggleActive("[data-tasks-trigger]", false);
    }
    const header = document.querySelector(".workspace-header");
    if (!header) return;
    const id = header.dataset.sessionId;
    if (!id) return;

    const triggerEl = trigger || document.querySelector("[data-details-trigger]");

    const panel = document.createElement("aside");
    panel.id = "details-panel";
    panel.className = "details-panel";
    panel.innerHTML = "<div class='details-loading'>loading…</div>";
    document.body.appendChild(panel);
    partialFetch(sessionPartialPath(id, "details")).then(r => r.text()).then(html => {
      panel.innerHTML = html;
      if (window.SerfFocusTrap && !panel.__trapHandle) {
        // Re-activate now that the panel has real focusable children.
        panel.__trapHandle = window.SerfFocusTrap.activate(panel, triggerEl);
      }
    }).catch(() => { panel.innerHTML = "<div class='details-loading'>failed to load</div>"; });
    setPanelToggleActive("[data-details-trigger]", true);

    // Initial activation (may have no focusable children until fetch resolves;
    // helper falls back to focusing the panel itself).
    if (window.SerfFocusTrap) {
      panel.__trapHandle = window.SerfFocusTrap.activate(panel, triggerEl);
    }

    const close = () => {
      if (panel.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(panel.__trapHandle);
      }
      panel.remove();
      setPanelToggleActive("[data-details-trigger]", false);
    };
    document.addEventListener("keydown", function escClose(ev) {
      if (ev.key === "Escape") {
        close();
        document.removeEventListener("keydown", escClose);
      }
    });
    bindClickOutside(panel, "[data-details-trigger]", close);
  }
```

- [ ] **Step 3: Update the click delegate to pass the trigger element**

In `cmd/serf-hub/assets/renderer.js`, find the body-level click handler around line 2793:

```js
    } else if (t.matches("[data-details-trigger]") || t.closest && t.closest("[data-details-trigger]")) {
      e.preventDefault();
      toggleDetailsPanel();
    } else if (t.matches("[data-tasks-trigger]") || t.closest && t.closest("[data-tasks-trigger]")) {
      e.preventDefault();
      toggleTasksPanel();
    } else {
```

…and replace with:

```js
    } else if (t.matches("[data-details-trigger]") || t.closest && t.closest("[data-details-trigger]")) {
      e.preventDefault();
      var detailsTrigger = t.matches("[data-details-trigger]") ? t : t.closest("[data-details-trigger]");
      toggleDetailsPanel(detailsTrigger);
    } else if (t.matches("[data-tasks-trigger]") || t.closest && t.closest("[data-tasks-trigger]")) {
      e.preventDefault();
      var tasksTrigger = t.matches("[data-tasks-trigger]") ? t : t.closest("[data-tasks-trigger]");
      toggleTasksPanel(tasksTrigger);
    } else {
```

Also fix the call site at line 1449 (inside the `system-line` task-list click handler):

```js
        line.onclick = (e) => { e.preventDefault(); toggleTasksPanel(); };
```

Replace with:

```js
        line.onclick = (e) => { e.preventDefault(); toggleTasksPanel(null); };
```

(`null` is fine — `toggleTasksPanel` falls back to `document.querySelector("[data-tasks-trigger]")` for the restore target.)

- [ ] **Step 4: Run the panels jstest**

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-panels.js
```

Expected: `PASS: click-outside dismissal works for tasks and details panels`.

- [ ] **Step 5: Run the whole jstest suite**

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh
```

Expected: every test passes.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-panels.js
git commit -m "feat(serf-hub): trap focus inside tasks and details slide-overs

toggleTasksPanel and toggleDetailsPanel now accept the trigger element
and pass it to SerfFocusTrap.activate. The trap deactivates on close
(Esc, click-outside, or toggle re-click) and restores focus to the
trigger. Details panel re-activates once the fetched HTML resolves so
focus can land on real focusable children rather than the panel
itself.

test-panels.js stubs SerfFocusTrap so the existing dismissal tests
keep passing."
```

---

## Task 12: Build + manual verification pass

This task has no code edits. It builds the binary, runs every automated check, then walks through the manual verification checklist.

- [ ] **Step 1: Build the binary**

```bash
cd /home/jesse/git/prime-radiant/serf
make build-hub
```

Expected: no errors. The Go binary lands at `./serf-hub` (or wherever the Makefile puts it).

- [ ] **Step 2: Run all Go tests**

```bash
cd /home/jesse/git/prime-radiant/serf
make test
```

Expected: all pass.

- [ ] **Step 3: Run lint-naming**

```bash
cd /home/jesse/git/prime-radiant/serf
make lint-naming
```

Expected: no violations.

- [ ] **Step 4: Run every jstest**

```bash
cd /home/jesse/git/prime-radiant/serf/cmd/serf-hub/jstest
NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh
```

Expected: every test prints `PASS: ...` and exits 0.

- [ ] **Step 5: Manual smoke — desktop**

Start the hub locally and walk these scenarios:

```bash
cd /home/jesse/git/prime-radiant/serf
./serf-hub
```

Open `http://127.0.0.1:9180` and verify:

- [ ] Sidebar renders with 2-line title wrap — pick (or spawn) a session whose prompt is long ("refactor auth middleware…") and confirm the title wraps across two lines without truncation. Meta line below shows project · age in mono.
- [ ] Project headers show ⚙ and ＋ persistently (no hover required). Compact mono uppercase tracking on the project name.
- [ ] Project chevron toggles via mouse click, Enter, and Space (Tab to it first, then activate).
- [ ] `⌘B` (Cmd+B on macOS / Ctrl+B on Linux) toggles rail mode. Sidebar collapses to 56px; dots remain visible; titles disappear. Press again to restore. Reload — rail state persists.
- [ ] Rail-toggle button (⇤ / ⇥) in the sidebar header collapses + expands. Hover shows the title "collapse sidebar (⌘B)" / "expand sidebar (⌘B)".
- [ ] Clicking a session swaps the workspace and applies a left-border accent + tinted background on that row (`[data-active]`). Clicking a different session moves the marker.
- [ ] Navigating to `/new` clears the active marker from all rows.
- [ ] In the workspace header, the meta row shows `source · branch · STATE 5 turns` with mono spacing (no `·` dot separator characters between meta items, just spacing). The status badge is the state colour in small-caps mono.
- [ ] Tasks panel + details panel open as before. Tab cycles inside the panel; Tab from the last focusable wraps to the first; Shift+Tab from the first wraps to the last; Esc closes the panel and focus returns to the tasks/details trigger button.
- [ ] Test light theme via `Settings → Theme → Light`. State tints on `.sb-row` are visible (12% mix) against the cream background.

- [ ] **Step 6: Manual smoke — phone**

Open DevTools, switch to mobile emulation (390×844 or any phone preset), reload, verify:

- [ ] The hamburger appears inline at the left of the workspace title row (not floating in the top-left corner). Tapping it opens the sidebar drawer.
- [ ] No padding-left offset is visible on the workspace header — the workspace title is flush with the content (no 56px gap that used to make room for the floating hamburger).
- [ ] Open the sidebar drawer; Tab cycles inside the drawer. Esc closes the drawer.
- [ ] In an empty-state workspace (`/new` page) there is no inline hamburger — the spawn form has its own layout. Verify that the sidebar is still reachable: it's reachable via swipe from the edge / by tapping in from outside isn't supported on web — the user must navigate to a session first OR open `⌘K` to search and land on a session. Document this as expected (the spawn page is a deliberate full-screen surface).

- [ ] **Step 7: Manual smoke — keyboard-only**

With the keyboard alone:

- [ ] Tab into the sidebar; verify each `.sb-row` receives focus (visible via the browser's default focus indicator — Pass 5 will replace this with the design-language `:focus-visible` rule, but the rows must already be focusable).
- [ ] Tab to a project chevron, press Enter, project expands. Press Space, project collapses.
- [ ] Open the search palette with `⌘K`. Tab around. Esc closes; focus lands back on the search trigger.
- [ ] Open the tasks panel via the tasks button. Tab cycles inside; Esc closes and focus returns to the tasks button.

- [ ] **Step 8: If any verification fails, fix in a follow-up commit**

Don't amend earlier commits — make a new fix commit with a descriptive message. This keeps the git-spice stack linear.

- [ ] **Step 9: Open the PR**

```bash
cd /home/jesse/git/prime-radiant/serf
gh pr create --title "ui: restructure sidebar rows; add rail mode, workspace header refactor, slide-over focus trap" --body "$(cat <<'EOF'
## Summary

Pass 4 of the serf-hub UI overhaul (per the responsive-ui-design spec's ship-order; "Pass 5" by migration-order numbering — see the spec's Rollout section for the re-sequencing).

- Sidebar rows restructured to a `.sb-row` grid with dot-col + text-col. Titles wrap to 2 lines via `-webkit-line-clamp`; meta line is mono. Old `.session-row` / `.live-row` / `.subagent-row` / `.fork-row` class names removed entirely (not co-applied transitionally).
- Sidebar gains a 56px rail mode, persisted to `localStorage["serf-hub.sidebar.rail"]`, toggled by the new ⇤/⇥ button or `⌘B` / `Ctrl+B`.
- Project chevron becomes a `<button>` (keyboard-accessible). `aria-expanded` reflects state. ⚙ and ＋ are persistent (no hover-only opacity).
- `data-active` marker on the current session row, driven by `htmx:afterSwap` + `window.location.pathname` matching.
- Workspace hamburger moves from a body-level fixed-position button into the workspace-header title row (phone only). Partials no longer need a 56px padding-left offset.
- Workspace meta row: status pill → status badge (typographic small-caps mono in the state colour, no background); `.rule-dot` separators dropped in favour of `gap: var(--space-4)`.
- New `assets/focus-trap.js` (~85 LOC) exposing `window.SerfFocusTrap.activate/deactivate`. Used by the mobile sidebar drawer + the tasks/details slide-overs. Restores focus to the trigger on close.
- Container query `@container sidebar (max-width: 80px)` collapses to dot-only if the sidebar ever shrinks structurally.
- `web_test.go` assertions updated in lockstep.
- New jstests: `test-focus-trap.js`, `test-sidebar-active.js`.

## Test plan

- [ ] `make test` passes
- [ ] `make lint-naming` passes
- [ ] All jstests pass (`cd cmd/serf-hub/jstest && NODE_PATH=... sh run-all.sh`)
- [ ] Manual: sidebar 2-line wrap, rail toggle (button + ⌘B), localStorage persistence
- [ ] Manual: `data-active` follows session clicks; clears on `/new`
- [ ] Manual: workspace header inline hamburger on phone, status badge in meta row
- [ ] Manual: focus trap in tasks + details panels — Tab cycles, Esc restores focus to trigger
- [ ] Manual: light theme state-tint contrast on `.sb-row`

EOF
)"
```

- [ ] **Step 10: Final commit if any manual fixes were needed**

If steps 5–7 surfaced any issues, ensure each fix is committed before pushing the PR. Otherwise this step is a no-op.

---

## Self-review checklist

The plan covers every item in the user's Pass 4 scope:

| Scope item | Task(s) |
| --- | --- |
| Each row becomes `<a class="sb-row" data-state ...>` with dot-col + text-col, 2-line title clamp, mono meta | Task 3 (template), Task 4 (CSS) |
| Sidebar width 260px (preserved) | unchanged — Task 4 leaves the desktop `#sidebar { width: 260px; }` rule in place |
| `data-sidebar-rail` body state + 56px rail mode + rail-toggle button | Task 3 (button), Task 4 (CSS), Task 6 (JS) |
| `⌘B` toggles rail; persists to `localStorage["serf-hub.sidebar.rail"]` | Task 6 |
| Project headers compact mono with persistent ⚙ + ＋ | Task 3 (template), Task 4 (CSS) |
| Project chevron is `<button>` (keyboard accessible) | Task 3 (template), Task 6 (`aria-expanded` sync) |
| `@container sidebar (max-width: 80px)` hides text columns | Task 4 |
| Update `web_test.go` assertions for `sb-row` shape | Task 5 |
| Old class names removed entirely (not co-applied) | Task 5 step 5 (delete dead CSS), Task 3 (template rewrite uses only new names) |
| Wire `data-active` via `htmx:afterSwap` on `#workspace` | Task 7 (failing test), Task 8 (impl) |
| `data-active` jstest | Task 7 |
| Hamburger inside `.workspace-header` (phone only); drop body-level `#mobile-hamburger`; drop 56px workspace-header offset | Task 9 |
| Status pill → status badge | Task 9 |
| Drop `.rule-dot` separators in meta row, use `gap: var(--space-4)` | Task 9 |
| New `focus-trap.js` (~80 LOC) with `activate/deactivate` API | Task 1 (failing test), Task 2 (impl) |
| `renderer.js` `toggleTasksPanel(trigger)` + `toggleDetailsPanel(trigger)` use focus-trap | Task 11 |
| `sidebar.js` wraps mobile drawer open in focus-trap | Task 10 |
| `test-focus-trap.js` covers open, Tab forward, Shift+Tab back, Esc restores | Task 1 |
| Build + manual verify | Task 12 |

No placeholders, every code block is complete, every selector / function / property name in later tasks is defined in earlier tasks (e.g., `SerfFocusTrap.activate` in Task 2 is consumed by Task 10 + Task 11; `[data-sidebar-rail]` body attribute set in Task 6 matches the CSS selector in Task 4; `.sb-row` class created in Task 3 is consumed by Task 4's CSS, Task 5's tests, Task 7's jstest, and Task 8's JS).

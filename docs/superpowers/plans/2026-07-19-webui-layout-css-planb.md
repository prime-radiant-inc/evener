# Web UI Layout + CSS Consolidation (Plan B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship spec increments 5–9 of `docs/superpowers/specs/2026-07-18-webui-foundation-experience-design.md`: the layout scale + breakpoint ladder, composer fixes, the server-rendered home launchpad, the canonical color-token migration, the state-color recolor, and the contrast/retired-treatments pass.

**Architecture:** No bundler, no framework, embedded assets. All client changes are CSS (`cmd/serf-hub/assets/style.css`) plus small vanilla-JS edits in the existing `window.Serf*` module pattern; the launchpad is server-rendered Go (`cmd/serf-hub/web.go` + a new `html/template` partial). `docs/web-ui/design-system.md` is the north star; addenda land in the same commit as the change they record.

**Tech Stack:** Go (`html/template`), vanilla CSS/JS, node+jsdom stylesheet-text/behavioral tests (`cmd/serf-hub/jstest/`), Go tests (`cmd/serf-hub`).

## Global Constraints

- Per-commit gate (must pass before every commit):
  `make build-hub` + `cd cmd/serf-hub/jstest && ./run-all.sh` + `GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub`.
  (jsdom resolves via `/tmp/serf-jstest-jsdom/node_modules`; run-all.sh finds it.)
- Breakpoint ladder (verbatim from spec): phone ≤767px (unchanged); tablet 768–1199px; desktop 1200–1799px; wide ≥1800px.
- Width scale (verbatim): `--measure: 720px` prose column; machine rows bleed right to `--measure-machine` (1000px, ~1200px at wide); left edges never move.
- Canonical token vocabulary: `--bg`, `--surface`, `--surface-2`, `--line`, `--hair`, `--ink`, `--ink-2`, `--ink-3`, `--ink-4`, `--accent`, `--attention`, `--error`, `--done`, `--success`. Canonicals adopt **shipped** values (no visual change) **except `--ink-3`**, which takes the doc value `#7e8593` (dark) / `#6b6b76` (light — computed 4.66:1 on `--surface #f1f1f2`, AA).
- The `--text-*` **type scale** (`--text-2xs`…`--text-2xl`) is canonical and stays; every "no legacy names" assertion carries this documented carve-out.
- State language (verbatim): blue=live, amber=needs-you, red=error, neutral=done.
- Favicon hex constants are deliberately theme-independent (dark browser chrome); they stay pinned constants, updated to the new language, base favicon neutral.
- Home launchpad: up to 8 sessions, Current+Recent tiers, sorted by `UpdatedAt` desc, **no live status dots**, **all interpolated strings HTML-escaped** (html/template), with a Go XSS test.
- Tests are deterministic (AGENTS.md): no provider credentials, network, or live model behavior in default tests.
- design-system.md addenda land in the same commit as the change they record: §3 hex table (Task 7), §4 wide-band rule (Task 2), §5 sidebar tri-state (Task 4), §8 breakpoint ladder (Task 2).
- Line numbers below refer to `cmd/serf-hub/assets/style.css` (5815 lines) and siblings at base commit `2e7dfaa2`. Earlier tasks shift later line numbers — locate by the quoted selector/text, not the line number.

---

### Task 1: Width scale — `--measure` tokens and the machine bleed

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (`:root` token block ~:56-122; `#workspace` ~:499-516; new rules after ~:516)
- Test: `cmd/serf-hub/jstest/test-layout-scale-css.js` (create)

**Interfaces:**
- Produces: CSS custom properties `--measure` (720px) and `--measure-machine` (1000px) defined on `:root`; `--workspace-content-max-w` redefined as `var(--measure-machine)`. Tasks 2, 3, 6 consume these tokens.

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-layout-scale-css.js`:

```js
// Width scale: one measure for prose, a wider machine bleed, capped container.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");

function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

assert(/--measure:\s*720px;/.test(css), "--measure: 720px defined on :root");
assert(/--measure-machine:\s*1000px;/.test(css), "--measure-machine: 1000px defined on :root");
assert(/--workspace-content-max-w:\s*var\(--measure-machine\);/.test(css),
  "workspace content cap follows --measure-machine");
assert(!/832px/.test(css), "shipped 832px cap is gone");
// Prose rows default to the measure; machine rows bleed right to the container.
assert(/\.conversation\s*>\s*\*\s*\{[^}]*max-width:\s*var\(--measure\)/.test(css),
  "conversation children default to the prose measure");
const bleed = css.match(/\.conversation\s*>\s*\.tool-call[\s\S]*?\{[^}]*max-width:\s*none/);
assert(bleed, "machine rows (.tool-call, …) bleed past the measure");
for (const sel of [".tool-call-cluster", ".subs", ".notification-card", ".task-card"]) {
  assert(bleed[0].includes(sel), "bleed list includes " + sel);
}
console.log("ok layout width scale");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-layout-scale-css.js`
Expected: FAIL with "--measure: 720px defined on :root"

- [ ] **Step 3: Implement**

In `style.css`, inside the `:root` block, after the `--space-9: 64px;` line (~:66), add:

```css
  /* Width scale — one measure for prose, a wider bleed for machine rows
     (design-system §4). Left edges never move; only the right edge of
     machine rows extends. The wide band (≥1800px) widens the bleed. */
  --measure: 720px;
  --measure-machine: 1000px;
```

Change `#workspace` (~:500) from `--workspace-content-max-w: 832px;` to
`--workspace-content-max-w: var(--measure-machine);`.

Immediately after the `.workspace-header, .conversation, .workspace-input { … }` rule (~:511-516), add:

```css
/* One width scale (design-system §4): prose rows hold the 720px measure;
   machine rows (tool calls, clusters, the subagent rail, cards) bleed right
   to --measure-machine. Children with their own max-width (e.g. .think's
   680px) keep it — those rules come later in the file and win. */
.conversation > * { max-width: var(--measure); }
.conversation > .tool-call,
.conversation > .tool-call-cluster,
.conversation > .subs,
.conversation > .notification-card,
.conversation > .task-card { max-width: none; }
```

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-layout-scale-css.js && ./run-all.sh`
Expected: PASS; full suite green (existing tests do not assert 832px — if one does, update its expectation to `var(--measure-machine)`).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-layout-scale-css.js
git commit -m "web: one width scale — 720px prose measure, machine rows bleed to 1000px"
```

---

### Task 2: Breakpoint ladder — tablet band, wide band, doc addenda

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (:539 side-panes hide; new wide-band block)
- Modify: `docs/web-ui/design-system.md` (§4 addendum, §8 addendum)
- Test: `cmd/serf-hub/jstest/test-breakpoint-ladder-css.js` (create)

**Interfaces:**
- Consumes: `--measure-machine` from Task 1.
- Produces: tablet band 768–1199px hides side panes; wide band ≥1800px sets `--measure-machine: 1200px`.

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-breakpoint-ladder-css.js`:

```js
// Breakpoint ladder: phone ≤767 (unchanged), tablet 768–1199 (panes hidden),
// desktop 1200–1799, wide ≥1800 (machine bleed widens to 1200px).
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

assert(/@media \(max-width: 1199px\) \{\s*\.side-panes, \.pane-splitter \{ display: none !important; \}\s*\}/.test(css),
  "side panes hidden through the tablet band (max-width: 1199px)");
assert(!/@media \(max-width: 767px\) \{ \.side-panes/.test(css),
  "old 767px pane-hiding rule is gone");
assert(/@media \(min-width: 1800px\) \{\s*:root \{\s*--measure-machine:\s*1200px;\s*\}\s*\}/.test(css),
  "wide band widens the machine bleed to 1200px");
// Phone band untouched: the phone tap floor still flips at 767px.
assert(/@media \(max-width: 767px\) \{\s*\n?\s*\/\*[^*]*\*\/\s*\n?\s*:root \{ --tap-min: 44px; \}/.test(css),
  "phone band (max-width: 767px) still sets --tap-min: 44px");
console.log("ok breakpoint ladder");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-breakpoint-ladder-css.js`
Expected: FAIL with "side panes hidden through the tablet band"

- [ ] **Step 3: Implement**

In `style.css` replace the rule at :539:

```css
@media (max-width: 1199px) { .side-panes, .pane-splitter { display: none !important; } }
```

(was `@media (max-width: 767px) { … }` — tablet joins phone in hiding side panes; the transcript was ~344px wide at 1024px with a pane open.)

After the Task 1 bleed rules, add:

```css
/* Wide band (≥1800px): the prose measure stays 720px at every width; only
   the machine bleed widens. */
@media (min-width: 1800px) {
  :root { --measure-machine: 1200px; }
}
```

- [ ] **Step 4: design-system.md addenda (same commit)**

In `docs/web-ui/design-system.md` §4, after the "Design rule:" paragraph (~:141-142), add:

```markdown
**Breakpoint ladder + wide band (2026-07-19 addendum).** Phone ≤767px; tablet 768–1199px
(side panes hidden, sidebar auto-rails — see §5); desktop 1200–1799px; wide ≥1800px.
The prose measure holds 720px at **every** width; the machine bleed (`--measure-machine`)
is 1000px below the wide band and 1200px at/above it. Left edges never move.
```

In §8, change the heading line `## 8. Mobile (≤767px)` — leave the heading, but add at the top of the section:

```markdown
> Breakpoint context (2026-07-19): this section is the **phone** band. The full ladder is
> phone ≤767px · tablet 768–1199px · desktop 1200–1799px · wide ≥1800px (§4).
```

- [ ] **Step 5: Run tests + gate**

Run: `cd cmd/serf-hub/jstest && node test-breakpoint-ladder-css.js && ./run-all.sh && cd ../../.. && make build-hub`
Expected: PASS, suite green, build ok.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-breakpoint-ladder-css.js docs/web-ui/design-system.md
git commit -m "web: breakpoint ladder — tablet band hides panes, wide band widens machine bleed"
```

---

### Task 3: Composer — dock spans the window, hit targets, ceilings, short-desktop, phone rebalance

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (:511-516 cap group, :1924-1929 dock, :2056 textarea, :2083 model pill, phone band ~:4551-4553, new short-desktop block)
- Test: `cmd/serf-hub/jstest/test-composer-layout-css.js` (create)

**Interfaces:**
- Consumes: `--measure` from Task 1.
- Produces: `.workspace-input` is a full-window-width dock; `.workspace-input > [data-composer-surface]` is the centered 720px content column.

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-composer-layout-css.js`:

```js
// Composer layout: the dock spans the window (design-system §6), the input
// column centers at the measure, hit targets and ceilings hold.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

const capGroup = css.match(/\.workspace-header,\s*\n\.conversation,\s*\n?[^{]*\{/);
assert(!capGroup || !capGroup[0].includes(".workspace-input"),
  ".workspace-input removed from the width-capped group (dock spans the window)");
assert(/\.workspace-input\s*\{[^}]*background:\s*var\(--bg-raised\)/.test(css),
  "dock carries the --bg-raised background step");
assert(/\.workspace-input > \[data-composer-surface\] \{[^}]*width:\s*min\(100%, var\(--measure\)\)[^}]*margin-inline:\s*auto/.test(css),
  "composer content column centers at the measure");
assert(/button\.composer-model-value \{[^}]*min-height:\s*30px/.test(css),
  "desktop model pill hit target ≥30px");
assert(/\.message-input \{[^}]*max-height:\s*min\(50vh, 264px\)/.test(css),
  "textarea has a px ceiling");
assert(/@media \(min-width: 768px\) and \(max-height: 639px\)/.test(css),
  "short-desktop band exists");
const phoneIdx = css.indexOf("@media (max-width: 767px) {\n  /* Touch target floor");
const shortIdx = css.indexOf("@media (max-width: 900px) and (max-height: 560px)");
const phoneBand = css.slice(phoneIdx, shortIdx);
assert(/\.controls-center \{[^}]*margin-left:\s*auto/.test(phoneBand),
  "phone control row rebalanced (model chip clusters right with the actions)");
console.log("ok composer layout");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-composer-layout-css.js`
Expected: FAIL with ".workspace-input removed from the width-capped group"

- [ ] **Step 3: Implement**

a. At :511-516, remove `.workspace-input` from the cap group so it reads:

```css
.workspace-header,
.conversation {
  width: min(100%, var(--workspace-content-max-w));
  margin-inline: auto;
}
```

b. At :1924, give the dock its container treatment (design-system §6: hairline top rule + background step — the composer's ONE containment device):

```css
.workspace-input { padding: var(--space-2) var(--space-6) var(--space-2); border-top: 1px solid var(--rule-soft); background: var(--bg-raised); }
```

c. At :1929, center the content column at the measure:

```css
.workspace-input > [data-composer-surface] { display: flex; flex-direction: column; min-width: 0; width: min(100%, var(--measure)); margin-inline: auto; }
```

d. At :2056, cap the textarea in px (50vh on a tall monitor was an unbounded wall):

```css
.message-input { width: 100%; min-height: 36px; max-height: min(50vh, 264px); background: transparent; border: none; resize: none; color: var(--text); font: inherit; font-family: var(--font-sans); font-size: var(--text-md); outline: none; line-height: var(--leading-snug); overflow-y: auto; }
```

e. At :2083, desktop model-pill hit target (phone overrides to `--tap-min` later in the file and wins):

```css
button.composer-model-value {
  cursor: pointer;
  color: var(--text-muted);
  padding: var(--space-1) var(--space-2);
  min-height: 30px;
  border-radius: var(--radius-md);
  transition: background var(--motion-fast), color var(--motion-fast);
}
```

f. Phone rebalance — in the phone band, at the existing rule (~:4552) `.controls-center { flex: 1; min-width: 0; overflow: hidden; }`, replace with:

```css
  /* Rebalanced action row: attach sits left; the model chip clusters with the
     action buttons on the right where the thumb is — no dead center gap. */
  .controls-center { flex: 0 1 auto; min-width: 0; overflow: hidden; margin-left: auto; }
```

g. Short-desktop band — add after the composer section (near the `.pane-compact` rules, ~:2149):

```css
/* Short desktop windows (<640px tall): compact the dock chrome so the
   transcript keeps the space. Phone has its own rules; this band is
   desktop-width only. */
@media (min-width: 768px) and (max-height: 639px) {
  .workspace-input { padding-block: var(--space-1); }
  .input-card { padding: var(--space-1) var(--space-3); }
  .input-card .input-controls { padding-top: var(--space-1); }
  .message-input { max-height: 96px; }
}
```

- [ ] **Step 4: Run tests + gate**

Run: `cd cmd/serf-hub/jstest && node test-composer-layout-css.js && ./run-all.sh && cd ../../.. && make build-hub`
Expected: PASS, suite green. If `test-mobile-css.js` or `test-composer-status-compact.js` asserts the old `.controls-center` flex or 50vh ceiling, update those expectations to the rules above (same commit).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-composer-layout-css.js
git commit -m "web: composer dock spans the window; hit targets, textarea ceiling, short-desktop band"
```

---

### Task 4: Sidebar tri-state — auto/rail/pane with migration and one effective-state helper

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js` (:1141-1174 rail block, :1181-1189 click handler, :1204-1213 ⌘B handler, :1218-1224 label sync, :1243 exports)
- Modify: `cmd/serf-hub/assets/settings-appearance.js` (:26-32 radio handler, :56-60 state reflect, :79-85 init IIFE)
- Modify: `cmd/serf-hub/templates/partials/settings/theme.html` (:29-32 radio group)
- Modify: `docs/web-ui/design-system.md` (§5 addendum)
- Test: `cmd/serf-hub/jstest/test-sidebar-tristate.js` (create)

**Interfaces:**
- Produces: `window.SerfSidebar.applySidebarMode(mode)`, `window.SerfSidebar.readSidebarMode()`, `window.SerfSidebar.effectiveSidebarState(mode)`. `body[data-sidebar-mode]` records the setting (`auto`|`rail`|`pane`); `body[data-sidebar-rail]` continues to reflect the **effective** state so all existing rail CSS and `panes.js`'s resizer-disable keep working unchanged.
- Consumers: `settings-appearance.js` calls `applySidebarMode`; `panes.js` unchanged.

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-sidebar-tristate.js` (pattern mirrors `test-sidebar-migration.js`):

```js
// Sidebar tri-state: auto|rail|pane persisted under the legacy key, binary
// prefs migrate (true→rail, false→pane, absent→auto), one effective-state
// helper drives body[data-sidebar-rail], ⌘B cycles rail→pane→auto.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");

function makeWindow(stored, desktopMatches) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
  const w = dom.window;
  if (stored !== null) w.localStorage.setItem("serf-hub.sidebar.rail", stored);
  w.matchMedia = (q) => ({ matches: q === "(min-width: 1200px)" ? desktopMatches : false, addEventListener() {}, addListener() {} });
  w.fetch = () => new Promise(() => {});
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(src);
  return w;
}
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

// Migration: binary "true" → rail, "false" → pane, absent → auto.
let w = makeWindow("true", true);
assert(w.SerfSidebar.readSidebarMode() === "rail", '"true" migrates to rail');
w = makeWindow("false", true);
assert(w.SerfSidebar.readSidebarMode() === "pane", '"false" migrates to pane');
w = makeWindow(null, true);
assert(w.SerfSidebar.readSidebarMode() === "auto", "absent pref defaults to auto");

// Effective state: auto follows the 1200px query; explicit modes pin.
w = makeWindow(null, true);
assert(!w.document.body.hasAttribute("data-sidebar-rail"), "auto on desktop ≥1200px → pane (no rail attr)");
assert(w.document.body.getAttribute("data-sidebar-mode") === "auto", "data-sidebar-mode records the setting");
w = makeWindow(null, false);
assert(w.document.body.hasAttribute("data-sidebar-rail"), "auto below 1200px → rail");
w = makeWindow("rail", true);
assert(w.document.body.hasAttribute("data-sidebar-rail"), "explicit rail pins rail even on desktop");

// Cycle: rail → pane → auto → rail, persisted.
w = makeWindow("rail", true);
w.SerfSidebar.cycleSidebarMode();
assert(w.SerfSidebar.readSidebarMode() === "pane", "rail cycles to pane");
w.SerfSidebar.cycleSidebarMode();
assert(w.SerfSidebar.readSidebarMode() === "auto", "pane cycles to auto");
w.SerfSidebar.cycleSidebarMode();
assert(w.SerfSidebar.readSidebarMode() === "rail", "auto cycles to rail");
assert(w.localStorage.getItem("serf-hub.sidebar.rail") === "rail", "cycle persists to storage");

console.log("ok sidebar tri-state");
process.exit(0);
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-tristate.js`
Expected: FAIL (eval throws or first assertion fails — `readSidebarMode` is not exported yet)

- [ ] **Step 3: Implement sidebar.js**

Replace the rail block (:1141-1179, from the `// Sidebar rail mode` comment through the `if (isRailEnabled()) { setRail(true); }` init) with:

```js
  // Sidebar tri-state — auto | rail | pane, persisted to localStorage under
  // the legacy key. body[data-sidebar-mode] records the SETTING;
  // body[data-sidebar-rail] reflects the EFFECTIVE state, so all rail CSS and
  // panes.js's resizer-disable keep reading one attribute. Migration: the old
  // binary pref maps "true"→rail, "false"→pane, absent→auto (auto is new:
  // rail below 1200px, pane at/above — before the tablet band existed the
  // sidebar simply stayed full, so this is a deliberate improvement).
  var SIDEBAR_MODE_KEY = "serf-hub.sidebar.rail";
  var SIDEBAR_DESKTOP_QUERY = "(min-width: 1200px)";

  function readSidebarMode() {
    try {
      var v = window.localStorage.getItem(SIDEBAR_MODE_KEY);
      if (v === "rail" || v === "pane" || v === "auto") return v;
      if (v === "true") return "rail";
      if (v === "false") return "pane";
    } catch (e) {
      // localStorage may be disabled; fall through to auto.
    }
    return "auto";
  }

  function effectiveSidebarState(mode) {
    if (mode === "rail" || mode === "pane") return mode;
    if (window.matchMedia && window.matchMedia(SIDEBAR_DESKTOP_QUERY).matches) return "pane";
    return "rail";
  }

  function applySidebarMode(mode) {
    var eff = effectiveSidebarState(mode);
    document.body.setAttribute("data-sidebar-mode", mode);
    if (eff === "rail") {
      document.body.setAttribute("data-sidebar-rail", "");
    } else {
      document.body.removeAttribute("data-sidebar-rail");
    }
    try {
      window.localStorage.setItem(SIDEBAR_MODE_KEY, mode);
    } catch (e) {
      // localStorage may be disabled; flip still works for this session.
    }
    syncRailToggleLabel();
  }

  function cycleSidebarMode() {
    var order = { rail: "pane", pane: "auto", auto: "rail" };
    applySidebarMode(order[readSidebarMode()]);
  }

  // Apply persisted mode ASAP — before first paint when possible.
  applySidebarMode(readSidebarMode());

  // In auto the effective state follows the viewport: re-resolve on crossings.
  if (window.matchMedia) {
    var sidebarModeMq = window.matchMedia(SIDEBAR_DESKTOP_QUERY);
    var onSidebarModeMq = function () {
      if (readSidebarMode() === "auto") applySidebarMode("auto");
    };
    if (sidebarModeMq.addEventListener) sidebarModeMq.addEventListener("change", onSidebarModeMq);
    else if (sidebarModeMq.addListener) sidebarModeMq.addListener(onSidebarModeMq);
  }
```

In the click handler (:1181-1189) replace `toggleRail();` with `cycleSidebarMode();`. In the ⌘B handler (:1204-1213) replace `toggleRail();` with `cycleSidebarMode();` (the phone guard at :1210 stays). Update the ⌘B comment (:1191-1195) to say it cycles `rail → pane → auto`.

In `syncRailToggleLabel` (:1218-1224), make the label name the mode so a press that doesn't change the pixels (pane→auto on desktop) still produces a state-visible change:

```js
  function syncRailToggleLabel() {
    var btn = document.querySelector("[data-sidebar-rail-toggle]");
    if (!btn) return;
    var mode = readSidebarMode();
    var eff = effectiveSidebarState(mode);
    btn.setAttribute("aria-label", "sidebar: " + mode + (mode === "auto" ? " (" + eff + ")" : ""));
    btn.setAttribute("title", "sidebar: " + mode + (mode === "auto" ? " (" + eff + ")" : "") + " (⌘B)");
  }
```

Extend the export at :1243:

```js
  window.SerfSidebar = { renderTree: renderTree, refresh: fetchTree, favorite: favorite, archive: archive, rename: rename, close: function () { setSidebarOpen(false); }, applySidebarMode: applySidebarMode, readSidebarMode: readSidebarMode, effectiveSidebarState: effectiveSidebarState, cycleSidebarMode: cycleSidebarMode };
```

- [ ] **Step 4: settings-appearance.js + theme.html**

Replace the `sidebar-mode` radio handler (:26-32) with:

```js
    if (target.matches('input[name="sidebar-mode"]')) {
      const v = target.value; // "auto" | "rail" | "pane"
      if (window.SerfSidebar && window.SerfSidebar.applySidebarMode) {
        window.SerfSidebar.applySidebarMode(v);
      } else {
        localStorage.setItem("serf-hub.sidebar.rail", v);
      }
      return;
    }
```

Replace the sidebar-mode reflect block (:56-60) with:

```js
    const sidebarModeRadios = document.querySelectorAll('input[name="sidebar-mode"]');
    if (sidebarModeRadios.length) {
      const stored = (window.SerfSidebar && window.SerfSidebar.readSidebarMode)
        ? window.SerfSidebar.readSidebarMode()
        : (localStorage.getItem("serf-hub.sidebar.rail") || "auto");
      sidebarModeRadios.forEach((r) => { r.checked = r.value === stored; });
    }
```

Delete the trailing sidebar-mode init IIFE (:79-85, `// Sidebar mode (pane / rail) — apply stored value…`) — sidebar.js is the single writer of the rail attributes on the app shell (confirm: `templates/app.html` loads `sidebar.js`; the settings page renders inside the app shell).

In `templates/partials/settings/theme.html` add the Auto option first in the group (:29-32):

```html
      <div class="val-radio-group" data-sidebar-mode-picker>
        <label class="val-radio"><input type="radio" name="sidebar-mode" value="auto"> Auto</label>
        <label class="val-radio"><input type="radio" name="sidebar-mode" value="pane"> Pane</label>
        <label class="val-radio"><input type="radio" name="sidebar-mode" value="rail"> Rail</label>
      </div>
```

- [ ] **Step 5: design-system.md §5 addendum (same commit)**

At the end of §5 in `docs/web-ui/design-system.md` (after the project-header paragraph, ~:257), add:

```markdown
**Tri-state mode (2026-07-19 addendum).** The sidebar mode is `auto | rail | pane`,
persisted per-browser. `auto` (the default) rails below 1200px and expands at/above it;
`rail`/`pane` pin the state. ⌘B cycles `rail → pane → auto`. The legacy binary
preference migrates: collapsed→`rail`, expanded→`pane`, unset→`auto`.
```

- [ ] **Step 6: Run tests + gate**

Run: `cd cmd/serf-hub/jstest && node test-sidebar-tristate.js && ./run-all.sh && cd ../../.. && make build-hub`
Expected: PASS, suite green. If `test-settings-appearance.js` asserts the two-value radio sync, update it for the third value and the `applySidebarMode` delegation (same commit).

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js cmd/serf-hub/assets/settings-appearance.js cmd/serf-hub/templates/partials/settings/theme.html cmd/serf-hub/jstest/test-sidebar-tristate.js docs/web-ui/design-system.md
git commit -m "web: sidebar tri-state (auto|rail|pane) with migration and one effective-state helper"
```

---

### Task 5: Picker panels clamp inside the viewport at tablet widths

**Files:**
- Modify: `cmd/serf-hub/assets/dir-picker.js` (:19-22 anchored branch)
- Modify: `cmd/serf-hub/assets/spawn.js` (`placeChipPicker` anchored branch — locate via `grep -n 'placeChipPicker' assets/spawn.js`)
- Test: `cmd/serf-hub/jstest/test-picker-clamp.js` (create)

**Interfaces:**
- Produces: both pickers keep the ≤767px bottom-sheet path; the anchored path clamps `left` so the fixed-width panel never overflows the viewport's right edge.

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-picker-clamp.js`:

```js
// Anchored chip pickers clamp inside the viewport (tablet band: an anchor
// near the right edge must not push the fixed-width panel off-screen).
const fs = require("fs");
const { JSDOM } = require("jsdom");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

// Behavioral: dir-picker's placeChipPicker clamps left (exported as a test
// seam by this task — see Step 3).
const src = fs.readFileSync(__dirname + "/../assets/dir-picker.js", "utf8");
const dom = new JSDOM(`<!DOCTYPE html><html><body><button id="chip">dir</button></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
const w = dom.window;
w.matchMedia = (q) => ({ matches: false }); // anchored (non-phone) path
Object.defineProperty(w, "innerWidth", { value: 900, configurable: true });
w.eval(src);
assert(w.SerfDirPicker && typeof w.SerfDirPicker.placeChipPicker === "function",
  "placeChipPicker exported as a test seam");
const anchor = w.document.getElementById("chip");
Object.defineProperty(anchor, "offsetLeft", { value: 880 });
Object.defineProperty(anchor, "offsetTop", { value: 40 });
Object.defineProperty(anchor, "offsetHeight", { value: 30 });
const picker = w.document.createElement("div");
Object.defineProperty(picker, "offsetWidth", { value: 520 });
w.SerfDirPicker.placeChipPicker(picker, anchor);
assert(picker.style.left !== "880px", "left is clamped away from the anchor's raw offset");
assert(parseInt(picker.style.left, 10) <= 900 - 520 - 8, "panel right edge stays inside the viewport");
assert(parseInt(picker.style.left, 10) >= 8, "clamp never pushes past the left edge");

// Mirror: spawn.js carries the same clamp (its placeChipPicker is a copy).
const spawnSrc = fs.readFileSync(__dirname + "/../assets/spawn.js", "utf8");
assert(/maxLeft/.test(spawnSrc) && /offsetWidth/.test(spawnSrc),
  "spawn.js placeChipPicker mirrors the viewport clamp");
console.log("ok picker viewport clamp");
process.exit(0);
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-picker-clamp.js`
Expected: FAIL with "left is clamped away from the anchor's raw offset"

- [ ] **Step 3: Implement**

In `dir-picker.js`, replace the anchored branch (:19-22):

```js
    // Clamp inside the viewport: at tablet widths an anchor near the right
    // edge would push the fixed-width panel off-screen.
    var pw = picker.offsetWidth || 520;
    var maxLeft = Math.max(8, (global.innerWidth || 1024) - pw - 8);
    picker.style.left = Math.min(anchor.offsetLeft, maxLeft) + "px";
    picker.style.top = (anchor.offsetTop + anchor.offsetHeight + 4) + "px";
    picker.style.zIndex = "50";
```

In `spawn.js`, find `placeChipPicker` (`grep -n 'function placeChipPicker' assets/spawn.js` — :469 at base commit) and apply the identical clamp to its anchored branch (same variable names — the two functions are documented mirrors).

Export the seam in `dir-picker.js` (the test calls it directly):

```js
  global.SerfDirPicker = {
    open: openDirPicker,
    placeChipPicker: placeChipPicker, // test seam (test-picker-clamp.js)
  };
```

- [ ] **Step 4: Run tests + gate**

Run: `cd cmd/serf-hub/jstest && node test-picker-clamp.js && ./run-all.sh && cd ../../.. && make build-hub`
Expected: PASS, suite green.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/dir-picker.js cmd/serf-hub/assets/spawn.js cmd/serf-hub/jstest/test-picker-clamp.js
git commit -m "web: clamp anchored chip pickers inside the viewport at tablet widths"
```

---

### Task 6: Home launchpad — server-rendered "Jump back in"

**Files:**
- Create: `cmd/serf-hub/templates/partials/workspace_empty.html`
- Modify: `cmd/serf-hub/web.go` (:70-73 template registration, :374-384 `handleWorkspaceEmpty`)
- Modify: `cmd/serf-hub/assets/style.css` (launchpad rules after the empty-state block, ~:5580)
- Test: `cmd/serf-hub/web_launchpad_test.go` (create)

**Interfaces:**
- Consumes: `s.memoTree(ctx)` → `hubcore.Tree` (`Projects[].Current/Recent`, `ArchivedProjects`, `IsArchived`, `IsTestRun`; `TreeNode{ID, Title, Project, Age, Kind, UpdatedAt}`).
- Produces: `/_partials/workspace/empty` renders the launchpad. Template name `workspace_empty`, data type:

```go
type workspaceEmptyData struct {
    Rows        []launchpadRow
    AllArchived bool
}
type launchpadRow struct {
    ID, Href, PartialHref, Title, Project, Age string
}
```

- [ ] **Step 1: Write the failing Go test**

Create `cmd/serf-hub/web_launchpad_test.go`:

```go
package main

import (
	"context"
	"html/template"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/identifier"
)

// launchpadServer stubs the navigation inputs so memoTree builds a fixed tree.
func launchpadServer(t *testing.T, metas []schema.SessionMeta) *WebServer {
	t.Helper()
	orig := hubNavigationInputs
	hubNavigationInputs = func(*WebServer, context.Context) ([]schema.SessionMeta, []hubcore.LiveEntry, map[string]identifier.Project) {
		return metas, nil, map[string]identifier.Project{}
	}
	t.Cleanup(func() { hubNavigationInputs = orig })
	tmpl := template.Must(template.New("workspace_empty.html").ParseFS(templatesRoot(), "templates/partials/workspace_empty.html"))
	return &WebServer{
		workspaceEmptyTmpl: tmpl,
		treeCache:          &hubcore.TreeCache{},
	}
}

func renderEmpty(t *testing.T, s *WebServer) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/_partials/workspace/empty", nil)
	rec := httptest.NewRecorder()
	s.handleWorkspaceEmpty(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return string(body)
}

func TestWorkspaceEmptyLaunchpadRendersRecentSessions(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01OLD", OriginalPrompt: "older session", UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "01NEW", OriginalPrompt: "newer session", UpdatedAt: now.Add(-time.Hour)},
	}
	body := renderEmpty(t, launchpadServer(t, metas))
	if !strings.Contains(body, "launchpad-list") {
		t.Fatalf("expected launchpad list, got:\n%s", body)
	}
	if strings.Index(body, "newer session") > strings.Index(body, "older session") {
		t.Fatalf("rows must sort by UpdatedAt desc:\n%s", body)
	}
	if !strings.Contains(body, `href="/s/01NEW"`) {
		t.Fatalf("rows link to the session route:\n%s", body)
	}
}

func TestWorkspaceEmptyLaunchpadEscapesTitles(t *testing.T) {
	metas := []schema.SessionMeta{
		{ID: "01XSS", OriginalPrompt: `<img src=x onerror=alert(1)>`, UpdatedAt: time.Now()},
	}
	body := renderEmpty(t, launchpadServer(t, metas))
	if strings.Contains(body, "<img src=x") {
		t.Fatalf("title must be HTML-escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;img src=x") {
		t.Fatalf("expected escaped title:\n%s", body)
	}
}

func TestWorkspaceEmptyZeroSessionsKeepsQuietWelcome(t *testing.T) {
	body := renderEmpty(t, launchpadServer(t, nil))
	if strings.Contains(body, "launchpad-list") {
		t.Fatalf("no sessions → no launchpad list:\n%s", body)
	}
	if !strings.Contains(body, "welcome-wordmark") {
		t.Fatalf("quiet wordmark welcome stays:\n%s", body)
	}
}
```

All-archived variant: add a case where every meta is archived (last activity older than the 2-week auto-archive window, e.g. `UpdatedAt: now.Add(-30 * 24 * time.Hour)`) and assert `welcome-wordmark` is present, `launchpad-list` is absent, and the search button label reads `Search all sessions`.

- [ ] **Step 2: Run test to verify it fails**

Run: `GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub -run TestWorkspaceEmpty -v`
Expected: FAIL to compile (`workspaceEmptyTmpl` undefined)

- [ ] **Step 3: Implement the template**

Create `cmd/serf-hub/templates/partials/workspace_empty.html`:

```html
{{define "workspace_empty"}}
<div class="empty-state empty-state-workspace">
  <div class="welcome-wordmark">serf<span class="welcome-dot">.</span></div>
  {{if .Rows}}
  <p class="empty-state-body">Jump back in, or start something new.</p>
  <ul class="launchpad-list">
    {{range .Rows}}
    <li><a class="launchpad-row" href="{{.Href}}" hx-get="{{.PartialHref}}" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="{{.Href}}">
      <span class="launchpad-title">{{.Title}}</span>
      <span class="launchpad-meta">{{if .Project}}<span class="launchpad-project">{{.Project}}</span> · {{end}}<span class="launchpad-age">{{.Age}}</span></span>
    </a></li>
    {{end}}
  </ul>
  {{else}}
  <p class="empty-state-body">Spawn a session, or pick one from the sidebar.</p>
  {{end}}
  <div class="empty-state-actions">
    <a class="btn btn-primary" href="/new" hx-get="/_partials/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ New session</a>
    <button class="btn btn-ghost" type="button" data-search-trigger>{{if .AllArchived}}Search all sessions{{else}}Search <kbd>⌘K</kbd>{{end}}</button>
  </div>
</div>
{{end}}
```

(html/template auto-escapes every interpolation — the XSS requirement is structural. No live status dots: the home page has no appwire connection, so the markup is age-only and can't go stale.)

- [ ] **Step 4: Implement the handler**

In `web.go` add the field to `WebServer` (next to `workspaceTmpl`):

```go
	workspaceEmptyTmpl  *template.Template
```

In `NewWebServer`, after `workspaceTmpl` is parsed, add:

```go
	workspaceEmptyTmpl := template.Must(template.New("workspace_empty.html").ParseFS(templatesRoot(), "templates/partials/workspace_empty.html"))
```

and set `workspaceEmptyTmpl: workspaceEmptyTmpl` in the struct literal.

Replace `handleWorkspaceEmpty` (:374-384) with:

```go
type launchpadRow struct {
	ID, Href, PartialHref, Title, Project, Age string
}

type workspaceEmptyData struct {
	Rows        []launchpadRow
	AllArchived bool
}

// handleWorkspaceEmpty renders the home launchpad: up to 8 sessions across
// projects from the Current+Recent tiers, most-recently-touched first. No
// live status dots — the home page has no appwire connection, so age-only
// markup can't go stale.
func (s *WebServer) handleWorkspaceEmpty(w http.ResponseWriter, r *http.Request) {
	tree, _ := s.memoTree(r.Context())
	var sessions []hubcore.TreeNode
	for _, p := range tree.Projects {
		if p.IsArchived || p.IsTestRun {
			continue
		}
		for _, n := range append(append([]hubcore.TreeNode{}, p.Current...), p.Recent...) {
			if n.Kind == "session" || n.Kind == "fork" {
				sessions = append(sessions, n)
			}
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	if len(sessions) > 8 {
		sessions = sessions[:8]
	}
	data := workspaceEmptyData{}
	for _, n := range sessions {
		title := n.Title
		if title == "" {
			title = "Untitled session"
		}
		data.Rows = append(data.Rows, launchpadRow{
			ID: n.ID, Href: "/s/" + n.ID, PartialHref: "/_partials/s/" + n.ID + "/workspace",
			Title: title, Project: n.Project, Age: n.Age,
		})
	}
	// Every session archived (or none): the quiet wordmark welcome is honest;
	// when archived sessions exist the search affordance says so.
	data.AllArchived = len(data.Rows) == 0 && (len(tree.Projects) > 0 || len(tree.ArchivedProjects) > 0)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.workspaceEmptyTmpl.ExecuteTemplate(w, "workspace_empty", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
```

Add `"sort"` to the imports; `hubcore` is already imported. Note: node IDs carry the ref form for remote threads (`appThreadTreeEntries` sets `meta.ID = ref.String()`), so `/s/<id>` routes both local and remote sessions exactly like the sidebar's `sessionHref`.

- [ ] **Step 5: Launchpad CSS**

After the `.empty-state-sidebar …` rules (~:5580), add:

```css
/* Home launchpad — "Jump back in". Age-only rows: no live dots (the home
   page has no socket), so nothing here can go stale. */
.launchpad-list { list-style: none; margin: var(--space-2) 0 0; padding: 0; width: 100%; max-width: 26rem; display: flex; flex-direction: column; gap: var(--space-1); }
.launchpad-row { display: flex; align-items: baseline; gap: var(--space-3); padding: var(--space-2) var(--space-3); border-radius: var(--radius-md); text-decoration: none; color: var(--text); }
.launchpad-row:hover { background: var(--bg-raised); }
.launchpad-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; text-align: left; font-size: var(--text-base); }
.launchpad-meta { flex: none; color: var(--text-muted); font-size: var(--text-xs); font-variant-numeric: tabular-nums; }
```

- [ ] **Step 6: Run tests + gate**

Run: `GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub -run TestWorkspaceEmpty -v && GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub && cd cmd/serf-hub/jstest && ./run-all.sh && cd ../../.. && make build-hub`
Expected: PASS everywhere. If an existing test asserts the old static empty markup (grep: `grep -rn 'empty-state-workspace\|welcome-wordmark' cmd/serf-hub/*_test.go`), update it to the template output (same commit).

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/templates/partials/workspace_empty.html cmd/serf-hub/web.go cmd/serf-hub/web_launchpad_test.go cmd/serf-hub/assets/style.css
git commit -m "web: home launchpad — server-rendered 'Jump back in' (escaped, age-only, ≤8 sessions)"
```

---

### Task 7: Token migration — canonical definitions, scripted rename, legacy/dead deletion

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (4 theme blocks :4-231; ~600 use sites; :270-275 and :5585-5600 dead/duplicate rules)
- Modify: `docs/web-ui/design-system.md` (§3 neutral-ramp alignment)
- Test: `cmd/serf-hub/jstest/test-color-system-css.js` (rewrite in place)

**Interfaces:**
- Produces: canonical tokens `--surface`, `--surface-2`, `--line`, `--hair`, `--ink`, `--ink-2`, `--ink-3`, `--ink-4`, `--attention`, `--done` defined in all 4 theme blocks; zero legacy names remain. Task 8 consumes `--attention`/`--done`.

**Mapping table (verified counts at base commit):**

| Legacy | Canonical | Use sites |
|---|---|---|
| `var(--text)` | `var(--ink)` | 159 |
| `var(--text-muted)` | `var(--ink-2)` | 256 |
| `var(--text-dim)` | `var(--ink-4)` | 43 |
| `var(--rule)` | `var(--line)` | 104 |
| `var(--rule-soft)` | `var(--hair)` | 3 |
| `var(--bg-raised)` | `var(--surface)` | 59 |
| `var(--surface-secondary)` | `var(--surface-2)` | 24 |
| `var(--panel)` | `var(--surface)` | 4 |
| `var(--panel-2)` | `var(--surface-2)` | 4 |
| `var(--border)` | `var(--line)` | 10 |
| `var(--muted)` | `var(--ink-2)` | 7 |
| `var(--tool)` | `var(--state-warning)` | 1 |
| `var(--user)` | `var(--ink-2)` | 1 |
| `var(--accent-secondary)` | `var(--accent)` | 1 |

(`--text-2xs`…`--text-2xl` — the type scale — is canonical and untouched. The exact-paren patterns below can't match it. All uses live in `style.css`; JS/templates have none — verified by grep at plan time, re-verify in Step 3.)

- [ ] **Step 1: Rewrite the failing contract test**

Replace `cmd/serf-hub/jstest/test-color-system-css.js` with:

```js
// Canonical color-token vocabulary: canonicals defined in all 4 theme blocks;
// no legacy names anywhere (the --text-* TYPE SCALE --text-2xs…--text-2xl is
// canonical and exempt — exact-paren/colon patterns can't match it).
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

// 4 theme blocks: :root, @media light :root, [data-theme="dark"], [data-theme="light"].
const blocks = [
  [/^:root \{([\s\S]*?)\n\}/m, ":root"],
  [/@media \(prefers-color-scheme: light\) \{\s*:root \{([\s\S]*?)\n  \}\n\}/, "media-light"],
  [/:root\[data-theme="dark"\] \{([\s\S]*?)\n\}/, "force-dark"],
  [/:root\[data-theme="light"\] \{([\s\S]*?)\n\}/, "force-light"],
];
const canonical = ["--surface", "--surface-2", "--line", "--hair", "--ink", "--ink-2", "--ink-3", "--ink-4", "--attention", "--done"];
for (const [re, name] of blocks) {
  const m = css.match(re);
  assert(m, "theme block found: " + name);
  for (const tok of canonical) {
    assert(m[1].includes(tok + ":"), name + " defines " + tok);
  }
  assert(!m[1].includes("--panel"), name + " has no legacy aliases");
}
assert(/--ink-3:\s*#7e8593;/.test(css), "dark --ink-3 takes the doc value #7e8593 (AA on raised)");
assert(/--ink-3:\s*#6b6b76;/.test(css), "light --ink-3 is #6b6b76 (4.66:1 on --surface)");

const legacy = [
  /var\(--text\)/, /var\(--text-muted\)/, /var\(--text-dim\)/,
  /var\(--rule\)/, /var\(--rule-soft\)/, /var\(--bg-raised\)/, /var\(--surface-secondary\)/,
  /var\(--panel\)/, /var\(--panel-2\)/, /var\(--border\)/, /var\(--muted\)/,
  /var\(--tool\)/, /var\(--user\)/, /var\(--accent-secondary\)/,
  /\s--text:/, /--text-muted:/, /--text-dim:/, /--rule:/, /--rule-soft:/,
  /--bg-raised:/, /--surface-secondary:/, /--panel:/, /--panel-2:/,
  /--border:/, /--muted:/, /--tool:/, /--user:/, /--accent-secondary:/,
];
for (const re of legacy) {
  assert(!re.test(css), "no legacy token matching " + re);
}
// Dead CSS stays dead: one .btn:active transform rule (the scale-pop).
assert(!/\.composer-send/.test(css) && !/#send-btn/.test(css), "dead .composer-send/#send-btn selectors gone");
assert((css.match(/\.btn:active/g) || []).length <= 2, "no duplicate .btn:active transform blocks");
console.log("ok canonical color tokens");
```

(If the file already has other assertions worth keeping, fold them in; the rewrite must keep the suite's existing intent.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-color-system-css.js`
Expected: FAIL (canonicals undefined / legacy names present)

- [ ] **Step 3: Scripted use-site rename**

Re-verify the scope, then run the rename:

```bash
cd cmd/serf-hub
grep -rn -- 'var(--text)\|var(--text-muted)\|var(--text-dim)\|var(--rule)\|var(--rule-soft)\|var(--bg-raised)\|var(--surface-secondary)\|var(--panel)\|var(--panel-2)\|var(--border)\|var(--muted)\|var(--tool)\|var(--user)\|var(--accent-secondary)' assets/*.js templates/ && echo "UNEXPECTED non-CSS uses — handle by hand"
sed -i \
  -e 's/var(--text)/var(--ink)/g' \
  -e 's/var(--text-muted)/var(--ink-2)/g' \
  -e 's/var(--text-dim)/var(--ink-4)/g' \
  -e 's/var(--rule-soft)/var(--hair)/g' \
  -e 's/var(--rule)/var(--line)/g' \
  -e 's/var(--bg-raised)/var(--surface)/g' \
  -e 's/var(--surface-secondary)/var(--surface-2)/g' \
  -e 's/var(--panel-2)/var(--surface-2)/g' \
  -e 's/var(--panel)/var(--surface)/g' \
  -e 's/var(--border)/var(--line)/g' \
  -e 's/var(--muted)/var(--ink-2)/g' \
  -e 's/var(--tool)/var(--state-warning)/g' \
  -e 's/var(--user)/var(--ink-2)/g' \
  -e 's/var(--accent-secondary)/var(--accent)/g' \
  assets/style.css
grep -c 'var(--ink)' assets/style.css   # expect ≥159+7+1=167 after definitions land
```

- [ ] **Step 4: Rewrite the 4 theme blocks**

Replace the token heads of all four theme blocks (definitions only — keep `--state-*`, `--error`, `--success`, diff palette, diagnostics, spacing/type/motion/z sections as they are). The `:root` block's color head becomes:

```css
:root {
  --bg: #0a0a0e;
  --surface: #16161e;
  --surface-2: #1c1c24;
  --ink: #ececf0;
  --ink-2: #7a7a86;
  /* Doc value (design-system §3): shipped #7a7a86 measured 4.24:1 on raised
     surfaces (sub-AA); #7e8593 passes on both. */
  --ink-3: #7e8593;
  /* Hairlines / non-text only — never words (sub-AA by design). */
  --ink-4: #5a5a64;
  --line: #1a1a20;
  /* The faint hairline (composer top rule, quiet separators). */
  --hair: color-mix(in srgb, var(--line) 50%, transparent);
  --accent: #7aa2f7;
  /* Amber = needs-you. Consumed by the state tokens (see below). */
  --attention: #e0af68;
  /* Neutral = done/settled. Recedes — it is --ink-3, not a color. */
  --done: var(--ink-3);
```

The other three blocks get the same names with their theme values:

- `@media (prefers-color-scheme: light)` `:root`: `--bg: #fafafa; --surface: #f1f1f2; --surface-2: #e6e6e8; --ink: #16161e; --ink-2: #5e5e6a; --ink-3: #6b6b76; --ink-4: #8a8a92; --line: #dadadc; --hair: color-mix(in srgb, var(--line) 50%, transparent); --accent: #2e58b8; --attention: #8a5a14; --done: var(--ink-3);`
- `:root[data-theme="dark"]`: same values as `:root`.
- `:root[data-theme="light"]`: same values as the media-light block.

In every block: delete the legacy alias comment + `--panel`/`--panel-2`/`--border`/`--muted`/`--tool`/`--user` lines, delete the old `--text`/`--text-muted`/`--text-dim`/`--rule`/`--rule-soft`/`--bg-raised`/`--surface-secondary`/`--accent-secondary` definition lines, and update the `--diagnostic-*` lines to their post-rename references (`--diagnostic-ui: var(--accent);` — the sed already rewrote its value; `--diagnostic-serf`/`--diagnostic-hub` keep referencing `--state-warning`/`--state-working` until Task 8). `--btn-primary-text` stays as-is.

- [ ] **Step 5: Dead CSS**

At :270-275, remove the two dead selectors so the rule reads:

```css
.btn:active,
.btn-primary:active,
.app-nav-toggle:active,
.sidebar-action:active { transform: translateY(0.5px) scale(0.97); transition-timing-function: var(--ease-emphasis); }
```

In the later "Pass 8 — :active press states" block (:5585-5600), remove ONLY the transform declarations that cancel the scale-pop (keep the surface drops):

```css
/* Pass 8 — :active press states (drop surface one step). The transform lives
   in the single scale-pop rule above; these only restyle the surface. */
.btn-primary:active { filter: brightness(0.95); }
.btn-secondary:active { background: var(--surface); }
.btn-ghost:active { background: var(--surface); color: var(--ink); }
.btn-danger:active { background: color-mix(in srgb, var(--error) 10%, transparent); }
```

(delete the `.btn:active, .btn-secondary:active, … { transform: translateY(0.5px); }` group entirely; the `var(--surface)`/`var(--ink)` names above reflect the Step 3 sed already applied.)

- [ ] **Step 6: design-system.md §3 alignment (same commit)**

Replace the neutral-ramp line in §3 (:69-71) with:

```markdown
Neutral ramp (aligned to shipped values, 2026-07-19): `--bg #0a0a0e`, `--surface #16181e`→**`#16161e`**,
`--surface-2 #1c1c24`, rules `--line #1a1a20` / `--hair` (a 50% `--line` mix — the *faint* hairline,
superseding the old `#2d313b` note); text `--ink #ececf0` (primary), `--ink-2 #7a7a86` (secondary),
`--ink-3 #7e8593` (tertiary, AA ~4.8:1), `--ink-4 #5a5a64` (**hairlines / non-text only** — never words).
Light theme: `--bg #fafafa`, `--surface #f1f1f2`, `--surface-2 #e6e6e8`, `--line #dadadc`,
`--ink #16161e`, `--ink-2 #5e5e6a`, `--ink-3 #6b6b76`, `--ink-4 #8a8a92`.
```

(Write it as clean replacement prose, not with the strike-through annotation style above — the doc should read as current truth.)

- [ ] **Step 7: Run tests + gate**

Run: `cd cmd/serf-hub/jstest && node test-color-system-css.js && ./run-all.sh && cd ../../.. && make build-hub`
Expected: the new contract test passes. Other jstest files that assert old token names FAIL — update each one's expected names per the mapping table (mechanical, same commit). Known candidates: `test-style-palette.js`, `test-mobile-css.js`, `test-pane-and-sidebar-css.js`, `test-sidebar-density-css.js`, `test-sidebar-polish-css.js`, `test-errored-light-tint.js`, `test-context-pressure-css.js`, `test-transcript-typography.js`, `test-font-size-presets.js` (should be unaffected — type-scale carve-out). Then `GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub`.

- [ ] **Step 8: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/ docs/web-ui/design-system.md
git commit -m "web: canonical color-token vocabulary (alias → rename → delete) + dead CSS removal"
```

---

### Task 8: State colors — blue=live, amber=needs-you, diagnostics lane, neutral favicon

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (4 theme blocks; :13-17 comment; 31 `--state-warning` uses)
- Modify: `cmd/serf-hub/assets/notifications.js` (:31-33 PLAIN_FAVICON, :35-43 STATE_COLORS, ~:127 buildFaviconDataURI base fill)
- Modify: `cmd/serf-hub/templates/thread.html` (:8 favicon)
- Test: `cmd/serf-hub/jstest/test-color-system-css.js`, `test-notifications-palette.js`, `test-style-palette.js`, `test-context-pressure-css.js`, `test-subagents.js` (update in place)

**Interfaces:**
- Consumes: `--accent`, `--attention`, `--done` from Task 7.
- Produces: `--state-working` = blue, `--state-awaiting` = amber, `--diagnostic-warning` (renamed from `--state-warning`), `--diagnostic-hub` = green.

- [ ] **Step 1: Extend the failing contract test**

Append to `test-color-system-css.js`:

```js
// State language: blue=live, amber=needs-you, red=error, neutral=done.
assert(/--state-working:\s*var\(--accent\);/.test(css), "working = blue (--accent)");
assert(/--state-awaiting:\s*var\(--attention\);/.test(css), "awaiting = amber (--attention)");
assert(/--state-idle:\s*var\(--done\);/.test(css), "idle = neutral (--done)");
assert(/--state-subagent:\s*var\(--done\);/.test(css), "subagent = neutral (--done)");
assert(!/--state-warning/.test(css), "--state-warning renamed away (diagnostics lane)");
assert(/--diagnostic-warning:\s*#e0af68;/.test(css), "dark --diagnostic-warning keeps the amber identity");
assert(/--diagnostic-hub:\s*#7dc98f;/.test(css), "dark --diagnostic-hub takes the freed green (≠ --diagnostic-ui blue)");
```

And create `cmd/serf-hub/jstest/test-favicon-language.js`:

```js
// Favicon language: pinned dark-theme constants (the icon renders against
// dark browser chrome regardless of page theme). Base circle is NEUTRAL —
// post-recolor blue means working, so a blue base would read "working" at rest.
const fs = require("fs");
const js = fs.readFileSync(__dirname + "/../assets/notifications.js", "utf8");
const thread = fs.readFileSync(__dirname + "/../templates/thread.html", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

assert(/%237e8593/.test(js), "PLAIN_FAVICON base is neutral #7e8593");
assert(/fill='#7e8593'/.test(js), "buildFaviconDataURI base circle is neutral");
assert(/needs_you:\s*"#e0af68"/.test(js), "needs_you dot is amber");
assert(/working:\s*"#7aa2f7"/.test(js), "working dot is blue");
assert(/error:\s*"#f7768e"/.test(js), "error dot stays red");
assert(!/%237aa2f7/.test(thread), "thread.html favicon is no longer blue");
assert(/%237e8593/.test(thread), "thread.html favicon is neutral");
console.log("ok favicon language");
process.exit(0);
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/jstest && node test-color-system-css.js; node test-favicon-language.js`
Expected: FAIL (old hues / old favicon)

- [ ] **Step 3: Re-hue + rename in style.css**

In all 4 theme blocks, change the state definitions:

- dark blocks (`:root`, `:root[data-theme="dark"]`):

```css
  --state-working: var(--accent);
  --state-awaiting: var(--attention);
  --state-idle: var(--done);
  --state-ended: #3a3a44;
  /* Subagents carry no ambient hue: blue when live, neutral when done. */
  --state-subagent: var(--done);
  /* Diagnostics lane — not session state. Warning keeps amber (its own
     identity, distinct from needs-you's container grammar); hub takes the
     green freed by live→blue so it can't collide with --diagnostic-ui. */
  --diagnostic-warning: #e0af68;
```

- light blocks: identical `var()` lines; `--state-ended: #7a7a82;`; `--diagnostic-warning: #8a5a14;`; `--diagnostic-hub: #2e7d4f;`
- dark blocks: `--diagnostic-hub: #7dc98f;` (replacing `--diagnostic-hub: var(--state-working);`)
- light blocks: `--diagnostic-hub: #2e7d4f;`
- `--diagnostic-serf: var(--diagnostic-warning);` (was `var(--state-warning)`)

Delete every `--state-warning:` definition line, then rename all uses:

```bash
cd cmd/serf-hub
sed -i 's/var(--state-warning)/var(--diagnostic-warning)/g' assets/style.css
grep -rn -- '--state-warning' assets/ templates/ && echo "LEFTOVER — fix by hand"
grep -rn '#7dc98f\|#2e7d4f' assets/*.js templates/ && echo "hardcoded old working-green — fix by hand"
```

Replace the stale comment (:13-17) with:

```css
  /* Four meanings, each exactly one (design-system §3): blue=live/working,
     amber=needs-you (awaiting), red=error, neutral=done/settled. The old
     warning tier left the state vocabulary: diagnostics use
     --diagnostic-warning (amber — their own lane, not a session state). */
```

- [ ] **Step 4: Favicon**

In `notifications.js`:

```js
  const PLAIN_FAVICON =
    "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><circle cx='50' cy='50' r='40' fill='%237e8593'/></svg>";

  // Dot color by attention level. Pinned DARK-THEME constants — the favicon
  // renders against dark browser chrome regardless of the page's active
  // theme. Post-recolor language: blue=working, amber=needs_you, red=error.
  // The base circle is neutral (#7e8593 = dark --ink-3): a blue base would
  // read as "working" at rest. No "idle" entry: idle never sets a dot.
  const STATE_COLORS = {
    error: "#f7768e",
    needs_you: "#e0af68",
    working: "#7aa2f7",
  };
```

In `buildFaviconDataURI` change the base circle `fill='#7aa2f7'` → `fill='#7e8593'`.

In `thread.html` :8 change `fill='%237aa2f7'` → `fill='%237e8593'`.

- [ ] **Step 5: Run tests + gate**

Run: `cd cmd/serf-hub/jstest && ./run-all.sh && cd ../../.. && make build-hub && GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub`
Expected: the two updated/new tests pass. `test-notifications-palette.js`, `test-style-palette.js`, `test-context-pressure-css.js`, `test-subagents.js` hard-assert the old world — rewrite their expectations to the new language (green→blue for working/live, blue→amber for awaiting/needs-you, `--state-warning`→`--diagnostic-warning`, favicon neutral base), same commit.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/assets/notifications.js cmd/serf-hub/templates/thread.html cmd/serf-hub/jstest/
git commit -m "web: state colors — blue=live, amber=needs-you, diagnostics lane, neutral base favicon"
```

---

### Task 9: Contrast pass — `--ink-4` stops coloring words

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (~36 `var(--ink-4)` color sites)
- Test: `cmd/serf-hub/jstest/test-contrast-css.js` (create)

**Interfaces:**
- Consumes: `--ink-3`, `--ink-4` from Task 7.
- Rule (from design-system §3): `--ink-4` is **hairlines / non-text only — never words**. Text, numbers, and meaning-carrying glyphs (carets, chevrons, separators, markers, status glyphs) use `--ink-3` minimum. Purely decorative fills and borders may stay `--ink-4`.

**Keep at `--ink-4` (decorative, non-text — verified at base commit):**
`.project-rollup-dot` background; `.assistant-message code` border-bottom; `.subs[data-stale="true"]` border-left-color; `.task-card-meter-fill` background; the 10px radio-dot `border` (~:3796); `.details-meter-fill` background; the phone sheet grab-handle background (~:4735). Everything else that says `color: var(--ink-4)` becomes `var(--ink-3)`.

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-contrast-css.js`:

```js
// Contrast: --ink-4 never colors words. It may appear ONLY in the
// documented decorative keep-list (fills and borders, not color:).
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

const keepPatterns = [
  /\.project-rollup-dot \{[^}]*background:\s*var\(--ink-4\)/,
  /\.assistant-message code \{[^}]*border-bottom:\s*1px solid var\(--ink-4\)/,
  /\.subs\[data-stale="true"\] \{[^}]*border-left-color:\s*var\(--ink-4\)/,
  /\.task-card-meter-fill \{[^}]*background:\s*var\(--ink-4\)/,
  /\.details-meter-fill \{[^}]*background:\s*var\(--ink-4\)/,
];
for (const re of keepPatterns) {
  assert(re.test(css), "keep-list site intact: " + re);
}
// No color: declaration may use --ink-4 anywhere.
assert(!/color:\s*var\(--ink-4\)/.test(css), "--ink-4 never colors words");
// Known word/glyph sites moved to --ink-3.
for (const sel of [".project-count", ".ask-question-num", ".tool-status-pending",
  ".notification-card-sub", ".tasks-list .task-row-chevron",
  "button.composer-model-value .caret", ".plan-item.pending .plan-glyph"]) {
  const block = css.match(new RegExp(sel.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + " \\{[^}]*\\}"));
  assert(block && block[0].includes("var(--ink-3)"), sel + " uses --ink-3");
}
console.log("ok contrast: --ink-4 is non-text only");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-contrast-css.js`
Expected: FAIL with "--ink-4 never colors words"

- [ ] **Step 3: Implement**

Enumerate and flip:

```bash
cd cmd/serf-hub
grep -n 'var(--ink-4)' assets/style.css
```

For every hit: if the declaration is `color:` → change to `var(--ink-3)`. If it is one of the keep-list declarations above → leave it. (Reference: 43 original `--text-dim` sites − 7 keep-list = ~36 flips. The sites include `.project-count`, `.subagent-parent-sep`, `.rollup-sep`, `.ask-question-num`, `.queue-preview-help`, `.caret`, `.user-image-cap-sep`, `li::marker`, `.tool-status-pending`, `.result-detail`, `.task-arrow`, `.g.unk`, `.steps`, `.age`, `.live`, `.plan-glyph` ×2, `.notification-card-sub`, `.notification-card-facts dt`, `.task-row-chevron`, `.spawn-row-caret`, and sidebar muted items.)

The `.think` tiers are already compliant (they bottom out at `--ink-2`, AA — the short tier's recede is carried by the always-quiet `.think` treatment and its short gist, per the shipped comment at the tier rules). Do not touch them.

- [ ] **Step 4: Run tests + gate**

Run: `cd cmd/serf-hub/jstest && node test-contrast-css.js && ./run-all.sh && cd ../../.. && make build-hub`
Expected: PASS, suite green.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-contrast-css.js
git commit -m "web: contrast — --ink-4 stops coloring words (~36 sites to --ink-3)"
```

---

### Task 10: Retired treatments — ALL-CAPS labels out, radius literals snapped to the two tokens

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (13 `text-transform: uppercase` sites; literal `border-radius` values)
- Test: `cmd/serf-hub/jstest/test-retired-treatments-css.js` (create)

**Interfaces:**
- Produces: zero `text-transform: uppercase` in the stylesheet; `border-radius` uses only `var(--radius-md)`, `var(--radius-pill)`, `0`, or the documented squircle exception.

- [ ] **Step 1: Write the failing test**

Create `cmd/serf-hub/jstest/test-retired-treatments-css.js`:

```js
// Retired treatments (design-system §3 "Retired from the old UI"): no
// ALL-CAPS label treatment; radius is the documented two tokens.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

assert(!/text-transform:\s*uppercase/.test(css), "no ALL-CAPS label treatment anywhere");

// Radius literals: only 0 (square), the two tokens, and the one documented
// squircle (30%, its own shape) may remain.
const literals = css.match(/border-radius:\s*[^v][^;]*/g) || [];
const bad = literals.filter((l) => !/border-radius:\s*var\(--radius-(md|pill)\)/.test(l)
  && !/^border-radius:\s*0[;\s]/.test(l)
  && !/30%/.test(l));
assert(bad.length === 0, "literal border-radius values snapped to tokens: " + JSON.stringify(bad));
console.log("ok retired treatments");
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-retired-treatments-css.js`
Expected: FAIL (13 uppercase sites; literal radii)

- [ ] **Step 3: Retire the 13 ALL-CAPS sites**

```bash
cd cmd/serf-hub
grep -n 'text-transform:\s*uppercase' assets/style.css
```

At base commit the 13 sites are: `section.panel h2` (:355), `.msg .role` (:441), :1744, :2281, `.task-list-body .task-type` (:2816), `.spawn-recent-header` (:3465), :3484, `.search-section-header` (:3573), `.settings-h3` (:3618), :3864, :3885, `.tasks-list .task-type-pill` (:4232), :4937. For each: delete the `text-transform: uppercase;` declaration and any accompanying `letter-spacing:` declaration, and if the rule carries `font-family: var(--font-mono)` on a human label (all 13 are labels), change it to `var(--font-sans)`. Machine text (paths, commands, code) keeps mono — none of these 13 qualify.

- [ ] **Step 4: Snap radius literals**

```bash
grep -n 'border-radius:\s*[0-9]' assets/style.css
```

Map: `3px`/`4px` → `var(--radius-md)`; `50%` → `var(--radius-pill)`; `16px 16px 0 0` → `var(--radius-md) var(--radius-md) 0 0`; `1px` → `var(--radius-md)` if it rounds a rectangle, else `0` if the element is intentionally square. Keep `0` values and the `30%` squircle (its comment already documents it as its own shape).

- [ ] **Step 5: Run tests + gate**

Run: `cd cmd/serf-hub/jstest && node test-retired-treatments-css.js && ./run-all.sh && cd ../../.. && make build-hub && GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub`
Expected: PASS, all gates green. Adjust the contract test's allowed-literal logic if a documented exception survives Step 4 — record the exception in a comment next to the value in style.css.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-retired-treatments-css.js
git commit -m "web: retire ALL-CAPS mono labels (13 sites); snap radius literals to the two tokens"
```

---

### Task 11: Final verification — full gate + visual matrix

**Files:**
- Modify: `docs/web-ui/design-system.md` (only if the matrix reveals drift)

- [ ] **Step 1: Full gate**

Run: `make build-hub && (cd cmd/serf-hub/jstest && ./run-all.sh) && GOCACHE=/tmp/serf-go-build-cache go test ./cmd/serf-hub`
Expected: all green.

- [ ] **Step 2: Playwright visual matrix (dev-time, not CI)**

Serve the worktree hub with dev assets (`SERF_HUB_ASSETS_DIR=<worktree>/cmd/serf-hub`) and screenshot: 390 / 768 / 1100 / 1440 / 2560 px widths **plus a short-height 1100×600 case**, × dark/light, × home/session. Review by eye against design-system.md: measure holds 720px at every width; machine bleed 1000→1200px at wide; dock spans the window with the card centered; tablet auto-rails the sidebar; home shows the launchpad; state colors read blue=live / amber=needs-you / neutral=done.

- [ ] **Step 3: Commit any fixes the matrix surfaced, then close out**

```bash
git add -p   # matrix fixes only
git commit -m "web: visual-matrix fixes"
```

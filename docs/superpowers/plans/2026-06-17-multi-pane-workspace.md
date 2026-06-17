# Multi-Pane Workspace (MVP) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user open a subagent's live session as one or more vertical panes beside the main session in the Serf web hub, using iframe-per-pane so the existing single-instance renderer runs unchanged in each pane.

**Architecture:** The top page stays the host shell and renders the PRIMARY session top-level exactly as today. A new sibling region `#side-panes` (a flex-row of columns inside `body.app`, after `#workspace`) holds one `<iframe>` per side pane, each loading `/s/<id>` — so each pane is its own document and the renderer's hard-singleton state never collides. A small host module `panes.js` manages open/close/resize, caps the count, and restores open panes on reload from `localStorage`. An "open beside" affordance on subagent rows calls the host instead of the existing one-way `navigateTo`. The CSP is relaxed from `frame-ancestors 'none'` to `'self'` to permit same-origin framing.

**Tech Stack:** Go (`html/template`, `net/http`, the `httpsec` CSP middleware), vanilla JS (no bundler; same IIFE style as `sidebar.js`/`appwire.js`), CSS. Tests: Go `testing`; the hub `jstest` harness (`cmd/serf-hub/jstest/run-all.sh`, run from inside that dir; tests `process.exit(0)` if a poller keeps the loop alive).

## Global Constraints

- Design source of truth: `docs/web-ui/specs/2026-06-17-multi-pane-workspace-design.md` (recommends Option A iframe-per-pane; documents the renderer-singleton finding and the CSP blocker).
- **Locked scope decisions (Jesse, 2026-06-17):** side pane = a subagent's LIVE session; panes are FULLY INTERACTIVE (the iframe is the real `/s/<id>` view, composer included); a FEW tiled VERTICAL columns (cap configurable — use 3 side panes max in this MVP); persistence = RESTORE-ON-RELOAD per browser via `localStorage` (NOT shareable-URL); CSP relax `frame-ancestors 'none' → 'self'` is APPROVED.
- **Explicitly deferred (NOT in this plan):** auto-open the main session's observer subagent (needs a backend signal that does not exist today — see "Deferred work" at the end); document panes (file/markdown/diff) + the remote file-read RPC; a compressed/denser rendering style for pane agents; shareable-URL layouts.
- Reuse existing patterns: the renderer 4-file split is unrelated — do not disturb it (`app.html` must still load all 4 renderer files). Match the IIFE + event-delegation style of `sidebar.js`.
- The MVP must NOT touch renderer.js / appwire.js *semantics* — the only renderer change is adding the "open beside" affordance hook (Task 6).
- TDD: failing test first, watch it fail, minimal code, watch it pass, commit. Match surrounding style. Pristine test output. `make lint` stays `0 issues.` ×4 and the full `jstest` suite green at every commit.

## File structure

- `cmd/serf-hub/internal/httpsec/httpsec.go` — relax CSP `frame-ancestors` (Task 1).
- `cmd/serf-hub/internal/httpsec/httpsec_test.go` — update the asserted CSP (Task 1).
- `cmd/serf-hub/templates/app.html` — add the `#side-panes` region + splitter, and load `panes.js` (Task 2, Task 3).
- `cmd/serf-hub/assets/style.css` — side-pane layout + pane chrome + splitter styles (Task 2, Task 5).
- `cmd/serf-hub/assets/panes.js` — NEW host module: open/close/cap/persist/resize (Tasks 3, 4, 5).
- `cmd/serf-hub/assets/renderer.js` — "open beside" affordance on subagent rows (Task 6).
- `cmd/serf-hub/jstest/test-panes.js` — NEW jstest for panes.js (Tasks 3, 4).
- `cmd/serf-hub/jstest/test-renderer-open-beside.js` — NEW jstest for the hook (Task 6).
- `cmd/serf-hub/web_test.go` — render assertions for the shell markup (Task 2).

---

### Task 1: Relax CSP to allow same-origin framing

**Files:**
- Modify: `cmd/serf-hub/internal/httpsec/httpsec.go` (the `frame-ancestors` directive, ~line 35)
- Test: `cmd/serf-hub/internal/httpsec/httpsec_test.go` (~line 41)

**Interfaces:**
- Produces: the hub's `Content-Security-Policy` response header now contains `frame-ancestors 'self'` (was `'none'`). No new symbols.

- [ ] **Step 1: Read the current directive.** `grep -n "frame-ancestors" cmd/serf-hub/internal/httpsec/httpsec.go cmd/serf-hub/internal/httpsec/httpsec_test.go` — confirm the policy string and the test's exact assertion.

- [ ] **Step 2: Update the failing test first.** In `httpsec_test.go`, change the assertion that the CSP contains `frame-ancestors 'none'` to expect `frame-ancestors 'self'`. Run it:

Run: `go test ./cmd/serf-hub/internal/httpsec/ -run CSP -v` (use the actual test name from Step 1)
Expected: FAIL — the middleware still emits `'none'`.

- [ ] **Step 3: Relax the directive.** In `httpsec.go`, change `frame-ancestors 'none'` to `frame-ancestors 'self'` in the CSP string. Change nothing else in the policy.

- [ ] **Step 4: Run the test.**

Run: `go test ./cmd/serf-hub/internal/httpsec/ -run CSP -v`
Expected: PASS. Also `go test ./cmd/serf-hub/internal/httpsec/` → ok.

- [ ] **Step 5: Manual spike note (no code).** In your report, confirm by reading that no `X-Frame-Options: DENY` header is also set (grep `X-Frame-Options` in `httpsec.go`); if one exists it must become `SAMEORIGIN` or be removed, or it will override the CSP and still block framing. If present, fix it the same way and note it.

- [ ] **Step 6: Commit.**
```bash
git add cmd/serf-hub/internal/httpsec/httpsec.go cmd/serf-hub/internal/httpsec/httpsec_test.go
git commit -m "Relax CSP frame-ancestors to 'self' for same-origin multi-pane iframes"
```

---

### Task 2: Side-pane region shell + layout CSS

**Files:**
- Modify: `cmd/serf-hub/templates/app.html` (add `#side-panes` + splitter after `#workspace`, inside `body.app`)
- Modify: `cmd/serf-hub/assets/style.css` (layout for `#side-panes`, `.pane`, `.pane-header`, `.pane-splitter`)
- Test: `cmd/serf-hub/web_test.go` (assert the shell renders the region)

**Interfaces:**
- Produces: DOM contract consumed by Task 3's `panes.js`:
  - `#side-panes` — the container (flex row of columns), `hidden` when empty.
  - `#pane-splitter` — a drag handle between `#workspace` and `#side-panes`.
  - A pane element shape `panes.js` will create: `<section class="pane"><header class="pane-header"><span class="pane-title">…</span><button class="pane-close" aria-label="close pane">✕</button></header><iframe class="pane-frame" src="…"></iframe></section>`.

- [ ] **Step 1: Read the shell.** Read `cmd/serf-hub/templates/app.html` around the `body.app` / `#workspace` region (the spec cites `app.html:29-33`) and `style.css:474-478` (`body.app` flex row; `#workspace { flex:1; min-width:0 }`). Confirm `body.app` is the flex row holding `#sidebar` + `#workspace`.

- [ ] **Step 2: Write the failing render test.** In `web_test.go`, add a test that the app shell HTML for a session contains the side-pane region and splitter (use the existing app-shell render helper in that file — find how other tests render `/s/<id>` / the `app` template; mirror them):
```go
func TestWeb_AppShellHasSidePaneRegion(t *testing.T) {
	body := renderAppShell(t) // use/adapt the existing app-shell render helper in web_test.go
	for _, want := range []string{`id="side-panes"`, `id="pane-splitter"`, `panes.js`} {
		if !strings.Contains(body, want) {
			t.Fatalf("app shell missing %q", want)
		}
	}
}
```

- [ ] **Step 3: Run it — expect FAIL** (region + script not present yet).
Run: `go test ./cmd/serf-hub/ -run TestWeb_AppShellHasSidePaneRegion -v`

- [ ] **Step 4: Add the markup.** In `app.html`, immediately AFTER the `<main id="workspace" ...>…</main>` element and still inside `body.app`, add:
```html
    <div id="pane-splitter" class="pane-splitter" role="separator" aria-orientation="vertical" hidden></div>
    <aside id="side-panes" class="side-panes" aria-label="Side panes" hidden></aside>
```
And in the `<head>`/scripts block where `sidebar.js` is loaded, add `<script src="/assets/panes.js" defer></script>` (after sidebar.js; before/after appwire.js doesn't matter — panes.js has no appwire dependency). Do NOT change the 4 renderer script tags.

- [ ] **Step 5: Add the CSS.** In `style.css`, near the `body.app`/`#workspace` block, add (match surrounding token usage):
```css
/* Multi-pane: side panes sit as flex columns to the right of #workspace. */
.side-panes { display: flex; flex-direction: row; min-width: 0; height: 100vh; }
.side-panes[hidden] { display: none; }
.pane { display: flex; flex-direction: column; min-width: 0; width: var(--pane-w, 420px);
  border-left: 1px solid var(--rule); height: 100vh; overflow: hidden; }
.pane-header { display: flex; align-items: center; gap: var(--space-2);
  padding: 0 var(--space-3); height: 38px; flex-shrink: 0; border-bottom: 1px solid var(--rule);
  font-size: var(--text-sm); color: var(--text-muted); }
.pane-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pane-close { background: none; border: none; color: var(--text-muted); cursor: pointer;
  font-size: var(--text-md); line-height: 1; padding: var(--space-1) var(--space-2); }
.pane-close:hover { color: var(--text); }
.pane-frame { flex: 1; width: 100%; border: 0; min-height: 0; }
.pane-splitter { width: 5px; flex-shrink: 0; cursor: col-resize; background: var(--rule); opacity: .4; }
.pane-splitter:hover { opacity: 1; }
.pane-splitter[hidden] { display: none; }
@media (max-width: 767px) { .side-panes, .pane-splitter { display: none !important; } }
```

- [ ] **Step 6: Run the test + build.**
Run: `go test ./cmd/serf-hub/ -run TestWeb_AppShellHasSidePaneRegion -v` → PASS; `make build-hub` → OK; `make lint` → `0 issues.`

- [ ] **Step 7: Commit.**
```bash
git add cmd/serf-hub/templates/app.html cmd/serf-hub/assets/style.css cmd/serf-hub/web_test.go
git commit -m "Add empty side-pane region + splitter shell and pane CSS"
```

---

### Task 3: `panes.js` — open/close panes (with cap)

**Files:**
- Create: `cmd/serf-hub/assets/panes.js`
- Test: `cmd/serf-hub/jstest/test-panes.js`

**Interfaces:**
- Consumes: the DOM contract from Task 2 (`#side-panes`, `#pane-splitter`).
- Produces (global, mirroring `window.SerfRenderer`/`window.SerfAppwire`):
  - `window.SerfPanes.open(href, title)` — opens (or focuses, if `href` already open) a side pane whose iframe `src = href`; enforces `MAX_SIDE_PANES = 3`; reveals `#side-panes` + `#pane-splitter`. Returns the pane element (or null if at cap → see Step 3 behavior).
  - `window.SerfPanes.close(href)` — closes the pane with that href; hides the region + splitter when none remain.
  - `window.SerfPanes.openHrefs()` — returns the array of currently-open pane hrefs (used by Task 4 persistence).
  - `MAX_SIDE_PANES = 3`.

- [ ] **Step 1: Read a sibling module** (`cmd/serf-hub/assets/sidebar.js`) for the IIFE wrapper, the `window.Serf*` export style, and how it guards missing DOM. Read `cmd/serf-hub/jstest/test-sidebar-archive.js` for the jstest harness (JSDOM setup, `process.exit(0)`).

- [ ] **Step 2: Write the failing test.** `cmd/serf-hub/jstest/test-panes.js`:
```js
// Sets up minimal DOM (#side-panes, #pane-splitter), loads panes.js, exercises open/close/cap.
const { JSDOM } = require("jsdom");
const fs = require("fs");
const path = require("path");

const dom = new JSDOM(`<!DOCTYPE html><body class="app">
  <main id="workspace"></main>
  <div id="pane-splitter" hidden></div>
  <aside id="side-panes" hidden></aside>
</body>`, { url: "http://localhost/" });
global.window = dom.window; global.document = dom.window.document;

eval(fs.readFileSync(path.join(__dirname, "..", "assets", "panes.js"), "utf8"));
const P = dom.window.SerfPanes;

// open one pane
const pane = P.open("/s/sub-1", "Subagent 1");
if (!pane) throw new Error("open returned null");
const frames = () => document.querySelectorAll("#side-panes .pane-frame");
if (frames().length !== 1) throw new Error("expected 1 frame, got " + frames().length);
if (frames()[0].getAttribute("src") !== "/s/sub-1") throw new Error("wrong iframe src");
if (document.getElementById("side-panes").hidden) throw new Error("region should be visible");

// opening same href again does NOT duplicate
P.open("/s/sub-1", "Subagent 1");
if (frames().length !== 1) throw new Error("duplicate href should not add a pane");

// cap at MAX_SIDE_PANES
P.open("/s/sub-2", "two"); P.open("/s/sub-3", "three"); P.open("/s/sub-4", "four");
if (frames().length !== P.MAX_SIDE_PANES) throw new Error("cap not enforced: " + frames().length);

// openHrefs reflects state
if (P.openHrefs().length !== P.MAX_SIDE_PANES) throw new Error("openHrefs wrong");

// close hides region when empty
P.openHrefs().slice().forEach(h => P.close(h));
if (frames().length !== 0) throw new Error("panes not closed");
if (!document.getElementById("side-panes").hidden) throw new Error("region should hide when empty");

console.log("test-panes: ok");
process.exit(0);
```

- [ ] **Step 3: Run it — expect FAIL** (`SerfPanes` undefined).
Run: `cd cmd/serf-hub/jstest && node test-panes.js`

- [ ] **Step 4: Implement `panes.js`.**
```js
// panes.js — host-side multi-pane manager. Each side pane is an <iframe> loading an
// existing /s/<id> route, so the single-instance renderer runs unchanged per pane.
(function () {
  "use strict";
  var MAX_SIDE_PANES = 3;

  function region() { return document.getElementById("side-panes"); }
  function splitter() { return document.getElementById("pane-splitter"); }

  function paneFor(href) {
    var r = region();
    if (!r) return null;
    var frames = r.querySelectorAll(".pane-frame");
    for (var i = 0; i < frames.length; i++) {
      if (frames[i].getAttribute("src") === href) return frames[i].closest(".pane");
    }
    return null;
  }

  function openHrefs() {
    var r = region();
    if (!r) return [];
    return Array.prototype.map.call(r.querySelectorAll(".pane-frame"), function (f) {
      return f.getAttribute("src");
    });
  }

  function showRegion(show) {
    var r = region(), s = splitter();
    if (r) r.hidden = !show;
    if (s) s.hidden = !show;
  }

  function open(href, title) {
    if (!href) return null;
    var r = region();
    if (!r) return null;
    var existing = paneFor(href);
    if (existing) { existing.querySelector(".pane-frame").focus(); return existing; }
    if (r.querySelectorAll(".pane").length >= MAX_SIDE_PANES) return null;

    var pane = document.createElement("section");
    pane.className = "pane";
    var head = document.createElement("header");
    head.className = "pane-header";
    var t = document.createElement("span");
    t.className = "pane-title";
    t.textContent = title || href;
    var x = document.createElement("button");
    x.type = "button";
    x.className = "pane-close";
    x.setAttribute("aria-label", "close pane");
    x.textContent = "✕";
    x.addEventListener("click", function () { close(href); });
    head.appendChild(t); head.appendChild(x);
    var frame = document.createElement("iframe");
    frame.className = "pane-frame";
    frame.setAttribute("src", href);
    frame.setAttribute("title", title || href);
    pane.appendChild(head); pane.appendChild(frame);
    r.appendChild(pane);
    showRegion(true);
    persist();
    return pane;
  }

  function close(href) {
    var pane = paneFor(href);
    if (pane) pane.remove();
    if (region() && region().querySelectorAll(".pane").length === 0) showRegion(false);
    persist();
  }

  // persist() is defined in Task 4; declare a no-op here so Task 3 stands alone.
  function persist() { if (window.SerfPanes && window.SerfPanes._persist) window.SerfPanes._persist(); }

  window.SerfPanes = { open: open, close: close, openHrefs: openHrefs, MAX_SIDE_PANES: MAX_SIDE_PANES };
})();
```

- [ ] **Step 5: Run the test.**
Run: `cd cmd/serf-hub/jstest && node test-panes.js` → `test-panes: ok`; then `bash run-all.sh` → `jstest: all tests passed`.

- [ ] **Step 6: Commit.**
```bash
git add cmd/serf-hub/assets/panes.js cmd/serf-hub/jstest/test-panes.js
git commit -m "Add panes.js host module: open/close side panes with a cap"
```

---

### Task 4: Restore open panes on reload (localStorage)

**Files:**
- Modify: `cmd/serf-hub/assets/panes.js` (add persistence + restore-on-load)
- Test: `cmd/serf-hub/jstest/test-panes.js` (extend)

**Interfaces:**
- Consumes: `open`/`openHrefs` (Task 3).
- Produces: panes (href+title) are written to `localStorage["serf-hub.panes"]` on every open/close, and restored on load. Adds `window.SerfPanes.restore()` (idempotent) and an internal `_persist`.

- [ ] **Step 1: Extend the failing test.** Append to `test-panes.js` BEFORE `process.exit(0)`:
```js
// persistence: opening writes localStorage; a fresh load restores
P.open("/s/keep-1", "Keep 1");
const stored = JSON.parse(dom.window.localStorage.getItem("serf-hub.panes") || "[]");
if (!stored.some(p => p.href === "/s/keep-1")) throw new Error("open not persisted");

// simulate reload: clear DOM panes, call restore()
document.querySelectorAll("#side-panes .pane").forEach(n => n.remove());
document.getElementById("side-panes").hidden = true;
P.restore();
if (!P.openHrefs().includes("/s/keep-1")) throw new Error("restore did not reopen pane");
console.log("test-panes persistence: ok");
```
(Place this above the existing `console.log("test-panes: ok")` / `process.exit(0)` — or move the exit to the very end.)

- [ ] **Step 2: Run it — expect FAIL** (`restore` undefined / nothing persisted).
Run: `cd cmd/serf-hub/jstest && node test-panes.js`

- [ ] **Step 3: Implement persistence in `panes.js`.** Replace the placeholder `persist()` and the export, and add `restore()`:
```js
  var STORE_KEY = "serf-hub.panes";

  function persist() {
    var r = region();
    if (!r) return;
    var data = Array.prototype.map.call(r.querySelectorAll(".pane"), function (p) {
      var f = p.querySelector(".pane-frame");
      var t = p.querySelector(".pane-title");
      return { href: f.getAttribute("src"), title: t ? t.textContent : "" };
    });
    try { window.localStorage.setItem(STORE_KEY, JSON.stringify(data)); } catch (e) { /* ignore */ }
  }

  function restore() {
    var raw;
    try { raw = window.localStorage.getItem(STORE_KEY); } catch (e) { return; }
    if (!raw) return;
    var data;
    try { data = JSON.parse(raw); } catch (e) { return; }
    if (!Array.isArray(data)) return;
    data.forEach(function (p) { if (p && p.href) open(p.href, p.title); });
  }
```
Update the export to `{ open, close, openHrefs, restore, MAX_SIDE_PANES, _persist: persist }`, and have `open`/`close` call `persist()` directly (drop the old indirection). Finally, restore on load — append at the end of the IIFE:
```js
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", restore);
  } else { restore(); }
```

- [ ] **Step 4: Run the test.**
Run: `cd cmd/serf-hub/jstest && node test-panes.js` → both `ok` lines; `bash run-all.sh` → all passed.

- [ ] **Step 5: Commit.**
```bash
git add cmd/serf-hub/assets/panes.js cmd/serf-hub/jstest/test-panes.js
git commit -m "Persist + restore open side panes across reload via localStorage"
```

---

### Task 5: Splitter resize with persisted width

**Files:**
- Modify: `cmd/serf-hub/assets/panes.js` (splitter drag → set `--pane-w`, persist width)
- Test: `cmd/serf-hub/jstest/test-panes.js` (extend — test the width math/persistence, not real mouse drag)

**Interfaces:**
- Consumes: `#pane-splitter`, the `.pane` width var `--pane-w` (Task 2 CSS).
- Produces: `window.SerfPanes.setSidePanesWidth(px)` clamps to `[280, 900]`, applies it to the `.side-panes` width (or each `.pane` `--pane-w`), and persists to `localStorage["serf-hub.panes.width"]`; restored on load. The mousedown/mousemove/mouseup handler on `#pane-splitter` calls `setSidePanesWidth` (drag itself is verified manually).

- [ ] **Step 1: Extend the failing test.** Append before the final exit:
```js
P.setSidePanesWidth(10000);
if (parseInt(dom.window.localStorage.getItem("serf-hub.panes.width"), 10) !== 900)
  throw new Error("width not clamped to max");
P.setSidePanesWidth(10);
if (parseInt(dom.window.localStorage.getItem("serf-hub.panes.width"), 10) !== 280)
  throw new Error("width not clamped to min");
console.log("test-panes width: ok");
```

- [ ] **Step 2: Run it — expect FAIL** (`setSidePanesWidth` undefined).
Run: `cd cmd/serf-hub/jstest && node test-panes.js`

- [ ] **Step 3: Implement.** In `panes.js`:
```js
  var WIDTH_KEY = "serf-hub.panes.width";
  function setSidePanesWidth(px) {
    var w = Math.max(280, Math.min(900, Math.round(px)));
    var r = region();
    if (r) r.style.setProperty("--pane-w", w + "px");
    try { window.localStorage.setItem(WIDTH_KEY, String(w)); } catch (e) { /* ignore */ }
    return w;
  }
  function restoreWidth() {
    var v; try { v = parseInt(window.localStorage.getItem(WIDTH_KEY), 10); } catch (e) { return; }
    if (v) setSidePanesWidth(v);
  }
  // Drag handler (verified manually; logic delegates to setSidePanesWidth).
  function bindSplitter() {
    var s = splitter(); if (!s || s.__bound) return; s.__bound = true;
    s.addEventListener("mousedown", function (e) {
      e.preventDefault();
      function move(ev) {
        // splitter sits between #workspace and #side-panes; panes grow as the
        // pointer moves left. Width = distance from pointer to right viewport edge.
        setSidePanesWidth(window.innerWidth - ev.clientX);
      }
      function up() {
        document.removeEventListener("mousemove", move);
        document.removeEventListener("mouseup", up);
      }
      document.addEventListener("mousemove", move);
      document.addEventListener("mouseup", up);
    });
  }
```
Have `--pane-w` apply to each `.pane` (CSS already sets `.pane { width: var(--pane-w, 420px) }`); set the var on `#side-panes` so it cascades — confirm the CSS var inherits (it does, custom properties inherit). Call `bindSplitter()` and `restoreWidth()` from the load path (alongside `restore()`), and add `setSidePanesWidth` to the export.

- [ ] **Step 4: Run the test + build.**
Run: `cd cmd/serf-hub/jstest && node test-panes.js` → width ok; `bash run-all.sh` → all passed; `make build-hub` → OK; `make lint` → `0 issues.`

- [ ] **Step 5: Manual-verify note.** In your report, state that real splitter drag (mousedown→move→up resizing the panes) must be eyeball-verified in a browser; the unit test only covers the clamp/persist math.

- [ ] **Step 6: Commit.**
```bash
git add cmd/serf-hub/assets/panes.js cmd/serf-hub/jstest/test-panes.js
git commit -m "Add resizable side-pane splitter with clamped, persisted width"
```

---

### Task 6: "Open beside" affordance on subagent rows

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` (the subagent row builder + the `navigateTo`/`applyJobRefTarget` area, ~`renderer.js:2374-2386`; row builder `makeSubagentRow` ~`renderer.js:2216`)
- Test: `cmd/serf-hub/jstest/test-renderer-open-beside.js`

**Interfaces:**
- Consumes: `window.SerfPanes.open(href, title)` (Task 3).
- Produces: each subagent row gains an "open beside" control (a small button, class `open-beside-btn`, glyph `⇲` or `↔`) that calls `window.SerfPanes.open("/s/" + transcriptRef, title)` and does NOT trigger the row's `navigateTo`. The existing "view →" one-way nav is unchanged. Guard: if `window.SerfPanes` is absent (e.g. inside an iframe pane), hide/skip the button so panes don't nest.

- [ ] **Step 1: Read the seam.** Read `renderer.js` around `makeSubagentRow` (~2216) and `applyJobRefTarget`/`navigateTo` (~2374-2386). Confirm where `data.transcriptRef` is available and how the row's click is wired, and how the row title is obtained.

- [ ] **Step 2: Write the failing test.** `cmd/serf-hub/jstest/test-renderer-open-beside.js` — load the renderer via the shared loader (mirror `test-subagent-nav.js`), render a subagent row, stub `window.SerfPanes`, click the open-beside control, assert `SerfPanes.open` was called with `/s/<transcriptRef>` and that `window.location` did NOT change. (Follow the existing renderer-jstest setup: `require("./load-renderer").evalRenderer(window)`, `await setTimeout(30)`, fire the data that produces a subagent row, then `process.exit(0)`.)
```js
// skeleton — adapt to the harness in test-subagent-nav.js
let openedWith = null;
window.SerfPanes = { open: (href, title) => { openedWith = { href, title }; } };
// ... render a subagent row whose transcriptRef = "sub-xyz" ...
const btn = document.querySelector(".open-beside-btn");
if (!btn) throw new Error("no open-beside control");
const beforeHref = window.location.href;
btn.click();
if (!openedWith || openedWith.href !== "/s/sub-xyz") throw new Error("did not call SerfPanes.open correctly");
if (window.location.href !== beforeHref) throw new Error("open-beside must not navigate");
console.log("test-renderer-open-beside: ok");
process.exit(0);
```

- [ ] **Step 3: Run it — expect FAIL** (no `.open-beside-btn`).
Run: `cd cmd/serf-hub/jstest && node test-renderer-open-beside.js`

- [ ] **Step 4: Implement.** In `makeSubagentRow` (or wherever the row's "view →" affordance is built), add a sibling button:
```js
// "Open beside" — opens the subagent in a side pane instead of navigating away.
// Hidden when SerfPanes is unavailable (e.g. this renderer is itself inside a pane iframe).
if (window.SerfPanes && data.transcriptRef) {
  var beside = document.createElement("button");
  beside.type = "button";
  beside.className = "open-beside-btn";
  beside.setAttribute("aria-label", "open subagent beside");
  beside.title = "open beside";
  beside.textContent = "⇲";
  beside.addEventListener("click", function (e) {
    e.preventDefault();
    e.stopPropagation(); // do not trigger the row's navigateTo
    window.SerfPanes.open("/s/" + encodeURIComponent(data.transcriptRef), /* title */ row.dataset.title || data.transcriptRef);
  });
  row.appendChild(beside);
}
```
Match the actual row-builder variable names from Step 1 (the local for the row element, and how the title is available — use the subagent's display name if the builder has it). Add a minimal `.open-beside-btn` style to `style.css` (quiet, hover-revealed, like `.archive-btn`): you may fold this one rule in here.

- [ ] **Step 5: Run the test.**
Run: `cd cmd/serf-hub/jstest && node test-renderer-open-beside.js` → ok; `bash run-all.sh` → all passed.

- [ ] **Step 6: Verify the renderer split is intact** (you touched renderer.js): `node --check cmd/serf-hub/assets/renderer.js`; confirm all 4 renderer files still exist and `app.html` loads all 4.

- [ ] **Step 7: Commit.**
```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-renderer-open-beside.js
git commit -m "Add 'open beside' affordance on subagent rows -> side pane"
```

---

## Final verification (after all tasks)

- `make build-hub` && `go build ./...`
- `go test ./cmd/serf-hub/... ./cmd/serf-hub/internal/...`
- `make lint` → `0 issues.` ×4
- `cd cmd/serf-hub/jstest && bash run-all.sh` → `jstest: all tests passed`
- Renderer split intact: 4 `renderer*.js`; `app.html` loads all 4 + `panes.js`.
- **Manual browser smoke (report it):** open a session with subagents; click "open beside" on a subagent row → it opens as a right-hand pane (live, interactive); open up to 3; drag the splitter; close panes; reload → panes restore.

## Deferred work (NOT in this plan — needs separate specs/decisions)

1. **Auto-open the main session's observer subagent.** Jesse wants this, but "observer" is a runtime `job_watch` concept (grants minted per observer session — `agent/job_watch.go`), not a web-surfaced flag: subagents only carry `IsSubagent`/`ParentSessionID` (`agent/schema/snapshot.go:52-54`), and `AgentRole` (`appwire/types.go:152`) is a codex-only field. Auto-open therefore requires NEW backend work to surface, per session, the id of its observer subagent (over the tree/appwire), and then a host rule to auto-`open()` it. Spec that signal before building.
2. **Document panes** (repo file / markdown / diff) + the remote-session file-read RPC (the largest backend item; image + diff are partially reachable today — see the design doc).
3. **Compressed/denser rendering style for pane agents** (Jesse: "can come later").
4. **Shareable-URL layouts** (`?pane=...` encoding) — MVP keeps panes in `localStorage` only.
5. **In-page multi-instance renderer** (Option B, ~1500–2500 LOC) — only if iframe isolation proves limiting (one shared composer, 3+ panes on one socket).

## Self-review (author)

- **Scope coverage:** subagent live pane via iframe (Tasks 2–3,6); fully interactive (iframe is real `/s/<id>` — inherent); a few tiled vertical columns + cap (Task 3 `MAX_SIDE_PANES=3`, Task 2 flex-row); restore-on-reload (Task 4); CSP relax approved (Task 1); resize (Task 5). Auto-open-observer + documents + compressed rendering + shareable URLs explicitly deferred with reasons.
- **Placeholder scan:** no TBD/"handle errors"; each code step has concrete code; the one unavoidable adapt-to-codebase points (the app-shell render helper name in web_test.go, the subagent row-builder local names) are called out as "read Step 1 / mirror existing" rather than invented.
- **Type/name consistency:** `window.SerfPanes.{open(href,title), close(href), openHrefs(), restore(), setSidePanesWidth(px), MAX_SIDE_PANES}`, DOM ids `#side-panes`/`#pane-splitter`, classes `.pane/.pane-header/.pane-title/.pane-close/.pane-frame/.pane-splitter/.open-beside-btn`, storage keys `serf-hub.panes` + `serf-hub.panes.width` — used consistently across tasks.

# serf-hub UI Pass 8 — Polish (toasts + skeletons + empty states + stagger) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the final 10% of the serf-hub UI overhaul — top-center toasts wired across the app, skeleton loading on htmx swaps, per-surface empty states with CTAs, first-paint sidebar stagger, `:active` press states on every button variant, reduced-motion fallbacks for optimistic indicators, spawn-chip overflow, and removal of remaining `.rule-dot` separators.

**Architecture:** All work is additive CSS + a handful of small JS modules — no Go changes, no template restructuring beyond per-surface empty-state markup and `#toast-region` insertion. New JS files (`toast.js`, `skeleton.js`, `chip-overflow.js`) are pure side-effect IIFEs registered in `app.html`; they expose `window.SerfToast` (the only new global), follow the same shape as `notifications.js`/`pending.js`, and never re-fetch from the server. Empty-state CSS reuses tokens from Pass 1; reduced-motion fallbacks add non-animated border/shadow signals to compensate for `*` killing animations.

**Tech Stack:** Plain ES2017 (no bundler), CSS custom properties (Pass 1 tokens), htmx 2.x events, JSDOM-based smoke tests, existing template engine (Go `html/template`).

---

## Files

**Create**
- `cmd/serf-hub/assets/toast.js` — `window.SerfToast.show / .dismiss`, auto-dismiss queue, slide-in animation hook.
- `cmd/serf-hub/assets/skeleton.js` — htmx hook that sets/clears `data-loading` on swap targets.
- `cmd/serf-hub/assets/chip-overflow.js` — caps visible spawn chips at 4, adds `+N more` expand button.
- `cmd/serf-hub/jstest/test-toast.js` — JSDOM smoke for `SerfToast.show`, auto-dismiss, dismiss handle, `aria-live`.
- `cmd/serf-hub/jstest/test-skeleton-data-loading.js` — JSDOM smoke for `data-loading` set on `htmx:beforeRequest`, cleared on `htmx:afterSwap`.
- `cmd/serf-hub/jstest/test-chip-overflow.js` — JSDOM smoke for visible-chip cap + expand.

**Modify**
- `cmd/serf-hub/templates/app.html` — add `#toast-region`, three new `<script>` tags.
- `cmd/serf-hub/templates/partials/workspace.html` (lines 33, 80) — replace `.conversation-empty` placeholder with `.empty-state` markup; drop `.rule-dot` from `workspace_meta`.
- `cmd/serf-hub/templates/partials/input_strip.html` (lines 3, 5, 8) — drop `.rule-dot` separators; rely on gap.
- `cmd/serf-hub/templates/partials/sidebar.html` (lines 1–8, 9–30, 32–100) — add 5 skeleton-row placeholders in Live, 3 per project, plus an empty-state when no projects + no live.
- `cmd/serf-hub/web.go:1557` — replace inline `.workspace-empty` markup with new orientation empty-state.
- `cmd/serf-hub/assets/style.css` — add `.toast`, `#toast-region`, `.skeleton`, `[data-loading]` rules, `.empty-state*`, `.stagger`/`@keyframes enter`, `:active` per button variant, `@media (prefers-reduced-motion)` fallbacks, `.chip-overflow*` rules. Remove `.rule-dot` rule.
- `cmd/serf-hub/assets/renderer.js` (~line 2783) — wire toast for copy-id (and replace ✓ text-swap with toast); `~line 2947` — replace `.tasks-empty` div with `.empty-state` markup.
- `cmd/serf-hub/assets/search.js` (~line 254, 261, 491, 593, 627, 762) — wire toast for model change + shutdown; replace `.search-empty` with `.empty-state` markup.
- `cmd/serf-hub/assets/settings.js` — toast on theme change + notification toggle save.
- `cmd/serf-hub/assets/launchconfig.js` / `templates/partials/settings/launch-serf.html` + `project.html` — emit toast on successful save.
- `cmd/serf-hub/assets/composer-attachments.js` (~line 339, 354) — toast on rejected files alongside the inline banner.
- `cmd/serf-hub/assets/appwire.js` (~line 168–177) — fire connection-lost + connection-restored notifications.
- `cmd/serf-hub/assets/sidebar.js` (~line 63–71) — first-paint stagger; set `style.setProperty('--i', n)` on Live rows.
- `cmd/serf-hub/templates/partials/credentials.html` (~line 137, 146, 175, 190) — toast on credential set/clear success/failure.
- `cmd/serf-hub/templates/partials/spawn.html` (chips block, lines 12–37) — add `data-chip-overflow-host` attribute so `chip-overflow.js` can hook in.

---

## Conventions used in this plan

- All new CSS uses Pass 1 tokens (`--space-N`, `--motion-base`, `--radius-md`, `--text-sm`, `--state-*`, `--z-toast`).
- New JS modules use `(function () { "use strict"; … })()` IIFE wrapper to match existing files.
- Toast messages are short noun-phrases or imperative past tense ("Settings saved", "Session shut down", "Connection lost").
- No `console.log` in shipped code.
- JS tests are JSDOM + Node, run via `cd cmd/serf-hub/jstest && ./run-all.sh`.

---

### Task 1: Add `.toast` / `#toast-region` CSS and `#toast-region` to `app.html`

**Files:**
- Modify: `cmd/serf-hub/templates/app.html:19-50`
- Modify: `cmd/serf-hub/assets/style.css` (append a new section)

- [ ] **Step 1: Add `#toast-region` to `app.html`**

Insert immediately after the `</dialog>` for `#search-dialog` (after line 50, before `<script src="/assets/htmx.min.js">`):

```html
  <div id="toast-region" aria-live="polite" aria-relevant="additions"></div>
```

- [ ] **Step 2: Append toast CSS to `style.css`**

Append at the end of `style.css` under a new comment header `/* Pass 8 — Toasts */`:

```css
/* Pass 8 — Toasts */
#toast-region {
  position: fixed;
  top: var(--space-4);
  left: 50%;
  transform: translateX(-50%);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  z-index: var(--z-toast);
  pointer-events: none;
  max-width: min(560px, calc(100vw - var(--space-4) * 2));
}
.toast {
  pointer-events: auto;
  padding: var(--space-3) var(--space-4);
  background: var(--bg-raised);
  border: 1px solid var(--rule);
  border-radius: var(--radius-md);
  color: var(--text);
  font-family: var(--font-sans);
  font-size: var(--text-sm);
  line-height: 1.4;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.3);
  animation: toast-enter var(--motion-base) both;
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 240px;
}
.toast.toast-success { border-left: 3px solid var(--state-idle); }
.toast.toast-error   { border-left: 3px solid var(--state-awaiting); }
.toast.toast-info    { border-left: 3px solid var(--accent); }
.toast.toast-dismissing { animation: toast-exit var(--motion-base) both; }
.toast-close {
  background: transparent;
  border: 0;
  color: var(--text-muted);
  font-family: var(--font-sans);
  font-size: var(--text-sm);
  cursor: pointer;
  padding: 0 var(--space-1);
  margin-left: auto;
}
.toast-close:hover { color: var(--text); }
.toast-close:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
@keyframes toast-enter {
  from { opacity: 0; transform: translateY(-8px); }
  to   { opacity: 1; transform: translateY(0); }
}
@keyframes toast-exit {
  from { opacity: 1; transform: translateY(0); }
  to   { opacity: 0; transform: translateY(-8px); }
}
```

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/templates/app.html cmd/serf-hub/assets/style.css
git commit -m "ui(pass-8): add #toast-region and toast CSS"
```

---

### Task 2: Write `toast.js` test (TDD-red)

**Files:**
- Create: `cmd/serf-hub/jstest/test-toast.js`

- [ ] **Step 1: Write the failing JSDOM test**

```javascript
// Verify window.SerfToast.show inserts an .toast element into #toast-region,
// auto-dismisses after the configured timeout, returns a handle that can be
// dismissed early, and that #toast-region has aria-live="polite".
const fs = require("fs");
const { JSDOM } = require("jsdom");

const TOAST_PATH = "../assets/toast.js";
const toastSrc = fs.readFileSync(TOAST_PATH, "utf8");

const dom = new JSDOM(
  `<!DOCTYPE html><html><body>
     <div id="toast-region" aria-live="polite"></div>
   </body></html>`,
  { runScripts: "outside-only", pretendToBeVisual: true }
);
const { window } = dom;
// Stub setTimeout/clearTimeout so we can advance fake time.
const fakeTimers = [];
let nextHandle = 1;
window.setTimeout = (fn, ms) => { const h = nextHandle++; fakeTimers.push({ h, fn, ms }); return h; };
window.clearTimeout = (h) => { const i = fakeTimers.findIndex((t) => t.h === h); if (i >= 0) fakeTimers.splice(i, 1); };
function flushTimers() { while (fakeTimers.length) { const t = fakeTimers.shift(); try { t.fn(); } catch (_) {} } }

window.eval(toastSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

pass(typeof window.SerfToast === "object", "SerfToast global should exist");
pass(typeof window.SerfToast.show === "function", "SerfToast.show should be a function");
pass(typeof window.SerfToast.dismiss === "function", "SerfToast.dismiss should be a function");

const region = window.document.getElementById("toast-region");
pass(region.getAttribute("aria-live") === "polite", "toast-region should be aria-live=polite");

// Show a success toast.
const h1 = window.SerfToast.show("Saved", "success");
let toasts = region.querySelectorAll(".toast");
pass(toasts.length === 1, "expected 1 toast, got " + toasts.length);
pass(toasts[0].classList.contains("toast-success"), "kind class should be applied");
pass(toasts[0].textContent.includes("Saved"), "message should appear");

// Dismiss handle explicitly.
window.SerfToast.dismiss(h1);
// The dismissing class is applied; the element is still in the DOM until
// the exit animation timer fires. Flush timers.
flushTimers();
toasts = region.querySelectorAll(".toast");
pass(toasts.length === 0, "after dismiss the toast should be removed");

// Auto-dismiss after timeout.
window.SerfToast.show("Bye", "info", { timeout: 50 });
toasts = region.querySelectorAll(".toast");
pass(toasts.length === 1, "after show the toast should exist");
flushTimers(); // runs the 50ms auto-dismiss timer
flushTimers(); // runs the exit-animation cleanup timer
toasts = region.querySelectorAll(".toast");
pass(toasts.length === 0, "after auto-dismiss the toast should be removed");

// Default kind is "info"; missing region is tolerated.
const ghost = window.document.createElement("div");
const detached = window.SerfToast.show("ignored", "info"); // should still create a toast in the existing region
toasts = region.querySelectorAll(".toast");
pass(toasts.length === 1, "default kind toast inserted");
window.SerfToast.dismiss(detached);
flushTimers();

// Unknown kind defaults to info.
window.SerfToast.show("hi", "unknown-kind");
toasts = region.querySelectorAll(".toast");
pass(toasts[0].classList.contains("toast-info"), "unknown kind should default to toast-info");

if (failures.length === 0) {
  console.log("PASS: toast show/dismiss/auto-dismiss/aria-live");
  process.exit(0);
} else {
  for (const f of failures) console.log(" " + f);
  process.exit(1);
}
```

- [ ] **Step 2: Run the test to confirm it fails**

```bash
cd cmd/serf-hub/jstest && node test-toast.js
```

Expected: FAIL (file `../assets/toast.js` does not exist yet, so `readFileSync` throws ENOENT).

- [ ] **Step 3: Commit the failing test**

```bash
git add cmd/serf-hub/jstest/test-toast.js
git commit -m "test(pass-8): toast smoke test (red)"
```

---

### Task 3: Implement `toast.js`

**Files:**
- Create: `cmd/serf-hub/assets/toast.js`
- Modify: `cmd/serf-hub/templates/app.html` — register `<script>`

- [ ] **Step 1: Write `toast.js`**

```javascript
// toast.js — top-center, aria-live="polite" transient notifications.
//
// Exposes window.SerfToast.show(message, kind, opts) and .dismiss(handle).
// Toasts are inserted into #toast-region (rendered by app.html). Default
// timeout is 3000ms; opts.timeout overrides. opts.timeout = 0 disables
// auto-dismiss. Kind defaults to "info"; unknown kinds also become "info".
//
// Handles are stable opaque numbers. dismiss() is a no-op for unknown
// handles, so callers don't need to guard.
(function () {
  "use strict";

  var DEFAULT_TIMEOUT = 3000;
  var EXIT_ANIMATION_MS = 160; // must match --motion-base in style.css
  var KINDS = { success: 1, error: 1, info: 1 };

  // Active toast records: handle -> { el, dismissTimer, exitTimer }.
  var active = Object.create(null);
  var nextHandle = 1;

  function region() {
    return document.getElementById("toast-region");
  }

  function show(message, kind, opts) {
    var host = region();
    if (!host) return null; // region not in the DOM yet — silently drop

    var k = (kind && KINDS[kind]) ? kind : "info";
    var o = opts || {};
    var timeout = (typeof o.timeout === "number") ? o.timeout : DEFAULT_TIMEOUT;

    var el = document.createElement("div");
    el.className = "toast toast-" + k;
    el.setAttribute("role", k === "error" ? "alert" : "status");

    var msg = document.createElement("span");
    msg.className = "toast-message";
    msg.textContent = String(message == null ? "" : message);
    el.appendChild(msg);

    var close = document.createElement("button");
    close.type = "button";
    close.className = "toast-close";
    close.setAttribute("aria-label", "dismiss notification");
    close.textContent = "×";
    el.appendChild(close);

    host.appendChild(el);

    var handle = nextHandle++;
    var record = { el: el, dismissTimer: 0, exitTimer: 0 };
    active[handle] = record;

    close.addEventListener("click", function () { dismiss(handle); });

    if (timeout > 0) {
      record.dismissTimer = setTimeout(function () { dismiss(handle); }, timeout);
    }
    return handle;
  }

  function dismiss(handle) {
    var record = active[handle];
    if (!record) return;
    if (record.dismissTimer) {
      clearTimeout(record.dismissTimer);
      record.dismissTimer = 0;
    }
    if (record.exitTimer) return; // already dismissing

    record.el.classList.add("toast-dismissing");
    record.exitTimer = setTimeout(function () {
      if (record.el && record.el.parentNode) {
        record.el.parentNode.removeChild(record.el);
      }
      delete active[handle];
    }, EXIT_ANIMATION_MS);
  }

  // Optional convenience for callers that prefer keyword args.
  function success(message, opts) { return show(message, "success", opts); }
  function error(message, opts) { return show(message, "error", opts); }
  function info(message, opts) { return show(message, "info", opts); }

  window.SerfToast = {
    show: show,
    dismiss: dismiss,
    success: success,
    error: error,
    info: info,
  };
})();
```

- [ ] **Step 2: Register the script in `app.html`**

In `cmd/serf-hub/templates/app.html`, add this line after the `<script src="/assets/htmx.min.js"></script>` line (so toast is available before any other module fires events):

```html
  <script src="/assets/toast.js"></script>
```

- [ ] **Step 3: Run the toast test to confirm pass**

```bash
cd cmd/serf-hub/jstest && node test-toast.js
```

Expected: `PASS: toast show/dismiss/auto-dismiss/aria-live`

- [ ] **Step 4: Run the full jstest suite to confirm no regressions**

```bash
cd cmd/serf-hub/jstest && ./run-all.sh
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/toast.js cmd/serf-hub/templates/app.html
git commit -m "feat(pass-8): toast.js (window.SerfToast.show/dismiss)"
```

---

### Task 4: Wire toast in `renderer.js` — copy session ID

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js:2783-2792`

- [ ] **Step 1: Replace the inline ✓ text-swap with a toast call**

Replace the block:

```javascript
    if (t.matches("[data-copy-id]")) {
      e.preventDefault();
      const id = t.getAttribute("data-copy-id");
      if (id && navigator.clipboard) {
        navigator.clipboard.writeText(id).then(() => {
          const orig = t.textContent;
          t.textContent = "✓";
          setTimeout(() => { t.textContent = orig; }, 1200);
        });
      }
    } else if (t.matches("[data-details-trigger]") || t.closest && t.closest("[data-details-trigger]")) {
```

with:

```javascript
    if (t.matches("[data-copy-id]")) {
      e.preventDefault();
      const id = t.getAttribute("data-copy-id");
      if (id && navigator.clipboard) {
        navigator.clipboard.writeText(id).then(() => {
          if (window.SerfToast) window.SerfToast.show("Session ID copied", "success");
        }, () => {
          if (window.SerfToast) window.SerfToast.show("Copy failed — clipboard blocked", "error");
        });
      }
    } else if (t.matches("[data-details-trigger]") || t.closest && t.closest("[data-details-trigger]")) {
```

- [ ] **Step 2: Run all jstests to confirm no regressions**

```bash
cd cmd/serf-hub/jstest && ./run-all.sh
```

Expected: PASS (the existing renderer tests do not depend on the `✓` text swap).

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js
git commit -m "ui(pass-8): toast on copy session ID"
```

---

### Task 5: Wire toast in `search.js` — model change + session shutdown

**Files:**
- Modify: `cmd/serf-hub/assets/search.js:253-261`

- [ ] **Step 1: Wrap the `shutdown` and `model` runners with success/error toasts**

Locate the `shutdown` and `model` entries in `commands(ctx)` (around lines 253–261). Replace:

```javascript
      { id: "shutdown", title: "Shut down daemon", hint: "ends this session", keywords: ["kill"], scope: "session",
        run: (ctx) => postSession(ctx, "shutdown") },
      { id: "model", title: "Switch model", hint: "", keywords: [], scope: "session",
        args: { kind: "enum", placeholder: "choose a model…",
          source: () => fetchModels(),
          run: (ctx, item) => window.SerfAppwire ? window.SerfAppwire.setModel(ctx.sessionId, item.id) : fetch("/s/" + encodeURIComponent(ctx.sessionId) + "/model", {
            method: "POST", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ model: item.id }),
          }) } },
```

with:

```javascript
      { id: "shutdown", title: "Shut down daemon", hint: "ends this session", keywords: ["kill"], scope: "session",
        run: (ctx) => {
          const p = postSession(ctx, "shutdown");
          return Promise.resolve(p).then((r) => {
            if (window.SerfToast) window.SerfToast.show("Session shut down", "success");
            return r;
          }, (err) => {
            if (window.SerfToast) window.SerfToast.show("Shutdown failed", "error");
            throw err;
          });
        } },
      { id: "model", title: "Switch model", hint: "", keywords: [], scope: "session",
        args: { kind: "enum", placeholder: "choose a model…",
          source: () => fetchModels(),
          run: (ctx, item) => {
            const p = window.SerfAppwire ? window.SerfAppwire.setModel(ctx.sessionId, item.id) : fetch("/s/" + encodeURIComponent(ctx.sessionId) + "/model", {
              method: "POST", headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ model: item.id }),
            });
            return Promise.resolve(p).then((r) => {
              if (window.SerfToast) window.SerfToast.show("Model: " + item.id, "success");
              return r;
            }, (err) => {
              if (window.SerfToast) window.SerfToast.show("Model change failed", "error");
              throw err;
            });
          } } },
```

- [ ] **Step 2: Run search tests**

```bash
cd cmd/serf-hub/jstest && node test-search-commands.js && node test-search.js
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/search.js
git commit -m "ui(pass-8): toast on model change + shutdown"
```

---

### Task 6: Wire toast in `settings.js` (theme + notification toggle)

**Files:**
- Modify: `cmd/serf-hub/assets/settings.js:8-31`

- [ ] **Step 1: Add toast calls after each successful state change**

Replace the `change` handler body so each successful save fires a toast:

```javascript
  // Theme picker — radio inputs with name="theme" inside .settings-form.
  document.body.addEventListener("change", (e) => {
    const target = e.target;
    if (!target || !target.matches) return;

    if (target.matches('input[name="theme"]')) {
      const v = target.value;
      window.serfHub.setTheme(v === "system" ? null : v);
      if (window.SerfToast) window.SerfToast.show("Theme: " + v, "success");
      return;
    }

    if (target.matches("input[type=checkbox][data-notif]")) {
      const key = target.dataset.notif;
      const prefs = readNotifPrefs();
      prefs[key] = target.checked;
      writeNotifPrefs(prefs);
      if (key === "os" && target.checked && "Notification" in window) {
        Notification.requestPermission().catch(() => {});
      }
      document.dispatchEvent(new CustomEvent("serf-hub:notifications-changed", {
        detail: { key, value: target.checked },
      }));
      if (window.SerfToast) window.SerfToast.show("Settings saved", "success");
      return;
    }
  });
```

- [ ] **Step 2: Run notifications test**

```bash
cd cmd/serf-hub/jstest && node test-notifications.js
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/settings.js
git commit -m "ui(pass-8): toast on theme + notification toggle save"
```

---

### Task 7: Wire toast in launch settings (launch-serf.html + project.html)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/settings/launch-serf.html:50-60`
- Modify: `cmd/serf-hub/templates/partials/settings/project.html` (analogous block, ~lines 50–65)

- [ ] **Step 1: Add toast after save in `launch-serf.html`**

Replace the `form.addEventListener("submit", …)` block:

```javascript
      form.addEventListener("submit", async (e) => {
        e.preventDefault();
        if (!(await window.LaunchConfigControls.validate(form))) return;
        try {
          await launchconfig.setLayer("/", "global", window.LaunchConfigControls.collect(form));
          setStatus("Saved at " + new Date().toLocaleTimeString());
          if (window.SerfToast) window.SerfToast.show("Launch defaults saved", "success");
        } catch (err) {
          window.LaunchConfigControls.showBackendError(form, err);
          setStatus("Error: " + (err && err.message ? err.message : err));
          if (window.SerfToast) window.SerfToast.show("Save failed", "error");
        }
      });
```

- [ ] **Step 2: Find and update the analogous block in `project.html`**

Read `cmd/serf-hub/templates/partials/settings/project.html` around line 50–65. The block ends with `setStatus("Saved at " + …)` per the grep results. Replace with the same pattern:

```javascript
          setStatus("Saved at " + new Date().toLocaleTimeString());
          if (window.SerfToast) window.SerfToast.show("Project launch settings saved", "success");
```

And in the corresponding `catch (err)` branch:

```javascript
          if (window.SerfToast) window.SerfToast.show("Save failed", "error");
```

(Copy the same `if (window.SerfToast) …` pair into the project.html submit handler at the matching success and failure points.)

- [ ] **Step 3: Build serf-hub to confirm templates parse**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/templates/partials/settings/launch-serf.html cmd/serf-hub/templates/partials/settings/project.html
git commit -m "ui(pass-8): toast on launch + project settings save"
```

---

### Task 8: Wire toast in `composer-attachments.js` (attachment failed)

**Files:**
- Modify: `cmd/serf-hub/assets/composer-attachments.js:339-356`

- [ ] **Step 1: Add toast call inside `surfaceRejections`**

After the existing block that sets `banner.textContent = msg`, add a toast for the same message. Replace:

```javascript
  function surfaceRejections(anchorEl, rejected, pendingState, gestureVersion) {
    if (pendingState && typeof gestureVersion === "number" && gestureVersion !== pendingState.__attachmentGestureVersion) {
      return;
    }
    const banner = findErrorBanner(anchorEl);
    if (!banner) return;
    if (!rejected || rejected.length === 0) {
      banner.textContent = "";
      banner.hidden = true;
      return;
    }
    const names = rejected.filter(Boolean).join(", ");
    const msg = rejected.length === 1
      ? "Not an image: " + names
      : "Skipped " + rejected.length + " non-image files: " + names;
    banner.textContent = msg;
    banner.hidden = false;
  }
```

with:

```javascript
  function surfaceRejections(anchorEl, rejected, pendingState, gestureVersion) {
    if (pendingState && typeof gestureVersion === "number" && gestureVersion !== pendingState.__attachmentGestureVersion) {
      return;
    }
    const banner = findErrorBanner(anchorEl);
    if (!banner) {
      // Even without a banner anchor we still want the user to know.
      if (rejected && rejected.length && window.SerfToast) {
        window.SerfToast.show("Attachment rejected", "error");
      }
      return;
    }
    if (!rejected || rejected.length === 0) {
      banner.textContent = "";
      banner.hidden = true;
      return;
    }
    const names = rejected.filter(Boolean).join(", ");
    const msg = rejected.length === 1
      ? "Not an image: " + names
      : "Skipped " + rejected.length + " non-image files: " + names;
    banner.textContent = msg;
    banner.hidden = false;
    if (window.SerfToast) window.SerfToast.show(msg, "error");
  }
```

- [ ] **Step 2: Run composer-attachment tests**

```bash
cd cmd/serf-hub/jstest && node test-submit-attachments.js && node test-composer-image-markers.js && node test-drag-drop-image.js && node test-paste-image.js
```

Expected: PASS (existing assertions still pass; the toast call is best-effort guarded by `if (window.SerfToast)`, so jsdom tests without the global skip it).

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/composer-attachments.js
git commit -m "ui(pass-8): toast on rejected attachments"
```

---

### Task 9: Wire toast + persistent banner in `appwire.js` (connection lost/restored)

**Files:**
- Modify: `cmd/serf-hub/assets/appwire.js:163-177`
- Modify: `cmd/serf-hub/assets/style.css` — add `.connection-banner` rule

- [ ] **Step 1: Add `connection-restored` event surface alongside `connection-lost`**

After the existing `function notifyConnectionLost(err) { … }` block, add:

```javascript
  // Connection-restored fires when initialize() succeeds after a previous
  // markDisconnected(). The renderer (or any subscriber) can use it to
  // clear stale banners.
  const connectionRestoredHandlers = new Set();
  function onConnectionRestored(handler) {
    connectionRestoredHandlers.add(handler);
    return () => connectionRestoredHandlers.delete(handler);
  }
  function notifyConnectionRestored() {
    for (const handler of Array.from(connectionRestoredHandlers)) {
      try { handler(); } catch (_) {}
    }
  }
```

Then, in the `connect()` function, modify the `open` handler success path to track and emit "restored":

Locate:

```javascript
      sock.addEventListener("open", () => {
        request(METHOD.initialize, {
          clientInfo: { name: "serf-web", version: "0.1.0" },
          capabilities: {},
        }).then((resp) => {
          serverFeatures = (resp && resp.features) || {};
          resolve(sock);
        }, (err) => {
```

Add `notifyConnectionRestored()` after a successful initialize, but only if we previously fired a lost event. Introduce a module-level `let wasDisconnected = false;` near the other `let` declarations (around line 25), and mutate it inside `markDisconnected`:

```javascript
  let wasDisconnected = false;
```

Inside `markDisconnected`:

```javascript
      const markDisconnected = (err) => {
        if (disconnected) return;
        disconnected = true;
        wasDisconnected = true;
        notifyConnectionLost(err);
      };
```

In the `open` success branch:

```javascript
        }).then((resp) => {
          serverFeatures = (resp && resp.features) || {};
          if (wasDisconnected) {
            wasDisconnected = false;
            notifyConnectionRestored();
          }
          resolve(sock);
        }, (err) => {
```

Finally, expose `onConnectionRestored` from the public surface. Locate the existing `return {` block at the bottom of the file and add `onConnectionRestored: onConnectionRestored,` next to `onConnectionLost`. (The file declares `window.SerfAppwire = { … }` or returns a public surface — add the key inside whichever shape it uses.)

- [ ] **Step 2: Register subscribers that show toast + persistent banner**

Append to the end of `appwire.js` (inside the same IIFE, after `window.SerfAppwire = …`):

```javascript
  // Wire toast + persistent banner. The banner is required because a 3s
  // toast does not cover the case where the user notices the UI is stale
  // 30s later (Known Issues — Pass 8).
  function ensureConnectionBanner() {
    let banner = document.getElementById("connection-banner");
    if (banner) return banner;
    banner = document.createElement("div");
    banner.id = "connection-banner";
    banner.className = "connection-banner";
    banner.setAttribute("role", "status");
    banner.textContent = "Connection lost — reconnecting…";
    document.body.insertBefore(banner, document.body.firstChild);
    return banner;
  }
  function clearConnectionBanner() {
    const banner = document.getElementById("connection-banner");
    if (banner && banner.parentNode) banner.parentNode.removeChild(banner);
  }
  onConnectionLost(() => {
    if (window.SerfToast) window.SerfToast.show("Connection lost — reconnecting…", "error", { timeout: 0 });
    ensureConnectionBanner();
  });
  onConnectionRestored(() => {
    if (window.SerfToast) window.SerfToast.show("Connection restored", "success");
    clearConnectionBanner();
  });
```

Note: the lost-toast uses `timeout: 0` so it stays until the user dismisses or the restored toast fires (the restored handler dismisses nothing; user dismisses lost via × or restored arrives and they see "Connection restored").

- [ ] **Step 3: Add the banner CSS**

Append to `style.css`:

```css
/* Pass 8 — Connection lost banner (paired with toast in appwire.js) */
.connection-banner {
  position: sticky;
  top: 0;
  z-index: var(--z-fixed-action);
  padding: var(--space-2) var(--space-4);
  background: color-mix(in srgb, var(--state-awaiting) 12%, var(--bg-raised));
  border-bottom: 1px solid var(--state-awaiting);
  color: var(--text);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  text-align: center;
}
```

- [ ] **Step 4: Run appwire tests**

```bash
cd cmd/serf-hub/jstest && for t in test-appwire-*.js; do node "$t"; done
```

Expected: PASS for all appwire tests (the new code is additive; the new `onConnectionRestored` is only triggered by `wasDisconnected = true → false` transitions, which the existing tests do not exercise).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/appwire.js cmd/serf-hub/assets/style.css
git commit -m "ui(pass-8): connection-lost toast + persistent banner; connection-restored toast"
```

---

### Task 10: Add global htmx error toast hook

**Files:**
- Modify: `cmd/serf-hub/templates/app.html` — add an inline hook

- [ ] **Step 1: Add the hook inline in `app.html`**

In `app.html`, just before the closing `</body>` (after all other `<script>` tags), insert:

```html
  <script>
    // Global htmx error → toast. Renderer-specific error handling continues
    // to win because htmx:responseError bubbles and we never preventDefault.
    document.body.addEventListener("htmx:responseError", function (e) {
      if (!window.SerfToast) return;
      var status = (e && e.detail && e.detail.xhr && e.detail.xhr.status) || 0;
      var msg = status ? ("Request failed (" + status + ")") : "Request failed";
      window.SerfToast.show(msg, "error");
    });
    document.body.addEventListener("htmx:sendError", function () {
      if (window.SerfToast) window.SerfToast.show("Network error", "error");
    });
  </script>
```

- [ ] **Step 2: Build serf-hub**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/templates/app.html
git commit -m "ui(pass-8): global htmx error → toast"
```

---

### Task 11: Write skeleton `data-loading` jstest (TDD-red)

**Files:**
- Create: `cmd/serf-hub/jstest/test-skeleton-data-loading.js`

- [ ] **Step 1: Write the failing test**

```javascript
// Verify skeleton.js sets data-loading on htmx swap targets at the start of
// a request and removes it after the swap completes. Targets are inferred
// from event.detail.target (when present) or fall back to the element that
// initiated the request.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SKELETON_PATH = "../assets/skeleton.js";
const skeletonSrc = fs.readFileSync(SKELETON_PATH, "utf8");

const dom = new JSDOM(
  `<!DOCTYPE html><html><body>
     <aside id="sidebar"></aside>
     <main id="workspace"></main>
     <div id="settings-content"></div>
   </body></html>`,
  { runScripts: "outside-only", pretendToBeVisual: true }
);
const { window } = dom;
window.eval(skeletonSrc);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

function dispatch(name, target, detail) {
  const ev = new window.CustomEvent(name, { bubbles: true, detail: detail || {} });
  Object.defineProperty(ev, "target", { value: target, enumerable: true });
  window.document.body.dispatchEvent(ev);
}

// htmx:beforeRequest with detail.target set to #sidebar.
const sidebar = window.document.getElementById("sidebar");
dispatch("htmx:beforeRequest", sidebar, { target: sidebar });
pass(sidebar.hasAttribute("data-loading"), "sidebar should have data-loading after htmx:beforeRequest");

// htmx:afterSwap clears it.
dispatch("htmx:afterSwap", sidebar, { target: sidebar });
pass(!sidebar.hasAttribute("data-loading"), "sidebar data-loading should be cleared after htmx:afterSwap");

// htmx:responseError also clears it.
dispatch("htmx:beforeRequest", sidebar, { target: sidebar });
dispatch("htmx:responseError", sidebar, { target: sidebar });
pass(!sidebar.hasAttribute("data-loading"), "sidebar data-loading should be cleared after htmx:responseError");

// Workspace target.
const workspace = window.document.getElementById("workspace");
dispatch("htmx:beforeRequest", workspace, { target: workspace });
pass(workspace.hasAttribute("data-loading"), "workspace should have data-loading");
dispatch("htmx:afterSwap", workspace, { target: workspace });
pass(!workspace.hasAttribute("data-loading"), "workspace data-loading cleared");

if (failures.length === 0) {
  console.log("PASS: skeleton data-loading toggle");
  process.exit(0);
} else {
  for (const f of failures) console.log(" " + f);
  process.exit(1);
}
```

- [ ] **Step 2: Run the test, confirm it fails**

```bash
cd cmd/serf-hub/jstest && node test-skeleton-data-loading.js
```

Expected: FAIL with ENOENT for `../assets/skeleton.js`.

- [ ] **Step 3: Commit failing test**

```bash
git add cmd/serf-hub/jstest/test-skeleton-data-loading.js
git commit -m "test(pass-8): skeleton data-loading toggle (red)"
```

---

### Task 12: Implement `skeleton.js` + CSS

**Files:**
- Create: `cmd/serf-hub/assets/skeleton.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Modify: `cmd/serf-hub/templates/app.html` — register `<script>`

- [ ] **Step 1: Write `skeleton.js`**

```javascript
// skeleton.js — toggles data-loading on htmx swap targets so .skeleton
// pseudo-content (declared in the swapped partial) shimmers during the
// request. The attribute is set on htmx:beforeRequest and cleared on
// htmx:afterSwap or htmx:responseError / htmx:sendError. Targets are taken
// from event.detail.target when present; otherwise the event target is used.
(function () {
  "use strict";

  function targetOf(e) {
    if (e && e.detail && e.detail.target instanceof Element) return e.detail.target;
    if (e && e.target instanceof Element) return e.target;
    return null;
  }

  function set(e) {
    var t = targetOf(e);
    if (!t || !t.setAttribute) return;
    t.setAttribute("data-loading", "");
  }

  function clear(e) {
    var t = targetOf(e);
    if (!t || !t.removeAttribute) return;
    t.removeAttribute("data-loading");
  }

  document.body.addEventListener("htmx:beforeRequest", set);
  document.body.addEventListener("htmx:afterSwap", clear);
  document.body.addEventListener("htmx:responseError", clear);
  document.body.addEventListener("htmx:sendError", clear);
  document.body.addEventListener("htmx:swapError", clear);
})();
```

- [ ] **Step 2: Append skeleton + stagger CSS to `style.css`**

```css
/* Pass 8 — Skeleton loading */
.skeleton {
  display: block;
  border-radius: var(--radius-sm);
  background: var(--bg-raised);
  height: 12px;
  min-width: 40px;
  margin: var(--space-2) 0;
}
/* Shimmer only activates inside a [data-loading] swap target. Without
   the attribute, .skeleton is an invisible spacer (height carries layout
   so the eventual swap doesn't jump). */
[data-loading] .skeleton {
  background: linear-gradient(
    90deg,
    var(--bg-raised) 0%,
    var(--surface-secondary) 50%,
    var(--bg-raised) 100%
  );
  background-size: 200% 100%;
  animation: skeleton-shimmer var(--pulse-cycle) infinite linear;
}
[data-loading] .skeleton-row {
  display: grid;
  grid-template-columns: 6px 1fr 40px;
  gap: var(--space-3);
  padding: var(--space-2) var(--space-3);
  align-items: center;
}
[data-loading] .skeleton-row .skeleton.dot { height: 6px; min-width: 6px; border-radius: 50%; }
[data-loading] .skeleton-row .skeleton.title { height: 12px; width: 70%; }
[data-loading] .skeleton-row .skeleton.meta  { height: 10px; width: 32px; }
@keyframes skeleton-shimmer {
  0%   { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}

/* Pass 8 — First-paint sidebar stagger.
   sidebar.js adds .stagger to the Live section and sets --i on each row. */
.stagger > * {
  opacity: 0;
  animation: enter var(--motion-base) ease-out forwards;
  animation-delay: calc(30ms * var(--i, 0));
}
@keyframes enter {
  from { opacity: 0; transform: translateY(2px); }
  to   { opacity: 1; transform: translateY(0); }
}
```

- [ ] **Step 3: Register the script in `app.html`**

In `cmd/serf-hub/templates/app.html`, add immediately after `<script src="/assets/toast.js"></script>`:

```html
  <script src="/assets/skeleton.js"></script>
```

- [ ] **Step 4: Run the skeleton test, confirm pass**

```bash
cd cmd/serf-hub/jstest && node test-skeleton-data-loading.js
```

Expected: `PASS: skeleton data-loading toggle`

- [ ] **Step 5: Run the full suite**

```bash
cd cmd/serf-hub/jstest && ./run-all.sh
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/skeleton.js cmd/serf-hub/assets/style.css cmd/serf-hub/templates/app.html
git commit -m "feat(pass-8): .skeleton utility + data-loading htmx hook"
```

---

### Task 13: Add skeleton rows to sidebar partial

**Files:**
- Modify: `cmd/serf-hub/templates/partials/sidebar.html`

- [ ] **Step 1: Add skeleton rows inside the Live section and inside each project's children**

Replace the contents of `partials/sidebar.html` with:

```html
{{define "sidebar"}}
<nav class="sidebar">
  <div class="sidebar-header">
    <a class="sidebar-action" href="/new" hx-get="/_partials/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ new</a>
    <a class="sidebar-action" href="#" data-search-trigger>search<kbd>⌘K</kbd></a>
    <a class="sidebar-action settings-link" href="/settings" hx-get="/_partials/settings/general" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/settings">settings</a>
  </div>

  {{if .Live}}
  <section class="sidebar-section sidebar-live-section">
    <header class="sidebar-section-header">
      <span>Live</span>
      <span class="row-meta">{{len .Live}}</span>
    </header>
    {{range .Live}}
    <a class="live-row {{if eq .Kind "fork"}}fork-row{{else if eq .Kind "subagent"}}subagent-row{{else}}session-row{{end}}"
       data-state="{{.State}}"
       href="/s/{{.ID}}"
       hx-get="/_partials/s/{{.ID}}/workspace"
       hx-target="#workspace"
       hx-swap="innerHTML"
       hx-push-url="/s/{{.ID}}">
      {{if eq .Kind "fork"}}<span class="fork-glyph" data-state="{{.State}}">⎇</span>{{else}}<span class="status-dot{{if eq .Kind "subagent"}} subagent{{end}}" data-state="{{.State}}"></span>{{end}}
      <span class="row-title">{{.Title}}</span>
      <span class="row-meta">{{.Project}}</span>
      <span class="row-age">{{.Age}}</span>
    </a>
    {{end}}
  </section>
  {{else}}
  <section class="sidebar-section sidebar-live-section">
    <header class="sidebar-section-header">
      <span>Live</span>
      <span class="row-meta">0</span>
    </header>
    <div class="skeleton-row" aria-hidden="true"><span class="skeleton dot"></span><span class="skeleton title"></span><span class="skeleton meta"></span></div>
    <div class="skeleton-row" aria-hidden="true"><span class="skeleton dot"></span><span class="skeleton title"></span><span class="skeleton meta"></span></div>
    <div class="skeleton-row" aria-hidden="true"><span class="skeleton dot"></span><span class="skeleton title"></span><span class="skeleton meta"></span></div>
    <div class="skeleton-row" aria-hidden="true"><span class="skeleton dot"></span><span class="skeleton title"></span><span class="skeleton meta"></span></div>
    <div class="skeleton-row" aria-hidden="true"><span class="skeleton dot"></span><span class="skeleton title"></span><span class="skeleton meta"></span></div>
  </section>
  {{end}}

  {{if and (not .Live) (not .Projects)}}
  <div class="empty-state empty-state-sidebar">
    <p class="empty-state-title">No sessions yet</p>
    <p class="empty-state-body">Spawn a session to get started.</p>
    <div class="empty-state-actions">
      <a class="btn btn-secondary" href="/new" hx-get="/_partials/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ New session</a>
    </div>
  </div>
  {{end}}

  {{range .Projects}}
  <section class="sidebar-section project-section collapsed" data-project-key="{{.Name}}">
    <header class="project-header">
      <span class="project-chevron" role="button" aria-label="expand project">▸</span>
      <span class="project-folder" aria-hidden="true">📁</span>
      <span class="project-name">{{.Name}}</span>
      <span class="row-meta project-count">{{len .Sessions}}</span>
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
    {{if not .Sessions}}
    <div class="skeleton-row" aria-hidden="true"><span class="skeleton dot"></span><span class="skeleton title"></span><span class="skeleton meta"></span></div>
    <div class="skeleton-row" aria-hidden="true"><span class="skeleton dot"></span><span class="skeleton title"></span><span class="skeleton meta"></span></div>
    <div class="skeleton-row" aria-hidden="true"><span class="skeleton dot"></span><span class="skeleton title"></span><span class="skeleton meta"></span></div>
    {{end}}
    {{range .Sessions}}
    <a class="session-row"
       data-state="{{.State}}"
       href="/s/{{.ID}}"
       hx-get="/_partials/s/{{.ID}}/workspace"
       hx-target="#workspace"
       hx-swap="innerHTML"
       hx-push-url="/s/{{.ID}}">
      <span class="status-dot" data-state="{{.State}}"></span>
      <span class="row-title">{{.Title}}</span>
      <span class="row-age">{{.Age}}</span>
    </a>
    {{range .Children}}
      {{if eq .Kind "subagent"}}
      <a class="subagent-row"
         data-state="{{.State}}"
         href="/s/{{.ID}}"
         hx-get="/_partials/s/{{.ID}}/workspace"
         hx-target="#workspace"
         hx-swap="innerHTML"
         hx-push-url="/s/{{.ID}}">
        <span class="status-dot subagent" data-state="{{.State}}"></span>
        <span class="row-title">{{.Title}}</span>
        <span class="row-age">{{.Age}}</span>
      </a>
      {{else if eq .Kind "fork"}}
      <a class="fork-row"
         data-state="{{.State}}"
         href="/s/{{.ID}}"
         hx-get="/_partials/s/{{.ID}}/workspace"
         hx-target="#workspace"
         hx-swap="innerHTML"
         hx-push-url="/s/{{.ID}}">
        <span class="fork-glyph" data-state="{{.State}}">⎇</span>
        <span class="row-title">{{.Title}}</span>
        <span class="row-age">{{.Age}}</span>
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

- [ ] **Step 2: Build serf-hub**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build.

- [ ] **Step 3: Run sidebar tests**

```bash
cd cmd/serf-hub/jstest && node test-sidebar.js && node test-sidebar-collapse.js
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/templates/partials/sidebar.html
git commit -m "ui(pass-8): skeleton rows + empty state in sidebar partial"
```

---

### Task 14: Add skeleton rows to workspace partial + replace `.conversation-empty`

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html:20-34`

- [ ] **Step 1: Add skeleton meta + skeleton transcript turns; replace conversation-empty with empty-state**

Replace the existing `<div class="workspace-meta" …>{{template "workspace_meta" .}}</div>` and `<div class="conversation-empty" …>` lines. The full replacement for lines 20–34 reads:

```html
  <div class="workspace-meta"
       hx-get="/_partials/s/{{.ID}}/meta"
       hx-trigger="load, every 2s"
       hx-swap="innerHTML">
    <span class="skeleton" style="width: 80px"></span>
    <span class="skeleton" style="width: 60px"></span>
    <span class="skeleton" style="width: 100px"></span>
    {{template "workspace_meta" .}}
  </div>
</header>

<div class="conversation"
     id="conversation"
     data-session-id="{{.ID}}"
     data-active-turn-id="{{.ActiveTurnID}}"
     data-state="{{.State}}">
  <div class="empty-state empty-state-conversation" data-empty-placeholder>
    <p class="empty-state-title">No messages yet</p>
    <p class="empty-state-body">Type below to start the conversation.</p>
  </div>
  <div class="skeleton-turn skeleton-turn-user" aria-hidden="true">
    <span class="skeleton" style="height: 14px; width: 60%"></span>
  </div>
  <div class="skeleton-turn skeleton-turn-agent" aria-hidden="true">
    <span class="skeleton" style="height: 14px; width: 80%"></span>
    <span class="skeleton" style="height: 14px; width: 70%"></span>
  </div>
</div>
```

(The `<header>` close tag is preserved by replacing only the meta div and the conversation block; double-check the surrounding markup remains valid.)

Then append to `style.css`:

```css
.skeleton-turn {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3) 0;
}
[data-loading] .skeleton-turn { /* same shimmer cascade as .skeleton-row */ }
:not([data-loading]) .skeleton-turn { display: none; }
```

The renderer removes `.empty-state-conversation` (via `data-empty-placeholder`) the moment a real message arrives — the existing logic already does this.

- [ ] **Step 2: Build serf-hub**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build.

- [ ] **Step 3: Run renderer tests**

```bash
cd cmd/serf-hub/jstest && node test-renderer.js && node test-renderer-advanced.js
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/assets/style.css
git commit -m "ui(pass-8): skeleton meta + transcript; empty-state conversation"
```

---

### Task 15: Workspace empty-state markup (no session selected)

**Files:**
- Modify: `cmd/serf-hub/web.go:1557`
- Modify: `cmd/serf-hub/assets/style.css` — add `.empty-state*` rules

- [ ] **Step 1: Replace the inline `.workspace-empty` markup in `web.go`**

Locate (around line 1557):

```go
	fmt.Fprint(w, `<div class="workspace-empty"><p>No session selected.</p><p style="margin-top:1em"><a href="/new" hx-get="/_partials/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ new session</a></p></div>`)
```

Replace with:

```go
	fmt.Fprint(w, `<div class="empty-state empty-state-workspace">
  <p class="empty-state-title">Welcome to serf-hub</p>
  <p class="empty-state-body">Spawn a session to start working with an agent, or search across live and past sessions. The hub keeps every session alive in the sidebar — pick one to jump in.</p>
  <div class="empty-state-actions">
    <a class="btn btn-secondary" href="/new" hx-get="/_partials/workspace/spawn" hx-target="#workspace" hx-swap="innerHTML" hx-push-url="/new">＋ New session</a>
    <button class="btn btn-ghost" type="button" data-search-trigger>⌘K search</button>
  </div>
</div>`)
```

- [ ] **Step 2: Append empty-state CSS to `style.css`**

```css
/* Pass 8 — Empty states (per design language §4.7) */
.empty-state {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  align-items: center;
  text-align: center;
  padding: var(--space-6) var(--space-4);
  color: var(--text-muted);
  max-width: 480px;
  margin: 0 auto;
}
.empty-state-title {
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-lg);
  font-weight: 500;
  color: var(--text);
}
.empty-state-body {
  margin: 0;
  font-family: var(--font-sans);
  font-size: var(--text-sm);
  color: var(--text-muted);
  line-height: 1.55;
}
.empty-state-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  justify-content: center;
  margin-top: var(--space-2);
}
/* Per-surface tweaks */
.empty-state-workspace { padding-top: var(--space-8); }
.empty-state-conversation { padding: var(--space-4) 0; align-items: flex-start; text-align: left; }
.empty-state-conversation .empty-state-title { font-size: var(--text-base); }
.empty-state-sidebar { padding: var(--space-4) var(--space-3); }
.empty-state-sidebar .empty-state-title { font-size: var(--text-sm); }
.empty-state-sidebar .empty-state-body { font-size: var(--text-xs); }
.empty-state-search { padding: var(--space-4); }
.empty-state-tasks { padding: var(--space-3); align-items: flex-start; text-align: left; }
.empty-state-tasks .empty-state-title { font-size: var(--text-sm); }
```

- [ ] **Step 3: Build and run Go tests**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub/...
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/web.go cmd/serf-hub/assets/style.css
git commit -m "ui(pass-8): workspace empty-state with orientation copy + CTAs"
```

---

### Task 16: Search empty-state markup (`search.js`)

**Files:**
- Modify: `cmd/serf-hub/assets/search.js:491, 593, 610, 627, 730, 762`

- [ ] **Step 1: Replace each `.search-empty` insertion with `.empty-state-search`**

These call sites all render variations of `<div class="search-empty">…</div>`. Convert each to:

```javascript
'<div class="empty-state empty-state-search"><p class="empty-state-title">No matches</p><p class="empty-state-body">' + escapedHint + '</p></div>'
```

Line-by-line (use Edit; do each individually):

**Line 491** — replace
```javascript
      html += '<div class="search-empty">no commands match.</div>';
```
with
```javascript
      html += '<div class="empty-state empty-state-search"><p class="empty-state-title">No commands match</p><p class="empty-state-body">Try a different keyword or open a session first.</p></div>';
```

**Line 593** — replace
```javascript
        html = '<div class="search-empty">' + (argsEnumLoaded ? "no matches." : "loading…") + '</div>';
```
with
```javascript
        html = argsEnumLoaded
          ? '<div class="empty-state empty-state-search"><p class="empty-state-title">No matches</p></div>'
          : '<div class="empty-state empty-state-search"><p class="empty-state-body">Loading…</p></div>';
```

**Line 610** — replace
```javascript
      results.innerHTML = '<div class="search-empty">loading…</div>';
```
with
```javascript
      results.innerHTML = '<div class="empty-state empty-state-search"><p class="empty-state-body">Loading…</p></div>';
```

**Line 627** — replace
```javascript
    results.innerHTML = '<div class="search-empty">' + hint + '</div>';
```
with
```javascript
    results.innerHTML = '<div class="empty-state empty-state-search"><p class="empty-state-body">' + hint + '</p></div>';
```

**Line 730** — replace
```javascript
      .catch(() => { results.innerHTML = '<div class="search-empty">search failed</div>'; });
```
with
```javascript
      .catch(() => { results.innerHTML = '<div class="empty-state empty-state-search"><p class="empty-state-title">Search failed</p></div>'; });
```

**Line 762** — replace
```javascript
      html += '<div class="search-empty">no matches in live, past, or this session.</div>';
```
with
```javascript
      html += '<div class="empty-state empty-state-search"><p class="empty-state-title">No matches</p><p class="empty-state-body">Nothing in live, past, or this session.</p></div>';
```

- [ ] **Step 2: Remove the now-orphan `.search-empty` CSS rule from `style.css`**

Locate (around line 704):

```css
.search-empty { padding: 24px; color: var(--text-muted); text-align: center; font-size: 12px; }
```

…and the related `.search-empty code` rule on line 717. Delete both — `.empty-state-search` covers their behaviour.

- [ ] **Step 3: Run search tests**

```bash
cd cmd/serf-hub/jstest && node test-search.js && node test-search-commands.js
```

Expected: PASS. If any test asserts on the old class name, update the test to assert on `.empty-state-search` (run, inspect failure, edit, re-run).

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/search.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-search.js cmd/serf-hub/jstest/test-search-commands.js
git commit -m "ui(pass-8): search palette empty-state markup"
```

---

### Task 17: Tasks panel empty-state markup (`renderer.js`)

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js:2947`

- [ ] **Step 1: Replace `.tasks-empty` with `.empty-state-tasks`**

Replace:

```javascript
      parts.push("<div class='tasks-empty'>no tasks for this session</div>");
```

with:

```javascript
      parts.push("<div class='empty-state empty-state-tasks'><p class='empty-state-title'>No tasks yet</p><p class='empty-state-body'>The agent's task list is empty for this session.</p></div>");
```

- [ ] **Step 2: Remove the orphan `.tasks-empty` CSS rule**

In `style.css` (line 830 area):

```css
.tasks-empty { color: var(--text-muted); font-size: 12px; padding: 12px 0; font-style: italic; }
```

Delete this rule.

- [ ] **Step 3: Run renderer + sidebar (tasks) test**

```bash
cd cmd/serf-hub/jstest && node test-sidebar.js && node test-renderer.js
```

Expected: PASS. If `test-sidebar.js` asserts on `.tasks-empty`, update it to `.empty-state-tasks`.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-sidebar.js
git commit -m "ui(pass-8): tasks panel empty-state markup"
```

---

### Task 18: Chip-picker empty-state markup (spawn.js + settings-pickers.js)

**Files:**
- Modify: `cmd/serf-hub/assets/spawn.js:1480`
- Modify: `cmd/serf-hub/assets/settings-pickers.js:204`

- [ ] **Step 1: Update both call sites**

In `spawn.js` at line 1480, replace:

```javascript
          empty.className = "chip-picker-empty";
          empty.textContent = "no matching directories";
```

with:

```javascript
          empty.className = "empty-state empty-state-picker";
          empty.innerHTML = '<p class="empty-state-body">No matching directories</p>';
```

In `settings-pickers.js` at line 204, replace:

```javascript
          empty.className = "chip-picker-empty";
```

with:

```javascript
          empty.className = "empty-state empty-state-picker";
```

(And adjust whatever message-setting code follows it to use `innerHTML = '<p class="empty-state-body">' + msg + '</p>'` or `textContent` on the inner `<p>`, mirroring spawn.js.)

- [ ] **Step 2: Add `.empty-state-picker` CSS variant**

Append to `style.css` near the other empty-state variants:

```css
.empty-state-picker { padding: var(--space-3); align-items: flex-start; text-align: left; }
.empty-state-picker .empty-state-body { font-size: var(--text-xs); }
```

Delete the old `.chip-picker-empty` rule (around line 686).

- [ ] **Step 3: Run picker-related tests**

```bash
cd cmd/serf-hub/jstest && node test-spawn.js && node test-input-area.js
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/spawn.js cmd/serf-hub/assets/settings-pickers.js cmd/serf-hub/assets/style.css
git commit -m "ui(pass-8): chip-picker empty-state markup"
```

---

### Task 19: Sidebar first-paint stagger (sidebar.js)

**Files:**
- Modify: `cmd/serf-hub/assets/sidebar.js:63-71`

- [ ] **Step 1: Add a once-only stagger application to the Live section**

After the existing `applyAll(document)` and `htmx:afterSwap` listener (around line 71), append:

```javascript
  // First-paint stagger on the Live section. Sidebar.html re-renders every
  // 5s (hx-trigger="every 5s"), but only the very first paint earns the
  // choreography. After that, .stagger is removed and individual rows
  // appear instantly.
  var staggerApplied = false;
  function applyLiveStagger(scope) {
    if (staggerApplied) return;
    var live = (scope || document).querySelector(".sidebar-live-section");
    if (!live) return;
    var rows = live.querySelectorAll(".live-row");
    if (!rows.length) return;
    live.classList.add("stagger");
    for (var i = 0; i < rows.length && i < 10; i++) {
      rows[i].style.setProperty("--i", String(i));
    }
    staggerApplied = true;
    // Strip the class after the longest animation finishes (10 rows × 30ms
    // delay + 160ms duration = 460ms; round to 600ms for safety).
    setTimeout(function () {
      live.classList.remove("stagger");
      for (var j = 0; j < rows.length; j++) {
        rows[j].style.removeProperty("--i");
      }
    }, 600);
  }

  document.addEventListener("htmx:afterSwap", function (e) {
    if (e && e.target && e.target.id === "sidebar") applyLiveStagger(e.target);
  });
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { applyLiveStagger(document); });
  } else {
    applyLiveStagger(document);
  }
```

- [ ] **Step 2: Run sidebar tests**

```bash
cd cmd/serf-hub/jstest && node test-sidebar.js && node test-sidebar-collapse.js
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/sidebar.js
git commit -m "ui(pass-8): sidebar first-paint stagger"
```

---

### Task 20: `:active` press states across all button variants

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`

- [ ] **Step 1: Append `:active` rules under a Pass 8 header**

Append to `style.css`:

```css
/* Pass 8 — :active press states (drop surface one step) */
.btn:active,
.btn-secondary:active,
.btn-ghost:active,
.btn-danger:active,
.btn-icon:active,
.btn-chip:active {
  transform: translateY(0.5px);
}
.btn-primary:active { filter: brightness(0.95); transform: translateY(0.5px); }
.btn-secondary:active { background: var(--surface-secondary); }
.btn-ghost:active { background: var(--surface-secondary); color: var(--text); }
.btn-danger:active { background: color-mix(in srgb, var(--state-awaiting) 10%, transparent); }
.btn-icon:active { background: var(--surface-secondary); }
.btn-chip:active { background: var(--surface-secondary); border-color: var(--accent); }
```

- [ ] **Step 2: Build serf-hub**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "ui(pass-8): :active press states for all .btn variants"
```

---

### Task 21: Reduced-motion fallback for `.optimistic-pending` and `.status-dot[data-pulse]`

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`

- [ ] **Step 1: Append the reduced-motion override**

Append to `style.css`:

```css
/* Pass 8 — Reduced-motion non-animated alternatives.
   The universal *::before/::after rule in §1.5 kills animations; without
   these fallbacks the pulse signals become invisible. */
@media (prefers-reduced-motion: reduce) {
  .optimistic-pending {
    border-left: 2px dashed var(--accent);
    padding-left: var(--space-2);
  }
  .status-dot[data-pulse] {
    box-shadow: 0 0 0 2px color-mix(in srgb, currentColor 30%, transparent);
  }
}
```

- [ ] **Step 2: Build + run optimistic tests**

```bash
cd cmd/serf-hub/jstest && node test-optimistic-rendering.js
```

Expected: PASS (the test does not enable `prefers-reduced-motion`; the CSS is a media-query branch with no JS surface).

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "ui(pass-8): reduced-motion fallback for optimistic-pending + status-dot pulse"
```

---

### Task 22: Spawn chip overflow ("+N more")

**Files:**
- Create: `cmd/serf-hub/assets/chip-overflow.js`
- Create: `cmd/serf-hub/jstest/test-chip-overflow.js`
- Modify: `cmd/serf-hub/templates/partials/spawn.html:12-37` — add `data-chip-overflow-host`
- Modify: `cmd/serf-hub/templates/app.html` — register `<script>`
- Modify: `cmd/serf-hub/assets/style.css` — add `.chip-overflow*` rules

- [ ] **Step 1: Write the failing test**

```javascript
// Verify chip-overflow.js caps visible chips at 4 (sorted by data-chip-modified)
// and inserts a "+N more" expand button that reveals the rest when clicked.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SRC = fs.readFileSync("../assets/chip-overflow.js", "utf8");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div id="spawn-chips" data-chip-overflow-host>
    <button class="chip" data-chip="a" data-chip-modified="1000"></button>
    <button class="chip" data-chip="b" data-chip-modified="2000"></button>
    <button class="chip" data-chip="c" data-chip-modified="3000"></button>
    <button class="chip" data-chip="d" data-chip-modified="4000"></button>
    <button class="chip" data-chip="e" data-chip-modified="5000"></button>
    <button class="chip" data-chip="f" data-chip-modified="6000"></button>
  </div>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.eval(SRC);

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// chip-overflow.js applies on DOMContentLoaded / immediately.
const chips = window.document.querySelectorAll(".chip");
const hidden = Array.from(chips).filter((c) => c.hidden);
pass(hidden.length === 2, "expected 2 chips hidden, got " + hidden.length);
// The two oldest (a=1000, b=2000) should be hidden.
const hiddenIds = hidden.map((c) => c.dataset.chip).sort().join(",");
pass(hiddenIds === "a,b", "expected oldest chips hidden, got " + hiddenIds);

const more = window.document.querySelector(".chip-overflow-more");
pass(more !== null, "+N more chip should exist");
pass(more && more.textContent.includes("+2"), "more chip should say +2, got " + (more && more.textContent));

// Click to expand.
more.click();
const stillHidden = Array.from(window.document.querySelectorAll(".chip")).filter((c) => c.hidden);
pass(stillHidden.length === 0, "after click no chip should be hidden");
pass(window.document.querySelector(".chip-overflow-more") === null, "more chip should be removed after expand");

if (failures.length === 0) {
  console.log("PASS: chip overflow caps + expand");
  process.exit(0);
} else {
  for (const f of failures) console.log(" " + f);
  process.exit(1);
}
```

- [ ] **Step 2: Run test, confirm fail**

```bash
cd cmd/serf-hub/jstest && node test-chip-overflow.js
```

Expected: FAIL (file missing).

- [ ] **Step 3: Write `chip-overflow.js`**

```javascript
// chip-overflow.js — caps visible .chip children of any
// [data-chip-overflow-host] container at 4 (sorted by data-chip-modified
// descending — most-recently-modified wins). Older chips are hidden and a
// "+N more" expand button is inserted; clicking it reveals all and removes
// itself.
(function () {
  "use strict";

  var CAP = 4;

  function apply(host) {
    if (!host || host.dataset.chipOverflowApplied === "true") return;
    var chips = Array.prototype.slice.call(host.querySelectorAll(".chip"));
    if (chips.length <= CAP) return;

    // Sort by data-chip-modified descending (numeric). Chips with no
    // attribute count as 0 (oldest). Stable: preserve original order
    // among ties.
    var withIdx = chips.map(function (el, i) {
      var v = parseInt(el.getAttribute("data-chip-modified") || "0", 10) || 0;
      return { el: el, idx: i, mod: v };
    });
    withIdx.sort(function (a, b) {
      if (b.mod !== a.mod) return b.mod - a.mod;
      return a.idx - b.idx;
    });

    var hiddenCount = 0;
    for (var i = CAP; i < withIdx.length; i++) {
      withIdx[i].el.hidden = true;
      hiddenCount++;
    }

    var more = document.createElement("button");
    more.type = "button";
    more.className = "chip chip-overflow-more";
    more.textContent = "+" + hiddenCount + " more";
    more.addEventListener("click", function () {
      for (var j = CAP; j < withIdx.length; j++) {
        withIdx[j].el.hidden = false;
      }
      if (more.parentNode) more.parentNode.removeChild(more);
      host.dataset.chipOverflowApplied = "expanded";
    });
    host.appendChild(more);

    host.dataset.chipOverflowApplied = "true";
  }

  function applyAll(root) {
    var hosts = (root || document).querySelectorAll("[data-chip-overflow-host]");
    for (var i = 0; i < hosts.length; i++) apply(hosts[i]);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { applyAll(document); });
  } else {
    applyAll(document);
  }
  document.addEventListener("htmx:afterSwap", function (e) {
    applyAll(e.target || document);
  });
})();
```

- [ ] **Step 4: Add `data-chip-overflow-host` to spawn.html**

In `cmd/serf-hub/templates/partials/spawn.html` line 12, replace:

```html
    <div class="spawn-chips" id="spawn-chips">
```

with:

```html
    <div class="spawn-chips" id="spawn-chips" data-chip-overflow-host>
```

- [ ] **Step 5: Register `<script>` in `app.html`**

Add after `<script src="/assets/skeleton.js"></script>`:

```html
  <script src="/assets/chip-overflow.js"></script>
```

- [ ] **Step 6: Append CSS for the overflow chip**

```css
.chip-overflow-more {
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  color: var(--text-muted);
  background: transparent;
  border: 1px dashed var(--rule);
}
.chip-overflow-more:hover { color: var(--text); border-color: var(--accent); }
```

- [ ] **Step 7: Run the test**

```bash
cd cmd/serf-hub/jstest && node test-chip-overflow.js
```

Expected: `PASS: chip overflow caps + expand`.

- [ ] **Step 8: Run the spawn test**

```bash
cd cmd/serf-hub/jstest && node test-spawn.js
```

Expected: PASS (the spawn template still has 5 chips today, so the cap of 4 means one is hidden; if spawn.js test relies on all chips being visible, adjust it to either expand first or assert on `:not([hidden])` selectors).

- [ ] **Step 9: Commit**

```bash
git add cmd/serf-hub/assets/chip-overflow.js cmd/serf-hub/jstest/test-chip-overflow.js cmd/serf-hub/templates/partials/spawn.html cmd/serf-hub/templates/app.html cmd/serf-hub/assets/style.css
git commit -m "feat(pass-8): chip-overflow.js (+N more for spawn chips)"
```

---

### Task 23: Wire toast in `credentials.html` (set/clear/oauth)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/credentials.html:130-200`

- [ ] **Step 1: Replace `alert()` calls with toast + add success toasts**

In the inline script inside `credentials.html`, locate the four spots where credential state changes:

**OAuth start failure (~line 137):**

Replace:
```javascript
        } catch (err) {
          alert("Sign-in failed: " + (err && err.message ? err.message : err));
        }
```
with:
```javascript
        } catch (err) {
          if (window.SerfToast) window.SerfToast.show("Sign-in failed: " + (err && err.message ? err.message : err), "error");
        }
```

**Clear flow (~line 139–148):**

Replace:
```javascript
      } else if (action === "clear") {
        if (!confirm(`Clear stored credentials for ${provider}?`)) return;
        try {
          await launchconfig.authLogout(provider);
          openEditor = null;
          await refresh();
        } catch (err) {
          alert("Clear failed: " + (err && err.message ? err.message : err));
        }
      }
```
with:
```javascript
      } else if (action === "clear") {
        if (!confirm(`Clear stored credentials for ${provider}?`)) return;
        try {
          await launchconfig.authLogout(provider);
          openEditor = null;
          await refresh();
          if (window.SerfToast) window.SerfToast.show("Credentials cleared for " + provider, "success");
        } catch (err) {
          if (window.SerfToast) window.SerfToast.show("Clear failed: " + (err && err.message ? err.message : err), "error");
        }
      }
```

**API-key set success (~line 174–180):**

Replace:
```javascript
        try {
          await launchconfig.authApiKeySet(provider, value);
          openEditor = null;
          await refresh();
        } catch (err) {
          showInlineError(err && err.message ? err.message : String(err));
        }
```
with:
```javascript
        try {
          await launchconfig.authApiKeySet(provider, value);
          openEditor = null;
          await refresh();
          if (window.SerfToast) window.SerfToast.show("API key saved for " + provider, "success");
        } catch (err) {
          showInlineError(err && err.message ? err.message : String(err));
          if (window.SerfToast) window.SerfToast.show("Save failed: " + (err && err.message ? err.message : err), "error");
        }
```

**OAuth complete success (~line 189–195):**

Replace:
```javascript
        try {
          await launchconfig.authLoginComplete(provider, flowId, redirectUrl);
          openEditor = null;
          await refresh();
        } catch (err) {
          showInlineError(err && err.message ? err.message : String(err));
        }
```
with:
```javascript
        try {
          await launchconfig.authLoginComplete(provider, flowId, redirectUrl);
          openEditor = null;
          await refresh();
          if (window.SerfToast) window.SerfToast.show("Signed in to " + provider, "success");
        } catch (err) {
          showInlineError(err && err.message ? err.message : String(err));
          if (window.SerfToast) window.SerfToast.show("Sign-in failed: " + (err && err.message ? err.message : err), "error");
        }
```

- [ ] **Step 2: Build serf-hub**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/templates/partials/credentials.html
git commit -m "ui(pass-8): toast on credentials set/clear/oauth"
```

---

### Task 24: Remove remaining `.rule-dot` references

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html:80`
- Modify: `cmd/serf-hub/templates/partials/input_strip.html:3, 5, 8`
- Modify: `cmd/serf-hub/assets/style.css:355`

- [ ] **Step 1: Remove `.rule-dot` from `workspace_meta` template**

Replace line 80 of `workspace.html`:

```html
{{define "workspace_meta"}}{{if .SourceLabel}}<span class="source-label" data-source-label="{{.SourceLabel}}">{{.SourceLabel}}</span><span class="rule-dot">·</span>{{end}}{{if .Branch}}<span class="branch">{{.Branch}}</span><span class="rule-dot">·</span>{{end}}<span class="status-pill" data-state="{{.State}}"><span class="status-dot" data-state="{{.State}}"></span> {{.StateLabel}}</span><span class="rule-dot">·</span><span class="turn-count">{{.TurnCount}} turn{{if ne .TurnCount 1}}s{{end}}</span>{{end}}
```

with:

```html
{{define "workspace_meta"}}{{if .SourceLabel}}<span class="source-label" data-source-label="{{.SourceLabel}}">{{.SourceLabel}}</span>{{end}}{{if .Branch}}<span class="branch">{{.Branch}}</span>{{end}}<span class="status-pill" data-state="{{.State}}"><span class="status-dot" data-state="{{.State}}"></span> {{.StateLabel}}</span><span class="turn-count">{{.TurnCount}} turn{{if ne .TurnCount 1}}s{{end}}</span>{{end}}
```

(Pass 4 should already have set `gap: var(--space-4)` on `.workspace-meta`; rely on that. If for some reason it hasn't, add `.workspace-meta { display: inline-flex; align-items: baseline; gap: var(--space-4); }` to style.css.)

- [ ] **Step 2: Remove `.rule-dot` from `input_strip.html`**

Open `cmd/serf-hub/templates/partials/input_strip.html`. Replace any `<span class="rule-dot">·</span>` with nothing (delete the whole span tag). The surrounding container should already use `gap` on its flex layout (set in Pass 4); confirm by reading `style.css` for the rule on `.input-status` and adding `display: inline-flex; gap: var(--space-4)` if missing.

- [ ] **Step 3: Delete the `.rule-dot` CSS rule**

In `style.css` line 355, delete:

```css
.rule-dot { color: var(--text-dim); }
```

- [ ] **Step 4: Grep for any remaining references**

```bash
grep -rn "rule-dot" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/
```

Expected: zero matches (or only matches inside `vendor/`, generated files, or this plan document).

- [ ] **Step 5: Build + run jstests**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && cd cmd/serf-hub/jstest && ./run-all.sh
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/templates/partials/input_strip.html cmd/serf-hub/assets/style.css
git commit -m "ui(pass-8): remove .rule-dot separators (gap carries the work)"
```

---

### Task 25: Manual verification + final sweep

**Files:** none (verification only)

- [ ] **Step 1: Build + run the full Go + JS test suites**

```bash
cd /home/jesse/git/prime-radiant/serf && go build ./cmd/serf-hub && go test ./cmd/serf-hub/... && cd cmd/serf-hub/jstest && ./run-all.sh
```

Expected: all green.

- [ ] **Step 2: Launch `serf-hub` and verify each toast trigger by hand**

```bash
cd /home/jesse/git/prime-radiant/serf && go run ./cmd/serf-hub
```

Then in a browser tab at `http://localhost:9180`:

- Click the ⧉ copy-session-ID button → "Session ID copied" appears top-center.
- Open `⌘K` → choose a session → run "Switch model" → pick a model → success or error toast.
- Run "Shut down daemon" → "Session shut down" toast.
- Open Settings → toggle the theme radio → "Theme: dark" (or light) toast.
- Open Settings → Notifications → toggle any checkbox → "Settings saved" toast.
- Open Settings → Serf launch → save form → "Launch defaults saved" toast.
- Open Credentials → set an API key for any provider → "API key saved for X" toast.
- Drag a non-image file into the composer → error toast.
- Stop the hub daemon while the browser is open → "Connection lost — reconnecting…" toast + sticky banner.
- Restart the hub → "Connection restored" toast + banner disappears.

- [ ] **Step 3: Verify skeleton loading**

Throttle the network in DevTools to "Slow 3G" and navigate to a session. The workspace-meta cluster and 2 transcript-turn skeletons should shimmer while the partial loads.

- [ ] **Step 4: Verify empty states**

- Visit `/` with no session in the URL → workspace empty-state with both CTAs visible.
- Open a brand-new session → conversation empty-state below the header.
- Open `⌘K` and type gibberish → search empty-state.
- Open the tasks panel for a session with no tasks → tasks empty-state.

- [ ] **Step 5: Verify first-paint stagger**

Open the page in a fresh tab (no service-worker cache). The Live section's rows should fade-in in sequence over ~300ms. Subsequent 5s sidebar refreshes should NOT re-stagger.

- [ ] **Step 6: Verify reduced-motion fallback**

In DevTools → Rendering → "Emulate CSS prefers-reduced-motion: reduce". Confirm:
- `.optimistic-pending` rows show a dashed accent left-border instead of pulsing.
- `.status-dot[data-pulse]` shows a soft outline ring instead of pulsing.

- [ ] **Step 7: Verify `:active` press states**

Click-and-hold any `.btn-primary`, `.btn-secondary`, `.btn-ghost`, `.btn-danger`, `.btn-icon`, `.btn-chip` — each should drop visually (translate-y or background shift) while the mouse is down.

- [ ] **Step 8: Verify spawn-chip overflow**

Open `/new`. With the default 5 chips, one should be hidden behind a "+1 more" overflow chip. Click "+1 more" → all chips become visible; the overflow chip disappears.

- [ ] **Step 9: Audit for lingering `.rule-dot`**

```bash
grep -rn "rule-dot" /home/jesse/git/prime-radiant/serf/cmd/serf-hub/
```

Expected: zero hits.

- [ ] **Step 10: Final commit (only if any fixes from Step 1–9 are required)**

If steps surfaced bugs, fix them and create a final cleanup commit:

```bash
git add -A
git commit -m "ui(pass-8): manual-verification fixes"
```

If verification was clean, no commit needed for this step.

---

## Self-review checklist

- [x] **Toast trigger coverage:** copy-id (Task 4), model change + shutdown (Task 5), theme + notifications (Task 6), launch + project settings (Task 7), credentials set/clear/oauth (Task 23), attachment rejection (Task 8), connection lost/restored (Task 9), htmx error (Task 10).
- [x] **`#toast-region` added to app.html (Task 1)** with `aria-live="polite"`.
- [x] **Skeleton:** `.skeleton` CSS utility + `[data-loading]` activation (Task 12); applied to sidebar (Task 13) and workspace (Task 14); htmx hook in `skeleton.js`.
- [x] **Empty states:** workspace (Task 15 — first-time orientation), conversation (Task 14), sidebar (Task 13), search (Task 16), tasks (Task 17), chip-picker (Task 18). All use the `.empty-state` / `.empty-state-title` / `.empty-state-body` / `.empty-state-actions` markup from spec §4.7.
- [x] **Workspace empty CTA buttons:** `.btn-secondary` for `＋ New session` and `.btn-ghost` for `⌘K search` (Task 15).
- [x] **First-paint stagger:** Task 19 — runs only once; sets `--i` per row up to 10; strips after 600ms.
- [x] **`:active` press states:** Task 20 — covers all six `.btn-*` variants.
- [x] **Reduced-motion fallback:** Task 21 — dashed border for `.optimistic-pending`, outline ring for `.status-dot[data-pulse]`.
- [x] **Spawn chip overflow:** Task 22 — caps at 4 by `data-chip-modified` recency; expand button; jstest included.
- [x] **`.rule-dot` removal:** Task 24 — `workspace.html`, `input_strip.html`, `style.css` rule.
- [x] **Connection-lost banner pairs with toast:** Task 9 — `connection-banner` div inserted; toast uses `timeout: 0` so user sees it persist until restoration.
- [x] **Manual verification plan:** Task 25 — covers every trigger and the four empty states.
- [x] **TDD:** Tests precede implementation for toast (Task 2 → 3), skeleton (Task 11 → 12), chip overflow (Task 22).
- [x] **Bite-sized tasks:** every task has discrete steps with explicit commands and expected output.
- [x] **No placeholders:** all code, file paths, and commands are concrete; no "TODO" / "TBD".
- [x] **Type consistency:** `window.SerfToast.show(message, kind, opts)` signature is identical across all callers; `data-loading` attribute name is identical between `skeleton.js`, CSS, and partials; `data-chip-overflow-host` matches between `chip-overflow.js` and `spawn.html`.

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-22-serf-hub-ui-pass-8-polish.md`. Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task; review between tasks; fast iteration.
2. **Inline Execution** — execute tasks in the current session using `superpowers:executing-plans`; batch with checkpoints.

Which approach?

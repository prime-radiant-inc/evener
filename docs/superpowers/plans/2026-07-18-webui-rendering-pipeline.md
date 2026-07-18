# Web Hub Rendering Pipeline (Plan A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate streaming jank in the Serf web hub transcript: frame-batched live events, coalesced markdown parsing, frozen-head/raw-tail long messages, idempotent finalization, append-only tool output, browser-native windowing, throttled scroll handling.

**Architecture:** No bundler, vanilla JS modules sharing `window.SerfRendererInternal`, loaded in dependency order (see `cmd/serf-hub/jstest/load-renderer.js` and `cmd/serf-hub/templates/app.html`). Live socket events batch per frame at the single `deliverNotification` site; synchronous replay paths (hydration, prepend) bypass the queue. Spec: `docs/superpowers/specs/2026-07-18-webui-foundation-experience-design.md` (§1 + Increments 1–4). Increments 5–9 (layout/CSS/launchpad) are Plan B, written after this lands.

**Tech Stack:** vanilla JS, jsdom-based `jstest` harness (`cmd/serf-hub/jstest/run-all.sh`), Go templates, embedded assets (`make build-hub`).

## Global Constraints

- Every commit leaves `make build-hub` + `cmd/serf-hub/jstest/run-all.sh` + `go test ./cmd/serf-hub` green.
- TDD: failing test first, minimal implementation, commit.
- `handleData(kind, data)` keeps its signature and stays SYNCHRONOUS for direct callers (hydration, prepend, tests). Only live socket delivery batches.
- The reconcile invariant (`renderer.js:677-684`): `reconcilePendingFromNotification` runs once per notification, after that notification's events apply — preserved exactly under batching.
- The streaming message element keeps `.assistant-message` + `data-turn-id` at all times.
- New renderer module files (if any) must be added to BOTH `templates/app.html` `<script>` list and `jstest/load-renderer.js` `RENDERER_FILES`, in dependency order. This plan adds NO new module files — everything lands in existing ones.
- jstest pattern (mirror `test-renderer-thinking.js`): JSDOM with `pretendToBeVisual: true` unless stated otherwise, `window.marked = { parse: (t) => t }`, `require("./load-renderer").evalRenderer(window)`, `window.SerfRenderer.init(conv)`, async IIFE, `process.exit(0)` at the end (renderer pollers keep the loop alive).
- Work branch: `webui-joy` (already created). Do not touch files outside `cmd/serf-hub/` in this plan.

---

### Task 1: Renderer-file mirror test

Guards the app.html ≡ RENDERER_FILES invariant that today is enforced by convention only.

**Files:**
- Create: `cmd/serf-hub/jstest/test-renderer-file-mirror.js`

**Interfaces:**
- Consumes: `require("./load-renderer").RENDERER_FILES` (array of filenames in dependency order).
- Produces: nothing (test only).

- [ ] **Step 1: Write the failing test**

```js
// The no-bundler renderer bundle must load in the same dependency order in the
// browser (templates/app.html) and in tests (load-renderer.js RENDERER_FILES).
// A module added to one list but not the other breaks either the app or every
// renderer test — this mirror makes the drift a loud failure.
const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { RENDERER_FILES } = require("./load-renderer");

const appHtml = fs.readFileSync(path.resolve(__dirname, "../templates/app.html"), "utf8");
const srcs = [...appHtml.matchAll(/<script[^>]+src="([^"]+)"/g)].map((m) => m[1]);
const scriptNames = srcs.map((s) => s.split("/").pop());
const mirrored = scriptNames.filter((n) => RENDERER_FILES.includes(n));
assert.deepStrictEqual(
  mirrored,
  RENDERER_FILES,
  "templates/app.html must load exactly the RENDERER_FILES, in the same order: " +
    JSON.stringify({ mirrored, RENDERER_FILES })
);
console.log("PASS: app.html script order mirrors RENDERER_FILES");
```

- [ ] **Step 2: Run it — expect PASS immediately** (the invariant holds today; this is a characterization test)

Run: `cd cmd/serf-hub/jstest && node test-renderer-file-mirror.js`
Expected: PASS. (If it fails, the lists have already drifted — fix whichever list is wrong before continuing.)

- [ ] **Step 3: Commit**

```bash
git add cmd/serf-hub/jstest/test-renderer-file-mirror.js
git commit -m "jstest: mirror app.html script order against RENDERER_FILES"
```

---

### Task 2: `scheduleFrame`/`cancelFrame` helpers (rAF feature-guard)

Plain jsdom (6 jstest files) has NO `requestAnimationFrame` — any new unguarded rAF call crashes those suites. One guarded helper now; all later tasks use it.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` (add methods near `bindWorkspaceViewport`, ~line 880; adopt in `bindWorkspaceViewport` at ~896)
- Test: `cmd/serf-hub/jstest/test-renderer-schedule-frame.js`

**Interfaces:**
- Produces (used by Tasks 5, 6, 13, 15):
  - `SerfRenderer.scheduleFrame(cb)` — schedules `cb` on rAF when available and document visible; else `setTimeout(cb, 16)`. Returns nothing.
  - `SerfRenderer.cancelFrame()` — cancels the pending scheduled callback, if any.

- [ ] **Step 1: Write the failing test** (deliberately NO `pretendToBeVisual` → no rAF)

```js
// scheduleFrame must work where window.requestAnimationFrame is undefined
// (plain jsdom, and as a proxy for exotic embedded webviews).
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only" });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const R = window.SerfRenderer;
R.init(window.document.getElementById("conversation"));

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  pass(typeof R.scheduleFrame === "function", "scheduleFrame exists");
  pass(typeof window.requestAnimationFrame === "undefined", "test env really has no rAF");
  let ran = 0;
  R.scheduleFrame(() => { ran++; });
  await new Promise((r) => setTimeout(r, 50));
  pass(ran === 1, "scheduleFrame falls back to a timer when rAF is missing");
  // cancelFrame suppresses a pending callback.
  R.scheduleFrame(() => { ran++; });
  R.cancelFrame();
  await new Promise((r) => setTimeout(r, 50));
  pass(ran === 1, "cancelFrame drops the pending callback");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: scheduleFrame works without requestAnimationFrame");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-renderer-schedule-frame.js`
Expected: FAIL — "scheduleFrame exists"

- [ ] **Step 3: Implement**

In `renderer.js`, add to the `SerfRenderer` object (near `bindWorkspaceViewport`):

```js
    // scheduleFrame runs cb on the next animation frame when rAF exists and the
    // tab is visible; otherwise on a 16ms timer. Plain jsdom has no rAF at all,
    // and hidden tabs never fire it — never call rAF unguarded.
    scheduleFrame(cb) {
      this.cancelFrame();
      const hidden = typeof document !== "undefined" && document.hidden;
      if (!hidden && typeof window !== "undefined" && typeof window.requestAnimationFrame === "function") {
        this.scheduledFrameRaf = window.requestAnimationFrame(() => {
          this.scheduledFrameRaf = null;
          cb();
        });
        return;
      }
      this.scheduledFrameTimer = setTimeout(() => {
        this.scheduledFrameTimer = null;
        cb();
      }, 16);
    },

    cancelFrame() {
      if (this.scheduledFrameRaf != null && typeof window !== "undefined" && typeof window.cancelAnimationFrame === "function") {
        try { window.cancelAnimationFrame(this.scheduledFrameRaf); } catch (e) {}
      }
      if (this.scheduledFrameTimer != null) clearTimeout(this.scheduledFrameTimer);
      this.scheduledFrameRaf = null;
      this.scheduledFrameTimer = null;
    },
```

Then in `bindWorkspaceViewport` (~line 886-897), replace the hand-rolled rAF:

```js
      let frame = null;
      const apply = () => {
        frame = null;
        ...
      };
      const schedule = () => {
        if (frame != null) return;
        frame = window.requestAnimationFrame(apply);
      };
```

with:

```js
      const apply = () => {
        if (this.sessionId !== sessionId || document.getElementById("workspace") !== workspace) return;
        if (Number.isFinite(viewport.height) && viewport.height > 0) {
          workspace.style.setProperty("--workspace-visible-height", viewport.height + "px");
        }
      };
      const schedule = () => this.scheduleFrame(apply);
```

and update its cleanup to call `this.cancelFrame()` instead of `cancelAnimationFrame`.

NOTE: `scheduleFrame` supports ONE pending callback (later calls cancel earlier ones). That is correct for every caller in this plan (viewport resize, scroll affordance, prepend settle each keep their own `tick` flag). Do not use it for the frame queue (Task 5 has its own scheduling because it must never cancel a queued flush).

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-renderer-schedule-frame.js && ./run-all.sh`
Expected: new test PASS; whole suite green.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-schedule-frame.js
git commit -m "renderer: guarded scheduleFrame/cancelFrame helpers (rAF-free environments)"
```

---

### Task 3: Split `applyEvent` out of `handle`; pass objects instead of JSON round-trip

Behavior-preserving refactor that Task 5 builds on. Also removes the per-event `JSON.stringify`→`parse` round trip (jank finding #6).

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js:1047-1068` (`handleData`, `handle`) and the post-switch settle block (~1400-1420)

**Interfaces:**
- Produces (used by Tasks 4, 5):
  - `SerfRenderer.applyEvent(kind, data)` — the event switch; `data` is a parsed object. Stamps `lastFrameAt`.
  - `SerfRenderer.handle(kind, ev)` — thin sync wrapper: buffers while `!descriptionsReady`, parses `ev.data` (string OR object), measures stick, calls `applyEvent`, settles.
  - `SerfRenderer.handleData(kind, data)` — unchanged signature; now passes the object through (no stringify).

- [ ] **Step 1: Characterization check — no new test; the entire existing suite is the test**

Run: `cd cmd/serf-hub/jstest && ./run-all.sh`
Expected: all green BEFORE the change (baseline).

- [ ] **Step 2: Implement**

Replace `handleData`/`handle` (renderer.js:1047-1068):

```js
    handleData(kind, data) {
      this.handle(kind, { data: data || {} });
    },

    handle(kind, ev) {
      // Buffer only during synchronous initialization. Task descriptions are
      // auxiliary metadata and must never gate transcript replay.
      if (!this.descriptionsReady && this.eventBuffer) {
        this.eventBuffer.push([kind, ev]);
        return;
      }
      let data = {};
      try { data = typeof ev.data === "string" ? JSON.parse(ev.data) : (ev.data || {}); } catch (e) {}
      // Measure before the DOM mutation: only stick to the bottom if the reader
      // is already there, so streaming frames don't yank them off history.
      const stick = this.suppressScrollSettle ? false : this.isNearBottom();
      const entriesBefore = this.conversation ? this.conversation.children.length : 0;
      this.applyEvent(kind, data);
      this.settleFrame(stick, entriesBefore);
    },

    // applyEvent is the event switch proper, split from the sync wrapper so the
    // frame-batched live path (flush) can apply N events and settle once.
    applyEvent(kind, data) {
      // Every frame from the daemon (incl. reasoning) resets the liveness clock.
      this.lastFrameAt = Date.now();
      switch (kind) {
        // …move the ENTIRE existing switch body here unchanged…
      }
    },

    // settleFrame runs the post-mutation work that used to live at the bottom of
    // handle(): render coalesced assistant/reasoning updates (Tasks 4, 8), count
    // new entries for the pill, and stick to the bottom when the reader was there.
    settleFrame(stick, entriesBefore) {
      if (this.dirtyAssistantMessages && this.dirtyAssistantMessages.size) {
        for (const m of this.dirtyAssistantMessages) this.renderAssistantMessage(m, m.textBuf);
        this.dirtyAssistantMessages.clear();
      }
      if (this.reasoningPreviewDirty) {
        this.reasoningPreviewDirty = false;
        this.updateReasoningPreview();
      }
      if (!this.conversation) return;
      const added = this.conversation.children.length - entriesBefore;
      if (added > 0) this.noteNewContent(added);
      if (stick && !this.suppressScrollSettle) this.scrollToBottom();
    },
```

Move the post-switch tail of the old `handle` (the `added`/`noteNewContent`/`scrollToBottom` block at ~1400-1420) into `settleFrame` as shown. Keep every comment that documents behavior.

Check the `eventBuffer` drain site (search `eventBuffer` for where buffered events replay): it must keep working with `ev` objects whose `.data` may now be an object — `handle` accepts both, so no change needed there.

- [ ] **Step 3: Run the suite**

Run: `cd cmd/serf-hub/jstest && ./run-all.sh && cd ../../.. && go test ./cmd/serf-hub`
Expected: all green (pure refactor).

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js
git commit -m "renderer: split applyEvent from handle; drop per-event JSON round trip"
```

---

### Task 4: Coalesce assistant markdown re-parse to once per frame

Kills the O(L²)-per-token re-parse for messages ≤4KB (jank finding #1, short-message half). Live path pays off fully once Task 5 lands; sync callers (tests, hydration) still settle per `handle()` call, so behavior is unchanged there.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` (`appendAssistantDelta` :2212-2217, `finalizeAssistantMessage` :2236-2251)
- Test: `cmd/serf-hub/jstest/test-renderer-delta-coalescing.js`

**Interfaces:**
- Consumes: `settleFrame` dirty-set block (Task 3), `renderAssistantMessage(m, text)`.
- Produces: `SerfRenderer.markAssistantDirty(m)` — registers `m` for one render at the next settle.

- [ ] **Step 1: Write the failing test**

```js
// Assistant deltas must coalesce to at most one marked.parse per settle, not
// one parse per delta event.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
let parses = 0;
window.marked = { parse: (t) => { parses++; return t; } };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);
const R = window.SerfRenderer;
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  parses = 0;
  // Direct internal call: five deltas, one settle (mirrors what the batched
  // flush does per frame; handleData settles per call by design).
  R.appendAssistantDelta("a");
  R.appendAssistantDelta("b");
  R.appendAssistantDelta("c");
  R.appendAssistantDelta("d");
  R.appendAssistantDelta("e");
  pass(parses === 0, "no parse before settle (got " + parses + ")");
  R.settleFrame(false, conv.children.length);
  pass(parses === 1, "exactly one parse per settle for N deltas (got " + parses + ")");
  const el = conv.querySelector(".assistant-message");
  pass(el && el.textContent === "abcde", "coalesced content renders");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: assistant deltas coalesce to one parse per settle");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-renderer-delta-coalescing.js`
Expected: FAIL — today each delta parses immediately (parses === 5 before settle).

- [ ] **Step 3: Implement**

```js
    markAssistantDirty(m) {
      if (!this.dirtyAssistantMessages) this.dirtyAssistantMessages = new Set();
      this.dirtyAssistantMessages.add(m);
    },

    appendAssistantDelta(delta) {
      const m = this.activeMessages.get(this.currentMessageId);
      if (!m) return;
      m.textBuf += delta;
      this.markAssistantDirty(m);
    },
```

In `finalizeAssistantMessage`, remove `m` from the dirty set before the final render so a later settle can't re-render a stale buffer:

```js
      this.activeMessages.delete(id);
      this.currentMessageId = null;
      if (this.dirtyAssistantMessages) this.dirtyAssistantMessages.delete(m);
```

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-renderer-delta-coalescing.js && ./run-all.sh`
Expected: new test PASS; suite green (existing tests call `handleData`, which settles synchronously, so they see rendered content).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-delta-coalescing.js
git commit -m "renderer: coalesce assistant markdown parse to once per settle"
```

---

### Task 5: Frame-batched live event delivery with `flush()`

The core jank fix. Live socket notifications queue and apply once per frame; hydration/prepend replay stays synchronous. Queue is drained + generation-guarded on transcript reset; per-notification reconcile replay is preserved.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` — `deliverNotification` (~673-685), the buffered-replay loop (~841-846), `resetTranscriptReplay` (~981-1000)
- Test: `cmd/serf-hub/jstest/test-renderer-frame-batching.js`

**Interfaces:**
- Consumes: `applyEvent`, `settleFrame`, `scheduleFrame`'s rAF guard pattern.
- Produces:
  - `SerfRenderer.flush()` — synchronously drains the frame queue (test hook + visibilitychange handler).
  - `SerfRenderer.scheduleFrameFlush()` — schedules one flush (rAF when visible, 250ms timer when `document.hidden`, 16ms timer when no rAF).
  - `SerfRenderer.cancelFrameFlush()` — cancels a scheduled flush.
  - `this.frameQueue` (array of `{method, params}`), `this.frameGeneration` (number).

- [ ] **Step 1: Write the failing test**

```js
// Live notifications batch: N deliveries before a flush apply together with a
// single settle; flush() drains synchronously for tests; a transcript reset
// invalidates queued events.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
const R = window.SerfRenderer;
R.init(conv);
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.appwireHydrated = true; // live mode: deliveries batch
  pass(typeof R.flush === "function", "flush() exists");
  pass(Array.isArray(R.frameQueue), "frameQueue exists");

  // Simulate the live delivery path with a stubbed projector.
  const realEvents = window.SerfAppwire.eventsFromNotification;
  window.SerfAppwire.eventsFromNotification = (method, params) => {
    if (method === "test/delta") return [["ASSISTANT_TEXT_DELTA", { delta: params.delta }]];
    return realEvents ? realEvents(method, params) : [];
  };
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  // Queue three deltas via the internal enqueue used by deliverNotification.
  R.enqueueLiveNotification("test/delta", { delta: "a" });
  R.enqueueLiveNotification("test/delta", { delta: "b" });
  R.enqueueLiveNotification("test/delta", { delta: "c" });
  pass(conv.querySelector(".assistant-message").textContent === "", "nothing renders before flush");
  R.flush();
  pass(conv.querySelector(".assistant-message").textContent === "abc", "flush applies all queued events");

  // Generation guard: queued events from before a reset never land.
  R.enqueueLiveNotification("test/delta", { delta: "STALE" });
  R.resetTranscriptReplay();
  R.flush();
  pass(!conv.textContent.includes("STALE"), "reset invalidates the queue");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: live notifications batch per frame; reset invalidates the queue");
  process.exit(0);
})();
```

NOTE for the implementer: `enqueueLiveNotification(method, params)` is the extracted queue-push used by `deliverNotification` — name it exactly this; the test drives it directly.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/serf-hub/jstest && node test-renderer-frame-batching.js`
Expected: FAIL — "flush() exists"

- [ ] **Step 3: Implement**

Add to `SerfRenderer`:

```js
    // Frame batching — LIVE socket events only. Replay paths (initial hydration,
    // buffered-notification replay, prependOlderTurns) call handleData directly
    // and never enter this queue.
    enqueueLiveNotification(method, params) {
      if (!Array.isArray(this.frameQueue)) { this.frameQueue = []; this.frameGeneration = 0; }
      this.frameQueue.push({ method, params });
      this.scheduleFrameFlush();
    },

    scheduleFrameFlush() {
      if (this.frameFlushScheduled) return;
      this.frameFlushScheduled = true;
      const run = () => {
        this.frameFlushScheduled = false;
        this.frameFlushRaf = null;
        this.frameFlushTimer = null;
        this.flush();
      };
      const hidden = typeof document !== "undefined" && document.hidden;
      if (!hidden && typeof window !== "undefined" && typeof window.requestAnimationFrame === "function") {
        this.frameFlushRaf = window.requestAnimationFrame(run);
      } else {
        // Hidden tab (rAF never fires there) or no rAF at all (plain jsdom).
        // The interval is best-effort — flush always drains the whole queue.
        this.frameFlushTimer = setTimeout(run, hidden ? 250 : 16);
      }
    },

    cancelFrameFlush() {
      if (this.frameFlushRaf != null && typeof window !== "undefined" && typeof window.cancelAnimationFrame === "function") {
        try { window.cancelAnimationFrame(this.frameFlushRaf); } catch (e) {}
      }
      if (this.frameFlushTimer != null) clearTimeout(this.frameFlushTimer);
      this.frameFlushRaf = null;
      this.frameFlushTimer = null;
      this.frameFlushScheduled = false;
    },

    flush() {
      this.cancelFrameFlush();
      const q = this.frameQueue;
      if (!q || !q.length) return;
      this.frameQueue = [];
      const gen = this.frameGeneration;
      const stick = this.suppressScrollSettle ? false : this.isNearBottom();
      const entriesBefore = this.conversation ? this.conversation.children.length : 0;
      for (const item of q) {
        if (gen !== this.frameGeneration) return; // transcript reset mid-flush
        for (const [kind, data] of window.SerfAppwire.eventsFromNotification(item.method, item.params)) {
          this.applyEvent(kind, data);
        }
        // Per-notification reconcile, in order, after that notification's
        // events applied — the invariant from deliverNotification.
        if (this.pending && this.reconcilePending) {
          this.reconcilePending(this.pending, item.method, item.params);
        }
      }
      this.settleFrame(stick, entriesBefore);
    },
```

Change `deliverNotification` (~673):

```js
      const deliverNotification = (method, params) => {
        if (!this.appwireHydrated || this.replayingBufferedNotifications) {
          for (const [kind, data] of window.SerfAppwire.eventsFromNotification(method, params)) {
            this.handleData(kind, data);
          }
          if (this.pending) reconcilePendingFromNotification(this.pending, method, params);
          return;
        }
        this.enqueueLiveNotification(method, params);
      };
      this.reconcilePending = reconcilePendingFromNotification;
```

In the buffered-replay loop (~841), bracket it so it stays synchronous:

```js
          this.replayingBufferedNotifications = true;
          try {
            while (pendingNotifications.length > 0) { …unchanged… }
          } finally {
            this.replayingBufferedNotifications = false;
          }
```

In `resetTranscriptReplay`, add at the top:

```js
      this.cancelFrameFlush();
      this.frameQueue = [];
      this.frameGeneration = (this.frameGeneration || 0) + 1;
```

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-renderer-frame-batching.js && ./run-all.sh`
Expected: new test PASS. Suite: appwire-notification tests that assert synchronously after delivering a notification WILL FAIL now — that is expected; Task 7 migrates them. If ANY other test fails, the batching leaked into a sync path — fix before continuing. (Do not commit until Task 7 completes; run Tasks 5-7 as one commit sequence.)

- [ ] **Step 5: (No commit yet — continue to Task 6/7, then commit all three)**

---

### Task 6: Flush on visibility change (hidden-tab correctness)

A queued rAF never fires once the tab hides — flush pending events on hide AND on return so no batch strands.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` — renderer init region (near other global listener bindings; follow the cleanup pattern of `bindWorkspaceViewport`)
- Test: `cmd/serf-hub/jstest/test-renderer-visibility-flush.js`

**Interfaces:**
- Consumes: `flush()`, `frameQueue`.

- [ ] **Step 1: Write the failing test**

```js
// A visibilitychange in either direction drains a pending frame queue — a
// scheduled rAF never fires once the tab hides.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
const R = window.SerfRenderer;
R.init(conv);
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.appwireHydrated = true;
  window.SerfAppwire.eventsFromNotification = (m, p) => m === "test/delta" ? [["ASSISTANT_TEXT_DELTA", { delta: p.delta }]] : [];
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.enqueueLiveNotification("test/delta", { delta: "x" });
  // Tab hides before the scheduled flush fires → event must still apply.
  window.document.dispatchEvent(new window.Event("visibilitychange"));
  pass(conv.querySelector(".assistant-message").textContent === "x", "hide drains the queue");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: visibilitychange drains the frame queue");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — nothing renders (queue sits until the rAF/timer fires).

- [ ] **Step 3: Implement**

Where the renderer binds page-level listeners during `init` (near `bindWorkspaceViewport()` / `bindScrollAffordance()` calls), add:

```js
      this.bindVisibilityFlush();
```

and the method:

```js
    // Flush queued live events on ANY visibility transition: a scheduled rAF
    // never fires once the tab hides, and on return we want the freshest state
    // painted immediately.
    bindVisibilityFlush() {
      if (this.visibilityFlushBound || typeof document === "undefined") return;
      this.visibilityFlushBound = true;
      document.addEventListener("visibilitychange", () => this.flush());
    },
```

(Page-level, bound once — no per-session cleanup needed, matching `notifications.js`'s pattern. `flush()` is a no-op on an empty queue.)

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-renderer-visibility-flush.js && ./run-all.sh`
Expected: new test PASS.

- [ ] **Step 5: (No commit yet — Task 7 next)**

---

### Task 7: Migrate appwire-notification jstests to `flush()`

Tests that deliver live notifications and assert synchronously now need one `R.flush()` (or `await` of the scheduled flush) before asserting.

**Files:**
- Modify: whichever `cmd/serf-hub/jstest/test-appwire-*.js` (and any other) files fail after Task 5

**Interfaces:**
- Consumes: `SerfRenderer.flush()`.

- [ ] **Step 1: Identify failures**

Run: `cd cmd/serf-hub/jstest && ./run-all.sh 2>&1 | grep -E '^(FAIL|TIMEOUT)'`
Expected: a list of test files that drive the notification path.

- [ ] **Step 2: Fix each failing test**

In each failing file, find where a notification is delivered (look for the test's stub of `onNotification` invocation, `deliverNotification`, `readThread` resolution, or `eventsFromNotification` being driven) and insert `R.flush()` immediately before the assertions that now observe stale DOM. Example shape:

```js
  // …test delivers a live notification…
  R.flush(); // live deliveries batch per frame; apply them now
  pass(conv.querySelector(".assistant-message").textContent === "…", "…");
```

Preserve every existing assertion — this is a mechanical migration, not a rewrite. If a test delivers notifications DURING hydration replay (before `appwireHydrated`), those deliveries stay synchronous — no `flush()` needed there; only live-mode deliveries need it.

- [ ] **Step 3: Run the full gate**

Run: `cd cmd/serf-hub/jstest && ./run-all.sh && cd ../../.. && make build-hub && go test ./cmd/serf-hub`
Expected: everything green.

- [ ] **Step 4: Commit (Tasks 5+6+7 together)**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/
git commit -m "renderer: frame-batch live socket events with flush-on-hide

Live notifications queue and apply once per frame (single stick measure +
settle); hydration/prepend replay stays synchronous; the queue drains on
visibilitychange in both directions and is generation-guarded on transcript
reset; per-notification pending reconcile replays in order after each
notification's events. jstest appwire suites migrated to the flush() hook."
```

---

### Task 8: Reasoning — append-only body, O(1) preview tail

Kills the O(L²) full-buffer `textContent` replace and the O(L) regex per delta.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` (`appendReasoningDelta` :2328-2338)
- Test: `cmd/serf-hub/jstest/test-renderer-reasoning-append.js`

**Interfaces:**
- Consumes: `settleFrame`'s `reasoningPreviewDirty` hook (Task 3).
- Produces: `SerfRenderer.updateReasoningPreview()` — refreshes the `.pv` teleprompter tail from the last 400 chars of `reasoningBuf`.

- [ ] **Step 1: Write the failing test**

```js
// Reasoning deltas append (no full-buffer rewrite) and the preview tail
// updates at settle from a bounded slice.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);
const R = window.SerfRenderer;
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.handleData("REASONING_START", { itemId: "r1" });
  R.handleData("REASONING_DELTA", { delta: "first ", itemId: "r1" });
  R.handleData("REASONING_DELTA", { delta: "second", itemId: "r1" });
  const body = conv.querySelector(".think .think-body");
  pass(body && body.textContent === "first second", "deltas append to the body");
  pass(body && body.childNodes.length === 2, "append-only: one text node per delta (got " + (body && body.childNodes.length) + ")");
  pass(typeof R.updateReasoningPreview === "function", "updateReasoningPreview exists");
  const pv = conv.querySelector(".think .pv");
  pass(pv && pv.textContent.includes("first second"), "preview tail reflects the buffer");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: reasoning appends text nodes; preview tail is bounded");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — today the body is a single full-buffer text node and `updateReasoningPreview` doesn't exist.

- [ ] **Step 3: Implement**

```js
    appendReasoningDelta(delta) {
      if (!this.reasoningEl) this.beginReasoning();
      this.reasoningBuf += delta || "";
      const body = this.reasoningEl.querySelector(".think-body");
      if (body) body.appendChild(document.createTextNode(delta || ""));
      // The teleprompter tail refreshes once per settle, from a bounded slice.
      this.reasoningPreviewDirty = true;
    },

    // updateReasoningPreview refreshes the one-line teleprompter tail. Bounded
    // to the last 400 chars so a long thought never pays O(buffer) per frame.
    updateReasoningPreview() {
      const el = this.reasoningEl;
      if (!el) return;
      const pv = el.querySelector(".pv");
      if (pv) pv.textContent = clip(String(this.reasoningBuf || "").slice(-400).replace(/\s+/g, " ").trim(), 200);
    },
```

(`clip` is imported from `window.SerfRendererInternal` at the top of renderer.js — same helper the old code used.)

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-renderer-reasoning-append.js && ./run-all.sh`
Expected: PASS + suite green.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-reasoning-append.js
git commit -m "renderer: append-only reasoning body, bounded preview tail"
```

---

### Task 9: Streaming text — frozen head, raw tail past 4KB

Long messages stop re-parsing entirely: the parsed DOM freezes, further deltas stream as plain text in a `.streaming-tail` node, and one full parse happens at finalization. Element keeps `.assistant-message` + `data-turn-id`.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` (`beginAssistantMessage` ~2165-2177, `appendAssistantDelta` from Task 4)
- Modify: `cmd/serf-hub/assets/style.css` (add `.streaming-tail` + caret rules near the `.assistant-message` block)
- Test: `cmd/serf-hub/jstest/test-renderer-streaming-tail.js`

**Interfaces:**
- Consumes: `markAssistantDirty` (Task 4).
- Produces: `m.tailEl` on the active-message record (Task 10's finalize relies on clearing it via full re-parse).

- [ ] **Step 1: Write the failing test**

```js
// Past 4KB the message stops re-parsing: the parsed head freezes, deltas stream
// as plain text in .streaming-tail, and finalization parses everything once.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
let parses = 0;
window.marked = { parse: (t) => { parses++; return "<p>" + t + "</p>"; } };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);
const R = window.SerfRenderer;
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  const big = "x".repeat(4100);
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: big });
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "**tail**" });
  const el = conv.querySelector(".assistant-message");
  pass(!!el, "message element exists");
  pass(el.dataset.turnId === "t1", "data-turn-id preserved in tail mode");
  const tail = el.querySelector(".streaming-tail");
  pass(!!tail, "streaming-tail node exists past 4KB");
  pass(tail && tail.textContent === "**tail**", "tail holds raw un-parsed markdown");
  pass(el.classList.contains("streaming"), "message carries .streaming for the caret");
  const before = parses;
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "more" });
  pass(parses === before, "no parse per delta in tail mode");
  R.handleData("ASSISTANT_TEXT_END", { text: big + "**tail**more" });
  pass(!el.querySelector(".streaming-tail"), "finalization replaces the tail with parsed output");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: long messages stream frozen-head/raw-tail and finalize once");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — no `.streaming-tail`, no `.streaming` class.

- [ ] **Step 3: Implement**

In `beginAssistantMessage`, add `el.classList.add("streaming")` after `el.className = "assistant-message"`.

Replace Task 4's `appendAssistantDelta`:

```js
    appendAssistantDelta(delta) {
      const m = this.activeMessages.get(this.currentMessageId);
      if (!m) return;
      m.textBuf += delta;
      if (!m.tailEl && m.textBuf.length > 4096) {
        // Frozen head, raw tail: stop re-parsing a long message per frame. The
        // DOM parsed so far stays; further deltas stream as plain text and the
        // whole buffer parses once at finalization. Switches on LENGTH only
        // (never fence state) and never flips back.
        const tail = document.createElement("span");
        tail.className = "streaming-tail";
        m.el.appendChild(tail);
        m.tailEl = tail;
        if (this.dirtyAssistantMessages) this.dirtyAssistantMessages.delete(m);
      }
      if (m.tailEl) {
        m.tailEl.appendChild(document.createTextNode(delta));
      } else {
        this.markAssistantDirty(m);
      }
    },
```

In `finalizeAssistantMessage`, the existing full `renderAssistantMessage(m, finalText)` already replaces the tail; add `m.el.classList.remove("streaming")` before it, and set `this.lastFinalizedAssistantEl = m.el` after (Task 10 uses this).

In `style.css`, near the `.assistant-message` rules:

```css
/* Frozen-head/raw-tail streaming (long messages): the tail is plain pre-wrap
   text with the one sanctioned streaming caret; finalization replaces it with
   parsed markdown. */
.assistant-message .streaming-tail {
  display: block;
  white-space: pre-wrap;
  color: var(--text);
}
.assistant-message.streaming .streaming-tail::after {
  content: "▍";
  color: var(--accent);
}
```

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-renderer-streaming-tail.js && ./run-all.sh`
Expected: PASS + green.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-renderer-streaming-tail.js
git commit -m "renderer: frozen-head/raw-tail streaming for long assistant messages"
```

---

### Task 10: Idempotent finalization (interrupt + codex END-after-TURN_COMPLETED)

Finalization runs on `ASSISTANT_TEXT_END`, `TURN_COMPLETED`, or `SESSION_END` — whichever first — and is idempotent: a late `ASSISTANT_TEXT_END` replaces the finalized block in place, never appends a duplicate. Interrupt never emits `ASSISTANT_TEXT_END` (serf source), so without this an interrupted long message stays raw forever; the codex path emits END right after TURN_COMPLETED, which without idempotence double-renders.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` — `finalizeAssistantMessage` (:2236-2251), `TURN_COMPLETED` case (:1104-1106, finalize BEFORE the turn-meta block), `SESSION_END` case (~1374), `beginAssistantMessage` (clear the guard)
- Test: `cmd/serf-hub/jstest/test-renderer-idempotent-finalize.js`

**Interfaces:**
- Consumes: `this.lastFinalizedAssistantEl` (set in Task 9's finalize edit).
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

```js
// TURN_COMPLETED finalizes from textBuf (interrupt: no ASSISTANT_TEXT_END ever
// arrives). A subsequent ASSISTANT_TEXT_END for that message (codex
// turn/completed-with-items path) REPLACES the block in place — no duplicate.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => "<p>" + t + "</p>" };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);
const R = window.SerfRenderer;
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "partial answer" });
  // Interrupt shape: TURN_COMPLETED with NO ASSISTANT_TEXT_END.
  R.handleData("TURN_COMPLETED", { turnId: "t1", turn: { id: "t1", duration_ms: 1200 } });
  let msgs = conv.querySelectorAll(".assistant-message");
  pass(msgs.length === 1, "TURN_COMPLETED finalizes the partial message (got " + msgs.length + ")");
  pass(msgs[0].textContent.includes("partial answer"), "partial content rendered at finalize");
  pass(msgs[0].querySelector(".turn-meta"), "turn-meta badge appended after finalize");
  // Codex shape: the synthesized END arrives AFTER TURN_COMPLETED.
  R.handleData("ASSISTANT_TEXT_END", { text: "partial answer, completed" });
  msgs = conv.querySelectorAll(".assistant-message");
  pass(msgs.length === 1, "late END replaces in place — no duplicate (got " + msgs.length + ")");
  pass(msgs[0].textContent.includes("completed"), "late END content applied");
  pass(msgs[0].querySelector(".turn-meta"), "turn-meta survives the in-place replace");
  pass(msgs[0].dataset.turnId === "t1", "turn id preserved through replace");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: finalization is idempotent across TURN_COMPLETED and a late END");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — today TURN_COMPLETED doesn't finalize, and the late END appends a second block.

- [ ] **Step 3: Implement**

In the `TURN_COMPLETED` case, immediately after `this.finalizeReasoning();`:

```js
          this.finalizeAssistantMessage();
```

(Finalize runs BEFORE the turn-meta block below it, so the badge appends onto the finalized message.)

In the `SESSION_END` case, add at the top:

```js
          this.finalizeAssistantMessage();
```

Replace `finalizeAssistantMessage` with the idempotent version:

```js
    finalizeAssistantMessage(data) {
      const id = this.currentMessageId;
      const m = this.activeMessages.get(id);
      const finalText = (data && data.text) || (m && m.textBuf) || "";
      if (!m) {
        // Idempotence: appwire can emit TURN_COMPLETED (which finalizes) and
        // then a synthesized ASSISTANT_TEXT_END for the same message in one
        // notification (codex turn/completed-with-items). Between turns
        // (activeTurnId cleared) a late END REPLACES the finalized block in
        // place — it must never append a duplicate.
        const last = this.lastFinalizedAssistantEl;
        if (!this.activeTurnId && last && last.isConnected && String(finalText).trim()) {
          const meta = last.querySelector(".turn-meta");
          try { last.innerHTML = window.marked.parse(finalText); }
          catch (e) { last.textContent = finalText; }
          if (meta) last.appendChild(meta); // re-parse destroyed children; the badge rides back
          return;
        }
        this.appendAssistantBlock(finalText);
        return;
      }
      this.activeMessages.delete(id);
      this.currentMessageId = null;
      if (this.dirtyAssistantMessages) this.dirtyAssistantMessages.delete(m);
      if (!String(finalText || "").trim()) {
        if (m.el.parentNode) m.el.parentNode.removeChild(m.el);
        return;
      }
      m.el.classList.remove("streaming");
      this.renderAssistantMessage(m, finalText);
      this.lastFinalizedAssistantEl = m.el;
    },
```

In `beginAssistantMessage`, add `this.lastFinalizedAssistantEl = null;` (a new message supersedes the guard).

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-renderer-idempotent-finalize.js && ./run-all.sh`
Expected: PASS + green. If `test-renderer-thinking.js` or hydration tests break, check the finalize ordering they assumed (TURN_COMPLETED used to leave messages unfinalized).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-idempotent-finalize.js
git commit -m "renderer: idempotent assistant finalization (interrupt + codex late-END)"
```

---

### Task 11: Communicate dedup against the streaming source buffer

With a raw tail, the last message's DOM `textContent` contains literal markdown, so `lastElementIsAssistantText` misfires and a mid-stream `communicate` message appends a duplicate.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` (`lastElementIsAssistantText` :2195-2199)
- Test: `cmd/serf-hub/jstest/test-renderer-communicate-dedup.js`

**Interfaces:**
- Consumes: `activeMessages`, `currentMessageId`, `normalizedAssistantText`.

- [ ] **Step 1: Write the failing test**

```js
// A communicate tool call landing while its agentMessage is still streaming
// (raw tail mode) must be deduped against the SOURCE buffer, not rendered text.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => "<p>" + t + "</p>" };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
window.SerfRenderer.init(conv);
const R = window.SerfRenderer;
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  const text = "x".repeat(4100) + " **bold tail**";
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: text }); // switches to raw-tail mode
  pass(R.lastElementIsAssistantText(text) === true,
    "dedup matches the streaming message by source, raw tail included");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: communicate dedup compares against the source buffer while streaming");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — DOM textContent (rendered head + raw tail) ≠ rendered incoming text.

- [ ] **Step 3: Implement**

```js
    lastElementIsAssistantText(text) {
      const last = this.conversation && this.conversation.lastElementChild;
      if (!last || !last.classList || !last.classList.contains("assistant-message")) return false;
      // While the last message is still streaming, its DOM may hold a raw
      // markdown tail — compare source against source instead.
      const m = this.activeMessages.get(this.currentMessageId);
      if (m && m.el === last) {
        return this.normalizedAssistantText(m.textBuf) === this.normalizedAssistantText(text);
      }
      return this.normalizedAssistantText(last.textContent) === this.renderedAssistantText(text);
    },
```

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-renderer-communicate-dedup.js && ./run-all.sh`
Expected: PASS + green.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-communicate-dedup.js
git commit -m "renderer: dedup communicate against the streaming source buffer"
```

---

### Task 12: Shell/read tool output — append-only streaming, chrome at bodyEnd

Kills the per-delta fold rebuild (jank finding #4). During streaming the output is one `<pre>` updated in place, height-clamped and tail-anchored in CSS; the 5-line fold, binary detection, and error replacement happen once at `bodyEnd`.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer-tools.js` (shell `bodyDelta` :528-532, `bodyEnd` :533-549; `readToolBodyDelta` :225-227, `readToolBodyEnd` :229-231)
- Modify: `cmd/serf-hub/assets/style.css` (streaming clamp rules)
- Test: `cmd/serf-hub/jstest/test-tool-streaming-output.js`

**Interfaces:**
- Consumes: `setExpandableOutput` (unchanged), `clip`.
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

```js
// Streaming shell output updates one <pre> in place (no fold rebuild per
// delta); the fold chrome appears once at bodyEnd.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body></body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
require("./load-renderer").evalRenderer(window);
const { toolRendererFor } = window.SerfRendererInternal || {};
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const shell = (window.SerfRendererInternal.toolRendererFor || window.toolRendererFor)("shell", {});
const host = window.document.createElement("div");
const state = { body: shell.body({ command: "make test" }, host), el: host };

shell.bodyDelta(state, "line1\nline2");
const pre = state.body.pre;
pass(pre && pre.textContent === "line1\nline2", "delta writes the pre directly");
pass(!state.body.wrap.querySelector(".tool-output-more"), "no fold chrome while streaming");
pass(state.body.wrap.classList.contains("streaming"), "wrap carries .streaming for the CSS clamp");
const moreLines = Array.from({ length: 20 }, (_, i) => "l" + i).join("\n");
shell.bodyDelta(state, moreLines);
pass(!state.body.wrap.querySelector(".tool-output-more"), "still no fold after >5 lines stream in");
shell.bodyEnd(state, { tool_state: JSON.stringify({ exit_code: 0 }) }, moreLines);
pass(!state.body.wrap.classList.contains("streaming"), "bodyEnd drops .streaming");
pass(!!state.body.wrap.querySelector(".tool-output-more"), "fold chrome built once at bodyEnd");
pass(state.body.pre.textContent.split("\n").length === 5, "head shows the first 5 lines");

if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
console.log("PASS: shell output streams append-only; fold builds at bodyEnd");
process.exit(0);
```

NOTE for the implementer: check how existing tool-renderer tests (e.g. `test-diff-line-kind.js`) obtain the renderer and the exact shape of `state.body` for shell (`shellTerminalBody`) — mirror that harness. The assertions above assume `state.body = { wrap, pre, footerEl }`; adjust the setup (not the behavior assertions) to the real shape.

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — today `bodyDelta` builds the fold on every delta.

- [ ] **Step 3: Implement**

In `renderer-tools.js`, shell renderer:

```js
    bodyDelta: (state, out) => {
      const b = state.body;
      if (!b || !b.pre) return;
      // Stream in place: one <pre>, tail-anchored, height-clamped by CSS. The
      // fold/binary/error chrome is built once at bodyEnd — never per delta.
      if (!b.streaming) { b.streaming = true; if (b.wrap) b.wrap.classList.add("streaming"); }
      b.pre.style.display = "";
      b.pre.textContent = clip(out, 8000);
      b.pre.scrollTop = b.pre.scrollHeight;
    },
    bodyEnd: (state, data, out) => {
      if (!state.body) return;
      state.body.streaming = false;
      if (state.body.wrap) state.body.wrap.classList.remove("streaming");
      const text = data.error || out || "";
      setExpandableOutput(state.body, clip(text, 8000), { moreClass: "shell-output-more", outputClassName: "shell-output terminal-output" });
      // …rest unchanged (footer, failure expansion)…
    },
```

Read renderer, same pattern:

```js
  function readToolBodyDelta(state, out) {
    const b = state.body;
    if (!b || !b.outputPre) return;
    if (!b.streaming) { b.streaming = true; if (b.wrap) b.wrap.classList.add("streaming"); }
    b.outputPre.style.display = "";
    b.outputPre.textContent = clip(out || "", 8000);
    b.outputPre.scrollTop = b.outputPre.scrollHeight;
  }

  function readToolBodyEnd(state, data, out) {
    if (state.body) {
      state.body.streaming = false;
      if (state.body.wrap) state.body.wrap.classList.remove("streaming");
    }
    setReadOutput(state, clip(data.error || out || "", 8000));
  }
```

In `style.css`:

```css
/* Streaming tool output: one clamped, tail-anchored pane until bodyEnd builds
   the fold chrome. */
.tool-body.streaming .terminal-output,
.tool-body.streaming .read-tool-preview {
  max-height: 16em;
  overflow-y: auto;
}
```

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-tool-streaming-output.js && ./run-all.sh`
Expected: PASS + green. If `test-appwire-tool-output-stream.js` asserts the old per-delta fold, update it to the new contract (stream = plain pre; fold at end).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer-tools.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/
git commit -m "renderer-tools: append-only streaming tool output, fold chrome at bodyEnd"
```

---

### Task 13: Transcript windowing + two-phase prepend settle

`content-visibility: auto` on every direct child of `#conversation`; prepend does a next-frame correction so estimate-based scroll restoration can't drift visibly.

**Files:**
- Modify: `cmd/serf-hub/assets/style.css` (`.conversation` block ~1592)
- Modify: `cmd/serf-hub/assets/renderer.js` (`prependOlderTurns` :4654-4659)
- Test: `cmd/serf-hub/jstest/test-transcript-windowing.js`

**Interfaces:**
- Consumes: `scheduleFrame` (Task 2).

- [ ] **Step 1: Write the failing test**

```js
// Windowing: stylesheet carries content-visibility on conversation children;
// prepend schedules a next-frame scroll correction.
const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

const css = fs.readFileSync(path.resolve(__dirname, "../assets/style.css"), "utf8");
pass(/\.conversation\s*>\s*\*\s*\{[^}]*content-visibility:\s*auto/.test(css),
  "conversation children get content-visibility: auto");
pass(/contain-intrinsic-size:\s*auto\s+\d+px/.test(css),
  "entries carry a remembered-size estimate");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="idle"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
const R = window.SerfRenderer;
R.init(conv);

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  let scheduled = 0;
  const realSchedule = R.scheduleFrame.bind(R);
  R.scheduleFrame = (cb) => { scheduled++; realSchedule(cb); };
  window.SerfAppwire = window.SerfAppwire || {};
  window.SerfAppwire.eventsFromTurns = () => [];
  R.prependOlderTurns([{ id: "old1" }]);
  pass(scheduled === 1, "prepend schedules a next-frame settle correction");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: windowing CSS present; prepend settles in two phases");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — no CSS rule, no scheduled correction.

- [ ] **Step 3: Implement**

In `style.css` (near `.conversation`):

```css
/* Browser-native windowing: off-screen transcript entries skip layout/paint.
   `auto` reuses each entry's last-rendered size, so scroll geometry is exact
   for anything rendered once; only never-rendered deep history uses the
   estimate (prepend corrects it on the next frame). */
.conversation > * {
  content-visibility: auto;
  contain-intrinsic-size: auto 240px;
}
```

In `prependOlderTurns`, replace the tail (:4654-4659):

```js
      const beforeHeight = sc.scrollHeight;
      const beforeTop = sc.scrollTop;
      const frag = document.createDocumentFragment();
      while (staging.firstChild) frag.appendChild(staging.firstChild);
      sc.insertBefore(frag, sc.firstChild);
      sc.scrollTop = beforeTop + (sc.scrollHeight - beforeHeight);
      // Second settle: freshly prepended entries may sit at estimated sizes
      // (content-visibility). After the browser lays out the near-viewport
      // ones, correct the residual drift so the reader's anchor holds.
      const settledHeight = sc.scrollHeight;
      this.scheduleFrame(() => {
        if (!sc.isConnected) return;
        const drift = sc.scrollHeight - settledHeight;
        if (drift) sc.scrollTop += drift;
      });
```

- [ ] **Step 4: Run tests + build**

Run: `cd cmd/serf-hub/jstest && node test-transcript-windowing.js && ./run-all.sh && cd ../../.. && make build-hub`
Expected: PASS + green.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-transcript-windowing.js
git commit -m "renderer: content-visibility windowing + two-phase prepend settle"
```

---

### Task 14: Hydration settle-once

Per-event stick measurement during the hydration replay loop is the O(N²) session-open path (jank finding #2). Suppress it; settle once at the end.

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` — the `readThread` `.then` hydration block (~808-847)
- Test: `cmd/serf-hub/jstest/test-renderer-hydration-settle.js`

**Interfaces:**
- Consumes: `this.suppressScrollSettle` (honored by `handle`/`settleFrame` since Task 3).

- [ ] **Step 1: Write the failing test**

```js
// During hydration replay, per-event stick/scroll work is suppressed; one
// scroll settle runs when hydration completes.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="active"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
const R = window.SerfRenderer;
R.init(conv);
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  pass(R.suppressScrollSettle === false || R.suppressScrollSettle === undefined,
    "not suppressing outside hydration");
  let measures = 0;
  const realIsNearBottom = R.isNearBottom.bind(R);
  R.isNearBottom = () => { measures++; return realIsNearBottom(); };
  R.suppressScrollSettle = true;
  R.handleData("TURN_STARTED", { turnId: "t1" });
  R.handleData("ASSISTANT_TEXT_START", {});
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "hi" });
  pass(measures === 0, "no stick measurement while suppressed (got " + measures + ")");
  R.suppressScrollSettle = false;
  R.handleData("ASSISTANT_TEXT_DELTA", { delta: "!" });
  pass(measures === 1, "measurement resumes after suppression");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: scroll settle is suppressible during replay");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — `suppressScrollSettle` honored since Task 3 in `handle`… verify: Task 3's `handle` checks it, so the flag mechanics may already pass. The REAL failing assertion for this task is the hydration wiring — extend the test: stub `window.SerfAppwire.readThread`/`onNotification`/`refForSession` so `R.connectAppwire` (or the init path that hydrates — check the method name containing `deliverNotification`, ~line 600-860) can be driven, then assert `isNearBottom` was called at most once around a multi-event hydration. Mirror the existing hydration test's appwire stubs (`test-renderer-hydration-order.js` — READ IT FIRST and reuse its stubbing).

- [ ] **Step 3: Implement**

In the `readThread(...).then` block: set the flag before the replay loop, clear it after the buffered-notification replay, and settle once:

```js
          hydratedNotificationKeys = hydrationKeysFromThread(thread);
          const hydrationEvents = Array.from(window.SerfAppwire.eventsFromThread(thread));
          this.suppressScrollSettle = true;
          try {
            for (const [kind, data] of hydrationEvents) {
              this.handleData(kind, data);
            }
            await this.loadOlderTurnsUntilPrimaryDialogue(
              this.eventsContainPrimaryDialogue(hydrationEvents),
              sessionId, conversation, appwireStream,
            );
            if (this.liveStream !== appwireStream || this.conversation !== conversation) return;
            this.surfaceSnapshotEscalations(thread);
            this.appwireHydrated = true;
            this.clearConnectionBanner();
            this.replayingBufferedNotifications = true;
            try {
              while (pendingNotifications.length > 0) { …unchanged… }
            } finally {
              this.replayingBufferedNotifications = false;
            }
          } finally {
            this.suppressScrollSettle = false;
          }
          this.scrollToBottom();
```

Also reset `this.suppressScrollSettle = false` in `resetTranscriptReplay` and in the `.catch` path so a failed hydration can't wedge scrolling.

- [ ] **Step 4: Run tests**

Run: `cd cmd/serf-hub/jstest && node test-renderer-hydration-settle.js && ./run-all.sh`
Expected: PASS + green (especially `test-renderer-hydration-order.js`).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-hydration-settle.js
git commit -m "renderer: suppress per-event scroll settle during hydration replay"
```

---

### Task 15: Throttled scroll handler + cached error anchors

The scroll listener currently does O(transcript) `querySelectorAll` + layout reads per scroll event (jank finding #5).

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js` — `bindScrollAffordance` (:4508-4522), `pickUrgentAnchor` (:4470-4477), `TOOL_CALL_END` case in `applyEvent`, `prependOlderTurns`, `resetTranscriptReplay`
- Test: `cmd/serf-hub/jstest/test-renderer-scroll-throttle.js`

**Interfaces:**
- Consumes: `scheduleFrame` (Task 2).
- Produces:
  - `SerfRenderer.onScrollAffordance()` — the extracted scroll handler body.
  - `SerfRenderer.errorAnchors()` — cached `Array` of `.tool-call[data-attention="error"]`; cache invalidated on `TOOL_CALL_END`, prepend, reset.

- [ ] **Step 1: Write the failing test**

```js
// Scroll handling is rAF-throttled (N events → one handler run) and error
// anchors are cached between invalidations.
const { JSDOM } = require("jsdom");
const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation" data-session-id="01TEST" data-state="idle"></div>
  <form data-input-form data-session-id="01TEST"><textarea class="message-input"></textarea></form>
</body></html>`, { runScripts: "outside-only", pretendToBeVisual: true });
const { window } = dom;
window.marked = { parse: (t) => t };
window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
require("./load-renderer").evalRenderer(window);
const conv = window.document.getElementById("conversation");
const R = window.SerfRenderer;
R.init(conv);
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

(async () => {
  await new Promise((r) => setTimeout(r, 30));
  pass(typeof R.onScrollAffordance === "function", "onScrollAffordance exists");
  pass(typeof R.errorAnchors === "function", "errorAnchors exists");
  // Throttle: 5 scroll events in one tick → 1 handler run.
  let runs = 0;
  const real = R.onScrollAffordance.bind(R);
  R.onScrollAffordance = () => { runs++; real(); };
  for (let i = 0; i < 5; i++) conv.dispatchEvent(new window.Event("scroll"));
  pass(runs <= 1, "scroll handler coalesced to at most one run per frame (got " + runs + ")");
  await new Promise((r) => setTimeout(r, 50));
  pass(runs === 1, "the coalesced run executes on the next frame");
  // Anchor cache: querySelectorAll runs once until invalidated.
  let queries = 0;
  const realQSA = conv.querySelectorAll.bind(conv);
  conv.querySelectorAll = (sel) => { if (sel.includes('data-attention="error"')) queries++; return realQSA(sel); };
  R.errorAnchors();
  R.errorAnchors();
  pass(queries === 1, "error anchors cached between calls (got " + queries + ")");
  R.handleData("TOOL_CALL_END", { callId: "nope" });
  R.errorAnchors();
  pass(queries === 2, "TOOL_CALL_END invalidates the cache");
  if (failures.length) { for (const f of failures) console.log(f); process.exit(1); }
  console.log("PASS: scroll handler throttled; error anchors cached with invalidation");
  process.exit(0);
})();
```

- [ ] **Step 2: Run test to verify it fails**

Expected: FAIL — methods don't exist; handler runs per event.

- [ ] **Step 3: Implement**

Replace the scroll listener in `bindScrollAffordance`:

```js
      if (!el.__serfScrollPillBound) {
        el.__serfScrollPillBound = true;
        el.addEventListener("scroll", () => {
          if (this.scrollAffordanceTick) return;
          this.scrollAffordanceTick = true;
          this.scheduleFrame(() => {
            this.scrollAffordanceTick = false;
            this.onScrollAffordance();
          });
        });
      }
```

Extract the body:

```js
    onScrollAffordance() {
      if (this.isNearTop()) this.maybeLoadOlderTurns();
      if (this.isNearBottom()) this.clearNewContentPill();
      else if (this.newContentCount > 0) this.renderNewContentPill();
      this.renderNeedsYouDock();
    },
```

Anchor cache + use in `pickUrgentAnchor`:

```js
    errorAnchors() {
      if (!this.errorAnchorCache) {
        this.errorAnchorCache = this.conversation
          ? Array.from(this.conversation.querySelectorAll('.tool-call[data-attention="error"]'))
          : [];
      }
      return this.errorAnchorCache;
    },
```

In `pickUrgentAnchor`, replace `const errors = Array.from(sc.querySelectorAll(...))` with `const errors = this.errorAnchors();`.

Invalidate: add `this.errorAnchorCache = null;` in the `TOOL_CALL_END` case of `applyEvent`, at the end of `prependOlderTurns`, and in `resetTranscriptReplay`.

NOTE: `scheduleFrame` supports one pending callback — the scroll tick flag makes that safe here, and Task 13's prepend settle uses its own call. If both race, the later cancels the earlier; the tick flag means the scroll handler re-arms on the next event, and prepend's settle is one-shot — acceptable; do NOT "fix" by removing the tick flag.

- [ ] **Step 4: Run the full gate**

Run: `cd cmd/serf-hub/jstest && node test-renderer-scroll-throttle.js && ./run-all.sh && cd ../../.. && make build-hub && go test ./cmd/serf-hub`
Expected: all green.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-scroll-throttle.js
git commit -m "renderer: rAF-throttled scroll handler + cached error anchors"
```

---

## Final verification (after Task 15)

- [ ] Full gate: `make build-hub && cmd/serf-hub/jstest/run-all.sh && go test ./cmd/serf-hub`
- [ ] Manual smoke with live assets: `SERF_HUB_ASSETS_DIR=$PWD/cmd/serf-hub ./serf-hub` (dev loop per design-system §10), open a streaming session at 1440px and 390px: streaming is smooth, long messages flip to raw tail and finalize formatted, interrupted turns finalize, scroll-up during streaming doesn't jump, session open doesn't stall.
- [ ] Playwright before/after screenshots (390/768/1100/1440/2560 × dark) archived to `/tmp/webui-study/after/` for the visual review with the product owner.

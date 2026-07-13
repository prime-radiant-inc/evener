# Mobile spawn form — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: use `superpowers:subagent-driven-development` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Update the mobile `/new` session creation form to match the approved design spec (Treatment A with an auto-expanding textarea) while keeping the existing desktop chip layout unchanged.

**Architecture:** Add a parallel, mobile-only row UI in the spawn template, show/hide it with CSS, restyle the mobile form via the existing `≤767px` media query, add a small auto-expand behavior to `spawn.js`, and cover the changes with JSDOM/CSS unit tests.

**Tech Stack:** Go templates (`cmd/serf-hub/templates/partials/spawn.html`), CSS (`cmd/serf-hub/assets/style.css`), vanilla JS (`cmd/serf-hub/assets/spawn.js`), JSDOM tests (`cmd/serf-hub/jstest/test-spawn.js`, `cmd/serf-hub/jstest/test-mobile-css.js`).

## Global Constraints

- Reuse the existing design tokens; do not add new color/type/spacing tokens.
- Do not change the desktop chip layout or the existing picker/attachment/submit logic.
- All editable fields on mobile stay at least `16px` to avoid iOS auto-zoom.
- All touch targets on mobile are at least `44px` (prefer `48px` for rows).
- Honor `prefers-reduced-motion`; do not animate textarea height for users who request reduced motion.
- Every task must end with its own tests passing before the next task starts.
- Commit after each task.

---

## Task 1: Restructure `spawn.html` for mobile rows

**Files:**
- Modify: `cmd/serf-hub/templates/partials/spawn.html`

**Interfaces:**
- Consumes: existing template data (`DefaultHarness`, `DefaultModel`, `DefaultWorkingDir`, `DefaultBranch`, `DefaultAccessMode`, etc.).
- Produces: a DOM where `.spawn-chips` is before the prompt heading (desktop), `.spawn-mobile-rows` is after the textarea (mobile), and the advanced summary uses sentence-case sans text.

- [ ] **Step 1: Move the desktop chip block before the prompt heading**

  Cut the existing `<div class="spawn-chips" id="spawn-chips">…</div>` block and paste it immediately after the opening `<form class="spawn-form" data-spawn-form>` and before `<h2 class="spawn-prompt">`. The desktop order must remain: chips → prompt heading → prompt textarea → attach row → advanced.

- [ ] **Step 2: Add the mobile-only row block after the textarea**

  After the `</div>` that closes `<div class="spawn-input-wrap" data-drop-zone>`, insert:

  ```html
  <div class="spawn-mobile-rows">
    <button class="btn spawn-row" type="button" data-chip="harness" data-spawn-row>
      <span class="spawn-row-label">Harness</span>
      <span class="spawn-row-value" data-chip-value-harness>{{.DefaultHarness}}</span>
      <span class="spawn-row-caret">▾</span>
    </button>
    <button class="btn spawn-row" type="button" data-chip="model" data-spawn-row>
      <span class="spawn-row-label">Model</span>
      <span class="spawn-row-value" data-chip-value-model>{{.DefaultModel}}</span>
      <span class="spawn-row-caret">▾</span>
    </button>
    <button class="btn spawn-row" type="button" data-chip="working_dir" data-spawn-row>
      <span class="spawn-row-label">Project</span>
      <span class="spawn-row-value" data-chip-value-working_dir>{{.DefaultWorkingDir}}</span>
      <span class="spawn-row-caret">▾</span>
    </button>
    <button class="btn spawn-row" type="button" data-chip="branch" data-spawn-row>
      <span class="spawn-row-label">Branch</span>
      <span class="spawn-row-value" data-chip-value-branch>{{.DefaultBranch}}</span>
      <span class="spawn-row-caret">▾</span>
    </button>
    <button class="btn spawn-row" type="button" data-chip="reasoning_effort" data-spawn-row>
      <span class="spawn-row-label">Reasoning</span>
      <span class="spawn-row-value" data-chip-value-reasoning_effort>(default)</span>
      <span class="spawn-row-caret">▾</span>
    </button>
    <button class="btn spawn-row" type="button" data-chip="access_mode" data-spawn-row>
      <span class="spawn-row-label">Mode</span>
      <span class="spawn-row-value" data-chip-value-access_mode>{{.DefaultAccessMode}}</span>
      <span class="spawn-row-caret">▾</span>
    </button>
  </div>
  ```

  Do not remove or duplicate the hidden inputs. The mobile rows read from the same hidden inputs and `data-chip-value-*` spans as the desktop chips.

- [ ] **Step 3: Replace the retired `<details>` advanced toggle with a plain button**

  Change:
  ```html
  <details class="spawn-advanced">
    <summary>advanced</summary>
    ...
  </details>
  ```
  to:
  ```html
  <div class="spawn-advanced">
    <button type="button" class="btn spawn-advanced-summary" data-spawn-advanced-toggle aria-expanded="false" aria-controls="spawn-advanced-body">Advanced options</button>
    ...
  </div>
  ```

- [ ] **Step 4: Verify the template still renders**

  Run the existing template assertions in `cmd/serf-hub/jstest/test-spawn.js` by checking the file still passes its static string checks.

  ```bash
  cd cmd/serf-hub/jstest
  node test-spawn.js
  ```

  Expected: `PASS: spawn navigation and harness-aware model defaults`

- [ ] **Step 5: Commit**

  ```bash
  git add cmd/serf-hub/templates/partials/spawn.html
  git commit -m "spawn(mobile): add mobile row markup"
  ```

---

## Task 2: Add mobile row styles in `style.css`

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`

**Interfaces:**
- Consumes: the new classes from Task 1 (`.spawn-mobile-rows`, `.spawn-row`, `.spawn-row-label`, `.spawn-row-value`, `.spawn-row-caret`, `.spawn-advanced-summary`).
- Produces: default rule that hides mobile rows on desktop, mobile rules that hide desktop chips and show/styled rows, and an updated advanced summary style.

- [ ] **Step 1: Hide mobile rows by default**

  In the spawn surface section (around line 3370), add after the existing `.spawn-recent-row` rule:

  ```css
  /* Mobile-only config rows; hidden on desktop where the chip rail is used. */
  .spawn-mobile-rows { display: none; }
  ```

- [ ] **Step 2: Hide desktop chips and reveal mobile rows on phone**

  In the existing `@media (max-width: 767px)` block that starts with the comment `/* ── Session-creation (spawn) form on phone …`, replace the current `.spawn-chips` and `.btn-chip` mobile rules with:

  ```css
  /* Desktop chips are replaced by the mobile settings-row block. */
  .spawn-chips { display: none; }
  .spawn-mobile-rows {
    display: flex;
    flex-direction: column;
    gap: 0;
    margin-top: var(--space-2);
  }
  ```

  Keep the existing `.spawn-pane` and `.spawn-prompt` rules. Keep the existing `.spawn-attach-row` rule or update it in Step 3.

- [ ] **Step 3: Style the mobile row rows**

  In the same mobile media block, add:

  ```css
  .spawn-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 48px;
    padding: var(--space-2) 0;
    border: none;
    border-bottom: 1px solid var(--rule);
    border-radius: 0;
    background: transparent;
    color: var(--text);
    font-family: var(--font-sans);
    font-size: var(--text-base);
    font-weight: 400;
    text-align: left;
    cursor: pointer;
  }
  .spawn-row:first-child { border-top: 1px solid var(--rule); }
  .spawn-row:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .spawn-row-label {
    font-weight: 500;
    color: var(--text);
  }
  .spawn-row-value {
    flex: 1;
    margin-left: var(--space-3);
    text-align: right;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text-muted);
  }
  .spawn-row-caret {
    flex: none;
    margin-left: var(--space-2);
    color: var(--text-dim);
    font-size: 10px;
  }
  .spawn-row:active:not([disabled]):not(:disabled) { background: var(--surface-secondary); }
  ```

- [ ] **Step 4: Adjust the textarea for mobile**

  In the same mobile media block, update or add:

  ```css
  .spawn-input {
    min-height: 96px;
    font-size: 16px;
    resize: none;
    padding: var(--space-4);
  }
  ```

  Ensure this rule is inside the mobile block so the desktop `min-height: 160px` is overridden.

- [ ] **Step 5: Update the advanced toggle style**

  Replace the existing `.spawn-advanced summary` rule (which uses uppercase/letter-spacing/mono) with a plain-button style that hides the body by default:

  ```css
  .spawn-advanced-summary {
    cursor: pointer;
    padding: var(--space-2) 0;
    font-family: var(--font-sans);
    font-size: var(--text-base);
    color: var(--text-muted);
    background: transparent;
    border: none;
    text-align: left;
  }
  .spawn-advanced-summary:hover { background: transparent; }
  .spawn-advanced.is-open .spawn-advanced-summary { color: var(--text); }
  .spawn-advanced:not(.is-open) .spawn-advanced-body { display: none; }
  ```

  Remove any `text-transform: uppercase`, `letter-spacing`, or `font-family: var(--font-mono)` from this rule.

- [ ] **Step 6: Run the mobile CSS test**

  ```bash
  cd cmd/serf-hub/jstest
  node test-mobile-css.js
  ```

  Expected: `PASS: mobile search palette CSS contract + layout guards`

- [ ] **Step 7: Commit**

  ```bash
  git add cmd/serf-hub/assets/style.css
  git commit -m "spawn(mobile): style mobile rows, textarea, and advanced summary"
  ```

---

## Task 3: Auto-expanding textarea in `spawn.js`

**Files:**
- Modify: `cmd/serf-hub/assets/spawn.js`

**Interfaces:**
- Consumes: the existing `init()` function and the `textarea[name=prompt]` element.
- Produces: an `input` listener that grows the textarea with content, capped by the CSS `max-height`.

- [ ] **Step 1: Add the auto-expand helper**

  Near the top of the IIFE, after `spawnEncodeAttachmentData`, add:

  ```javascript
  function autoExpandTextarea(ta) {
    if (!ta) return;
    ta.style.height = "auto";
    const nextHeight = ta.scrollHeight;
    ta.style.height = nextHeight + "px";
  }
  ```

  The CSS `max-height` on `.spawn-input` caps the rendered height; the JS simply sets the measured scroll height.

- [ ] **Step 2: Wire the listener in `init()`**

  In `init()`, after the existing `keydown` listener for the prompt textarea (around line 1153), add:

  ```javascript
    const promptTa = form.querySelector("textarea[name=prompt]");
    if (promptTa) {
      promptTa.addEventListener("input", () => autoExpandTextarea(promptTa));
      autoExpandTextarea(promptTa);
    }
  ```

  Remove the duplicate `form.querySelector("textarea[name=prompt]")` calls already present in the file; reuse the `promptTa` variable.

- [ ] **Step 3: Verify the JS still loads**

  ```bash
  cd cmd/serf-hub/jstest
  node test-spawn.js
  ```

  Expected: `PASS: spawn navigation and harness-aware model defaults`

- [ ] **Step 4: Commit**

  ```bash
  git add cmd/serf-hub/assets/spawn.js
  git commit -m "spawn(mobile): auto-expand prompt textarea"
  ```

---

## Task 4: Add tests

**Files:**
- Modify: `cmd/serf-hub/jstest/test-spawn.js`
- Modify: `cmd/serf-hub/jstest/test-mobile-css.js`

**Interfaces:**
- Consumes: the updated template, CSS, and JS from Tasks 1–3.
- Produces: passing assertions that lock in the new mobile structure, auto-expand behavior, and mobile CSS rules.

- [ ] **Step 1: Add template structure checks in `test-spawn.js`**

  After the existing static assertions near the top of the file (around line 30), add:

  ```javascript
  const spawnMobileRowsIndex = spawnTemplateSrc.indexOf('class="spawn-mobile-rows"');
  const spawnMobileRowIndex = spawnTemplateSrc.indexOf('data-spawn-row');
  const spawnPromptIndex = spawnTemplateSrc.indexOf('class="spawn-prompt"');
  const spawnInputIndex = spawnTemplateSrc.indexOf('class="spawn-input"');
  assert(spawnMobileRowsIndex !== -1, "spawn template should include mobile row block");
  assert(spawnMobileRowIndex !== -1, "spawn template should include data-spawn-row buttons");
  assert(spawnPromptIndex < spawnInputIndex, "prompt heading should appear before textarea");
  assert(spawnInputIndex < spawnMobileRowsIndex, "mobile rows should appear after textarea");
assert(spawnTemplateSrc.indexOf('spawn-advanced-summary') !== -1, "spawn template should use sentence-case advanced summary");
assert(!spawnTemplateSrc.includes("<summary>advanced</summary>"), "spawn template should not use the old lowercase mono summary");
assert(!spawnTemplateSrc.includes('<details class="spawn-advanced">'), "spawn template should not use details element for advanced options");
assert(spawnTemplateSrc.indexOf('data-spawn-advanced-toggle') !== -1, "spawn template should include an advanced toggle button");
  ```

- [ ] **Step 2: Add auto-expand test in `test-spawn.js`**

  After the recent-prompt click assertion (around line 668), add:

  ```javascript
  const expandDom = new JSDOM(`<!DOCTYPE html><html><body>
    <form data-spawn-form>
      <textarea class="spawn-input" name="prompt" style="min-height:96px;max-height:320px;"></textarea>
      <button class="btn btn-primary spawn-btn" type="submit">spawn</button>
      <input type="hidden" name="harness" value="serf">
      <input type="hidden" name="model" value="openai/gpt-5.5">
      <input type="hidden" name="working_dir" value="/tmp/expand">
      <input type="hidden" name="branch" value="">
      <input type="hidden" name="access_mode" value="full">
      <input type="hidden" name="agent" value="default">
      <input type="hidden" name="reasoning_effort" value="">
    </form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/new",
  });
  expandDom.window.matchMedia = () => ({ matches: true, addListener: () => {}, removeListener: () => {} });
  expandDom.window.eval(dirPickerSrc);
  expandDom.window.eval(spawnSrc);
  const expandTa = expandDom.window.document.querySelector('textarea[name="prompt"]');
  const startHeight = expandTa.offsetHeight;
  expandTa.value = "Line 1\nLine 2\nLine 3\nLine 4\nLine 5";
  expandTa.dispatchEvent(new expandDom.window.Event("input", { bubbles: true }));
  assert(expandTa.offsetHeight > startHeight, "textarea should grow after input event");
  ```

- [ ] **Step 3: Add CSS assertions in `test-mobile-css.js`**

  After the existing `inputZoomRule` assertion (around line 163), add:

  ```javascript
  pass(mobile.indexOf(".spawn-mobile-rows") !== -1, "mobile stylesheet must include .spawn-mobile-rows");
  pass(/\.spawn-mobile-rows\s*\{[^}]*display:\s*flex/s.test(mobile), "mobile must show spawn-mobile-rows as flex");
  pass(/\.spawn-chips\s*\{[^}]*display:\s*none/s.test(mobile), "mobile must hide desktop .spawn-chips");
  pass(/\.spawn-row\s*\{[^}]*min-height:\s*48px/s.test(mobile), "mobile spawn rows must be 48px tall");
  pass(/\.spawn-row\s*\{[^}]*font-family:\s*var\(--font-sans\)/s.test(mobile), "mobile spawn rows must use sans font");
  pass(/\.spawn-input\s*\{[^}]*min-height:\s*96px/s.test(mobile), "mobile spawn textarea must have a 96px min-height");
  pass(/\.spawn-input\s*\{[^}]*resize:\s*none/s.test(mobile), "mobile spawn textarea must hide the resize handle");
pass(/\.spawn-advanced-summary\s*\{[^}]*background:\s*transparent/s.test(mobile), "mobile advanced summary must have transparent background");
pass(!/\.spawn-advanced-summary\s*\{[^}]*text-transform:\s*uppercase/s.test(mobile), "mobile advanced summary must not be uppercase");
pass(!/\.spawn-advanced-summary\s*\{[^}]*font-family:\s*var\(--font-mono\)/s.test(mobile), "mobile advanced summary must not use mono font");
pass(/\.spawn-advanced:not\(\.is-open\)\s+\.spawn-advanced-body\s*\{[^}]*display:\s*none/s.test(mobile), "mobile advanced body must be hidden when not open");
  ```

- [ ] **Step 4: Run the test files**

  ```bash
  cd cmd/serf-hub/jstest
  node test-spawn.js
  node test-mobile-css.js
  ```

  Expected: both print `PASS`.

- [ ] **Step 5: Commit**

  ```bash
  git add cmd/serf-hub/jstest/test-spawn.js
  git add cmd/serf-hub/jstest/test-mobile-css.js
  git commit -m "test(spawn): cover mobile rows, structure, and textarea auto-expand"
  ```

---

## Task 5: Final verification

**Files:** None (verification only).

- [ ] **Step 1: Run the Go hub tests**

  ```bash
  go test -short ./cmd/serf-hub/...
  ```

  Expected: all packages pass.

- [ ] **Step 2: Run the full JS test suite**

  ```bash
  cd cmd/serf-hub/jstest
  ./run-all.sh
  ```

  Expected: `jstest: all tests passed`

- [ ] **Step 3: Commit if clean**

  If both test suites pass, the branch is ready for the final review phase.

  ```bash
  git status
  ```

  Expected: no untracked files related to this work.

# Ask UI Iteration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refine the existing docked `ask_user` response UI so its alternatives are native, accessible choices, its draft fields stay obvious and reachable, and its action footer remains visible without changing answer composition or settlement semantics.

**Architecture:** Keep `pendingAsk` and the existing renderer lifecycle as the only state owner. Render the question list in the dock's scrollable region, render one shared footer as a sibling owned by the input form, and rebuild controls from `pendingAsk` while restoring focus by question key and control data attribute. CSS makes the ask-mode input form a constrained flex column so only questions scroll.

**Tech Stack:** Browser JavaScript, CSS, Node.js/JSDOM contract tests, Go Hub tests.

## Global Constraints

- `docs/superpowers/specs/2026-07-09-mobile-safearea-ask-dock-design.md` is authoritative; the supplied uncommitted draft is reference material only.
- Preserve option, multi-select, free-text, note, decide, fallback, **skip**, optimistic send, conflict recovery, cross-client settlement, and transcript history semantics.
- Preserve the existing ability to click an active free-text, decide, fallback, or skip choice again to clear that resolution.
- Preserve the exact existing `[answers]` payload format and the existing `SerfAppwire.startTurn` send path.
- Keep `.workspace-input` as the only active response surface and keep the transcript anchor noninteractive.
- Hidden composer controls must remain hidden and inert during ask mode.
- Default tests must be deterministic and must not use provider credentials, network access, quota, current model behavior, or ambient developer state.
- Do not add `package.json`, `package-lock.json`, or a checked-in JSDOM dependency; use the repository's existing `NODE_PATH=/tmp/serf-jstest-jsdom/node_modules` convention.

---

### Task 1: Accessible Ask Controls and Stable Form Lifecycle

**Files:**
- Modify: `cmd/serf-hub/jstest/test-ask-card.js`
- Modify: `cmd/serf-hub/jstest/test-ask-compose.js`
- Modify: `cmd/serf-hub/jstest/test-ask-submit.js`
- Modify: `cmd/serf-hub/assets/renderer.js`

**Interfaces:**
- Consumes: `pendingAsk.items`, `setQuestionResolution(item, resolution, options)`, `composeAskAnswers(items)`, and `SerfAppwire.startTurn(ref, text)`.
- Produces: one `[data-ask-response-dock]` question scroller and one sibling `[data-ask-footer]`; alternative choices identified by `[data-option-kind="free"]` and `[data-option-kind="decide"]`; the existing `[data-ask-skip-btn]` and `[data-ask-fallback-btn]` behavior remains available.

- [ ] **Step 1: Write failing renderer contract tests**

  Update the JSDOM harness forms to carry `class="workspace-input"`, then assert all of these contracts in `test-ask-card.js`:

  ```js
  const form = window.document.querySelector("[data-input-form]");
  const responseDock = form.querySelector("[data-ask-response-dock]");
  const footer = form.querySelector("[data-ask-footer]");
  pass(footer && footer.parentElement === form,
    "the shared footer is a form-owned sibling of the scrolling question dock");
  pass(!responseDock.contains(footer),
    "the shared footer does not scroll with the question list");

  const options = Array.from(responseDock.querySelectorAll("[data-ask-option]"));
  pass(!!responseDock.querySelector('[data-ask-option][data-option-kind="free"]'),
    "Something else is a native option choice");
  pass(!!responseDock.querySelector('[data-ask-option][data-option-kind="decide"]'),
    "let serf decide is a native option choice");
  pass(!!responseDock.querySelector("[data-ask-skip-btn]"),
    "the spec-required skip resolution remains available");
  pass(!!responseDock.querySelector("[data-ask-note-field]") &&
       !responseDock.querySelector("[data-ask-note-field]").hidden,
    "the optional note field is visible without a disclosure toggle");
  pass(!responseDock.querySelector("[data-ask-note-toggle]"),
    "the obsolete note disclosure is absent");
  pass(responseDock.querySelector("[data-ask-options]").getAttribute("role") === "radiogroup",
    "single-select answer choices expose radiogroup semantics");
  pass(footer.querySelector("[data-ask-answered-count]").getAttribute("aria-live") === "polite",
    "answer progress is announced politely");
  ```

  Add focus-rebuild cases for the free radio, decide radio, visible note field, skip button, fallback button, and form-owned send button. Assert that clicking a checked free/decide radio again clears it, matching the current toggle behavior. Add a submit-in-flight assertion in `test-ask-submit.js` that the shared send button becomes disabled before the `startTurn` promise settles, remains disabled if a second acknowledged question rebuilds the footer, and is enabled again after a non-settling error.

- [ ] **Step 2: Run the focused tests and verify RED**

  Run:

  ```sh
  NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-ask-card.js
  NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-ask-submit.js
  ```

  Expected: FAIL because free/decide are buttons, the note field is disclosed by `+`, the footer is inside the dock, and the send button is not disabled during the request.

- [ ] **Step 3: Implement native alternative controls and a form-owned footer**

  In `renderer.js`, give the footer stable identity and live progress semantics:

  ```js
  footer.setAttribute("data-ask-footer", "");
  count.setAttribute("aria-live", "polite");
  count.setAttribute("aria-atomic", "true");
  ```

  Change `renderPendingAskDock()` to remove a stale footer, rebuild only questions inside the dock, append a fresh footer to the form, and restore footer focus against the form rather than the dock. Change `clearPendingAskDock()` to remove both the dock and footer.

  Inside `buildAskQuestionEl`, create a local option-label builder that:

  ```js
  function makeOptionLabel(opt, kind, visibleLabel) {
    // Regular options use checkbox for multi-select and radio otherwise.
    // Alternative free/decide choices always use radio and share groupName.
    // Set data-option-label for regular options and data-option-kind for alternatives.
    // On change, route every mutation through setQuestionResolution.
  }
  ```

  The implementation must render regular options first, then `Something else…` (`kind="free"`) and `let serf decide` (`kind="decide"`) in the same `[data-ask-options]` container. Set its role to `group` for multi-select and `radiogroup` otherwise, with `aria-labelledby` referencing both the question header and text. Activating free or decide reveals and focuses its associated text field. A click or keyboard activation on an already selected free/decide choice must clear it instead of immediately reselecting it. Preserve the existing fallback and skip buttons, including toggle-off behavior; set `aria-pressed` consistently on both.

  Replace the note disclosure button with one always-visible input whose `aria-labelledby` includes the question header, question text, and a visually hidden `note` label. Notes continue to mutate only `item.note` and must survive dock rebuilds.

  Update `setQuestionResolution` so regular options, free/decide radios and inputs, fallback, and skip all rehydrate from `item.resolution`. `buildAskFooterEl()` must initialize the send button with `send.disabled = !!(this.pendingAsk && this.pendingAsk.sending)` so a rebuild cannot expose an enabled duplicate action. Update `updateAskFooter()` to query `[data-ask-footer]`. In `sendAskAnswers()`, set `pa.sending = true`, synchronize the current footer button, and in `finally` query the currently connected `[data-ask-send-btn]` rather than mutating a possibly detached button.

- [ ] **Step 4: Preserve exact composition and settlement behavior in tests**

  Update `test-ask-compose.js` helpers to select alternative radios by `data-option-kind`. Retain explicit golden assertions for option, free, decide, fallback, and skip output, including notes. Continue to assert that successful settlement restores the composer, cross-client input safely removes the dock/footer, and conflict recovery puts the byte-exact composed reply into the normal composer without retry.

- [ ] **Step 5: Run focused tests and verify GREEN**

  Run:

  ```sh
  NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-ask-card.js
  NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-ask-compose.js
  NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-ask-submit.js
  ```

  Expected: all three scripts print `PASS` and exit 0.

- [ ] **Step 6: Commit the renderer task**

  ```sh
  git add cmd/serf-hub/assets/renderer.js \
    cmd/serf-hub/jstest/test-ask-card.js \
    cmd/serf-hub/jstest/test-ask-compose.js \
    cmd/serf-hub/jstest/test-ask-submit.js
  git commit -m "feat(hub): refine ask response controls"
  ```

### Task 2: Constrained Ask-Dock Layout and Mobile Footer

**Files:**
- Modify: `cmd/serf-hub/jstest/test-mobile-css.js`
- Modify: `cmd/serf-hub/assets/style.css`

**Interfaces:**
- Consumes: Task 1's form-level `data-response-mode="ask"`, scrolling `[data-ask-response-dock]`, sibling `[data-ask-footer]`, and option/fallback/skip hooks.
- Produces: a bounded flex-column ask form where the question list scrolls, the shared footer does not, and mobile footer padding includes left/right safe-area insets.

- [ ] **Step 1: Write failing CSS contract tests**

  Add assertions to `test-mobile-css.js` for these exact contracts:

  ```js
  pass(/\.workspace-input\[data-response-mode="ask"\]\s*\{[^}]*display:\s*flex[^}]*flex-direction:\s*column[^}]*max-height:\s*min\(56dvh,\s*560px\)/s.test(css),
    "ask mode is a bounded flex column");
  pass(/\.ask-response-dock\s*\{[^}]*flex:\s*1\s+1\s+auto[^}]*min-height:\s*0[^}]*overflow-y:\s*auto/s.test(css),
    "only the question dock is the bounded scroll region");
  pass(/\.workspace-input\s*>\s*\[data-composer-surface\]\[hidden\]\s*\{[^}]*display:\s*none/s.test(css),
    "the hidden composer surface cannot occupy ask-mode layout space");
  pass(/@media\s*\([^)]*max-width:[^)]*\)[\s\S]*?\.ask-footer\s*\{[^}]*safe-area-inset-left[^}]*safe-area-inset-right/s.test(css),
    "the mobile ask footer clears horizontal safe areas");
  ```

- [ ] **Step 2: Run the CSS contract and verify RED**

  Run:

  ```sh
  NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-mobile-css.js
  ```

  Expected: FAIL because ask mode is not yet a constrained form flex column and the footer is still styled as a sticky child of the scroller.

- [ ] **Step 3: Implement the constrained ask layout**

  In `style.css`:

  - Make `.workspace-input[data-response-mode="ask"]` a flex column bounded by `max-height: min(56dvh, 560px)` so neither desktop nor phone questions can squeeze the transcript to zero.
  - Give `.ask-response-dock` `flex: 1 1 auto`, `min-height: 0`, and `overflow-y: auto`; move the existing viewport-derived height bound to the parent form and remove the dock's bottom padding.
  - Remove sticky positioning from `.workspace-input[data-response-mode="ask"] .ask-footer` because the footer is now outside the scroller.
  - Style `.ask-footer` as `flex: 0 0 auto`, a stable full-width action row with its own background, top rule, and padding.
  - Add `.workspace-input > [data-composer-surface][hidden] { display: none; }`.
  - In the phone media query, add footer padding with `calc(env(safe-area-inset-left/right) + var(--space-4))` while retaining the workspace input's bottom safe-area padding.
  - Style option, fallback, and skip controls with stable `min-height: var(--tap-min)` and `min-width: var(--tap-min)`; keep visible focus and selected states. Do not remove skip styling.

- [ ] **Step 4: Run focused layout and renderer tests and verify GREEN**

  Run:

  ```sh
  NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-mobile-css.js
  NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-renderer-viewport-dock.js
  NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 90 node cmd/serf-hub/jstest/test-ask-card.js
  ```

  Expected: all scripts print `PASS` and exit 0.

- [ ] **Step 5: Commit the layout task**

  ```sh
  git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-mobile-css.js
  git commit -m "feat(hub): keep ask actions above mobile viewport"
  ```

### Task 3: Integrated Verification

**Files:**
- Verify only; no planned production changes.

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces: deterministic evidence that the complete Hub UI remains compatible.

- [ ] **Step 1: Run the full deterministic Hub JSDOM suite**

  ```sh
  cd cmd/serf-hub/jstest
  NODE_PATH=/tmp/serf-jstest-jsdom/node_modules timeout 900 ./run-all.sh
  ```

  Expected: every script passes with no failures.

- [ ] **Step 2: Run Hub Go tests**

  ```sh
  cd "$(git rev-parse --show-toplevel)"
  go test ./cmd/serf-hub -count=1
  ```

  Expected: `ok` for `cmd/serf-hub` and its tested subpackages.

- [ ] **Step 3: Run repository hygiene checks**

  ```sh
  git diff --check main...HEAD
  git status --short
  ```

  Expected: no whitespace errors; status contains no generated dependency metadata or unrelated files.

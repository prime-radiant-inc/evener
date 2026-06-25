# Hover-Only Turn Timing Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hide transcript time/runtime metadata by default and reveal it only when the relevant task/tool row is hovered or keyboard-focused.

**Architecture:** Keep the existing renderer output and timing-formatting helpers unchanged. Implement the behavior as CSS-only visibility rules in `cmd/serf-hub/assets/style.css`, verified by deterministic static CSS contract tests in `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`.

**Tech Stack:** Serf hub static assets, CSS, Node-based CSS contract tests, Go package tests via `go test ./cmd/serf-hub`.

## Global Constraints

- Keep timing metadata in the rendered DOM; do not remove it or move it into a tooltip-only path.
- Hide timing metadata visually by default for task/tool turn rows so the transcript is quieter.
- Reveal timing metadata when the user hovers the relevant row/turn.
- Reveal timing metadata on `:focus-within` so keyboard users can access the same metadata when focusing an interactive row/control.
- Prefer CSS-only behavior: opacity/visibility transition on the existing metadata element, scoped to existing selectors such as `.tool-call .tool-meta` and any task-row metadata selectors discovered during implementation.
- Avoid JavaScript state for this; no timing logic should change.
- The metadata remains in the DOM so assistive technology can still access it.
- Do not add unnecessary tab stops just for this hover affordance unless testing shows keyboard access is otherwise impossible for interactive metadata.
- Do not change timing data calculation or formatting.
- Do not add new runtime metadata.
- Do not redesign transcript rows beyond timing metadata visibility.
- Default tests must be deterministic and must not depend on provider credentials, network access, quota, current model behavior, or ambient developer machine state.

---

## File Structure

- Modify `cmd/serf-hub/assets/style.css`: change `.tool-call .tool-meta` so timing metadata is hidden by default and revealed on `.tool-call:hover` and `.tool-call:focus-within`.
- Modify `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`: add static CSS contract assertions for default-hidden and hover/focus reveal behavior.

---

### Task 1: Hover/focus reveal for tool timing metadata

**Files:**
- Modify: `cmd/serf-hub/assets/style.css`
- Test: `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`

**Interfaces:**
- Consumes: existing `.tool-call .tool-meta` selector and `.tool-call` row structure.
- Produces: CSS contract where `.tool-call .tool-meta` is hidden by default, and `.tool-call:hover .tool-meta` plus `.tool-call:focus-within .tool-meta` reveal it.

- [ ] **Step 1: Write the failing CSS contract test**

In `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`, add these assertions before the final `if (failures.length > 0)` block:

```js
// Transcript tool timing metadata should stay out of the scan path until the
// user shows row-level intent with hover or keyboard focus.
pass(
  ruleContains(".tool-call .tool-meta", /opacity:\s*0\b/) &&
    ruleContains(".tool-call .tool-meta", /visibility:\s*hidden\b/),
  "tool timing metadata should be visually hidden by default"
);
pass(
  ruleContains(".tool-call:hover .tool-meta", /opacity:\s*1\b/) &&
    ruleContains(".tool-call:hover .tool-meta", /visibility:\s*visible\b/),
  "tool timing metadata should reveal on row hover"
);
pass(
  ruleContains(".tool-call:focus-within .tool-meta", /opacity:\s*1\b/) &&
    ruleContains(".tool-call:focus-within .tool-meta", /visibility:\s*visible\b/),
  "tool timing metadata should reveal on keyboard focus within the row"
);
```

- [ ] **Step 2: Run the CSS contract test to verify it fails**

Run:

```bash
node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Expected: FAIL with at least `FAIL: tool timing metadata should be visually hidden by default`.

- [ ] **Step 3: Implement the CSS-only reveal behavior**

In `cmd/serf-hub/assets/style.css`, replace the existing single-line `.tool-call .tool-meta` rule:

```css
.tool-call .tool-meta { margin-left: auto; color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); white-space: nowrap; }
```

with these rules:

```css
.tool-call .tool-meta {
  margin-left: auto;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  white-space: nowrap;
  opacity: 0;
  visibility: hidden;
  transition: opacity var(--motion-fast), visibility var(--motion-fast);
}
.tool-call:hover .tool-meta,
.tool-call:focus-within .tool-meta {
  opacity: 1;
  visibility: visible;
}
```

- [ ] **Step 4: Run the CSS contract test to verify it passes**

Run:

```bash
node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Expected: PASS with `PASS: pane compact and full-border sidebar resize CSS contracts`.

- [ ] **Step 5: Run the hub package tests**

Run:

```bash
go test ./cmd/serf-hub -count=1
```

Expected: PASS.

- [ ] **Step 6: Review diff for scope**

Run:

```bash
git diff -- cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Expected: only the CSS hover/focus reveal rules and CSS contract assertions changed.

- [ ] **Step 7: Commit implementation**

Run:

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
git commit -m "fix(web): reveal tool timing on hover"
```

Expected: commit succeeds and includes only those two files.

---

## Self-Review

- Spec coverage: Task 1 keeps timing metadata in the DOM, hides it visually by default, reveals it on hover and focus-within, uses CSS-only behavior, and leaves timing calculation/formatting unchanged.
- Placeholder scan: no placeholder tasks or unspecified implementation steps remain.
- Type/name consistency: the selector names `.tool-call`, `.tool-meta`, `.tool-call:hover .tool-meta`, and `.tool-call:focus-within .tool-meta` are consistent across the test and implementation steps.

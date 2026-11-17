# Activity Sidebar Automatic Paging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Overview activity lists page automatically, show truthful initial totals and static placeholders, and share one visual row rhythm.

**Architecture:** Keep `SessionActivityStore` and `ActivityViewport` ownership unchanged. Adjust the shared page-boundary presentation and the existing Agents, Jobs, and Tasks bodies; update the current session-activity and design-system guides with the resulting behavior.

**Tech Stack:** React 19, TypeScript, CSS Modules, Zustand-backed activity stores, Vitest, Testing Library.

**Spec:** `docs/superpowers/specs/2026-11-17-activity-sidebar-auto-paging-design.md`

## Global Constraints

Work only in the `activity-sidebar-auto-paging` worktree. Preserve cursor paging, visible-demand reads, scroll restoration, folds, and recovery. Use existing widgets and spacing tokens; add no widget API. Tests assert behavior and structured values, not production copy. Run frontend gates serially under host load.

## Review Focus

- A closed inactive-agent fold must not stop later-page reads or hide the true history total; pin this in the existing sidebar paging test.
- A summary with unknown direct totals must not imply zero history; pin this in the existing Jobs/Watches tab tests.
- A visible later page must remain reachable and refresh/reconnect must preserve it; retain the existing Jobs multi-page and Activity view-retention tests, changing only manual-boundary interactions.
- Empty complete collections must not render permanent placeholders; pin the boundary's no-more-pages case and existing empty-tab cases.
- Task and activity lists must use the same gap without changing standalone Tasks sheet behavior; use focused DOM/class assertions and the browser row fixture.

---

### Task 1: Automatic page boundaries with truthful totals

**Files:**
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/ActivityPageBoundary.tsx`
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/ActivityPageBoundary.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/AgentsTab.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/JobsTab.tsx`
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.test.tsx`
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/JobsWatchesTabs.test.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/ActivityPanel.test.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/ActivityPanel.mobile.test.tsx`
- Test: `cmd/evener-hub/frontend/src/shell/statusbar/StatusBar.test.tsx`

**Interfaces:**
- Consumes: `ActivityScope.counts`, direct-session `snapshot.summary`, `ActivityPageBoundary.loadMore(resource)`, existing `Skeleton` and `Disclosure` widgets.
- Produces: no new public API; all activity continuation controls use automatic demand and Skeleton presentation.

- [ ] **Step 1: Convert the page-boundary tests to the new behavior.** Replace manual-button assertions with assertions that no continuation button exists and pending pages render Skeleton lines. Keep loading/error guards and the no-more-pages case.

- [ ] **Step 2: Run the focused boundary test and confirm it fails on the old button rendering.** Run `cd cmd/evener-hub/frontend && npx vitest run src/shell/activitybar/ActivityPageBoundary.test.tsx`.

- [ ] **Step 3: Replace the boundary button with Skeleton.** Keep the element ref, intersection observation, `loadMore`, restore pacing, and error/permanent guards. Render no button.

- [ ] **Step 4: Extend the existing sidebar paging test for automatic Agents loading and the summary-backed inactive total.** Supply known direct-session `total` and `active` counts that exceed loaded rows. Open Agents with inactive history folded, drive the boundary's existing observer, and assert cursor order and the last row without clicking a page control.

- [ ] **Step 5: Extend Jobs coverage for a completed total greater than its loaded terminal rows and for unknown summary totals.** Assert the known summary value is used and unknown counts do not create a history fold.

- [ ] **Step 6: Run the focused tests and confirm the new cases fail for the old Agents gate, manual button, and loaded-row totals.** Run Vitest on `ActivityPageBoundary.test.tsx`, `ActivitySidebar.test.tsx`, and `JobsWatchesTabs.test.tsx`.

- [ ] **Step 7: Implement the tab changes.** Remove Agents' `enabled` gate. Read direct-session summary counts for Agents and Jobs history labels, using `total - active` only when known. Convert Agents' history control to the same `Disclosure` pattern Jobs uses while retaining its existing fold id, row-reveal state, and scroll anchor.

- [ ] **Step 8: Update existing manual continuation interactions in ActivityPanel and StatusBar tests to trigger the same automatic observer path, preserving their recovery assertions.**

- [ ] **Step 9: Run the focused suites and confirm they pass.** Run all test files named above with one Vitest invocation.

- [ ] **Step 10: Commit the automatic-loading and totals slice.** Stage only the named implementation and test files, inspect the staged diff, and commit `feat(web): page activity collections automatically` with the required Evener provenance trailer.

### Task 2: Shared list rhythm, task placeholders, and product docs

**Files:**
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/activitybar.module.css`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/TasksTab.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/taskspanel.module.css`
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/TasksTab.test.tsx`
- Modify: `docs/product/session-activity.md`
- Modify: `docs/web-ui/design-system.md`

**Interfaces:**
- Consumes: `scope.counts.tasksTotal`, existing `TasksPanelBody`, and the shared `.stack` spacing.
- Produces: one `--space-1` row gap across activity and task lists, and count-sized initial Tasks Skeleton placeholders.

- [ ] **Step 1: Add the initial Tasks placeholder case to the existing Tasks tab suite.** Use a model with a known total and an unresolved task-list response; assert the Skeleton line count equals that total, then resolve and assert the actual rows replace it. Keep unknown totals bounded rather than inventing rows.

- [ ] **Step 2: Add a focused structural spacing check.** Assert Tasks' list and the activity stack resolve the same `--space-1` gap; assert the Agents-only history inset is gone. Use existing browser row-geometry fixtures for the actual rendered check rather than treating CSS source text alone as visual proof.

- [ ] **Step 3: Run the focused Tasks and spacing checks and confirm they fail on the old gap/two-line placeholder.**

- [ ] **Step 4: Implement the shared rhythm and task placeholders.** Set task-list gap to `--space-1`, remove `foldButton`, and size initial Tasks Skeleton lines from the known total with the existing default when no total is known. Update the evergreen session-activity and design-system guides to describe automatic visible-boundary paging, static placeholders, and summary-backed totals.

- [ ] **Step 5: Run focused tests and Biome on every touched source file.** Run `npx biome check --write` from `cmd/evener-hub/frontend` with explicit touched paths, then the focused Vitest suites and `npm run typecheck`.

- [ ] **Step 6: Commit the visual/docs slice.** Stage only the named files, inspect the staged diff, and commit `fix(web): align activity sidebar list presentation` with the required Evener provenance trailer.

### Task 3: Whole-change verification and review

**Files:**
- Review the complete branch diff and all files changed above.

- [ ] **Step 1: Run the frontend gate serially.** From the worktree root run `make test-web`; if Chrome is available, run `make test-web-browser`. Report exactly which lanes ran.

- [ ] **Step 2: Inspect the real Overview in both themes and at desktop/phone widths.** Verify automatic later pages, static placeholders, correct totals, shared padding, folds, and no horizontal overflow. Use the existing overflow/shell guards where they cover the changed geometry.

- [ ] **Step 3: Run `make lint` once before pushing and fix only failures this change caused.** Do not run other heavy gates concurrently.

- [ ] **Step 4: Run the simplify-code skill and one Par review on the finished change.** Apply only evidence-backed, in-scope findings, then rerun affected focused checks.

- [ ] **Step 5: Report deliverables and limits.** Name the worktree/branch, commits, actual checks, browser evidence, and any unverified behavior. Use shepherd-pr if Jesse asks for a PR.

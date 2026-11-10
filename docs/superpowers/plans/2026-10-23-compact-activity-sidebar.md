# Compact Activity Sidebar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the browser activity sidebar compact title-and-time rows across Agents, Jobs, Watches, and Tasks while preserving their current actions and context.

**Architecture:** Compose timestamps through the existing `ListRow` label and `Disclosure` summary content. A small row helper owns source-time selection and visual timing; an explicit sidebar clock supplies one `now` to rows and Watch details. The shared task body accepts a presentation prop so only the sidebar changes.

**Tech Stack:** React 19, TypeScript, CSS Modules, existing Evener widgets and AppWire types, Vitest, Testing Library, Chrome geometry guards.

**Spec:** `docs/superpowers/specs/2026-10-23-compact-activity-sidebar-design.md`

## Global Constraints

- Browser sidebar only: no native, backend, protocol, standalone Activity tree, or standalone Tasks appearance changes.
- Preserve existing groups, actions, disclosures, activity ownership, subscriptions, status shapes, and task styling.
- No new widget, public widget API, raw controls, widget-internal CSS, or per-row interval timers.
- Use real activity timestamps; omit missing times instead of fabricating them.
- Tests assert source timestamps, accessible state, structure, actions, and geometry, never UI copy.
- Write and watch focused behavior tests fail before implementing the behavior they cover.

## Review Focus

- Successful settled Agents/Jobs lose their visible status line: preserve the resolved status through an accessible description, not a nested `title`. Task 1 covers this.
- Expanded Watch details and first-line timestamps can disagree: supply one visible-sidebar clock to both and prove ticks cause no extra transport work. Task 2 covers this.
- Long titles can push timestamps to another line or overlap: measure actual geometry at desktop and narrow widths, not just DOM order. Task 3 covers this.
- Legacy settled Tasks without `completedAt` need a truthful fallback, not a missing row or fabricated run time. Task 2 covers this.
- Resumed Agents and retained Watches have older evidence alongside newer state: choose the current Agent run's quiet anchor and never label a Watch's creation time as a new delivery. Task 1 covers this.
- Changing the shared Tasks body can silently restyle the standalone Sheet: pin the presentation boundary and standalone rendering. Task 2 covers this.

---

### Task 1: Compact Agent, Job, and Watch rows

**Files:**
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/activityTime.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/activityRows.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/activitybar.module.css`
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/activityRows.test.tsx`

**Interfaces:**
- Consumes: existing `SessionDelegate`, `JobActivityJob`, `SessionWatch`, `relativeTime`, `absoluteTime`, status and glyph helpers.
- Produces: `ActivityTime({ at: string | undefined, meaning: string, now: number })`, returning a `<time>` with the source ISO `dateTime`, relative display, and meaning in its accessible tooltip; `agentActivityTime(sub)`, `jobActivityTime(job)`, and `watchActivityTime(watch: SessionWatch)` returning `{ at, meaning } | undefined`. `AgentRow`, `JobRow`, and `WatchRow` accept optional `now?: number`; when omitted they use a render-time instant for standalone fixture callers, while Task 2 supplies the shared clock in production.

- [ ] **Step 1: Write failing source-time and density tests**

  Table-drive live/settled Agents and Jobs, missing fields, and Watches with and without deliveries. Assert the rendered `<time>`'s `dateTime` equals the authoritative source and the visible text changes when the same row is rendered with different `now` values. Assert successful settled rows have no meta line, failures retain an accessible outcome and the existing shape, live intent remains, and Watch detail disclosure still works. Assert successful and cancelled settled rows expose their status through the interactive row's accessible name or description. Include a resumed Agent whose previous `latestActivityAt` predates `runStartedAt`, and an armed Watch with historical deliveries but no `deliveryTimes`; their stamps must respectively select the current run start and retain creation/armed meaning rather than imply a new delivery.

- [ ] **Step 2: Run the row tests and verify RED**

  Run: `cd cmd/evener-hub/frontend && npm test -- src/shell/activitybar/activityRows.test.tsx`

  Expected: existing tests pass; new cases fail because row timestamps and the settled-row presentation are not implemented.

- [ ] **Step 3: Implement the time helper and row composition**

  Implement the exact interfaces above using the spec's timestamp table and existing formatters. Reuse the delegate quiet-anchor rule rather than unconditionally preferring old `latestActivityAt`. Compose each title and `<time>` through the existing `ListRow` label slot or Watch `Disclosure` summary; no new public widget API. Add layout-only local CSS for the timestamp and failure meta where needed. Use `jobIsFailed`/`activityDelegateState` for outcomes, not status-string guesses. Preserve `onDrill`/`onOpen`, full-title access, `useDotFlip`, live intent, and `watchMeta`.

- [ ] **Step 4: Run the row tests and verify GREEN**

  Run: `cd cmd/evener-hub/frontend && npm test -- src/shell/activitybar/activityRows.test.tsx`

  Expected: all cases pass with no new warnings.

- [ ] **Step 5: Commit the working row change**

  `git add` only the four Task 1 paths; review the staged diff and commit `feat(webui): compact activity row timestamps`.

### Task 2: Sidebar clock and scoped Tasks presentation

**Files:**
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/activityTabs.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/AgentsTab.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/JobsTab.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/WatchesTab.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/activitybar/TasksTab.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/TasksPanel.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/taskspanel.module.css`
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.test.tsx`
- Test: `cmd/evener-hub/frontend/src/shell/activitybar/TasksTab.test.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx`

**Interfaces:**
- Consumes: Task 1's `ActivityTime` and row `now` props; existing `useNowTick`, `TaskRow`, `TaskCheck`, and task group/disclosure logic.
- Produces: `ActivityTabBodyProps.now: number`; `TasksPanelBody({ ..., presentation?: "panel" | "compact" })`, default `"panel"`; `taskActivityTime(task)` returning `{ at, meaning } | undefined`. The visible `ActivitySidebar` owns the single 30-second interval and supplies its `now` to the tab body and expanded Watch detail. No per-row intervals are introduced.

- [ ] **Step 1: Write failing clock and task-presentation tests**

  With a fixed epoch and fake timers, render the real `ActivitySidebar` with Agents, Jobs, and an expanded Watch. Advance exactly 30 seconds and assert every relative label/detail derives from the same instant while each `dateTime` remains the original source. Assert one 30-second interval, no added interval on tab switches, zero after close/unmount, and unchanged transport-call counts. Keep the existing agent quiet-threshold timeout separate.

  Add transport-backed task cases for `completedAt`, legacy `updatedAt` fallback, open-task `updatedAt`, missing times, and task disclosure. Assert completed titles remain non-struck and cancelled titles retain their existing struck attribute. Render the standalone Tasks body without the new prop and assert it does not receive the compact presentation.

- [ ] **Step 2: Run the focused tests and verify RED**

  Run: `cd cmd/evener-hub/frontend && npm test -- src/shell/activitybar/ActivitySidebar.test.tsx src/shell/activitybar/TasksTab.test.tsx src/panes/session/chrome/TasksPanel.test.tsx`

  Expected: existing behavior passes; new clock, source-time, and presentation-boundary cases fail for the missing behavior.

- [ ] **Step 3: Implement one clock and the scoped task row**

  Own the interval only when the sidebar is open and has a resolved scope. Pass `now` through the existing tab props to rows and `ActivityWatchDetail`; remove WatchesTab's separate interval. Compose the task title and `ActivityTime` in the existing `Disclosure` summary, preserving `TaskCheck`, latest notes, task grouping, and task details. Use the spec's task timestamp fallback and opt in only from `TasksTab`. Add layout-only CSS; do not reach into `Disclosure` or other widget internals.

- [ ] **Step 4: Run the focused tests and verify GREEN**

  Run the Step 2 command.

  Expected: all cases pass, including the standalone Tasks boundary and existing actions/disclosures.

- [ ] **Step 5: Commit the working integration**

  `git add` only Task 2 paths; review the staged diff and commit `feat(webui): share compact sidebar timing`.

### Task 3: Browser geometry, product contract, and delivery

**Files:**
- Modify only if a real measured defect requires it: `cmd/evener-hub/frontend/src/shell/activitybar/activitybar.module.css` and/or `cmd/evener-hub/frontend/src/panes/session/chrome/taskspanel.module.css`
- Modify: `cmd/evener-hub/frontend/src/dev/shellguard-entry.tsx` if more real fixture states are needed for geometry
- Modify: `cmd/evener-hub/frontend/scripts/shellguard/run.mjs` only for concrete title/time and touch geometry assertions
- Modify: `docs/product/session-activity.md`
- Modify: `docs/product/subsystems.md`
- Modify: `docs/superpowers/specs/2026-10-23-compact-activity-sidebar-design.md` to record approved status and implementation clarifications, without changing Jesse's selected direction

**Interfaces:**
- Consumes: the completed compact rows and real shellguard fixtures.
- Produces: browser evidence for long titles, no overlap, no horizontal overflow, touch sizing, both themes, and preserved row actions; evergreen documentation of the compact presentation contract.

- [ ] **Step 1: Add failing geometry checks where coverage is absent**

  Use real activity rows with long titles and fixed times. Assert each `<time>` stays on the title's first line, does not overlap it, and stays inside the row; assert no horizontal overflow and existing 44px touch sizing. Cover desktop and narrow browser widths and successful/failed/live states. Keep fixtures real, not duplicate production markup.

- [ ] **Step 2: Run the focused browser check and verify RED for missing geometry coverage**

  Import `overviewOnPage` from `scripts/shellguard/run.mjs` in a small Node verification driver and use its existing `startBrowserGuard` setup with `scripts/shellguard/settingsGeometry.vite.config.mjs`. Run only the Overview journey for the new geometry case, rather than unrelated shellguard suites. Expected: it fails only if the new geometry contract is not met; if the implementation already passes, record that the check passed immediately and do not manufacture a product failure.

- [ ] **Step 3: Correct any confirmed geometry defect at its source**

  Use the smallest local layout correction, then rerun the same browser check. Do not adjust test tolerances or hide overflow to obtain a pass.

- [ ] **Step 4: Verify and document**

  Run affected focused tests, `npm run typecheck`, and Biome on touched files from the frontend. Run targeted browser checks on this host; CI owns `make test-web`, `make test-web-browser`, and the full suite. Visually inspect desktop and narrow layouts in both themes with real components. Update the owning product guide and subsystem map to state the durable compact row and timestamp behavior. Run simplify-code and two independent implementation reviewers on the immutable base/head diff.

- [ ] **Step 5: Commit and shepherd**

  Review and commit only intended files. Open a PR with `shepherd-pr`, push each review round once, and let CI run full verification. Resolve findings red-first, merge the base before final approval, and admin squash-merge only when the repository's green-check/settled-review policy permits. Report the PR, merge SHA, checks actually run, and anything not verified.

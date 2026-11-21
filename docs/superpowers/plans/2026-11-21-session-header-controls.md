# Session Header Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put Find then Overview directly in every session header while retaining Verbosity and all unrelated session behavior.

**Architecture:** One `SessionInspectionActions` component serves the desktop `PaneScaffold.actions` slot and mobile `StackHost`. The existing session menu delegates Overview to the same helper and omits Find/Overview only when an ordinary session header owns them.

**Tech Stack:** React 19, TypeScript, CSS Modules, Zustand, Vitest, Testing Library.

**Spec:** `docs/superpowers/specs/2026-11-21-session-header-controls-design.md`

## Global Constraints

- Keep Verbosity in the composer menu and leave rail menus, cascade columns, non-session mobile panes, and the existing sidebar icon unchanged.
- Use existing widgets and their 44px coarse-pointer targets; add no widget prop or variant.
- Production change stays within the couple-hundred-line size budget.
- Follow behavior assertions rather than copy or styling oracles in tests.

## Review Focus

- Ordinary session versus cascade ownership: only the ordinary session composer loses duplicate Find/Overview items; Task 1 pins this.
- Overview scope and repeated activation: Find targets its pane and Overview opens, scopes, and closes on the same session; Task 1 pins this.
- Mobile Back destinations: root fallback shows Sessions, while retained-parent, back-stack, and attention paths keep Back; Task 2 extends the existing host cases.
- Non-session mobile navigation: existing document/drawer tests must keep passing in Task 2.
- Narrow touch layout: Task 2 verifies real desktop, tablet, and phone rendering, including title truncation and 44px targets.

---

### Task 1: Shared Find and Overview actions

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionInspectionActions.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/sessionMenu/SessionMenu.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`
- Test: `cmd/evener-hub/frontend/src/panes/session/Session.states.test.tsx`

**Interfaces:**
- Produces: `SessionInspectionActions({ sessionRef, paneId }: { sessionRef: string; paneId?: string })`, rendering Find then Overview with existing widgets.
- Produces: `openSessionOverview(sessionRef, paneId, isMobile)` for both the new component and existing `SessionChrome` menu.
- Adds: `SessionMenuProps.showOverview?: boolean`, default `true`; ordinary session chrome passes `false`, rail callers remain unchanged.

- [ ] **Step 1: Write failing action and placement tests**

Extend the existing session-chrome setup to render `SessionInspectionActions`. Assert Find creates a request for the owning pane, Overview toggles the correctly scoped sidebar and selected state, and Find precedes Overview structurally. Extend an existing session-header test to assert the actions occupy `PaneScaffold`'s action slot. Add an ordinary-session menu case showing Find/Overview are no longer duplicated and Verbosity still opens its control; retain a cascade/non-header case showing existing access remains.

- [ ] **Step 2: Run the tests and confirm the new cases fail for missing behavior**

Run from `cmd/evener-hub/frontend`:

```bash
npx vitest run src/panes/session/chrome/SessionChrome.test.tsx src/panes/session/Session.states.test.tsx
```

Expected: FAIL because the shared component and header/menu behavior do not exist.

- [ ] **Step 3: Implement the shared component and ordinary-session wiring**

Create the two actions in Find/Overview order using `Button size="sm"` on desktop and `IconButton` with `Tooltip` below 900px. Reuse the existing Find request bridge and extract the current Overview open/toggle logic without changing it. Mount the component from the ordinary session scaffold on desktop. Suppress duplicate Find/Overview menu items only when the owning workspace pane is an ordinary session; leave Verbosity and every other menu item intact.

- [ ] **Step 4: Run the focused tests and confirm they pass**

Run the Step 2 command again. Expected: both files PASS.

- [ ] **Step 5: Commit Task 1**

Stage only the Task 1 files and commit `feat(web): add session inspection header actions` with the required Evener provenance trailer.

### Task 2: Consolidate mobile session navigation

**Files:**
- Modify: `cmd/evener-hub/frontend/src/shell/mobile/StackHost.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/mobile/StackHost.module.css`
- Test: `cmd/evener-hub/frontend/src/shell/mobile/StackHost.test.tsx`
- Modify: `docs/web-ui/design-system.md`
- Modify: `docs/product/subsystems.md`

**Interfaces:**
- Consumes: `SessionInspectionActions` from Task 1 for the focused mobile session.
- Produces: no new reusable API. Existing `handleBack` behavior remains the authority for a Back activation.

- [ ] **Step 1: Write failing mobile navigation tests**

Add cases to `StackHost.test.tsx`: a session with no Back destination renders Sessions but not Back and opens the session panel; a session with a retained parent or valid back-stack destination renders Back but not Sessions; the focused session top bar contains Find then Overview. Keep the existing attention, cascade, session-panel, and non-session drawer cases unchanged and running.

- [ ] **Step 2: Run the host suite and confirm the new cases fail**

Run:

```bash
npx vitest run src/shell/mobile/StackHost.test.tsx
```

Expected: FAIL because current session top bars always render Back and a separate trailing Sessions control.

- [ ] **Step 3: Implement the minimal session-only top-bar composition**

Render the shared inspection actions for the focused session. For sessions, place Sessions in the leading slot only when no retained parent, valid back-stack destination, or attention Back exists; otherwise render Back there instead. Do not change `handleBack`, non-session drawer placement, or single-pane restrictions. Left-align session titles through the existing CSS module.

- [ ] **Step 4: Run focused and regression tests**

Run:

```bash
npx vitest run src/shell/mobile/StackHost.test.tsx src/panes/session/chrome/SessionChrome.test.tsx src/panes/session/Session.states.test.tsx src/shell/mobile/TreeDrawer.test.tsx
```

Expected: all four files PASS. Then run Biome on touched frontend files and `npm run typecheck` from `cmd/evener-hub/frontend`.

- [ ] **Step 5: Verify and document the behavior**

Inspect the real session at 1440px, 834px, and 390px using the repository's fixture UI. Confirm action order, Overview selected state, Find targeting, 44px touch targets, title truncation, and the Sessions/Back transition. Update the design-system and S02 subsystem text to describe the shipped ownership. Delete this implementation plan in the completing commit, as required by the product documentation convention.

- [ ] **Step 6: Commit Task 2**

Stage only the Task 2 files and plan deletion and commit `feat(web): consolidate mobile session navigation` with the required Evener provenance trailer.

# Session Panel Panes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace desktop SessionChrome Tasks, Activity, and Details sheets with true togglable secondary panes while preserving mobile sheets, durable ref-keyed panel state, route placement, palette commands, and accessible focus behavior.

**Architecture:** Add three eagerly registered, lazily rendered pane types (`sessionTasks`, `sessionActivity`, `sessionDetails`) with `{ ref: string }` params and secondary-slot placement. Extract the existing panel bodies behind shared desktop-pane and mobile-sheet hosts, moving state that must survive dockview tab unmounts into stores keyed by session ref. Make workspace toggling, route placement, palette scope, breakpoint crossing, and focus markers explicit rather than relying on DOM trigger lookup or mount-time focus.

**Tech Stack:** React/TypeScript, Zustand vanilla stores, dockview, Vitest/jsdom, CSS modules, Biome, Vite/Chrome browser guards.

## Global Constraints

- Read `docs/testing.md` before changing tests; default tests remain deterministic and use fake clients/transports rather than provider credentials, network, wall-clock sleeps, or ambient machine state.
- Desktop uses dockview panes; mobile (`useIsMobile`, viewport below 900px) keeps the existing Sheet hosts and open-only behavior.
- The three new pane types are non-routed, use params `{ ref: string }`, open only in the secondary slot, and have titles `Tasks · <session-name>`, `Activity · <session-name>`, and `Details · <session-name>` with raw-ref fallback.
- Desktop button and palette actions are true toggles; mobile buttons and mobile palette actions retain legacy open-only Sheet behavior.
- Durable Tasks and Activity reader state is store-backed and keyed by session ref; no coverage is removed from existing panel suites.
- In-flight store-owned fetches always publish completion regardless of body mountedness; do not carry over mounted-component completion drops.
- The Activity summary preserves the established-attempt and collapsed-trigger freshness gates, root-fetch-only badge publishing, `counts.complete` gating, and one-fetch-at-a-time behavior.
- `routePlacementIsApplied` must accept a focused session panel pane only after the current route has completed placement; fresh navigation/deferred boot placement still focuses the routed pane.
- Pending pane-focus markers fire only for toggle-open, are cancelled before first mount if activation is lost, and never focus on ordinary tab remount.
- Do not add deep links, change panel content or badge math, remove the Sheet widget, add dockview Escape-to-close/focus trapping, or change dockview layout policy beyond secondary placement.
- Before frontend gates, run `npx biome check --write` on every touched frontend file. Run `make test-web`; on a Chrome-capable host also run `make test-web-browser`.

---

## File and Boundary Map

- `cmd/serf-hub/frontend/src/shell/paneRegistry.ts`: extend the pane-type union and retain the existing typed registration boundary.
- `cmd/serf-hub/frontend/src/panes/sessionPanels/`: create the eager registration module, pane prop/types, shared host helpers, and three lazy pane components.
- `cmd/serf-hub/frontend/src/shell/workspace.ts`: add ref-aware `togglePane`, `isPaneOpen`, and pending-focus state/actions without exposing `sameParams`.
- `cmd/serf-hub/frontend/src/shell/AppShell.tsx`: eagerly import the registration module and make route-placement focus rules aware of panel panes and completed placement.
- `cmd/serf-hub/frontend/src/shell/routing.ts`: make the exhaustive non-routed switch handle all three panel types.
- `cmd/serf-hub/frontend/src/stores/`: add ref-keyed Tasks, Activity, and Activity-summary stores plus quiescent workspace-based eviction.
- `cmd/serf-hub/frontend/src/panes/session/chrome/`: extract body components, retain mobile Sheet hosts, and route desktop controls through the workspace/pane APIs.
- `cmd/serf-hub/frontend/src/shell/palette/`: derive session refs from panel panes and dispatch desktop toggles directly.
- `cmd/serf-hub/frontend/src/widgets/panescaffold/index.tsx`: add the focusable scaffold region used by toggle-open and BackToParentAction focus transfer.
- `cmd/serf-hub/frontend/src/shell/mobile/StackHost.tsx`: suppress the generic top-bar back path for panel panes crossing into mobile.
- Existing colocated tests under `shell`, `panes/session/chrome`, `stores`, `widgets/panescaffold`, and new `panes/sessionPanels` tests: pin each normative behavior in the spec.

---

### Task 1: Register panel pane types and create pane hosts

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/paneRegistry.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/routing.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/sessionPanels/index.ts`
- Create: `cmd/serf-hub/frontend/src/panes/sessionPanels/SessionPanelPane.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/sessionPanels/sessionPanelPane.test.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/sessionPanels/index.test.ts`

**Interfaces:**
- Produces `SessionPanelParams = { ref: string }` and `SessionPanelKind = "tasks" | "activity" | "details"`.
- Produces three `PaneDescriptor<SessionPanelParams>` entries with IDs `sessionTasks`, `sessionActivity`, and `sessionDetails`.
- Produces a shared host contract that receives `params`, `paneId`, `focused`, and a panel kind, claims the thread while mounted, and renders a `PaneScaffold` loading state before model hydration.

- [ ] **Step 1: Add failing registry, routing, and restore tests.** Assert all three IDs are valid registered pane types, descriptors title through `ctx.threadName?.(ref) ?? ref`, `paneToURL` returns `null`, and a restored `{ paneType: "sessionTasks", paneParams: { ref: "ref_a" } }` survives validation.

```ts
expect(paneFor("sessionTasks").title({ ref: "ref_a" }, { threadName: () => "Build" })).toBe("Tasks · Build");
expect(paneToURL("sessionActivity", { ref: "ref_a" })).toBeNull();
expect(paneFor("sessionDetails").title({ ref: "ref_a" }, {})).toBe("Details · ref_a");
```

- [ ] **Step 2: Run the focused tests and verify they fail** with missing IDs/descriptors or exhaustive-switch errors:

```bash
cd cmd/serf-hub/frontend
npx vitest run src/shell/paneRegistry.test.ts src/shell/routing.test.ts src/shell/paneRestore.test.ts src/panes/sessionPanels/index.test.ts
```

- [ ] **Step 3: Implement the eager registration module.** Keep the registration module light; use `lazy(() => import("./SessionPanelPane"))` for components, register all three descriptors, and import `../panes/sessionPanels` from `AppShell.tsx` beside the doc/transcript registration imports.

- [ ] **Step 4: Implement the shared pane host.** Validate `params.ref`, call the same connection-ready `threadsStore.ensureThread(ref)` / `releaseThread(ref)` lifecycle used by `Session.tsx`, render `PaneScaffold` plus `EmptyState` while `threadsStore` has not hydrated a model, and dispatch the selected body after hydration. Pass `BackToParentAction parentRef={ref}` and the pane ID to the host so later tasks can wire focus markers.

- [ ] **Step 5: Run the focused tests and commit.**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/shell/paneRegistry.test.ts src/shell/routing.test.ts src/shell/paneRestore.test.ts src/panes/sessionPanels/index.test.ts src/panes/sessionPanels/sessionPanelPane.test.tsx
cd ../../..
git add cmd/serf-hub/frontend/src/shell/paneRegistry.ts cmd/serf-hub/frontend/src/shell/routing.ts cmd/serf-hub/frontend/src/shell/AppShell.tsx cmd/serf-hub/frontend/src/panes/sessionPanels
git commit -m "feat(web): register session panel panes"
```

---

### Task 2: Add workspace toggle, open-state, and pending-focus APIs

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/workspace.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/paneActions.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/workspace.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/paneActions.test.ts`

**Interfaces:**
- Produces `WorkspaceStoreState.togglePane(type: PaneTypeId, params: unknown): { paneId: string; opened: boolean }`.
- Produces `isPaneOpen(state: WorkspaceStoreState, type: PaneTypeId, params: unknown): boolean`.
- Produces pending-focus operations keyed by pane ID: `requestPaneFocus(paneId)`, `consumePaneFocus(paneId): boolean`, and `cancelPaneFocus(paneId)`.
- Keeps `sameParams` module-private and makes the store, not callers, resolve the pane ID required by `closePane`.

- [ ] **Step 1: Write failing store tests** for new secondary panes: first toggle creates/focuses exactly one pane, second toggle closes it and nulls focus, a third toggle creates a new pane in `slot: "secondary"`, identical refs dedupe, and `isPaneOpen` distinguishes refs.

```ts
const first = workspaceStore.getState().togglePane("sessionTasks", { ref: "ref_a" });
expect(first.opened).toBe(true);
expect(workspaceStore.getState().panes).toMatchObject([{ type: "sessionTasks", slot: "secondary" }]);
expect(workspaceStore.getState().focusedPaneId).toBe(first.paneId);
expect(workspaceStore.getState().togglePane("sessionTasks", { ref: "ref_a" }).opened).toBe(false);
expect(workspaceStore.getState().focusedPaneId).toBeNull();
```

- [ ] **Step 2: Write pending-focus tests** proving a marker is consumed once, cancellation prevents later consumption, and ordinary `focusPane`/restore paths never create a marker.

- [ ] **Step 3: Run the focused tests and verify failure.**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/shell/workspace.test.ts src/shell/paneActions.test.ts
```

- [ ] **Step 4: Implement the store methods.** The open path calls existing `openPane(type, params, { slot: "secondary" })`, requests focus for the returned new pane, and returns `opened: true`; the close path finds the matching record using the private `sameParams`, calls `closePane(id)`, cancels any pending marker, and returns `opened: false`. Preserve existing singleton/open semantics and test reset behavior.

- [ ] **Step 5: Add `togglePane` to `paneActions.ts`** as the stable imperative seam used by SessionChrome and palette handlers, and add fake-dockview tests for toggle-close behavior after a pane has been popped out or moved outside the normal grid.

- [ ] **Step 6: Run tests, format, and commit.**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/shell/workspace.ts src/shell/paneActions.ts src/shell/workspace.test.ts src/shell/paneActions.test.ts
npx vitest run src/shell/workspace.test.ts src/shell/paneActions.test.ts
cd ../../..
git add cmd/serf-hub/frontend/src/shell/workspace.ts cmd/serf-hub/frontend/src/shell/paneActions.ts cmd/serf-hub/frontend/src/shell/workspace.test.ts cmd/serf-hub/frontend/src/shell/paneActions.test.ts
git commit -m "feat(web): add session panel pane toggles"
```

---

### Task 3: Make route placement tolerate focused panel panes without weakening navigation

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx`

**Interfaces:**
- Keeps `openRouteAsPane` behavior unchanged for boot, deferred deep links, and navigation.
- Extends `routePlacementIsApplied(pathname, tree)` to recognize a focused `sessionTasks`, `sessionActivity`, or `sessionDetails` pane only when the current pathname has completed placement.
- Adds `placedPathname` bookkeeping and a pathname-qualified placement guard confirmation.

- [ ] **Step 1: Add failing route tests** for top-level routes with same-ref and aside-ref focused panel panes, nested-session routes with focused panel panes, and non-panel focused panes that must still re-focus the routed session.

- [ ] **Step 2: Add failing placement tests** for a deferred boot deep link over a restored active panel, `/thread/X` ↔ `/s/X` navigation while a panel is focused, and a stale guard armed for pathname A clearing during pathname B without marking B placed.

- [ ] **Step 3: Run the focused tests and verify the current route effect re-focuses the main/child session pane.**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/shell/AppShell.test.tsx
```

- [ ] **Step 4: Implement the predicate and bookkeeping.** Keep all structural checks. For top-level routes accept `focusedPaneId === main.id` or any panel pane; for nested routes accept `focusedPaneId === child.id` or any panel pane. Apply the relaxed clause only when `pendingSessionRef` is empty, the route equals the last completed `placedPathname`, and no current route change is awaiting placement. Record the pathname when the predicate is true and when the in-progress guard clears for the same pathname only.

- [ ] **Step 5: Re-run route tests and commit.**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/shell/AppShell.tsx src/shell/AppShell.test.tsx
npx vitest run src/shell/AppShell.test.tsx
cd ../../..
git add cmd/serf-hub/frontend/src/shell/AppShell.tsx cmd/serf-hub/frontend/src/shell/AppShell.test.tsx
git commit -m "fix(web): preserve panel focus during route placement"
```

---

### Task 4: Implement ref-keyed Tasks and Activity reader stores with eviction

**Files:**
- Create: `cmd/serf-hub/frontend/src/stores/tasksPanel.ts`
- Create: `cmd/serf-hub/frontend/src/stores/activityPanel.ts`
- Create: `cmd/serf-hub/frontend/src/stores/panelStoreEviction.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.ts`
- Create: `cmd/serf-hub/frontend/src/stores/tasksPanel.test.ts`
- Create: `cmd/serf-hub/frontend/src/stores/activityPanel.test.ts`
- Create: `cmd/serf-hub/frontend/src/stores/panelStoreEviction.test.ts`

**Interfaces:**
- `tasksPanelStore` is keyed by ref and retains rows, daemon-gone state, and the last failure; its actions include `beginFetch`, `publishFetch`, `setRows`, `setFailure`, and `resetForTests`.
- `activityPanelStore` is keyed by ref and retains the reducer-owned tree, expansion, selection, grafted continuations, and continuation failures; its actions use the existing `reconcileActivityState` semantics.
- Fetch completion is store-owned and publishes even if the initiating body unmounted.
- Eviction subscribes to workspace changes, settles through a microtask, and removes entries only when no open session or panel pane references the ref.

- [ ] **Step 1: Read the existing Tasks/Activity reducers and tests**, then write failing store tests for retained rows after failed refresh, daemon-gone rows, activity selection/expansion and continuation graft retention, and publish-after-unmount behavior.

- [ ] **Step 2: Write failing eviction tests** for a mounted session claim, an open-but-backgrounded panel with no thread claim, closing one of multiple ref panes, and a transient multi-set restore sequence that must not evict between intermediate states.

- [ ] **Step 3: Run the focused store tests and verify failure.**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/stores/tasksPanel.test.ts src/stores/activityPanel.test.ts src/stores/panelStoreEviction.test.ts
```

- [ ] **Step 4: Implement the stores by lifting only durable state.** Keep row disclosure IDs and existing wire parsing; move panel-local reducer state into ref entries. Preserve load-state meanings and existing retained-list/error behavior. Ensure root fetches update both activity summary and activity panel state, while continuation patches never overwrite root badge counts.

- [ ] **Step 5: Install quiescent eviction** from the workspace pane list and integrate reset helpers into test setup. Do not key eviction solely on thread claims because dockview unmounts background tabs.

- [ ] **Step 6: Update panel tests to consume store-backed state, format, run all store and existing panel suites, and commit.**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/stores src/panes/session/chrome/TasksPanel.tsx src/panes/session/chrome/ActivityPanel.tsx
npx vitest run src/stores src/panes/session/chrome/TasksPanel.test.tsx src/panes/session/chrome/ActivityPanel.test.tsx
cd ../../..
git add cmd/serf-hub/frontend/src/stores cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx
git commit -m "feat(web): retain panel state across pane remounts"
```

---

### Task 5: Extract shared panel bodies and desktop/mobile hosts

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/DetailsPanel.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/sessionPanels/SessionPanelPane.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/TasksPanel.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/DetailsPanel.test.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/sessionPanels/SessionPanelPane.test.tsx`

**Interfaces:**
- Each panel exports a body component that takes the hydrated `ThreadModel` and ref, but owns no durable state.
- Mobile hosts retain the current `Sheet`, imperative `open()` handles, and open-only semantics.
- Desktop hosts render the body inside the registered pane's `PaneScaffold`, use the new stores, and perform mount/unmount freshness signals.

- [ ] **Step 1: Add failing pane-host tests** for loading before hydration, Details ticking after hydration, Tasks mount fetch, Activity retained state, daemon-gone rendering, and ref claim/release.

- [ ] **Step 2: Add remount tests** that mount, mutate, unmount, and remount each body, asserting rows, activity selection/expansion/grafts, and failures remain intact while ordinary remount does not focus the scaffold.

- [ ] **Step 3: Run the existing and new panel tests before implementation to establish failures.**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/chrome/TasksPanel.test.tsx src/panes/session/chrome/ActivityPanel.test.tsx src/panes/session/chrome/DetailsPanel.test.tsx src/panes/sessionPanels/SessionPanelPane.test.tsx
```

- [ ] **Step 4: Extract body render functions/components without changing visible copy or badge calculations.** Keep `Tasks N/M`, `Activity`, and Details content unchanged; route fetches and reducer transitions through the stores from Task 4.

- [ ] **Step 5: Implement the desktop pane host.** Use `PaneScaffold`, `EmptyState` before hydration, `BackToParentAction`, ref-keyed title, and the exact Activity `needsVisibleFetch` gate: bump mismatch OR retained non-ready state (`idle`, `failed`, `unsupported`, `ended`). A fetch started by a body publishes to the store after unmount.

- [ ] **Step 6: Keep the mobile Sheet host behavior intact**, except that it reads the same stores and records the same mounted signal, then run the full panel suite, format, and commit.

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/panes/session/chrome src/panes/sessionPanels
npx vitest run src/panes/session/chrome src/panes/sessionPanels
cd ../../..
git add cmd/serf-hub/frontend/src/panes/session/chrome cmd/serf-hub/frontend/src/panes/sessionPanels
git commit -m "feat(web): share session panel bodies across hosts"
```

---

### Task 6: Preserve Activity summary freshness and badge behavior

**Files:**
- Create: `cmd/serf-hub/frontend/src/stores/activitySummary.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`
- Create: `cmd/serf-hub/frontend/src/stores/activitySummary.test.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ActivityPanel.test.tsx`

**Interfaces:**
- `activitySummaryStore` is keyed by ref and exposes mounted-state, established-attempt, loading, summary counts, and `counts.complete` state.
- The Activity trigger reads summary state and renders bare `Activity` until a complete root summary exists, then `Activity · N`.
- Root fetches publish to both the activity panel store and summary; continuation patches do not publish badge counts.

- [ ] **Step 1: Add failing summary tests** for no fetch before first open, establishment at fetch start even on failure, collapsed suppression, one fetch while a body is mounted, background refresh while a panel is open but backgrounded, and incomplete counts hiding the number.

- [ ] **Step 2: Run the focused tests and confirm current component-local behavior cannot satisfy the store contract.**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/stores/activitySummary.test.ts src/panes/session/chrome/ActivityPanel.test.tsx
```

- [ ] **Step 3: Implement the summary store and mount signal.** The trigger owns the established/collapsed gate; whichever Sheet or pane body is mounted owns freshness; a background root fetch reconciles retained Activity state; all completions publish irrespective of mountedness.

- [ ] **Step 4: Update Activity trigger rendering and preserve existing badge fixtures.** Assert root-only summary publication and `counts.complete` gating in tests.

- [ ] **Step 5: Format, run summary/activity tests, and commit.**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/stores/activitySummary.ts src/stores/activitySummary.test.ts src/panes/session/chrome/ActivityPanel.tsx src/panes/session/chrome/ActivityPanel.test.tsx src/panes/session/chrome/SessionChrome.tsx
npx vitest run src/stores/activitySummary.test.ts src/panes/session/chrome/ActivityPanel.test.tsx src/panes/session/chrome/SessionChrome.test.tsx
cd ../../..
git add cmd/serf-hub/frontend/src/stores/activitySummary.ts cmd/serf-hub/frontend/src/stores/activitySummary.test.ts cmd/serf-hub/frontend/src/panes/session/chrome
git commit -m "fix(web): keep activity badge fresh across pane hosts"
```

---

### Task 7: Wire SessionChrome toggles, overflow state, and palette dispatch

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/palette/paletteContext.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/palette/commands.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/useIsMobile.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/palette/paletteContext.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/palette/commands.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/useIsMobile.test.ts`

**Interfaces:**
- Desktop trigger handlers call `togglePane("sessionTasks" | "sessionActivity" | "sessionDetails", { ref })`; mobile handlers call the existing Sheet handle.
- Desktop triggers expose `aria-pressed={isPaneOpen(...)}`.
- Overflow menu labels append a checked adornment such as `Tasks ✓` when open.
- `buildPaletteContext` derives `sessionRef` from a focused session panel's params.
- `/tasks` and `/status` dispatch desktop toggles with context ref and use the legacy DOM click only on mobile; export the existing module-private viewport predicate for plain command handlers.

- [ ] **Step 1: Add failing SessionChrome tests** for desktop open/close, pressed state, mobile sheet open-only behavior, and overflow adornments after the pane causes the trigger row to collapse.

- [ ] **Step 2: Add failing palette tests** for focused session panel scope, desktop direct dispatch with collapsed chrome and unmounted session chrome, mobile legacy click, and toggle-close behavior.

- [ ] **Step 3: Run focused tests and verify current DOM-global commands fail in the collapsed/wrong-session cases.**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/chrome/SessionChrome.test.tsx src/shell/palette/paletteContext.test.ts src/shell/palette/commands.test.ts src/shell/useIsMobile.test.ts
```

- [ ] **Step 4: Implement the desktop/mobile branch.** Keep current labels, counts, menu IDs, and mobile imperative handles. Use store selectors for pressed state and make the menu's visible adornment derive from the same selector.

- [ ] **Step 5: Implement palette context and dispatch.** Keep all unrelated session-scoped commands unchanged. Make `/tasks` target `sessionTasks` and `/status` target `sessionDetails` with `ctx.sessionRef`; preserve the accepted DOM-focus-to-body result for palette-initiated close.

- [ ] **Step 6: Format, run focused tests, and commit.**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/panes/session/chrome/SessionChrome.tsx src/panes/session/chrome/SessionChrome.test.tsx src/shell/palette src/shell/useIsMobile.ts src/shell/useIsMobile.test.ts
npx vitest run src/panes/session/chrome/SessionChrome.test.tsx src/shell/palette/paletteContext.test.ts src/shell/palette/commands.test.ts src/shell/useIsMobile.test.ts
cd ../../..
git add cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.tsx cmd/serf-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx cmd/serf-hub/frontend/src/shell/palette cmd/serf-hub/frontend/src/shell/useIsMobile.ts cmd/serf-hub/frontend/src/shell/useIsMobile.test.ts
git commit -m "feat(web): toggle session panels from chrome and palette"
```

---

### Task 8: Implement pane focus transfer, BackToParentAction, and breakpoint behavior

**Files:**
- Modify: `cmd/serf-hub/frontend/src/widgets/panescaffold/index.tsx`
- Modify: `cmd/serf-hub/frontend/src/widgets/panescaffold/panescaffold.module.css`
- Modify: `cmd/serf-hub/frontend/src/widgets/panescaffold/panescaffold.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/sessionPanels/SessionPanelPane.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/mobile/StackHost.tsx`
- Modify: the existing `BackToParentAction` implementation and colocated tests, located by symbol search before editing
- Modify: `cmd/serf-hub/frontend/src/shell/mobile/StackHost.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/sessionPanels/SessionPanelPane.test.tsx`

**Interfaces:**
- `PaneScaffold` adds a `tabIndex={-1}` focusable content region and an optional stable pane-scaffold marker that `BackToParentAction` can query.
- Toggle-open consumes the pending marker and focuses the scaffold exactly once; ordinary dockview remount and restore do not focus.
- `BackToParentAction` chooses imperative focus vs pending marker by querying the parent's scaffold region in the DOM, never by checking workspace-store openness.
- Panel pane IDs are excluded from StackHost's generic top-bar back path; `BackToParentAction` is the return path after desktop→mobile crossing.

- [ ] **Step 1: Add failing scaffold and focus tests** for focusable content, one-time toggle-open focus, no focus on reactivation, pre-mount marker cancellation, and focus transfer to an already mounted parent.

- [ ] **Step 2: Add failing mobile crossing tests** proving panel panes render full-screen, generic StackHost back is suppressed, BackToParentAction remains available, and crossing back returns the pane to dockview state.

- [ ] **Step 3: Run focused tests and verify the current scaffold has no focus target and StackHost exposes the generic back path.**

```bash
cd cmd/serf-hub/frontend
npx vitest run src/widgets/panescaffold/panescaffold.test.tsx src/shell/mobile/StackHost.test.tsx src/panes/sessionPanels/SessionPanelPane.test.tsx
```

- [ ] **Step 4: Implement focusable scaffold and marker consumption.** On panel host activation, cancel the marker if the pane loses activation before lazy mount completes; otherwise consume it and call `.focus()` after dockview activation. Do not add Escape handling or a focus trap.

- [ ] **Step 5: Implement BackToParentAction's two DOM-query paths.** If the parent's scaffold is present, focus it after activation; if absent, record a marker consumed by the parent on mount. Document and test the accepted orphan-restore body-focus behavior.

- [ ] **Step 6: Suppress the generic mobile top-bar back affordance for the three panel IDs, format, run focused tests, and commit.**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/widgets/panescaffold src/shell/mobile src/panes/sessionPanels
npx vitest run src/widgets/panescaffold/panescaffold.test.tsx src/shell/mobile/StackHost.test.tsx src/panes/sessionPanels/SessionPanelPane.test.tsx
cd ../../..
git add cmd/serf-hub/frontend/src/widgets/panescaffold cmd/serf-hub/frontend/src/shell/mobile cmd/serf-hub/frontend/src/panes/sessionPanels
git commit -m "feat(web): move focus into session panel panes"
```

---

### Task 9: Complete integration coverage, browser guard, and final verification

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/paneRestore.test.ts`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.test.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/index.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/*.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/widgets/panescaffold/panescaffold.test.tsx`
- Modify: the existing `cmd/serf-hub/frontend/scripts/overflowguard` fixture/guard only if needed to cover panel-trigger collapse geometry

**Interfaces:**
- No new production interface; this task closes the acceptance matrix from spec §10.
- The existing browser guard remains the proof for ResizeObserver-driven collapse at real widths; jsdom tests must not claim to prove CSS geometry.

- [ ] **Step 1: Add restore round-trip coverage** for all three panel types through `layoutJSON`/`restoreLayout`, including raw-ref title fallback before hydration.

- [ ] **Step 2: Add thread claim/release coverage** for session-plus-panel claims in open/close/reorder sequences and verify models are evicted only after the last pane reference closes.

- [ ] **Step 3: Add final remount and badge pins** for retained rows, daemon-gone state, Activity selection/expansion/grafts, no focus on remount, established/collapsed/single-fetch gates, and root-only summary publication.

- [ ] **Step 4: Add or extend the browser guard** to open a panel at widths where the main session pane falls below 640px and assert the overflow menu exposes a visible checked adornment without horizontal overflow.

- [ ] **Step 5: Run formatting and canonical gates.**

```bash
cd cmd/serf-hub/frontend
npx biome check --write src
cd ../../..
make test-web
make test-web-browser
```

- [ ] **Step 6: Inspect the final diff and commit the verification-only test changes.**

```bash
git diff --check
git status --short
git diff --stat
git commit -m "test(web): cover session panel pane integration"
```

- [ ] **Step 7: Run the whole frontend gate once more after the final commit and record exact output.** A Chrome-unavailable environment is reported as an unavailable required browser prerequisite, not as a passing browser gate.

```bash
make test-web
make test-web-browser
```

---

## Plan Self-Review

- **Spec coverage:** Registration/routing (§1) is Task 1; extraction and hydration (§2, §8) are Tasks 1 and 5; toggle and close semantics (§3) are Task 2; route placement (§4) is Task 3; durable stores and eviction (§5) are Task 4; Activity freshness (§6) is Task 6; palette (§7) is Task 7; a11y and breakpoint crossing (§9, §11) are Task 8; all explicit test obligations (§10) are Task 9.
- **Incomplete-marker scan:** No unresolved markers or unspecified error-handling step is used. Each task names paths, interfaces, test behavior, commands, and commit intent.
- **Type consistency:** `SessionPanelParams`, the three pane IDs, `togglePane`, `isPaneOpen`, and focus-marker operations are introduced before their consumers. The desktop host consumes the stores before SessionChrome and palette wiring consume the toggle APIs.
- **Scope:** This is one frontend subsystem with coordinated store, shell, pane, and test changes; it does not include routing/deep-link or unrelated panel-content changes.

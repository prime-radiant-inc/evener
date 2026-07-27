# Primary Navigation Main-Pane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make primary navigation, route URLs, desktop restore, and mobile shared-store state obey one atomic main-pane replacement policy.

**Architecture:** `workspaceStore.replacePrimary` will own the atomic transition for Settings, Spawn, and top-level sessions. It will compare a stable identity (`"settings"`, `"spawn"`, or the canonical session ref), preserve secondary panes only when the requested identity is already main, and otherwise replace the entire workspace with one main pane. AppShell will be the sole route interpreter: Rail navigates to canonical URLs, AppShell resolves nested ownership from the authoritative tree, and DockHost reapplies its captured route intent through the same operation after restore.

**Tech Stack:** TypeScript, React 19, Zustand vanilla store, dockview, Vitest, Testing Library, existing FakeClient/tree fixtures, Biome.

## Global Constraints

- Primary navigation has one rule: Settings, the new-session form, and a top-level session always occupy the main pane.
- Changing that primary context replaces the current main pane and closes every secondary pane.
- Reopening the same primary context keeps its secondary panes.
- For Settings, changing only the section updates the existing main settings pane rather than treating it as a new primary context.
- A nested session must never occupy main, including after restoring a legacy layout.
- Clicking “New session” navigates to `/new`, replaces main with the spawn form, and clears the old secondary group.
- When creation succeeds, navigation moves to the created session route and replaces the spawn form with that top-level session in main.
- The browser URL, workspace store, dockview layout, and reload restoration must all describe the same primary navigation intent.
- Rail activation navigates to the canonical session URL; AppShell remains the single interpreter of route intent.
- Captured main panes are reapplied through the primary replacement operation; captured secondary panes are reopened only after their main owner.
- Generic additive `openPane` is not used to reapply a captured primary route.
- Preserve browser Back/Forward behavior, singleton parameter updates, and the shared workspace-store behavior used by mobile StackHost.
- The main pane has no tab or close affordance; no visual design changes are in scope.
- Do not add backward-compatible URL aliases or put session lineage into the generic workspace store.
- Default tests remain deterministic and must not depend on provider credentials, network access, model behavior, quota, wall-clock timing, or ambient machine state.
- Do not assert large serialized JSON or generated strings; assert structured pane state, URL state, rendered behavior, and focused layout effects.

---

## Current Code Map

- `cmd/serf-hub/frontend/src/shell/workspace.ts` owns the logical pane list, focus, slots, and generic `openPane` behavior.
- `cmd/serf-hub/frontend/src/shell/workspace.test.ts` is the real store contract suite and already registers fixture pane descriptors.
- `cmd/serf-hub/frontend/src/shell/AppShell.tsx` currently resolves routes but duplicates primary placement in `openSettingsInMain` and the `openPane("spawn")` branch.
- `cmd/serf-hub/frontend/src/shell/sessionPlacement.ts` currently repeats close-then-open promotion for top-level and nested sessions.
- `cmd/serf-hub/frontend/src/shell/rail/Rail.tsx` currently opens sessions directly through `sessionPlacement.ts` and has a tree-arrival placement repair effect.
- `cmd/serf-hub/frontend/src/shell/DockHost.tsx` currently captures routed panes before `restoreLayout` and reopens every captured pane through additive `openPane`.
- `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx`, `DockHost.test.tsx`, `Rail.test.tsx`, `sessionPlacement.test.ts`, `StackHost.test.tsx`, and `Spawn.test.tsx` already contain deterministic real-component seams for the requested regressions.
- `cmd/serf-hub/frontend/src/shell/routing.ts` already emits canonical `/s/{ref}`, `/new`, `/settings`, and `/settings/{section}` paths and intentionally keeps aliases inbound-only. It must not gain aliases.

## Task 1: Add the complete behavioral RED suite before production edits

**Files:**

- Modify: `cmd/serf-hub/frontend/src/shell/workspace.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/sessionPlacement.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/DockHost.test.tsx`

**Interfaces:**

- This task adds tests against the intended public store operation `replacePrimary(type, params, identity)` even though the production declaration does not exist yet.
- Existing test helpers remain the boundaries: `resetWorkspaceStoreForTests`, the real registered pane modules, the existing `FakeClient`, the existing tree fetch fixture, and the real DockHost/StackHost hosts.

- [ ] **Step 1: Read the test-specific guidance and identify the break each test catches.**

  Before editing, re-read `docs/testing.md` and `skills/test-driven-development/writing-good-tests.md`. Record these break mappings in test comments where the surrounding file already documents its fixture:

  - Settings → rail top-level session → Settings catches a rail activation that opens a pane without changing the canonical URL and leaves the next same-path Settings navigation unhandled.
  - Saved session layout → `/settings` catches DockHost restoring Settings additively to secondary.
  - Saved session layout → `/new` catches the same restore drift for Spawn.
  - Top-level A → B catches a replacement that leaves A or A’s secondary panes in the workspace.
  - Reselecting A catches a replacement operation that clears secondary panes even when the primary identity is unchanged.
  - Nested child with unrelated main catches owner promotion that only moves the child or uses the child as main.
  - Successful creation catches `/new` route navigation that does not replace Spawn in the shared store.

- [ ] **Step 2: Add the store RED tests with hand-derived state expectations.**

  In `workspace.test.ts`, add a `describe("replacePrimary", () => {` block using the real test descriptors and literal expected records. Cover the following tests:

  ```ts
  test("replaces main and clears every secondary pane when the primary identity changes", () => {
    const workspace = workspaceStore.getState();
    workspace.openPane("doc", { ref: "secondary-a" });
    workspace.openPane("doc", { ref: "secondary-b" });

    const sessionId = workspace.replacePrimary("session", { ref: "local:session-b" }, "local:session-b");

    expect(workspaceStore.getState().panes).toEqual([
      { id: sessionId, type: "session", params: { ref: "local:session-b" }, slot: "main" },
    ]);
    expect(workspaceStore.getState().focusedPaneId).toBe(sessionId);
  });

  test("preserves secondary panes when the requested primary identity is already main", () => {
    const workspace = workspaceStore.getState();
    const mainId = workspace.replacePrimary("session", { ref: "local:session-a" }, "local:session-a");
    const secondaryId = workspace.openPane("doc", { ref: "secondary" });

    const repeatedId = workspace.replacePrimary("session", { ref: "local:session-a" }, "local:session-a");

    expect(repeatedId).toBe(mainId);
    expect(workspaceStore.getState().panes).toEqual([
      { id: mainId, type: "session", params: { ref: "local:session-a" }, slot: "main" },
      { id: secondaryId, type: "doc", params: { ref: "secondary" }, slot: "secondary" },
    ]);
  });

  test("updates a singleton settings section in place while preserving secondary panes", () => {
    const workspace = workspaceStore.getState();
    const settingsId = workspace.replacePrimary("settings", { section: "general" }, "settings");
    const secondaryId = workspace.openPane("doc", { ref: "secondary" });

    const updatedId = workspace.replacePrimary("settings", { section: "credentials" }, "settings");

    expect(updatedId).toBe(settingsId);
    expect(workspaceStore.getState().panes).toEqual([
      { id: settingsId, type: "settings", params: { section: "credentials" }, slot: "main" },
      { id: secondaryId, type: "doc", params: { ref: "secondary" }, slot: "secondary" },
    ]);
  });

  test("notifies subscribers once for a primary replacement", () => {
    const snapshots: Array<ReadonlyArray<OpenPaneRecord>> = [];
    const unsubscribe = workspaceStore.subscribe((state) => snapshots.push(state.panes));

    workspaceStore.getState().replacePrimary("spawn", {}, "spawn");

    unsubscribe();
    expect(snapshots).toHaveLength(1);
    expect(snapshots[0]).toEqual(workspaceStore.getState().panes);
  });
  ```

  Use the existing `OpenPaneRecord` import or a local structural type already used by the test file; do not assert serialized Dockview JSON.

- [ ] **Step 3: Add route and rail RED tests without changing production code.**

  In `AppShell.test.tsx`, add real rendered tests for:

  1. Start at `/settings`, load the existing `TREE_RESPONSE_WITH_NESTED_SESSION`, click the real rail row for a top-level session, assert `window.location.pathname === "/s/local%3As1"` and that the sole main pane is `local:s1`, then click the real rail Settings button, assert `window.location.pathname === "/settings"`, and assert Settings is main with no Settings secondary pane.
  2. Open `/s/local:session-a`, add a real secondary doc through the workspace store, dispatch the same-tab route change to `/s/local:session-b`, and assert A and the secondary doc are gone and B is the only main pane.
  3. Open `/s/local:session-a`, add a secondary doc, dispatch the same route a second time, and assert the main id and secondary id remain unchanged.
  4. Start with Settings main and a secondary doc, navigate to `/new`, and assert Spawn is the only main/secondary-free pane and the real Spawn button is rendered.
  5. Start with an unrelated session main and a secondary doc, resolve a tree where `local:child` belongs to `local:owner`, route to `/s/local:child`, and assert owner is main, child is secondary, the unrelated pane and its secondary are gone, and child is focused.

  The route changes must use the existing `navigate`/`popstate` seam rather than sleeps. Use the existing `waitFor` only for a state or rendered result that is driven by a real fetch or React commit.

  In `Rail.test.tsx`, add a focused rendered activation test if the AppShell test cannot reliably identify the rail’s top-level row. It must click a real `[role="treeitem"]` and assert the URL, not call a production handler directly.

- [ ] **Step 4: Add restore and creation RED tests.**

  In `DockHost.test.tsx` or `AppShell.test.tsx`, use the existing real DockHost save/restore setup to create a saved layout containing a real session main and a real secondary doc, reset the in-memory store, mount at `/settings`, and assert Settings is the sole main pane with no secondary panes. Repeat at `/new` and assert Spawn is the sole main pane. The test must inspect `workspaceStore.getState().panes`, not the serialized layout’s exact text.

  Add a successful creation test to the existing real Spawn/AppShell harness in `AppShell.test.tsx`. Begin at `/new`, ensure Spawn is main, use the existing scripted `startThread` response that returns `local:created`, submit through the real Spawn control, wait for the canonical `/s/local%3Acreated` URL, and assert the created session is main, Spawn is absent, and no old secondary remains.

- [ ] **Step 5: Run the focused tests and prove RED before any production edit.**

  Run from `cmd/serf-hub/frontend`:

  ```sh
  npx vitest run src/shell/workspace.test.ts src/shell/AppShell.test.tsx src/shell/rail/Rail.test.tsx src/shell/sessionPlacement.test.ts src/shell/DockHost.test.tsx src/panes/spawn/Spawn.test.tsx --no-file-parallelism
  ```

  Expected result: the new tests fail because `replacePrimary` is missing and the existing route/restore paths still use direct/additive placement. Resolve only test syntax or fixture errors; do not alter production code in this task. Record the failing test names and the expected missing-operation or wrong-slot assertions in the implementation report.

- [ ] **Step 6: Commit the tests-only RED checkpoint.**

  ```sh
  git status --short
  git add cmd/serf-hub/frontend/src/shell/workspace.test.ts cmd/serf-hub/frontend/src/shell/AppShell.test.tsx cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx cmd/serf-hub/frontend/src/shell/sessionPlacement.test.ts cmd/serf-hub/frontend/src/shell/DockHost.test.tsx
  git commit -m "test: pin primary navigation placement regressions"
  ```

  Stage only files that actually changed.

## Task 2: Implement the atomic store operation and route-owned placement

**Files:**

- Modify: `cmd/serf-hub/frontend/src/shell/workspace.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/sessionPlacement.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/workspace.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/sessionPlacement.test.ts`
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx` only when the RED test needs a fixture-only correction.

**Interfaces:**

- Add to `WorkspaceStoreState`:

  ```ts
  replacePrimary(type: "settings" | "spawn" | "session", params: unknown, identity: string): string;
  ```

- Export stable constants from `workspace.ts`:

  ```ts
  export const SETTINGS_PRIMARY_ID = "settings";
  export const SPAWN_PRIMARY_ID = "spawn";
  ```

  A session’s identity is its canonical ref, such as `local:session-a`; do not add ancestry or owner data to `OpenPaneRecord`.

- `openTopLevelSession(ref)` remains the contextual helper used by existing non-route transcript actions, but delegates directly to `replacePrimary("session", { ref }, ref)` instead of manually closing panes. `openNestedSessionWithOwner(ref, ownerRef)` accepts a resolved non-null owner ref, replaces the owner through the same operation, then opens the child with `{ slot: "secondary" }`.

- [ ] **Step 1: Implement `replacePrimary` as one store transition.**

  In `workspace.ts`, validate the requested registered pane descriptor, locate the current main pane, and compare primary identity without storing lineage:

  ```ts
  function primaryMatches(pane: OpenPaneRecord | null, type: PrimaryPaneType, identity: string): boolean {
    if (pane?.type !== type) return false;
    if (type === "settings") return identity === SETTINGS_PRIMARY_ID;
    if (type === "spawn") return identity === SPAWN_PRIMARY_ID;
    const ref = (pane.params as { ref?: unknown }).ref;
    return typeof ref === "string" && ref === identity;
  }
  ```

  For a matching main pane, update its params when `sameParams` changes, remove only duplicate panes with the same requested primary identity, focus the main id, and call Zustand `set` at most once. For a different main identity, allocate one id and call `set` once with exactly one `{ slot: "main" }` requested pane, dropping the old main and all secondary panes. The returned id must be the existing main id for a match and the new id for a replacement. Do not implement this by calling `closePane` and `openPane`; those calls would expose an intermediate state to DockHost and persistence.

- [ ] **Step 2: Run the store and placement tests to GREEN.**

  ```sh
  npx vitest run src/shell/workspace.test.ts src/shell/sessionPlacement.test.ts --no-file-parallelism
  ```

  Expected result: the new store identity, clearing, preservation, singleton-section, and one-notification tests pass; existing generic `openPane` and restore tests remain green. If a test fails, change the implementation, not the expected contract.

- [ ] **Step 3: Route all primary AppShell branches through the operation.**

  In `AppShell.tsx`, remove `openSettingsInMain` and call the store operation with the stable identity:

  ```ts
  if (route.type === "settings") {
    workspaceStore.getState().replacePrimary("settings", route.params, SETTINGS_PRIMARY_ID);
    return;
  }
  if (route.type === "spawn") {
    workspaceStore.getState().replacePrimary("spawn", route.params, SPAWN_PRIMARY_ID);
    return;
  }
  ```

  Keep Welcome’s existing fallback behavior. For a session route, continue waiting for a successful tree refresh before deciding ownership; a top-level ref calls `openTopLevelSession`, and a nested ref calls `openNestedSessionWithOwner(ref, ancestorRef)`. This preserves lineage outside the store and ensures the owner is main before the child is opened secondary.

- [ ] **Step 4: Make Rail route-only for session activation.**

  In `Rail.tsx`, remove the direct session-placement imports, `mainPaneRef` subscription, and tree-arrival repair effect. Keep tree shaping and row behavior. Replace `openSession` with canonical URL navigation:

  ```ts
  function openSession(session: ApiTreeNode): void {
    const url = paneToURL("session", { ref: session.ref });
    if (url !== null) navigate(url);
  }
  ```

  Import `paneToURL` beside `navigate`. Do not change the existing `/new` or `/settings` buttons; they already navigate and AppShell will now enforce their placement. Do not make `navigate` dispatch for a same-path request and do not add URL aliases.

- [ ] **Step 5: Update contextual session placement to use the same operation.**

  In `sessionPlacement.ts`, make top-level promotion a single `replacePrimary` call. For nested placement, call `replacePrimary("session", { ref: ownerRef }, ownerRef)` and then `openPane("session", { ref }, { slot: "secondary" })`. The second call is intentionally additive because the child is contextual and must not become main. Remove close-first duplication from callers; the operation owns replacement atomically.

- [ ] **Step 6: Run all focused navigation tests to GREEN.**

  ```sh
  npx vitest run src/shell/workspace.test.ts src/shell/AppShell.test.tsx src/shell/rail/Rail.test.tsx src/shell/sessionPlacement.test.ts --no-file-parallelism
  ```

  Confirm the RED cases now pass and the output contains no unhandled errors or React warnings. Inspect `git diff --check` and run `npx biome format --write` only on changed TypeScript/TSX files when formatting is needed.

- [ ] **Step 7: Commit the atomic operation and route ownership changes.**

  ```sh
  git status --short
  git add cmd/serf-hub/frontend/src/shell/workspace.ts cmd/serf-hub/frontend/src/shell/sessionPlacement.ts cmd/serf-hub/frontend/src/shell/AppShell.tsx cmd/serf-hub/frontend/src/shell/rail/Rail.tsx cmd/serf-hub/frontend/src/shell/workspace.test.ts cmd/serf-hub/frontend/src/shell/sessionPlacement.test.ts cmd/serf-hub/frontend/src/shell/AppShell.test.tsx cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx
  git commit -m "fix: centralize primary navigation placement"
  ```

  Stage only changed files.

## Task 3: Apply captured restore intent through the same policy and finish route/create coverage

**Files:**

- Modify: `cmd/serf-hub/frontend/src/shell/DockHost.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/DockHost.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/AppShell.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/shell/mobile/StackHost.test.tsx`

**Interfaces:**

- DockHost receives the existing pre-mount routed panes from AppShell; it must partition them into one captured primary intent and any captured secondary panes.
- The primary intent is reapplied with `replacePrimary`; only captured secondary panes use additive `openPane` after the primary operation.
- No layout schema or URL schema changes are introduced.

- [ ] **Step 1: Change DockHost’s restore merge to capture route intent by slot.**

  In `handleReady`, capture non-Welcome routed panes before `restoreLayout`. Preserve the current type/params values, but identify the captured main pane separately from captured secondary panes:

  ```ts
  const routed = workspaceStore.getState().panes.filter((pane) => pane.type !== "welcome");
  const routedPrimary = routed.find((pane) => pane.slot === "main");
  const routedSecondary = routed.filter((pane) => pane.slot === "secondary");
  ```

  Restore the saved layout first. Then, if `routedPrimary` is Settings, Spawn, or Session, call the matching `replacePrimary` operation with `SETTINGS_PRIMARY_ID`, `SPAWN_PRIMARY_ID`, or the session ref. After that operation has established the main owner, reopen each `routedSecondary` with `openPane(type, params, { slot: "secondary" })`. Do not call generic `openPane` for `routedPrimary`. Keep `keepExistingFocus` only where it remains meaningful for captured secondary restoration; nested route restoration must leave the routed child focused after it is reopened.

  When the route was a nested session but tree data had not arrived before DockHost ready, the captured child may temporarily be the primary route intent. AppShell’s existing tree-dependent route effect will later resolve the owner and call `openNestedSessionWithOwner`, which replaces that temporary primary and reopens the child secondary. This preserves the existing deferred tree contract without adding lineage to workspace state.

- [ ] **Step 2: Run restore tests and prove the saved-layout regressions GREEN.**

  ```sh
  npx vitest run src/shell/DockHost.test.tsx src/shell/AppShell.test.tsx --no-file-parallelism
  ```

  Confirm the `/settings` and `/new` reload tests show exactly one main pane and no secondary panes, while existing generic saved-layout restoration, main-header hiding, secondary close affordance, and root welcome fallback tests remain green.

- [ ] **Step 3: Verify successful creation replaces Spawn through the real route flow.**

  Run the new creation test in its actual file:

  ```sh
  npx vitest run src/shell/AppShell.test.tsx --no-file-parallelism
  ```

  The assertion must observe the route, `workspaceStore.getState().mainPane()`, absence of a Spawn pane, and absence of old secondary panes after the real `startThread` response. Do not add a second direct store call inside Spawn just to satisfy the test; AppShell’s route listener owns the replacement.

- [ ] **Step 4: Verify mobile and Back/Forward behavior on the shared state.**

  In `StackHost.test.tsx`, add one deterministic shared-store observation: seed Settings plus a secondary doc, call the same route-owned replacement operation used by AppShell for `/s/local%3Asession-a`, render StackHost, and assert the focused session is rendered full-screen while the secondary state was cleared. In `AppShell.test.tsx`, exercise browser history with the existing `pushState` plus `popstate` test seam for Back and Forward; assert route-driven replacement rather than a direct helper call. Do not add sleeps or a second mobile store.

  ```sh
  npx vitest run src/shell/mobile/StackHost.test.tsx src/shell/AppShell.test.tsx --no-file-parallelism
  ```

- [ ] **Step 5: Run the complete required verification suite.**

  From `cmd/serf-hub/frontend`, run each command separately and record its exit code and relevant summary:

  ```sh
  npm test
  npm run typecheck
  npm run lint
  npm run build
  npm run layoutguard
  npm run overflowguard
  npm run spawnguard
  ```

  `spawnguard` is applicable because Spawn’s route placement is covered. If a command is unavailable or blocked by missing dependencies/browser tooling, report the exact command and error; do not claim a pass or install dependencies into the source checkout.

- [ ] **Step 6: Format and inspect the final diff.**

  Use Biome only for changed TS/TSX files:

  ```sh
  npx biome format --write src/shell/workspace.ts src/shell/workspace.test.ts src/shell/sessionPlacement.ts src/shell/sessionPlacement.test.ts src/shell/AppShell.tsx src/shell/AppShell.test.tsx src/shell/rail/Rail.tsx src/shell/rail/Rail.test.tsx src/shell/DockHost.tsx src/shell/DockHost.test.tsx src/shell/mobile/StackHost.test.tsx
  git diff --check
  git diff --stat
  git diff -- cmd/serf-hub/frontend/src/shell/workspace.ts cmd/serf-hub/frontend/src/shell/AppShell.tsx cmd/serf-hub/frontend/src/shell/DockHost.tsx cmd/serf-hub/frontend/src/shell/rail/Rail.tsx cmd/serf-hub/frontend/src/shell/sessionPlacement.ts
  git status --short
  ```

  Re-run focused tests after any formatter change. The worktree must contain only the intended tracked changes before the final commit.

- [ ] **Step 7: Commit restore and final behavioral coverage.**

  ```sh
  git status --short
  git add cmd/serf-hub/frontend/src/shell/DockHost.tsx cmd/serf-hub/frontend/src/shell/DockHost.test.tsx cmd/serf-hub/frontend/src/shell/AppShell.test.tsx cmd/serf-hub/frontend/src/shell/mobile/StackHost.test.tsx
  git commit -m "fix: restore primary routes through workspace policy"
  ```

  Omit files that did not change. Do not push, merge, close the kata, or modify another worktree.

## Self-review against the approved spec

- [x] Primary Settings, Spawn, and top-level session identities are explicit and use one atomic store operation.
- [x] A changed primary clears the old main and all secondaries; a matching primary preserves secondaries and updates singleton Settings params.
- [x] Nested routes resolve their top-level owner from the authoritative tree in AppShell, make that owner main, and place the child secondary.
- [x] `/new` is route-owned as main and successful creation re-enters the same route policy for the created top-level session.
- [x] Rail session activation updates the canonical URL and no longer owns session placement.
- [x] DockHost restore applies captured primary intent through `replacePrimary`, then reopens captured secondary intent.
- [x] Existing Back/Forward `popstate` handling remains route-driven and receives the same primary/nested policy.
- [x] Mobile StackHost continues reading the same `workspaceStore`; no mobile-only store or pane behavior is added.
- [x] Existing main-header hiding and no-main-close behavior remains unchanged.
- [x] Inbound aliases remain exactly where they are in `routing.ts`; no new backward compatibility aliases are introduced.
- [x] No serialized-layout string snapshots or generated-command assertions are added.
- [x] The focused RED suite is created and run before production edits; GREEN, full frontend tests, typecheck, lint, build, layoutguard, overflowguard, and spawnguard are required before completion.

## Risks and verification limits

- The Linear connector is unavailable in this environment, so the required ticket discovery/state transition cannot be performed; no issue will be created.
- Dockview restore tests use the repository’s existing real harness and structured `workspaceStore` state; they do not assert the exact serialized layout payload.
- Nested deep links can initially be ambiguous until `/api/tree` resolves. The plan preserves the existing pending-ref behavior and verifies the eventual owner promotion, rather than adding a guessed parent.

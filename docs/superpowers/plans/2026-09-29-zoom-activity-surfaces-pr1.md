# Zoom Activity Surfaces PR 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the first two zoom levels of the activity surfaces - the status bar (glance) and the activity sidebar (triage) - plus the motion foundation the whole system animates with.

**Architecture:** A scope is a session ref; the scope path and counts are derived from the navigation store (no duplicated activity state). The status bar is shell chrome under the workspace; the sidebar is a right shell region beside DockHost (not a dockview panel). Drilling a subagent opens its transcript pane split right (`slot: "secondary"`); focusing it re-scopes both surfaces. Motion comes from the `motion` package behind a single wrapper module, budgeted by a new spatial token.

**Tech Stack:** React 19, zustand (vanilla stores + useStore), dockview (existing workspace), `motion` (new dep), vitest, Biome.

**Spec:** `docs/superpowers/specs/2026-09-29-zoom-activity-surfaces-design.md`

## Global Constraints

- Every color is a `var(--*)` token from `src/styles/tokens.css`; no hex/rgb/hsl literals outside that file (token-contract.test.ts enforces).
- Motion durations/easings come from `--motion-*` tokens; no ms literals in components. No idle motion; motion only answers "where did it go" after a user action.
- `prefers-reduced-motion: reduce` collapses all new motion to instant, via the one MotionProvider.
- Components never import `motion` directly - only `src/motion` does.
- Tests are deterministic: FakeClient at the wire boundary, no network, no provider credentials, no wall-clock sleeps (fake timers or injected clocks).
- Match surrounding file style; no `noNonNullAssertion` violations; Biome clean via `cd cmd/evener-hub/frontend && npx biome check --write <paths>` before gates.
- Gates before the PR: `make test-web`, `make vet`, `make lint` from the repo root.
- Copy follows the design system: sentence case, plain verbs, errors say what happened.

## Review Focus

Five input classes the spec implies but a happy-path test would miss, most dangerous first:

1. **Focused pane is not a session** (settings/doc/welcome focused, or nothing open): bar and sidebar must keep the last session scope and never blank or crash mid-read. Pinned in Task 2 (derivation) and Task 3 (component).
2. **Leaf summary not loaded in the nav store** (a deep subagent of an unloaded project): the path degrades to the leaf's own title only, counts render what the summary carries, no throw. Pinned in Task 2.
3. **Watch rows omitted by hub caps** (`omitted_watches`/`omitted_armed_watches` set): the chip and the tab header report `+N more · M armed total`, never an understated count. Pinned in Tasks 2 and 5.
4. **Session with no task list**: the tasks chip hides entirely and the Tasks tab shows an empty state with an Open affordance, not a blank panel. Pinned in Tasks 3 and 6.
5. **Reduced motion**: sidebar slide and any future spatial transitions collapse to instant with identical final layout. Pinned in Task 1 (wrapper) and Task 4 (sidebar).

---

### Task 1: Motion foundation

The `motion` package behind one wrapper module, the spatial token, the import boundary, and the design-system philosophy section.

**Files:**
- Create: `cmd/evener-hub/frontend/src/motion/index.tsx`
- Create: `cmd/evener-hub/frontend/src/motion/motion.test.tsx`
- Create: `cmd/evener-hub/frontend/src/motion/import-boundary.test.ts`
- Modify: `cmd/evener-hub/frontend/package.json` (dependencies)
- Modify: `cmd/evener-hub/frontend/src/styles/tokens.css:208-220` (motion block)
- Modify: `docs/web-ui/design-system.md:560-587` (§5 Motion budget)

**Interfaces:**
- Produces: `MotionProvider` (React component, wraps children in `MotionConfig reducedMotion="user"`), `m` and `AnimatePresence` re-exports. Later tasks import `{ MotionProvider, m, AnimatePresence }` from `"../motion"` (path relative to importer).
- Produces: token `--motion-duration-spatial: 240ms`.

- [ ] **Step 1: Install the dependency**

From `cmd/evener-hub/frontend`: `npm install motion@^12 --save-exact=false`. Confirm `node_modules/motion` exists and `package-lock.json` updated. (Fresh worktree: if `node_modules` is absent, `npm ci` first, then the install. Never run `npm ci` through a symlinked `node_modules`; check `[ -L node_modules ]` first.)

- [ ] **Step 2: Write the failing wrapper test**

`src/motion/motion.test.tsx`:

```tsx
import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { AnimatePresence, m, MotionProvider } from "./index";

describe("MotionProvider", () => {
  test("renders children and exposes the wrapped primitives", () => {
    const { container } = render(
      <MotionProvider>
        <AnimatePresence>
          <m.div data-testid="subject" initial={{ opacity: 0 }} animate={{ opacity: 1 }} />
        </AnimatePresence>
      </MotionProvider>,
    );
    expect(container.querySelector('[data-testid="subject"]')).not.toBeNull();
  });
});
```

Run: `npx vitest run src/motion/motion.test.tsx` from `cmd/evener-hub/frontend`. Expected: FAIL (module does not exist).

- [ ] **Step 3: The wrapper module**

`src/motion/index.tsx`:

```tsx
// The one module that imports the motion library. Components animate through
// these re-exports, never through "motion/react" directly, so the library,
// its configuration, and the reduced-motion contract each live in exactly
// one place (import-boundary.test.ts enforces the boundary).
//
// reducedMotion="user": a prefers-reduced-motion system collapses every
// animation to its end state - the layout still changes, only the
// interpolation is gone. That is the design system's spatial-motion rule and
// it is not per-component opt-in.
import { AnimatePresence, m, MotionConfig } from "motion/react";
import type { ReactNode } from "react";

export { AnimatePresence, m };

export function MotionProvider({ children }: { children: ReactNode }) {
  return <MotionConfig reducedMotion="user">{children}</MotionConfig>;
}
```

Run the test. Expected: PASS.

- [ ] **Step 4: The spatial token**

In `src/styles/tokens.css`, inside the motion block after `--motion-duration-hover`:

```css
  --motion-duration-spatial: 240ms; /* user-initiated geometry changes: sidebar slide, zoom columns */
```

Also extend the block's comment: add "Spatial transitions (240ms) are user-initiated geometry changes only; data arriving never animates geometry."

- [ ] **Step 5: The import boundary test**

`src/motion/import-boundary.test.ts`, modeled on `src/styles/token-contract.test.ts`'s file-scanning approach: read every `.ts`/`.tsx` under `src/` (skip `node_modules`, test files included), fail if any file other than `src/motion/index.tsx` contains `from "motion` or `from 'motion`. Implementation uses `node:fs`/`node:path` recursive walk, following token-contract.test.ts's walker.

Run: `npx vitest run src/motion/import-boundary.test.ts`. Expected: PASS (only the wrapper imports the library).

- [ ] **Step 6: Mount MotionProvider at the shell root**

In `src/shell/AppShell.tsx`, wrap the top-level returned tree in `<MotionProvider>` (innermost, inside `ClientProvider`, around the layout div). Import from `../motion`.

- [ ] **Step 7: Design-system §5 addition**

In `docs/web-ui/design-system.md`, at the end of §5 (after the "One approved exception" paragraph), add:

```markdown
### Spatial motion (2026-09-29, zoom activity surfaces)

A fourth budget, `--motion-duration-spatial` (240ms, still `--motion-easing-standard`), for
user-initiated geometry changes: the activity sidebar sliding in, a zoom column opening, a column
collapsing to a spine. Spatial motion answers *where did it go* - a panel that arrives from the
edge it lives on, a column that narrows into the spine it became. Motion that doesn't preserve
spatial continuity is decoration and stays banned. Data arriving (a job finishing, a watch firing)
never animates geometry: state change is what the attention hues are for. Architecture: one library
(`motion`), one wrapper module (`src/motion`), one reduced-motion switch (`MotionProvider`,
`reducedMotion="user"`), durations from tokens, never literals. Components import the wrapper, never
the library.
```

- [ ] **Step 8: Gates and commit**

`npx biome check --write src/motion/ src/shell/AppShell.tsx` then `make test-web` from the repo root (covers typecheck + biome + unit). Commit:

```bash
git add cmd/evener-hub/frontend/package.json cmd/evener-hub/frontend/package-lock.json \
  cmd/evener-hub/frontend/src/motion cmd/evener-hub/frontend/src/styles/tokens.css \
  cmd/evener-hub/frontend/src/shell/AppShell.tsx docs/web-ui/design-system.md
git commit -m "feat(web): motion foundation - wrapped motion package, spatial budget, reduced-motion contract"
```

---

### Task 2: Scope derivation (`statusScope.ts`)

Pure functions turning navigation store state + a leaf ref into the scope path and the four counts. No React.

**Files:**
- Create: `cmd/evener-hub/frontend/src/shell/statusbar/statusScope.ts`
- Create: `cmd/evener-hub/frontend/src/shell/statusbar/statusScope.test.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/rail/railNodes.ts` (export two existing pure functions)
- Create: `cmd/evener-hub/frontend/src/shell/focusedSession.ts`
- Modify: `cmd/evener-hub/frontend/src/shell/AppShell.tsx:162-166` (use the extracted module)

**Interfaces:**
- Consumes: `selectLocation(ref)` from `stores/navigation/selectors` (existing; returns a resource whose data is `NavigationSessionLocation` carrying `top_level_ref` and `session`).
- Consumes from railNodes: `activeWatchCount` (already exported; add `subagentIsCurrent` to exports).
- Produces:
  ```ts
  export type ActivityTab = "agents" | "jobs" | "watches" | "tasks";
  export interface ScopeCrumb { ref: string; title: string }
  export interface ScopeCounts { activeSubagents: number; runningJobs: number; armedWatches: number; tasksDone: number; tasksTotal: number }
  export interface ActivityScope { leaf: NavigationSessionSummary; path: ScopeCrumb[]; counts: ScopeCounts }
  export function deriveScope(navigation: NavigationState, leafRef: string): ActivityScope | null
  ```
- Produces: `focusedActivityScopeRef(): string | null` in `shell/focusedSession.ts` - like AppShell's `focusedSessionRef` but also accepts a focused `transcript` pane whose params carry a session `ref` (not `job:` refs).

- [ ] **Step 1: Export the rail's pure predicates**

In `railNodes.ts`: add `export` to `subagentIsCurrent`. (`activeWatchCount` is already exported.) Nothing else changes.

- [ ] **Step 2: Failing tests for derivation**

`statusScope.test.ts` builds a minimal navigation state: one project resource whose session tree is root A with children B (active, 1 running job, 1 armed watch, tasks 2/5), C (idle), plus 201 `more_subagents`, and an unrelated root D. Tests:

```ts
// deriveScope for leaf B:
// - path is [A, B] with titles and refs
// - counts: activeSubagents counts B's own current children (0), runningJobs 1, armedWatches 1, tasks 2/5
// deriveScope for leaf A: counts.activeSubagents is 1 (B current, C folds)
// deriveScope for an unloaded ref: null
// deriveScope where the location's session is present but the top-level root's
//   tree doesn't contain the ref (partially loaded): path is [B] (leaf only)
// counts never include more_subagents or folded-inactive children
```

The store state shape for tests: construct the plain object the selectors read (`selectLocation` reads `state.resources`); follow `stores/navigation/selectors.test.ts` fixtures for the exact resource-map shape.

Run: `npx vitest run src/shell/statusbar/statusScope.test.ts`. Expected: FAIL (module missing).

- [ ] **Step 3: `statusScope.ts`**

```ts
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { activeWatchCount, subagentIsCurrent } from "../rail/railNodes";
import { selectLocation } from "../../stores/navigation/selectors";
import type { NavigationState } from "../../stores/navigation/store"; // the state type selectors consume

export type ActivityTab = "agents" | "jobs" | "watches" | "tasks";
export interface ScopeCrumb { ref: string; title: string }
export interface ScopeCounts {
  activeSubagents: number;
  runningJobs: number;
  armedWatches: number;
  tasksDone: number;
  tasksTotal: number;
}
export interface ActivityScope {
  leaf: NavigationSessionSummary;
  path: ScopeCrumb[];
  counts: ScopeCounts;
}

// Counts report active work only: current subagents (the rail's own
// predicate), running jobs, armed watches including the hub-omitted rows,
// and the task summary. The completed/inactive folds never inflate a chip.
export function scopeCounts(session: NavigationSessionSummary): ScopeCounts {
  return {
    activeSubagents: (session.children ?? []).filter(subagentIsCurrent).length,
    runningJobs: session.running_jobs?.length ?? 0,
    armedWatches: activeWatchCount(session),
    tasksDone: session.tasks?.done ?? 0,
    tasksTotal: session.tasks?.total ?? 0,
  };
}

// The title path from the root that contains `leafRef` down to it, walking
// the root's loaded children. A tree that doesn't contain the ref (partially
// loaded project) degrades to the leaf alone rather than throwing.
export function scopePath(navigation: NavigationState, leafRef: string): ScopeCrumb[] | null {
  const location = selectLocation(leafRef)(navigation)?.data;
  if (!location?.session) return null;
  const leaf = location.session;
  if (location.top_level || location.top_level_ref === leafRef) {
    return [{ ref: leaf.ref, title: leaf.title }];
  }
  const root = selectLocation(location.top_level_ref)(navigation)?.data?.session;
  const trail: ScopeCrumb[] = [];
  const walk = (node: NavigationSessionSummary, ancestors: ScopeCrumb[]): ScopeCrumb[] | null => {
    const here = [...ancestors, { ref: node.ref, title: node.title }];
    if (node.ref === leafRef) return here;
    for (const child of node.children ?? []) {
      const found = walk(child, here);
      if (found) return found;
    }
    return null;
  };
  const full = root ? walk(root, []) : null;
  return full ?? [{ ref: leaf.ref, title: leaf.title }];
}

export function deriveScope(navigation: NavigationState, leafRef: string): ActivityScope | null {
  const path = scopePath(navigation, leafRef);
  if (!path) return null;
  const leaf = selectLocation(leafRef)(navigation)?.data?.session;
  if (!leaf) return null;
  return { leaf, path, counts: scopeCounts(leaf) };
}
```

(If `NavigationState` isn't exported from the store module, use the parameter type of `selectLocation` via `Parameters<typeof selectLocation>[0]`... it isn't: `selectLocation(ref)` is curried. Use `Parameters<ReturnType<typeof selectLocation>>[0]`. Confirm against `selectors.ts` and use whatever that module's selectors accept, named exactly as they name it.)

- [ ] **Step 4: `focusedSession.ts`**

Move AppShell.tsx's `focusedSessionRef` (lines ~162-166) into `src/shell/focusedSession.ts` exported, and add:

```ts
// The scope the activity surfaces describe: the focused session pane's ref,
// or the session ref of a focused transcript pane (a drilled subagent
// transcript re-scopes the bar and sidebar to it - that IS the zoom). Job
// transcripts (job:<id>) keep their parent's scope via parentRef.
export function focusedActivityScopeRef(): string | null;
```

Read the focused pane from `workspaceStore.getState()`: pane type `session` → `params.ref`; pane type `transcript` with `params.ref` not starting `job:` → `params.ref`; `transcript` with a `job:` ref → `params.parentRef`; anything else → the LAST non-null answer (module-level sticky variable, so focusing settings never blanks the surfaces; null only when nothing was ever focused). AppShell's own uses switch to the import.

- [ ] **Step 5: Tests pass + regression run**

`npx vitest run src/shell/statusbar src/shell/focusedSession` (add focusedSession tests for the pane-type matrix incl. the sticky-last behavior). Then `npx vitest run src/shell/rail src/shell/AppShell.test.tsx` to prove the rail/AppShell refactor didn't drift.

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/frontend/src/shell/statusbar cmd/evener-hub/frontend/src/shell/focusedSession.ts \
  cmd/evener-hub/frontend/src/shell/rail/railNodes.ts cmd/evener-hub/frontend/src/shell/AppShell.tsx
git commit -m "feat(web): scope derivation for the activity status surfaces"
```

---

### Task 3: Status bar

The glance surface: breadcrumb + four counters, desktop-only, chips open the sidebar.

**Files:**
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.ts`
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/activitySidebarStore.test.ts`
- Create: `cmd/evener-hub/frontend/src/shell/statusbar/StatusBar.tsx`
- Create: `cmd/evener-hub/frontend/src/shell/statusbar/StatusBar.test.tsx`
- Create: `cmd/evener-hub/frontend/src/shell/statusbar/statusbar.module.css`
- Modify: `cmd/evener-hub/frontend/src/shell/AppShell.tsx` (layout regions)
- Modify: `cmd/evener-hub/frontend/src/shell/AppShell.module.css` (workspace column wrapper)

**Interfaces:**
- Consumes: `deriveScope`, `ActivityTab`, `ScopeCounts` from Task 2; `focusedActivityScopeRef` from Task 2.
- Produces: `activitySidebarStore` with `{ open: boolean; tab: ActivityTab; openWith(tab?: ActivityTab): void; close(): void; setTab(tab: ActivityTab): void }` and `useActivitySidebarStore`; `resetActivitySidebarStoreForTests`.
- Produces: `StatusBar` (no props; reads stores).

- [ ] **Step 1: Sidebar store with failing test**

`activitySidebarStore.ts` - follow `chromeStore.ts`'s exact pattern (zustand vanilla + `useStore` + reset-for-tests). Initial `{ open: false, tab: "agents" }`. `openWith(tab?)` opens and sets tab when given; `close()` closes and keeps the tab.

Test: open/close/openWith/setTab transitions and the reset helper. Run, watch fail (missing module), implement, watch pass.

- [ ] **Step 2: StatusBar test-first**

`StatusBar.test.tsx` renders `<StatusBar />` against a navigation store seeded like Task 2's fixture (root A focused as a session pane via `workspaceStore`), with the sidebar store reset. Assert:

- crumb shows "A"; chips read `⌘1`, `$1`, `◉1`, `☑2/5`.
- clicking the `⌘` chip calls `activitySidebarStore.getState().open` → true with `tab === "agents"`.
- focusing a transcript pane for B (workspaceStore.openPane("transcript", { ref: B, parentRef: A }, { slot: "secondary" })) re-scopes: crumbs "A › B".
- no task list on the leaf → no `☑` chip.
- leaf with `omitted_armed_watches: 1` and one armed watch → chip reads `◉2`.

The nav-store seeding: reuse the fixture pattern from `stores/navigation/selectors.test.ts` (whatever shape `selectLocation` reads).

Run, watch fail.

- [ ] **Step 3: StatusBar.tsx**

```tsx
// The glance surface of the zoom system: the scope breadcrumb (where you're
// reading) and four counters (what it's doing), always on, desktop only.
// Chips escalate to the sidebar preselected to the matching tab.
import { Chevron } from "../../widgets";
import { activitySidebarStore, useActivitySidebarStore } from "../activitybar/activitySidebarStore";
import { focusedActivityScopeRef } from "../focusedSession";
import { useNavigationStore } from "../../stores/navigation/store";
import { deriveScope } from "./statusScope";
import styles from "./statusbar.module.css";

export function StatusBar() {
  const navigation = useNavigationStore();
  const sidebar = useActivitySidebarStore();
  // Re-derive on navigation changes; the focused ref is read imperatively
  // because workspace focus already re-renders this subtree on pane changes.
  const ref = focusedActivityScopeRef();
  const scope = ref ? deriveScope(navigation, ref) : null;
  // ...render: crumbs (buttons calling openSessionByRef), then chips
  // ⌘ counts.activeSubagents | $ runningJobs | ◉ armedWatches | ☑ done/total (hidden when tasksTotal === 0)
  // chip onClick: activitySidebarStore.getState().openWith(tab)
}
```

(The component subscribes to the workspace store too - `useWorkspaceStore((s) => s.focusedPaneId)` - so focusing a drilled transcript re-renders it. Verify against workspace.ts's exported `useWorkspaceStore`.)

CSS `statusbar.module.css`: 30px strip, `var(--surface-canvas)`, top border `var(--edge)`, tokens only.

- [ ] **Step 4: AppShell wiring**

In AppShell.tsx around line 941-948: wrap the desktop `<DockRegion />` in a vertical flex column with `<StatusBar />` beneath it, and mount `<ActivitySidebarRegion />` (Task 4's component; for this task mount a placeholder `null`-rendering import is NOT acceptable - instead wire StatusBar only in this task, sidebar mounts in Task 4):

```tsx
) : (
  <div className={styles.workspaceColumn}>
    <DockRegion />
    <StatusBar />
  </div>
)}
```

`workspaceColumn` in AppShell.module.css: `display: flex; flex-direction: column; flex: 1; min-width: 0; min-height: 0;` with DockRegion keeping `flex: 1; min-height: 0;`.

- [ ] **Step 5: Tests pass + AppShell regression**

`npx vitest run src/shell/statusbar src/shell/activitybar src/shell/AppShell.test.tsx`. Then biome the touched files.

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/frontend/src/shell/statusbar cmd/evener-hub/frontend/src/shell/activitybar \
  cmd/evener-hub/frontend/src/shell/AppShell.tsx cmd/evener-hub/frontend/src/shell/AppShell.module.css
git commit -m "feat(web): activity status bar - glance surface of the zoom system"
```

---

### Task 4: Activity sidebar skeleton + Agents tab with drill

The triage surface opens/closes with spatial motion; the Agents tab drills.

**Files:**
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.tsx`
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/AgentsTab.tsx`
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/activityRows.tsx` (shared row components)
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/activitybar.module.css`
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/ActivitySidebar.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/shell/AppShell.tsx` (mount the region)
- Modify: `cmd/evener-hub/frontend/src/shell/rail/RailRow.tsx` (export WatchGlyph is already exported - no change; verify only)

**Interfaces:**
- Consumes: `activitySidebarStore`, `deriveScope`, `focusedActivityScopeRef`, `subagentIsCurrent`; `openPane` from `shell/workspace.ts` via the store's `getState()`.
- Produces: `ActivitySidebar` (no props); row components `AgentRow`, `JobRow`, `WatchRow` in activityRows.tsx with props `(session: NavigationSessionSummary, ...)`-based signatures as coded below.

- [ ] **Step 1: Shared rows, test-first**

`activityRows.tsx`: the row grammar from the prototype, ported to wire data:

- `AgentRow({ sub, onDrill })`: `⌘` glyph toned by state (active → `var(--alive)`, awaiting → `var(--attention)`, failed → `var(--danger)`, else ink-low), title, state text ("working"/"waiting on you"/"idle"/"failed"), rollup line from `sub.subagents` tally + `running_jobs` + `activeWatchCount(sub)` + `tasks`, and `last_message`-style detail absent for subagents (wire carries none - omit).
- `JobRow({ job })`: `$` glyph via `jobStatusDotState(job.status, terminal)`, command in `--font-mono`, `jobStatusDisplay(job.status, job.reason)`.
- `WatchRow({ watch, now })`: `WatchGlyph` (from `shell/rail/RailRow.tsx`, already exported), `watchName(watch)`, `watchMeta(watch, now)`.

`now` comes from a tiny `useNow(30_000)` interval hook in the sidebar (no shared rail clock dependency).

- [ ] **Step 2: Sidebar test-first**

`ActivitySidebar.test.tsx`: with the store open on "agents" and the Task 2 fixture focused, assert: breadcrumb renders the path; the segmented control shows four tabs with counts; Agents tab lists current children (B) and folds C behind "Inactive subagents (1)" plus a passive `+201 more` note from `more_subagents`; clicking B's row calls `openPane("transcript", { ref: B.ref, parentRef: A.ref }, { slot: "secondary" })` (spy on `workspaceStore`). Sidebar closed → renders nothing. Also assert the aside carries the motion wrapper class and `prefers-reduced-motion` test: with the media query mocked reduce, mounting is instant (no animation timer pending - assert final geometry immediately present).

Run, watch fail.

- [ ] **Step 3: ActivitySidebar.tsx**

Structure (all class names from `activitybar.module.css`, tokens only):

```tsx
import { AnimatePresence, m } from "../../motion";
// ...
export function ActivitySidebar() {
  const open = useActivitySidebarStore((s) => s.open);
  const navigation = useNavigationStore();
  const ref = focusedActivityScopeRef();
  const scope = ref ? deriveScope(navigation, ref) : null;
  return (
    <AnimatePresence initial={false}>
      {open && scope ? (
        <m.aside
          className={styles.sidebar}
          initial={{ x: 320, opacity: 0 }}
          animate={{ x: 0, opacity: 1 }}
          exit={{ x: 320, opacity: 0 }}
          transition={{ duration: "var(--motion-duration-spatial)" /* see note */ }}
        >
          {/* breadcrumb (crumb buttons call openSessionByRef), SegmentedControl tabs,
              tab body: AgentsTab | JobsTab | WatchesTab | TasksTab, close button */}
        </m.aside>
      ) : null}
    </AnimatePresence>
  );
}
```

NOTE on the duration: the motion library takes seconds or a spring, not a CSS var. The wrapper's job is to hide that: add to `src/motion/index.tsx` an exported `SPATIAL` transition constant reading the token once (`getComputedStyle(document.documentElement).getPropertyValue("--motion-duration-spatial")` parsed to ms/1000, fallback 0.24), so components write `transition={SPATIAL}` and the token stays the single source. Update Task 1's wrapper accordingly when implementing (this is the same task sequence, so just build it in Task 1 with this shape; Task 1's test should cover SPATIAL parsing).

- [ ] **Step 4: AgentsTab.tsx**

```tsx
export function AgentsTab({ scope }: { scope: ActivityScope }) {
  const children = scope.leaf.children ?? [];
  const current = children.filter(subagentIsCurrent);
  const inactive = children.filter((c) => !subagentIsCurrent(c));
  const more = scope.leaf.more_subagents ?? 0;
  // current rows: AgentRow with onDrill={() => workspaceStore.getState().openPane(
  //   "transcript", { ref: sub.ref, parentRef: scope.leaf.ref }, { slot: "secondary" })}
  // inactive fold: Disclosure-style local state, "Inactive subagents (N)" where
  //   N = inactive.length + more; pages locally 20 at a time over `inactive`;
  //   a passive "+N more" line (no control) when more > 0 - the wire does not
  //   fetch subagent pages (railNodes' OverflowRailNode.passive precedent).
}
```

- [ ] **Step 5: Mount + AppShell regression**

Mount `<ActivitySidebar />` in AppShell as the right sibling of the workspace column (desktop branch only). Run `npx vitest run src/shell` and biome the new files.

- [ ] **Step 6: Commit**

```bash
git add cmd/evener-hub/frontend/src/shell/activitybar cmd/evener-hub/frontend/src/shell/AppShell.tsx \
  cmd/evener-hub/frontend/src/motion
git commit -m "feat(web): activity sidebar - triage surface with agents drill"
```

---

### Task 5: Jobs and Watches tabs

**Files:**
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/JobsTab.tsx`
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/WatchesTab.tsx`
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/JobsWatchesTabs.test.tsx`

**Interfaces:**
- Consumes: `JobRow`, `WatchRow` from activityRows (Task 4); `deriveScope` output.
- Produces: `JobsTab({ scope })`, `WatchesTab({ scope, now })`.

- [ ] **Step 1: Tests first**

Jobs tab: running jobs first then completed; a `command_exited_nonzero` renders its `jobStatusDisplay` text and the danger glyph; clicking a job calls `openPane("transcript", { ref: \`job:${job.job_id}\`, parentRef: scope.leaf.ref }, { slot: "secondary" })`.

Watches tab: armed and fired rows render `watchMeta` wording; header shows `+2 more · 1 armed total` when `omitted_watches: 2, omitted_armed_watches: 1` (reuse railNodes' `watchCountLabel` for the exact grammar).

Run, watch fail.

- [ ] **Step 2: Implement both tabs** - straightforward mapping over `scope.leaf.running_jobs`, `scope.leaf.completed_jobs`, `scope.leaf.watches` with the shared rows; no local state beyond the test's.

- [ ] **Step 3: Run + biome + commit**

`npx vitest run src/shell/activitybar`, biome, commit `feat(web): sidebar jobs and watches tabs`.

---

### Task 6: Tasks tab + the SessionChrome affordance + gates

**Files:**
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/TasksTab.tsx`
- Create: `cmd/evener-hub/frontend/src/shell/activitybar/TasksTab.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.tsx` (desktop Activity button opens the sidebar instead of the Sheet)

**Interfaces:**
- Produces: `TasksTab({ scope })`.

**Spec deviation, deliberate:** the spec said the Tasks tab embeds `TasksPanelBody`. That component needs the session's full `ThreadModel`, which means subscribing to the thread for any scoped session - wrong weight for a glance surface. Instead: the tab shows the nav summary (`tasks.done/total`, current task name) plus an "Open tasks" button that opens the existing `sessionTasks` pane (`openPane("sessionTasks", { ref }, { slot: "secondary" })`). Full embedding can follow if review wants it.

- [ ] **Step 1: Tests first** - summary line (`2 of 5 done · now: <current>`), empty state when no task list ("No task list for this session." + Open affordance hidden), Open button calls openPane("sessionTasks", ...).

- [ ] **Step 2: Implement TasksTab.**

- [ ] **Step 3: Retarget the desktop Activity affordance** - in SessionChrome.tsx, the desktop replacement button that today opens the ActivityPanel sheet via `activityRef` now calls `activitySidebarStore.getState().openWith()`; the hidden panel stays (it owns background refresh) - only the button's onClick changes. The mobile Sheet path is untouched. Update the SessionChrome tests that assert sheet-opening (`SessionChrome.test.tsx` / `SessionChrome.edge.test.tsx`) to assert the store transition instead.

- [ ] **Step 4: Full gates** - from repo root: `make test-web`, `make vet`, `make lint`. Fix what fails; do not weaken checks.

- [ ] **Step 5: Commit** - `feat(web): sidebar tasks tab; desktop activity affordance opens the sidebar`.

---

## After the plan

1. `/simplify-code:simplify-code` on the branch diff.
2. Open the PR (base `main`, title `feat(web): zoom activity surfaces, phase 1 - status bar + activity sidebar`), body summarizing the spec + the prototype link.
3. `/shepherd-pr:shepherd-pr` through CI + roborev.

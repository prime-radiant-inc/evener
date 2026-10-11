# Zoom activity surfaces - design

2026-09-29. Status: draft for review.

## Intent

The rail currently nests every subagent, job, and watch under its session row, three grouping
modes deep; the docked activity sheet covers one session at a time and doesn't feel right. Jesse
reviewed nine throwaway prototypes (branch `activity-ux-prototypes`, harness at
`cmd/evener-hub/frontend/activityux.html`) and picked the **Zoom**: one system at three depths of
commitment, each one click from the last. This spec is the production build of that prototype.

The UX contract, from the prototype (section `#zoom`):

- **Glance - the status bar.** A 30px strip under the workspace: scope breadcrumb on the left, four
  counters on the right (agents `⌘`, jobs `$`, watches `◉`, tasks `☑`), scoped to what you're
  reading. Always on.
- **Triage - the sidebar.** A chip click opens a right sidebar preselected to that kind's tab:
  Agents | Jobs | Watches | Tasks, with per-kind renderers, the completed fold, and pagination.
  The sidebar always describes the scope you're reading.
- **Immerse - the cascade.** An agent-row click drills: the subagent's transcript opens beside its
  parent's. Past two readable columns, ancestors collapse to live spines (state dot, vertical name,
  count cluster with a peek popover). Depth 6 renders as 5 spines + 2 columns.

Two verbs, one row vocabulary: popovers *peek* at scopes you're not reading (spines, rail rows);
the sidebar *supervises* the scope you are reading.

## Scope model

A scope is a session ref. The drill stack is a path of refs from a root session down through its
subagent tree, **derived from the navigation store**, not duplicated into new state:

- The navigation manifest already carries each session's children with state, `running_jobs`,
  `completed_jobs`, `watches`, `omitted_watches`, `omitted_armed_watches`, `more_subagents`, and
  `tasks` summaries (see `NavigationSessionSummary`); the rail's `railNodes.ts` already reads all of
  it, including the current/inactive split (`CURRENT_SUBAGENT_STATES`).
- The leaf scope is the focused session's ref (`focusedSessionRef`, currently private to
  AppShell.tsx, becomes shared). One exception, specified in PR 2: when the focused pane is a
  `sessionZoom` pane, the leaf is the pane's current drill leaf, not its root - the pane publishes
  its leaf ref to the shared focus surface so the bar and sidebar keep describing what you're
  reading. The path is found by walking the tree from the root that contains the leaf; breadcrumbs
  are that path's titles.
- Scope change is just pane focus change in PR 1: drilling opens the subagent's transcript pane,
  which when focused re-scopes the bar and sidebar to it. PR 2 adds the zoom pane, where the drill
  stack is the pane's own geometry.

Counts never include the completed fold: chips and counters report active work (active + awaiting
subagents, running jobs, armed watches, tasks done/total). Completed subagents read once, as the
fold's total.

## Phase plan

Two PRs. PR 1 ships glance + triage with drill landing on a plain transcript split (existing
dockview behavior), plus the motion foundation everything uses. PR 2 ships the `sessionZoom` pane
and rewires drill to it.

## PR 1: motion foundation, status bar, activity sidebar

### Motion foundation

- Dependency: `motion` (the framer-motion successor) in `cmd/evener-hub/frontend/package.json`.
- `src/motion/index.tsx`: the only module that imports the package. Exports a `MotionProvider`
  (wraps `MotionConfig` with `reducedMotion="user"` so `prefers-reduced-motion` collapses every
  animation to instant, one place, no exceptions) and the blessed primitives the app uses
  (`m` components, `AnimatePresence`). Components never import `motion` directly; a new
  import-boundary check alongside the existing `lint-package-imports` script enforces it.
- Tokens (`src/styles/tokens.css`, the motion block): `--motion-duration-spatial: 240ms` for
  user-initiated geometry changes (sidebar slide, column open/collapse in PR 2). The existing three
  budgets and the "no idle motion" law are unchanged.
- `docs/web-ui/design-system.md` §5 grows a **spatial motion** subsection - philosophy and
  architecture:
  - Motion answers *where did it go*, never *did something happen* (state change is what the
    attention hues are for). A transition that doesn't preserve spatial continuity is decoration
    and is banned.
  - Only user-initiated scope changes produce spatial motion: opening the sidebar, drilling,
    popping back. Data arriving (a job finishing, a watch firing) never animates geometry.
  - One library, one wrapper, one reduced-motion switch; durations and easings come from tokens,
    never literals.
  - `prefers-reduced-motion: reduce` collapses to instant - the layout still changes, only the
    interpolation is gone.

### Status bar (`src/shell/statusbar/`)

New shell chrome under the workspace region (right of the rail, bottom of the main column), desktop
only - mobile keeps the existing per-session Sheet.

- `StatusBar.tsx`: scope breadcrumb (path titles; clicking a crumb focuses that session's pane)
  plus the four counters. Reads the navigation store via selectors; subscribes narrowly.
- `statusScope.ts`: pure functions deriving the scope path and counts from the nav manifest +
  resources for a leaf ref (unit-testable without React).
- Chip click opens the sidebar preselected to that kind's tab. Counters with no items render but
  stay quiet (ink-low); the tasks chip hides when the scope has no task list.

### Activity sidebar (`src/shell/activitybar/`)

A right shell region beside DockHost, sibling to the rail: not a dockview panel, so it persists
across pane switches and can't be dragged into the tab system. Open/tab state lives in a small
store (`activitySidebarStore.ts`); `chromeStore.ts` stays untouched.

- `ActivitySidebar.tsx`: breadcrumb + `SegmentedControl` tabs + per-kind body, all scoped to the
  leaf. Close button returns to bar-only.
- Agents tab: the leaf's current children (state glyph, name, rollup line, detail) plus an
  "Inactive subagents"/completed fold with the true total (`more_subagents`-aware) and the same
  paginate-20 behavior as the prototype, fetching further pages via the existing navigation loaders.
  Row click drills (opens the transcript pane split right, `slot: "secondary"`).
- Jobs tab: running then completed jobs (command, status, age, intent). Row click opens the job's
  transcript pane (`ref: job:<id>`, `parentRef`) - the existing job-log view.
- Watches tab: the leaf's watches (name, cadence meta via the shared `watchMeta`/`watchName`),
  honest `+N more · M armed` when the hub omitted rows.
- Tasks tab: embeds the real `TasksPanelBody` for the leaf ref (already store-backed and
  refcounted), not a reimplementation.

### PR 1 files

New: `src/motion/index.tsx`, `src/shell/statusbar/{StatusBar.tsx,statusScope.ts,*.test.ts}`,
`src/shell/activitybar/{ActivitySidebar.tsx,activitySidebarStore.ts,rows.tsx,*.test.tsx}`,
`src/motion/motion.test.tsx`. Modified: `tokens.css`, `design-system.md` §5, `AppShell.tsx`
(layout regions + shared `focusedSessionRef`), `package.json`, import-boundary lint.

### PR 1 acceptance

- Bar visible under the workspace on desktop, correct counts against a FakeClient fixture with 8
  active subagents, 203 completed (folded), chains 4 deep.
- Chip → sidebar opens on the matching tab with a 240ms spatial slide; Esc/close returns to
  bar-only. Reduced motion: instant, same layout.
- Agents tab drill opens the subagent's transcript split right; focusing it re-scopes bar +
  sidebar (crumbs show the path).
- `make test-web`, `make lint`, `make vet` green; reduced-motion and token-contract tests included.

## PR 2: the `sessionZoom` pane (cascade)

- New pane type `sessionZoom` (`src/panes/zoom/`): params `{ ref }` (the root session). Renders
  the column stack for the drill path: spines for ancestors beyond the two rightmost scopes,
  readable columns for parent + leaf, each column embedding the real transcript view for its ref
  (the same store-backed, remount-safe components the transcript pane uses; durable state stays in
  the refcounted stores, per DockHost's remount contract).
- Columns are read-only views of a subagent's transcript plus a header (name, state) and an "Open"
  affordance that opens the full session pane. Composing/steering stays in the session pane -
  editing inside a column is a non-goal for PR 2.
- Drill rewiring: in the zoom pane, agent rows (sidebar scoped to the leaf, or spine peeks) push
  onto the stack instead of opening a floating split; breadcrumb/spine clicks pop. Drilling from
  outside the zoom pane keeps PR 1 behavior (plain transcript split) - the zoom pane is opt-in per
  root session via a pane action ("Open as zoom").
- Motion choreography: new column slides in from the right at `--motion-duration-spatial`; a column
  collapsing to a spine animates width to the spine width; spine expansion reverses it. Implemented
  with the wrapped library's layout transitions inside the zoom pane only.
- Geometry rule: exactly two readable columns; ancestors collapse in arrival order. Minimum column
  widths from the prototype (400px parent, flex leaf); past that the stack scrolls horizontally.

### PR 2 acceptance

- Depth-6 drill renders 5 spines + 2 columns; spine peek popovers list that scope's activity and
  drill from it (truncating and regrowing the stack).
- Column open/collapse/spine transitions animate at the spatial budget and are instant under
  reduced motion; layout persists across reloads through the existing workspace layout JSON.
- Same gates green.

## Non-goals

- Mobile: the bar, sidebar, and zoom pane are desktop shell chrome. The mobile host keeps the
  existing Activity sheet.
- Cross-session supervision in the bar: the bar shows the focused session's tree. (A fleet-wide
  view was prototyped as the board/drawer and not picked.)
- Editing or composing inside zoom columns.
- Changing what the rail renders (activity rows stay there for now; removal is a separate decision
  once the zoom earns it).

## Data limits honored

- `more_subagents` / `omitted_descendants`: folds show true totals, pages fetch through the
  existing loaders; no counter ever invents a total the wire didn't carry.
- `omitted_watches` / `omitted_armed_watches`: watch headers and chips report "+N more · M armed
  total" exactly as the rail does.
- Children caps: the Agents tab shows what the wire carries and says so when capped.

## Risks

- **Embedding transcripts N-deep.** The transcript view must mount several times in one pane.
  Mitigation: the stores are already refcounted per ref and DockHost's contract forces
  remount-safety; PR 2's first task is a two-column embedding spike with tests before any chrome
  work.
- **Sidebar as shell region vs dockview.** A custom region duplicates a little layout code but keeps
  the sidebar out of the tab system, which is the point. If dockview later grows a pinned region,
  migrate then.
- **Focus semantics.** Bar/sidebar scoping keys off focused-pane identity; weird focus states (a doc
  pane focused) must keep the last session scope rather than blanking. Specified in statusScope
  tests.

## Testing strategy

Unit: scope derivation, count aggregation, fold/pagination math, store transitions. Component:
StatusBar and ActivitySidebar against FakeClient fixtures at prototype scale; zoom pane column
stack in PR 2. Motion: wrapper honors reduced-motion; tokens contract covers the new token. Gates:
`make test-web` (unit + typecheck + biome), `make test-web-browser` where Chrome allows,
`make merge-approval-gate` before merge.

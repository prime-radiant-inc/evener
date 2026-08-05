# Session panels as togglable panes — design

Date: 2026-08-04 (v3, revised after adversarial review round 2)
Status: approved direction, under revision review

## Goal

The session pane's three auxiliary panels — **Tasks**, **Activity**, and
**Details** — currently open as `Sheet` slide-overs from buttons in
`SessionChrome` (the status row below the composer). On desktop they instead
open as real panes in the dockview pane manager, and the buttons below the
composer toggle those panes on and off. On mobile the existing slide-over
behavior stays.

Locked user decisions (unchanged from v1):

- **Mobile:** hybrid — desktop toggles dockview panes, mobile keeps the sheets.
- **Toggle-off:** a true toggle — clicking the button while its pane is open
  closes the pane, even when that pane is focused.
- **Naming:** the activity panel keeps its current "Activity" label.

## Revision history

v2 addresses the fifteen verified findings from adversarial review round 1.
The material changes relative to v1:

1. **Open-time focus is now designed explicitly** (v1 said nothing, and the
   AppShell route effect would have reverted focus to the main pane, making
   the first toggle click appear to do nothing). See §3 and §4.
2. **Durable panel state moves into stores keyed by session ref** — v1's
   claim that pane remount "matches close/reopen sheet semantics" was false:
   dockview unmounts inactive tabs, while the sheets never unmount their
   panels' state. See §5.
3. **The Activity badge's data source is preserved** by extracting the
   fetch-while-closed summary into a store, instead of letting it die with
   the (usually closed) panel pane. See §6.
4. **Palette session scope** extends to the new panel panes. See §7.
5. **Pre-hydration render states** are specified for all three panes. See §8.
6. **A11y obligations** replace v1's silence. See §9.
7. **Toggle mechanics** are specified against the real store API (close needs
   a pane id; `sameParams` is module-private). See §3.
8. Testing obligations expanded (§10); the unreachable "toggle with session
   pane closed" edge case was corrected to the reachable restore case (§11).

v3 addresses the fourteen verified findings from adversarial review round 2
(two reviewers, 7 legitimate findings each, overlapping on three):

1. **§4 now covers nested-session routes and asides.** v2 extended only the
   top-level branch of `routePlacementIsApplied`, which would have left panel
   toggles dead for subagent sessions (the exact v1 bug v2 claimed to fix).
   The focus condition is generalized: a focused panel pane never
   invalidates route placement. (A2#1, B2#1)
2. **§6 preserves the badge's real freshness gates.** v2's "same trigger as
   today" was false: today's background fetch is gated on the panel having
   been opened at least once and suppressed while the chrome is collapsed.
   The summary store reproduces both gates, and the open panel body
   publishes into it so one fetch serves badge and panel. (A2#2, B2#4)
3. **Palette `/tasks` and `/status` dispatch `togglePane` directly** on
   desktop instead of clicking a DOM-global selector — v2 kept a mechanism
   that no-ops when the chrome is collapsed (which opening a panel itself
   causes) or targets the wrong session's trigger in restored layouts.
   (A2#4, B2#2)
4. **§9's focus-into-pane fires only on toggle-open**, not on every dockview
   remount, via a consumed pending-focus marker. (A2#5)
5. **The §5/§6 stores get an eviction policy** tied to the threads store's
   model eviction. (A2#6, B2#3)
6. **The route effect's trigger is described correctly** (pane-list, route,
   and tree changes — not focus-only changes). (A2#3, B2#5)
7. **Desktop→mobile breakpoint crossing** behavior is specified (§9, §11).
   (A2#7)
8. The dockview drag-into-main-group trap is acknowledged (§11, B2#6), and
   §10's test plan closes the nested-route, palette-dispatch, eviction,
   popout-removal, and browser-guard gaps (B2#7).

## Current state (verified against the code)

- `panes/session/chrome/SessionChrome.tsx` renders `DetailsPanel`,
  `TasksPanel`, and `ActivityPanel`. Each is a trigger `Button` plus a
  `Sheet`; the panel components stay mounted while their sheet is closed, so
  their component-local state survives close/reopen. Below a measured width
  (640 px, `NARROW_CHROME_WIDTH_PX`) the triggers collapse into the "⋯"
  `SessionActionsMenu`, which opens the same sheets via imperative handles.
- The pane manager is `shell/workspace.ts` (`openPane`/`closePane`, main +
  secondary slots, dedupe by type + deep-equal params via the module-private
  `sameParams`). `openPane` focuses a newly created pane; `closePane` of the
  focused pane sets `focusedPaneId: null` (no fallback in the store — focus
  is re-derived afterward by AppShell's route effect on routed sessions).
- `shell/AppShell.tsx`'s route effect re-runs on pane-list, route, and
  `/api/tree` changes (NOT on focus-only changes: `focusPane` mutates only
  `focusedPaneId`, leaving the `panes` array identity intact, so a native
  tab click never re-runs it) and, for a top-level session route, considers
  placement "applied" only when `focusedPaneId === main.id`
  (`routePlacementIsApplied`). A newly opened panel pane takes focus on open,
  so opening one re-runs the effect (the pane list changed), fails the
  predicate, and gets reverted: `openRouteAsPane` re-focuses the main pane.
  (Empirically reproduced in review round 1.) The nested-session branch has
  its own focus requirement (`focusedPaneId` must be the subagent's session
  pane), so it needs the same treatment — see §4.
- Pane types self-register via `registerPane` (eager registration module +
  lazy component; `panes/transcript` is the precedent). `AppShell.tsx`
  imports every registration module eagerly so restored dockview layouts
  resolve their types; `routing.ts`'s `paneToURL` returns `null` for
  non-routed types, enforced by an exhaustive switch.
- `shell/DockHost.tsx` documents (live-probe confirmed) that dockview
  UNMOUNTS a panel's React tree when its tab is inactive; durable state must
  live in stores keyed by the pane's params.
- `shell/palette/paletteContext.ts` derives `sessionRef` only when the
  focused pane's type is exactly `"session"`; `commandsInScope` filters out
  all session-scoped commands otherwise.
- The palette's `/tasks` command synthesizes a click on `data-tasks-trigger`
  and no-ops when that element isn't rendered (today: when the chrome is
  collapsed — a pre-existing, accepted trade-off, kata vybn).
- `useIsMobile()` (viewport < 900 px) is the same predicate AppShell uses to
  pick StackHost vs DockHost, so branching panel behavior on it cannot
  disagree with host selection.
- The Activity trigger badge (`Activity · N`) is computed from the fetched
  activity tree. While the sheet is closed a background fetch on
  `model.jobsUpdatedAt` bumps keeps it fresh — but only under two gates
  (`ActivityPanel.tsx:437-452`, pinned by `ActivityPanel.test.tsx`): the
  panel must have been opened at least once (a `fetchedBumpRef`-based
  "established" gate — a never-opened panel shows no count even during live
  job activity), and the fetch is suppressed while the trigger is hidden
  (`hideTrigger`, i.e. the chrome is collapsed into the "⋯" menu). The Tasks
  badge (`Tasks N/M`) comes from `model.tasks` and needs no fetch.

## Design

### 1. New pane types

Three new `PaneTypeId`s: `sessionTasks`, `sessionActivity`, `sessionDetails`,
each with params `{ ref: string }`.

- One eager registration module (`panes/sessionPanels/index.ts`) registers
  all three; each pane component is a lazy chunk. `AppShell.tsx` imports the
  registration module alongside doc/transcript so layout restore resolves
  them.
- `paneToURL` returns `null` for the new types (compiler-enforced by the
  exhaustive switch): contextual surfaces, not deep-linkable.
- Tab titles: `Tasks · <session-name>`, `Activity · <session-name>`,
  `Details · <session-name>` via `PaneTitleCtx.threadName`, raw-ref fallback.
- Not singletons; per-session dedupe comes from the store's same-params rule.
- Placement: secondary slot only (`openPane(..., { slot: "secondary" })`).

### 2. Content extraction

Each existing panel splits into a **body** (rendering + interaction) with two
hosts: the existing **Sheet host** (mobile) and a new **pane host** (desktop,
`PaneScaffold` + body). Crucially, the bodies do NOT own durable state — see
§5. The pane hosts claim `ensureThread(ref)` on mount and
`releaseThread(ref)` on unmount, with the same connection-readiness deferral
`Session.tsx` uses, so a panel pane keeps its thread hydrated even when the
session pane is closed (reachable via layout restore — see §11). The Details
pane runs its own `useNowTick` instead of receiving `now` from SessionChrome.

Fetch behavior: Tasks and Activity bodies fetch on mount and re-fetch on the
same push signals as today (`model.tasks` / `model.jobsUpdatedAt` changes
while mounted). Because durable state is store-backed (§5), a remount
re-renders the retained content immediately and freshens it in the
background — strictly better than the sheet's first-open loading state, and
no worse on subsequent ones.

### 3. Toggle mechanics

- `workspace.ts` gains a store method `togglePane(type, params)`: if an open
  pane matches type + `sameParams`, `closePane(id)`; otherwise open it in the
  secondary slot and focus it. Implementing this inside the store keeps
  `sameParams` private and gives the close path the pane id it needs. An
  exported `isPaneOpen(state, type, params)` selector backs the buttons'
  `aria-pressed`.
- **Opening focuses the panel.** A toggle click must produce a visible
  change; opening a background tab is indistinguishable from nothing
  happening (and would make the second click an invisible close). This
  requires §4's route-effect change to stick.
- Closing the focused panel nulls store focus; AppShell's route effect then
  re-focuses the main session pane, exactly as after a native tab close.
- **Hidden-but-open case:** with focus-on-open, a panel is hidden only after
  the user manually activates another tab in the group. The locked decision
  stands: the button still closes it. The `aria-pressed` state (and, when
  collapsed, the menu adornment in §9) keeps that state visible; this matches
  how dockview tab close buttons behave on background tabs.

### 4. Route-effect awareness

`routePlacementIsApplied` (`AppShell.tsx`) learns that a focused panel pane
never invalidates route placement — panel panes are auxiliary surfaces, so
focusing one is always compatible with any session route whose required
pane structure is present. Concretely:

- **Top-level session route** (`/s/{ref}`): the existing structural checks
  stay (main is the routed session, exactly one session pane for the ref),
  and the focus requirement becomes: `focusedPaneId === main.id` OR the
  focused pane is a `sessionTasks`/`sessionActivity`/`sessionDetails` pane
  (of any ref — this covers asides: a child session opened beside the
  parent without navigation, whose chrome toggles a panel for the child's
  ref while the route stays on the parent).
- **Nested-session route** (`/s/{childRef}`): the existing structural
  checks stay (owner in main, child session in secondary), and the focus
  requirement becomes: `focusedPaneId === childPanes[0].id` OR the focused
  pane is a panel pane. Without this, toggling a subagent's panel would
  revert focus to the subagent's session pane and the panel would open as
  an invisible background tab — the v1 bug surviving for a first-class
  session arrangement.
- Settings/spawn routes need no change: panel panes can't exist there
  (`replacePrimary` on a primary mismatch replaces the whole pane list, so
  no session chrome is ever mounted beside them), and the welcome route
  already returns applied unconditionally.

`openRouteAsPane` is unchanged.

### 5. Durable state lives in stores keyed by ref

Dockview unmounts inactive tabs, and even the active panel remounts whenever
the user switches secondary tabs — a state-loss event with no sheet analog.
Following the codebase's own rule (`DockHost.tsx`: durable state belongs in a
store keyed by the pane's params) and the existing precedent
(`widgets/disclosure`'s store, already keyed by session+task id):

- **Tasks:** a `tasksPanelStore` keyed by ref holds the fetched rows, the
  daemon-gone terminal flag, and the last load failure. The retained-list
  behaviors pinned today (rows survive a failed refresh; daemon-gone never
  wipes a retained list) become store semantics and therefore survive
  unmount. Row disclosure is already store-backed.
- **Activity:** an `activityPanelStore` keyed by ref holds what
  `ActivityPanel`'s reducer owns today: the tree (including
  continuation-grafted older branches), expansion, inspector selection, and
  continuation failures. Remount restores the reader's exact place; the
  mount fetch reconciles via the existing `reconcileActivityState`.
- **Details:** stateless beyond the model and its own tick — nothing to lift.

**Eviction:** the threads store evicts a ref's model when its last claim
releases; leaving panel-store entries behind would retain the largest
per-ref client payloads (full grafted activity trees) for the page's
lifetime. Each panel store evicts its entry for a ref when the threads
store evicts that ref's model — retention is therefore bounded by the claim
graph: entries live exactly as long as something (session pane, panel pane,
restored layout) holds the thread. The disclosure store's page-lifetime
booleans remain the outlier, as today.

Sheet hosts read the same stores, so mobile behavior is unchanged (and the
mobile sheet's close-unmounts-children behavior stops being load-bearing).

### 6. The Activity badge keeps its data source — and its exact behavior

The badge's fetch-while-closed moves out of the panel into an
`activitySummaryStore` keyed by ref, owned by the chrome's Activity trigger
component. The store reproduces today's two gates exactly (changing them
would change wire behavior):

- **Established gate:** no fetch fires for a session whose activity panel
  was never opened; the badge shows the bare "Activity" label until the
  first open, exactly as today.
- **Collapsed suppression:** when the chrome is collapsed and the trigger
  unrendered, there is no owner and no fetch — today's `hideTrigger`
  suppression, unchanged.

**One fetch, two consumers:** while the Activity pane is open, the panel
body's own tree fetch publishes its summary into the store, so a
`jobsUpdatedAt` bump produces ONE `listJobs` round-trip serving both badge
and panel — no double fetch. The badge never fires its own request while
the pane is mounted.

Tasks and Details badges are unaffected (`model.tasks`, no badge
respectively).

### 7. Palette integration

`buildPaletteContext` derives `sessionRef` from a focused panel pane's params
as well as from session panes, so session-scoped commands (`/interrupt`,
`/model`, `/tasks`, …) stay in scope while reading a session's panel.

`/tasks` and `/status` change mechanism on desktop: instead of
`clickTrigger("[data-tasks-trigger]")` — a DOM-global first-match that
no-ops when the trigger isn't rendered (collapsed chrome, or the session
pane unmounted behind its own focused panel) and can hit the WRONG
session's trigger in restored multi-session layouts — they dispatch
`togglePane("sessionTasks" | "sessionDetails", { ref: ctx.sessionRef })`
directly. This keeps the commands working in every state the design makes
common, collapsed chrome included. On mobile they keep the legacy
synthesized click, which opens (not toggles) the sheet, as today.

### 8. Pre-hydration and terminal states

Each pane host renders, in order: a loading state (`PaneScaffold` +
`EmptyState`, mirroring Session.tsx's "Loading transcript…") until its
`ThreadModel` hydrates; the body once hydrated; and an honest
session-unavailable state if the claim rejects terminally (no connected
client / thread not found), rather than loading forever. This is a new state
for Details specifically, which today has no loading state because
SessionChrome gates on the model. All existing panel body states (loading,
unsupported source, daemon-gone, stale-with-error, retry) carry over,
now backed by the §5 stores.

### 9. Chrome, overflow menu, and a11y

- Buttons branch on `useIsMobile()`: mobile opens the Sheet (unchanged);
  desktop calls `togglePane`. Desktop buttons render `aria-pressed` from
  `isPaneOpen`.
- The narrow-width "⋯" overflow items route identically. Since `MenuItem`
  has no checked/pressed field, an open panel's overflow item carries a
  visible adornment in its label (e.g. "Tasks ✓") so the open state survives
  collapse. Opening a panel shrinks the session pane and can itself trigger
  the collapse — the adornment keeps the toggle state legible in exactly
  that situation.
- **A11y obligations** (replacing what the Sheet provided): on toggle-open —
  and ONLY on toggle-open — DOM focus moves into the pane. Mechanism:
  `togglePane`'s open path records a pending-focus marker for the new pane
  id; the pane host consumes it on mount and focuses its scaffold region.
  This is required because dockview remounts a pane on every tab
  re-activation, so a naive focus-on-mount would yank focus on every tab
  switch back to the panel and on boot restore. `PaneScaffold` gains a
  focusable region (`tabIndex={-1}` on its content wrapper) for this — it
  has none today. `aria-pressed` announces the state change from the
  button. There is deliberately no Escape-to-close (dockview panes are
  persistent surfaces, not dialogs; keyboard close paths are the toggle
  button again and the tab's close affordance). Focus on close falls back
  with the route effect's refocus of the main pane.
- **Breakpoint crossing:** the workspace store survives the
  DockHost↔StackHost swap, so a panel pane open at a desktop→mobile
  crossing renders full-screen in StackHost (it reads the same §5 stores,
  so it works), with StackHost's own back affordance for non-welcome panes
  providing the return path. The locked mobile decision governs the toggle
  AFFORDANCE (mobile chrome buttons open sheets); an already-open pane
  crossing the breakpoint degrades to a readable full-screen pane rather
  than being destroyed.

### 10. Testing

New obligations beyond v1's list:

- `togglePane`/`isPaneOpen` store tests: open→close→open, dedupe per ref,
  secondary placement, focus-on-open, close-nulls-focus.
- **Route-effect tests**: top-level session route with a focused panel pane
  (same ref AND an aside's different ref) counts as applied; **nested
  session route** with a focused panel pane counts as applied; focusing a
  non-panel pane still reverts on both. (The nested cases are mandatory —
  v2's test plan would have passed with the nested bug in.)
- **Restore round-trip tests** for the three new types through
  `layoutJSON`/`restoreLayout`, including the pre-hydration title fallback.
- **Claim/release refcount tests**: session pane + panel pane claim pairs —
  open panel, close session, close panel, reordered — against the threads
  store's refcounting and model eviction, **plus eviction tests for the
  three panel stores** (entry gone when the model is evicted, retained
  while any claim holds).
- **Palette tests**: `/tasks` and `/status` dispatch `togglePane` with the
  palette-context ref on desktop — including with collapsed chrome and with
  the session pane unmounted behind its focused panel — and keep the legacy
  click on mobile.
- **Popout toggle-off test**: store-driven `closePane` removes a popped-out
  panel through DockHost's reconciliation (the path toggle-off relies on
  when a user popped a panel out).
- **Remount-state pins**: simulated unmount/remount of each panel body
  preserves retained rows / daemon-gone / activity selection+expansion /
  continuation grafts (the behaviors today's suites pin for sheet
  close/reopen); focus does NOT move on re-activation remounts, only after
  a toggle-open.
- **Badge behavior pins**: established gate (no fetch before first open),
  collapsed suppression, single-fetch-while-open.
- SessionChrome tests: desktop toggle vs mobile sheet (stubbed
  `useIsMobile`), `aria-pressed`, overflow adornment.
- Existing panel suites move with the extracted bodies/stores; coverage is
  not reduced.
- Gates: `npx biome check --write` on touched files, `make test-web`, and
  `make test-web-browser` — the collapse interplay (opening a panel shrinks
  the session pane under 640px) is ResizeObserver-driven and invisible to
  jsdom, so a browser guard for it is REQUIRED, not optional.

### 11. Edge cases

- **Layout restore** of a panel pane (the reachable version of v1's
  mis-stated "toggle with the session pane closed" case — the toggle
  affordances live in the session's own chrome): the pane's own
  `ensureThread` claim re-hydrates the thread; §8 states cover the wait and
  the terminal failure.
- Session ends while a panel pane is open: the existing daemon-gone / ended
  states render from the stores.
- Opening a panel activates its tab, hiding (unmounting) the previously
  active secondary tab — safe by construction once all panes, these three
  included, keep durable state in stores.
- **Desktop→mobile breakpoint crossing:** an open panel pane renders
  full-screen in StackHost (§9); crossing back returns it to its dockview
  group (layout geometry itself doesn't survive the swap, as today).
- **Drag-into-main trap (acknowledged, pre-existing):** dockview lets a
  user drag any secondary tab into the main group, whose tab header
  `syncGroupHeaders` then hides — the dragged panel becomes invisible while
  still "open" (`aria-pressed` stays true). Toggle-off still closes it
  (store-driven removal reconciles regardless of geometry), so it's
  recoverable; this hazard already exists for transcript/doc panes and
  fixing dockview's drop policy is out of scope here.

## Out of scope

- Deep links / routing for the panel panes.
- Any change to what the panels show (content, fields, badge math).
- Removing the `Sheet` widget or its other consumers.
- Escape-to-close or focus-trapping in dockview panes (see §9).
- Dockview layout policy beyond secondary-slot placement.

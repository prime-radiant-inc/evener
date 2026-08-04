# Session panels as togglable panes — design

Date: 2026-08-04 (v2, revised after adversarial review round 1)
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
- `shell/AppShell.tsx`'s route effect re-runs on every workspace change and,
  for a top-level session route, considers placement "applied" only when
  `focusedPaneId === main.id` (`routePlacementIsApplied`). Any other focused
  pane — including a newly opened panel pane — is reverted: the effect calls
  `openRouteAsPane`, which re-focuses the main pane. (Empirically reproduced
  in review round 1.) This is the central interaction the design must handle.
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
  activity tree, kept fresh while the sheet is closed by a background fetch
  on `model.jobsUpdatedAt` bumps (`ActivityPanel.tsx`). The Tasks badge
  (`Tasks N/M`) comes from `model.tasks` and needs no fetch.

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

`routePlacementIsApplied` (`AppShell.tsx`) learns the panel pane types: for a
top-level session route, placement also counts as applied when the focused
pane is a `sessionTasks`/`sessionActivity`/`sessionDetails` pane **whose ref
equals the route's session ref**. Focus reverting to the main pane still
happens for every other case (closing the panel, focusing an unrelated pane).
Nested-session route logic is untouched. This is one predicate edit plus
tests; no change to `openRouteAsPane`.

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

Sheet hosts read the same stores, so mobile behavior is unchanged (and the
mobile sheet's close-unmounts-children behavior stops being load-bearing).

### 6. The Activity badge keeps its data source

The badge's fetch-while-closed moves out of the panel into an
`activitySummaryStore` keyed by ref: the chrome's Activity button owns a
subscription that fetches the summary on `model.jobsUpdatedAt` bumps (the
same trigger as today) and the badge renders `Activity · N` from it. The
panel body continues to fetch the full tree for its own rendering. Tasks and
Details badges are unaffected (`model.tasks`, no badge respectively).

### 7. Palette integration

`buildPaletteContext` derives `sessionRef` from a focused panel pane's params
as well as from session panes, so session-scoped commands (`/interrupt`,
`/model`, `/tasks`, …) stay in scope while reading a session's panel. `/tasks`
itself is unchanged mechanically: it clicks `data-tasks-trigger`, which
toggles the pane on desktop and — as today — opens (not toggles) the sheet on
mobile. The collapsed-chrome case where the trigger isn't rendered remains
the pre-existing accepted trade-off.

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
- **A11y obligations** (replacing what the Sheet provided): on toggle-open,
  DOM focus moves into the pane (the pane host focuses its scaffold region,
  `tabIndex={-1}`, on mount) so keyboard and screen-reader users land in the
  content they just summoned, and `aria-pressed` announces the state change.
  There is deliberately no Escape-to-close (dockview panes are persistent
  surfaces, not dialogs; close is the same button again or the tab's close
  affordance). Focus on close falls back with the route effect's refocus of
  the main pane.

### 10. Testing

New obligations beyond v1's list:

- `togglePane`/`isPaneOpen` store tests: open→close→open, dedupe per ref,
  secondary placement, focus-on-open, close-nulls-focus.
- **Route-effect tests**: a focused panel pane of the routed session counts
  as applied (no focus revert); focusing any other pane still reverts.
- **Restore round-trip tests** for the three new types through
  `layoutJSON`/`restoreLayout`, including the pre-hydration title fallback.
- **Claim/release refcount tests**: session pane + panel pane claim pairs —
  open panel, close session, close panel, reordered — against the threads
  store's refcounting and model eviction.
- **Remount-state pins**: simulated unmount/remount of each panel body
  preserves retained rows / daemon-gone / activity selection+expansion /
  continuation grafts (the behaviors today's suites pin for sheet
  close/reopen).
- **Palette-context tests**: session commands stay scoped while a panel pane
  is focused.
- SessionChrome tests: desktop toggle vs mobile sheet (stubbed
  `useIsMobile`), `aria-pressed`, overflow adornment.
- Existing panel suites move with the extracted bodies/stores; coverage is
  not reduced.
- Gates: `npx biome check --write` on touched files, `make test-web`, and
  `make test-web-browser` (the collapse interplay depends on real geometry
  jsdom can't see; check whether a browser guard is warranted).

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

## Out of scope

- Deep links / routing for the panel panes.
- Any change to what the panels show (content, fields, badge math).
- Removing the `Sheet` widget or its other consumers.
- Escape-to-close or focus-trapping in dockview panes (see §9).
- Dockview layout policy beyond secondary-slot placement.

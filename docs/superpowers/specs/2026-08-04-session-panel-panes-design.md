# Session panels as togglable panes — design

Date: 2026-08-04 (v7, revised after adversarial review round 6)
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

v4 addresses the fourteen verified findings from adversarial review round 3
(reviewer A: 6, reviewer B: 8 — B took the round):

1. **§5 eviction keys on open panes, not the claim graph.** A backgrounded
   panel pane is unmounted and holds NO claim, so claim-graph eviction would
   have destroyed its store state while the pane was still open (A3#1) —
   and "bounded by the claim graph" was also inaccurate under mutation
   pinning (B3#8).
2. **§6 gets a real mount signal.** The pane host records its mounted state
   in the summary store; the badge's background fetch is suppressed exactly
   while a mounted body owns freshness, and resumes for open-but-
   backgrounded panes — matching today's closed-sheet freshness (A3#2,
   B3#2). Summary publishing is root-fetches-only and carries the
   `counts.complete` gate (A3#3); the established gate's store lifetime is
   described honestly rather than as exact parity (B3#7).
3. **§8 drops the terminal-rejection state** — the threads store retries
   hydration forever for a dead ref (there is no terminal rejection to
   catch), so a restored pane for a deleted session shows the same loading
   state a restored session pane shows; store-level retry discrimination is
   out of scope (B3#1).
4. **§4's relaxed focus clause applies only to workspace-initiated
   changes** — a fresh navigation or boot deep link still focuses the
   routed pane, preserving today's boot-restore behavior and parity with
   transcript/doc panes (A3#4).
5. **§9's crossing return path is a real one**: the panel panes render the
   existing `BackToParentAction` header action (as transcript/doc panes
   do), because StackHost's back stack resets across host swaps and its
   fall-through rewrites the URL to "/" (B3#3).
6. **§3/§9 acknowledge the aside case**: opening an aside's panel unmounts
   the aside's chrome (same secondary group), so the toggle button and its
   pressed state vanish while the panel is focused; close paths are the
   tab-x and re-activating the session tab (B3#4).
7. **§7 acknowledges that `/tasks` and `/status` become close-capable on
   desktop** (mobile stays open-only), specifies the non-hook mobile
   predicate the dispatch branch needs (A3#6), and documents that a
   palette-initiated toggle-close drops DOM focus to `<body>` (B3#5, B3#6).
8. **§10's popout test uses the fake-api precedent** — jsdom's
   `window.open` is unimplemented, so a real-dockview popout test is not
   writable in the current harness (A3#5).

v5 addresses the twelve verified findings from adversarial review round 4
(reviewer A: 4, reviewer B: 8 — B took the round):

1. **§4's scoping rule is restated against real signals.** v4 claimed the
   route effect "already distinguishes" navigation from workspace changes
   via `openedForPathnameRef` — it does not (the session branch never reads
   it), and the classification was self-contradictory: a deferred boot deep
   link's placement run is triggered by the TREE LANDING, which v4 called
   workspace-initiated, so the boot guarantee failed under v4's own rule
   (A4#1, B4#1). The relaxation now keys on placement state: no pending
   deep link and no route change since the last completed placement.
2. **§6's background fetch reconciles the panel store** — v4 updated only
   the summary store, so a backgrounded pane remounted to stale content
   with a second fetch where today one fetch keeps both badge and body
   fresh (B4#2). The body's mount fetch is also gated on the entry's
   last-fetched bump (B4#3's extra-fetch cell).
3. **The mount signal is recorded by whichever host mounts the body** —
   pane host or sheet host — closing v4's mobile double-fetch hole (B4#3).
4. **The established gate is described as attempt-based**, matching the
   code (`fetchedBumpRef` is set at fetch start, not on success) (B4#4).
5. **§5's "mobile behavior is unchanged" claim is corrected** — store-backed
   state survives mobile Back where component state died today; benign and
   now stated (B4#5).
6. **§10 drops the v3 leftover eviction-test semantics** (A4#2), adds the
   placement-rule tests round 4 proved necessary (B4#6), and removes the
   vacuous store-level popout phrasing (A4#3).
7. **§9/§11 acknowledge the remaining edge cases honestly**: crossed-over
   panels have no close affordance on mobile (B4#7), BackToParentAction now
   carries DOM focus into the parent's scaffold region (B4#8), and the
   orphan-restore focus revert is documented as matching the transcript/doc
   precedent (A4#4).

v6 addresses the six verified findings from adversarial review round 5
(a tie: 4 legitimate findings each):

1. **§4 specifies `placedPathname`'s update points** (A5#1): it is written
   BOTH when the applied-predicate evaluates true AND on the
   `routePlacementInProgressRef` guard run that confirms an effect-driven
   placement — v5 named the signal but not its writes, and the guard
   swallows exactly the run a natural implementation would set it on,
   leaving the relaxation permanently off.
2. **§9 gives BackToParentAction's focus move a mechanism** (A5#2, B5#1):
   it records a pending-focus marker for the parent pane (consumed on mount
   when the parent must be created) AND imperatively focuses the scaffold
   region when the parent is already mounted — the mount-consumed marker
   alone never fires for an already-mounted main pane.
3. **StackHost's top-bar Back is suppressed for panel panes** (B5#2) — it
   falls through to welcome and rewrites the URL after a crossing, and v5
   left it visible beside BackToParentAction.
4. **§2 and §6 agree on the mount fetch** (B5#3): Tasks fetches on mount;
   Activity's mount fetch follows §6's gate.
5. **§6's gate matches `needsVisibleFetch` exactly** (A5#3, B5#4): bump
   mismatch OR a retained non-ready load state (idle/failed/unsupported/
   ended) refetches on mount.
6. **§5's eviction rule explicitly covers all three stores** — tasks,
   activity, and the §6 summary store (A5#4).

v7 addresses the eight verified findings from adversarial review round 6
(reviewer A: 4, reviewer B: 5 — B took the round):

1. **In-flight fetches publish into the store regardless of mountedness**
   (A6#3, B6#1) — the `mountedRef` completion-drop pattern must NOT be
   carried into the store-backed bodies; combined with the attempt-based
   bump and the exact `needsVisibleFetch` gate, a dropped completion wedges
   the entry in "loading" with a matching bump and no refetch path.
2. **§4's guard-run write is qualified** (A6#1): the guard records the
   pathname it was armed for, and a guard run only writes `placedPathname`
   when it clears for that same pathname — a guard surviving into an
   unrelated navigation (reachable via nested routes' focus-only
   placements) confirms nothing about the new route.
3. **§9 names the BackToParentAction path-selection signal** (A6#2): a DOM
   query for the parent's scaffold region — present means mounted
   (imperative focus), absent means unmounted (marker for mount
   consumption). A store-openness check would misfire on mobile, where the
   parent is open-in-store but unmounted.
4. **§9's close-focus claims are corrected** (B6#2, B6#3): the route
   effect's refocus is a STORE-focus fallback that doesn't move DOM focus;
   tab-x close of a focused panel strands DOM focus on `<body>` (accepted,
   same class as the palette-close drop); the "never strands" sentence is
   scoped to the non-orphan paths.
5. **Pending-focus markers are cancelled on pre-mount deactivation**
   (B6#5) — a marker recorded at toggle-open must not survive the pane
   losing its tab activation before the lazy chunk's first mount, or it
   would fire on an ordinary later re-activation, violating the
   only-on-toggle-open rule.
6. **§9's crossing sentence is fixed** (B6#4): a crossed-over panel can be
   CLOSED after crossing back to desktop — nothing auto-closes it; §11's
   "crossing back returns it" stands.
7. **§5 eviction evaluates quiescent state** (A6#4): the check runs
   settled (microtask-debounced), so the multi-set restore/re-apply
   sequence during a host swap can't evict a ref whose panes continuously
   exist.

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

Fetch behavior: the Tasks body fetches on mount (its panel has no freshness
gate today). The Activity body's mount fetch follows §6's gate (bump
mismatch or a non-ready retained load state), and both bodies re-fetch on
the same push signals as today (`model.tasks` / `model.jobsUpdatedAt`
changes while mounted). Because durable state is store-backed (§5), a
remount re-renders the retained content immediately and freshens it in the
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
  collapsed, the menu adornment in §9) keeps that state visible **whenever
  the chrome is mounted** — for a top-level session (the main pane, always
  visible) that's always. The **aside case** is the exception: the aside's
  session pane and its panel share the secondary group, so opening the
  panel unmounts the aside's chrome, and while the panel is focused there
  is no visible pressed state and no toggle button — the close paths are
  the tab's close affordance and re-activating the session tab (which
  remounts the chrome with its pressed state intact, read from
  `isPaneOpen`). This matches how every other secondary-pane surface
  behaves and is accepted.

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

`openRouteAsPane` is unchanged. One scoping rule keeps boot and navigation
behavior intact: the relaxed focus clause applies only when the current
route has ALREADY been fully placed — no deferred deep link
(`pendingSessionRef` is null) and no route change since the last completed
placement. The placement signal is NEW bookkeeping this change adds: a
`placedPathname` ref written at BOTH points where placement is observed
complete — when `routePlacementIsApplied` evaluates true AND on the
`routePlacementInProgressRef` guard run that swallows the re-render an
effect-driven placement just caused — with one qualification: the guard
records the pathname it was armed FOR, and a guard run writes
`placedPathname` only when it clears for that same pathname. (A nested-
route placement can end in a focus-only `openPane` that never re-runs the
effect, leaving the guard armed into an unrelated later run — including a
navigation. Such a run confirms nothing about the new route; treating it as
a confirmation would enable the relaxation where placement never ran. The
swallowed navigation itself is a pre-existing AppShell hazard, untouched
here.) The existing `openedForPathnameRef` is never read in the
session branch and cannot serve, and the tree landing is not a usable
discriminator either — a deferred deep link's placement run is triggered by
the tree arriving, with no pathname change. With the placement-state rule,
a boot deep link over a restored layout and an in-app navigation
(including `/thread/X` ↔ `/s/X`) both focus the routed pane exactly as
today, and panel panes get no privilege over transcript/doc panes at boot.

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
releases — but claims are held by MOUNTED hosts, and dockview unmounts
background tabs, so an open-but-backgrounded panel pane holds nothing.
Eviction keyed on the claim graph would destroy a still-open panel's state.
Instead, each of the three panel stores — tasks, activity, and the §6
activity summary store — subscribes to the workspace store and evicts its
entry for a ref when NO open pane references that ref (no session pane, no
panel pane of any kind) — retention is bounded by what's actually on
screen, and a backgrounded panel's state survives exactly as long as its
pane exists. The check evaluates QUIESCENT state (settled via a microtask,
not synchronously inside each `set`): host-swap restore/re-apply sequences
rewrite the pane list in several consecutive sets, and a synchronous check
would evict a ref whose panes exist before and after the sequence but
vanish inside it. Rail-driven session deletion already closes every pane
for a deleted ref generically, so the cascade still reaches these stores.

Sheet hosts read the same stores. One honest mobile divergence: StackHost
keeps panes in the workspace store across Back navigation, so store-backed
panel state now survives leaving and returning to a session where today's
component state died with the unmounted chrome — returning shows retained
content instead of a fresh loading state. Benign, and consistent with the
desktop behavior.

### 6. The Activity badge keeps its data source — and its freshness

The badge's fetch-while-closed moves out of the panel into an
`activitySummaryStore` keyed by ref, owned by the chrome's Activity trigger
component. The store reproduces today's two gates:

- **Established gate:** no background fetch fires before the first root
  fetch is INITIATED for the ref — today's gate is attempt-based
  (`fetchedBumpRef` is set at fetch start, so even a failed first open
  establishes it), and the store reproduces exactly that. The badge shows
  the bare "Activity" label until a summary exists.
  (The flag lives in the store, so its lifetime is the store entry's, not a
  chrome instance's: a session re-opened after its panel established the
  summary shows the count immediately. That is a deliberate, benign
  divergence from today's component-instance gate — not exact parity, and
  no wire-behavior change.)
- **Collapsed suppression:** when the chrome is collapsed and the trigger
  unrendered, there is no owner and no fetch — today's `hideTrigger`
  suppression, unchanged.

**One fetch at a time, via a mount signal.** Whichever host mounts the body
— the desktop pane host OR the mobile sheet host (the sheet unmounts its
children on close, so an open sheet IS a mounted body) — records the body's
mounted state in the summary store on mount/unmount. While a body is
mounted, it owns freshness: it fetches on `jobsUpdatedAt` bumps and
publishes the summary, and the badge fires nothing — one `listJobs`
round-trip per bump on both platforms. When the pane is open but
backgrounded (body unmounted), the badge's background fetch resumes — and
because that fetch is a root fetch, it also reconciles the §5 panel-store
entry when one exists, so re-activating the panel remounts to FRESH
retained content, exactly as reopening the sheet does today. The body's
mount fetch follows today's `needsVisibleFetch` semantics exactly: it fires
when the entry's last-fetched bump doesn't match `model.jobsUpdatedAt` OR
the retained load state is non-ready (idle / failed / unsupported / ended —
today refetches those on reopen even with no bump), and is skipped
otherwise. **In-flight fetches are store-owned:** a started fetch always
publishes its result into the store, whether or not the body that started
it is still mounted. Today's `mountedRef` completion-drop exists only to
avoid setState-after-unmount, which a store makes moot — carrying it over
would wedge an entry in a loading state with an already-advanced
(attempt-based) bump and no refetch path, since neither gate fires for
`loading` with a matching bump. Publishing happens only
from ROOT fetches — continuation patches carry partial-window counts
(`mergeSession` adopts `patch.counts`), so grafts never overwrite the badge
summary — and the stored summary keeps the `counts.complete` flag the label
gates on.

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
synthesized click, which opens (not toggles) the sheet, as today. The
dispatch branch needs a non-React mobile predicate: `useIsMobile.ts`'s
module-private `isMobileViewport()` gets exported (palette run handlers are
plain functions; a hook call there would be a hooks violation).

Two acknowledged behavior deltas:

- **Close-capable on desktop:** today's palette commands are open-only
  (clicking an open sheet's trigger is a no-op); with `togglePane` they
  close an open pane, including the one being read. This matches the locked
  true-toggle decision and the commands' existing "Toggle" titles; mobile
  stays open-only, a platform difference.
- **Focus on palette-initiated close:** the palette's FocusScope restores
  focus only if its pre-palette element still exists; closing the pane that
  contained it drops DOM focus to `<body>` (dockview's programmatic
  `setActive` does not move DOM focus). Accepted and documented; the
  button-initiated close keeps DOM focus on the button.

### 8. Pre-hydration and dead-session states

Each pane host renders a loading state (`PaneScaffold` + `EmptyState`,
mirroring Session.tsx's "Loading transcript…") until its `ThreadModel`
hydrates, then the body. There is NO terminal session-unavailable state:
the threads store retries hydration indefinitely for any `thread/read`
rejection (a dead ref is indistinguishable from a transport failure at that
layer, and the claim neither resolves nor rejects), so a restored pane for
a deleted session shows the loading state — exactly what a restored SESSION
pane shows for the same ref today (Session.tsx documents the same
contract). Changing the store's retry discrimination is out of scope. This
is a new state for Details specifically, which today has no loading state
because SessionChrome gates on the model. All existing panel body states
(loading, unsupported source, daemon-gone, stale-with-error, retry) carry
over, now backed by the §5 stores.

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
  switch back to the panel and on boot restore. A marker is CANCELLED if
  the pane loses its tab activation before its first mount completes (the
  lazy chunk may still be resolving), so a stale marker can never fire on
  an ordinary later re-activation. `PaneScaffold` gains a
  focusable region (`tabIndex={-1}` on its content wrapper) for this — it
  has none today. `aria-pressed` announces the state change from the
  button. There is deliberately no Escape-to-close (dockview panes are
  persistent surfaces, not dialogs; keyboard close paths are the toggle
  button again and the tab's close affordance). The route effect's
  refocus-on-close is a STORE-focus fallback only — it does not move DOM
  focus (§7), so a tab-x close of a focused panel strands DOM focus on
  `<body>`: accepted, the same class as the palette-close drop (§7);
  button-initiated close keeps DOM focus on the button.
  `BackToParentAction` (below) moves DOM focus into the parent pane's
  scaffold region, by two paths selected by a DOM query for the parent's
  scaffold region — the one signal that means "mounted": present (the
  common desktop case, parent in main) → focus it imperatively after
  activation; absent (parent unmounted, or not yet created — the orphan
  case, and ALWAYS the case on mobile, where StackHost mounts only the
  focused pane) → record a pending-focus marker for the parent pane id,
  consumed on mount. (A store-openness check would misfire here: the
  workspace store knows open, not mounted.) In the §11 orphan case the
  route effect's revert can unmount the newly focused parent before or
  after the marker fires, and DOM focus can still land on `<body>` there —
  accepted, matching the transcript/doc precedent; the "never strands"
  guarantee covers the non-orphan paths.
- **Breakpoint crossing:** the workspace store survives the
  DockHost↔StackHost swap, so a panel pane open at a desktop→mobile
  crossing renders full-screen in StackHost (it reads the same §5 stores,
  so it works). StackHost's own back affordance is NOT a reliable return
  path here — its back stack is component-local and resets across the host
  swap, so Back would fall through to welcome and rewrite the URL to "/" —
  and it is therefore SUPPRESSED for the panel pane types
  (`showBack` excludes them); the return path is the `BackToParentAction`
  header action every panel pane renders (the shared component
  transcript/doc panes already use, keyed on the session ref), which
  re-focuses/reopens the parent session regardless of host or back-stack
  state. A crossed-over panel has no close
  affordance on mobile (StackHost has no tab-x, the mobile buttons operate
  sheets, and BackToParentAction only navigates); it can be closed after
  crossing back to desktop — §11's "crossing back returns it" stands;
  nothing auto-closes it — or on session deletion. Accepted. The locked
  mobile decision governs the toggle AFFORDANCE (mobile chrome buttons open
  sheets); an already-open pane crossing the breakpoint degrades to a
  readable full-screen pane rather than being destroyed.

### 10. Testing

New obligations beyond v1's list:

- `togglePane`/`isPaneOpen` store tests: open→close→open, dedupe per ref,
  secondary placement, focus-on-open, close-nulls-focus.
- **Route-effect tests**: top-level session route with a focused panel pane
  (same ref AND an aside's different ref) counts as applied; **nested
  session route** with a focused panel pane counts as applied; focusing a
  non-panel pane still reverts on both. (The nested cases are mandatory —
  v2's test plan would have passed with the nested bug in.)
- **Placement-rule tests**: a deferred boot deep link over a restored
  layout whose active tab is a panel pane STILL focuses the routed session
  once the tree lands; an in-app navigation (`/thread/X` ↔ `/s/X`) with a
  focused panel still focuses the routed pane; the relaxed clause applies
  only once placement for the current route has completed. (Round 4 proved
  these are the regression-prone direction; the harness supports them via
  AppShell.test.tsx's saved-layout fixtures.)
- **Restore round-trip tests** for the three new types through
  `layoutJSON`/`restoreLayout`, including the pre-hydration title fallback.
- **Claim/release refcount tests**: session pane + panel pane claim pairs —
  open panel, close session, close panel, reordered — against the threads
  store's refcounting and model eviction, **plus eviction tests for the
  three panel stores** (entry retained while any pane references the ref —
  including a backgrounded panel holding no claim — and gone when the last
  pane for the ref closes).
- **Palette tests**: `/tasks` and `/status` dispatch `togglePane` with the
  palette-context ref on desktop — including with collapsed chrome and with
  the session pane unmounted behind its focused panel — and keep the legacy
  click on mobile.
- **Popout toggle-off test**: at the paneActions level with a faked
  dockview api (the `paneActions.test.ts` precedent — jsdom's `window.open`
  is unimplemented, so a real-dockview popout cannot exist under vitest).
  (The workspace store itself is geometry-blind — slots are assign-once —
  so a store-level "panel outside the main grid" test would pin nothing.)
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
  `ensureThread` claim re-hydrates the thread; §8 covers the wait,
  including the dead-session case (loading state, same as a restored
  session pane).
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
- **Orphan restored panel:** `BackToParentAction` from a restored panel
  whose session pane isn't open CREATES that session pane, which re-runs
  the route effect and snaps focus back to the routed pane (the relaxation
  covers panel panes only) — the session opens as a background tab. This
  matches what restored transcript/doc panes do today; the label and the
  reopen still work. Accepted.

## Out of scope

- Deep links / routing for the panel panes.
- Any change to what the panels show (content, fields, badge math).
- Removing the `Sheet` widget or its other consumers.
- Escape-to-close or focus-trapping in dockview panes (see §9).
- Dockview layout policy beyond secondary-slot placement.

# Issue #4953: the palette refuses a stale needs-you row instead of closing silently

## Intent and scope

Make the Mod+J command palette's empty-query needs-you rows agree with the
shared needs-you controller's staleness admission decision, exactly as #4846
made the floating Next-needs-you list agree with it.

The palette renders the same queue from `selectNeedsYouRows`, but its needs-you
`PaletteItem` carries only the `NavigationSessionSummary`. The published
controller snapshot already carries the per-row `selectable` and `refreshState`
fields (#4846). A row whose page is refreshing or failed therefore renders as
enabled; activating it closes the palette through `deferNeedsYouAction`, and the
deferred `controller.select(ref)` then declines the row,
`selectNeedsYou`'s `choice?.selectable` guard finding it unselectable. The reader
sees the palette vanish and lands nowhere.

Intended deliverables:

- `cmd/evener-hub/frontend/src/shell/palette/CommandPalette.tsx`;
- `cmd/evener-hub/frontend/src/shell/palette/CommandPalette.test.tsx`;
- the "Floating Next-needs-you control" section of `docs/web-ui/README.md`,
  adding: "The palette renders each row from the controller's published choice:
  a row whose page is refreshing or failed is refused, says why, and stays in
  the palette until the page is current again.";
- this specification.

No change to `cmd/evener-hub/frontend/src/shell/needsYouActions.ts`, the
floating list, the navigation store, the revalidator, selectors, routing or the
selection admission policy.

## Chosen change

1. `CommandPalette` already mounts under the shell's `NeedsYouProvider` and
   receives the one controller. It now passes `snapshot.choices` into
   `PaletteBody`; needs-you `PaletteItem`s carry a `NeedsYouChoice` instead of a
   bare summary. The palette keeps the controller's queue order and dedupe: the
   choices are built from `selectNeedsYouRows` by the controller. The palette
   trusts `selectable` as published; `rowPageKey`'s own page lookup, including
   its pre-existing preference for the earliest needs-you page that contains a
   ref, is unchanged.
2. The palette's shared `Row` treats `!choice.selectable` as unavailable:
   `aria-disabled="true"`, the row's existing unavailable styling, and
   `data-refresh-state` exposing the structured `refreshing`/`failed` value.
   The row's trailing line states the refusal in place of the usual "needs you"
   hint: "Refreshing" for a stale page and "Couldn't refresh" for a failed one.
   A listed row always owns a page, so the no-page branch gets no reason text,
   matching #4846. Selectable rows keep their current dot, title and hint.
3. Activating a refused needs-you row returns without closing the palette and
   without navigating. The palette reads the controller's current snapshot at
   activation, not only the choice captured by the last React commit, so a page
   that went stale between paint and keypress is refused too. The row stays in
   place and becomes enabled when the controller publishes a selectable choice
   again. A selectable row keeps the existing deferral path: close the palette,
   wait for the controller to be unblocked, then `controller.select(ref)`.

Recovery stays where #4846 put it: the revalidator refreshes an invalidated page
and retains failed rows, and the controller's own Next/Load-more demand retries
a failed boundary page. The palette adds no second refresh path and does not
change the controller's refusal policy.

## Preservation

- Non-needs-you rows (commands, args, handoff, live/past/in-session search
  results) keep their rendering, availability and activation behavior.
- Needs-you order, dedupe, section header, page-demand behavior and the Mod+J
  command are unchanged.
- A selectable needs-you row other than the focused session's own row, whose
  page is still current when the deferred selection runs, closes the palette
  and navigates through the controller's deferred selection, including the
  existing cancellation fences.
- The race where a row is selectable at activation and its page turns stale
  before the deferred controller acts keeps its current outcome: the palette
  closes and no navigation happens.

## Non-goals

- No change to #4846's floating list, the controller's refusal policy, the
  revalidator, the navigation store or `selectNeedsYouRows`.
- No new notice, toast, retry button, error-strip state or refresh path.
- No reordering, filtering or redesign of the palette.
- The currently focused session's own needs-you row is out of scope. The
  controller also declines `ref === focusedSessionRef()`, which is not part of
  `NeedsYouChoice.selectable`; the palette already closes on that row and lands
  the reader on the session they were reading. #4953 changes only the
  refreshing/failed refusal, and filtering or refusing that row would change
  palette contents for an unrelated case.
- No full browser suite.

## Verification

Red-first component test in `CommandPalette.test.tsx`, through the real provider
and controller:

1. Seed two needs-you rows and mark the page `stale:true, loading:true` before
   opening the palette. The first row reports `aria-disabled="true"` and
   `data-refresh-state="refreshing"`; Enter and click each leave the palette
   open, keep the route and history unchanged, and do not navigate.
2. Restore the same page to current (`stale:false, loading:false, error:null`)
   through a notifying `navigationStore.setState`. The row loses
   `aria-disabled`/`data-refresh-state`; Enter and click then close the palette
   and open the session through the normal deferred path.
3. A failed page (`stale:true, error`) renders `aria-disabled="true"` with
   `data-refresh-state="failed"`; click leaves the palette open and does not
   navigate.
4. A page that turns stale between the last paint and a click, in the same
   React act, is still refused: the palette stays open and the route does not
   change. This proves activation consults the controller's current snapshot,
   not only the rendered choice.

The test fails before the change at the first `aria-disabled`/refresh-state
assertion. It asserts rendered state and observable behavior, not copy.

Commands, from the worktree:

- `cd cmd/evener-hub/frontend && npx vitest run src/shell/palette/CommandPalette.test.tsx src/shell/palette/CommandPalette.edge.test.tsx src/panes/session/NextNeedsYou.test.tsx src/shell/needsYouActions.test.ts`
- `cd cmd/evener-hub/frontend && npm run typecheck`
- `cd cmd/evener-hub/frontend && npx biome check --write src/shell/palette/CommandPalette.tsx src/shell/palette/CommandPalette.test.tsx` then `npx biome ci` on those paths
- `make build` from the repository root

The full frontend suite and browser suite are CI's job under the lane brief's
verification rule, not this lane's.

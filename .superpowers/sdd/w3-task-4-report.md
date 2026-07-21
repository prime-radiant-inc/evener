# Wave 3 Task 4 report — mobile stack host

Branch `w3-mobile`, off Task 2's tip `2b7adbf4e`. 7 commits. Full suite green (715 tests, 53
files), `tsc --noEmit` clean, `eslint src` clean, `npm run build` clean — each verified twice in a
row for stability. Zero touches outside `src/shell/mobile/**` and `src/shell/useIsMobile.ts(+test)`
(`git diff --stat 2b7adbf4e HEAD` — 7 files, all within scope).

## Commits

| Commit | Unit |
|---|---|
| `436cb5e3f` | useIsMobile — reactive <900px breakpoint hook |
| `176ced6fb` | StackHost — one focused pane, full-screen, remount-safe |
| `29a8084c9` | StackHost — back navigation, own back-stack |
| `9112fee49` | StackHost — sync the address bar to the focused pane |
| `ffe29f47e` | TreeDrawer — tree rail slot, wired into StackHost's top bar |
| `82fb60931` | StackHost — document the AppShell mount contract |
| `19c75c809` | TreeDrawer — de-duplicate the placeholder's own heading |

## TDD evidence

Every unit was written test-first at the granularity DockHost/workspace's own Task 2 report
describes ("textbook" per functional area, not literally one assertion at a time): a batch of
related tests written, run to confirm the right RED (module/behavior missing, not a typo), then
just enough implementation to go GREEN, then the next batch. Two real bugs were caught this way,
not just absent implementations:

- **The remount-safety test (`switching away from a pane and back remounts it fresh`) failed for
  the right reason on the first run**: without a `key={pane.id}` on the rendered pane, React kept
  the SAME `Session`/fixture component instance mounted across a focus change between two panes of
  the same registered type (identical `Component` reference, only `params`/`paneId` differ) — a
  local click-counter fixture stayed at `1` instead of resetting to `0`. This is exactly the
  "unmount, not hide" contract DockHost's own `PaneHost` comment documents (dockview always
  remounts fresh on tab activation, confirmed live in that task's report) — StackHost has to
  reproduce it explicitly since, unlike dockview, nothing else forces a remount when the pane TYPE
  stays the same. Fixed with `key={focusedPane.id}`; the test result is the actual proof, not
  inspection.
- **The `TreeDrawer` auto-close test failed on the first run for an unrelated, process reason**:
  `workspaceStore.getState().openPane(...)` called *after* the component was already mounted and
  subscribed, then asserted on synchronously with no `await`/`act()`, raced React's own scheduling
  — the assertion ran before the resulting re-render/effect had flushed. Not a StackHost/TreeDrawer
  bug; fixed by wrapping the post-mount store mutation in `act()`, the same idiom
  `workspace.test.ts`/`DockHost.test.tsx` already use for this exact situation (`DockHost.test.tsx`
  even uses `vi.waitFor()` for the analogous case). Logged here since it's a good example of "read
  the actual failure before theorizing" — Task 2's own report names the identical lesson.

One deliberate, disclosed exception to strict red-first: `StackHost.module.css`'s layout rules
(flex structure, `padding-bottom: env(safe-area-inset-bottom)`) were authored as a cohesive
stylesheet alongside the first implementation pass rather than declaration-by-declaration, matching
this codebase's own established treatment of CSS (`sheet.module.css`'s slide-in animations were
clearly written as a whole; its tests — and my one addition, the safe-area-inset assertion in
`StackHost.test.tsx` — exist as source-content regression *locks*, not iteratively red-green'd per
CSS line). All *behavioral* (TSX) code followed strict red-green.

## Mount contract (for AppShell, documented in `StackHost.tsx`'s own header comment)

```ts
const isMobile = useIsMobile();
...
{route === null ? <NotFound /> : isMobile ? <StackHost /> : <DockHost />}
```

`StackHost` takes **no props** — exactly like `DockHost`, it reads `useWorkspaceStore` directly.
A breakpoint crossing unmounts whichever host was showing and mounts the other fresh: dockview's
own layout/geometry does not survive (its `DockviewApi` is torn down on `DockHost` unmount, per
`workspace.ts`'s `registerDockviewApi(null)`), which is accepted — layout persistence is
desktop-only by design (requirement 5). `useWorkspaceStore`'s own `panes`/`focusedPaneId` — what
*both* hosts actually render from — live above either host's lifecycle and survive the swap
unaffected. Unlike `DockHost`, `StackHost` registers no module-level singleton of its own, so no
explicit teardown beyond what React already does is needed on unmount.

I did not verify this contract by literally rendering it inside `AppShell` (that file is
forbidden), but I did verify the *reasoning* two ways: (1) `StackHost`'s own test suite renders it
completely standalone, the same boundary `DockHost.test.tsx` uses for `DockHost`, so it's proven to
work with zero assumptions about its parent; (2) the "reuse popstate, don't rebuild" requirement
is satisfied structurally, not just by omission — grepped to confirm neither `StackHost.tsx` nor
`TreeDrawer.tsx` registers a `popstate` listener anywhere, and `StackHost`'s reactivity to
`useWorkspaceStore` (proven by the "switching the focused pane... swaps which one is rendered"
test) is the *entire* mechanism AppShell's existing `usePathname`/`openRouteAsPane` chain would
drive through on a real back/forward — so browser back/forward working through `StackHost` follows
from tests already written, not a new one that would just re-test AppShell's own routing glue
(already covered exhaustively by `AppShell.test.tsx`, e.g. its
"navigating from one session deep link to another, post-mount" test, which dispatches a real
`popstate`).

## Drawer design: side="bottom", not "right"

`widgets/sheet` supports only `"right" | "bottom"` — a genuine left-slide isn't implementable
without editing `widgets/sheet` (forbidden), so the real choice was bottom vs. the already-existing
right variant. Bottom wins on the merits independent of that constraint: Sheet's header (title +
close button) sits at the *top* of a right-anchored, full-height sheet — on a tall phone held
one-handed, that's outside the thumb's natural reach (Fitts's-law/thumb-zone reasoning: the bottom
half of the screen is reachable one-handed, the top corners are not). A bottom sheet's own header
sits close to that reach, and "pull up from the bottom" matches the native
iOS/Android bottom-sheet/action-sheet pattern users already know — Material Design explicitly
recommends a modal bottom sheet over a side drawer on narrow viewports for the same reason. A
right-edge sheet, sliding in over a host that already occupies the full screen width (no side-by-
side layout the way desktop's dockview allows), would also read as "just another panel" rather
than clearly-distinct navigation chrome.

## Back-stack design

`useWorkspaceStore` intentionally tracks only *current* focus (`workspace.ts`'s own header
comment), not history — so StackHost keeps its own component-local back-stack (a `useRef<string[]>`
of previously-focused ids, most-recent-last) rather than extending the locked store. Considered and
rejected: driving the in-app back button via `window.history.back()` instead — this fails the
plan's own "pops to the previously-focused pane **or welcome**" requirement whenever the user's
*first* pane this session was a deep link (zero prior in-app history entries), since
`history.back()` would then leave the app rather than land on welcome. The local stack sidesteps
this by construction: it's independent of how many real browser history entries exist. A
`wentBackRef` flag distinguishes "the user tapped back" from any other focus change so an abandoned
pane is never re-pushed (no A↔B ping-ponging); stale stack entries (a pane closed since it was
stacked) are skipped by `popValidBackTarget`. Welcome itself never shows a back affordance
(it's the stack's root) regardless of what the stack holds — tested explicitly (11 back-navigation
tests cover single/multi-level pop, exhausted-stack fallback, no-ping-pong, and the
mounted-with-a-pre-seeded-pane case where this component never itself observed a "transition into"
the initial pane).

Known, disclosed, narrow limitation: the back-stack is component-local, not module-level, so it
resets across a `StackHost` unmount/remount (a breakpoint crossing to desktop and back). Real
phones never cross the 900px breakpoint mid-session, so this only matters for a resized dev browser
window — the same class of narrow limitation Task 2's own report discloses for `DockHost`'s
"remount re-runs the whole boot sequence."

## Rail slot contract (`TreeDrawer.tsx`)

`TreeDrawer` takes one optional prop, `children`, which is the *entire* integration surface for the
sibling stream building the real rail (`src/shell/rail/**` — outside this stream's scope in both
directions, so the two can only meet through props at a merge step, not a direct file edit):

```tsx
<TreeDrawer><Rail /></TreeDrawer>
```

replaces the placeholder outright. No bespoke `onSelect`/`onClose` callback prop was added:
`TreeDrawer` already watches `useWorkspaceStore`'s `focusedPaneId` itself and closes automatically
whenever it changes while open, so the real rail only ever needs to call `workspaceStore`'s own
`openPane()` — exactly what every other pane-opening trigger in the app already does (Welcome's
"New session" button, AppShell's routing glue, DockHost's dockview interactions). Tested that this
distinguishes a real navigation from a same-pane store update (re-opening an already-focused
singleton with different params updates the store without changing `focusedPaneId`'s own value —
must not close the drawer; verified directly, not assumed).

## Files

Created (all within the assigned ownership):
- `cmd/serf-hub/frontend/src/shell/useIsMobile.ts` + `.test.ts`
- `cmd/serf-hub/frontend/src/shell/mobile/StackHost.tsx` + `.module.css` + `.test.tsx`
- `cmd/serf-hub/frontend/src/shell/mobile/TreeDrawer.tsx` + `.test.tsx`

Touched: nothing else. `AppShell.tsx`, `workspace.ts`, `routing.ts`, `paneRegistry.ts`,
`DockHost.tsx`, and every widget are read-only references, confirmed via `git diff --stat` against
Task 2's tip.

## Self-review

- **`useIsMobile` re-derives the media query in both the `useState` initializer and the effect**
  (two separate `window.matchMedia(...)` calls) rather than sharing one `MediaQueryList` — real
  browsers return equivalent, independently-live lists for the same query either way, and the two
  calls happen in the same synchronous render→commit→effect pass with no chance for the viewport to
  change in between, so this is correct, not just simple. Kept the two-call form because threading
  a single `MediaQueryList` through a ref would be more machinery for the same guarantee.
- **`StackHost` runs three separate small effects** (back-stack bookkeeping, URL sync, welcome
  backstop) rather than one combined effect — deliberate, mirroring DockHost's own
  three-separate-effects precedent (structural/focus/title-sync) for the same reason: each answers
  one question with its own minimal dependency array, and combining them would blur which one a
  future change is actually touching.
- **`popValidBackTarget` mutates its `backStack` argument in place** (via `.pop()`) rather than
  returning a new array — matches the ref it's always called against
  (`backStackRef.current`, itself intentionally mutated in place, same idiom as `DockHost.tsx`'s
  `pushedParamsRef`) and avoids allocating a throwaway array on every back tap for no observable
  benefit.
- **The top bar always renders a `.leading` wrapper div, even when the back button is absent**
  (welcome) — not strictly required by `justify-content: space-between`'s own 2-child positioning
  (verified: it anchors first/last children to the row's edges regardless of either child's own
  size), but needed so the drawer trigger stays the *second* flex child rather than becoming the
  *only* one (which would collapse to `flex-start`, i.e. jump to the left) whenever back is hidden.
- **`focused` is hardcoded `true` on the one pane `StackedPane` ever renders** — correct by
  construction (StackHost never renders more than one pane at a time, so whatever it renders IS the
  focused one), not a placeholder needing a real value later.

## Concerns

- **Not independently verified in a real mobile viewport this task** — the plan's Task 7 (wave
  gate) owns the chrome-skill 390px manual smoke test across the merged whole; this task's
  verification is the full automated suite (RTL + jsdom) plus reading the rendered CSS module
  source for properties jsdom can't evaluate (`env()`), same boundary Task 2's own report drew for
  "mock only what jsdom can't do."
- **The rail slot placeholder's copy ("Session tree arrives from a parallel wave-3 stream") is my
  own wording**, not reviewed by whoever owns the rail — cheap to change, flagging in case the
  actual rail work wants different phrasing before merge.
- **Sheet's own `.bottom` variant has no `env(safe-area-inset-bottom)` of its own** (only
  `StackHost`'s outer container does, per requirement 4's literal scope) — a real phone's home-
  indicator area could sit under the sheet's own bottom edge/footer. Out of this task's scope
  (`widgets/sheet` is forbidden) and not asked for; flagging for whoever next touches that widget.
- **Composer safe-area accommodation is a comment only** (`StackHost.module.css`), per the task's
  own instruction ("composer accommodation comment for wave 5") — no composer exists yet to
  accommodate.

## Verification

```
npx vitest run   → EXIT=0  (715 passed, 53 files; 2 full reruns, identical both times)
npx tsc --noEmit → EXIT=0  (no output, both runs)
npx eslint src    → EXIT=0  (no output, both runs)
npm run build     → EXIT=0  (tsc --noEmit && vite build; dist/ shows Welcome-*.js/Session-*.js
                              still separately chunked; the >500kB main-bundle warning is
                              pre-existing from Task 2's dockview inclusion, not new here — this
                              task's own files add ~4kB of source and pull in no new dependency)
```

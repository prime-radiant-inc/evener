# Wave 3 Task 2 report — dockview desktop host + workspace store + layout persistence

Branch `w3-shell`, off Task 1's tip `17f6c803e`. 10 commits. Full suite green (678 tests, 50
files), `tsc --noEmit` clean, `eslint src` clean, `npm run build` clean — each verified twice in
a row for stability. Zero touches to any forbidden path (`git diff --stat 17f6c803e HEAD --
src/protocol src/stores src/widgets src/panes/welcome '**/*.go'` and the same for
`vite.config.ts`/`tsconfig.json`/`eslint.config.js`/`package.json` are all empty).

## Commits

| Commit | Unit |
|---|---|
| `99b3d97ce` | paneRegistry — PaneTitleCtx gains an optional thread-name lookup |
| `46052c0b3` | session pane — placeholder for wave 4's real transcript view |
| `b8e404357` | NotFound — quiet routing-level fallback |
| `7b58b5da0` | workspace store — openPane/closePane/focusPane + layout persistence |
| `863bf68cb` | dockview-theme.css — restyle via surface/edge/ink only |
| `27f4205d1` | DockHost — dockview desktop host |
| `ca07c3b60` | AppShell — mount DockHost, wire routing to the workspace store |
| `e85752bf8` | DockHost test — cover `opts.beside` splitting into a real group |
| `4186ed11c` | AppShell — fix spurious welcome tab on 404-to-deep-link navigation |
| `78e47c8e6` | DockHost test — cover unmount clearing a pending debounced save |

## TDD evidence

Every unit followed red→green: test written first, run to confirm failure, implementation added
until green. `workspace.ts` (26 tests) and the placeholder panes were "textbook" TDD — write,
red (module doesn't exist / behavior missing), implement, green, no surprises. The two files
below are where TDD caught real bugs, not just absent implementations:

**`DockHost.tsx`: a genuine race, caught by a real save-then-restore round trip, not by
inspection.** `restoreLayout()` mutates dockview's panels directly via `fromJSON()` (the only way
to restore split/group geometry — individual `addPanel()` calls can't reconstruct it) while
workspace state updates separately; those two writes can't land in the same React commit. My
first version ran the restore-or-fallback boot sequence inside a `useEffect` keyed on `[api]`.
The structural reconciliation effect (a sibling effect, same commit) saw dockview's freshly-
restored panels alongside a still-stale-empty `panes` on that same render pass, read that as
"these are stale," and deleted everything `restoreLayout()` had just created. The "restores a
previously-saved layout" test failed with `saved: null` at first (a different, earlier bug — see
below), and after fixing that, failed again by rendering an empty dockview. Root-caused via
`console.log`-ing `workspaceStore.getState().panes` mid-test rather than guessing. Fix: do the
restore-or-fallback synchronously inside `onReady`, *before* calling `setApi()` — so `panes` is
already caught up by the time any reconciliation effect ever sees a non-null `api`. Documented at
length in both `DockHost.tsx` and `workspace.ts`.

**`AppShell.tsx`: a second, related race — this one live-reproduced twice, not just caught by a
failing assertion.** The plan requires the *initial* route's pane to open before DockHost's own
`onReady` can wrongly fall back to welcome (React runs child effects before parent effects, so a
plain `useEffect` in AppShell always runs *after* DockviewReact's mount effect within one commit
— never before). First fix: open the initial pathname's pane during render (a `useRef`-guarded
call, not an effect), since nothing is mounted/subscribed to `workspaceStore` yet for that update
to conflict with. This passed all tests, but `console.error` showed `Cannot update a component
(DockHost) while rendering a different component (AppShell)` on the "clicking New session"
test — the render-phase call was *also* firing on later re-renders, once DockHost already existed
and was subscribed. Fixed by splitting "initial route" (render-phase) from "every pathname change
after mount" (a plain effect). All green, no more warning — but I then reasoned through a further
edge case rather than stopping there: what if DockHost's *first-ever* mount isn't AppShell's
*first-ever* render? (Loading directly on an unresolved path — NotFound renders, DockHost never
mounts — then navigating to a real one.) I verified this live before trusting my own reasoning:
added a throwaway test, ran it, and confirmed `TABS: ['Welcome', 'ref_from_404']` — the same race,
in a narrower shape my first fix didn't cover. Second fix: track whether DockHost has *ever*
mounted (a ref set once `route !== null`), not whether this is AppShell's first render — render-
phase opening now stays safe through however many renders it takes to first reach a resolvable
route, then permanently defers to the effect once DockHost genuinely exists. Locked with a real
regression test (not the throwaway).

Both fixes are documented in the code with the reasoning, not just the "what."

**Two smaller test-writing mistakes, also worth naming since they cost real time before I
recognized them as *my* bugs, not product bugs**: (1) a `findByText` on a bare ref string threw
"found multiple elements" the first time a pane had no known thread name, because the dockview
tab title and the placeholder pane's own `PaneScaffold` title both render the raw ref — fixed by
scoping/counting matches instead of assuming one. (2) An initial ~15-minute detour chasing a
"dockview cleanup/disposal" theory for a hanging test, based on reading only the `tail`-truncated
failure output; the real error (once I read the actual message, not just the DOM dump) was
`Found multiple elements with the text: ref_b` — the exact same query-ambiguity bug in a
different test, nothing to do with dockview lifecycle at all. Logged here because both are
process lessons (verify the actual error before theorizing) as much as code fixes.

## dockview-in-jsdom findings

Real `dockview-react` renders in jsdom throughout this task's tests — no mock of dockview's own
behavior anywhere. Two things jsdom (or this Node version) doesn't provide, both stubbed minimally
and disclosed in the test files that need them:

- **`ResizeObserver` is absent from jsdom entirely.** dockview-core dials one on mount to drive
  its auto-resizing; without a stub, mounting `<DockviewReact>` throws
  `ReferenceError: ResizeObserver is not defined` immediately. A no-op class (`observe`/
  `unobserve`/`disconnect`, all empty) is sufficient — nothing in these tests asserts on actual
  pixel geometry, only on DOM structure/content/counts.
- **`localStorage` is unavailable under this project's vitest+jsdom setup — but the root cause is
  Node 26's own doing, not jsdom's.** Verified directly rather than assumed: a bare
  `new JSDOM(...)` constructed with vitest's exact same options has a working
  `window.localStorage`; only inside a vitest test does `localStorage`/`window.localStorage` come
  back `undefined`, printing `ExperimentalWarning: localStorage is not available because
  --localstorage-file was not provided` — and running `node -e 'console.log(typeof
  localStorage)'` against this repo's Node reproduces the identical warning standalone. Node 26
  ships its own global `localStorage` accessor (disabled without a flag this project's test
  script doesn't pass) that shadows jsdom's real implementation. A minimal in-memory `Storage`
  stub (`getItem`/`setItem`/`removeItem`/`clear` only — nothing here calls `.length`/`.key()`)
  works around it, scoped to each test file that needs it (no shared test-utils module exists in
  this project — `stores/threads.test.ts` documents the same duplication tradeoff for its own
  helper). A real fix belongs in `vite.config.ts` (`test.environmentOptions` or `setupFiles`),
  which is on this task's forbidden-files list.

Everything else worked as real dockview, with real, sometimes-surprising behavior worth recording
because it shaped the design (all verified via live probes before being relied on, not assumed):

- **Inactive panel content is fully removed from the DOM**, not just CSS-hidden — confirmed via a
  direct check (`queryByText` for an inactive pane's text returns `null`). Shapes several test
  assertions (can't assume a previously-active pane's text lingers).
- **`onDidActivePanelChange` fires *synchronously*** for both `addPanel`'s default-active
  behavior and an explicit `setActive()` call — but a single `setActive()` call on an *existing*
  panel fires it *twice* (once re-affirming the outgoing panel, once for the real target),
  confirmed via a live probe. This is exactly why the "user vs api origin" filter on the mirror-
  into-store handler is load-bearing, not defensive-programming theater: dockview's own docs say
  the field exists "to avoid feedback loops," and the probe showed a concrete case where an
  unconditional mirror would (in a more complex reconciliation sequence) briefly apply a wrong
  intermediate value.
- **`onDidLayoutChange` fires one microtask *after* `addPanel()`**, not synchronously — confirmed
  by checking the handler's call count after 0, 1, and N microtask/macrotask turns. Doesn't affect
  the debounce design (a `setTimeout` in the handler works fine regardless of when the handler
  itself first fires) but matters for test sequencing under fake timers (see below).
- **The default tab's close (×) button has no `aria-label`** — `.dv-default-tab-action` is a
  bare `<div>` wrapping an SVG, no accessible name. A real, disclosed gap in dockview itself, not
  something this task's scope (CSS restyling only, no custom tab renderer) can or should fix —
  flagged as a concern below, same "known gap, documented not silently fixed" treatment §7 of
  `design-system.md` already uses for two Wave 2 items.
- **`className` on `DockviewReact` lands on an inner (gridview-level) wrapper, not the outermost
  `.dv-shell` div** — the outer div keeps a separate, hardcoded `dockview-theme-abyss` default
  (dockview defaults `options.theme` to its own built-in "abyss" theme independently of the
  `className` prop, and applies *that* theme's className to the outer wrapper). Confirmed by
  dumping every element's classList in a mounted tree. Harmless: CSS custom properties resolve
  from the nearest ancestor that defines them, and `dockview-theme-serf` sits closer to every
  `.dv-tab`/`.dv-groupview`/`.dv-content-container` element than the outer div does — but
  asserted precisely in `DockHost.test.tsx` rather than left as an unverified assumption, since
  seeing "abyss" in devtools would otherwise look like a real bug.
- **dockview ships an off-screen `aria-live` announcer** (`.dv-live-region`, "polite"; a second
  "assertive" one for errors) that announces things like `"Doc ref_a closed"` — a genuine, welcome
  accessibility feature, and also a real gotcha for test queries: a loose text match can find the
  announcement instead of (or alongside) the DOM you actually meant to assert on.

## Layout persistence design

- **Key**: `serf.workspace.layout.v1` (exact string from the plan). **Debounce**: 400ms,
  `onDidLayoutChange` → `clearTimeout` + reschedule, same idiom as `widgets/combobox`'s own
  `onQuery` debounce (timer for the save side; the test drives it with `vi.useFakeTimers()` +
  `act(() => vi.advanceTimersByTime())`, mirroring `combobox.test.tsx`'s own debounce test
  exactly — never a bare sleep).
- **Boot**: `readStoredLayout()` returns `undefined` for "absent, corrupted JSON, or
  `localStorage` itself throwing" — all three collapse to the same outcome (best-effort
  persistence, never fatal to the workspace). If present, `restoreLayout(json)` is attempted;
  regardless of its own success/failure, DockHost falls back to opening welcome whenever the
  workspace is *still empty* afterward — not simply whenever `restoreLayout()` reports failure.
  This matters for two cases the "just check the boolean" version would get wrong: (a) other code
  (AppShell's routing) can already have opened a pane before this runs, and must never be stomped
  on; (b) a *successfully* restored but empty layout (every tab was closed before the last save)
  still needs the fallback, since a blank dockview with no chrome of its own to open a new pane
  from is a dead end.
- **`restoreLayout()` validates twice**: dockview's own `fromJSON()` structural check (verified
  against dockview-core's actual source — it throws synchronously on non-object data or a
  malformed grid root, *after* already clearing whatever was there), and this app's own check that
  every restored panel's `params.paneType` is both a real `PaneTypeId` *and* currently registered
  (`paneFor()` doesn't throw) — the latter specifically covers a layout saved by a later build
  (once a pane type this one hasn't shipped exists) loaded by an older one, realistic given
  layouts persist to `localStorage` across deploys. Either failure clears the api and empties the
  store, so a rejected restore never leaves a panel this app can't render.

## Locked-interface compliance

`useWorkspaceStore` shape (`openPane`/`closePane`/`focusPane`/`layoutJSON`/`restoreLayout`) and
its singleton-focus semantics match the plan's Locked interfaces block exactly. `PaneTitleCtx`
gained one field beyond Task 1's empty placeholder (see Deviations below) — additive, not a
breaking change to the locked shape (`title(params, ctx)`'s own signature is unchanged).

## Files

Created:
- `cmd/serf-hub/frontend/src/shell/{DockHost.tsx, DockHost.test.tsx, DockHost.module.css}`
- `cmd/serf-hub/frontend/src/shell/{workspace.ts, workspace.test.ts}`
- `cmd/serf-hub/frontend/src/shell/dockview-theme.css`
- `cmd/serf-hub/frontend/src/shell/{NotFound.tsx, NotFound.test.tsx}`
- `cmd/serf-hub/frontend/src/panes/session/{index.tsx, index.test.tsx, Session.tsx, Session.test.tsx}`

Modified:
- `cmd/serf-hub/frontend/src/shell/AppShell.tsx` + `.test.tsx` — mounts DockHost (desktop) /
  NotFound (unresolved path); `openRouteAsPane()` routing glue; mobile seam commented, not built
  (Task 4 owns `useIsMobile()` + the breakpoint, per the plan).
- `cmd/serf-hub/frontend/src/App.test.tsx` — the same ResizeObserver/localStorage stubs
  `AppShell.test.tsx` needs, since `<App/>` renders `<AppShell/>` at the default route. No change
  to `App.tsx` itself.
- `cmd/serf-hub/frontend/src/shell/paneRegistry.ts` and
  `cmd/serf-hub/frontend/src/styles/token-contract.test.ts` — see Deviations below.
- `routing.ts` was **not** touched — `AppShell.tsx` calls `urlToPane()` directly and drives
  `workspaceStore` itself; no workspace-glue hook was needed there.

## Deviations from the literal file list (both disclosed, both necessary)

1. **`shell/paneRegistry.ts`**: `PaneTitleCtx` (`Record<string, never>` in Task 1) gains one
   optional field, `threadName?(ref: string): string | undefined`. Task 1's own report flagged
   this exact seam: *"Left empty and documented as a seam; add fields when a real `title()`
   implementation needs them"* — this task's session-pane tab title is that real implementation.
   Made **optional** specifically so `src/panes/welcome/index.test.tsx`'s existing
   `title({}, {})` call (a file this task must only consume, not edit) keeps compiling unchanged;
   verified this was the actual failure `tsc` produced before choosing optional over required.
2. **`src/styles/token-contract.test.ts`**: adds one named naming-exception (`dockview-theme.css`
   is allowed as a non-`.module.css` filename — it targets dockview's own unscoped className,
   which CSS Modules' hashed class names structurally can't do). The wave-3 plan's own Global
   Constraints state this file "is on the token-contract allowlist" as a settled premise; no other
   task in the plan owns this file, and `dockview-theme.css` cannot exist compliantly without it.
   Every *other* mechanism in the contract (no chromatic literal, the attention/alive/danger
   allowlist) applies to `dockview-theme.css` unchanged — confirmed by running the full contract
   suite (121/121 pass, including two newly-generated per-file checks for this exact file).

Both are minimal, mechanically necessary, and disclosed here rather than made silently.

## Self-review

- **Three separate reconciliation effects in `DockHost.tsx`** (structural add/remove/param-push;
  focus; title-sync) rather than one combined effect. Deliberate: each has a different, minimal
  dependency array (title-sync alone depends on the *reactive* `threads` map; the other two would
  re-run needlessly on every unrelated thread update if combined), and each answers exactly one
  question. The initial-creation title uses a one-time `threadsStore.getState()` snapshot (not
  the reactive `threads` value) specifically so the structural effect's own dependency array
  doesn't grow to include it — the title-sync effect is the one place `PaneTitleCtx.threadName`
  is wired reactively, matching requirement 4's own wording precisely.
- **`sameParams()` is `JSON.stringify` equality**, not a deep-equal library. Justified by the
  actual param shapes in play (`{ref}`, `{ref,path}`, `{section?}`, `{}` — a couple of
  primitive-valued keys each, no real key-ordering risk) rather than assumed; a real deep-equal
  dependency would be more machinery than this needs.
- **Pane ids are a monotonic counter** (`pane_<type>_<n>`), not `crypto.randomUUID()` — chosen
  specifically so tests can assert on exact generated ids without mocking randomness, and reset
  via `resetWorkspaceStoreForTests()` between tests for determinism.
- **The `PaneHost` adapter reads `api.isActive` directly** (via `onDidActiveChange`) for the
  `focused` prop, rather than deriving it from `useWorkspaceStore`'s `focusedPaneId`. Both are
  kept in sync by the focus-reconciliation effect, but reading dockview's own truth avoids a
  render-order dependency between an individual panel's host component and DockHost's own effects
  — simpler to reason about, and it's what a live probe showed to actually reflect the visible tab
  state at every point in a reconciliation sequence.
- **`docs/superpowers/plans` §Global Constraints' "restyle... mapped ONLY onto our tokens
  (surface/edge/ink families)" was read as scoping *color*, not geometry** — `dockview-theme.css`
  restyles the ~13 color-bearing `--dv-*` custom properties dockview exposes and leaves structural
  ones (border-radius, tab height/margins, the multi-hue "tab group color" chips feature, the
  genuinely transient drag-over/active-sash colors) at dockview's own defaults. A narrower reading
  than "restyle everything dockview exposes," chosen because the plan's own sentence is explicitly
  about token *families* (all four of which are color concepts) and because touching geometry
  wasn't asked for.

## Concerns

- **Main JS bundle grew to ~636 kB (170 kB gzip)**, past Vite's 500 kB warning threshold — new
  this task (Wave 3 Task 1's own build had no such warning). Dockview isn't code-split; it's
  pulled in by `DockHost.tsx`, which `AppShell.tsx` renders unconditionally on desktop, so it
  loads with the main bundle rather than a lazy chunk. `Welcome`/`Session` remain correctly
  code-split (confirmed in the `dist/` output: separate `Welcome-*.js`/`Session-*.js` chunks, each
  under 1 kB). Whether to lazy-load `DockHost` itself is a real design tradeoff (a loading flash
  while dockview's own chunk fetches, versus faster initial parse) that wasn't asked for this task
  and that I didn't decide unilaterally — flagging for Task 7's wave-gate or a dedicated pass.
- **Known, disclosed limitation: unmounting and remounting `DockHost` mid-session re-runs its
  whole boot sequence**, including a fresh `restoreLayout()` from `localStorage` — there's no
  guard distinguishing "the app's true first mount" from "a remount after being unmounted." The
  *only* way `DockHost` unmounts in this task's shipped surface is navigating to a genuinely
  unknown path (NotFound renders in its place) and back; `workspace.panes` itself survives the
  unmount unaffected (it's a module-level store, not component state), so the practical effect is
  a possible full re-sync from whatever's in `localStorage` rather than a crash or lost data —  a
  narrow, rare interaction. Not fixed here: solving it needs `DockHost` to know "have I ever
  mounted before, anywhere," which is a bigger seam (module-level, outside any one component's
  lifecycle) than this task's scope calls for; flagging rather than guessing at a fix.
- **dockview's default tab close button has no `aria-label`** (see jsdom findings above) — a
  library gap, not fixable within this task's CSS-only restyling scope; would need a custom tab
  renderer (explicitly out of scope — the plan's restyling is via CSS custom properties, not a
  component swap).
- **Singleton pane params-only changes (e.g. re-opening settings on a different section) aren't
  guaranteed to trigger `onDidLayoutChange`.** `updateParameters()` isn't listed among the
  "structural" mutations (`add`/`remove`/`move`/`float`/`popout`/`maximize`/`load`/`clear`) that
  `onDidMutateLayout`'s own doc comment enumerates, so a reload immediately after switching a
  singleton pane's section (with no *other* layout change since) could restore the *previous*
  section. Zero observable impact in this task's own shipped surface (only "welcome" is currently
  singleton, and its only param is ephemeral note text, not something worth persisting) — flagged
  for whoever builds the real "settings" pane in a later wave, since that one's params (which
  section is showing) plausibly *are* worth persisting exactly.
- **Wave 1 note in Task 1's report is still true and still not this task's to fix**:
  `AppwireClientLike` has no way to distinguish a genuine reconnect-with-a-fresh-client from the
  current single-shot `connect()`; unrelated to this task's surface, not touched.

## Verification

```
npx vitest run   → EXIT=0  (678 passed, 50 files; 2 full reruns, identical both times)
npx tsc --noEmit → EXIT=0  (no output)
npx eslint src    → EXIT=0  (no output)
npm run build     → EXIT=0  (tsc --noEmit && vite build; dist/ shows Welcome-*.js and
                              Session-*.js as separate small chunks, confirming code-splitting
                              held; one chunk-size warning, see Concerns)
```

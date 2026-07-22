# W5 close fix F3 — flake-cluster root cause

**Branch:** `w5f-flake` (base `2510b8adc`) · frontend root `cmd/serf-hub/frontend`
**Status:** root cause found, reproduced, fixed, and proven by controlled A/B + mutation.

## TL;DR

The flake cluster is **not** toast-singleton state bleed (the standing suspect is
disproven below). The root cause is **a cold `React.lazy(() => import(chunk))`
boundary racing Testing Library's default 1000 ms `findBy*` deadline under CPU
load.** When the lazy chunk's *first* transform+import happens with nothing having
warmed it, and the machine is busy (parallel workers / CI), that transform+import
exceeds 1000 ms; the Suspense fallback (`null`) is still mounted; `findByText`
times out. The established fix in this codebase is to `await import(chunk)` in
`beforeAll` so `React.lazy` resolves from a warm module cache — awaiting the real
completion, not widening a deadline.

Two test files carried a **degraded copy** of that warm-up (they awaited `./index`,
which only `import type`s the component — erased at runtime — leaving the real
lazy chunk cold). Fixed both.

## Reproduction evidence

Suite is green at baseline: `136` files / `2023` tests, ~14 s, ~7 workers.

The full suite alone did **not** reproduce the flake in 12 back-to-back runs. It
reproduces when `src/panes/session/index.test.tsx` transforms `Session.tsx` **cold**
(runs before any sibling warms it) **and** the CPU is loaded. Two independent
reproductions:

1. **Cluster + shuffle** (`--sequence.shuffle` over the sighting files, concurrent
   full-suite load), **first iteration**, seed **`1784678033701`**:
   ```
   FAIL src/panes/session/index.test.tsx > the registered component renders the ref it was opened with
   TestingLibraryElementError: Unable to find an element with the text: ref_abc123
     <body><div /></body>          ← Suspense fallback; Session chunk never loaded
     index.test.tsx:49  expect(await screen.findByText("ref_abc123"))
   test duration: 1015 ms          ← right at the 1000 ms findBy deadline
   ```
2. **Single file, cold, under load — pre-fix baseline: `11 / 15` failed (73 %).**
   Every failure identical: same test, same `ref_abc123` miss, all clustered at
   **1009–1025 ms** (i.e. the 1000 ms deadline + ε). The failing DOM is empty
   (`<div/>`), i.e. the fallback — **not** stale/bled content, which is positive
   evidence *against* singleton bleed.

## Mechanism (with code citations)

- `src/panes/session/index.tsx:19` registers the pane with
  `component: lazy(() => import("./Session"))`. Line 9 imports Session as
  `import type` only → **no runtime load of `Session.tsx` from `./index`**.
- `src/panes/session/index.test.tsx` (pre-fix) `beforeAll` awaited **only**
  `import("./index")`. That registers the pane but leaves `./Session` cold. The
  last test renders `<Suspense fallback={null}><SessionComponent…/></Suspense>`
  and asserts `await screen.findByText("ref_abc123")` (`:49`). On first render the
  lazy boundary triggers `import("./Session")` — a large chunk (build output:
  `Session-*.js` ≈ 77 kB / 24 kB gz, and it transitively pulls the transcript,
  composer, widgets, and stores). Its cold transform+import, under load, overruns
  the 1000 ms deadline → fallback still showing → `findByText` fails.
- The **correct** pattern already lives in `src/shell/AppShell.test.tsx:65–77`,
  whose `beforeAll` awaits the *actual* lazy chunks
  (`import("../panes/welcome/Welcome")`, `import("../panes/session/Session")`,
  `import("./DockHost")`) and whose comment states the principle verbatim: "the
  slow part of lazy-loading is the transform/import work, an awaitable completion,
  not something to race with a widened findBy deadline." The two `index.test.tsx`
  files paraphrased that comment but warmed the wrong module.

**Why it was a rare transient (not deterministic):** in the full suite,
`AppShell.test.tsx` / `Session.test.tsx` import `Session.tsx` early, warming Vite's
shared (main-process) transform cache for every worker; by the time
`session/index.test.tsx` renders, the transform is cached and fast. The race only
opens when that file transforms Session first *and* the box is loaded — exactly the
subset-run + parallel conditions the dev streams hit.

### Why the toast-singleton hypothesis is disproven
- `src/widgets/toast/store.ts` already ships `resetToastStoreForTests()`; every
  test that touches the store resets it in `beforeEach`/`afterEach`
  (`toast.test.tsx:6-10`, and on W7 all its new settings-section tests).
- Vitest 4 default `isolate: true` gives each test file a fresh module graph, so
  the `let toasts` singleton is fresh per file anyway.
- The reproduced failure shows an **empty** DOM, not leftover toasts.
- The full suite (where all toast-touching files run together) never flaked from
  bleed across 20+ runs.

## The fix and its relationship to W7's fix

**Fix (mine):** warm the real lazy chunk in `beforeAll`, keeping `./index` for
`registerPane`:

- `src/panes/session/index.test.tsx` — add `await import("./Session");`
- `src/panes/welcome/index.test.tsx` — add `await import("./Welcome");`
  (`welcome/index.test.tsx` had the **identical latent defect** — awaited `./index`,
  left `./Welcome` cold — and its last test does the same lazy-render + `findByText`.
  Not a reported sighting, but the same root cause; fixed to prevent the same flake.)

Both edits also rewrite the stale comment to describe what is actually warmed and
why. This is "await the real completion," not a widened timeout — consistent with
the codebase's own AppShell.test.tsx rationale.

**Relationship to W7 — the brief's premise is a misdiagnosis; flag for the
controller:**

- W7 **never touched** `src/widgets/toast/**` (empty diff vs the branches'
  merge-base `d40a2e5cee`). **There is no shared toast-singleton fix my branch is
  missing.** Nothing toast-related to port.
- What W7 actually did near "AppShell/Session" is commit `3d58927ab`
  ("settings pane shell, nav, and routing wire-up"): it added
  `await import("../panes/settings/Settings")` to `AppShell.test.tsx`'s `beforeAll`
  — i.e. **the same lazy-import-warm mechanism**, applied to the Settings pane,
  because W7's new settings deep-link tests render `<AppShell/>` and route to a cold
  Settings chunk. Sighting #2 (AppShell/Session, "suspected toast-singleton") is the
  **same root cause**, and W7 already fixed its instance.
- The only toast thing W7 did is add `resetToastStoreForTests()` to its **own new**
  settings-section test files' `beforeEach` (files that push toasts). That is
  correct local hygiene, scoped to files that **do not exist on my branch**. This is
  almost certainly what got loosely narrated as a "toast-singleton test-isolation
  fix."

**So my fix is SAME MECHANISM, DIFFERENT FILE from W7's** (W7 warmed Settings in
`AppShell.test.tsx`; I warm Session/Welcome in the two `index.test.tsx`). W7 left
both `index.test.tsx` untouched (empty diff vs merge-base), so **there is no merge
conflict at integration** — the changes are additive and disjoint. Controller
action needed: none for toast; just be aware the "toast isolation" framing was
wrong, and that the general rule is "any test that renders a `React.lazy` chunk and
asserts via `findBy` must warm that chunk in `beforeAll`" (AppShell.test.tsx already
complies; the two index tests now do too).

## Post-fix proof

All from `cmd/serf-hub/frontend`, exit-code checked.

| Run | Conditions | Result |
|---|---|---|
| Pre-fix baseline | `index.test.tsx` cold, under load, ×15 | **11 fail / 15** (73 %) |
| Mutation (warm line removed) | cold, under sustained load, ×8 | **8 fail / 8** (100 %) — bites reliably |
| Post-fix targeted | cold, under load, ×20 then ×12 | **32 pass / 32** (0 fail) |
| Full suite (clean) | ×3 uncontended | **136 files / 2023 tests pass** each |
| `npx tsc --noEmit` | — | pass |
| `npm run lint` (biome ci) | 409 files | pass, no findings |
| `npm run build` | + `git restore dist/PLACEHOLDER` | pass |

The mutation A/B is the clincher: **remove the one line → 8/8 fail; restore it →
32/32 pass**, under identical sustained load. The cluster-shuffle repro seed was
`1784678033701`.

## Secondary observation (OUT OF SCOPE — not a cluster member, not fixed)

Under a **deliberately pathological** load (two full test campaigns oversubscribing
the CPU, stretching the 14 s suite to 44 s), `src/protocol/tokenFlood.test.tsx`'s
"500 deltas … render-count probe" failed **once** with `Test timed out in 5000 ms`
(the whole-**test** budget, a different mechanism from the 1000 ms `findBy`
deadline). It statically imports `Session` (`:13`), so it is **not** the lazy-import
root cause — it is a genuinely compute-bound render probe hitting the test wall only
when the CPU is ~3× oversubscribed. It passed all 12 earlier clean full-suite runs
and all 3 final clean runs; it is not one of the three sightings. I did **not**
"fix" it: widening its `testTimeout` would violate the await-behavior-not-timeouts
principle, and reducing the probe's work is a product/test change outside this
stream's sightings and file manifest. Recommend the controller note it only if CI
runners are CPU-starved; otherwise it does not reproduce under normal parallelism.

## Files changed
- `cmd/serf-hub/frontend/src/panes/session/index.test.tsx`
- `cmd/serf-hub/frontend/src/panes/welcome/index.test.tsx`

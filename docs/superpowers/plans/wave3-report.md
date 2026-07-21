# Web Rewrite Wave 3 — Report (M3 workspace shell)

Status: COMPLETE. All seven tasks done (T3∥T4∥T5 parallel streams + T6 Go stream, each merged
back to the wave branch; T7 the sequential wave close), live proof captured (merge-restore and
the gesture-back fix, both CDP-verified against a built hub), full gate green. Wave branch:
`w3-shell`, HEAD `b731ff094`. Not yet merged to integration — outside this task's own scope.

## What shipped

- **Shell skeleton** (T1): pane registry (register/lookup/singleton semantics); hand-rolled
  `shell/routing.ts` (`paneToURL`/`urlToPane`/`navigate`) instead of the plan's stated
  react-router, since the actual surface — 7 known path shapes, no nested routes or loaders — is
  ~90 lines of plain functions (see Deviations); client context; the welcome pane; `AppShell`
  wiring it all together. `App.tsx` rewired: dev harness demoted to `/dev/harness`.
- **Dockview desktop host** (T2): `DockHost` over the real `dockview-react` library;
  `shell/workspace.ts` (`openPane`/`closePane`/`focusPane`/`layoutJSON`/`restoreLayout`) as the
  single source of truth dockview mirrors; layout persistence to `localStorage`
  (`serf.workspace.layout.v1`, 400ms-debounced); `dockview-theme.css` restyling dockview's color
  custom properties onto `--surface`/`--edge`/`--ink` only; the session pane placeholder (proves
  deep-link → `openPane` → real dockview panel end to end, no real transcript yet).
- **Tree rail** (T3): `stores/tree.ts` (`GET /api/tree`, debounced refetch on qualifying
  notifications); `shell/rail/railNodes.ts` (pure tree-shaping: session/project/loading nodes,
  expand-state resolution); `RailRow` (row rendering, Cadence liveness dot, favorite star,
  actions menu); `Rail` (collapse chrome, rename/delete-project confirmation dialogs);
  `actions.ts` (REST helpers: favorite/rename/archive/delete-project).
- **Mobile stack host** (T4): `useIsMobile()` (reactive `<900px` breakpoint hook); `StackHost`
  (one focused pane full-screen, its own component-local back-stack, URL sync); `TreeDrawer`
  (bottom sheet housing the rail on mobile, `children` as its entire integration contract).
- **Connection/auth chrome** (T5): `auth.ts` (401 detection via a plain `fetch("/")` probe);
  `chrome/webNotBuilt.ts` (503 "web app not built" detection); `ToastRegion`; `ConnectionBanner`
  rewritten for a real fresh-client retry (never `window.location.reload()` — `AppwireClient`'s
  own `connect()` is single-shot and never resets); `threads.ts` reactively rewires its
  notification/ready handlers whenever `connectionStore`'s client identity changes.
- **`serf/tree/changed` hub broadcast** (T6, Go): `ScopeHub` notification on roster refresh
  deltas, past-index changes, and all four mutation types (archive/favorite/rename/
  project-delete) — exactly once per successful mutation, reusing `Roster`/`PastIndex`'s own
  `SetOnChange` fingerprint gating rather than a new diff. Tree-wire gaps closed alongside it:
  live and orphan-live rows now carry `Tier`/`Favorite`/`Rename`; `hubapi.TreeProject` gained
  `Favorite`.
- **Wave close** (T7, this task): DockHost's boot now *merges* a restored layout with a routed
  deep link (base + focused addition) instead of suppressing restore outright; `DockHost` is
  `React.lazy()`-loaded so dockview (~637kB) never reaches the mobile bundle;
  `AppwireClient.retryNow()` plus a "Retry now" button on `ConnectionBanner`'s reconnecting
  state; the rail's project rows gain pin/unpin now that the wire carries it; `StackHost` now
  tells a real browser back/forward apart from its own in-app Back button
  (`event.isTrusted`, live-verified via a CDP-driven back); device/theme/viewport screenshots
  against a built hub; the wave gate.

## Review-caught defects (all fixed + independently re-verified)

Deep-link vs. saved-layout precedence — DockHost's `handleReady` restored a stale layout
*unconditionally*, wiping out an already-routed deep link (Task 2 review, Critical; fixed there
to "routed pane wins outright"; upgraded in Task 7 to the richer merge behavior the same review
deferred) · AppShell render-phase-vs-effect route-opening race, twice (React child-before-parent
effect ordering meant a plain `useEffect` always lost to DockHost's own `onReady`; a second,
narrower shape surfaced when DockHost's first-ever mount wasn't AppShell's first-ever render —
both live-reproduced, Task 2) · pane-id collision after a restore, self-caught during Task 7's
own merge-restore TDD cycle (a freshly-minted post-restore id can collide with one the restored
layout already used, since both counters reset to 0 per page load — `restoreLayout()` now bumps
past every id it restores) · nested Menu triggers breaking Tree's roving tabindex, plus an
unstopped ArrowDown bubbling into `moveTo()` and silently relocating focus (Task 3 review,
probe-verified both directions) · `StackHost`'s dead-pane back-target skip had zero test
coverage, and `useIsMobile` could stick on a stale render-time viewport snapshot (Task 4 review,
two Important findings) · `threads.ts` stranding an already-open pane's notifications on a client
swap — caught by the implementer's own trap test before it ever shipped (Task 5) ·
`serf/tree/changed` double-broadcasting on rename/project-delete, then two residual zero-broadcast
gaps, then live/orphan-live rows never stamped `Tier`/`Favorite`/`Rename` (Task 6, three
successive review rounds, each with a break/confirm/restore verification) · `StackHost`'s own
Back button moving the user *forward* to the pane a real browser back had just left (Task 7,
live-reproduced via a CDP-driven back against a built hub, not by inspection).

## Deviations from the plan (all controller-ruled or independently justified, recorded in the SDD ledger)

- **react-router is installed (`^8.2.0`, not the plan's stated v7) but never used.** Task 1 chose
  a hand-rolled `routing.ts` — pure `paneToURL`/`urlToPane` transforms plus one `navigate()`
  helper — since the actual routing surface never needed nested routes, loaders, or a component
  tree. `paneToURL`/`urlToPane` have zero router dependency, so a later wave can still introduce
  react-router without touching their contract. Worth a plan-doc correction; not a functional gap.
- **`AppwireClientLike` gained two additive methods across the wave**: `connect()` (Task 1 fix
  wave, needed to exercise the `serverInfo`-population duty against an injected client) and
  `retryNow()` (Task 7). Both are pure additions to the locked client-facing surface.
- **`PaneTitleCtx` gained an optional `threadName` lookup** (Task 2) — Task 1's own report
  flagged this exact seam ("left empty and documented... add fields when a real title()
  implementation needs them") and the session pane's tab title is that implementation.
- **`hubapi.TreeProject` gained `Favorite`** (Task 6, tree-wire gaps round) — a gap the rail
  stream's own review found and relayed cross-task, not something Task 3 could fix inside its own
  frontend-only scope.
- **Task 7's "Retry now" button reads the client from `connectionStore`, not `useClient()`'s
  React context**, despite the punch list's literal wording. `useClient()` is fixed to whatever
  `AppShell` constructed at mount (Global Constraints: "one `AppwireClient` per window... injected
  via context") and goes stale the instant `ConnectionBanner`'s own manual retry swaps
  `connectionStore`'s client to a fresh instance — a genuine correctness hazard, not a stylistic
  preference, verified by tracing exactly when each value diverges. `connectionStore`'s client is
  always the one actually reconnecting; `ConnectionBanner` already reads it that way for its
  closed-reason re-probe, for the identical reason.
- **The "component-local, ONLY IF it stays within StackHost" gesture-back fix is real but
  partial**, disclosed as such rather than oversold: a single real back/forward composes
  correctly with the in-app Back button; two consecutive real back/forward steps remain a
  narrower residual (pinned by its own test), since fully closing that needs tracking
  `window.history`'s own position — a bigger seam than a component-local fix, the same conclusion
  Task 4's own report already reached for the whole problem before this narrowed it.

## Standing patterns for later waves

- **Go `omitempty bool` fields need no `normalize*` handling** — unlike nullable arrays (still
  `?? []` everywhere), a wire-nullable *bool* just collapses to present-when-true/absent-when-
  false; type it as a plain optional (`favorite?: boolean`) and nothing else changes.
- **Restored/persisted ids can collide with freshly-minted ones whenever both sides reset their
  counter to the same starting point independently** (here: `nextPaneSeq` resets to 0 every page
  load, and a saved layout's ids came from a *different* page load's own independently-reset
  counter) — mint fresh ids only after bumping the counter past whatever was just restored, not
  before.
- **Harness fidelity: `Event.isTrusted` cannot be forged from script — in jsdom or any real
  browser** (`Object.defineProperty` throws on a real instance; this is spec-mandated, not a
  jsdom gap). When a feature genuinely needs to know "did a real user gesture cause this," the
  testable seam is a module-level flag with its own test-only setter (mirrors `workspace.ts`'s
  `registerDockviewApi`/`resetWorkspaceStoreForTests` precedent) — never attempt to construct a
  fake trusted DOM event.
- **Gate hygiene: `make build-hub`'s own target order embeds a stale frontend.**
  `build-hub: build-runtime build-web` runs the Go build (which `//go:embed all:frontend/dist`
  captures at compile time) *before* the frontend rebuild that would have refreshed `dist/` — so
  a single `make build-hub` invocation always embeds a `dist/` that's one build cycle behind
  source. Confirmed by tracing served bytes directly (`curl` with the auth Bearer token,
  bypassing any browser cache entirely) after a live gesture-back fix appeared not to take
  effect. To verify a frontend change against a live hub: build the frontend first
  (`npm run build`), *then* the Go binary — never rely on one `make build-hub` call picking up
  same-session source edits. Not fixed here (build tooling, outside this task's scope) — flagging
  for whoever next touches the Makefile.
- **A client-side cache-bust proves nothing about server-side embed staleness.** A `?_cachebust=`
  query parameter forces the browser to skip its own cache, but the server still returned the
  same stale bytes — the giveaway was `document.scripts[0].src` still naming the old build's
  content-hashed filename after a "fresh" navigation. Verify with a request tool that bypasses
  the browser entirely (`curl`) whenever a rebuild "isn't taking."

## Verification

```
cmd/serf-hub/frontend:
  npx vitest run   → EXIT=0  (873 passed, 61 files; 2 full reruns, identical both times)
  npx tsc --noEmit → EXIT=0  (no output)
  npx eslint src    → EXIT=0  (no output)
  npm run build     → EXIT=0  (main bundle ~311kB, DockHost chunk ~351kB — both under Vite's
                                500kB warning threshold; no chunk-size warning, down from Task
                                2's original ~637kB single bundle)

go test ./cmd/serf-hub/...  → EXIT=0  (all packages ok; re-run with -count=1 to bypass the
                                        build cache for a genuine second execution, identical)
```

Live proof (chrome skill, CDP-driven, against a built `serf-hub` with `SERF_HUB_WEB=new` on a
non-default port): merge-restore (a saved 4-tab layout survives a fresh deep-linked page load,
the new tab appended and focused) and the gesture-back fix (a real back gesture followed by the
in-app Back button now lands on welcome, not back on the page just left) both confirmed against
the actual built binary, not just the automated suite. Screenshots:
`.superpowers/sdd/w3t7-screens/`.

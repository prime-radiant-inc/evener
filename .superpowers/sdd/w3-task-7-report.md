# Wave 3 Task 7 report — the wave close

Branch `w3-shell`, off the assembled tip `c7f49e153` (all six streams merged: shell skeleton,
dockview host, rail, mobile stack, chrome, tree push). 5 commits, each its own TDD cycle. Full
suite green (873 tests, 61 files; 2 full reruns, identical both times), `tsc --noEmit` clean,
`eslint src` clean, `npm run build` clean, `go test ./cmd/serf-hub/...` clean (both a normal run
and a `-count=1` cache-bypassed rerun). Zero touches to any `.go` file (`git diff --stat
c7f49e153..HEAD` — 20 files, all under `cmd/serf-hub/frontend/src/`).

## Commits

| Commit | Unit |
|---|---|
| `1460a1e06` | DockHost merges the restored layout with the routed pane (+ workspace.ts id-collision fix) |
| `e29e04dfd` | lazy-load DockHost so mobile never fetches dockview |
| `551443c8f` | AppwireClient.retryNow() + ConnectionBanner's Retry now |
| `cc5bb99b6` | rail adopts TreeProject.Favorite — project rows pin/unpin |
| `b731ff094` | StackHost distinguishes a real browser back from an in-app one |

## 1. Merge-restore

**The bug, precisely.** DockHost's `handleReady` only ever attempted `restoreLayout()` when
`workspaceStore.getState().panes.length === 0`. In the *real*, AppShell-integrated app, that
condition was true almost exactly never: `AppShell`'s routing glue always opens *something*
during render before `DockHost` ever mounts — a deep-linked session, or the plain `welcome`
pane for `/` itself. So the restore branch was live code only in `DockHost.test.tsx`'s own
standalone-mount tests (nothing pre-opens a pane there); every *real* page load with a saved
layout skipped it. Traced this by re-reading `AppShell.tsx`'s `openRouteAsPane` before touching
anything — it was not a hypothesis, the code makes it unconditional.

**The fix.** `handleReady` now: captures whatever's already in `workspaceStore.panes` (type +
params, not id — see below for why) *before* attempting restore; if a stored layout exists,
restores it unconditionally (replacing `panes` wholesale, same as before); then re-opens each
captured entry via the normal `openPane()` path, which both appends it on top of the restored
set *and* naturally dedupes against an identical pane the restored layout might already contain.
The existing "still empty → open welcome" fallback runs last, unchanged in shape, now correctly
covering both "nothing was ever routed and nothing was saved" and "restore succeeded into an
empty saved layout."

**A second, real bug the merge design exposed: pane-id collision.** Restored panel ids come from
a *previous* page load's own independently-numbered counter (`nextPaneSeq`, reset to 0 every
fresh load). Re-opening a captured pane *after* restore mints its id from *this* page's own
counter — which can legitimately produce the exact same string a restored panel already uses
(both sessions' first-ever pane is `pane_doc_1`, etc.). Before this task, that only mattered if a
user opened a *new* pane sometime after a restore — a latent, rare hazard. The merge-restore
design makes it hit on nearly every boot that has both a saved layout and a routed deep link, so
it needed fixing here: `restoreLayout()` now bumps `nextPaneSeq` past every id it just restored.
Caught by my own test, not inspection — see TDD evidence.

**TDD evidence.** Rewrote the DockHost-level test that encoded the *old* "wins alone" semantics
into "a routed pane opened before mount merges into a stale saved layout as its focused member" —
deliberately crafted so both phases' `nextPaneSeq` counters produce colliding id suffixes (both
reset via `resetWorkspaceStoreForTests`), so the SAME test proves both the merge behavior and the
collision fix. Ran RED against the unmodified `handleReady` first (only the routed pane's tab
appeared, none of the restored ones) — confirmed for the right reason. Added a workspace.ts-level
unit test for the collision fix in isolation (`FakeDockviewApi`, no real dockview), verified RED
by temporarily stashing the `bumpPastRestoredIds` call and confirming the exact expected failure,
then restored. Added the two other required scenarios: "no saved layout" (already covered,
unchanged, by the pre-existing "falls back to opening welcome" test — proven identical before and
after by construction, not coincidence) and "corrupt layout + deep link → deep link wins alone"
(new test; `restoreLayout`'s own failure path already clears the store to empty before the
re-open runs, so this needed no special-casing in `handleReady` at all).

One downstream test needed updating for the same reason: `AppShell.test.tsx`'s "a saved layout
from a previous session doesn't suppress a fresh deep link" asserted exactly one tab after a
fresh deep link — the old "wins alone" behavior. Renamed and rewritten to assert the restored
tab is *also* present, with the deep link appended and focused/active.

## 2. Bundle split

`AppShell.tsx` now `React.lazy()`s `DockHost` (a named export, adapted with `.then((m) => ({
default: m.DockHost }))`, same pattern `App.tsx`'s own `DevHarnessRoute` already uses), wrapped
in `<Suspense fallback={null}>` — `fallback={null}` because `DockHost`'s own boot sequence
already produces the first meaningful paint synchronously once its chunk resolves; there's no
useful intermediate state to show. `StackHost` (mobile) is unaffected — it was never the one
pulling in dockview.

**Evidence, not assertion**: `npm run build`'s own output now shows `DockHost-*.js` (~351kB) and
`DockHost-*.css` (dockview's own stylesheet, imported inside the lazy-loaded module) as separate
chunks; the main bundle drops from ~637kB to ~311kB (both now under Vite's 500kB warning
threshold — the chunk-size warning Task 2's report flagged is gone). Confirmed the split is
genuinely lazy, not just separately chunked: `dist/index.html` references only the main bundle
script; grepping the main bundle's own source for the DockHost chunk's filename finds it inside
a dynamic `import()` call, never a static `<script>` tag.

`AppShell.test.tsx` and `App.test.tsx` (the two files rendering through the new lazy boundary)
now pre-warm `import("./DockHost")` / `import("./shell/DockHost")` in their own `beforeAll`,
following `App.test.tsx`'s own established pattern exactly (its header comment: "the slow part of
lazy-loading... is an awaitable completion — not something to race with a widened findBy
deadline") — no timeout anywhere was touched.

## 3. retryNow

`AppwireClient.retryNow()`: no-op unless `state === "reconnecting"`; otherwise clears the pending
backoff timer and calls the same `attemptReconnect()` the timer itself would have, immediately.
Deliberately does not reset `reconnectAttempts` — a failed manual attempt falls back into the
same backoff sequence it would have anyway, just one attempt further along. A new
`reconnectInFlight` flag (set for `attemptReconnect()`'s whole duration) guards against a second
`retryNow()` call — or the backoff timer firing concurrently, though that case can't actually
arise given `retryNow()` disarms the timer first — stacking a second dial on top of one already
in flight.

TDD with fake timers in `protocol/reconnect.test.ts`, mirroring that file's own `dialer()`/
`socketAt()`/`flushUntil()` helpers: mid-backoff `retryNow()` dials instantly (zero time
advanced, backoff timer count drops to 0 with no new one armed); a successful `retryNow()`
reaches ready, refires `onReady`, resumes the heartbeat, exactly like an ordinary reconnect;
calling it twice while the first attempt is still waiting on its own socket to open dials only
once; a failed `retryNow()` attempt falls back to the *next* backoff delay in the normal
doubling sequence (not reset to the base), confirmed by advancing exactly that delay and no less;
explicit no-op checks for `"ready"` and `"closed"` states.

`AppwireClientLike`/`FakeClient` gained `retryNow()` (a call-count stub — `FakeClient` doesn't
model timers/sockets at all, so there's nothing for it to actually short-circuit; its only job in
tests is proving the UI wiring calls it).

**Deviation, disclosed**: `ConnectionBanner`'s new "Retry now" button reads the client from
`useConnectionStore((s) => s.client)` — already destructured in the component for the
closed-reason re-probe — not `useClient()`'s React context, despite the punch list's literal
"via the client context" wording. Traced why before deciding: `useClient()`'s value is fixed to
whatever `AppShell` constructed at mount (a `useState` lazy initializer that never updates), but
`ConnectionBanner`'s *own* `handleRetry` (the "closed" state's existing Retry button) swaps
`connectionStore`'s client to a fresh instance on every manual retry — after which `useClient()`
would return a permanently-dead, already-`"closed"` orphan. A user who retries from "closed",
reaches "ready" on the fresh client, then later drops back into "reconnecting" would have "Retry
now" silently do nothing if wired to context. `connectionStore`'s client is always the live one —
verified this is exactly why the closed-reason effect already reads it that way, for the
identical reason.

## 4. Rail wire fields

`TreeProject` (`stores/tree.ts`) gained `favorite?: boolean`, mirroring the hub's own
`omitempty bool` field (Task 6's tree-wire gaps round) field-for-field — no `normalize*` change
needed, since (unlike the nullable arrays this file already handles) a wire-nullable bool just
collapses to present/absent, identical to how `TreeNode.favorite` was already typed.

Project rows gain a favorite star (mirroring `SessionRow`'s existing one) and an "Add/Remove
pinned" menu item, wired through the *already-generic* `setFavorite("project", project.key, ...)`
— `/api/favorite` already accepted `kind:"project"`; only the frontend's read (wire type) and
write (row affordance) sides were missing it. The synthetic `"no-project"` bucket keeps its
existing all-or-nothing exclusion (no actions at all) rather than special-casing favorite in —
its own favorite validation is a separate, already-disclosed gap (Task 6's own report) this task
doesn't touch.

Investigated the punch list's "remove the tier-heuristic caveat comments" line directly against
the current source rather than assuming what it meant: `session.favorite`/`session.rename` were
*already* consumed with no tier gating anywhere in `RailRow.tsx` — the one concrete stale
assumption left in the codebase was a single test, "menu never offers Favorite or Rename for a
project row (not supported by the wire shape)," whose *Favorite* half was made false by Task 6.
Split it: kept the Rename half (still true — projects have no rename concept at all), added the
new favorite coverage. Also added an explicit test pinning favorite+rename working on a
`tier: "live"` row specifically (the exact shape Task 6's Go fix targeted) — this was already
correct by construction (no tier gate exists to break it), so this is pinning coverage for
already-correct behavior, not a red-first fix, disclosed as exactly that distinction.

## 5. Gesture-back adjudication

**Live-reproduced first, not guessed at.** Built the wave (`make build-hub`), ran it with
`SERF_HUB_WEB=new` on port 9280, drove it via the chrome skill at a 390px viewport: two in-app
forward navigations (welcome → session A → session B), then a *real* browser back (CDP's `back`
action, not a synthetic `dispatchEvent`) landing on session A, then a tap of the in-app Back
button. Observed: the tap moved the user to session B — forward, not backward — exactly the
composition gap `StackHost.tsx`'s own comment disclosed and left unfixed.

**Verified the disclosed "can't fix" reasoning was actually wrong, live, before deciding to
fix.** The comment claimed "PopStateEvent itself carries nothing distinguishing" a real
back/forward from `routing.ts`'s own synthetic dispatch. Installed a `popstate` listener logging
`event.isTrusted`, dispatched one synthetic popstate (`false`) and drove one real CDP back
(`true`) — confirmed live, `event.isTrusted` is exactly the missing signal, and it's a standard,
unspoofable DOM property (confirmed separately: `Object.defineProperty` to fake it on a real
Event throws, "Cannot redefine property," both in this jsdom version and understood to hold in
every real browser per spec — a script can never construct a trusted event).

**Fix, decided in scope**: the task's own bar was "implement the disclosed fix shape ONLY if it
stays within StackHost." `isTrusted`-based detection genuinely does — no other file needs
touching. Implemented it: a `useEffect`-installed `popstate` listener records `isTrusted` into a
module-level flag (a *real* Event's `isTrusted` can't be faked from a test, so a
`setLastPopstateWasTrustedForTests` test-only setter mirrors `workspace.ts`'s own
`registerDockviewApi`/`resetWorkspaceStoreForTests` precedent instead); the bookkeeping effect
skips pushing onto the local back-stack when the immediately-preceding popstate was real;
`popValidBackTarget` also skips a candidate that equals the *current* focus (a real back/forward
can leave the stack's own top stale at exactly that value — without this, the fix alone would
trade "moves forward" for a different confusing symptom, "the first tap does nothing").

**Disclosed honestly, not oversold**: this fixes exactly one real back/forward step. A *second*
consecutive real back is indistinguishable, from this component's own vantage point, from any
other non-popstate-driven focus change, so the stack's stale top entry from the original forward
walk can resurface again. Traced through by hand why (the flag only knows "the last popstate was
real," not how many real history steps actually happened) and pinned it with its own test rather
than leaving it merely asserted in prose — fully eliminating it needs tracking
`window.history`'s own position, the "bigger seam" the original comment already named.

**Rebuilt and re-verified the fix live**, not just in jsdom — this caught a real, unrelated
process trap: `make build-hub` runs `build-runtime` (the Go build, which `//go:embed`s
`frontend/dist` at compile time) *before* `build-web` (which regenerates `dist/`), so a single
`make build-hub` invocation always embeds a `dist/` one cycle stale. Confirmed the served bytes
directly via `curl` with the auth Bearer token (bypassing any browser cache question entirely)
before concluding the fix wasn't working — it was; the *binary* wasn't fresh. Rebuilt in the
correct order (`npm run build` first, then the Go binaries) and re-verified: the real-back +
in-app-Back sequence now lands correctly on welcome, one tap, live.

## 6. Device/viewport smoke + screenshots

Built (frontend-then-Go, per the ordering finding above), ran `SERF_HUB_WEB=new ./serf-hub -addr
127.0.0.1:9280 -serf ./serf` (non-default port; `-addr`/`-serf` confirmed via `./serf-hub -h`,
not guessed), authenticated via the `[hub] auth URL` stderr line (`GET /auth?token=...`, sets the
`serf_hub_auth` cookie). Captured, all against the verified-fresh build:

- `01-desktop-1440-dark-rail-welcome.png` — 1440×900, dark (default), rail + welcome.
- `02-desktop-deeplink-merges-into-restored-layout.png` — a saved 4-tab layout (Welcome +
  3 sessions, built via real save round-trips) survives a *fresh* page load at a new deep link;
  the new tab is appended and focused — live proof of item 1, not just the automated suite.
- `03-mobile-390-stackhost-drawer-open.png` — 390×844, `StackHost` full-screen + the tree drawer
  (bottom sheet) open.
- `04-desktop-1440-light-theme.png` — `data-theme="light"` (the app's own real theming
  mechanism, `tokens.css`'s `:root[data-theme="light"]` selector — no production toggle exists
  yet, so this is applied directly, the same mechanism the app itself would use once one ships).
- `05-gesture-back-fix-live-proof.png` — the item-5 fix's own live confirmation.

Enabled console logging (`enable_console_logging`/`get_console_messages`) at both viewports after
a fresh navigation each time — zero messages captured either time (no warnings, no errors).
Killed the hub afterward; `pgrep -f "\./serf-hub"` confirmed clean.

Saved to `.superpowers/sdd/w3t7-screens/`.

## Files

Modified only (no new files):
- `cmd/serf-hub/frontend/src/shell/{DockHost.tsx,DockHost.test.tsx}`,
  `shell/{workspace.ts,workspace.test.ts}` — item 1.
- `cmd/serf-hub/frontend/src/{App.test.tsx,shell/AppShell.tsx,shell/AppShell.test.tsx}` — item 2.
- `cmd/serf-hub/frontend/src/protocol/{client.ts,reconnect.test.ts,testing/fakeClient.ts}`,
  `shell/{ConnectionBanner.tsx,ConnectionBanner.test.tsx}` — item 3.
- `cmd/serf-hub/frontend/src/stores/{tree.ts,tree.test.ts}`,
  `shell/rail/{Rail.tsx,Rail.test.tsx,RailRow.tsx,RailRow.test.tsx}` — item 4.
- `cmd/serf-hub/frontend/src/shell/mobile/{StackHost.tsx,StackHost.test.tsx}` — item 5.
- `docs/superpowers/plans/wave3-report.md` (new), `.superpowers/sdd/w3t7-screens/*.png` (new) —
  items 6-7.

## Self-review

- **Redid every screenshot against a build I'd personally confirmed fresh** (via `curl` +
  `document.scripts[0].src`), after discovering the `make build-hub` ordering trap mid-task —
  the first round (still functionally valid for what it proved: item 1, general chrome/theme
  rendering) predated items 3-5's frontend code in the actually-running binary. Not left as a
  known gap; redone rather than hand-waved.
- **`bumpPastRestoredIds`'s regex (`/_(\d+)$/`) is best-effort**, not a strict pane-id-format
  parser — a future id scheme without a trailing numeric suffix would simply not get bumped
  against (safe: worst case reverts to the pre-fix collision risk for that one shape, never a
  crash). Matches `nextPaneId`'s own literal format (`pane_<type>_<n>`) exactly today.
- **The `popValidBackTarget` signature grew a third parameter**
  (`currentFocusedPaneId: string | null`) rather than a wrapping options object — kept positional
  since it's a small, private (unexported) function with exactly one call site, consistent with
  its own existing two-positional-argument shape.
- Ran the full suite, typecheck, lint, and build after *every* commit in this task, not just
  once at the end — each item's own commit message states the gate it passed.

## Concerns

- **`make build-hub`'s embed-ordering trap** (see item 5) is a real, reproducible build-tooling
  issue, not fixed here (out of this task's scope — build infrastructure, not the workspace
  shell). Flagged in `wave3-report.md`'s Standing Patterns section for whoever next touches the
  Makefile.
- **The gesture-back fix's own residual** (two consecutive real back/forward steps) is disclosed,
  pinned by a test, and — per the task's own explicit framing — an accepted boundary, not
  something I judged worth the bigger `window.history`-position rearchitecture this task's
  component-local constraint ruled out.
- **Items 3 (retryNow) and 4 (rail favorites) were not separately live-screenshotted** — reaching
  a "reconnecting" state or having real session data live both need infrastructure (a killed
  socket mid-session; a spawned agent session) beyond what a placeholder-pane smoke test needed
  for items 1/5/6. Both are exhaustively unit-tested (11 new `reconnect.test.ts` cases; 6 new
  rail tests); flagging the narrower live-verification scope rather than implying they were
  clicked through in the browser too.

## Verification

```
cmd/serf-hub/frontend:
  npx vitest run   → EXIT=0  (873 passed, 61 files; 2 full reruns, identical both times)
  npx tsc --noEmit → EXIT=0  (no output)
  npx eslint src    → EXIT=0  (no output)
  npm run build     → EXIT=0  (main ~311kB, DockHost chunk ~351kB, no chunk-size warning)

go test ./cmd/serf-hub/...            → EXIT=0  (all packages ok)
go test -count=1 ./cmd/serf-hub/...   → EXIT=0  (genuine re-execution, not the build cache,
                                                   identical)
```

Live: merge-restore and the gesture-back fix both confirmed against a built, running
`serf-hub -addr 127.0.0.1:9280` with `SERF_HUB_WEB=new`, via the chrome skill. Console clean at
both viewports. Hub killed; `pgrep -f "\./serf-hub"` clean afterward.

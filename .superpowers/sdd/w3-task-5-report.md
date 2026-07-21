# Wave 3 Task 5 report — connection/auth chrome + the real retry

Branch `w3-chrome`, off `2b7adbf4e`. 7 commits, scope verified via
`git diff --stat 2b7adbf4e HEAD`: exactly the owned files
(`src/auth.ts(+test)`, `src/shell/ConnectionBanner.*`, `src/shell/chrome/**`)
plus the two sanctioned cross-boundary edits (`src/stores/threads.ts` +
`threads.test.ts`'s wiredClient rewire; `src/stores/connection.ts`'s stale
comment). `AppShell.tsx` untouched throughout.

## Commits

| Commit | Unit |
|---|---|
| `8c1e945d8` | threads.ts reactive rewiring (the trap fix) + connection.ts/threads.test.ts sanctioned folds |
| `d9e577030` | `src/auth.ts` — 401 detection |
| `20bb11f14` | `shell/chrome/webNotBuilt.ts` — 503 detection |
| `65f395c4f` | `NOT_BUILT_MESSAGE` addition |
| `128468ac0` | `shell/chrome/ToastRegion.tsx` |
| `ec9948d44` | `ConnectionBanner.tsx` rewrite — real retry, banner states, auth/not-built chrome |

## TDD evidence — the trap test especially

Every unit: test written first, run to confirm the RIGHT red, then
implementation until green.

**The trap test** (`stores/threads.test.ts`, describe "client swap (manual
retry) rewiring"). Before touching `threads.ts`, I wrote two tests directly
against the described trap:

1. "swapping to a fresh client re-hydrates tracked refs, routes its
   notifications, and detaches the dead client's handlers (no double
   delivery)" — connects fake client A, hydrates `ref_a` through it, kills A
   (`emitStateChange("closed")`), then does **nothing but**
   `connectionStore.getState().connect(b)` for a fresh, already-ready fake
   client B (no `ensureThread`/`send`/etc. call follows — that's the whole
   point: it must work with no action call at all). Asserts, in order: B's
   own `thread/read` re-hydrates the tracked ref (B's scripted response
   deliberately differs from A's, so a stale snapshot would fail this);
   B's live notification reaches the model; the *same* notification shape
   injected via dead A does **not** — proving detachment, not just that B
   independently works.
2. "swapping to a fresh client that is not yet ready waits for its own
   onReady" — B starts `"connecting"`; asserts zero `thread/read` calls
   before `b.emitReady()`, exactly one after.

Ran both against the unmodified `threads.ts` first:
```
× swapping to a fresh client re-hydrates... : expected [...] length +0, got 1
  (flushUntil never saw B's re-hydrate land — stale A snapshot persisted)
× swapping to a fresh client that is not yet ready...: expected length 1, got +0
  (b.emitReady() was a no-op — nothing had ever subscribed to B)
```
Both red for the described reason, not a typo. Implemented `rewireClient()`
(detach old client's `onNotification`/`onReady` via their own unsubscribe
functions, attach the new ones, eagerly call `handleReady()` if the new
client is already `"ready"` since `onReady` only fires on a *future*
transition) plus a module-level `connectionStore.subscribe()` that calls it
reactively on every client-reference change. Both new tests went green
immediately; one pre-existing test ("wires onNotification/onReady... across
multiple store calls") broke as a **direct, correct** consequence — its
premise (spy attached *after* connect(), proving idempotency only across
later actions) no longer matches reality now that wiring happens at
`connect()` time. Rewrote it to attach spies *before* `connect()` and assert
wiring happens exactly once there, staying at one through subsequent
actions — a strictly stronger version of the same intent, not a weakened one.

Full `threads.test.ts`: 31/31 green (29 pre-existing + 2 new), isolated run
and full-suite run both clean.

**ConnectionBanner rewrite**: wrote the full new 17-test suite first
(new `createClient` prop, no "Reload" text anywhere, auth/not-built
messages), ran it against the *old* component — 13/17 failed, every failure
because the old component still said "Reload" / had no `createClient` prop /
never probed. Implemented the new component once; all 17 passed on the
first run.

## Auth-detection findings (src/auth.ts)

Read `cmd/serf-hub/internal/hubedge/auth_token.go` directly. `AuthGuard`
wraps the server's *entire* mux (`web.go`: `auth(httpsec.CSPMiddleware(mux))`)
before any route dispatch — every unauthenticated request gets 401,
including the `/rpc` WebSocket upgrade itself, rejected at the plain-HTTP
layer before the handshake ever completes. Browsers do not expose a failed
WS handshake's HTTP status to JS (`AppwireClient.waitForOpen` only ever sees
a generic error/close), so **a dropped `/rpc` connection cannot, by itself,
distinguish "wrong auth cookie" from "hub unreachable."** The only reliable
signal is a plain `fetch()`, which does expose `response.status`.
`checkAuthStatus()` fetches `"/"` (always registered, always guarded unless
the guard is disabled entirely) and reads `401` directly.

`/api/health` — the task's own hypothesized signal — is a dead end,
confirmed by reading `hubedge.isAuthExempt()`: it's explicitly exempt from
auth. It returns 200 unconditionally regardless of auth state; fetching it
can never observe a 401.

## 503 "web app not built" findings (shell/chrome/webNotBuilt.ts)

Read `cmd/serf-hub/webnext.go` + `web_api.go`. Both of the task's
hypothesized signals turned out false, verified against source rather than
assumed:
- `fetch("/api/health")` returning HTML — false. `handleAPIHealth` never
  touches `distFS()` and is also auth-exempt; always 200 JSON, built or not.
- "connect failing with a specific signature" — false. `/rpc` is registered
  unconditionally, independent of `dist/`; a dropped WS handshake carries no
  build-status information.

The real, honestly-scoped signal: `serveSPAIndex` (the only handler
registered for `"/"`) returns 503 when `dist/index.html` is missing.
`checkWebNotBuilt()` fetches `"/"` and reads that status directly — reliable
because no other code path can produce a 503 there.

**Structural limitation, documented at length in the file rather than
glossed over**: a cold page load hitting the 503 gets a `text/plain` body
with no `<script>` tag — this app's own JS never boots in that case, so
there is no running instance of the detector to run at all. This is not a
detection gap to fix; it's not fixable client-side, full stop. The one real
case this check earns its keep for: an *already-loaded* tab (from before
`dist/` went missing) whose connection later drops can still `fetch()` from
JS that's already running and tell "just reconnect" apart from "ask the
operator to build the frontend."

`AuthGuard` runs before `serveSPAIndex` ever sees a request, so 401 and 503
can never both describe the same response — the two checks never conflict.

## Mount contracts

- **`ToastRegion`** (`shell/chrome/ToastRegion.tsx`): render `<ToastRegion/>`
  exactly once, near the app root (e.g. AppShell's outermost div, alongside
  `<ConnectionBanner/>`) — never per-pane. It's a thin wrapper over
  `widgets/toast`'s own `<Toast/>` (already the aria-live region); any code
  anywhere can `useToasts().push(...)` without importing this component at
  all. **Not mounted in AppShell** — per this task's own constraint, that's
  the merge/integration step, one line.
- **`ConnectionBanner`**: mount point and required prop unchanged
  (`<ConnectionBanner state={connectionState}/>`, same as Task 1). The new
  `createClient` prop is optional with a real-`AppwireClient` default, so
  AppShell's existing call keeps compiling and behaving identically.

## Sanctioned folds

- `stores/connection.ts`'s `connect()` doc comment claimed
  "AppwireClientLike exposes no way to read back the InitializeResponse" —
  false since Task 1's fix wave added `connect()` to the interface. Rewrote
  to state the actual reason `serverInfo` stays unset here: this function
  only mirrors `ConnectionState`, it never calls the client's own
  `connect()` — each caller that *does* drive a handshake (AppShell's boot;
  now ConnectionBanner's retry) sets `serverInfo` itself. Verified my "only
  caller" framing against a full `grep` for `.connect()` call sites before
  writing it — `dev/DevHarness.tsx` also calls `client.connect()` (fire-and-
  forget, never sets `serverInfo`), so the comment names both real callers
  rather than overclaiming "the only place."
- `threads.test.ts:111`'s title had the identical stale claim; retitled and
  commented with the same corrected reasoning.

## Files

Created:
- `cmd/serf-hub/frontend/src/auth.ts`, `auth.test.ts`
- `cmd/serf-hub/frontend/src/shell/chrome/webNotBuilt.ts`, `.test.ts`
- `cmd/serf-hub/frontend/src/shell/chrome/ToastRegion.tsx`, `.test.tsx`

Rewritten (owned):
- `cmd/serf-hub/frontend/src/shell/ConnectionBanner.tsx`, `.test.tsx`
  (`.module.css` unchanged — same DOM shape, no new tokens needed)

Modified (sanctioned, narrow):
- `cmd/serf-hub/frontend/src/stores/threads.ts` — `rewireClient()` +
  `connectionStore.subscribe()`; `requireClient()` now delegates to it;
  `resetThreadsStoreForTests()` also calls the stored unwire functions
  before clearing them (previously reset `wiredClient` directly, which
  would have left a stale unwire closure from a discarded test client
  callable by the next test's first rewire — fixed for correctness, not
  just symmetry).
- `cmd/serf-hub/frontend/src/stores/threads.test.ts` — 2 new tests (the
  trap), 1 existing test's spy-timing rewritten (see TDD section), 1 title
  fix (sanctioned fold).
- `cmd/serf-hub/frontend/src/stores/connection.ts` — comment fix only
  (sanctioned fold).

## Self-review

- **`checkAuthStatus` and `checkWebNotBuilt` each own an independent
  `fetch("/")` call** rather than sharing one probe — both fire when a
  "closed" banner mounts, so two round trips where one would do. Considered
  unifying into a shared low-level fetcher; decided against it because
  neither `src/auth.ts` (top-level) nor `shell/chrome/` is the obvious owner
  of a generic "hub root probe" utility, and the actual duplication is ~3
  trivial lines (try/fetch/catch), not complex logic that risks drifting out
  of sync. Flagging explicitly in case Jesse would rather collapse it to one
  round trip.
- **No client is ever explicitly `.close()`'d when connectionStore swaps to
  a new one** — on inspection this isn't a new gap: a client only ever
  reaches the "closed" state (the precondition for the Retry button existing
  at all) by already having torn itself down internally (socket nulled,
  heartbeat/reconnect disarmed) via its own `close()`/failure path, so
  there's nothing left to release. This was already true before this task
  (nothing closes an outgoing client on any `connectionStore.connect()`
  swap, including AppShell's own).
- **Retry button disables + relabels ("Retrying…") while in flight** — not
  explicitly asked for, but directly serves "the real retry" being actually
  robust (prevents a double-click from spawning two concurrent fresh
  clients). Small, tested, and mirrors the existing disabled-state pattern
  other widgets already use.
- **No ARIA live-region on the banner itself** — matches the pre-existing
  Task 1 scope (never had one); not added here since it wasn't asked for and
  would be a new a11y surface, not part of "the real retry" or the auth/503
  chrome this task scopes.
- Ran the full suite, typecheck, lint, and build **after** every commit,
  not just before, including a final pass after the last commit.
- `npm run build` deletes and regenerates `dist/`, which wipes the
  git-tracked `dist/PLACEHOLDER` stub (`.gitignore`: `dist/*` then
  `!dist/PLACEHOLDER`) as a side effect. Caught this via `git status` after
  the first build run and restored it with `git checkout HEAD --`; did the
  same after the final build verification. Not a code change, just a
  verification-tooling side effect worth flagging so nobody mistakes it for
  intentional.

## Concerns

- The two-fetch duplication above (auth check + not-built check) — see
  self-review.
- `webNotBuilt.ts`'s check is real but structurally narrow (see findings
  above) — it will never fire for the most common real-world "not built"
  case (a cold load), only for an already-running tab whose build later went
  missing. Documented at length in the file; flagging here too so it isn't
  mistaken for broader coverage than it has.
- `ConnectionBanner`'s `handleRetry` doesn't guard against the component
  unmounting mid-retry (no `cancelled` flag around `setRetrying(false)`,
  unlike the probe effect). Verified this is currently unreachable — AppShell
  renders `<ConnectionBanner state={...}/>` unconditionally, so the
  component instance never unmounts across state transitions — so I didn't
  add speculative guard code for a scenario that can't currently occur.
  Worth revisiting if a future change ever makes ConnectionBanner's own
  mounting conditional.

## Verification

```
npx vitest run          → EXIT=0  (706 passed, 53 files; 2 full reruns after
                                    the final commit, identical both times;
                                    no console warnings/errors — grepped for
                                    "warning|not wrapped in act|error", none)
npx tsc --noEmit         → EXIT=0  (no output)
npx eslint src            → EXIT=0  (no output)
npm run build              → EXIT=0  (tsc --noEmit && vite build; same
                                    636.84 kB main-chunk warning Task 2's
                                    report already documented as pre-existing
                                    — unchanged by this task, not a
                                    regression)

Isolated re-run, this task's own riskiest edit:
npx vitest run src/stores/threads.test.ts src/protocol/client.test.ts \
  src/protocol/reducer.test.ts src/protocol/reconnect.test.ts \
  src/dev/DevHarness.test.tsx
  → EXIT=0  (80 passed, 5 files — 78 pre-existing + 2 new trap tests)
```

`git diff --stat 2b7adbf4e HEAD` — 11 files changed, all within owned or
sanctioned scope; `AppShell.tsx` and every other file untouched.

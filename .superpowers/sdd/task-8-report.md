# Task 8 report — Dev harness page: live end-to-end proof

**Status:** DONE
**Commit:** `webui: dev harness — live protocol-core proof` (this task's commit, branch `w1-stores`)

## What changed

### `cmd/serf-hub/frontend/src/dev/DevHarness.tsx` (new)
The Wave 1 dev harness: connection state line, `thread/list` results as clickable rows,
selected thread's `ThreadModel` rendered as live-updating `<pre>` JSON.

- `bootstrapClient()`: wires a real `AppwireClient` (`rpcURLFromLocation(window.location)`)
  into `connectionStore` and kicks off `client.connect()`. Guarded two ways: `import.meta.env.MODE
  === "test"` short-circuits unconditionally (see "Deviations/findings" below — this guard exists
  because of a real bug this task caught); a `connectionStore.getState().client` truthy check
  makes it idempotent across remounts (e.g. Fast Refresh) so a live connection is never dropped
  and re-dialed.
- Thread list: fetches `thread/list` on every transition into `"ready"` (including post-reconnect,
  so the list repopulates without a page reload), rendering each row as
  `{ref} — {preview}` inside a `<button>`.
- Selection: clicking a row calls `useThreadsStore().ensureThread(ref)` (refcounted per the
  store's contract) in a `useEffect`, releasing it via `releaseThread(ref)` on deselect/unmount.
  The selected model renders via `JSON.stringify(selectedModel, null, 2)` inside a `<pre>`, which
  re-renders live as `applyNotification` folds in wire notifications through `useThreadsStore`.

### `cmd/serf-hub/frontend/src/dev/DevHarness.module.css` (new)
One rule (`font-family: monospace`) on the harness's wrapper — satisfies the plan's global
CSS-Modules-only constraint with the smallest possible footprint; `<pre>` is monospace by user-agent
default already, so this is what makes the connection line and thread rows monospace too.

### `cmd/serf-hub/frontend/src/App.tsx` (one-line delta)
Mounts `<DevHarness />` alongside the existing wave-1 placeholder text (kept in place — a sibling
worktree, `webui-workspace-shell`, also edits this file, and `App.test.tsx`'s existing
`getByText(/workspace shell/i)` assertion needed to stay green without touching that test file,
which is outside this task's ownership):

```tsx
import { DevHarness } from "./dev/DevHarness";

export function App() {
  return (
    <main>
      serf workspace shell — wave 1
      <DevHarness />
    </main>
  );
}
```

## Tests (TDD)

### `cmd/serf-hub/frontend/src/dev/DevHarness.test.tsx` (new, 4 tests)
Written first against a not-yet-existing `DevHarness.tsx` (red: `Failed to resolve import
"./DevHarness"`), then green after implementing:

1. **"shows the connection state and lists threads from a scripted thread/list"** — wires a
   `FakeClient` via `connectionStore.getState().connect(fake)` before render (no sockets), scripts
   `thread/list`, asserts the connection line and both thread rows render.
2. **"clicking a thread row calls ensureThread and shows the model JSON"** — scripts `thread/read`,
   clicks a row via `@testing-library/user-event`, asserts the `<pre>` JSON contains the hydrated
   `threadId` and that `fake.calls` recorded the `thread/read` round trip.
3. **"an injected item/agentMessage/delta updates the live-updating JSON view"** — hydrates a
   thread with an in-progress `agentMessage` item, injects `fake.emitNotification({method:
   "item/agentMessage/delta", ...})` inside `act()`, asserts `"pendingText"` and the delta text
   appear in the re-rendered JSON.
4. **"renders an empty list without crashing when thread/list responds with data: null"** —
   regression test for the finding below; scripts `thread/list` returning `{data: null}` (the real
   wire shape when a live hub has zero matching threads), waits for the round trip via a
   `flushUntil` poll on `fake.calls`, asserts the connection line survives and the list renders
   empty rather than crashing.

All four pre-wire a `FakeClient` through `connectionStore.getState().connect(fake)` — the same
seam `threads.test.ts` uses — so `bootstrapClient()`'s no-op guard is exercised for real (not just
assumed): `connectionStore.getState().client` is already non-null by the time the effect runs.

### Gate results
- `npm run test` → 79 passed (75 pre-existing + 4 new).
- `npm run typecheck` → clean.
- `npm run lint` → clean.
- `make test-web` (from worktree root) → all three, clean.
- `make build-web && make build-hub` → both succeed; `dist/PLACEHOLDER` restored, working tree
  clean after build (Makefile's `git checkout -- dist/PLACEHOLDER` step, verified).

## Finding: `thread/list`'s `data` field can be wire-`null`, not just `[]`

Caught by the live-verification pass (see below), not by unit tests initially — the fakeClient's
scripted responses had never returned anything but `data: [...]`, so nothing exercised this path
before this task.

**Root cause** (verified against source, not guessed): `appwire.ThreadListResponse.Data` is
`[]Thread \`json:"data"\`` (`appwire/types.go`) — no `omitempty`. `cmd/serf-hub/app_threadlist.go`
declares `var threads []appwire.Thread` (nil) and only `append`s on a match; with zero matching
threads the response is genuinely `{"data":null}` on the wire. `types.gen.ts` faithfully emits
`data: Thread[]` (non-nullable) because Go's static type system gives the codegen nothing else to
say — this is a real, known Go/JSON/TS boundary gap, not a codegen bug (the mapping is exactly
what Task 3's brief specifies). The legacy client already guards this
(`cmd/serf-hub/assets/appwire.js:336`: `resp.data || []`), confirming it's a real, previously-hit
behavior of this exact endpoint, not a hypothetical.

**Where it bit:** `DevHarness.tsx`'s `thread/list` handler did `setThreadList(resp.data)` with no
guard. Against my live test hub (real `~/.serf` state, real provider), the very first render (in
a real browser, not jsdom) threw `TypeError: Cannot read properties of null (reading 'map')` at
the `threadList.map(...)` line, and — because React 18/19 unmounts the tree on an uncaught render
error with no error boundary present — the harness rendered nothing (`#root` stayed empty) with no
thrown exception surfacing to the page's own script tag (module-top-level `createRoot(...).render()`
does not itself throw; React swallows it via its own uncaught-error path, logging via
`console.error`/`reportError`, not via an exception the caller can catch).

**Fix (in my own owned file, no controller ruling needed):** `setThreadList(resp.data ?? [])`,
mirroring the legacy client's existing `resp.data || []` and the same defensive pattern
`reducer.ts` already uses everywhere else a Go slice field crosses the wire (`thread.turns ?? []`,
`turn.items ?? []`). Regression test 4 above pins it. This is squarely a DevHarness-owned
consumption-boundary bug, not a protocol-core defect — `types.gen.ts`/`client.ts`/`reducer.ts` all
behave exactly as designed; the gap was in my own component trusting a TS type that Go's JSON
marshaling doesn't actually guarantee.

## Live-run log (Part 2)

All commands from the worktree root unless noted. Hub binary path confirmed via
`make -n build-hub` → `scripts/build-runtime-pair.sh` → outputs `./serf` and `./serf-hub` at repo
root (not a `cmd/` subpath — verified before running, not guessed).

```
make build-web && make build-hub                          # both succeed
./serf-hub -h                                              # confirmed flags: -config, -addr, -serf
pgrep -fl serf-hub ; pgrep -fl "serf serve"                # confirmed clean before starting
SERF_HUB_WEB=new ./serf-hub -addr 127.0.0.1:9280 -serf "$PWD/serf" &
# [hub] auth URL: http://127.0.0.1:9280/auth?token=...
```

Navigated (chrome skill) to the auth URL directly against the built-in embedded SPA (not through
`npm run dev`/vite proxy — this exercises the real `go:embed frontend/dist` path Task 2 built,
end to end). `/auth?token=...` redirected to `/`; harness rendered `connection: ready` and an
empty thread list (screenshot: `t8-evidence/01-auth-then-app-ready.png`) — the first attempt hit
the `data: null` finding above (blank page); after the fix and rebuild, this came up clean.

Spawned a real session via `POST /api/spawn` (Bearer token), `cwd=/tmp/serf-w1t8-live/work`,
`launch_overrides.env.SERF_STATE_DIR=/tmp/serf-w1t8-live/state` (daemon-state isolation, matching
`docs/agentic-testing.md`'s established convention — the hub's own `~/.serf` state root is
intentionally NOT isolated, matching that doc, because `hostlock.AcquireLock` enforces at most one
`serf-hub` per host via a hardcoded `~/.serf/hub.lock` flock regardless of `-addr`/`-config`, so
there is only ever one hub to point at and no cross-run state collision is possible), model
`openai/gpt-5.5`, prompt "write a haiku about websockets, then stop". Re-navigated to see the new
session (thread-list refresh is only on a ready-transition, not push-driven for brand-new threads
— reload/re-navigate to see a newly spawned session is expected, matching the brief's scope);
clicked it (screenshot: `02-session-appears-in-list.png`); the model JSON showed the real haiku
("Sockets hum softly / Frames flicker through open pipes / Night replies in code") — full
hydrate→reducer→render round trip against a live daemon (screenshot: `03-thread-opened-model-json.png`).

**Live streaming** — fired follow-up prompts via `POST /s/<sid>/send` and polled the DOM in a
single `eval` (to avoid MCP round-trip latency racing the stream) capturing the selected model's
`pendingText` every ~500ms:

```
t=3045ms  item_assistant_37  chunks=12   len=55
t=3548ms  item_assistant_37  chunks=48   len=220
t=4049ms  item_assistant_37  chunks=77   len=386
t=4552ms  item_assistant_37  chunks=104  len=506
t=5054ms  item_assistant_37  chunks=132  len=644
t=5556ms  item_assistant_37  chunks=160  len=808
t=6058ms  item_assistant_37  chunks=188  len=956
```
(`t8-evidence/04-live-streaming-pendingText-growth.json`, plus several further confirmed runs
with different turn/item ids — turn_26/28/29 — all showing the same monotonic growth while
`lastTurnStatus: "inProgress"`.) Monotonic growth, `<pre>` re-rendering on every notification,
confirmed live against the real hub/daemon/provider.

**Reconnect** — `kill -9` on the hub PID; the SAME browser tab (no navigate/reload) showed
`connection: reconnecting` within 510ms; restarted the hub (`SERF_HUB_WEB=new ./serf-hub -addr
127.0.0.1:9280 ...`, same addr); the tab showed `connection: ready` with the thread list
repopulated (1 button) within 511ms of the next poll, and the previously-selected thread's model
re-hydrated correctly (`turnCount: 22`, correct `ref`/`threadId`) — all without any `navigate` call
in between (`t8-evidence/07-reconnect-sequence.json`).

**Cleanup** — `POST /s/<sid>/shutdown` → 204, daemon process gone (`pgrep` confirmed), hub killed,
`/tmp/serf-w1t8-live` removed, `pgrep serf-hub` clean, `pgrep "serf serve"` clean.

## Anomaly: shared-browser contention with concurrent Wave-1 agents (environmental, not a code bug)

During the live run, `list_tabs` repeatedly showed other tabs at `localhost:5173`/`5183`/`5184`
(`/dev/widgets`) — evidently other concurrently-running Wave-1/Wave-2 tasks' own dev harnesses,
also connected to **this same test hub on port 9280** (their own hub startup presumably lost the
`hostlock` race to mine and they proceeded to point their frontend's dev-proxy at the hub that
happened to already be up). Both `kill_chrome`+`set_profile` attempts to get an isolated browser
failed (another concurrent process kept auto-respawning Chrome on the shared profile before my
`set_profile` call could land). Net effect: `eval`/`click` reliably targeted my own tab throughout
(every capture explicitly verified `location.href` and the exact `turnId`/`itemId` I'd fired,
matching every time) — the JSON evidence above is trustworthy. The `screenshot` action specifically
was not reliably tab-scoped under this contention (it repeatedly captured whichever tab was
frontmost, not the CDP target `eval` was correctly pinned to) — a tooling limitation, not
something I could fix from here. I did not close or otherwise interfere with the other tabs (not
mine to disrupt). Three genuinely-verified screenshots exist (`01`–`03`, all pre-dating the worst
of the contention); I discarded several visually-contaminated capture attempts rather than keep
misleading evidence in the directory, and rely on the directly-verified `eval` JSON (`04`, `07`)
as the primary record for the streaming/reconnect claims specifically.

## Self-review
- `ensureThread`/`releaseThread` refcounting: exercised for real (selection → deselection via a
  second click isn't wired in this harness — there's no "close" affordance by design, matching the
  brief's "no styling/interaction beyond the minimum" — but unmount-driven release is covered by
  the effect's cleanup function; not separately unit-tested here since `threads.test.ts` already
  covers `releaseThread` exhaustively at the store layer).
- No `any` introduced; `MODE === "test"` guard uses `import.meta.env.MODE` (Vite/Vitest's
  documented, verified — not assumed — mechanism: confirmed via a throwaway diagnostic test before
  relying on it, since CLAUDE.md forbids inventing technical details).
- CSS Modules only (no inline `style=`, no CSS-in-JS), per the plan's global constraints.
- The `data: null` finding's fix is a one-line, minimal, correctly-scoped change in my own file —
  no protocol-core or Go changes made or needed.

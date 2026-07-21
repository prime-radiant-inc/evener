# Wave 5 Task 5 — Session chrome

Branch `w5-chrome`, off the wave chokepoint `e299f4803` (T1). Scope:
`cmd/serf-hub/frontend/src/panes/session/chrome/**` (new directory, 21
files) plus the one sanctioned cross-wave edit,
`transcript/messages/SteeringItem.tsx` (+ its test). 6 commits, each a
green, independently-buildable logical group: `npx tsc --noEmit` → full
`npx vitest run` (with a test-file-count cross-check against
`find src -name "*.test.ts*" | wc -l`) → `npm run lint` (`biome ci src`) →
`npm run build` → `git restore dist/PLACEHOLDER`, in that order, before
every commit.

Status: **complete**, with two explicit NEEDS_CONTEXT gaps (below) — both
are missing `stores/threads.ts` actions I could not add myself (that file
is T1's frozen chokepoint, outside this stream's manifest).

## Commit range

`e299f4803..2aa8e73cd` (6 commits on `w5-chrome`):

- `a24f0dd3f` T5(A): status row + `taskData.ts` parser
- `809e05c50` T5(B): session actions menu (fork/aside/compact/clear/shutdown/rename)
- `0533700ce` T5(C): goal display/set + remount-safe optimistic override
- `2f46ac18b` T5(D): tasks panel (aggregate-only)
- `4cbd1b780` T5(E): SessionChrome composition (fills the T1 placeholder)
- `2aa8e73cd` T5(F): steering classification suppression (cross-wave edit)

23 files changed, 2534 insertions(+), 23 deletions(-).

## Test summary

Final full-suite state: **114 test files, 1653 tests, all passing** (up
from the 107/? baseline at branch start — this stream added 7 new test
files under `chrome/` plus extended `SteeringItem.test.tsx` in place).
`npx tsc --noEmit` clean, `biome ci src` clean, `npm run build` clean, on
every one of the 6 commits (re-verified, not just claimed). One transient
`findByText` timeout was observed in `panes/session/index.test.tsx` during
a scoped partial run (`vitest run src/panes/session`) right after the
SessionChrome commit — reproduced 0/3 times on re-run, and the full-suite
gate (the actual required check) passed cleanly both before and after;
diagnosed as cold-transform-cache timing for the now-heavier Session lazy
chunk under a specific partial-run schedule, not a logic regression (that
test file is outside this stream's manifest regardless).

TDD was followed file-by-file: every new pure-logic module
(`statusFormat.ts`, `taskData.ts`, `sessionActions.ts`) and every component
had its test written first, confirmed red against the not-yet-created
file, then implemented to green. The `SteeringItem.tsx` edit extended the
existing test file in place (5 new tests) rather than rewriting any of the
12 pre-existing ones, per the task's explicit instruction.

## `serf/tasks/list`'s real response shape (investigated, cited, pinned)

The catalog types `TaskListResponse.Data` as `any` (`appwire/types.go:
896-898`), so I traced the actual daemon handler chain rather than
guessing:

- `cmd/serf-hub/app_rpc.go:678-684` — the hub's `serf/tasks/list` handler
  delegates to `source.ListTasks(ctx, params)`.
- `cmd/serf-hub/internal/appsource/local_daemon.go:330-342` — for a real
  local serf daemon, this calls `client.TasksList(ctx, params)`, which
  itself issues `serf/tasks/list` **again**, this time to the actual agent
  daemon process (not the hub).
- `server/appwire_runtime.go:713-721` (`handleAppTasksList`) — the daemon's
  own handler: `Data: fn()` where `fn` is `s.tasksFn`, or `nil` if no
  `tasksFn` is registered at all.
- `server/server.go:625-631` (`SetTasksFunc`) + `cmd/serf/serve.go:596` —
  `SetTasksFunc(func() any { return getSession().Tasks() })` is called
  **unconditionally** by every real serf daemon session.
- `agent/session_tools.go:957-959` (`Session.Tasks()`) →
  `agent/task/task_store.go:202-206` (`TaskStore.View()`) — returns
  `append([]Task{}, s.tasks...)`: always a non-nil, possibly-empty slice.
- `agent/task/task_store.go:54-79` — the real `Task` struct, JSON tags:
  `id, type, description, prompt, status, depends_on?, notes?,
  reasoning_effort?, insert?, created_at?, updated_at?, completed_at?`.
  `status` ∈ `open|in_progress|done|cancelled` (lines 28-36); `type` ∈
  `research|implement|verify|fix` (lines 42-50).
- `cmd/serf-hub/internal/appsource/codex_source.go:405-407` — the Codex
  source doesn't support tasks at all: it rejects the call with
  `appwire.Unavailable(...)`, it does not return null data.

**Conclusion**: `Data` is `null`/`undefined` only when no `tasksFn` is
registered (an old daemon); for every real serf-daemon session it's a JSON
array of the `Task` shape above (possibly `[]`, never `null`, once a task
store exists). `chrome/taskData.ts`'s `parseTaskListData` narrows exactly
this shape into a `TaskRow[]`, tested against a fixture built from these
real field names (not invented), including malformed-entry defense
(drops an entry missing `id`/`type`/`description`/`prompt`/`status` rather
than throwing or discarding the whole list).

## NEEDS_CONTEXT: two features are built as far as possible without a disallowed wire call

The wave's binding constraint ("wire truth ... all wire calls through
threads-store actions") and the task's own explicit instruction ("if the
store lacks a models action, a local wire call through the client is NOT
allowed ... report NEEDS_CONTEXT") both apply, and I verified by reading
the entire `stores/threads.ts` file and grepping the whole frontend tree:
**every single `client.request(...)` call site in this codebase today
lives inside `stores/threads.ts`** — there is no precedent anywhere for a
component or a different store issuing one directly. `stores/threads.ts`
is T1's frozen chokepoint and outside my manifest, so I did not add to it.

1. **Model-catalog switch is not built.** `model/list` exists on the wire
   (`appwire.MethodModelList`, params `{harness?, cwd?}`, result
   `{data: ModelDescriptor[]{provider, model}, diagnostics?, recent?}`),
   but `ThreadsStoreState` has no action wrapping it. I built the model
   **chip** (read-only, `StatusRow.tsx`, via `statusFormat.ts`'s
   `modelLabel`) and the reasoning-effort **switcher** (fully interactive —
   `reasoningEffortLevels`/`supportsReasoning`/`setReasoningEffort` are all
   already on `ThreadModel`/`ThreadsStoreState`, no gap there). What's
   missing is the interactive "pick a different model" Combobox itself.
   **Unblocks with**: a `listModels(harness?, cwd?): Promise<ModelListResponse>`
   action on `ThreadsStoreState` wrapping `model/list`.

2. **Tasks panel shows the aggregate only, not the per-task row list.**
   `model.tasks` (`{total, done}`) is real, live-pushed data (via
   `serf/task/updated`) already on `ThreadModel` — that part is fully
   wired and tested. But it's `null` until the first live push arrives
   (`hydrateThread` seeds it `null`, never from the snapshot) — a cold- or
   quiet-session pane may show "no task activity yet" indefinitely without
   a fetch-on-open. The parser for the fetched list
   (`chrome/taskData.ts::parseTaskListData`) is built and tested against
   the wire-true shape above, ready to consume a response, but nothing
   calls `serf/tasks/list` because no store action exists to.
   **Unblocks with**: a `listTasks(ref): Promise<TaskListResponse>` (or
   equivalent) action on `ThreadsStoreState` wrapping `serf/tasks/list`.

Sanction either (or both) and I'll wire the remaining UI in a follow-up —
both are small (the pattern is identical to every other action already in
that file).

## Design decisions made (beyond-parity license; documenting rather than silently picking one)

- **Dollar cost is not shown.** `appwire.EstimateCost` is a Go-side
  computation over a pricing table that never crosses the wire.
  `ThreadModel` carries raw `SerfUsage` token counts (real wire truth,
  shown) but no cumulative session cost field; `TurnModel.cost` is a
  per-turn formatted string covering only whatever turns happen to be
  loaded client-side (the last N via `turnLimit`, older pages fetched on
  demand), so summing those would silently under-count. Showing a dollar
  figure would mean inventing a client-side pricing table sourced from
  nowhere — status row shows token usage (↑/↓) instead.
- **Chrome-level "Fork" forks from the most recent turn with a real
  `userMessage` item** (`sessionActions.ts::lastUserMessageText`, scanning
  backward — the very last turn isn't always user-initiated, e.g. a
  goal-continuation turn), pre-filling an editable dialog with that turn's
  text and submitting via `editedInput` (not `deferInput`): the daemon
  requires one of the two when `sourceTurnId` is set
  (`cmd/serf-hub/app_threadlifecycle.go:361-363`), and `deferInput` would
  leave the recovered text with nowhere to land — there's no shared drafts
  store this stream can reach (`composer/drafts.ts` is T2's own manifest)
  to stage it in the new pane. Fork is disabled entirely when no turn
  anywhere has a real user message to recover.
- **Goal's optimistic update lives in a small ref-keyed module cache, not
  component state.** `goal/set` has no live push at all (confirmed: no
  `goal`-changed entry in `appwire/protocol.go`'s Notifications catalog),
  and `stores/threads.ts` has no action to patch `ThreadModel.goal`
  locally either. Component state would violate the wave's own
  remount-safety constraint (dockview unmounts an inactive pane's whole
  tree on a tab switch), so the override lives in a module-private
  `Map<ref, {baseline, override}>` (mirrors `stores/threads.ts`'s own
  `refCounts`/`inflightHydrates` pattern) — each entry remembers the
  `model.goal` it was computed against, so a later genuinely-fresh hydrate
  (e.g. after a reconnect) invalidates a stale optimistic value instead of
  masking every future update forever. Tested for both the remount-survives
  and the fresh-hydrate-supersedes cases.
- **Reasoning-effort display and switcher are merged into one interactive
  `Select`**, not a separate passive chip plus a separate switcher —
  showing the same value twice side-by-side would be redundant UI. Falls
  back to plain text when the model supports reasoning but offers no
  ladder (never a hardcoded fallback ladder — `ThreadModel.supportsReasoning`
  is always a concrete boolean here, never the wire's genuinely-unknown
  case the legacy picker's own `DEFAULT_EFFORT_LEVELS` fallback existed for).
- **Aside is gated on `capabilities.forkFromTurn`** (there's no separate
  `aside` capability field on the wire) — a documented, reasonable proxy,
  not independently verified against every possible source type.

## Concerns (as of the initial submission, superseded below)

- The two NEEDS_CONTEXT gaps above are the main open items — both are
  small, well-specified store additions.
- The transient `findByText` timeout in `panes/session/index.test.tsx`
  (see Test summary) is worth knowing about if it recurs, though it did
  not reproduce on repeated runs and the required full-suite gate was
  green throughout.
- I did not touch `Session.tsx` (frozen for the wave, per T1) — confirmed
  the header-vs-footer mount question never came up: the task only ever
  needed the footer slot T1 already carved.

---

## Addendum: both NEEDS_CONTEXT gaps unblocked and closed

The coordinator landed the sanctioned T1 addendum on `w5-interaction`
(commit `da1b43f85`): `listModels(refresh?: boolean): Promise<ModelListResponse>`
(session-lifetime cache + concurrent-call dedup + refresh bypass, never
Conflict-mapped) and `listTasks(ref: string): Promise<unknown>` (returns
`TaskListResponse.Data` verbatim, this stream's own `parseTaskListData`
owns interpretation; a Codex-source thread rejects with
`appwire.Unavailable`, code `-32014`, `serfErrorInfo: "actionUnavailable"`).

**Step 1 — distribution.** `git cherry-pick da1b43f85` onto `w5-chrome`
applied clean (no conflicts: this stream never touched `stores/threads.ts`
or its test file). Landed as commit `1ee3ab60f`, content-identical to the
`w5-interaction` original; the wave merge will reconcile the duplicate
patch across the two branches, as the coordinator noted.

**Step 2 — the two blocked surfaces, now built:**

- **Model-switch Combobox** (`chrome/ModelSwitch.tsx`, new file): shows
  the current model as a passive chip with a "Change model" trigger
  (gated on `capabilities.changeModel`) that reveals a `Combobox` loaded
  from `listModels()`. Re-fetches on every open rather than caching in
  component state — `listModels()` is already session-lifetime cached in
  the store, so a repeat call is effectively free, and a second local
  cache would only risk a stale "Loading models…" flash on a dockview
  remount that the store already has the answer for. Picking an option
  closes the picker optimistically and calls `setModel`, no rollback on
  failure (matches the legacy picker's own documented convention,
  `parity-m5-composer.md` §H) — toast either way. Wired into `StatusRow`
  in place of the old passive-only Chip; `reasoningEffortLevels`/
  `supportsReasoning` needed zero extra wiring, since `StatusRow` already
  re-renders from the live `ThreadModel` and `thread/model/changed`'s own
  reasoning-ladder update flows through to the existing
  `ReasoningEffortControl` for free.

- **TasksPanel per-task rows**: fetches via `listTasks(ref)` on every
  open, and again automatically whenever the live `model.tasks` aggregate
  changes WHILE the panel stays open (the plan's push-driven-plus-fetch-
  on-open model) — the aggregate's object reference only changes when the
  reducer's own `serf/task/updated` case re-assigns it, verified this
  never refires on an unrelated model update (a dedicated test covers
  "does not fetch while closed, even if the aggregate changes"). Rows
  render in the exact order the wire returns them — no client-side
  re-sort, matching the legacy panel's own behavior (verified: `renderTasksInto`,
  `cmd/serf-hub/assets/renderer-panels.js:341-358`, has no `.sort()` call).
  Status glyph/tone per row is the legacy grammar
  (`planGlyphForStatus`/`planStateClass`, `renderer-format.js:496-521`)
  translated onto this app's own `Chip`-tone vocabulary rather than the
  legacy's `window.SerfIcons` SVG fragments (no equivalent exists in this
  client): done/open stay neutral, in\_progress reads as the "agent
  working" hue (alive), cancelled reads the same as a failure (danger) —
  matching that file's own stated reasoning verbatim ("a plan item that
  will not happen reads the same as a failure").

  Failure handling, exactly as instructed: a Codex-source
  `actionUnavailable` rejection shows an honest "Task list isn't
  available" inline state with **no toast** (it's a capability gap, not a
  bug). I additionally folded a resolved-but-uninterpretable response
  (`parseTaskListData` returning `null` — e.g. an old daemon with no
  `tasksFn` registered, which resolves with `null` data rather than
  rejecting, per `server/appwire_runtime.go:713-721`) into that SAME
  unsupported state, for the identical reason: nothing actually failed at
  the transport level in either case. Any other rejection toasts (the
  wave's convention) AND shows an inline error state, since there's
  nothing else left to show. A confirmed-empty fetch (genuinely zero
  tasks) now shows the legacy panel's own definitive copy ("No tasks yet"
  / "The agent's task list is empty for this session") in place of the
  old ambiguous aggregate-only "no activity yet" text, since a real fetch
  now backs that claim.

**New commit range**: `e299f4803..ed00fce64` (10 commits total on
`w5-chrome`, 4 new since the initial submission):

- `1ee3ab60f` — cherry-picked T1 addendum (`listModels`/`listTasks`)
- `2b5eae134` — T5(G): model-switch Combobox
- `ed00fce64` — T5(H): tasks panel per-task rows

**Test summary**: 115 test files, 1681 tests, all passing (up from
114/1653 — `+11` from the cherry-picked `threads.test.ts` extension,
`+8` from a new `ModelSwitch.test.tsx`, and `TasksPanel.test.tsx` grew
from 5 to 10 tests reflecting the redesigned fetch-on-open behavior).
`npx tsc --noEmit` clean, `biome ci src` clean, `npm run build` clean, on
both new commits, re-verified fresh at HEAD after the last commit.
TDD followed throughout: `ModelSwitch.test.tsx` written and confirmed red
before `ModelSwitch.tsx` existed; `TasksPanel.test.tsx` rewritten to
cover the new fetch lifecycle and confirmed red (7 of 10 failing) against
the prior aggregate-only implementation before the rewrite.

**Concerns (current)**:

- None blocking. Both previously-open NEEDS_CONTEXT gaps are closed.
- The status-glyph-to-Chip-tone translation (see above) is a documented,
  reasonable but independent re-derivation of the legacy's icon grammar,
  not a byte-for-byte visual port (no visual-pixel parity is a stated
  non-goal of the rewrite) — worth a design-review glance, not a
  correctness concern.
- The `panes/session/index.test.tsx` transient timeout noted in the
  original submission did not recur during any of this addendum's gate
  runs.

This stream is now a finished unit, ready for review.

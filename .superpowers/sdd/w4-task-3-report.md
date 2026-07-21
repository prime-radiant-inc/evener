# Wave 4 Task 3 — Tool renderers + agent-work surfaces

Branch `w4-tools`, off `afb420c01` (T1's transcript core). Scope:
`cmd/serf-hub/frontend/src/panes/session/transcript/tools/**` (30 files, 3637
lines) plus the one sanctioned extension to `src/stores/threads.ts`
(`watchThread`/`releaseWatchedThread`). 18 commits, each a green, independently
buildable logical group (2x-verified full suite + tsc + eslint + `npm run
build` after every one; final state re-verified once more at the end).

## TDD evidence

Every file was test-first: a failing test observed red, then the minimum
implementation to go green, per file. Two cases are worth calling out because
TDD caught real bugs before any consumer ever touched the code:

- **`subagentModuleStore.ts`**: the first version of `useSubagentRows`
  freshly `Array.from()`+`sort()`'d the row map on every selector call.
  `renderHook` (real `useSyncExternalStore` underneath zustand's `useStore`,
  not a mock) threw "Maximum update depth exceeded" — a new array reference
  every render reads as "changed every time" and infinite-loops. Fixed by
  precomputing and storing the sorted array itself, only on actual mutation
  (same tactic `stores/threads.ts`'s own `frameTimes` already uses). This
  would have shipped a crash on first render had it only been checked by eye.
- **`rememberedArgs`** (see "Critical wire-truth finding" below): found by
  writing a throwaway diagnostic test against the *real* reducer
  (`hydrateThread` → `applyNotification` with a wire-accurate `item/completed`
  payload), not by inspecting Go source alone. Deleted immediately after
  confirming; never committed.

Final tallies: 1180 tests across 82 files project-wide (baseline was 985; this
stream added ~195), 0 tsc errors, 0 eslint findings, `npm run build` clean,
full suite run twice with identical results both times, `threads.test.ts`
isolate-run separately (62/62, including all 50 pre-existing wave-1 tests
untouched).

## Critical wire-truth finding: tool-call args do not survive settlement

While building the first descriptor (`read_file`) I found — and then verified
live, not just by reading Go source — that **`ItemModel.argumentsJSON` is
`undefined` for any *settled* tool call**, which is the overwhelming majority
of what a transcript actually shows.

Root cause, traced to two independent layers:

1. `internal/appprojector/appwire_projection.go`'s `EventToolCallEnd` case
   builds the `item/completed` notification's `ThreadItem` struct literal
   without ever setting `ArgumentsJSON` — only `item/started` carries it. (A
   local `argsJSON` variable IS computed there, but only to feed
   `Description` — itself *also* dropped by the next layer.)
2. `protocol/reducer.ts`'s `item/completed` handler replaces the item
   wholesale via `wireItemToModel(item)`; `mergeReasoning` only carries
   `reasoningSummaries` forward from the prior state, nothing else.

Verified with a throwaway test driving the real `reducer.ts` through
`hydrateThread` → `item/started` (args present) → `item/completed` (wire-
accurate payload, args absent) and logging the result — `argumentsJSON` was
confirmed `undefined` on the settled item. (A pre-existing frontend fixture,
`tool-and-jobs.jsonl`, incorrectly *includes* `argumentsJson` on its own
`item/completed` line — it doesn't match what the Go projector actually
sends; flagging this as a likely latent test-fixture bug in wave 1-3
infrastructure, not something I touched.)

**Fix, entirely within my file ownership**: `helpers.ts`'s `rememberedArgs`
caches a call's parsed args the first time they're seen non-empty (i.e.
while `item/started`'s payload is intact), keyed by `callId` (falling back to
`id`), and serves that cache once the settled item's own args go missing.
Every descriptor uses this instead of a bare `parseArgs(item.argumentsJSON)`.
**Residual gap** (documented in code, not silently papered over): a session
opened cold on an already-completed historical turn never rendered that
call's `item/started`, so there's no cache entry to fall back to — target
info degrades to blank for that one case. The durable fix (preserve
`argumentsJSON` from the prior item, the way `mergeReasoning` already does
for `reasoningSummaries`) belongs in `protocol/reducer.ts`, outside
`transcript/tools/**`'s ownership; recommend a fast-follow there.

## Other verified (not assumed) wire-truth corrections vs. the parity checklist

The parity checklist was written against the legacy `renderer-tools.js`;
several of its assumptions no longer match the current Go implementation
(verified by reading the actual `agent/*.go` source directly, not inferred):

- **Tool call failure has no reliable ItemModel signal at all.**
  `appwire_projection.go` hard-codes `Status: "completed"` on every
  `EventToolCallEnd` regardless of exit code — status can never distinguish
  success/failure. `ThreadItem.Error` exists on the wire but `wireItemToModel`
  never copies it. Shell's `autoExpand`/result-suffix therefore parses a text
  heuristic instead: `agent/session_tools_shell.go`'s `formatShellResult`
  appends a trailing `"[exit <N> ...]"` bracketed footer to the tool's own
  output text (plus a second, differently-shaped fallback trailer,
  `"exit_code=N duration_ms=N timed_out=bool"`, for the non-streaming
  execution-environment path) — both parsed, documented in code as a
  heuristic coupled to that Go formatter's current wording, not a contract.
- **`write_file`/`edit_file`'s output is not a diff.** Legacy's
  `diffRenderer`/parity checklist assumed raw output was itself diff text;
  `agent/execenv/local.go`'s `WriteFile`/`EditFile` now return plain
  confirmation strings (`"wrote N bytes to X"` / `"edited X: N
  replacement(s)"`). `edit_file` still synthesizes its own diff from
  `old_string`/`new_string` input args (legacy did this too); `write_file`
  has no prior-content signal anywhere on the wire, so no diff is shown for
  it — a deliberate parity deviation, not an oversight.
- **`web_fetch`'s output actually is JSON** (`agent/tool_web_fetch.go`
  returns a plain map, which — having no `StateResult` wrapper — falls
  through the registry's default `json.MarshalIndent` path). Parses
  `size_bytes`/`answer` directly instead of a byte-count-of-raw-text/line-
  preview approximation.
- **Only `delegate`'s output is JSON among the job/delegate family.**
  `delegateToolResult` (job_id/delegate_id/transcript_ref/status, always
  `JSON.parse`-able) vs. `job_list`/`job_stop`/`delegate_send`, whose
  human-formatted text (`formatJobList`/`formatJobStop`/`formatDelegateSend`)
  carries the real structured data only in `tool_state`/`Raw` — dropped by
  the reducer before `ItemModel`, same gap as `argumentsJSON`. These three
  read their target from input args and their outcome from the tool's own
  trailing bracket footer (`trailingBracketFooter`/`statusWordFromText`,
  reading by word-search rather than field position since each footer field
  is independently conditional — verified against the Go formatters).
- **The registered "read one job" tool is `job_status`, not
  `job_read_output`.** `DefJobReadOutput` exists in
  `agent/internal/tool/definitions.go` but is never wired into
  `registerJobToolsWithRegistrar`. Registered both names defensively
  (`job_status` live, `job_read_output` as a no-cost alias per the parity
  doc's own naming) plus a generic `job_`-prefix predicate for the rest of
  the family (`job_watch`).
- **`job_send_message` is a retired/banned tool name** (confirmed via
  `agent/coordinator_workflow_plugin_test.go`'s own "banned names"
  assertion) — kept as a defensive `delegate_send` alias reading its
  legacy `target` arg, per the parity doc.

## Per-tool parity coverage map

| Tool(s) | File | Target/summary source | Body | Notes |
|---|---|---|---|---|
| read_file | fsTools.tsx | args (path) + output (readLineRange) | tail-sliced/-folded | |
| grep, grep_files, grep_search | fsTools.tsx | args (pattern/path/glob_filter) | head-clipped | one descriptor, 3 names |
| list_dir, list_directory | fsTools.tsx | args (path/pattern) | head-clipped | |
| glob | fsTools.tsx | args (pattern/glob) | head-clipped | |
| shell, exec_command, run_shell_command | shellTool.tsx | args (command) + output footer | tail-sliced/-folded + `$ command` header | autoExpand on nonzero exit (heuristic, documented) |
| edit_file | editTools.tsx | args (old/new_string → synthesized diff) | DiffBlock | never autoExpands |
| write_file | editTools.tsx | args (path) | plain confirmation text | no diff — verified no data exists for one |
| apply_patch | editTools.tsx | args (patch → patchTargets) | DiffBlock (v4a text) | |
| web_fetch | webTools.tsx | args (url) + parsed JSON size_bytes | parsed `answer` field | |
| web_search | webTools.tsx | args (query/q) + output line count | up-to-5-line list | only a live commandExecution item on Gemini; historical-replay-only on OpenAI/Anthropic |
| use_skill | useSkillTool.tsx | args (skill_name) | raw output text | |
| job_status, job_read_output | jobTools.tsx | args (job_id) + parsed JSON status | head-clipped (already JSON) | only job_status is live |
| job_list | jobTools.tsx | args (status filter) | head-clipped | |
| job_stop | jobTools.tsx | args (job_id) + footer | head-clipped | also updates an existing subagent row |
| delegate_send, job_send_message | jobTools.tsx | args (to/target) + footer | head-clipped | also updates an existing subagent row; job_send_message is dead-but-aliased |
| job_* (fallback) | jobTools.tsx | args (operation) | head-clipped | job_watch and anything else unmatched |
| delegate | subagentModule.tsx | args (task) | aggregated module (see below) | |
| ask_user | askUser.tsx | args (questions[].header) | per-question cards | read-only this wave, defensive parsing |
| (thread-level) sandbox escalation | sandboxEscalation.tsx | n/a | approve/deny card | standalone — no mount point in this stream, see below |

Dead-legacy items from the parity checklist (`SUBAGENT_SEVERITY`, the 6-row
count-cap constant, `renderLivePlan`/`taskFoldGroup`) were **not** ported, per
the checklist's own Highlights section.

## Subagent module design (the interface-boundary problem and its resolution)

`ToolRenderProps` is locked as `{item, live}` — no `turn`, no sibling items.
Confirmed by reading `ToolCallItem.tsx`: it receives the full
`ItemRenderProps` (turn included) but its own destructuring drops `turn`
before ever calling a descriptor's `body`. A single tool descriptor
therefore cannot see its siblings to build "one aggregated block per turn"
through props alone.

**Resolution**: `subagentModuleStore.ts`, a same-directory Zustand store
(mirrors `stores/threads.ts`'s own `createStore`/`useStore` idiom) keyed by
`(turnId, rowKey)`. Every `delegate` item computes its own row (spawned/
running/done/failed + duration + result preview, derived entirely from that
one item's parsed JSON output) and upserts it via `useLayoutEffect`. The
*first* `delegate` item to mount in a turn claims "leadership" via a lazy
`useState` initializer and is the only one that renders the module chrome
(tally + rows + fold); this is deterministic, not a race, because
`VirtualList` windows a whole `TurnBlock` as one row (confirmed in
`TurnBlock.tsx`) — a turn's items always mount/unmount together. Non-leader
`delegate` items in the same turn render nothing further (their own one-line
summary still shows via `ToolCallItem`'s mandatory summary span — that's T1's
file, untouched).

**Scope decision**: `job_status`/`job_stop`/`delegate_send` also *update* an
existing row (never spawn one — mirrors the legacy `reconcileSubagent`'s
identical "update only" rule) via `updateSubagentRowIfExists`, correlating by
`delegate_id` > `job_id` > callId. This was necessary, not optional: `delegate`
defaults to `background=true`, so its own tool call typically settles almost
immediately with the child still `"running"` and never gets a second
completion event — without correlating the follow-up calls that actually
check on/message the child, every row would show "running" forever and the
feature could never demonstrate "spawned → running → done/failed" at all.
`job_list`/`job_watch` are *not* correlated — they're orientation calls over
many jobs at once, and reliably mapping an arbitrary listing back to
individual rows would need more inference than the wire supports.

Folding is done-kind-only (running/failed/unknown always visible — "a live or
broken child is never hidden by count," parity §12), with an expand/collapse
toggle beyond 6 done rows. A failed row flags the module via
`data-has-failure`, never averaged into one number. Open-transcript calls
`workspaceStore.getState().openPane("session", {ref: transcriptRef})` against
the *child's* own ref (from `delegate`'s parsed output) — `"session"` stands
in for a dedicated transcript pane type until Wave 8 registers one, per this
stream's own instructions.

## Watched children / `watchThread` design

`watchThread(ref)`/`releaseWatchedThread(ref)` — the one sanctioned extension
to `stores/threads.ts` — is a structural mirror of `ensureThread`/
`releaseThread`, differing in three ways: (1) `includeTurns:false` (a watched
row only needs live status/liveness, never the full turn history a real pane
needs — matches "readThread ref,false,true,false" from the stream brief
exactly); (2) an independent refcount (`watchRefCounts`, not `refCounts`), so
a real pane and a watching subagent row on the same ref never fight over one
counter; (3) storage in new `watchedThreads`/`watchedFrameTimes` fields,
deliberately **separate** from `threads`/`frameTimes` — a ref can legitimately
be both `ensureThread`'d (a real pane open) and `watchThread`'d (a parent
session's subagent row watching it) simultaneously, and releasing one must
never disturb the other's data or lifecycle. Tested explicitly (both-tracked-
at-once, independent release order, frameTimes never doubled into either map
from one shared notification).

`handleNotification`/`handleReady` are *extended*, not rewritten, via a
factored-out `applyToMap` helper shared by both the real and watched passes;
both fall through as pure no-ops when `watchedThreads` is empty, which is
true for every pre-existing test and every session that never calls
`watchThread` — confirmed by all 50 pre-existing `threads.test.ts` tests
passing unchanged, plus the full 1180-test project suite. 12 new tests mirror
`ensureThread`/`releaseThread`'s own test shapes closely (hydration params,
independent refcounting, reconnect resubscribe, release-during-inflight).

`WatchedChildIndicator` (`watchedChild.tsx`) wires a "running" subagent row
with a `transcriptRef` into this: watch on mount, release on unmount, Cadence
driven by `watchedFrameTimes`. A watch failure (or no client connected)
degrades to rendering nothing — never crashes the module over a live-status
nicety.

## Sandbox escalation ground truth (investigated per the task's own request)

Escalations are **thread-level, not item-level** — confirmed, not assumed:

- `serf/sandbox/escalation/requested` (a notification) and
  `SerfThread.pendingEscalations` (a snapshot list on the hydrate response)
  are the only two wire paths. `protocol/reducer.ts`'s `applyNotification`
  has **no case** for the notification method at all (falls to `default`, a
  silent no-op), and `hydrateThread` never reads
  `thread.serf.pendingEscalations`. Both read directly, not inferred.
- Neither `ItemRenderProps` nor `ToolRenderProps` carries a `ref` or the
  owning `ThreadModel` — so unlike every other T3 surface, there is
  **no `registerToolRenderer`/`registerItemRenderer` integration point for
  this at all**. It isn't a `ThreadItem`; there's nothing to dispatch on.
- The legacy renderer's own `appendSandboxEscalation` mounts the card as a
  direct sibling of turns in the conversation container, not nested in any
  tool row — confirming this was never item-scoped even in the original
  design, and the parity checklist itself explicitly excludes escalation
  *rendering* detail from its scope (its own header: "diagnostics/escalation
  recovery actions are out of scope for this file").

Given that, I shipped a fully working, fully tested, standalone unit rather
than forcing it into a slot that doesn't structurally exist:
`useSandboxEscalations(ref)` (live subscribe via `useClient()`, `resolve()`
calling `serf/sandbox/escalation/resolve`), `SandboxEscalationCard`
(approve/deny, harness-prompt-styled so it can never be mistaken for
model-authored content), and `SandboxEscalationRail` (wires them together,
disabling a card the instant it's clicked — before the response arrives — to
prevent a double-submit race). TDD'd against `FakeClient`
(`sandboxEscalation.test.tsx`, 12 tests): live notification → card appears,
approve/deny → correct wire params, resolution → card removed, duplicate
notifications de-duplicated, wrong-ref notifications ignored.

**Two gaps remain, both requiring changes outside `transcript/tools/**`'s
ownership**, documented in the file header rather than silently skipped:
(1) no snapshot-seeding — an escalation already pending before this hook
mounts (reconnect/cold-open) needs `pendingEscalations` projected into
`ThreadModel` first, which only `protocol/reducer.ts` can do; (2) no mount
point in the live tree — needs a `Session.tsx`-level slot (T4 has SessionPane
edit rights per the wave-4 plan) or wave-close integration.

## ask_user cards

Read-only this wave, per the task. Ground truth:
`agent/internal/tool/definitions.go`'s `DefAskUser` gives the exact
`argumentsJson` shape (`questions[].{header,question,options[].{label,
detail,recommended?},multi_select?,why?,if_unanswered?}`, 1-4 questions).
`ask_user`'s own `Output` is always the *same fixed string* on success
(`agent/session_tools_ask.go`'s `askUserAckText`) — carries no per-call
information, so this reads entirely from input args via `rememberedArgs`.
Parsing is defensive at both the whole-payload level (malformed JSON/missing
`questions` → a fallback card, never a crash) and the per-question level (one
malformed question inside an otherwise-valid array is skipped, not fatal to
the rest).

## The registration import line

Nothing in this stream's ownership imports `transcript/tools/**` yet (by
design — integration is controller-owned, per the wave-4 plan's own
"controller-owned integration" convention). Add, in a T1-or-later-owned file
(e.g. `ToolCallItem.tsx`, mirroring its own existing `import
"./ToolCallItem"`-style side-effect pattern, or `Session.tsx`):

```ts
import "./transcript/tools"; // or "./tools" / "./ToolCallItem"-relative, depending on where it lands
```

`sandboxEscalation.tsx`'s `SandboxEscalationRail` needs a **separate**,
explicit mount (it has no `registerToolRenderer` call to piggyback on):

```tsx
<SandboxEscalationRail sessionRef={ref} />
```

somewhere in `Session.tsx`'s own render tree (T4/controller territory).

## Self-review / concerns

- **`rememberedArgs`/`subagentModuleStore`/watch-leader-election are all
  page-lifetime, unbounded-growth caches** (never evicted). Same trade-off
  `stores/threads.ts`'s own `threads`/`frameTimes` maps already accept for a
  session's lifetime; not a new risk class, but worth naming if a very
  long-lived session with thousands of tool calls ever becomes a concern.
- **Test-isolation footgun, found and fixed twice** (`jobTools.test.tsx`'s
  `job_watch` tests, `askUser.test.tsx` broadly): `rememberedArgs`' cache is
  keyed by `callId ?? id`, module-scoped per test *file* (vitest isolates
  file-level, not test-level) — two tests sharing a fixed default id can
  silently read each other's cached args when one test's own args parse to
  `{}`. Fixed locally in both files (unique ids); did **not** retroactively
  touch the other five already-green files that never hit this in practice,
  to avoid unnecessary churn on working, already-reviewed code. Flagging
  clearly here since it's a sharp edge for whoever adds tests to this
  directory next.
- **Named prop `ref` on `WatchedChildIndicatorProps`** works correctly in
  this React 19 setup (confirmed by passing tests) since React 19 forwards
  `ref` to function components as a plain prop rather than reserving it —
  but it's easy to misread as a mistake. Aliased everywhere it's destructured
  (`{ref: childRef}`); `SandboxEscalationRail` uses `sessionRef` instead of
  `ref` outright to avoid the ambiguity a second time.
- **`web_search`'s live-vs-historical output-shape split** (bare prose on
  Gemini live; `"Title — URL"` lines only on OpenAI/Anthropic historical
  replay) is real and verified, but the body's "up to 5 trimmed lines" design
  is a reasonable compromise for both shapes rather than a shape-specific
  render — flagging as a place a future pass could special-case if it turns
  out to read poorly for the prose case in practice.
- **Shell's `autoExpand`/exit-code detection is a text heuristic**, not a
  wire contract (documented extensively in `shellTool.tsx`'s own header) —
  it will silently stop working if `agent/session_tools_shell.go`'s footer
  wording ever changes. The correct long-term fix is projecting
  `ThreadItem.error`/exit-code data into `ItemModel`, outside this stream's
  ownership.
- No custom keyboard handling was needed anywhere in this stream's scope —
  every interaction is a native `<button onClick>` (Button/Chip widgets), so
  the "consume-then-stop keys" binding constraint has no surface here.

## Verification (all green, 2x)

```
npx tsc --noEmit                          # 0 errors
npx eslint src                            # 0 findings
npx vitest run                            # 82 files, 1180 tests, run twice, identical
npx vitest run src/stores/threads.test.ts # isolate-run: 62/62 (50 pre-existing + 12 new)
npm run build                             # tsc --noEmit && vite build — clean
```

No file outside `cmd/serf-hub/frontend/src/panes/session/transcript/tools/**`
and the two sanctioned `stores/threads.ts`/`stores/threads.test.ts` files was
touched (`git diff --stat` against every other path in the tree is empty).

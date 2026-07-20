# Task 6 report — ThreadModel + reducer + golden fixtures

**Status:** DONE
**Worktree:** `webui-stream-reducer` (branch `stream-webui-reducer`)
**Commits:**
- `3ef1c416c` — `webui: ThreadModel reducer with golden notification fixtures`
- `3dabccf81` — `webui: reducer — turn/completed gated to the active turn (cross-thread safety)` (fix wave 1, see below)

Note: `task-6-report.md` / `task-6-fix-report.md` already present in this
directory were stale content from an unrelated prior initiative (a "Home
launchpad" feature and a "canonical project identity" fix) — nothing to do
with this stream. This report overwrites `task-6-report.md`;
`task-6-fix-report.md` was left untouched (not mine to overwrite, and not
referenced by my brief).

## Files (all newly created, my exclusive ownership list)

- `cmd/serf-hub/frontend/src/protocol/model.ts` — `ItemModel`/`TurnModel`/`ThreadModel`, verbatim from the brief's locked "Produces" block.
- `cmd/serf-hub/frontend/src/protocol/reducer.ts` — `hydrateThread`, `applyNotification`, `prependOlderTurns`, `notificationTargetsThread`.
- `cmd/serf-hub/frontend/src/protocol/reducer.test.ts` — 15 tests (4 golden-fixture snapshots + 11 unit tests).
- `cmd/serf-hub/frontend/src/protocol/fixtures/{basic-turn,streaming-with-reset,tool-and-jobs,queue-and-status}.jsonl`
- `cmd/serf-hub/frontend/src/protocol/__snapshots__/reducer.test.ts.snap` (generated)

## TDD evidence

1. **RED (missing module):** wrote all 4 fixtures + `reducer.test.ts` first, importing from a nonexistent `./reducer`. `npm run test -- protocol/reducer` failed with `Failed to resolve import "./reducer"` — correct failure (feature missing, not a typo).
2. **GREEN:** implemented `reducer.ts`; all 13 initial tests passed, 4 snapshots written.
3. **Second RED/GREEN cycle (mid-review, before commit):** while self-reviewing against the Go source I'd read for wire semantics, I found that `userMessage`/`systemMessage` items are projected straight to `item/completed` with **no preceding `item/started`** (`internal/appprojector/appwire_projection.go` lines 152-176: a new turn emits `turn/started` with an empty turn, then `item/completed` alone carries the userMessage item). My first-pass `item/completed` handler only *replaced* an existing in-flight item and silently no-op'd otherwise — it would have dropped every user message from the live view until reload. Wrote a new test (`item/completed inserts an item that had no preceding item/started`), confirmed it failed (`expected an item at index 0`), then extended `item/completed` to upsert (replace if found, else insert via the same turn-resolution logic as `item/started`, factored into a shared `resolveInsertTurnId` helper). Confirmed green afterward.
4. Final (at commit `3ef1c416c`): `npm run test -- protocol/reducer` → 14/14 pass; full suite → 15/15 pass (incl. App.test.tsx), pristine output. `npm run typecheck` clean. `npm run lint` clean. (Corrected: an earlier draft of this report and my reply to the coordinator mis-stated this as 15/16 — verified against `git show 3ef1c416c:.../reducer.test.ts | grep -c '^test('` = 10 standalone + 4 golden-fixture = 14, not 11+4=15.)

## Fixture-shape verification against the protocol

`docs/appwire-protocol.md`'s type-reference section only documents top-level params/response/payload types, not nested domain types (`Thread`, `SerfThread`, `Turn`, `ThreadItem`, `QueueState`, etc.) or the several notifications marked `(inline)` (their shape exists only in prose, e.g. "inline {threadId, ref, turn}"). For those I cross-checked `types.gen.ts` (field names/casing/optionality) directly against the Go source that actually constructs each notification (`internal/appprojector/appwire_projection.go`), not just the prose summary, specifically:

- `turn/started`, `item/started`, `item/completed` → confirmed `{threadId, ref, turn}` / `{threadId, ref, turnId, item}` exactly (lines 164-173, 235-245, 262-272).
- `serf/steering/injected` → confirmed `{threadId, ref, text, images, source?}` (line 581-593).
- `serf/job/started`/`serf/job/finished` → confirmed **only** `{threadId, ref, job: SerfJobInfo}` — the doc's prose "with status/reason/exitCode/output" describes fields *inside* `job` (`SerfJobInfo` already has status/reason/exitCode), not extra top-level fields. I did not invent a top-level `output` field.
- `turn/completed` (`TurnCompletedParams`) → confirmed it is **only** `{turnId, turn}` — no `ref`/`threadId` at all, unlike every other lifecycle notification. This is a real wire asymmetry, not an oversight on my part — see Concerns below.
- Wire item type for the `ask_user` tool → confirmed `type: "commandExecution"`, `toolName: "ask_user"` (no special-cased wire type exists for it) via `internal/appprojector/appwire_projection.go` and `internal/apptranscript`.
- `Turn.itemsView` on a freshly-started turn is the Go zero value `""` (the `startedTurn` helper never sets it), vs `"full"` on reload-path turns (`apptranscript.go`) — fixtures reflect this distinction (not that it affects `TurnModel`, which doesn't carry `itemsView` at all).
- Canonical status strings (`ThreadStatus.type`: idle/active/awaiting/warning/closed/notLoaded/systemError; `Turn.status`: inProgress/completed/failed/interrupted) pulled from `appwire/types.go` constants, not guessed.

Every fixture line uses exactly these confirmed field names/casings.

## Snapshot review (by eye, before accepting)

Read the full generated `.snap` file and checked all four fixtures for turn counts, joined text, statuses, and askPending:

- **basic-turn**: 2 turns (1 prior settled + 1 new). New turn's item text is `"Hello, world!"` — the `item/completed` payload's text, *not* `"Hello, world"` (what the three deltas alone join to) — confirms payload-wins. `activeTurnId` cleared to `undefined` after `turn/completed`. `lastFrameAt` = 1006, matching exactly 7 applied notifications (1000..1006) — confirms nothing was silently skipped.
- **streaming-with-reset**: 1 turn, 2 final items (the reset agentMessage, `item_2001a`, is entirely absent — reset removed it, not just cleared it). The reasoning item shows `reasoningSummaries: [["Weighing approach A"," vs approach B."],["Double-checking the math."]]` — two summaryIndexes, correctly separated, and preserved through both `item/completed` and `turn/completed` (mergeReasoning). `lastFrameAt` = 1014, matching 15 applied notifications.
- **tool-and-jobs**: 1 turn, 1 commandExecution item; `output` is the full 3-line completed-payload string (with tab characters and a trailing "PASS" line), not the 2-line concatenation of the two streamed deltas — confirms output-wins mirrors the text-wins behavior. `askPending` stayed `false` throughout (toolName is `run_tests`, not `ask_user` — correct negative case). `lastFrameAt` = 1008, matching 9 applied notifications, which also confirms `serf/job/started`/`serf/job/finished`/`serf/steering/injected` were genuinely applied (each consumes a `now` tick) rather than silently ignored, even though none of them change any other visible field.
- **queue-and-status**: 1 (untouched prior) turn; `queue` fully populated from `thread/queueChanged`; `status.type` flipped idle→active; `modelProvider`/`model` correctly *split* from the hydrate-time duplicate (`"anthropic/claude-sonnet-4-5"` for both) into `"anthropic"` / `"claude-opus-4-1"` via `thread/model/changed`; `tasks: {total:5, done:2}`; `name` updated via rename. `lastFrameAt` = 1004, matching 5 applied notifications.

All four matched intent on first generation; no snapshot needed a second look after edits.

## Concerns (judgment calls worth double-checking)

1. ~~**`turn/completed` bypasses `notificationTargetsThread`.**~~ **RESOLVED in the fix wave below** — the original version applied `turn/completed` unconditionally to any model where the turnId happened to already exist in `model.turns`. Since turn IDs are per-thread sequential (`turn_%d`), that is unsafe across threads on a multiplexed transport; see "Fix wave" below. `notificationTargetsThread` itself is unaffected: it still faithfully returns `false` for `turn/completed` (no ref/threadId to check), which is now the SAME answer the case's own gate effectively enforces via `activeTurnId`.
2. **`model`/`modelProvider` at hydrate time.** The wire `Thread` snapshot has only one relevant field, `modelProvider` — `appwire/types.go` documents it as intentionally overloaded ("ModelProvider (on Thread, not here) stays the model field"). I set both `ThreadModel.modelProvider` and `.model` from that single value at hydrate, with `thread/model/changed` later splitting them properly (demonstrated in the queue-and-status fixture/snapshot). Alternative would have been leaving `.model` empty until the first live update; I judged the duplicate more useful for an initial render. Flagging since it's not spelled out in the brief.
3. **`askPending` item-lifecycle rule.** Inferred (not in the brief) from `agent/session_tools_ask.go` + `appwire/types.go`'s `AskPending` doc comment: flips `true` on `item/started` and `false` on `item/completed` for a `commandExecution` item with `toolName === "ask_user"`. Grounded in source, but it's my own inference of "item lifecycle," not literal brief text — worth confirming against whatever task actually renders the ask-user UI.
4. **`images`/`outputImages` → `string[]`.** The locked `ItemModel` type demands `string[]`, but the wire gives structured `InputItem[]`/`OutputImage[]`. I take `url ?? path ?? name` (mirrors `renderToolOutputImages`'s `img.url` convention in the legacy `renderer.js`). No fixture line currently exercises a non-empty images array (none of the 4 required scenarios call for one), so this mapping is typechecked but not test-covered — flagging rather than inventing a 5th untested scenario outside the brief's list.
5. **`startedAt`/`completedAt` epoch→ISO.** Wire gives epoch-ms numbers; locked `ItemModel`/`TurnModel` want `string`. Converted via `new Date(ms).toISOString()`. `durationMs` stays numeric (matches the locked type). Reasonable, but confirm it's the format later UI/formatting code expects.
6. **No `@types/node` in this project** (checked `package.json` and `node_modules/@types/`) and I'm forbidden from touching `package.json`/`tsconfig.json`. The brief's fixture-loading pseudocode implied `node:fs`; I used Vite's native `?raw` import instead (`import basicTurnFixture from "./fixtures/basic-turn.jsonl?raw"`), ambiently typed by the `"vite/client"` entry already in `tsconfig.json`'s `types` array — no config changes needed. Flagging since another stream hitting the same wall might not immediately know why `node:fs` fails to typecheck here.

## Verification run (at commit `3ef1c416c`, before the fix wave below)

```
npm run test -- protocol/reducer   → 14/14 pass
npm run test                       → 15/15 pass (incl. App.test.tsx), pristine
npm run typecheck                  → clean
npm run lint                       → clean
```

## Self-review checklist

- Only my 8 owned files touched (verified via `git status` before and after commit — no forbidden files).
- `npm ci` run first in a fresh worktree (Step 0), succeeded.
- TDD followed throughout, including the mid-review upsert fix (test written and confirmed failing before the fix).
- Every fixture line's field names cross-checked against Go source, not guessed.
- Reducer is pure throughout: every branch returns either the *same* model reference or a *new* object, verified explicitly via `toBe`/`not.toBe` in tests, not just inferred. Corrected wording (an earlier draft of this bullet mis-stated it): "item/turn not found" branches return a *new* object (`{...model, lastFrameAt: now}`) — only "unhandled method", "known method that doesn't target this thread" (`notificationTargetsThread` false), and (as of the fix wave) "turn/completed for a turn that isn't this model's active turn" return the *same* reference.
- Commit message matches the brief exactly.

## Fix wave 1 — cross-thread turn/completed safety (coordinator review)

**Commit:** `3dabccf81` — `webui: reducer — turn/completed gated to the active turn (cross-thread safety)`

**Finding (Important, from coordinator review):** turn IDs are per-thread
sequential (`turn_%d`, `internal/appprojector`), and `TurnCompletedParams`
carries no `ref`/`threadId`. My original guard only checked that `turnId`
existed *somewhere* in `model.turns` — so thread A's `turn/completed` for its
own `turn_1` could overwrite thread B's unrelated, already-settled `turn_1`
if it were ever (mis)delivered to B's model, silently corrupting B's history
and, if B's `activeTurnId` happened to equal `turn_1`, clearing it too. This
was also the one place the reducer broke the same-reference-for-inapplicable-
notifications invariant (it always returned a new object via `{...model,
lastFrameAt: now}` even when the turn genuinely didn't belong to this model).

**Fix:** gated the `turn/completed` case on `model.activeTurnId === turnId`
(reducer.ts). A turn can only complete while active, and only one turn is
ever active per thread at a time, so this is both the safe *and* correct
match — simpler than the old "does a turn with this id exist anywhere in
history" check, and it restores the same-reference invariant for the
non-matching case (a true no-op `return model`). Added a doc comment on
`applyNotification` itself (not just the case) stating the store-layer
routing contract: `turn/completed` must be delivered only to the model whose
`activeTurnId` matches `turnId`; this gate is the reducer's independent
second line of defense, not a substitute for correct subscription routing;
and the one case it structurally cannot resolve — two panes simultaneously
mid-turn on the exact same numbered `turn_N` — is left to the store's
subscription-based delivery, since both models would pass the gate.

**RED (verified before committing the fix):** temporarily removed just the
new guard line (`if (model.activeTurnId !== turnId) return model;`),
re-ran the new test alone:

```
$ npm run test -- protocol/reducer -t "does not cross-apply"
FAIL  ... turn/completed does not cross-apply to a different thread's same-numbered turn
AssertionError: expected +result not to equal modelB   (toBe failed)
  - "id": "item_b1", "text": "B's own answer"
  + "id": "item_a1", "text": "A's answer"
```

Confirmed this reproduces exactly the described bug (B's own settled turn
overwritten by A's content) before restoring the fix.

**New test:** `turn/completed does not cross-apply to a different thread's
same-numbered turn` (reducer.test.ts) — builds thread A (active `turn_1`)
and thread B (settled, unrelated `turn_1`), applies A's `turn/completed` to
B's model, asserts `result` is B's model by reference (`toBe`) and B's item
text (`"B's own answer"`) is untouched. Also asserts, as a sanity check in
the same test, that the identical notification *does* correctly settle A's
own model (`not.toBe`, item text `"A's answer"`) — proving the gate accepts
the right target and rejects the wrong one, not just one or the other.

**GREEN (final):**

```
npm run test -- protocol/reducer   → 15/15 pass (incl. the new sibling-immunity test)
npm run test                       → 16/16 pass (incl. App.test.tsx), pristine
npm run typecheck                  → clean
npm run lint                       → clean
```

All 4 existing golden-fixture snapshots are byte-identical to before this fix
(`git diff --stat` on `__snapshots__/reducer.test.ts.snap` shows no changes)
— confirmed, since every fixture's `turn/completed` always completes that
same model's own active turn, so the new gate is a no-op for all of them.

Also corrected: the stale rationale comment on the existing test
("`turn/completed` applies... unconditionally") now describes the
`activeTurnId`-gated behavior instead.

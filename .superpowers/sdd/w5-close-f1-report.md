# W5 close fix F1 — reducer honesty report

Branch `w5f-reducer`, base `2510b8adc`.
Commits (pre-report): `a45644eef` (Item 1), `034ff8546` (Item 2).

## Status: DONE_WITH_CONCERNS

Both items implemented, TDD RED-first, all gates green. Concerns are doc-drift
follow-ups in sibling-owned (rendering-layer) files that my data-layer change
makes stale — deliberately not edited per the brief's "do NOT edit rendering"
boundary and this stream's file ownership. Details at the end.

---

## Item 1 — askPending is wire-authoritative (stop the reducer clobbering it)

### What I found
The wire's thread-level `askPending` (`SerfThread.askPending`, `types.gen.ts:655`)
mirrors the daemon's long-lived `HasPendingAsk` ("this session is waiting on a
human answer", `agent/session_tools_ask.go:67`). It is **snapshot-only**: it
appears on `SerfThread` and on **no notification** (verified — `askPending` occurs
exactly once in `types.gen.ts`, on `SerfThread`; no notification param carries it).

`protocol/reducer.ts` was overwriting that field from item lifecycle:
- `item/started`: `askPending: isAskUserItem(item) ? true : model.askPending`
- `item/completed`: `askPending = isAskUserItem(item) ? false : model.askPending`

So whenever an `ask_user` item opened/settled, the reducer **inferred** a
thread-level value from an item event — clobbering the wire's authoritative
snapshot value even though no wire message said the thread field changed. This
conflates two distinct concepts: the thread-level wire signal vs. the AskDock's
own item-derived pending set (`composer/askDock`, which is correct and untouched).

### What I changed and why
`protocol/reducer.ts`:
- Removed the `askPending` recompute from `item/started` and both return paths of
  `item/completed` (3 sites).
- Removed the now-dead `isAskUserItem` helper + its doc comment.
- Kept `hydrateThread`'s `askPending: thread.serf.askPending ?? false` — the wire
  snapshot is the single, authoritative writer of this field.

Net: the reducer no longer fabricates or clobbers the thread-level field; only a
wire snapshot (`thread/read` → `hydrateThread`, i.e. initial hydrate and every
reconnect re-hydrate) sets it, which is exactly what the field means.

### RED-first evidence
Rewrote the existing test `askPending flips from thread snapshot and item lifecycle`
(which encoded the buggy behavior — it asserted the flip at `:1040`/`:1062`) into two
honest-contract tests. Against the **unfixed** reducer:
```
× item lifecycle never clobbers the wire's thread-level askPending
  AssertionError: expected false to be true   (item/completed(ask_user) clobbered true→false)
```
The `askPending is wire-authoritative from the thread snapshot` test passed already
(snapshot path was never the defect). After the fix: both green (58 reducer tests).

### Mutation verification (net-zero)
Re-introduced the clobber (`askPending: false` on `item/completed`'s existing-turn
return), ran the suite → `item lifecycle never clobbers…` bit
(`expected false to be true`). Reverted to a zero net diff; `git diff` of
`reducer.ts` shows only the 4 intended removals.

### Consumer grep (before → after)
Non-test readers of `model.askPending`:
- **Before & after — unchanged set.** Only one non-test reader:
  `panes/session/transcript/flow/useTranscriptScroll.ts:85`
  (`isAttentionWorthy` → `pillNeedsYou`).
- `Composer.tsx` does **not** read `model.askPending`; it reads the item-derived
  `useAskDockPending(ref)` (askDock). So the composer never relied on the recompute.

**Why `useTranscriptScroll` needs no repoint** (checked before and after): its live
"needs-you" signal for a genuinely resting ask is carried by
`model.status.type === "awaiting"`, not by the item-recomputed askPending. A pending
`ask_user` call rests the session in `awaiting` (`agent/events/payloads.go:21`:
"ask_user call at the transcript tail rests 'awaiting'"), emitted **live** via
`thread/status/changed` (`internal/appprojector/appwire_projection.go:836`,
`p.threadStatus(state)`), which the reducer already folds into `model.status`
(`reducer.ts` `case "thread/status/changed"`). So dropping the item-lifecycle
askPending recompute does not regress `pillNeedsYou`. `model.askPending` now
honestly reflects the wire snapshot (also `awaiting`-aligned). No repoint of
askDockStore/AskDock was required either (they were already correct).

---

## Item 2 — carry tool-result item `error` into `ItemModel`

### What I found
The wire's `ThreadItem` carries `error?: string` (`types.gen.ts`), populated on
**both** paths:
- **Live**: `EventToolCallEnd` sets `Error: data.Error`
  (`internal/appprojector/appwire_projection.go:436`), with `Status: "completed"`
  hardcoded regardless of error (`:437` region — the known Go limitation).
- **Snapshot/reload**: `internal/apptranscript/apptranscript.go:358` sets
  `item.Error = StringifyToolContent(...)` when `part.ToolResult.IsError` (else it
  sets `Output`), also with `Status: "completed"` hardcoded.

But `ItemModel` had no `error` field and `wireItemToModel` dropped it, so a
denied/errored tool call (e.g. a denied `ask_user`) was indistinguishable from a
clean completion — status is always `"completed"`, and the failure text was gone.

### What I changed and why
- `protocol/model.ts`: added `error?: string` to `ItemModel` (with a comment on why
  presence — not status — is the failure signal, since status is hardcoded).
- `protocol/reducer.ts`: mapped `error: item.error` in `wireItemToModel`. This is the
  **single** conversion point for every path — live `item/started`/`item/completed`,
  `turn/completed` (full itemsView), and snapshot `hydrateThread` all fold through it —
  so one line covers both the live-notification and snapshot/reload paths the brief
  asked for. The merge helpers (`mergeReasoning`/`mergeArguments`/`mergeObservedTiming`)
  and `settleItem` all spread the fresh model, so `error` is preserved through
  settlement with no extra work.

### RED-first evidence + tests for both paths
Added two tests (field added to `ItemModel` first so they typecheck). Against the
**unmapped** reducer:
```
× item/completed maps the wire item's error onto the model (live path)
  AssertionError: expected undefined to be 'denied: user rejected'
× hydrateThread maps a settled item's error onto the model (snapshot path)
  AssertionError: expected undefined to be 'exit status 1'
```
After adding `error: item.error`: both green. (This RED→GREEN is itself the
bite-evidence — the tests fail precisely when the mapping is absent.) The live-path
test also asserts `status === "completed"` alongside `error` set, documenting that
error presence is the honest failure signal.

### Snapshot fixtures
Adding an item-level field grew the 4 fixture snapshots. Regenerated with
`vitest -u`; the diff is **purely additive**: 8 `"error": undefined` lines (one per
fixture item — no fixture exercises an errored tool), zero removals, no other model
field moved.

### Nothing Go-blocked for the error field
Both live and reload paths deliver `item.error` today, so the mapping is
end-to-end real on both. The **only** Go-blocked piece is the hardcoded
`Status: "completed"` (explicitly out of scope per the brief) — which is exactly why
`error` presence (not status) must be the failure signal.

### Rendering follow-up this field enables (data layer only, per brief)
The field is the data layer; rendering is sibling territory. It enables:
- `transcript/tools/shellTool.tsx` to read `item.error` directly as a failure signal
  instead of its current fragile "`[exit <N> · …]`" text-footer heuristic (its own
  header documents error was "dropped by wireItemToModel").
- `ToolCallItem`/`AskDock` to distinguish a denied/errored `ask_user` from a clean
  completion.

---

## Gates (from `cmd/serf-hub/frontend`)
- `npx tsc --noEmit` — clean.
- `npx vitest run` — **136 test files, 2026 tests, all pass** (baseline was 136 files
  / 2023 tests; +3 net: rewrote 1 askPending test into 2, +2 error tests). File count
  unchanged at 136 (all changes in existing `reducer.test.ts`).
- `npm run lint` (Biome) — clean.
- `npm run build` — succeeded; `git restore dist/PLACEHOLDER` done; worktree clean,
  no built dist tracked.
- Each commit AND-chained `tsc && vitest && lint` before committing.

### Flake observed (NOT mine — the sibling flake stream's territory)
`src/protocol/tokenFlood.test.tsx > … render-count probe` failed **once** under
full-suite load, then passed 3/3 in isolation and on the committing full-suite runs.
It is a timing-sensitive 500-delta render-count probe that streams `agentMessage`
deltas (no `ask_user`, no error). My change cannot affect it: for non-ask items the
removed line resolved to `model.askPending` — the same value the `...model` spread
already carries — so the produced objects are value-identical and render behavior is
byte-identical. Pre-existing flake, flagged for the flake-investigation sibling stream.

---

## Concerns / follow-ups (deliberately NOT edited — sibling/rendering territory)
Item-ownership boundary respected: these are rendering-layer files outside this
stream's manifest, and the brief says do not edit rendering. My changes make three
comments stale; handing them off rather than editing:

1. `transcript/tools/helpers.ts:1-9` — header states `ThreadItem.error … [is] dropped
   by wireItemToModel`. **Now false** for `error` (still true for `raw`). Update when
   the rendering owner wires `item.error`.
2. `transcript/tools/shellTool.tsx:1-19` — header states `ThreadItem.error … is
   dropped … Neither field is a usable failure signal here`, justifying the text-footer
   heuristic. **Now false**: `item.error` is available and is the cleaner signal.
3. `transcript/ToolCallItem.tsx:3` — cites `reducer.ts's isAskUserItem`, which Item 1
   removed. Dangling citation; the underlying fact (ask_user is `commandExecution` +
   `toolName "ask_user"`) is still true.

None of these are functional regressions; all three are doc-drift the rendering owner
(or a maintaining-documentation pass) should reconcile.

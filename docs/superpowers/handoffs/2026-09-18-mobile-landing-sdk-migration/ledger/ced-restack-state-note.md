# C/E/D restack — state note (lane retiring, past 540k tokens)

Worktree: `.claude/worktrees/sdk-d21-native-model` (absolute path from repo root). Task: restack the
three held native pieces of #1580's D23c-2b split (C, E, D) onto main after piece A landed
(#1859/#1892/#1893/#1894/#1895), holding B3's and D6's independent conversation.ts work.

## Status: done, all three pushed, disposition comments posted, gates green

All three PRs report `mergeable: true` as of this note.

## The three heads and stacking order

Original stacking (from `git log --first-parent`): E and D both branched directly off C's OLD tip
(`57476d548`), not off each other. Per the coordinator's instruction, restacked in the order
**C → E → D** (E rebased onto C's new tip; D rebased onto E's new tip, not onto C directly).

| Piece | PR | Branch | Old head | New head (pushed) |
|---|---|---|---|---|
| C | #1737 | `claude/c2b-c-native-rows-are-a-projection` | `57476d5481e5ad81ecca9fe12f8b0cb285ea598b` | `d4f256200de2ddce2f6d6d9d4679a136e3e34a0b` |
| E | #1738 | `claude/c2b-e-question-sheet-bounded-identity` | `36cfd4818aeb22d758faec1a953dfd6f8b1fc693` | `ffb4cbae17a030601da3b5034fbf8787e6f37f4a` |
| D | #1740 | `claude/c2b-d-native-page-history-cutover` | `1a0b754ae5b7530957321f082746e3ec3df7d277` | `55c6eab8b6fe04b09f5d692869651440019b7fbd` |

Each pushed with `--force-with-lease=<branch>:<old head>` from the table above.

## Rebase mechanics (C specifically)

C's branch history was NOT 4 commits — `git log --first-parent e692ca1e7..57476d548` showed 13
commits. The bottom 9 (`aa9f96408`..`ccdd9c96e`/`ba45d3936`, the ask-resolution work plus a web
warning-guard fix, plus a no-op "merge: origin/main into piece A" commit with no diff of its own)
were byte-equivalent to what's already squash-merged on main as #1892/#1893/#1894/#1895. Rebasing
all 13 naively (`git rebase --onto origin/main <merge-base>`) tries to replay dozens of already-landed
main commits and explodes into unrelated conflicts. Fix: moved the rebase base up to C's last
redundant commit (`ba45d3936`) and replayed only the 4 commits that are actually C's own
(`8cd55f9e7` "rows are a projection of the model", `eead00500`, `94b09b288`, `57476d548`). E and D,
being clean 3-commit and 4-commit branches respectively, didn't have this problem — plain
`git rebase --onto <new-base> <old-base> <branch>` worked directly.

**Lesson for future rebase-a-held-decomposed-piece work:** before rebasing, always check
`git log --first-parent <merge-base-with-old-main>..<branch-tip>` for a no-op "merge origin/main"
commit or redundant early commits that duplicate content since squashed onto main under different
SHAs — replaying those wastes enormous effort resolving conflicts against yourself.

## Hunk-resolution rules applied (which of main's mechanisms won where)

The general rule, per the brief: keep main's landed mechanism, re-express the piece's own change on
top, never reintroduce a re-derivation A (or a later main fix) already deleted/improved. Concretely:

- **Warning composition** — main's `joinWarningParts`/`hasWarningText` (package, type-safe, `unknown`-
  guarded) always won over any piece's own ad-hoc `.trim()`/`.filter()` composition. This directly
  fixed the High "crash on non-string title/hint" finding all three r3 reviews flagged independently —
  it was the SAME finding on all three PRs because all three inherited the same ad-hoc filter from a
  common ancestor.
- **Reasoning precedence** — main's landed length-comparison heuristic (`#1859` round: "a SETTLED
  item's text is always authoritative... an ACTIVE item's joined summary overtakes its seed as soon
  as a delta arrives") won over C/D's original simpler `reasoningText(item)` (chunks-always-win) —
  *initially*. This turned out to be the one place I got the resolution wrong on first pass (see the
  bug-hunt below): D's own test asserted the opposite for the settled+non-blank case, and tracing the
  reducer's actual `mergeReasoning`/`mergeCompletedText` contract plus main's OWN parallel test
  (`project.test.ts`'s "shows the completion's authoritative text over a stale seeded
  reasoningSummaries entry") confirmed main's heuristic is correct and D's test was stale. Fixed D's
  test, not the heuristic.
- **Image folding** — main's reducer-level "absent/empty input images = unchanged, never removed"
  rule (already baked into the model before any of C/D/E's code runs) meant no store-side
  compensating logic was needed at all; D's cutover reads attachments straight off the already-folded
  model item via `itemAttachments`, so this mechanism needed no reconciliation — it "wins" simply by
  main's fix living below the layer C/D touch.
- **askPending-keyed caching** — C's own later commit (`94b09b288`, applied cleanly, no conflict)
  already fixed `liveAsksFor`'s memo to key on `{turns, askPending}` not `turns` alone; this predates
  and is orthogonal to the mechanism question above.
- **Identity/truncation helpers** — D's own cutover (import directly from `project.ts`, no local
  copies) is the FINAL, correct state; the intermediate state (C re-exporting from `state/
  conversation.ts` to fix the Medium duplication finding) was a necessary stepping stone since C
  doesn't get to delete the store's own copies (D's job) but shouldn't leave THREE copies either.
- **B3's turn/cursor coherence (#1919/#1920)** and **D6's conversation.ts work** — never touched by
  C/E/D's own commits; no conflicts arose with them because they live in different regions of
  `state/conversation.ts` (loadOlder/rehydrate cursor logic, session-usage derivation) than the
  notification-switch statement C/D's history collides with. Nothing to reconcile — confirmed by
  checking the diffs didn't overlap, not by assumption.

## The package bug found and fixed: mapTurn/settleFirstMatchingTurn array identity

**File:** `appwire-client/typescript/reducer.ts` (package-level, shared by web and native — NOT
owned by any of C/E/D, but required to make D's own committed tests pass).

**Bug:** `mapTurn`/`settleFirstMatchingTurn` both used `turns.map((t) => ...)`, and
`Array.prototype.map` allocates a new array UNCONDITIONALLY, even when every mapped element comes
back identical to its input (i.e., when the named `turnId` matches nothing in the array at all). The
doc comment above `mapTurn` claims "turns not matching pass through unchanged (same reference)" —
true per-element, false for the outer array. D's `applyNotification` (mobile's store) depends on
`applied.turns === state.conversation.turns` to detect "the reducer couldn't place this frame
anywhere" (a warning/steer/turn-completed naming a turn outside the loaded window) and trigger an
authoritative reread. With the always-new array, that gap was NEVER detected — the phone would show
a silently incomplete transcript until some unrelated later resync.

**Fix (commit `107c797a7` on D, `claude/c2b-d-native-page-history-cutover`):** both functions now
track whether anything actually changed and return the ORIGINAL array reference when nothing did.

**How it surfaced:** 3 of D's own tests failed after the mechanical rebase —
`"rereads when a warning/an injected steer/a turn settle names an active turn this window does not
hold"` (`mobile/src/state/conversation.test.ts`). Traced through `case "warning"` and
`case "turn/completed"` in `reducer.ts` to find both call `mapTurn`/`settleFirstMatchingTurn`
directly with NO pre-check (unlike `item/agentMessage/delta` etc., which guard via `findItemTurnId`
before ever calling `mapTurn`) — confirming the gap-detection assumption was violated specifically
for these three notification types.

**Test coverage:** `reducer.test.ts` (216/216, unchanged — no regression) is the package's own
suite; the 3 mobile tests above now pass. No NEW test was added for `mapTurn`/`settleFirstMatchingTurn`
directly (they're unexported internals) — the fix is proven by the 3 existing mobile tests that
depend on the contract, which is the same falsification pattern the reducer's own doc comments use
elsewhere (assert behavior through the public case handlers, not the private helper).

**Residual worth flagging to the coordinator:** this bug likely affects OTHER notification types
too if any other `case` handler calls `mapTurn`/`settleFirstMatchingTurn` without a `findItemTurnId`-
style pre-check — I did not audit every case handler for this pattern, only the 3 that had failing
tests (`warning`, `turn/completed`, `evener/steering/injected`). Worth a targeted audit if anyone
hits a similar "gap not detected" symptom elsewhere.

## r3 findings: fixed / refuted / deferred, per piece

### #1737 (C) — `reviews/1737-r3.md`
- High (`.trim()` crash) — **fixed** in the rebase itself (joinWarningParts).
- Medium 1 (question refs truncated before storage) — **out of scope for C, deferred to E** (E owns
  `mobile-native/src/questionAnswers.ts`; confirmed E's `pendingQuestions` already reads canonical
  `liveAsksFor` refs, not truncated `conversation.items`).
- Medium 2 (live path drops attachments on image-less completion) — **already fixed** by #1859
  (`fc64c7575`), confirmed present at C's rebased head, untouched by C's own commits.
- Medium 3 (truncation/cap constants duplicated across 3 files) — **fixed**, commit `f399f03a9`:
  `state/conversation.ts` imports+re-exports from `project.ts`; `services/conversation.ts`'s unused
  copy deleted.
- Low (vacuous unparseable-ask test) — **fixed**, commit `d4f256200`.

### #1738 (E) — `reviews/1738-r3.md`
- High (warning crash) — **fixed**, inherited from C's rebase.
- Medium (question signature incompatible with drafts) — **already fixed** by E's own later round 4
  (`questionsIdentity` JSON-array-of-`{key,digest}` rewrite).
- Medium (stale `items` on askPending-only change) — **confirmed open, deferred to D** (the whole
  dual-write incremental applier this describes is what D's cutover deletes).
- Medium (live reasoning deltas ignore summaryIndex) — **confirmed open, deferred to D** (same
  mechanism, same reason).
- Medium (`liveAskQuestions` collapses undefined to empty) — **refuted**: `ThreadModel.askPending`
  is a required `boolean` (`model.ts:242`), unreachable in production.
- Medium (`turnRowCache` stale on in-place mutation) — **refuted**, per measurement already recorded
  in C's own commit (`8cd55f9e7`): the reducer never mutates a `TurnModel` in place, so the
  WeakMap-keyed cache invalidates on any real content change; `askState` is checked explicitly
  because answerability is the one whole-model property that doesn't change the turn's own reference.
- Medium (live warning path keeps legacy failure row) — **already fixed** via #1895
  (`foldWarningParams`/`joinWarningParts`).
- Low (`rawWarningFrame` allocates before truncating) — **already fixed** via #1893
  (`prunedForStringify`).
- Low (`liveAsksFor` leaks a mutable Map) — **confirmed open, deferred** (Lows rule) — not filed as
  its own issue (low risk, no current caller mutates it); noted in the PR comment only.
- Low (duplicate identity/truncation helpers, `conversation.ts` vs `project.ts`) — **confirmed open
  for the identity family** (truncation half already fixed on #1737) — **filed as
  prime-radiant-inc/evener#1925**, new owner: whoever picks up the follow-up-PR Lows batch. (D's
  own cutover, landing after this note, actually retires #1925 by importing the identity helpers
  directly — worth closing #1925 once D merges, referencing D's commit.)

### #1740 (D) — `reviews/1740-r3.md`
- Medium (`.trim()` crash) — **fixed**, inherited from C.
- Medium (answer submission reads truncated copies, not canonical refs) — **already fixed**,
  inherited from E.
- Low (unused `isInProgressStatus` import) — **fixed**, commit `55c6eab8b`.
- Low (attachment filenames bypass display bound, `truncateItem`'s `attachments` case /
  `TranscriptImages.tsx`) — **confirmed open** — **filed as prime-radiant-inc/evener#1930**, new
  owner: same follow-up-PR Lows batch as #1925.

## GitHub issues filed this lane
- **#1925** — `mobile: state/conversation.ts duplicates project.ts's row-identity helpers` (Low, from
  #1738-r3). Likely moot once D merges — check and close referencing D's commit if so.
- **#1930** — `mobile: truncateItem never bounds an attachment's filename` (Low, from #1740-r3). Still
  open after D; needs its own small fix in `project.ts`'s `truncateItem` `attachments` case.

## Gates cheat-sheet (mobile/src + mobile-native, from `.claude/worktrees/sdk-d21-native-model`)

- **vitest, shared `mobile/src` files**: `cd mobile-native && npx vitest run --root .. --config
  mobile-native/vitest.config.mts <path(s)>` — e.g. `mobile/src/conversation/project.test.ts
  mobile/src/state/conversation.test.ts mobile-native/src/liveImages.test.ts`. Full shared suite:
  same command with just `mobile/src` — 820 tests, 7 files, at the final head.
- **vitest, mobile-native's own suite**: `cd mobile-native && npx vitest run` (no `--root`/`--config`
  override needed) — 825 tests, 86 files, at the final head.
- **vitest, package files** (`appwire-client/typescript`): `cd cmd/evener-hub/frontend && npx vitest
  run ../../../appwire-client/typescript/<file>.test.ts` — e.g. `reducer.test.ts`, 216 tests.
- **typecheck, native**: `cd mobile-native && npm run check` (tsc, `tsconfig.check.json`). Required
  whenever a package port/exported type/`project.ts` export changes.
- **typecheck, web**: `cd cmd/evener-hub/frontend && npm run typecheck`.
- **biome, package**: `cd cmd/evener-hub/frontend && npx biome ci ../../../appwire-client/typescript`.
  NEVER run biome (even `check --write`) over `mobile/` or `mobile-native/` — no config there, a bare
  run retabs whole files.
- **lint, web**: `cd cmd/evener-hub/frontend && npm run lint` (biome over `src` + the package).
- **`make lint-package-imports`**: from the repo root. Run whenever any package file changes.
- **`make test-api-package`**: from the repo root. Run whenever `index.ts`/`tsconfig.build.json`/the
  qualifier changes, or (as here) any package file changes and you want extra confidence.
- **Falsification pattern used throughout**: for a rebase-conflict resolution, run the piece's own
  touched test files before AND after taking one side of a hunk to confirm which side's assertions
  the resolved production code actually satisfies — do not trust either side's comment/claim without
  running it. This is how the `mapTurn` bug and the reasoning-precedence test bug were both found:
  neither was a flagged review finding, both were "the rebased tests don't pass, why."
- **Node_modules**: `cmd/evener-hub/frontend/node_modules` and `mobile-native/node_modules` were real
  directories (not symlinks) in this worktree, already populated — never ran `npm ci`.
- **One process slip, corrected**: briefly created a scratch git worktree at `/tmp/d-original-check`
  outside the assigned worktree to A/B-test a hypothesis against D's pre-rebase branch tip; removed it
  immediately via `git worktree remove --force` (not `rm -rf`) once caught. No files in the assigned
  worktree or the three PR branches were affected. Flagging so the coordinator knows it happened,
  even though the leftover cleanup was already back to a clean baseline before this note.

## Stopping here per the coordinator's instruction (past 540k tokens)

All three PRs (#1737, #1738, #1740) are pushed, `mergeable: true`, disposition comments posted with
full hunk lists and gate results. Nothing else owed by this lane.

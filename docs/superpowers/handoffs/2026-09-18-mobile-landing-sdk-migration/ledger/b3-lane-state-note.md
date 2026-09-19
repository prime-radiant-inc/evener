# D18 B3 lane state note (retired 2026-09-18 ~14:35 PDT, near 500k tokens)

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/sdk-d17-activity-panel`.
Working tree clean, on branch `b3a-session-usage-totals`. Original PR #1885 (`claude/sdk-d18-b3-
phone-usage-totals`) closed after five review rounds per the "five review rounds means decompose"
rule; commented "Decomposed after five rounds into #1919, #1920; closing." and closed via `gh pr
close 1885`. That branch's final commit `8ab8c4fba` (plus a local-only merge commit `d09fc0c02`
pulling in current `origin/main`, used only to compute clean path-filtered hunks — never pushed
past `8ab8c4fba`) is otherwise dead; the two split PRs below are where the work actually lives.

## Landed as two stacked PRs, both base main

### #1919 — B3b, `fix(mobile): the conversation keeps its turns and wire cursor coherent across
paging and rehydrate`
Branch `b3b-turns-wire-cursor`, head `d07d60263c4d0997d08cf45c2c24de4bfcaaa87c`. State-layer
mechanism only: `mobile/src/state/conversation.ts` + `mobile/src/services/conversation.ts`'s turn
plumbing. `git diff -M --stat origin/main...HEAD`: 4 files, 577 insertions(+)/2 deletions(-); 102
non-test lines (84 in `conversation.ts` + 18 in `services/conversation.ts`).

What it closes: the store's own top-level `olderCursor` (a UI-only, intentionally capped "is there
another page" signal, F8) and the conversation's own `ThreadModel.olderCursor` (the wire truth
`sessionTokens` reads) are two different values that five review rounds kept finding collapsed
into one, at a different site each time:
- `loadOlder` derives `conversation.olderCursor` from the wire's own `nextCursor`, never the
  store's capped field.
- `rehydrate` preserves page-loaded turn history (a same-session reread's own itemLimit-bounded
  window can't see turns an earlier `loadOlder` fetched), gated on turn ownership tracked
  SEPARATELY from item ownership (`pageOwnedTurnIds`, alongside the pre-existing `pageOwnedIds`) —
  a page whose display rows were all deduped/evicted still keeps its turns for the merge.
- `rehydrate`'s cursor carries the prior conversation's own wire cursor only when the merge
  actually contributed a turn beyond the fresh read's own window, not whenever page history merely
  exists somewhere in the session's lifetime (my own round-4 fix for the first bug reintroduced a
  narrower version of it at the rehydrate site; the panel caught that too).
- Both merges (loadOlder's and rehydrate's) go through the package's `mergeOlderItemPage`
  (`turnsMatch`/`mergePageTurn`) instead of an id-only filter — `thread/turns/list` is itself
  item-paginated, so a turn can split into fragments across a page boundary, or two turns can be
  logically the same but carry different ids. `ConversationService.loadOlder` and
  `ConversationReadProjection` now both expose the wire `ThreadTurnsListResponse` (`turnsPage`) so
  the store folds it through the package's real merge, never a second hand-rolled one.

Per-state table (all 5 rows, one test each) lives as a comment + two `describe` blocks ("D18 B3
round 5" and "D18 B3 round 6") in `mobile/src/state/conversation.test.ts`.

Falsified: reverted `services/conversation.ts` + `state/conversation.ts` alone to `origin/main`,
ran the two test files — 8 failed; restored, green. Gates: 485 targeted tests, 798 full
`mobile-native` suite (3 pre-existing `react-test-renderer` file-load failures, present on
`origin/main` already), 801 shared `mobile/src` suite, `npm run check` at the same 4 pre-existing
errors as `origin/main`.

### #1920 — B3a, `feat(mobile): session usage totals from the package store (D18 B3)`
Branch `b3a-session-usage-totals`, head `d40bf549bf3273d4154dae3eb668da692cc089b2`. Stacked on
#1919 (footer half only — its own PR body says "closes nothing else", cites the decompose ruling).
`git diff -M --stat origin/main...HEAD`: 8 files, 237 insertions(+)/33 deletions(-); 117 non-test
lines.

What it does: `mobile-native/src/transcriptPresentation.ts`'s `accountingFor` derives the footer's
token total from the package's `sessionTokens(conversation)` (the same turn-summed-with-scope
derivation the web `DetailsPanel` uses) instead of reading `conversation.usage` directly; the
thread's own cumulative `cacheReadTokens`/`totalTokens` breakdown is read independently of the
derived pair and never inherits its scope; `usageRows` gives each footer row its own unit so a
turn-summed "loaded" pair can't leak its label onto the whole-session cache/total figure; a Go
zero value on `cacheReadTokens`/`totalTokens` is treated as absent, matching `sessionTokens`'s own
rule for input/output. `tokenUnitLabel` moved into `appwire-client/typescript/threadUsage.ts` so
the web panel and the native footer share one function instead of two copies drifting apart
(`DetailsPanel.test.tsx` untouched, still green — the rendered string never moved).

Falsified: reverted the 6 production files alone to `origin/main`, ran
`mobile-native/src/transcriptPresentation.test.ts` — 10 failed; restored, green. Gates: 36 targeted
tests, 807 full `mobile-native` suite / 790 shared `mobile/src` suite (same pre-existing failures as
B3b), `threadUsage.test.ts` + `DetailsPanel.test.tsx` (42 tests) from `cmd/evener-hub/frontend`,
`biome ci`/`npm run lint` clean, `npm run typecheck` at the same pre-existing error count.

## How the split was built (in case the pattern recurs)
Both PRs are FILE-DISJOINT (B3b: `mobile/src/state|services/conversation*.ts`; B3a:
`mobile-native/src/transcriptPresentation.ts`/`TranscriptUsage.tsx`,
`appwire-client/typescript/threadUsage.ts`/`index.ts`,
`cmd/evener-hub/frontend/.../DetailsPanel.tsx`/`detailsAccounting.ts`) — no merge conflicts between
them, so "stacked" is a review/merge-order convention (B3b lands first; B3a's footer code doesn't
literally need B3b's commits to compile since it only calls the already-merged package's
`sessionTokens`), not a literal git stack. Both branch straight off `origin/main`.

Building the hunks: `git diff origin/main..<old-branch-head> -- <paths>` then `git apply` on a
fresh `origin/main` checkout — **only works cleanly if `<old-branch-head>` is a descendant of the
CURRENT `origin/main`** (a merge commit, not just the branch's own history). I got this wrong once:
two other lanes' commits landed on `mobile/src/state/conversation.ts`/`.test.ts` while I worked, and
diffing my old (non-descendant) branch head straight against fresh `origin/main` produced a diff
that included REVERTING their work (741/138 line "changes" instead of my real ~200). Fix: `git
merge --no-ff origin/main` into the old branch first (one import-list conflict, trivial union
resolve), verify tests, THEN take the path-filtered diff from the merge commit. Worth remembering
for the next multi-round-then-decompose lane.

## Residuals not fixed, filed nowhere yet (flag for the coordinator)
- **B3a's Low-adjacent residual (not itself a finding, mine)**: the round-4 disposition already
  flagged that `DetailsPanel.tsx`'s Usage section never surfaces `cacheReadTokens`/`totalTokens` at
  all (only the native footer does) — asymmetric surfaces by design, not a bug, but worth a
  standalone issue if anyone wants web parity there.
- Turns are never capped (`RETAINED_ITEM_CAP` only bounds `conversation.items`); a very long session
  paged all the way back via repeated `loadOlder` grows `conversation.turns` unbounded. Accepted
  back in round 3 as out of scope (usage totals only need turn metadata, not full content) and
  never revisited — still true after B3b, not filed as an issue.

## Gates cheat-sheet (mobile-native / mobile shared, same as other phone lanes)
- `cd mobile-native && npx vitest run --root .. --config mobile-native/vitest.config.mts <path>`
  for a single file under `mobile/src`; plain `npx vitest run <path>` for files under
  `mobile-native/src`. `npm run test:shared` for the whole shared `mobile/src` suite.
- `npm run check` (tsc, `tsconfig.check.json`) — 3-4 pre-existing `react-test-renderer`
  module-not-found errors are normal (missing dependency, not a real gate failure); count them on
  `origin/main` before touching anything if unsure.
- Package/web gates (`npx biome ci ../../../appwire-client/typescript`, `npm run lint`, `npm run
  typecheck`, `npx vitest run <package-or-web-test>` from `cmd/evener-hub/frontend`) only apply when
  `appwire-client/typescript` or `cmd/evener-hub/frontend` files are touched (B3b touches neither).
- Falsification pattern used throughout: `git diff -- <prod files> > /tmp/x.diff && git checkout
  HEAD -- <prod files>` (or `origin/main`/an older commit mid-split), run the new tests, confirm
  they fail, `git apply /tmp/x.diff` to restore, confirm green, `git status --short` clean.
- `node_modules` in this worktree is a real directory (not a symlink) in `mobile-native/`; never
  ran `npm ci` this lane (nothing was missing).

Stopping here per the coordinator's instruction (near 500k tokens). Nothing else is owed on this
lane — #1919 and #1920 are open, pushed, gated, and falsified; #1885 is closed.

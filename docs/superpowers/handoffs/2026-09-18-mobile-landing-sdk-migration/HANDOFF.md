# Handoff: native iPhone landing (#1116) and the AppWire SDK migration — 2026-09-18 evening

Written by the coordinator agent ("Bot") for the next coordinator. Repo: prime-radiant-inc/evener.
Everything here was true at about 17:30 PDT on 2026-09-18 (PR state read 17:00-17:30). Re-check heads before acting; PRs move.

The ledger directory (gitignored, on Jesse's machine) is the primary record:
`/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/issue-1116-merge-review-1fc3f3/.superpowers/sdd/2026-09-12-mobile-landing-queue-cont/`
Referred to below as `$L`. `$L/progress.md` is the action ledger (2300+ dated lines). Every lane that
retired left a `*-state-note.md` there. `$L/reviews/<PR>-simplify.md` are the opus /simplify verdicts,
`$L/reviews/raw/<PR>-<HEAD9>.md` are mirrors of the RoboRev raw panels. `$L/bin/` has the two coordinator
scripts. `$L/BRIEF-COMMON.md` is the common implementer brief every lane brief points at.

## 1. The charge

Two standing assignments from Jesse:

1. Land native iPhone v1 (issue #1116; the issue body is the spec). TestFlight build 5 and device
   acceptance are parked by Jesse's 2026-09-16 focus ruling.
2. Migrate both frontends (web `cmd/evener-hub/frontend`, native `mobile-native` + `mobile/src`) onto
   the TypeScript SDK package `@evener/appwire-client` (`appwire-client/typescript/`), PR by PR, per
   `docs/superpowers/plans/2026-09-12-sdk-migration.md`. Direction: lift the web version into the
   package first, point the phone at it in a later PR. Only the phone-pointing halves need product
   rulings, and all nine pending rulings are now answered (section 7).

Focus is SDK rows only. A2 #1480 (transcript Seq fold) is parked, WIP-committed, do not touch.

## 2. Why this handoff, and what the wrap-up did

At ~16:05 PDT the Anthropic **weekly usage limit** hit ("resets Sep 24 at 8am America/Los_Angeles").
Six sonnet implementer lanes died mid-task. Section 6 lists each lane, what it was doing, and exactly
where its work sits. The coordinator session itself kept enough budget to write this memo.

Wrap-up actions taken (all ledgered in `$L/progress.md`):

- Pushed the D2 lane's unpushed restack of #1922 (`claude/sdk-d28-d2a-reconnect-recovery`) onto the
  new head of #1915. Own change lines verified identical to the pre-restack diff (only hunk offsets
  differ). Pushed with `--force-with-lease` against the old head `a2fd89ae6`. New head `7d5d68b09`.
  The `+`/`-` lines are byte-identical, so per the standing rule the reviewer is skipped for this
  refresh, but CI must go green at `7d5d68b09` before merge.
- Parked the D25d-1 round-4 lane's uncommitted failing-first test on a side branch
  `claude/sdk-d25d-1-r4-wip` (one commit, test only) so #1916's own CI stays meaningful. The
  production fix for that round-4 Medium is NOT written (section 6, D25d-1).
- Pushed the two coordination branches (`claude/issue-1116-merge-review-1fc3f3`,
  `claude/mobile-app-integration-6d4885`; docs-only commits) so nothing local is unpushed.
- Copied the watcher scripts and the status board HTML into `$L/bin/` and `$L/` respectively (the
  session scratchpad they lived in goes away with the session).

One uncommitted change is deliberately left in place: `sdk-d14-navigation-store` has a working-tree
deletion of one test in `mobile-native/src/connectionDisplay.test.ts` ("a hub change resets everReady,
even for a screen instance reused across it"). The D2 must-fix lane made it right before dying and I
could not tell whether it was intentional (the next test covers a similar case) or a slip. Decide,
then commit or revert it. Do not lose it silently: a coverage drop is worse than a red test.

## 3. Jesse's rules (apply all of them; Rule #1: any exception needs Jesse's explicit permission)

Merge gate, per PR:
- Current-head CI green (check-runs on the head SHA; CI builds the merge with main, not the head).
- RoboRev review body read clean **from the raw panel members** on host magic-kingdom
  (`$L/bin/rawreviews.sh <PR> <HEAD9>`), never from a watcher verdict. Members: codex/gpt-5.6-luna,
  codex/meta/muse-spark, pi/deepseek (often "No review output generated", 26 chars = empty),
  codex/glm-5.3-vision (usually 429s). Read `- **Severity**` bullets and `**Severity: low** —` lines.
  A "No Issues Found" section can sit beside a `## Medium` section in the same combined comment.
- /simplify run (one opus reviewer per PR or per settled stack; tiny PRs "by reading"). Must-fixes
  are pushed and re-reviewed; follow-up-only items go to a follow-up PR.
- Merge command, always:
  `SHA=$(gh api repos/prime-radiant-inc/evener/pulls/N --jq .head.sha); gh pr merge N --squash --admin --match-head-commit "$SHA"`
  Never pad a 9-char SHA to 40. `gh run list --commit` also needs the full SHA.
- **Lows-only review → merge, and put the Lows in a follow-up PR.** Never fold a Low into the PR when
  the review is Lows-only (Jesse: "lows in followups because they're much cheaper to review").
- **Any change past five review rounds is decomposed into smaller PRs.** (Fold #1808 got rounds 6 and
  7 as an explicit one-off exception; it has merged.)
- No verdict carry-over across a head change: a rebased head waits for new CI + a new review, EXCEPT a
  byte-identical refresh (own `+`/`-` lines identical after a rebase) skips the reviewer but still
  waits for CI.
- No draft PRs. **Every stacked PR is opened with base `main`** (a PR based on another branch gets no
  CI); the body says "Stacked on #N". After a predecessor squash-merges:
  `git rebase --onto origin/main <old-pred-head> <branch>`, verify own change lines identical,
  `git push --force-with-lease=<branch>:<old-head>` from the lane's normal worktree (never a detached
  temp checkout), then grep the tip for duplicated functions (git's 3-way merge can duplicate a whole
  function silently after a stacked merge; it happened on D15 #1630).
- Tiny stacked PRs: 80–150 non-test lines target, 400 = hard ceiling that needs the coordinator's
  explicit OK (and Jesse's if it is a product change).
- Seam refutations: a finding on a stacked PR that the next PR in the chain fixes is refuted by
  citing that PR, because they merge back to back.
- Measure before refuting or accepting (grep the oracle tests; reproduce the construct; a stale
  comment is not the contract). Tests already asserting a contract outrank a review finding.
- TDD with failing-first tests and falsification (revert only production files, watch fail, restore).
- Never rewrite an implementation or add backward compatibility without explicit permission. The
  package is in-repo-only: no compat shims for renamed fields.
- Never a "refresh to see the latest" affordance in the native app; it auto-refreshes.
- Never delete directories recursively with force. Never bare `git stash` (the stash is shared across
  worktrees); make WIP commits instead.
- Never run biome over `mobile/` or `mobile-native/`; never `npx biome` from the repo root (an
  unrelated biome 0.3.3 resolves and exits 0). Run `npm run lint` in `cmd/evener-hub/frontend`, and
  `npx biome ci ../../../appwire-client/typescript` from that directory.
- node_modules in worktrees: if it is a symlink never `npm ci`; if a real dir with a missing module,
  `npm ci` once.
- Never name a host global inside the package (ports only: sessionStorage, crypto, BroadcastChannel,
  setInterval, window, document all come in through the adapter).
- Wire-shaped tests use Go omitempty shapes (absent, never `[]` or `0`).
- Any change to what the appwire projector emits needs a `cmd/evener-tui` test case in the same PR.
- Go: use `$(go env GOROOT)/bin/gofmt`, never the PATH gofmt. Implementer gates include
  `go vet -tags evenerfuzz ./...` and `GOOS=windows go vet -tags evenerfuzz ./...`.
- Lean on CI, not local full suites (Jesse 2026-09-17): lanes run targeted tests + typecheck + lint.
- Every residual becomes a GitHub issue the same turn (cited in the PR comment and the ledger).
- Ask Jesse product rulings one at a time, each with tradeoffs and a recommendation. Estimate in lines
  of code, never wall-clock. Honest judgment, no sycophancy.
- Sub-agent cost: implementers are sonnet with careful briefs; /simplify reviews are opus; bounded
  mechanical tasks (board refresh) are haiku. Lanes retire at a round boundary past ~400–600k tokens
  with a state note in `$L`. One lane per worktree; check the worktree table (section 10) before
  every spawn.
- Ledger every action with `$(date '+%Y-%m-%d %H:%M %Z')` appended to `$L/progress.md`. GitHub
  polling only via REST `gh api` (never `gh pr view` loops). Republish the status board at every
  merge batch. Coordinator tooling stays out of the repo (in `$L`).
- zsh gotchas: `${var}:refs/...`; no backticks or `<A>` inside double-quoted printf/echo (use
  `printf -- '%s'` with single quotes); `set -- $p` does not word-split in a zsh for loop.

## 4. Mechanics

- Watcher: `bash $L/bin/watch-pr.sh <PR> <HEAD9> > <log> 2>&1` (REST poller; exits "HEAD MOVED" when
  superseded; run in the background with a 10-minute timeout and re-arm). It prints a verdict that is
  NOT the merge gate; read the panel.
- Raw panels: `$L/bin/rawreviews.sh <PR> <HEAD9>` (ssh to magic-kingdom). Mirror the output to
  `$L/reviews/raw/<PR>-<HEAD9>.md`.
- Post-merge main CI: `gh run list --branch main --workflow CI --commit <merge_commit_sha> --limit 1 --json status,conclusion`.
- Status board: https://claude.ai/artifact/U17469rqTGB3gFpEDsmG2o (v19, 55 merges). Source copy at
  `$L/sdk-queue-board.html`. Refresh: a haiku agent edits the rows from the ledger, then
  `Artifact publish` with `url` set to that URL.
- Memory: `ledger-memory save` wrapper (see MEMORY.md header). The `mobile-landing-queue-status`
  memory is refreshed with this handoff.
- Flake attribution: before blaming a merge, check the failing test's module is in the merge's
  touched paths. Known families: #1879 (retention eligibility races the async session namer; nine
  occurrences on 2026-09-18; fix #1921 is test-only and open) and #1394 (race-modules/agent job hits
  the 10-minute go test cap; not a hang).

## 5. Open PRs owned by this queue (state at ~17:30 PDT)

Legend: CI = check-run conclusions at the head SHA; Panel = what the latest RoboRev combined comment
at that head contains (RE-READ THE RAW PANEL before acting; these are section headers, not verdicts).

| PR | Branch | Head | CI | Panel | Next action |
|---|---|---|---|---|---|
| #1902 D6 p6b | claude/sdk-d6-p6b-native-offline-discard | 90812476e | 15 green | Medium x3 + Low (nativePreferences.ts probe/discard API unconsumed → p6c seam; live-model nudge can discard a valid replacement; new provider behaviour untested) | Read raw panel. Seam item refuted by #1904. The other two are p6b-own: one more round via lane 10, then merge; then lane 10 restacks p6c. |
| #1904 D6 p6c | claude/sdk-d6-p6c-screen-offline-recovery | 3c6436d6f | 15 green | clean at this head; owes two items from its own earlier round (discard disabled while `domain.loading` in keybindingOfflineRecovery.ts:45-47; stale `offlineStorageUnavailable` after a live recovery) | After p6b merges: restack, apply the two items, one round, merge. |
| #1792 D6 p7 | claude/sdk-d6-p7-draft-generation-stamp | 41fc2f2e8 | 15 green | clean | Restack after p6c; byte-identical refresh → CI only → merge. |
| #1841 D6 p8 | claude/sdk-d6-p8-settings-hub-generation | c2afb3836 | 15 green | clean at head (earlier marker Medium was p6b's, fixed by branding markers with a unique symbol in #1902 r3) | Restack after p7; CI; merge. |
| #1844 D6 p9 | claude/sdk-d6-p9-checkpointed-draft-editor | a50ac1a95 | none (mergeable_state dirty: conflicts with main until restacked) | clean at head | Restack after p8 (expect conflicts in keybindingsStore.ts); CI; merge. Carries option A (persistCheckpointedDraft with restoreDraft callback). |
| #1845 D6 p10 | claude/sdk-d6-p10-transcript-hub-defaults | 737b7cebd | none (dirty, same reason) | round 3 head never reviewed | Restack after p9; needs CI + a fresh panel read; merge. Then Stack A (D6 pieces 11–13) may start. |
| #1890 #1810 S2 | claude/plugins-path-scrub-s2-remove-reorder | bda74b984 | 15 green | clean at r5 head | Merge (simplify done for the stack: follow-up only). r5 was the LAST allowed round; a real finding at r6 means decomposition. |
| #1897 #1810 S3 | claude/plugins-path-scrub-s3-migration-close | d3c099449 | 15 green | Medium x3 in the package marketplaces store (reconciliation bypasses the revision/generation fence; leaves error/loading stale; `applied.marketplaces` not validated as an array) | Stacked on #1890. Read the panel: the fence item matches the "unfenced reconciliation set()" follow-up in `1810-s2s3-state-note.md`. Decide fix-in-round vs follow-up (it is not a Low). Then rebase onto main after #1890 merges, CI, merge. |
| #1906 #1806 S2 | claude/1806-s2-ask-boundary-restore | c4761e308 | RED: `tests` job (= #1879 flake, `TestRetirementStartReplayExecutesOnce`, occurrence 9) | Medium x2 + Low, same live/restore class (terminal-state selection not centralized; `roundEntryResolvesAskBoundary` misreads a non-carrier TurnFailure; live clear passes empty textEvidence) | Round 4 was sent to the S2/S3 lane, which died. Resume in `hub-askpending-notify`. This is round 4 of 5. |
| #1907 #1806 S3 | claude/1806-s3-ask-boundary-carrier-defer | fd069ec96 | 15 green | clean | Merge-ready behind #1906 (stacked). Simplify must-fix already applied. |
| #1915 D28 D2 | claude/sdk-d28-d2-flap-banner | b689acb3b | 15 green | High + Medium + Low (**this panel is at b689acb3b**: stores never notified of reconnection / initial reads before ready; deferred confirmation callbacks capture stale readiness) | The High/first Medium are #1922's content (seam). The simplify must-fixes are in b689acb3b. Read the raw panel; refute seams by citing #1922; anything D2-own goes to a round-4 lane in `sdk-d14-navigation-store`. |
| #1922 D28 D2a | claude/sdk-d28-d2a-reconnect-recovery | 7d5d68b09 (pushed by the wrap-up) | in progress | panel is for the old head a2fd89ae6 (stacked view, 2 D2a-own Mediums fixed in r2) | Byte-identical restack: wait for CI at 7d5d68b09. Simplify must-fix ("a test that proves nothing") status unknown: check `$L/reviews/1922-simplify.md` against the branch. Merges after #1915. Closes #1914. |
| #1916 D25d-1a | claude/sdk-d25d-1-native-outbox-storage | 8d7732de2 | RED: `tests` job (cause not read; check the log, likely the #1879 family) | clean at r3 head; the round-4 Medium came in on #1917's stacked panel (sequence allocation read-then-write; no unique (target_ref, intent_sequence) index) | Round 4 lane died. Its failing-first test is on `claude/sdk-d25d-1-r4-wip`. Resume in `sdk-d25c-pending-turns`: write the fix (allocate + persist in one statement or BEGIN IMMEDIATE, plus the unique index), the two simplify must-fixes (COLUMNS constant; unreachable state default), refute composerText (oracle omits it). Round 4 of 5. |
| #1917 D25d-1b | claude/sdk-d25d-1b-native-outbox-storage-read | ee0013dc8 | 15 green | stacked panel = 1a items above + Low (implements clause) | Simplify must-fix: add `implements MutationOutboxStorage`. Rebase onto #1916 r4; merge after it. |
| #1919 D18 B3b | b3b-turns-wire-cursor | 6c0271680 | 15 green | Medium + Low, one class (rehydrate drops the accumulated fragment instead of merging fresh-wins-with-fallback) | Round 3 sent: lift `mergeTurnHistory(older, newer)` out of the package's `mergeOlderItemPage` and use it on both paths. Lane died. Resume in `sdk-d17-activity-panel`. Round 3 of 5. |
| #1920 D18 B3a | b3a-session-usage-totals | d40bf549b | 15 green | Medium (was B3b content, refuted as seam) | Merge after #1919 (file-disjoint, review-order stack). Simplify follow-up only. |
| #1921 #1879 fix | claude/1879-retirement-flake | 0a08b4d16 | 15 green | clean at r3 head | Test-only. /simplify by reading, then merge. Highest leverage: it removes most of the red CI noise. `TestRetirementTreeSettleDrainsPendingRootAttention` is unexplained (three next steps in `$L/1879-lane-state-note.md`). |
| #1931 #1862 follow-ups | claude/1862-stack-followups | 4d2daddc8 | RED: `web` job | High (`askDock.testFixture.ts` imports `@evener/appwire-client/testing/*` but `.testFixture.ts` is not a test-support name for `check-package-tests` → rename to an allowed test-support name) + Low | Real finding, fix it (rename + imports), one round, merge. Lane died right after committing; worktree `sdk-followups-1862` is clean and pushed. |
| #1737 D23c-2b C | claude/c2b-c-native-rows-are-a-projection | d4f256200 | 15 green | Medium (`truncateItem` truncates question option labels used for answer composition, project.ts:1072-1076 / questionAnswers.ts:14-18 → keep canonical refs separate) + Low (#1925 duplicates; stale comment) | Round 4 for C. The C/E/D lane retired with `$L/ced-restack-state-note.md` (read its "restack mechanics" section before rebasing anything held). Fresh lane in `sdk-d21-native-model`. |
| #1738 D23c-2b E | claude/c2b-e-question-sheet-bounded-identity | ffb4cbae1 | 15 green | Medium x2 (persisted-draft incompatibility after upgrade in questionAnswers.ts:78-88 / draftRepository.ts:93-98; inconsistent live warning rendering) + Low | Stacked on C. Ruling #1842 says no draft-signature migration: measure whether the first Medium is that class before refuting. |
| #1740 D23c-2b D | claude/c2b-d-native-page-history-cutover | 55c6eab8b | 15 green | Medium (transient warnings persist after `turn/completed`, reducer.ts:1354 / conversation.ts:1902) + Low; Critical/High sections read "None" | Stacked on E. Fixed a real package bug (mapTurn/settleFirstMatchingTurn array identity on a no-op fold; #1930 filed). |
| #1480 A2 | claude/transcript-a2-seq | cfc633b5b | 15 green | Medium + Low | PARKED by Jesse. Do not work it. |
| #1580 D23c-2b oracle | claude/sdk-d23c2b-rows-projection | 53bb2b6ca | 15 green | Medium + Low | Reference only (the split's oracle). #1549 (D6 oracle) was closed 2026-09-18 for the same confusion; consider closing #1580 once C/E/D land. |

Merged today (2026-09-18) through 15:37 PDT: 16, total 55 since the morning of 2026-09-17; main was
green after each. Last merges: #1900 p6a (ead2caba7), #1732 (efbd6b0f8), #1895 (0a53ef91f),
#1808 fold (8d8ed0d27), #1894 (4b9b4bafb), #1905 (07eb8edf7), #1893 (0a10857d1), #1913, #1912, #1889.

## 6. The lanes that died at the weekly limit, and lane 10

Each row: worktree (absolute path under `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/`),
what it was doing, and how to resume. Resume protocol for any lane: `git status --short`,
`git log --oneline -5`, check for a rebase or cherry-pick in progress
(`ls "$(git rev-parse --git-dir)/rebase-merge"`, `CHERRY_PICK_HEAD`), report, then continue.

1. **D2 must-fix lane** — `sdk-d14-navigation-store`, branch `claude/sdk-d28-d2a-reconnect-recovery`.
   Had applied the #1915 simplify must-fixes (pushed as b689acb3b on `claude/sdk-d28-d2-flap-banner`)
   and restacked #1922 onto it (pushed by the wrap-up as 7d5d68b09). Its last words: "All green.
   Let's also run `make lint-package-imports` per the brief". Left the uncommitted test deletion
   described in section 2. Remaining: decide that deletion; confirm the #1922 simplify must-fix
   landed; answer #1915's panel at b689acb3b; read `$L/d28-lane2-state-note.md` for the
   whenReady/act/confirm gating pattern.
2. **B3b round-3 lane** — `sdk-d17-activity-panel`, branch `b3b-turns-wire-cursor` @ 6c0271680,
   clean and pushed. Task: lift `mergeTurnHistory` out of `mergeOlderItemPage` (package
   `state/...`), use it on both the loadOlder and rehydrate paths; per-state table in
   `mobile/src/state/conversation.test.ts`. Background: `$L/b3-lane-state-note.md`,
   `$L/reviews/1919-simplify.md`.
3. **#1862 follow-ups lane** — `sdk-followups-1862`, branch `claude/1862-stack-followups` @ 4d2daddc8
   (#1931), clean and pushed. It committed just before dying. Remaining: the High rename
   (`askDock.testFixture.ts`), the Low, CI green, one panel read. The 20-item source list is in
   `$L/1862-split-state-note.md`.
4. **D25d-1 round-4 lane** — `sdk-d25c-pending-turns`, branch `claude/sdk-d25d-1-native-outbox-storage`
   @ 8d7732de2 (clean; the r4 test is on `claude/sdk-d25d-1-r4-wip`). Task in section 5 (#1916 row).
   Background: `$L/d25d-lane-state-note.md` (insertNew vs replace mapping; enqueue is no longer
   idempotent on a repeated id, which D25d-2 must know), `$L/reviews/1916-simplify.md`,
   `$L/reviews/1917-simplify.md`, `$L/d25d-design-note.md` (the 4-PR split Jesse approved).
5. **C/E/D lane** — `sdk-d21-native-model`, branch `claude/c2b-d-native-page-history-cutover` @
   55c6eab8b, clean and pushed. It DID write `$L/ced-restack-state-note.md` before dying. Remaining:
   round 4 on #1737/#1738/#1740 (section 5 rows).
6. **#1806 S2/S3 lane** — `hub-askpending-notify`, branch `claude/1806-s3-ask-boundary-carrier-defer`
   @ fd069ec96, clean and pushed. Was starting #1906 round 4 (section 5 row). Background:
   `$L/1806-split-state-note.md` (the `steeringAnswersAsk(source, origin, textEvidence)` predicate,
   the `onRefuse` seam, and the REFUTED askedThisRound counter fix with its pinning test).

**Lane 10 (D6 chain lander)** — `sdk-d6-split`, branch `claude/sdk-d6-p6b-native-offline-discard`
@ 90812476e, clean. Parked, not dead: it was told "wait for the coordinator's go for p6c". Its job is
the serial rebase/merge of p6b → p6c → p7 → p8 → p9 → p10 (byte-identical refresh per piece, merge on
CI green, one review round where a piece owes items). Chain background: `$L/d6-lane9-state-note.md`,
`$L/d6-split-plan.md`, `$L/d6-pieces-10-13-design-note.md`.

Also retired earlier today with clean worktrees and state notes (no action unless their PRs need a
round): `hub-broadcast-after-apply` (#1810 S2/S3, `$L/1810-s2s3-state-note.md`),
`threads-ready-guard` (#1879, `$L/1879-lane-state-note.md`).

## 7. Rulings recorded today (all 2026-09-18, all saved as `ruling-*` memories)

1. #1823: no askPending fallback.
2. #1759: phone decoders ignore unknown keys (already implemented by #1869).
3. A6: a superseded settings write resolves with the hub's current value (matches the web).
4. D25d: the phone adopts durable pending-turn rows; 4-PR split approved (D25d-1 outbox storage,
   D25d-2 dispatcher wiring, D25d-3 rows, D25d-4 recovery).
5. #1842: no draft-signature migration.
6. B3: the phone adopts summed session usage.
7. D28: both halves (D1 ConnectionProvider, merged as #1888; D2 screens survive a flap, #1915/#1922).
8. D17': withdrawn.
9. #1810: keep RemoveMarketplace's save-before-delete; the applied-with-litter outcome is typed all
   the way to the CLI (S2 #1890).
Process: fold #1808 got two extra rounds; #1549 closed as oracle-only.

## 8. Follow-up PRs gated on merges (Lows and simplify follow-ups, one PR per stack)

- #1806 stack: S1 Low (delivery path bypasses the fail-closed journal check) + simplify list (8) in
  `$L/1806-split-state-note.md`.
- #1810 stack: S1 simplify (8), S2 comment Low, orphan-clone gc = #1923, the client store's unfenced
  reconciliation `set()`; lists in `$L/1810-split-state-note.md` and `$L/1810-s2s3-state-note.md`.
- B3: 5 (B3b) + 4 (B3a) items in `$L/reviews/1919-simplify.md`, `$L/reviews/1920-simplify.md`.
- D6: #1918 (three Lows) + fold/p6a/p6b/p6c simplify follow-ups in `$L/reviews/1808-simplify.md`,
  `1900-`, `1902-`, `1904-simplify.md`.
- D2: 7 (#1915) + 8 (#1922) items in `$L/reviews/1915-simplify.md`, `1922-simplify.md` (one
  `useStoreConnection` hook; store-level connectionChanged for the hub overview; the became-ready edge
  into the package).
- D25d: 6 + 6 items in `$L/reviews/1916-simplify.md`, `1917-simplify.md`; issues #1927, #1928, #1929.
- #1862: in flight as #1931.
- C/E/D: #1925 (row-identity helper duplication), #1930 (package array identity bug, fixed in #1740).

## 9. Not started

- **Stack A**: D6 pieces 11–13 as tiny PRs (A1 publish surface ~25, A2 localStore ~110, A3
  crossTabSync ~150, A4 transitions ~45, A5 hubHalf ~150). Base is p10 #1845; DO NOT START until
  the D6 chain drains. Plan and oracles: `$L/tiny-stacks-plan.md`.
- **D25d-2/3/4** after #1916/#1917 merge (`$L/d25d-design-note.md`; note enqueue non-idempotence).
- Plan doc fix: fill the PR column for D14-2 (#1584), D14-3' (#1609), D28 web lift (#1623, #1667).
- #1116 deliverables still parked: TestFlight build 5, device acceptance (memory `testflight-build-4-live`).

## 10. Worktree assignment table (one lane per worktree)

Free for a fresh lane once their PRs land: `sdk-d6-split` (lane 10 parked there),
`sdk-d21-native-model`, `sdk-d25c-pending-turns`, `sdk-d14-navigation-store`, `sdk-d17-activity-panel`,
`hub-askpending-notify`, `hub-broadcast-after-apply`, `threads-ready-guard`, `sdk-followups-1862`.
Coordinator worktree: `issue-1116-merge-review-1fc3f3` (never write code there). All other
`.claude/worktrees/*` entries are finished lanes from earlier phases; all their branches match origin.
Two stale oddities: `evener-dev-bounded-list` has two dirty files from the closed #1289 lineage
(leave them); `sdk-d6-transcript-display` holds the closed #1549 oracle at dfde92160 (keep as the
D6 test reference).

## 11. Open questions for Jesse (one at a time)

1. Wait for the weekly reset (Sep 24, 8am PT) or change plan/model to keep the six lanes moving now?
   Recommendation: if lanes can run, the order is #1921 (kills the flake noise) → #1931 (one rename)
   → #1890/#1897 → #1906/#1907 → lane 10's chain → D25d-1 r4 → B3b r3 → D2 → C/E/D.
2. #1580 (oracle for the D23c-2b split) is still open and confusing other agents like #1549 was:
   close it now, or when C/E/D land? Recommendation: close now with the branch kept.

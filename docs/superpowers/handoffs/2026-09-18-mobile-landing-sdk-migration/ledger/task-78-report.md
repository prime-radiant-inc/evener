# Task 78 — issue #1172 (oversized activity entry paging)

**Status:** DONE_WITH_CONCERNS
**Commit:** 88755c3d6 on `claude/activity-oversized-entry-1172` (worktree `.claude/worktrees/activity-oversized-entry`, off daaaea797). Not pushed.
**Lines changed:** agent/jobs_activity.go +26/-1, agent/jobs_activity_past_test.go +75.

## Fix
`trimActivityTrailingEntry` now mints `ResumeIndex: i+1` (instead of `i`) and appends a
`Branch.Error` naming the entry when `i == 0`. Trimming runs tail-first, so index 0 means the
dropped entry was the only one that session's page still held — nothing competed for the budget
it blew, and re-pointing at it reproduces the page forever. New helper `activityEntryRef` names
the entry by job/delegate ID. `ResumeIndex`/`JobsEpoch`/`DelegatesEpoch`/`Revision` fencing
untouched; no `After` cursor from #1123; no wire-shape change.

No existing test asserted the re-point behavior (`ResumeIndex` assertions in tests cover only the
work-budget path at jobs_activity_past_test.go:274); nothing needed changing.

## RED / GREEN
Test: `TestLoadSessionJobActivityTree_SizeTrimAdvancesPastEntryLargerThanAPage`
(agent/jobs_activity_past_test.go) — one 4.25MB shell job then an ordinary one, walked through
`LoadSessionJobActivityTree` with a 4-page cap.

- RED (fix reverted via file copy-aside, test kept): `--- FAIL: ... page 2 re-minted page 1's continuation "eyJ2IjoxLCJyb290Ijoib3ZlcnNpemVkZW50cnlyb290Iiwic2Vzc2lvbiI6Im92ZXJzaXplZGVudHJ5cm9vdCIsInBhdGgiOm51bGx9" -- the client can never advance past the oversized entry`
- GREEN: `--- PASS: TestLoadSessionJobActivityTree_SizeTrimAdvancesPastEntryLargerThanAPage (0.02s)`

## Gates (agent module)
- `gofmt -l` on both touched files: clean
- `go vet ./...`: 0
- `go test -count=1 ./...`: zero non-ok lines
- `go test -race -count=3 -run 'TestLoadSessionJobActivityTree_SizeTrim|TrimActivity'`: ok
- `$(go env GOPATH)/bin/golangci-lint run ./...`: `0 issues.`
- AGENTS.md / make/testing.mk name no deadline audit for the agent module (grep: no hits).

## Concern — separate, worse defect found in the same function (needs its own issue)
Every size-trim mint uses the index into the *already-resumed* page, not the session's absolute
entry index, so a size trim on page 2+ mints a continuation pointing back into the page it just
returned. Probe on unmodified main (60 shell jobs x 250KB, no oversized entry): page 1 = job_00..15,
page 2 = job_16..31, and pages 3-10 repeat job_16..31 with an identical token — the same
never-advancing loop #1172 describes, reachable with entirely ordinary jobs. My fix does not
address it (verified: the probe still stalls at page 2 with the fix applied) and cannot without
threading the page's resume offset (`-startDepth` identifies the target session) into
`trimActivityTrailingEntry`, which changes its signature and four existing direct-call sites —
outside this task's ruling. Probe was scratch-only, not committed. Recommend filing it.

# Task 79 — issue #1177 (page-relative ResumeIndex), same branch

**Status:** DONE
**Commit:** fc516b124 on top of 88755c3d6 (not amended, not pushed).
**Lines changed:** agent/jobs_activity.go +44/-12, agent/jobs_activity_past_test.go +69,
agent/jobs_activity_branches_test.go +59/-6 (6 direct call-site updates, one new unit test).

## Fix
New `activityTrimResume{depth, index}` carries the page's starting position from
`projectBoundedActivityTree` (`depth: -startDepth`, `index: resumeIndex`) through
`trimActivityTreeToFit` into `trimActivityTrailingEntry`; `offsetAt(path)` applies the offset only
where `len(path) == depth` — the one session the page resumed. Every mint is now
`offset + i`, and the #1172 index-0 skip advances past the entry's absolute position.
Six direct call sites (2 in `trimActivityTreeToFit` tests, 4 in `trimActivityTrailingEntry` tests)
take `activityTrimResume{}`; no assertion or expected value changed.

## RED / GREEN
- `TestLoadSessionJobActivityTree_SizeTrimResumesAtAbsoluteIndexOnLaterPages` (60 shell jobs x 250KB,
  walked with a jobCount page cap, token uniqueness + job_00..job_59 in order).
  RED: `page 2 minted page 1's continuation again ("...aWR4IjoxNn0=") -- a size trim on a resumed page reported its position within that page instead of the session's own entry numbering`
  GREEN: `--- PASS: TestLoadSessionJobActivityTree_SizeTrimResumesAtAbsoluteIndexOnLaterPages (0.38s)`
- `TestTrimActivityTrailingEntry_MintsResumedSessionsOwnIndex` (nested depth, no 4MB fixture — pins
  that the offset applies at the resumed session's depth and nowhere else).
  RED (offset removed by copy-aside): `child ResumeIndex = 1, want 6 (the child's own entry after j1, not this page's index 1)`
  GREEN: `--- PASS: TestTrimActivityTrailingEntry_MintsResumedSessionsOwnIndex (0.00s)`
- Task 78's test still passes, and is still RED without the index-0 skip (verified by copy-aside with
  the offset threading in place): `page 2 re-minted page 1's continuation "eyJ2IjoxLCJyb290Ijoib3ZlcnNpemVkZW50cnlyb290..." -- the client can never advance past the oversized entry`

## Gates (agent module)
gofmt clean on all three touched files; `go vet ./...` 0; `go test -count=1 ./...` zero non-ok lines;
`go test -race -count=3` on the trim set ok (24/24 PASS); `golangci-lint run --concurrency=2 ./...`
under GOGC=50: `0 issues.`

## Concerns
None outstanding. Both loops in `trimActivityTrailingEntry` are now covered; the work-budget mint
path (`markActivitySessionTruncated`) was already absolute and is untouched.

# Task 80 — PR #1179 RoboRev round 1 (High: data-loss regression in 88755c3d6)

**Status:** DONE
**Commit:** f580b66b2 on top of fc516b124 (no amend, not pushed).
**Lines changed:** agent/jobs_activity.go +22/-6, agent/jobs_activity_past_test.go +180,
agent/jobs_activity_branches_test.go +12/-2.

## Fix
The skip branch is now `if i == 0 && resume.targets(path)` — new `activityTrimResume.targets`
(`len(path) == r.depth`), which `offsetAt` is also expressed in terms of. `i == 0` in a session the
page is not built around only means this page ran out of room; the continuation minted there
re-targets that session and the next page carries none of the ancestors' entries, so the entry is
delivered rather than lost. An entry that genuinely fits no page reaches index 0 again as the
target, one page later, and is skipped there.

## RED / GREEN
- `TestLoadSessionJobActivityTree_SizeTrimKeepsNestedEntryOverflowedByASibling` (reviewer's fixture:
  3.9MB root job, delegate child holding one 0.4MB job; the walk follows continuations minted
  anywhere in the tree, not just the root's).
  RED at fc516b124: `branch errors ["job \"job_child\" is too large to render in one response and was skipped"] across 2 pages, want none -- the child's job is renderable on a page filtered to it; the root's sibling is what did not fit`
  GREEN: `--- PASS: TestLoadSessionJobActivityTree_SizeTrimKeepsNestedEntryOverflowedByASibling (0.03s)`
- `TestLoadSessionJobActivityTree_SizeTrimSkipsNestedEntryLargerThanAPage` (added so the gating
  cannot silently trade the loop back in: a 4.25MB job nested one hop down must still be skipped and
  the walk must terminate). Not RED at fc516b124 — it is a termination guard, and it is not vacuous:
  with the skip branch deleted it fails `page 2 re-minted the continuation it was loaded with (...)`.
  GREEN: `--- PASS: TestLoadSessionJobActivityTree_SizeTrimSkipsNestedEntryLargerThanAPage (0.05s)`
- Task 78's and 79's three tests still pass unchanged.

## Existing test whose expectation changed
`TestTrimActivityTrailingEntry_MintsResumedSessionsOwnIndex` (added by fc516b124, this PR's own, not
a main test). It asserted `root ResumeIndex = 1` after the root's sole delegate entry was trimmed
while the page's target was the child one hop deeper. That encoded the buggy skip: the root is not
the target, so index 0 there is not evidence the entry is too large, and advancing past it dropped
it. It now asserts `root ResumeIndex = 0` (the root's own top-relative position) plus
`session.Branch.Error == ""`. The child-side assertion (`ResumeIndex = 6`) is unchanged.

## Gates (agent module)
gofmt clean on all three files; `go vet ./...` 0; `go test -count=1 ./...` zero non-ok lines;
`-race -count=3` on the trim set ok (68s); `GOGC=50 golangci-lint run --concurrency=2 ./...`: `0 issues.`

## Concerns
None. Both skip conditions now have a paging test on each side of them (skipped when the entry is
the target's own and unrenderable; delivered when the overage came from a sibling).

# Task 87 — PR #1179 RoboRev round 2 (1 Medium, 1 Low)

**Status:** DONE. Commits: `17217d525` (Medium), `2c94d1988` (Low). No amend, no push, merge 7df64fb75 untouched.

## Medium — filtered snapshots dropped the fold generations (agent/jobs_activity.go +9)
Verified. `activityFilterSnapshotToDelegate` rebuilt the ancestor snapshot without `JobsEpoch` /
`DelegatesEpoch`, so on a resumed page `projectBoundedActivityTree` handed the trim a zero
`DelegatesEpoch` and every continuation minted there encoded zero. The next request read the real,
nonzero generation and rejected a fresh token. Fix: carry both fields through the filter (2 fields
+ a doc comment on the function).
- RED: `TestLoadSessionJobActivityTree_NestedContinuationSurvivesNonzeroFoldEpochs` —
  `page 3: activity continuation is stale: the underlying journal changed; restart pagination without a continuation -- a continuation minted on a resumed page must carry the generations the next request checks`
- GREEN: `--- PASS: ...NestedContinuationSurvivesNonzeroFoldEpochs (0.28s)`
- The fixture warms the fold caches, then restamps the root's delegates.jsonl and the child's
  jobs.jsonl (same size, new mtime = a rewrite, which is what bumps a generation), and gives the
  child meta the root ID so both sessions read the root's shared delegate journal.

**Pre-existing on main: yes.** `git show daaaea797:agent/jobs_activity.go` has the filter
byte-identical (no epoch fields) and the same `trimActivityTreeToFit(tree, rootID,
snapshot.DelegatesEpoch, ...)` call. The depth-truncation mint (`markActivityDelegateTruncated`,
main line 1084) reads the same page-root `DelegatesEpoch` and was equally affected; it is fixed by
the same change. The work-budget mint was never affected (it reads each session's own snapshot) —
probed and confirmed clean at this head.

## Low — the resumed skip had no assertion of its own (tests only, +115/-3)
- `TestTrimActivityTrailingEntry_MintsResumedSessionsOwnIndex` now decodes the child's continuation
  after the skip and asserts `ResumeIndex == 6` plus a branch error naming j1.
- New `TestLoadSessionJobActivityTree_SizeTrimSkipsOversizedEntryOnAResumedPage`: 20 filler jobs fill
  page 1, the oversized entry lands on page 2 (a resume), a tail job sits behind it; the walk must
  deliver every job but the oversized one exactly once, in order, with a strictly advancing token.
- Both RED against a copy with the offset dropped from the skip branch (`resumeIndex = i + 1`):
  `child ResumeIndex after the skip = 1, want 6 (past j1's own entry 5, not past this page's index 0)`
  and `page 5 minted page 2's continuation again ("...aWR4IjoyMH0=") -- the skip rewound to this page's own index instead of the entry's`
  GREEN for both after restoring.

## Gates (agent module)
gofmt clean; `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...`
all 0; `go test -count=1 ./...` zero non-ok lines; `-race -count=3` on the trim set ok (204s under
load); `GOGC=50 golangci-lint run --concurrency=2 ./...`: `0 issues.`

## Concerns
One observation, not fixed and not a regression from this PR: every trim mint uses the PAGE ROOT's
`DelegatesEpoch` for all sessions, while the staleness check compares it against the CONTINUATION
TARGET's own. Those agree only because a child's meta names the root, so both read the same shared
delegates.jsonl. A child whose meta lacks `JobTreeRootSessionID` reads its own (usually absent)
journal at generation 0, and a nested continuation minted while the root's generation is nonzero
would be rejected as stale. My first draft of the epoch fixture hit exactly that before I set the
child's root ID. Worth its own issue if that meta field is ever optional in practice.

# Task 93 — PR #1179 RoboRev round 3 (2 Mediums)

**Status:** DONE. `cfab29d64` (finding 1, Go), `f29440fc4` (finding 2, web). No amend, no push, merge 77e8533ce untouched.

## 1. `i == 0` does not prove the entry is the cause — CONFIRMED, fixed (jobs_activity.go +85/-30)
Measured first. Bare root envelope: **222 bytes**. Realistic worst case — a 33-hop ancestor chain
(`activityMaxNewDepth+1`), 8KB label per session, 4KB task/description/mandate per delegate, a 16KB
continuation token: **722,084 bytes = 17.2%** of the 4,194,304-byte limit. So ordinary metadata
cannot approach it — but three envelope inputs are unbounded, which is why I did not refute:
`Label` is `SessionDisplayName`, i.e. the session's `OriginalPrompt` verbatim when it has no name
(a pasted file); delegate `Task`/`Mandate`/`Description`; and `Branch.Error`, which appends one
message per job record of an unsupported type. A 4MB pasted prompt reproduces it.
Fix: `trimActivityTrailingEntry` now reports the entry it dropped (`activityTrimmedEntry`) and mints
at the entry's own position; `trimActivityTreeToFit` re-encodes the tree without it and applies the
skip only when that brought the page under the limit. With nothing left to drop and the page still
over, `markActivityEnvelopeTooLarge` reports the response's own size and withdraws the
continuation, which could only produce that same page.
- RED: `TestLoadSessionJobActivityTree_OversizedEnvelopeIsNotBlamedOnAnEntry` —
  `branch error "job \"job_00\" is too large to render in one response and was skipped" blames a small job for an oversized response envelope`
- GREEN: that test passes; the five earlier paging tests still pass.
- The unit test's skip assertions now call `skipActivityTrimmedEntry` directly (same numbers: index 6,
  error names j1) and additionally pin `unrepresentable` true for the target, false for the root.
  Four call sites take `_, ok :=`; no assertion or expected value changed.

## 2. Empty root with a continuation is unreachable in the web UI — CONFIRMED, fixed (ActivityPanel +45/-4)
`ActivityPanelBody` rendered "No retained activity yet" for any empty root, and `collectContinuations`
needs a last row, so neither the branch error nor a control appeared. Now an empty page that carries
a continuation or an error gets its own state: the error (or a refused page's failure) as the hint,
plus a control that follows the continuation.
- RED: `ActivityPanel.test.tsx` "an empty page that carries a continuation stays readable and loadable" —
  `Unable to find an element with the text: /too large to render in one response/i`
- GREEN: passes; the click issues `evener/jobs/list {ref, continuation: "tok_after_skip"}` and the next page's row renders.
- Go side unchanged for this finding; the empty-root payload already carried everything the UI needed.

## Gates
Go: gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` all 0;
`go test -count=1 ./...` zero non-ok lines; `-race -count=3` on the trim set ok (78s);
`GOGC=50 golangci-lint run --concurrency=2 ./...`: `0 issues.`
Web: `npx biome check --write` on the two touched files; `make test-web` PASS (typecheck/test/lint);
`make test-web-browser` PASS all five guards (layoutguard owns two activity cases).
`scripts/web/web-preflight.sh` installed this worktree's frontend deps first.

## Concerns
When the envelope alone exceeds the limit the response still goes out over 4MB — nothing can be
dropped to shrink it — but it now says so instead of blaming an entry. Capping `Label` (and the
per-record `Branch.Error` accumulation) at projection time is the real fix and belongs in its own
issue.

# Task 96 — PR #1179 RoboRev round 4 (1 Medium, 2 Lows)

**Status:** DONE. `c884cb282` (Medium), `c53ff9b82` (Low 1), `85386f18b` (Low 2). No amend, no push, merge d791c6d82 untouched.

1. **Skip diagnostic measured after the size check** (jobs_activity.go +10/-5, branches_test +42).
   Fix: apply the skip (error + advanced token), recompute, marshal, and keep it only if the page
   still fits; otherwise restore the branch and carry on trimming.
   RED `TestTrimActivityTreeToFit_SkipDiagnosticStaysInsideTheLimit`: `trimmed response is 4194389 bytes, 85 over the 4194304-byte limit -- a skip has to be measured with the diagnostic it writes`. GREEN: passes.
2. **Stale counts after the envelope error** (jobs_activity.go +4, branches_test +28, past_test +3).
   Fix: recompute after `markActivityEnvelopeTooLarge`. RED `TestTrimActivityTreeToFit_EnvelopeTooLargeLeavesConsistentCounts`:
   `counts = {Active:0 Failed:0 Completed:0 Complete:true}, want Complete false alongside branch error "activity response is 4210867 bytes..."`. GREEN: passes; the integration envelope test now also fails any page that reports an error and calls itself complete.
3. **Untested empty-page outcome** (ActivityPanel.test.tsx +40). New case: entries empty, `branch.error`
   set, no continuation → the error renders, "No retained activity yet" does not, and no Load more
   control appears. Passes as written (the branch was already implemented); proved non-vacuous by
   narrowing `emptyPageIsPartial` to the continuation alone, which fails it with
   `Unable to find an element with the text: /with no entries rendered/i`.

**Gates.** Go: gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` all 0;
`go test -count=1 ./...` zero non-ok lines; `-race -count=3` on the trim set ok (78s, run for both Go
commits); `GOGC=50 golangci-lint run --concurrency=2 ./...` `0 issues.` (both). Web: biome on the
touched test; `make test-web` PASS (typecheck/test/lint).

**Concern.** In the corner the Medium's fixture creates — the entry-less page within the diagnostic's
length of the cap — the page now keeps a continuation pointing at the unrenderable entry and says
nothing, so a client re-requesting it gets that page again. Staying inside the cap and reporting the
omission are not both available there; the real fix is bounding what the envelope can contain
(`Label`, and the per-record `Branch.Error` accumulation), which I flagged in Task 93 as deserving
its own issue.

## Task 96 follow-up — the skip always advances (`6d60b6275`)
`skipActivityTrimmedEntryWithinLimit` tries the named message, then the fixed
`one entry was too large to render and was skipped`, each measured with the page; if neither fits it
advances with no message and `slog.Warn`s, naming the entry and session. The branch is never
restored, so the token always moves past an entry no page can render.
RED (fixture resized to leave room for the token but not the sentence):
`ResumeIndex = 0, want 1 -- the entry fits no page, so the token has to advance past it whether or not the page can say so`
GREEN: passes, response ≤ cap, and the captured log names job_huge (the test swaps the default
slog handler so the run's output stays pristine).
Residual: when the page has less slack than the advanced token itself costs (~12 bytes), the
response goes over the cap by that much rather than looping — the ruling's stated preference.

# Task 101 — PR #1179 RoboRev round 5 (1 Medium, 2 Lows)

**Status:** DONE with one open question. `4614dc17d` (Medium), `de8ec4d15` (Low 1), `f3eb696ed` (Low 2).
No amend, no push, merge f913db486 untouched.

1. **Live root cannot page into a closed child — confirmed, fixed the small way.**
   RED `TestJobActivityTree_LiveRootChildContinuationSurvivesHistoricalEpochs` (live session, retained
   delegate, child read historically with a restamped delegate journal):
   `live root's own continuation into its closed child was rejected: activity continuation is stale: the underlying journal changed; restart pagination without a continuation`. GREEN: passes.
   Fix: `root.live == nil &&` guards the generation comparison. Why that is safe: a live root's pages
   carry zeros for both generations (`loadLiveActivityBase` reads neither fold cache), so the
   comparison can only reject, never confirm. The fence that remains is Revision — the root's
   `jobActivityClock` is shared with every session spawned under it and bumps on every job
   started/finished anywhere in that tree, so any daemon-side change between mint and resume is
   caught. Given up: noticing a child journal rewritten *out of band* mid-pagination, which today is
   indistinguishable from the zeros a live mint always carries. The full fix is #1195 (each session
   mints its own generations, ~40 lines across the collector, the trim signature, six call sites and
   `markActivityDelegateTruncated`); I did not do it here.
2. **Silent fallback minted an unmeasured token — confirmed, fixed.** The advance is now applied and
   marshalled before the page is accepted. RED (against `4614dc17d`, fixture with 4 bytes of slack):
   `server log "...msg=\"activity page skipped an entry it had no room to report\"..." does not carry bytes=4194311 -- the overrun was never measured`. GREEN: passes.
3. **Test-only `skipActivityTrimmedEntry` — deleted.** `TestTrimActivityTrailingEntry_MintsResumedSessionsOwnIndex`
   now drives `mintActivityTrimContinuation` + `explainActivitySkippedEntry`, the production pair;
   no assertion changed.

**Gates.** gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` all 0;
`go test -count=1 ./...` zero non-ok lines; `-race -count=3` on the trim + continuation sets ok
(76-82s, run for each commit); `GOGC=50 golangci-lint run --concurrency=2 ./...` `0 issues.` each.

**Open question for Jesse/the coordinator.** Low 1 asked for a page that never exceeds the cap; Task
96's follow-up ruled that a skip must always advance. In the corner where the page has less slack
than the advanced token costs (~8-12 bytes) those cannot both hold: the alternatives are an over-cap
page that advances, or an in-cap page whose token points at an entry no page can render (the #1172
loop). I kept the advance and made the overrun measured and logged (entry, session, bytes, limit).
Say the word if you want the opposite priority.

# Task 108 — PR #1179 RoboRev round 6 (2 Mediums, 1 Low)

**Status:** DONE. `5725fe2e8` (Medium 1 — refuted, guard test), `2b4b0b928` (Medium 2 — fixed),
Low 3 refuted by reference to #1195. No amend, no push, merge 59f0602e7 untouched.

1. **Stranded nested continuation — REFUTED with a fixture.** `TestLoadSessionJobActivityTree_TrimmingADelegateKeepsItsChildReachable`
   (20 root jobs x 250KB + a delegate whose child holds 10 x 250KB) forces exactly the order the
   finding needs. Instrumented run: `page 1: rootEntries=16 delegates=0 childEntries=0` — the child
   was trimmed to empty and the delegate dropped after it — then `page 2: rootEntries=5 delegates=1
   childEntries=10`. All 30 jobs arrive exactly once, no branch errors. The reason nothing strands:
   the owner mints at the dropped entry's OWN index, so the next page resumes AT the delegate and
   re-renders its subtree from the top with the room the dropped siblings freed. The child's
   continuation is only lost when the delegate itself cannot fit a page, and then nothing under it
   is renderable anyway. Test committed as a guard; no production change.
2. **Delegate creation did not move the live fence — CONFIRMED, fixed.** RED
   `TestJobActivityTree_LiveContinuationRejectedAfterADelegateAppears`:
   `resume delivered [dlg_b] against a list that shifted under it; want the continuation refused`.
   GREEN: the resume is refused with the live-session-changed error. Fix: `delegateTreeController.appendLocked`
   calls the new `(*Session).noteJobTreeShapeChange` for every appended `Created` event, so the clock
   (owned by `Session.jobActivityClock`, already reachable through the controller's `rootRuntime`)
   now means "the shape of this tree changed". `delegate_created` is the only structural insert event
   in delegatestore; retained-terminal eviction happens during the fold that a creation triggers, so
   it is covered by the same bump. Full agent suite green, including the delegate suites.
3. **Depth-limit placeholders mint JobsEpoch 0 — CONFIRMED, deferred to #1195.** The placeholder at
   `agent/jobs_activity.go:454-461` is built without loading the child, so its `JobsEpoch` is 0, and
   `agent/jobs_activity.go:1118` mints that 0 into the depth-truncation continuation
   (`markActivityDelegateTruncated`, `:1223`). Establishing the real epoch means folding the child's
   jobs.jsonl — precisely the per-session work the depth limit exists to avoid — so it is not the
   local read the ruling allows. Recommend adding to #1195: "depth-limit placeholder mints JobsEpoch 0
   (jobs_activity.go:454-461 -> :1118); same page-root-vs-target epoch family."

**Gates.** gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` all 0;
`go test -count=1 ./...` zero non-ok lines (run for both commits); `-race -count=3` on the trim +
continuation sets ok (68-80s); `GOGC=50 golangci-lint run --concurrency=2 ./...` `0 issues.` each.

**Concerns.** The clock bump is the first write from the delegate controller into the session's
activity clock. It also moves `JobActivityTree.Revision`, which the web uses as a freshness signal,
so an activity panel now refreshes when a delegate appears — correct, but a behaviour change beyond
pagination. Task 101's open question (in-cap page vs always-advancing token) is still unanswered.

# Task 110 — PR #1179 RoboRev round 7 (1 Medium)

**Status:** DONE. `83ec6cc4d`. No amend, no push, merge b785c6162 untouched.

**Confirmed.** The bump was memory-only; nothing wrote the new revision down, so a restart restored
the pre-delegate value and accepted a continuation minted against the old list.
- RED (pre-fix seam: bump without the unsaved flag): `persisted JobTreeRevision = 0, want at least 1 -- a restart would restore a revision from before the delegate appeared and accept a continuation minted against the old list`
- GREEN: `TestJobTreeShapeChange_SurvivesARestart` passes — the meta carries the moved revision and a
  clock restored from it is at least that value.

**Path job events use, and whether I matched it.** `emitWithJobTreeRevision` (session.go) bumps the
clock and immediately calls `s.maybeAutoSave()`; `Meta()` reads the live clock into
`JobTreeRevision` (session_state.go:172) and restore replays it with
`jobClock.ensureAtLeast(meta.JobTreeRevision)` (session_init.go:875). I matched that encoding exactly
— same clock, same meta field, same `maybeAutoSave` — with one difference forced by locking:
`maybeAutoSave` takes `metaSaveMu` then `s.mu`, and the bump happens inside
`delegateTreeController.appendLocked` under the tree's `c.mu`, while several session paths hold
`s.mu` and then call into the controller. Saving there would have introduced a c.mu -> s.mu order
against an existing s.mu -> c.mu one. So `noteJobTreeShapeChange` bumps and sets an
`atomic.Bool`, and `CommitStart` — now a thin wrapper over the locked body — calls
`persistJobTreeShapeChange` after the lock is released. `delegate_tree_start.go:768` is the only
site that appends `delegate_created`, and it is reached only through `CommitStart`.

**Gates.** gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` all 0;
`go test -count=1 ./...` zero non-ok lines; `-race -count=3` on the activity set (61s) and the
delegate set (184s) ok; `GOGC=50 golangci-lint run --concurrency=2 ./...` `0 issues.`

**Note for the queue.** `gofmt` on PATH here is Homebrew Go 1.26.1 while the toolchain is go1.27.0,
and they disagree on one pre-existing construct in delegate_tree_start.go. A `gofmt -w` with the PATH
binary silently reformatted 19 unrelated lines and the gate caught it; I reverted that and now check
with `$(go env GOROOT)/bin/gofmt`. Other lanes running `gofmt -w` on that file will hit the same.

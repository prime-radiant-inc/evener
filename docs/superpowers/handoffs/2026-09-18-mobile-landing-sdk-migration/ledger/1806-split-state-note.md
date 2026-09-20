# #1806 split state note (agent: the daemon owns the ask boundary)

Written because the implementer session passed ~600k tokens. Stopping point after
round-1 review fixes on all three split PRs, rebased and pushed, dispositions posted.
#1806 itself is closed (comment posted: "Decomposed after five rounds into #1905,
#1906, #1907; closing.").

Work happened in `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/hub-askpending-notify`
(a separate worktree from this coordinator one — never write code there from this
worktree). That worktree is currently checked out on `claude/1806-s3-ask-boundary-carrier-defer`,
working tree clean, nothing uncommitted.

## The three PRs, current heads

All base `main`, stacked S1 -> S2 -> S3 (each branch's commits include its
predecessors' until they merge — normal for a stack opened against `main`).

- **S1 #1905** `claude/1806-s1-ask-boundary-live` @ `9899c9630207db2f7a1d4f53c7eb6cd584d1063f`
  - Commit 1 (`2f23354dd`): the live mechanism — `schema.TurnFailureInfo.SteeringCarrier`,
    `steeringSourceAnswersAsk`/`steeringCarrierClaimAnswersAsk` predicates, gated entry
    clear in `processOneInput`, clear-before-emit ordering in `consumeSteeringMessage`,
    gated failure tags (`emitSteeringCarrierTurnFailure`, `recordFailedSteeringSelection`).
  - Commit 2 (`9899c9630`): round-1 review fixes — reworded forward-referencing comments
    (nothing here reads the tag yet, that's S2); restored original admit/unpark-after-emit
    order (only the ask-pending clear moves ahead of `EventSteeringInjected`);
    `steeringCarrierClaimAnswersAsk` fails closed on a missing journal record; 4 new
    behavioral tests for both `TurnFailure` tagging shapes x both steer kinds; **the
    drain-ladder bug below**.
  - Own diff vs `main`: 8 files, 634 insertions(+), 64 deletions(-). Non-test production
    ≈286 lines (above the 150 target, under the 400 ceiling — flagged in the disposition,
    not split further: it's one cohesive mechanism plus its own review round).
  - **Owes**: nothing known. Round-1 disposition posted. Not yet reviewed at the new head.

- **S2 #1906** `claude/1806-s2-ask-boundary-restore` @ `8bc4805c195de97f0eca319af982a8a6e025e7a3`
  - Commit 1 (`7d14c744e`, was `fe2ec0873` before the S1 rebase): the restore mechanism —
    `turnResolvesAskBoundary`, `steeringOriginBoundary`, rewritten `deriveRestoredState`/
    `deriveRestoredAskPending`, `divergenceTurn` threading through `session_init.go`/
    `session_state.go`, plus the round-6 Medium fix (`roundEntryResolvesAskBoundary`, a
    non-resolving carrier's generic completion no longer stops the scan early).
  - Commit 2 (`8bc4805c1`): round-1 review fixes — `deriveRestoredAskPending` now
    accumulates questions across multiple non-resolving rounds (live `askPending` only
    ever appends, never replaces — a human-note carrier's own round can post a brand-new
    question without answering an earlier one); `turnResolvesAskBoundary`'s `TurnSteering`
    case now falls back to `isHumanNoteSteer`'s text-shape check for a kindless,
    provenance-less note (inherited fork prefixes: `steeringOriginBoundary` nils the
    journal lookup regardless of what's passed).
  - Own diff vs S1's tip: 7 files, 1047+/55- (commit 1) then 2 files, 204+/7- (commit 2).
    Non-test production ≈244 (round 1) + ~65 (round 2) lines.
  - **Owes**: nothing known. Round-1 disposition posted (folds in the kindless-note
    finding that #1907's panel also raised against this same mechanism). Not yet
    reviewed at the new head.

- **S3 #1907** `claude/1806-s3-ask-boundary-carrier-defer` @ `a3da29b828932f4c63ffdf3b6fb7d7bfae447392`
  - Commit (`a3da29b82`, was `cc2622cf0` before rebasing): the `defer` on the
    steering-carrier claim id + `sessionLifecycleFault(ctx, "steering_carrier_drain")`
    panic-injection point + its test. Untouched by either review round — the panel at
    `cc2622cf0` (reviewing the full stacked S1+S2+S3 diff) found nothing against this
    piece's own 10 lines; all three findings landed on S1/S2's mechanisms and are fixed
    there (see above).
  - Own diff vs S2's tip: 2 files, 54+/2-, unchanged by the two rebases.
  - **Owes**: nothing known. Round-1 disposition posted (points at #1905/#1906 for the
    findings that landed on their pieces). Not yet reviewed at the new head.

**Every PR** was rebased through both S1 fix rounds via `git checkout <branch> &&
git rebase <predecessor-branch>`, with `agent/session_ask_test.go` conflicting each time
(two independent test blocks inserted at the same point in the file — never a semantic
conflict) resolved by `git checkout --conflict=diff3 -- agent/session_ask_test.go`, then
locating the `<<<<<<< ours` / `||||||| base` / `=======` / `>>>>>>> theirs` line numbers
and concatenating the `ours` block then the `theirs` block (straight concatenation, no
reordering needed since insertion points never overlapped semantically), verified with
`go build ./...`, `go vet ./...`, and a duplicate-symbol grep
(`grep -o '^func Test[A-Za-z0-9_]*\|^func [a-zA-Z][A-Za-z0-9_]*' session_ask_test.go | sort | uniq -d`)
before `git add` + `git rebase --continue`. Pushed with `--force-with-lease` (the S1
push was a plain push, no rebase needed there). All three own-diff stats were confirmed
unchanged by the rebase (`git diff --stat <predecessor-tip>..<new-tip>` matches the
pre-rebase number) before pushing.

## The drain-ladder bug (found by measurement, not in any review panel)

**Where**: `agent/session_lifecycle.go`, the drain-ladder gate inside `ProcessInputKind`'s
loop, ~line 1395 (now `awaiting := s.State() == SessionAwaiting || s.askPendingCount() > 0`).

**The stale invariant**: the gate used to read `awaiting := s.State() == SessionAwaiting`
alone, with a comment proving this is equivalent to `askPendingCount() > 0` at that exact
capture point. The proof rested on: `processOneInput` used to clear `askPending`
unconditionally at entry for every accepted turn, so nothing could reach this capture
with a pending ask the CURRENT round didn't itself just post (`askedThisRound`, a delta
`deliverIfCommunicated` maps straight to `SessionAwaiting`). S1's own change (gated entry
clear for a non-answering carrier) breaks that premise: a human-note carrier's entry
clear is skipped, so `askPending` can stay nonzero across the carrier's own round without
`askedThisRound` ever being true for it.

**What goes wrong without the fix**: a note-carrier round that only acknowledges the note
(a "communicate" tool call, `askedThisRound == false` since nothing NEW was posted)
settles `SessionIdle` via `deliverIfCommunicated` at THIS capture point — the general
"idle -> awaiting" upgrade (`armAwaitingAtSettle`) runs later, at the outer loop's own
terminal settle, not before this capture. Reading state alone therefore says "not
awaiting" while `askPendingCount() == 1` (a genuinely unanswered question), and
`!awaiting` is true, so the ladder pops and RUNS a queued `FollowUp` past the unanswered
question — measured directly: a `FollowUp` queued before the note, drained through
`ProcessPendingUserInput`, ran a third model request carrying the follow-up text while
ask1 was still live-pending.

**Fix**: `awaiting := s.State() == SessionAwaiting || s.askPendingCount() > 0` — the OR
only ADDS coverage for this gap; it never changes behavior for the pre-existing case
(when `askedThisRound` is true, both sides of the OR already agree). Comment above it
rewritten to describe the fix rather than the now-false "iff" proof.

**Test**: `TestAskUser_FollowUpNotDrainedWhilePendingAskSurvivesAHumanNoteCarrierRound`
in `agent/session_ask_test.go` (S1's test file, landed in commit `9899c9630`). Drives:
ask1 posted -> `FollowUp("run the tests")` queued -> `SetHumanNote` -> real
`ProcessPendingUserInput` (note carrier's round ends via a real `communicate` tool call,
matching `TestAskUser_RestoreDoesNotResolveAcrossASuccessfulHumanNoteCarrierCompletion`'s
shape) -> asserts the follow-up's own model request never fired within that call, and
`askPendingCount()` is still 1 afterward. Falsified: reverting just the `|| s.askPendingCount() > 0`
disjunct made it fail (`the queued follow-up ran while ask1 was still pending`); restored,
green.

This same class of bug (a proof of equivalence between two encodings of "is there
unfinished business" silently invalidated by a later change to one side) is exactly what
the `review-drift-close-the-class` ledger memory warns about — worth a second look at
`agent/session_goal.go`'s and `agent/session_compaction.go`'s own `askPendingCount()`-keyed
guards (session_tools_ask.go's doc comments cite both as callers) to confirm THEY don't
have a similar state-vs-pending-set drift, since this PR touched the same underlying
mechanism. Not checked in this session — flagging as a residual, not filing an issue yet
since it's speculative (no measured bug there).

## Go gates cheat-sheet for package `agent` (from `agent/`, this session's exact commands)

Per-PR, run ALL of these before pushing; all were green on every branch/commit in this
session.

```
$(go env GOROOT)/bin/gofmt -l .                      # toolchain gofmt, NEVER the PATH one
go vet ./...
go vet -tags evenerfuzz ./...
GOOS=windows go vet -tags evenerfuzz ./...
golangci-lint run ./...                              # from agent/ — it's its own go.work module
go test -count=1 -run '<TestNames>' .                # targeted, plain
go test -count=1 -tags evenerfuzz -run '<TestNames>' .
go test -race -count=1 -run '<TestNames>' .           # only the tests that exercise the sync/order claim
go test -count=1 -run 'TestNoBareWallClockDeadlineInAgentTests' .   # bare-wall-clock audit, see below
```

For this PR's own test set, `<TestNames>` was
`'TestAskUser|TestRestored|TestInherited|TestHistoryCopy|TestRebuilt|TestAcceptSteeringCarrierInput'`
for the plain/evenerfuzz runs, and a narrower subset (the specific new/changed tests) for
`-race`, matching each round's disposition. No full-package `go test -count=1 .` was run
after the first attempt (see below) — the brief's "lean on CI, not local full gates" rule
holds; targeted + CI is enough.

**Never run a bare `go test -count=1 .` in this package.** It takes 5-10+ minutes
(historical note: 400-600s), and running it while ALSO editing files for a falsification
check in the same worktree risks the background job seeing inconsistent source (it
doesn't corrupt the binary since Go compiles before running, but it wastes the run and
the process needs killing cleanly — killed one mid-session via `kill <pid>` after
confirming via `ps aux` which PID belonged to THIS worktree, since an unrelated worktree
(`hub-broadcast-after-apply`) had its own unrelated `go test` running concurrently on the
same box — never kill a PID without confirming its cwd first).

**Bare-wall-clock audit** (`agent/deadline_audit_test.go`'s
`TestNoBareWallClockDeadlineInAgentTests`): scans `agent/*_test.go` for
`context.WithTimeout`/`WithDeadline`/`time.After`/etc. fed a literal duration with no
`// TRIPWIRE: <why>` comment on the same or the line directly above. This is what the
original #1806 CI failure was (a missing TRIPWIRE comment on one `context.WithTimeout` in
a round-5 helper) — every new test in this split that uses
`context.WithTimeout(context.Background(), 30*time.Second)` needs the
`// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.`
comment directly above it (copy the exact wording other tests in the same file already
use — the audit doesn't care about wording, only presence, but consistency matters for
readers).

**Shared-TMPDIR retention flakes to skip** (only relevant if you ever DO run a wider
suite than targeted tests — did not come up this session since no full-package run
completed, but documented per the coordinator's ask): prior rounds' dispositions on this
same PR (rounds 2-4, in the original #1806 history) recorded 5 tests that fail on a
concurrent-worktree box independent of any code change:
`TestRetirementAutonomousReLockRetryRefusedRearms`, `TestRetirementAutonomousReLock`,
`TestRetirementSafetyEscalation`, `TestScratchRetentionTerminalReleaseAllowsCollection`,
`TestRetirementSharedChildScratchBindingsRestore`. If a full `go test -count=1 .` is ever
run in this package and these fail, `-skip` them and rerun before concluding anything is
broken; they fail identically with a diff stashed out, on the unmodified base, per those
earlier rounds' own measurement.

## What's NOT done / next steps for whoever picks this up

- No CI check run on the new heads (9899c9630 / 8bc4805c1 / a3da29b82) was polled after
  pushing — the task instructions say no polling; whoever resumes should check
  `gh pr checks 1905` / `1906` / `1907` (or wait for round-2 review panels) before
  merging.
- The `review-drift-close-the-class` residual above (session_goal.go /
  session_compaction.go's own askPendingCount()-keyed guards) is unexamined — worth a
  targeted look, not urgent.
- No further review rounds have run against the post-fix heads listed above as of this
  note; expect round-2 panels on all three before merge.
- Merge order is S1 -> S2 -> S3 once each is CI green + RoboRev clean, per the standing
  merge-on-lows-then-follow-up / five-review-rounds norms already in BRIEF-COMMON.md.

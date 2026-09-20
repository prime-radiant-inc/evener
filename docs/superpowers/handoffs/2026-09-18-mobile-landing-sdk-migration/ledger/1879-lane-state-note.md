# #1879 lane state note (retired 2026-09-18 ~16:00 PDT, past 400k tokens)

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/threads-ready-guard`.
Branch: `claude/1879-retirement-flake` @ `0a08b4d168bfdc691c5dfefae09d11b66f17b66b` (base main).
Working tree clean. PR: #1921, base main, three rounds of review, all green as of this head.
Test-only change throughout (0 non-test lines across all three commits:
`2e7c2fe0a` round 1, `6953aa1c1` round 2, `0a08b4d16` round 3).

## Root cause, three lines
`launchInitialPromptNamer`/`launchCompactionNamerGated` (`agent/session_namer.go`)
start the background session namer in its own un-joined goroutine, which correctly
holds an `"autonomous"` retirement blocker (`s.naming.pending`,
`agent/session_retirement_evidence.go:76`) until it settles. Two test call sites —
the shared `assertRetirementEvidenceEligible` helper (~15 callers) and
`TestRetirementSafetyDetachedLifetime`'s own raw `TryClaim` — checked eligibility
right after a turn settled without waiting for that goroutine, so under CI load
they could observe it still in flight and fail with `"settled owner not eligible"
... Category:autonomous ... DelegateID:` (exactly issue #1879's logged text).

## The shared seam: `retirementClaimAfterFirstTurn`
`agent/session_retirement_evidence_test.go` now has one function:
```go
func retirementClaimAfterFirstTurn(root *Session, c *RetirementController) (*RetirementClaim, RetirementSnapshot, error) {
	root.sendersWG.Wait()
	return c.TryClaim(true)
}
```
`assertRetirementEvidenceEligible` and `TestRetirementSafetyDetachedLifetime`'s
"independent detached lifetime blocked eligibility" check both call it — it's the
only place the fix lives. The regression test
(`TestRetirementClaimAfterFirstTurnAwaitsInFlightNamer`) calls this exact function
too (not a re-implementation), so reverting its one `sendersWG.Wait()` line
reproduces both #1879 occurrences at once and reliably fails the test. Round 2's
first attempt at this test built a *second*, independent copy of
`sendersWG.Wait()+TryClaim` inside its own goroutine — that one didn't actually
depend on the real call site's fix at all (round 3 finding 4 caught it); collapsing
onto one shared function is what makes reverting the fix and re-testing meaningful.

Why the regression is deterministic, not timing-based: `resultCh` is buffered, so
whatever it holds is fixed at the instant `retirementClaimAfterFirstTurn`'s
`TryClaim` call actually ran, independent of when the test reads it.
`sync.WaitGroup`'s own invariant guarantees a *correct* implementation's
`sendersWG.Wait()` cannot return before the test's `release` channel closes, so
that call cannot have happened yet; a version that skips the wait calls
`TryClaim` immediately against the still-blocked real namer and gets back a
genuine ineligible verdict — exactly the value the post-`release` check receives,
whenever it happens to run. The `started` channel + `runtime.Gosched()` loop
before that (no wall-clock literal) just makes an *early*, specific failure
likely; it is documented as a best-effort diagnostic, not the proof — round 3
flagged an earlier version of this comment for overclaiming that.

## Reproduction numbers
- Deterministic forced-interleaving regression (final form,
  `TestRetirementClaimAfterFirstTurnAwaitsInFlightNamer`): **fails 20/20** with
  `retirementClaimAfterFirstTurn`'s wait reverted, **passes 20/20** restored.
  (Round 1's first cut of this test: 5/5 fail / pass; each round re-falsified
  after restructuring.)
- Statistical reproduction attempts — did **not** reproduce naturally on this
  16-core dev box (consistent with a narrow, load-dependent window: 6 CI hits
  across "dozens of concurrent" jobs in one day vs. thousands of local runs):
  - `TestRetirementAttentionArmRetryPending`: 500 baseline + 500 under
    `GOMAXPROCS=2` with 12 `yes`-loop CPU-burn processes + a parallel
    `go test -count=3 ./agent/` load job — 0 failures.
  - `TestRetirementSafetyColdJobWatchContent`: 50 under the same contention — 0.
  - `TestRetirementSafetyDetachedLifetime`: 30 under the same contention — 0.
  - `TestRetirementTreeSettleDrainsPendingRootAttention`: 100 under the same
    contention — 0 (see below).
- CI: 6 flake hits logged on #1879 before round 1's fix landed; 1 more
  (`TestRetirementSafetyDetachedLifetime`, run 35397613924, the second,
  unprotected call site) after round 1 landed at `2e7c2fe0a`, which drove round
  2's fix. No further CI failures reported against this PR as of `0a08b4d16`.

## Still unexplained: `TestRetirementTreeSettleDrainsPendingRootAttention`
1 of the original 6 CI occurrences (run 35385964042, 2026-09-18 19:40Z). Same
blocker shape (`Category:autonomous`, no `DelegateID`, all-zero `Phase:resident`
snapshot fields) but structurally different from the three fixed above:
- It never calls `ProcessInput` on root before its critical assertion — only
  `root.createDelegate` + waiting on the delegate's own `done` channel — so
  root's own namer shouldn't be armed yet.
- Its critical check is a **direct** `c.TryClaim(true)` +
  `hasRetirementBlocker(state.Blockers, "notification")`, not
  `assertRetirementEvidenceEligible` or `retirementClaimAfterFirstTurn`, so none
  of this PR's fixes touch it.
- The observed failure shows *only* the `autonomous` blocker; the expected
  `notification` blocker (for the pending root delegate attention the test's own
  poll loop — `root.hasPendingRootDelegateAttention()` — had just confirmed
  true) is entirely absent. That's the real puzzle: something drained
  `root.rootAttentionWakeIDs` between the poll and the claim, which shouldn't be
  possible since `root.notifyFunc` is nil until `awaitTreeQuiesced`'s
  `DrainJobTree` call wires it up later in the test.

**Tried:** read the full call chain (`createDelegate` → `delegateRuntime.create`
→ `preseedInput` → `trackAndLaunchPreparedSubagent` → `launchSubagentRun` →
`sub.run` → `processOneInput`); confirmed `sub.run`'s default `EntryUserInput`
path reaches `acceptUserInputWithSkillSelection`, which calls
`launchInitialPromptNamer` — meaning the **child's own** session may launch its
own async namer during task processing, independently of root. 100 iterations
under `GOMAXPROCS=2` + heavy CPU contention reproduced nothing.

**Try next:**
1. Confirm (or rule out) the child-namer hypothesis directly: does the child
   session constructed by `createDelegate`/`selectSubagentModel` actually resolve
   a namer model (`sessionNamerEnabled`)? Instrument with the same
   `cfg.testOnly.namerClient` hook used in this PR's regression, but on the
   *child* session, and force it in-flight; check whether the tree-scan's
   `s.retirementEvidence()` call for that child (delegate_tree_retirement.go:100)
   reports `autonomous` with the child's `SessionID` and empty `DelegateID` —
   matching the failure log.
2. Separately, chase the missing `notification` blocker: audit every place that
   can clear `s.rootAttentionWakeIDs` (`session_attention.go`) for a path that
   doesn't require `notifyFunc` to be wired — e.g. something the delegate's own
   completion/finalize sequence calls directly on the parent, bypassing the
   wake-callback indirection. `driveChildIfNotStopGated` /
   `driveSubagentNotificationTurn` (`subagents.go`) are the most promising
   candidates; trace whether either can run synchronously off the child's
   completion without `notifyFunc` being set.
3. If reproduced, use the same TDD pattern as this PR: a held/release gate on
   whatever async work is found, `started`+`Gosched` for an early diagnostic,
   the buffered-channel post-release read as the real proof, falsify by
   reverting, restore, re-falsify.

## Agent-package gates cheat-sheet
- `$(go env GOROOT)/bin/gofmt -l <files>` — **never** the Homebrew/PATH `gofmt`;
  it disagrees with this repo's toolchain on some constructs and silently
  reformats unrelated lines (memory: `evener-gofmt-use-toolchain-binary`).
- `go vet ./agent/...`, `go vet -tags evenerfuzz ./agent/...`,
  `GOOS=windows go vet -tags evenerfuzz ./agent/...` — all three, every time;
  the fuzz-tagged and Windows variants catch real things the plain one doesn't.
- `golangci-lint run ./agent/...` — note it flags idiomatic-modernization findings
  too (e.g. `for i := 0; i < N; i++` → `for range N`, `modernize` linter), not just
  correctness; check the output even when you expect 0 issues.
- `TestNoBareWallClockDeadlineInAgentTests` (`agent/deadline_audit_test.go`) —
  scans `agent/*_test.go` (non-recursive) for a bare numeric-literal wall-clock
  bound fed into `context.WithTimeout`/`WithDeadline`, `time.After`,
  `time.NewTimer`/`NewTicker`/`AfterFunc`, or this package's
  `waitForCondition`/`awaitWithin` helpers. Two outs: a `// TRIPWIRE: <why this
  ceiling sits far above the expected time>` comment on the literal's own line or
  the line directly above it (a hang guard, never the mechanism), or naming the
  duration as its own constant/variable. Run it explicitly
  (`go test -run '^TestNoBareWallClockDeadlineInAgentTests$' ./agent/`) after
  adding any new channel-based synchronization test — it's easy to add a
  legitimate-looking timeout and forget the marker.
- **The 10-minute race cap is not a hang.** `go test -race -run TestRetirement
  -count=20 ./agent/` (the whole family, matching the issue brief's literal gate
  command) reliably hits Go's 600s per-package test timeout and dumps every
  goroutine — this is `agent`'s test suite being large and `-race`-mode being
  slow at that count, not a deadlock (matches the pre-existing
  `agent-race-job-runs-at-its-time-cap` pattern). Scope race runs to a `-run`
  regex naming exactly the tests you touched plus directly related ones
  (e.g. `-run '^(TestFoo|TestBar)$'`); that finishes in under a minute and is
  the meaningful signal.
- **This dev box has pre-existing, unrelated environmental flakes** in
  `agent/*_test.go` when you run the whole `^TestRetirement` family: on
  `TestRetirementAutonomousReLock(RetryRefusedRearms)`, a git
  `worktree list --porcelain` scan intermittently can't find an entry it expects
  (this repo has 80+ registered worktrees from other concurrent agent sessions);
  on `TestRetirementSafetyEscalation` / `TestRetirementSharedChildScratchBindingsRestore`,
  a macOS `/var` → `/private/var` symlink trips the sandbox's
  refuse-to-traverse-a-symlink policy, and leftover `/tmp/evener-sandbox-*` dirs
  (from heavy repeated local `go test` invocations) print retention-manifest
  warnings. None of these are caused by code in this PR — different files,
  different mechanisms, confirmed by diffing against a clean checkout. Don't
  chase them when auditing an `agent/` change; just check the failing test's
  file/mechanism against your actual diff before attributing.
- Falsification pattern used throughout this PR: `cp file /tmp/file.bak`, edit
  out the fix (or replace with a no-op), run the regression at `-count=20`,
  confirm it fails every time (and *how* — check which assertion fires, not just
  the exit code), `cp /tmp/file.bak file` to restore, re-run to confirm green.

# Task 5: Final verification and cleanup report

Status: DONE

Implemented deferred resume SessionStart hook delivery.

## Commits

- Existing plan commits in final log:
  - `db8b0605 fix(agent): preserve resume hooks across rejected turns`
  - `40f72157 test(agent): cover deferred resume session start hooks`
  - `fbe7a520 fix(agent): drain resume hooks on first user turn`
  - `1a567bb7 fix(agent): defer resume session start hooks`
- Verification fix commit created by Task 5:
  - `915c4e88 fix(agent): drain deferred resume hooks during delegate restore`

## Commands run

1. Focused package tests:

```bash
go test ./agent -run 'TestRestoreSession|TestSession_Notification|TestSession_PreCompactHook|TestSession_Compact' -count=1
```

Result: PASS

Evidence:

```text
ok  	primeradiant.com/serf/agent	0.140s
```

2. Full agent package tests, first attempt:

```bash
go test ./agent -count=1
```

Result: FAIL

Failure:

```text
--- FAIL: TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce (0.11s)
    job_delegate_send_test.go:2373: SessionStart hook executions = 0, want 1
FAIL
FAIL	primeradiant.com/serf/agent	9.043s
```

Diagnosis: resume SessionStart hooks were deferred only when `runSessionStartHooks` was true. Delegate restore uses `deferRestoreSideEffects: true`, which passed `runSessionStartHooks=false`; this skipped recording pending resume hooks entirely. After recording pending resume hooks for resume restores regardless of deferred side effects, `runDeferredRestoreSideEffects` also needed to drain those pending hooks so delegate reconstruction side effects still run exactly once on the winning retained runtime.

Fix: scoped changes in `agent/session_init.go`:

- `initPlugins` now records pending resume SessionStart hooks before checking `runSessionStartHooks`.
- `runDeferredRestoreSideEffects` now drains pending SessionStart hooks after deferred job/watch notification recovery completes.

3. Targeted regression/focused tests after fix:

```bash
go test ./agent -run 'TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce|TestDelegateReconstructionRacingParentCloseDoesNotTrackOrRunSideEffects|TestDelegateReconstructionParentCloseBeforeDeferredSideEffectsDoesNotRunThem|TestRestoreSession|TestSession_Notification|TestSession_PreCompactHook|TestSession_Compact' -count=1
```

Result: PASS

Evidence:

```text
ok  	primeradiant.com/serf/agent	0.437s
```

4. Required focused package tests after fix:

```bash
go test ./agent -run 'TestRestoreSession|TestSession_Notification|TestSession_PreCompactHook|TestSession_Compact' -count=1
```

Result: PASS

Evidence:

```text
ok  	primeradiant.com/serf/agent	0.156s
```

5. Required full agent package tests after fix:

```bash
go test ./agent -count=1
```

Result: PASS

Evidence:

```text
ok  	primeradiant.com/serf/agent	8.785s
```

6. Broader deterministic tests:

```bash
go test ./... -count=1
```

Result: PASS

Evidence: all packages passed, ending with:

```text
ok  	primeradiant.com/serf/tools/tool-fluency/cmd/serf-fluency	0.046s
```

7. Pre-commit check:

```bash
git diff --check
```

Result: PASS, exit 0.

8. Final diff/status inspection:

```bash
git status --short
git diff --stat HEAD~3..HEAD
git log --oneline -5
```

Result: PASS/inspected.

Output before writing this report:

```text
 M .superpowers/sdd/task-1-report.md
 .superpowers/sdd/task-4-report.md  | 200 +++++++++++++++++++++++
 agent/session_init.go              |  49 +++---
 agent/session_lifecycle.go         |  54 ++++---
 agent/session_resume_hooks_test.go | 315 +++++++++++++++++++++++++++++++++++++
 4 files changed, 578 insertions(+), 40 deletions(-)
915c4e88 fix(agent): drain deferred resume hooks during delegate restore
db8b0605 fix(agent): preserve resume hooks across rejected turns
40f72157 test(agent): cover deferred resume session start hooks
fbe7a520 fix(agent): drain resume hooks on first user turn
1a567bb7 fix(agent): defer resume session start hooks
```

## Final git status

Before writing this required Task 5 report, the only dirty file was the pre-existing unrelated file:

```text
 M .superpowers/sdd/task-1-report.md
```

After writing this report, expected dirty files are:

```text
 M .superpowers/sdd/task-1-report.md
?? .superpowers/sdd/task-5-report.md
```

## Unrelated worktree changes left untouched

- `.superpowers/sdd/task-1-report.md` remained modified and was not staged or committed.
- The brief mentions unrelated hub renderer files to leave untouched if present: `cmd/serf-hub/assets/renderer.js`, `cmd/serf-hub/jstest/test-renderer-notifications.js`. They were not present in `git status --short` during this task.

## Summary

All required verification commands pass after the scoped Task 5 fix. A fix commit was required and created because the initial full agent package verification exposed a regression in delegate reconstruction with deferred restore side effects.

## Task 5 Fix Report 2

Status: DONE

Fixed the rejected Task 5 review finding: deferred delegate restore no longer drains/delivers resume `SessionStart` hook model-facing output during restore side-effect processing. Resume hook commands can still execute exactly once for retained delegate lifecycle side effects, while their `ModelContext` and `UserMessages` are preserved and delivered exactly once with the first accepted real post-resume user turn.

## Files changed

- `agent/session.go`
  - Updated `pendingSessionStartKind` comment and state to distinguish deferred output delivery from restore-time lifecycle execution.
  - Added `pendingSessionStartResult` to hold captured resume hook output when a deferred delegate restore runs hooks before the first user turn.
- `agent/session_init.go`
  - Replaced restore-time `drainPendingSessionStartHooks(context.Background())` with `runPendingSessionStartHooksForRestoreSideEffects(context.Background())`.
  - Added helpers to run pending resume hooks during deferred restore side effects without delivering output, store the `hooks.RunResult`, and later consume it from `drainPendingSessionStartHooksForUserTurn`.
  - Kept `initPlugins` recording pending resume hooks even when restore side effects are deferred.
- `agent/job_delegate_send_test.go`
  - Extended `TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce` to cover resume hook output ordering for deferred delegate restore.
  - The test now uses a resume `SessionStart` hook that emits both `additionalContext` and a `systemMessage`, asserts no restore-time steering/history/warning delivery, then asserts first real delegate user input receives the context and user message exactly once while hook command execution remains once.
- `agent/job_delegate_test.go`
  - Added `drainEventWarningsContaining` helper used by the new deterministic regression assertions.

## Review findings fixed

### Critical finding

`runDeferredRestoreSideEffects()` no longer calls the generic immediate-delivery drain. It now calls `runPendingSessionStartHooksForRestoreSideEffects`, which runs the pending resume hook command for lifecycle side effects and stores the hook result without calling `deliverHookContext`, `deliverHookUserMessage`, or `takePendingSessionStartKind`. The pending state therefore remains available for the first accepted real user turn.

### Important finding 1

Delegate restore side effects and lazy model-facing resume hook delivery are now separate paths:

- deferred delegate restore executes the pending resume hook command at most once and captures `hooks.RunResult` in `pendingSessionStartResult`;
- first accepted real user turn consumes the captured result via `drainPendingSessionStartHooksForUserTurn`, delivers `ModelContext` before the user prompt, emits `UserMessages`, and clears pending state;
- if no deferred restore side-effect execution occurred, the first user turn still runs and delivers the pending hook directly as before.

### Important finding 2

Regression coverage was added to the delegate reconstruction side-effect test. It deterministically verifies that restored/deferred delegate resume hook `ModelContext`/`UserMessages` are not delivered during restore/deferred side effects and are delivered exactly once on the first accepted real post-restore delegate user turn.

### Minor finding

The `pendingSessionStartKind` comment now describes deferred output delivery and the captured-result path used when deferred delegate restore has already executed the hook for lifecycle side effects.

## Tests run

1. Previously failing delegate reconstruction side-effect focused tests:

```bash
go test ./agent -run 'TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce|TestDelegateReconstructionRacingParentCloseDoesNotTrackOrRunSideEffects|TestDelegateReconstructionParentCloseBeforeDeferredSideEffectsDoesNotRunThem' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.427s
```

2. Task 4 resume-hook regressions, including MaxTurns:

```bash
go test ./agent -run 'TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput|TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks|TestRestoreSessionStartHooksDrainOnlyOnce|TestRestoreSessionMaxTurnsRejectedUserInputKeepsPendingResumeHooks|TestRestoreSessionRejectedUserInputKeepsPendingResumeHooks' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.090s
```

3. Focused affected Task 5 tests for `session_init.go` / `session_lifecycle.go` changes:

```bash
go test ./agent -run 'TestRestoreSession|TestSession_Notification|TestSession_PreCompactHook|TestSession_Compact' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.163s
```

## Notes

- An initial focused test run failed to build because the new helper in `job_delegate_test.go` needed the `events` import; fixed before final verification.
- An initial version of the regression hook emitted plain stdout after `additionalContext`, which the hook runner correctly treated as model context rather than `UserMessages`; changed the hook fixture to emit JSON `systemMessage` plus `additionalContext` so the test covers both channels.
- The pre-existing unrelated `.superpowers/sdd/task-1-report.md` modification remains unstaged and uncommitted.

## Task 5 Fix Report 3

Status: DONE

Fixed the latest Task 5 re-review race: deferred delegate restore-side-effect resume `SessionStart` hook execution and first real post-resume user-turn delivery are now mutually exclusive. A user turn arriving while restore-side-effect hook execution is in flight waits for the captured restore result instead of running the same hook again, so resume hook output is not lost and is delivered exactly once with the first accepted real user input.

## Files changed

- `agent/session.go`
  - Added `pendingSessionStartInFlight` and `pendingSessionStartCond` to the pending resume hook state.
  - Updated the state comment to document in-flight restore execution and first-user-turn wait semantics.
- `agent/session_init.go`
  - Added condition-variable coordination around pending resume `SessionStart` hooks.
  - `runPendingSessionStartHooksForRestoreSideEffects` now marks hook execution in flight before releasing `s.mu`, stores the captured `hooks.RunResult`, clears the in-flight flag, and broadcasts waiters when execution completes.
  - `drainPendingSessionStartHooksForUserTurn` now waits when restore-side-effect hook execution is in flight, then consumes the captured result exactly once; if no restore execution is in flight, it still runs and delivers the pending hook directly.
  - Clearing semantics now clear pending kind/result/in-flight state when user-turn delivery consumes the result.
- `agent/job_delegate_send_test.go`
  - Made `TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce` cover the racing first-user-turn case deterministically.
  - The test uses FIFO blocking primitives in the hook command: it proves the hook has started and is blocked, starts the first delegate user turn while restore side effects are still in flight, asserts no premature model request and no second hook execution, releases the hook, then verifies captured context/user-message output is delivered exactly once on that user turn.

## Review findings fixed

### Critical finding

The restore side-effect hook runner no longer has a check/run/store race with the first real user turn. Restore side effects claim the pending resume hook by setting `pendingSessionStartInFlight` under `s.mu` before running it. If `drainPendingSessionStartHooksForUserTurn` enters during that unlocked hook run, it waits on `pendingSessionStartCond` until restore execution finishes, then consumes `pendingSessionStartResult`. It does not take/clear `pendingSessionStartKind` or run the hook itself while restore execution is in flight.

### Important finding

Added deterministic regression coverage for the exact racing first-user-turn scenario. The test no longer relies on sleeps: FIFO handshakes prove the restore hook is in progress before the first user turn starts, and release it only after assertions show the user turn has not completed, no adapter request was made, and hook execution count is still one. After release, the first user turn receives the resume hook context, the hook user message is emitted exactly once, and the hook marker remains at one execution.

## Tests run

1. New racing regression test:

```bash
go test ./agent -run TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.164s
```

2. Delegate reconstruction side-effect focused tests:

```bash
go test ./agent -run 'TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce|TestDelegateReconstructionRacingParentCloseDoesNotTrackOrRunSideEffects|TestDelegateReconstructionParentCloseBeforeDeferredSideEffectsDoesNotRunThem' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.253s
```

3. Task 4 resume-hook regressions, including MaxTurns:

```bash
go test ./agent -run 'TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput|TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks|TestRestoreSessionStartHooksDrainOnlyOnce|TestRestoreSessionMaxTurnsRejectedUserInputKeepsPendingResumeHooks|TestRestoreSessionRejectedUserInputKeepsPendingResumeHooks' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.096s
```

4. Focused affected tests for touched session lifecycle/delegate code:

```bash
go test ./agent -run 'TestRestoreSession|TestSession_Notification|TestSession_PreCompactHook|TestSession_Compact|TestSendDelegateMessage|TestDelegateReconstruction' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	1.357s
```

## Notes

- The pre-existing unrelated `.superpowers/sdd/task-1-report.md` modification remains unstaged and uncommitted.

## Task 5 Fix Report 4

Status: DONE

Fixed the latest Task 5 re-review findings around resume `SessionStart` hook output ordering for watch/autonomous entries and strengthened the delegate restore race test.

## Files changed

- `agent/session_lifecycle.go`
  - `processOneInput` now tells `acceptUserInput` whether the entry is a real `EntryUserInput`.
  - `acceptUserInput` drains deferred resume `SessionStart` output only when `kind == EntryUserInput`; `EntryWatchDelivery` keeps its existing shared accept path but no longer consumes resume hook output.
- `agent/session_resume_hooks_test.go`
  - Added `TestRestoreSessionWatchDeliveryDoesNotDrainResumeSessionStartHooks`.
  - The regression first verified RED: before the production fix, the watch-delivery model request contained `RESUME_HOOK_CONTEXT`.
  - The test now asserts watch delivery does not run/deliver resume hook context or hook user messages, then the later real user input receives the context exactly once and emits the user message exactly once.
- `agent/session.go`
  - Added package-private test-only `pendingSessionStartWaitEntered` instrumentation for deterministic race-test rendezvous.
- `agent/session_init.go`
  - Made `pendingSessionStartForUserTurn` context-aware while waiting for in-flight restore hook execution. Cancellation wakes the condition wait and returns without clearing pending kind/result/in-flight state.
  - Removed unused generic pending `SessionStart` drain/take helpers after `rg` confirmed no callers, avoiding future accidental autonomous-entry drains.
- `agent/job_delegate_send_test.go`
  - Strengthened `TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce` with a deterministic wait-entry barrier. The test now proves the first delegate user turn reached the pending restore-hook wait before the FIFO hook release.

## Review findings fixed/evaluated

### Critical finding

Fixed. Deferred resume `SessionStart` model-facing output is now drained only for accepted real `EntryUserInput`. `EntryWatchDelivery` no longer falls through to a drain-capable path, so watch/autonomous turns do not consume or deliver resume hook context/user messages. A later real user input receives the pending output exactly once.

### Important finding: deterministic race proof

Fixed. The delegate reconstruction race test no longer relies on only observing that the hook is blocked. It installs test-only instrumentation on the retained child and waits until the user turn has entered the pending `SessionStart` wait path before releasing the blocked restore hook.

### Important finding: context cancellation while waiting

Fixed cleanly. `pendingSessionStartForUserTurn(ctx)` now uses `context.AfterFunc` to broadcast the condition variable on cancellation. If cancellation occurs while the first user turn waits for in-flight restore hook execution, it returns without clearing pending output. Restore completion still stores/broadcasts the captured result for a later accepted user turn.

### Minor finding

Fixed. Removed unused generic pending `SessionStart` drain/take helpers after verifying with `rg` that they had no callers.

## Tests run

1. New watch-delivery regression:

```bash
go test ./agent -run 'TestRestoreSessionWatchDeliveryDoesNotDrainResumeSessionStartHooks' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.051s
```

2. Strengthened racing delegate reconstruction regression:

```bash
go test ./agent -run TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.187s
```

3. Task 4 resume-hook regressions, including MaxTurns:

```bash
go test ./agent -run 'TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput|TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks|TestRestoreSessionStartHooksDrainOnlyOnce|TestRestoreSessionMaxTurnsRejectedUserInputKeepsPendingResumeHooks|TestRestoreSessionRejectedUserInputKeepsPendingResumeHooks' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.099s
```

4. Core focused rerun after removing unused helpers:

```bash
go test ./agent -run 'TestRestoreSessionWatchDeliveryDoesNotDrainResumeSessionStartHooks|TestConcurrentDelegateReconstructionRunsRestoreSideEffectsOnce|TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput|TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks|TestRestoreSessionStartHooksDrainOnlyOnce|TestRestoreSessionMaxTurnsRejectedUserInputKeepsPendingResumeHooks|TestRestoreSessionRejectedUserInputKeepsPendingResumeHooks' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.240s
```

5. Affected watch/delegate tests:

```bash
go test ./agent -run 'TestWatchOriginCommunicateEndTurnResumesParentOnce|TestSendDelegateMessage|TestDelegateReconstruction' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	1.301s
```

6. Affected session lifecycle/resume tests:

```bash
go test ./agent -run 'TestRestoreSession|TestSession_Notification|TestSession_PreCompactHook|TestSession_Compact' -count=1
```

Result: PASS

```text
ok  	primeradiant.com/serf/agent	0.153s
```

7. Diff whitespace check:

```bash
git diff --check
```

Result: PASS, exit 0.

## Notes

- The new watch regression was verified RED before the production fix: `go test ./agent -run TestRestoreSessionWatchDeliveryDoesNotDrainResumeSessionStartHooks -count=1` failed because the watch-delivery request contained `<SYSTEM-REMINDER>RESUME_HOOK_CONTEXT</SYSTEM-REMINDER>`.
- The pre-existing unrelated `.superpowers/sdd/task-1-report.md` modification remains unstaged and uncommitted.

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

# Task 4 Report: Deterministic resume hook ordering regressions

Status: DONE_WITH_CONCERNS

## Files changed

- `agent/session_resume_hooks_test.go`
  - Added deterministic regression tests:
    - `TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput`
    - `TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks`
    - `TestRestoreSessionStartHooksDrainOnlyOnce`
    - `TestRestoreSessionRejectedUserInputKeepsPendingResumeHooks`
  - Added real plugin/session/LLM helper plumbing using `fakeAdapter`, `RestoreSessionFromMetaWithConfig`, `collectEvents`, and real hook start event counting.
  - Adjusted the brief's manifest snippet to match the actual plugin loader: `.claude-plugin/plugin.json` with nested handler array under `hooks`. The brief's `.claude-plugin.json` path and direct handler fields did not compile/pass against current loader semantics.

- `agent/session_lifecycle.go`
  - Minimal production ordering fix required by the new regressions: pending resume `SessionStart` hooks are still drained only from `acceptUserInput`, but now drain before the accepted user turn is appended so model-facing resume hook context precedes the first resumed user prompt in the model request.
  - This production change was required because the planned ordering assertions failed with current Tasks 1-3 code: the first model request contained the accepted user prompt before `RESUME_HOOK_CONTEXT`, contrary to the Task 4 expected ordering.

## Commit hash(es)

- `40f72157d52a6379d680ad37ca7ef4166a5734d0` — `test(agent): cover deferred resume session start hooks`

## Tests run with output summary

### Initial focused command after adding tests

Command:

```bash
go test ./agent -run 'TestRestoreSession(DefersResumeSessionStartHooksUntilUserInput|NotificationDoesNotDrainResumeSessionStartHooks|StartHooksDrainOnlyOnce|RejectedUserInputKeepsPendingResumeHooks)' -count=1
```

Output summary:

```text
ok  	primeradiant.com/serf/agent	0.006s [no tests to run]
```

Reason: run from the parent session working directory rather than the isolated worktree before correcting command directory.

### Focused run after creating tests in isolated worktree

Command:

```bash
cd /home/jesse/git/prime-radiant/serf/.worktrees/resume-hook-ordering && gofmt -w agent/session_resume_hooks_test.go && go test ./agent -run 'TestRestoreSession(DefersResumeSessionStartHooksUntilUserInput|NotificationDoesNotDrainResumeSessionStartHooks|StartHooksDrainOnlyOnce|RejectedUserInputKeepsPendingResumeHooks)' -count=1 -v
```

Output summary:

```text
FAIL: plugin initialization: reading plugin manifest ".../.codex-plugin/plugin.json": no such file or directory
```

Fix: adjusted test helper to write `.claude-plugin/plugin.json` instead of `.claude-plugin.json`, and to use the current nested hook handler schema.

### Focused run after manifest/schema test-helper fix

Command:

```bash
cd /home/jesse/git/prime-radiant/serf/.worktrees/resume-hook-ordering && gofmt -w agent/session_resume_hooks_test.go && go test ./agent -run 'TestRestoreSession(DefersResumeSessionStartHooksUntilUserInput|NotificationDoesNotDrainResumeSessionStartHooks|StartHooksDrainOnlyOnce|RejectedUserInputKeepsPendingResumeHooks)' -count=1 -v
```

Output summary:

```text
FAIL: request text missing current/first/accepted user after RESUME_HOOK_CONTEXT
```

Root cause: Tasks 1-3 drained resume hooks after appending the accepted user turn, so hook context appeared after the user prompt in the first model request.

Fix: moved pending resume hook drain and immediate steering append before appending the accepted user turn inside `acceptUserInput`.

### Final focused regression run

Command:

```bash
cd /home/jesse/git/prime-radiant/serf/.worktrees/resume-hook-ordering && go test ./agent -run 'TestRestoreSession(DefersResumeSessionStartHooksUntilUserInput|NotificationDoesNotDrainResumeSessionStartHooks|StartHooksDrainOnlyOnce|RejectedUserInputKeepsPendingResumeHooks)' -count=1 -v
```

Output summary:

```text
--- PASS: TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks (0.05s)
--- PASS: TestRestoreSessionRejectedUserInputKeepsPendingResumeHooks (0.05s)
--- PASS: TestRestoreSessionStartHooksDrainOnlyOnce (0.05s)
--- PASS: TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput (0.05s)
PASS
ok  	primeradiant.com/serf/agent	0.061s
```

### Self-review checks

Command:

```bash
cd /home/jesse/git/prime-radiant/serf/.worktrees/resume-hook-ordering && git diff --check -- agent/session_resume_hooks_test.go agent/session_lifecycle.go && git status --short
```

Output summary:

```text
 M .superpowers/sdd/task-1-report.md
 M agent/session_lifecycle.go
?? agent/session_resume_hooks_test.go
```

No `git diff --check` output; exit code 0. Unrelated dirty file remained unstaged.

## Self-review notes

- Used required `superpowers:test-driven-development` skill before changing code.
- Added tests before production changes and observed failures:
  - First failure validated that the brief snippet needed codebase-correct manifest path/schema changes.
  - Second failure validated the ordering regression the tests were meant to catch.
- Tests use real Serf plumbing and real hook start event counting via emitted events; no stubbed hook counts.
- Production change is minimal and scoped to ordering within `acceptUserInput`; no autonomous notification/continuation drain path was added.
- Staged and committed only `agent/session_resume_hooks_test.go` and `agent/session_lifecycle.go`.

## Concerns

- DONE_WITH_CONCERNS because production code did require a minimal change to satisfy the planned Task 4 ordering tests, despite Task 4 primarily requesting tests. The reason is documented above: Tasks 1-3 drained in `acceptUserInput`, but after appending the user turn, so the required request order failed.
- The brief's helper snippet used a plugin manifest location/schema that did not match the actual current loader. I made the minimal codebase-correct adjustment and documented it.
- Pre-existing unrelated modified file remains: `.superpowers/sdd/task-1-report.md`. I did not stage or commit it.

## Task 4 Fix Report

Status: DONE

### Files changed

- `agent/session_lifecycle.go`
  - Moved the `MaxTurns` proceed gate to run before any pending resume `SessionStart` hook drain or user prompt append.
  - Kept ordinary pending steering drain in its original post-turn-count location, after `UserPromptSubmit` hooks and immediately before the first LLM call.
  - Drains pending resume hook output only after the user turn is accepted, before appending the accepted user prompt, preserving model-facing order: restored history, resume hook context, accepted user prompt.

- `agent/session_init.go`
  - Split SessionStart hook execution from result delivery so the accepted-user path can append resume hook model context directly before the user prompt without draining the general steering queue early.
  - Preserved existing startup/other SessionStart delivery behavior through `deliverSessionStartHookResult`.

- `agent/session_resume_hooks_test.go`
  - Added `TestRestoreSessionMaxTurnsRejectedUserInputKeepsPendingResumeHooks`, a deterministic regression for the reviewed MaxTurns rejection path.

### Commit hash(es)

- Previous Task 4 commit under review: `40f72157d52a6379d680ad37ca7ef4166a5734d0` — `test(agent): cover deferred resume session start hooks`
- Fix commit: pending at time of report append; see final response for committed hash.

### Tests run with output summaries

Command:

```bash
go test ./agent -run 'TestRestoreSession(DefersResumeSessionStartHooksUntilUserInput|NotificationDoesNotDrainResumeSessionStartHooks|StartHooksDrainOnlyOnce|RejectedUserInputKeepsPendingResumeHooks|MaxTurnsRejectedUserInputKeepsPendingResumeHooks)' -count=1 -v
```

Output summary:

```text
--- PASS: TestRestoreSessionMaxTurnsRejectedUserInputKeepsPendingResumeHooks (0.06s)
--- PASS: TestRestoreSessionDefersResumeSessionStartHooksUntilUserInput (0.06s)
--- PASS: TestRestoreSessionRejectedUserInputKeepsPendingResumeHooks (0.06s)
--- PASS: TestRestoreSessionStartHooksDrainOnlyOnce (0.06s)
--- PASS: TestRestoreSessionNotificationDoesNotDrainResumeSessionStartHooks (0.06s)
PASS
ok  	primeradiant.com/serf/agent	0.072s
```

Command:

```bash
go test ./agent -run 'TestSession_MaxTurns_StopsAcrossInputsAndEmitsEvent|TestMaxTurns_CountsConversationTurns|TestSession_MaxTurns_SetsStateToIdle' -count=1 -v
```

Output summary:

```text
--- PASS: TestSession_MaxTurns_StopsAcrossInputsAndEmitsEvent (0.03s)
--- PASS: TestSession_MaxTurns_SetsStateToIdle (0.03s)
--- PASS: TestMaxTurns_CountsConversationTurns (0.03s)
PASS
ok  	primeradiant.com/serf/agent	0.055s
```

### Review findings fixed

1. **MaxTurns-rejected input consumed pending resume hooks before returning false**
   - Fixed by checking `s.cfg.MaxTurns > 0 && turns >= s.cfg.MaxTurns` before draining pending resume `SessionStart` hooks or appending the user prompt.
   - Added `TestRestoreSessionMaxTurnsRejectedUserInputKeepsPendingResumeHooks`: it restores with pending resume hook state, forces the first attempted user input to be rejected by MaxTurns with zero model requests and no hook/prompt history append, then disables the cap and verifies the next accepted input receives `RESUME_HOOK_CONTEXT` exactly once.

2. **Ordinary pending steering drain moved earlier than before**
   - Fixed by removing the early general `drainSteeringForTurn` call and keeping ordinary steering drain at the original post-turn-count/pre-LLM location.
   - Resume hook context is inserted in the accepted-user path via `drainPendingSessionStartHooksForUserTurn`, which runs only the pending resume hook and appends its model context directly before the accepted prompt; it does not drain unrelated steering queued before the turn.

### Concerns

- Pre-existing unrelated modified file remains: `.superpowers/sdd/task-1-report.md`. It was not staged by this fix workflow.

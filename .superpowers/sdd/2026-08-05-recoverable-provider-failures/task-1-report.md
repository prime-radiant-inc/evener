# Task 1 Report: Recover the agent session after provider failure

## Status

DONE_WITH_CONCERNS

## Summary

Implemented the Task 1 lifecycle core so terminal provider failures fail the current turn, block an active goal, clear active provenance at the idle boundary, and leave the session open in `SessionIdle` for later input. The regression proves a second `ProcessInput` succeeds on the same session after a non-retryable HTTP 403 provider error.

Before changing production code, I inspected the outer error path in `agent/session_lifecycle.go:686-722`. It already calls `terminateGoalOnError` and `finishProcessingAtBoundary(..., SessionIdle)` for non-cancellation errors. Therefore, the smallest correct change was to remove the inner non-retryable close path from `handleModelError` and rely on the existing outer owner, avoiding duplicate goal termination and boundary handling.

The new event test drains `Session.Events()` in a goroutine, closes the session only after the successful second operation, and waits on the drain goroutine's `done` channel before asserting event counts. This prevents event draining from racing assertions.

## Files Changed

Committed Task 1 files:

- `agent/session_model_call.go`
- `agent/session_model_test.go`
- `agent/session_provenance_test.go`
- `agent/fuzz_mc_classify_model_error_test.go`
- `agent/lifecycle_seqfuzz_test.go`
- `agent/session_goal.go`

Required report written after the code commit:

- `.superpowers/sdd/2026-08-05-recoverable-provider-failures/task-1-report.md`

## TDD Evidence

Invoked `superpowers:test-driven-development` before modification and followed red-green-refactor.

### RED

Initial focused run after adding the tests:

```sh
gofmt -w agent/session_model_test.go agent/session_provenance_test.go && go test ./agent -run 'TestSession_NonRetryableProviderErrorLeavesSessionIdle|TestNonRetryableModelErrorClearsActiveProvenanceAtIdleBoundary' -count=1
```

Result: **FAIL**. The provenance regression failed because state was `closed`; the provider regression initially exposed a test-construction mismatch (`NewOpenAIProfile` selected provider `openai` while the required adapter was registered as `kimi-anthropic`). I reconciled the brief's sample with repository APIs using the concrete profile construction:

```go
WithProviderID(newKimiAnthropicProfile("k3"), "kimi-anthropic")
```

Then reran the focused command:

```sh
gofmt -w agent/session_model_test.go && go test ./agent -run 'TestSession_NonRetryableProviderErrorLeavesSessionIdle|TestNonRetryableModelErrorClearsActiveProvenanceAtIdleBoundary' -count=1
```

Result: **FAIL as expected**. Both tests reported state `"closed"`, want `"idle"`.

### GREEN

After the minimal lifecycle implementation:

```sh
gofmt -w agent/session_model_call.go agent/session_goal.go && go test ./agent -run 'TestSession_NonRetryableProviderErrorLeavesSessionIdle|TestNonRetryableModelErrorClearsActiveProvenanceAtIdleBoundary' -count=1
```

Result: **PASS** (`ok primeradiant.com/serf/agent 0.550s`).

## Exact Verification Commands and Results

Required focused/unit and tagged deterministic fuzz gates, final run after self-review:

```sh
gofmt -w agent/session_model_call.go agent/session_model_test.go agent/session_provenance_test.go agent/fuzz_mc_classify_model_error_test.go agent/lifecycle_seqfuzz_test.go agent/session_goal.go
go test ./agent -run 'TestSession_NonRetryableProviderErrorLeavesSessionIdle|TestSession_ProvideErrorReturnsErrorToCaller|TestProviderErrorEmitsStructuredCause|TestNonRetryableModelErrorClearsActiveProvenanceAtIdleBoundary|TestGoalErrorBlockIsPersisted' -count=1
go test -tags serffuzz ./agent -run '^FuzzMcClassifyModelError$' -count=1
git diff --check
```

Results:

- Focused five-test agent gate: **PASS** (`ok primeradiant.com/serf/agent 0.576s`)
- Tagged classifier fuzz seed gate: **PASS** (`ok primeradiant.com/serf/agent 0.476s`)
- `git diff --check`: **PASS**, no output

An earlier complete required-gate run also passed (`0.461s` and `0.396s` respectively).

## Commit

- `ff859dbbe765a0cd3443dde889343505bb72be04` — `fix: keep sessions open after provider failures`

Only the six planned agent files were included in this commit.

## Self-Review

- Confirmed `handleModelError` no longer calculates provider retryability or returns a close decision.
- Confirmed `classifyModelError` has exactly four inputs and `modelErrorDecision` contains only `Action` and `EmitContextLenWarn`.
- Confirmed cancellation and one-time content-filter compaction/retry behavior are unchanged.
- Confirmed terminal provider errors retain `fmt.Errorf("provider error: %w", err)` and preserve `errors.As` access to `llm.Error`.
- Confirmed the existing outer lifecycle path owns goal blocking and transition to idle; no duplicate `terminateGoalOnError`, provenance completion, or idle-boundary call remains in `handleModelError`.
- Confirmed the active goal becomes `goal.StatusBlocked`, the failed turn emits error/turn-ended events, and a second input succeeds before the session is closed.
- Confirmed event assertions wait until `sess.Close()` closes the events channel and the drain goroutine signals completion.
- Confirmed provenance checks now assert the idle boundary and no longer use stale closed-session wording.
- Confirmed lifecycle fuzz comments treat auth faults as terminal turns, while `lifecycleModel.closed` remains driven only by observed `SessionClosed` (which comes from actual close behavior).
- Searched the affected classifier/lifecycle tests for stale `CloseSession`, `llmErrNonRetryable`, `non-retryable close`, and closed-failed-turn semantics; no matches remained.
- `git status --short` was clean immediately after the code commit; this report is intentionally written afterward and is not part of the Task 1 code commit.

## Concerns

- During `git commit`, Git printed: `Unable to create .../.git/packed-refs.lock: Operation not permitted`, but the command exited 0, created commit `ff859dbbe765a0cd3443dde889343505bb72be04`, and the commit contents/hash were verified afterward. This appears to be an environment-level packed-refs maintenance warning rather than a failed commit.
- No functional or test concerns remain.

## Artifact/Scratch Paths

- Report: `/Users/jesse/.local/state/serf/projects/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa/worktrees/Users-jesse-prime-radiant-toil-suite-serf-uo4YId7isa/recoverable-provider-failures/.superpowers/sdd/2026-08-05-recoverable-provider-failures/task-1-report.md`
- Session scratch directory (no retained scratch artifact created): `/private/var/folders/g6/_sjng8h14gs3xt6c7t72w0180000gn/T/serf-sandbox-1999864929`

## Fix: provider-terminal recovery ownership

### Files changed

- `agent/session_model_call.go`: terminal `handleModelError` now terminates the
  active goal and settles the open session at idle before returning the wrapped
  provider error. Added a narrow marker predicate for the outer boundary.
- `agent/session_lifecycle.go`: the generic error tail skips only the
  provider-terminal wrapper, preventing duplicate goal reports and provenance
  effects while retaining non-provider and budget-exhaustion handling.
- `agent/session_model_test.go`: provider recovery now requires exactly one
  blocked goal-ended report; non-provider failure coverage now verifies idle
  state and blocked-goal behavior.

### Verification

```sh
gofmt -w agent/session_model_call.go agent/session_model_test.go agent/session_provenance_test.go agent/fuzz_mc_classify_model_error_test.go agent/lifecycle_seqfuzz_test.go agent/session_goal.go
go test ./agent -run 'TestSession_NonRetryableProviderErrorLeavesSessionIdle|TestSession_ProvideErrorReturnsErrorToCaller|TestProviderErrorEmitsStructuredCause|TestNonProviderErrorOmitsCause|TestNonRetryableModelErrorClearsActiveProvenanceAtIdleBoundary|TestGoalErrorBlockIsPersisted' -count=1
go test -tags serffuzz ./agent -run '^FuzzMcClassifyModelError$' -count=1
```

Results: all commands passed. The focused agent gate reported `ok
primeradiant.com/serf/agent 0.449s`; the tagged classifier fuzz seed gate reported
`ok primeradiant.com/serf/agent 0.517s`; `git diff --check` produced no output.

### Self-review

- Provider terminal handling now owns goal termination and the idle boundary;
  the outer path recognizes only the `provider error: ` wrapper containing an
  `llm.Error` and skips those two effects.
- Cancellation remains on the existing raw-error path; non-provider errors and
  budget exhaustion retain the generic outer lifecycle behavior.
- Failed-turn projection and `%w` provider wrapping remain unchanged.
- The actual 403 provider regression proves one blocked goal-ended report, idle
  state, and successful recovery on a second input; non-provider coverage proves
  its goal and idle behavior remains intact.
- `systematic-debugging` was requested but unavailable in this environment
  (`skill "systematic-debugging" not found`); the fix instead traced the model
  call and outer lifecycle paths before changing ownership.

# Final fix report — delegate_send always wakes

Date: 2026-08-04
Starting HEAD: `fdc138a50`

## Summary

Addressed the final whole-branch review findings:

- Removed retained internal `OnIdle` plumbing from `sendMessageArgs` and updated legitimate internal constructions/assertions accordingly.
- Removed stale shipped `on_idle` / `target_idle` references from shipped comments, prompts, probe fixtures, and scenario docs while leaving unrelated `settleGoalOnIdle` and historical SDD/review artifacts intact.
- Diagnosed the earlier verification-timeout claim with isolated Go caches: the focused command does not hang under isolated `GOCACHE` / `GOTMPDIR` / `GOFLAGS=-mod=readonly`; it finishes quickly and surfaces real failures when present.
- Fixed one additional regression exposed during verification: runtime-lost delegates should not preflight-resume before retained-runtime fallback logic decides whether a retained child can still resume.
- Fixed the retained-running fallback regression test so it models a resumable child correctly and asserts the resumed run's actual output.

## Files changed

### Runtime / tests

- `agent/job_delegate.go`
  - Removed `OnIdle` from `sendMessageArgs`.
  - Deferred runtime-lost resumability preflight until the restore path (`sub == nil || sub.sess == nil`) so retained-runtime fallback still works for a tracked child whose running-job lookup fails.
- `agent/job_watch.go`
  - Removed internal watch-send construction of `OnIdle: "start"`.
- `agent/job_watch_observer_test.go`
  - Removed assertion on `sends[0].OnIdle`.
- `agent/job_watch_send_test.go`
- `agent/job_delegate_send_test.go`
- `agent/job_delegate_isolation_test.go`
- `agent/job_delegate_budget_test.go`
- `agent/job_delegate_model_selection_test.go`
- `agent/job_delegate_create_test.go`
- `agent/job_delegate_exact_create_send_fuzz_test.go`
- `agent/tree_counter_test.go`
- `agent/root_watch_tree_program_fuzz_test.go`
- `agent/fuzz_jdr_restore_lifecycle_test.go`
- `agent/dld_e2e_test.go`
- `agent/cov_w3dlg_send_test.go`
  - Removed obsolete `OnIdle: "start"` constructions.
  - Updated retained-running fallback coverage to use a resumable delegate test session and to assert the resumed run's actual completion text.

### Shipped docs / comments / fixtures

- `agent/internal/worktree/lockstate.go`
- `tools/tool-fluency/probes/delegate_send.yaml`
- `internal/bundled/plugins/coordinator-workflow/agents/coordinator.md`
- `test/scenarios/job-send-message-surface.md`
- `test/scenarios/job-watch-passive-observer-noop-filter.md`
- `test/scenarios/subagent-cancel-runaway.md`
- `test/scenarios/job-delegate-result-schema.md`
- `test/scenarios/INDEX.md`

## Line-level evidence

- `agent/job_delegate.go:234-241`
  - `sendMessageArgs` no longer has an `OnIdle` field.
- `agent/job_watch.go:3438-3445`
  - watch-delivery sends now construct `sendMessageArgs` without `OnIdle`.
- `agent/job_watch_observer_test.go:167-171`
  - observer assertion now checks watch/background flags only.
- `agent/cov_w3dlg_send_test.go:75-132`
  - retained-running lookup-failure coverage now resumes successfully with a resumable child session and asserts `resumed complete`.
- `agent/internal/worktree/lockstate.go:79-80`
  - stale worktree comment updated from ``delegate_send(on_idle:"start")`` to ``delegate_send(...)``.
- `tools/tool-fluency/probes/delegate_send.yaml:5-7`
  - probe prompt no longer instructs `on_idle="start"`.
- `internal/bundled/plugins/coordinator-workflow/agents/coordinator.md:67-74`
  - bundled coordinator prompt now states that plain `delegate_send` wakes/resumes an idle implementer.
- `test/scenarios/job-send-message-surface.md:1-68`
  - scenario now documents automatic idle resume instead of `target_idle` / explicit `on_idle` start.
- `test/scenarios/job-watch-passive-observer-noop-filter.md:55-60`
  - observer follow-up scenario no longer passes `on_idle`.
- `test/scenarios/subagent-cancel-runaway.md:17-35`
  - runaway follow-up scenario now uses plain `delegate_send`.
- `test/scenarios/job-delegate-result-schema.md:50-56`
  - schema-inheritance scenario no longer passes `on_idle`.
- `test/scenarios/INDEX.md:321-365`
  - scenario index entries now describe automatic idle restart.

## Verification environment

Commands were run with isolated Go caches and readonly module mode:

- `GOCACHE=/tmp/serf-delegate-send-verify.NCDYX8/gocache`
- `GOTMPDIR=/tmp/serf-delegate-send-verify.NCDYX8/gotmp`
- `GOFLAGS=-mod=readonly`

## Verification commands and exact outputs

### 1) Timeout diagnosis — initial isolated reproduction

Command:

```sh
timeout 20s go test ./agent -run '^TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume$' -count=1 -v
```

Observed output:

```text
=== RUN   TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
=== PAUSE TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
=== CONT  TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
    cov_w3dlg_send_test.go:119: sendDelegateMessage returned error: target_not_resumable:child_session_busy
--- FAIL: TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume (0.08s)
FAIL
FAIL	primeradiant.com/serf/agent	0.581s
FAIL
```

Exit status: `1`

Diagnosis:

- Under isolated caches, the command did **not** hang or reach the 20-second timeout. It completed quickly and exposed a real logic bug.
- Root cause: `sendDelegateMessage` was running runtime-lost resumability preflight too early. For a retained child marked `running` where lookup of the active job fails, the code should fall through to the retained-runtime path first; instead, preflight saw the tracked running child and rejected with `target_not_resumable:child_session_busy`.

### 2) After runtime fix — second isolated reproduction

Command:

```sh
go test ./agent -run '^TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume$' -count=1 -v
```

Observed output before final test adjustment:

```text
=== RUN   TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
=== PAUSE TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
=== CONT  TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
    cov_w3dlg_send_test.go:131: started record = &{... Status:completed ... StructuredResult:map[artifacts:[] data:map[] message:first complete] ...}, output should contain resumed completion
--- FAIL: TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume (0.09s)
FAIL
FAIL	primeradiant.com/serf/agent	0.570s
FAIL
```

Diagnosis:

- Again, no hang. The command completed quickly and exposed a test/setup problem.
- Root cause: the retained child used `newTestSession(t)`, which is a minimal session with its own no-op adapter and did not model the resumed-child path intended by the regression. The test also expected `second complete` / resumed output while the actual resumed child produced different output.
- Fix: switched the retained child to `newDelegateTestSession(t, c)` so the resumed delegate uses a resumable delegate-capable test session, and updated the expectation to match the resumed completion text actually driven by the test adapter.

### 3) Final timeout diagnosis result

Command:

```sh
timeout 20s go test ./agent -run '^TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume$' -count=1 -v
```

Observed output:

```text
=== RUN   TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
=== PAUSE TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
=== CONT  TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
--- PASS: TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume (0.08s)
PASS
ok  	primeradiant.com/serf/agent	0.572s
```

Timing wrapper output:

```text
real 11.04
user 44.80
sys 7.76
```

Interpretation:

- The earlier “timeout” behavior is not reproducible as a hang once caches are isolated and the actual failures are fixed.
- The command returns successfully; the issue was masked test/logic failure, not a need to increase timeouts or suppress verification.

### 4) Compile-only checks

Command:

```sh
go test ./agent -run '^$' -count=1
```

Output:

```text
ok  	primeradiant.com/serf/agent	0.377s [no tests to run]
real 1.48
user 1.78
sys 0.74
```

Command:

```sh
go test ./agent/internal/tool -run '^$' -count=1
```

Output:

```text
ok  	primeradiant.com/serf/agent/internal/tool	0.242s [no tests to run]
real 0.62
user 0.67
sys 0.61
```

### 5) Focused tool-definition check

Command:

```sh
go test ./agent/internal/tool -run '^TestDefDelegateSendShape$' -count=1 -v
```

Output:

```text
=== RUN   TestDefDelegateSendShape
--- PASS: TestDefDelegateSendShape (0.00s)
PASS
ok  	primeradiant.com/serf/agent/internal/tool	0.176s
real 0.39
user 0.28
sys 0.56
```

### 6) Focused delegate-send / watch / resume checks

Command:

```sh
go test ./agent -run '^(TestSendDelegateMessageTerminalDelegateResumeCreatesNewJob|TestDelegateResumeKeepsDelegateIDAndUpdatesLatestJob|TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume|TestWatchSendToResumedRunningDelegateSteersActiveRun|TestDrainDeliversDelegateTargetedSends|TestDelegateIsolation_SecondJobViaDelegateSendRunsInSameLaneAndReportsWorktree|TestDelegate_ToolRoundBudgetExhaustionIsDurableAndResumable)$' -count=1 -v
```

Output:

```text
=== RUN   TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
=== PAUSE TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
=== RUN   TestDelegate_ToolRoundBudgetExhaustionIsDurableAndResumable
--- PASS: TestDelegate_ToolRoundBudgetExhaustionIsDurableAndResumable (0.07s)
=== RUN   TestDelegateIsolation_SecondJobViaDelegateSendRunsInSameLaneAndReportsWorktree
=== PAUSE TestDelegateIsolation_SecondJobViaDelegateSendRunsInSameLaneAndReportsWorktree
=== RUN   TestSendDelegateMessageTerminalDelegateResumeCreatesNewJob
=== PAUSE TestSendDelegateMessageTerminalDelegateResumeCreatesNewJob
=== RUN   TestDelegateResumeKeepsDelegateIDAndUpdatesLatestJob
=== PAUSE TestDelegateResumeKeepsDelegateIDAndUpdatesLatestJob
=== RUN   TestWatchSendToResumedRunningDelegateSteersActiveRun
=== PAUSE TestWatchSendToResumedRunningDelegateSteersActiveRun
=== RUN   TestDrainDeliversDelegateTargetedSends
=== PAUSE TestDrainDeliversDelegateTargetedSends
=== CONT  TestDelegateResumeKeepsDelegateIDAndUpdatesLatestJob
=== CONT  TestDrainDeliversDelegateTargetedSends
=== CONT  TestWatchSendToResumedRunningDelegateSteersActiveRun
=== CONT  TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume
=== CONT  TestSendDelegateMessageTerminalDelegateResumeCreatesNewJob
=== CONT  TestDelegateIsolation_SecondJobViaDelegateSendRunsInSameLaneAndReportsWorktree
--- PASS: TestW3Dlg_SendTerminalRunningSubLookupFailureFallsThroughToResume (0.16s)
--- PASS: TestDelegateResumeKeepsDelegateIDAndUpdatesLatestJob (0.17s)
--- PASS: TestSendDelegateMessageTerminalDelegateResumeCreatesNewJob (0.18s)
--- PASS: TestDelegateIsolation_SecondJobViaDelegateSendRunsInSameLaneAndReportsWorktree (0.35s)
--- PASS: TestWatchSendToResumedRunningDelegateSteersActiveRun (0.38s)
--- PASS: TestDrainDeliversDelegateTargetedSends (0.38s)
PASS
ok  	primeradiant.com/serf/agent	0.834s
real 1.64
user 2.08
sys 1.16
```

## Remaining reference audit

After edits, remaining `on_idle` matches in the requested audit scope are intentional only:

- `agent/internal/tool/definitions_test.go:327-328`
  - negative assertion that the public schema must **not** expose `on_idle`
- `test/scenarios/tui-queue-then-completes.md:413,421,426`
  - unrelated shell variable `session_idle`, not delegate-send policy

No remaining shipped `target_idle`, `pass on_idle`, or `OnIdle` runtime plumbing remains in the audited delegate-send surfaces.

## Status before commit

- `git diff --check`: clean
- `git status --short`: modified tracked files only, matching the intended review-fix scope

## Concerns

- I did **not** run the broader repository gates (`go test ./agent/...`, `make lint`, `make test`) because the delegated task explicitly requested compile-only and focused verification for this fix turn.
- The time wrapper around `timeout 20s ...` reported `real 11.04` despite the test body finishing in ~0.57s; I recorded that exact output, but the key diagnostic fact is that the command exited `0` and did not hit the timeout boundary.

# Owned Managed Job Drain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every session-owned managed shell job keep a one-shot run or subagent run alive until its terminal notification has forced the owning agent through a final model turn, while preserving immediate disownership for detached processes.

**Architecture:** Generalize the existing `Session.DrainJobTree` delegate-only accounting with one owned-drain-job predicate covering delegate and shell records. Keep the current durable notification ledger, queue, wake, and `EntryNotification` turn path; do not add a second completion mechanism. The root CLI already invokes the drain after a successful turn. A subagent will invoke the same drain after all of its ordinary completion continuations and before it records a terminal result, so it remains live and non-reclaimable while owned work runs.

**Tech Stack:** Go 1.25, Evener session/job plumbing, append-only `jobstore`, scripted LLM providers, fake clocks, and controllable `execenv.StreamingExecutor` test doubles.

**Normative design:** `docs/superpowers/specs/2026-08-09-one-shot-background-job-drain-design.md`

## Global Constraints

- Read `docs/testing.md` before changing tests. Default tests must use scripted providers and deterministic local process doubles; credentials, network access, quota, and live model behavior are forbidden.
- Every job enrolled in `jobManager` drains: delegates, explicit background shells, and foreground shells promoted after their wait bound.
- `JobRecord.Background` is live-only metadata and must not participate in lifecycle decisions.
- Detached processes never enter `jobManager`, never notify a session, and never hold a drain open.
- A notification turn may launch more managed work. Quiescence must be checked again after every turn.
- A subagent remains `SubagentRunning` until its entire live session tree is quiescent. Do not add a retention-GC exception for a prematurely terminal subagent.
- Fatal root model errors are terminal, not idle: preserve the original error, skip the drain, and let normal one-shot shutdown stop owned work.
- Stop-gated descendants remain outside the live drain tree and must not be resurrected.
- Do not change interactive root-session behavior, shell execution modes, runtime limits, detached visibility, or job schemas.
- Do not add wording or rendered-command tests. Assert structured job types, statuses, notification delivery, model-turn counts, process signals, and final results.
- Use TDD for every production change: write a behavioral test, observe the intended failure, implement the smallest change, rerun the focused test, then commit.
- Run `gofmt` on touched Go files. Never hand-format unrelated code and never use `git add -A`.

## File Map

**Production code:**

- Modify: `agent/session_jobtree_drain.go` — shared drain-job eligibility, outstanding/live accounting, stall diagnostics, and durable-pending rematerialization.
- Modify: `agent/subagents.go` — keep a subagent run nonterminal while its owned job tree drains.
- Modify: `cmd/evener/run.go` — update the delegate-only comment to describe all managed jobs; the control flow remains unchanged.

**Behavioral tests:**

- Modify: `agent/session_jobtree_drain_test.go` — managed-shell accounting, foreground promotion, cancellation, durable notifications, batching, descendants, and stop gates.
- Modify: `agent/session_jobtree_drain_stall_test.go` — running shell liveness and generalized stall diagnostics.
- Create: `agent/subagent_owned_job_drain_test.go` — subagent final-result and retention-pressure contracts.
- Modify: `cmd/evener/scripted_provider_test.go` — reusable structured shell-call fixture.
- Modify: `cmd/evener/run_drain_test.go` — root one-shot success, failure, and chained-job cycles.
- Create: `cmd/evener/run_drain_error_test.go` — fatal model-error boundary.
- Re-run unchanged: `cmd/evener/run_detached_test.go` — detached survival and nonparticipation.

**Focused evaluation:**

- Read: `/Users/jesse/git/prime-radiant/harbor-runner/docs/superpowers/research/2026-08-09-codex-vs-evener-luna-trajectories.md` — evidence defining the five affected failures.
- Create: ignored run artifacts under `/Users/jesse/git/prime-radiant/harbor-runner/runs/` — five-task Luna max confirmation, never submitted.

---

### Task 1: Generalize drain accounting and liveness to every managed job

**Files:**

- Modify: `agent/session_jobtree_drain.go`
- Modify: `agent/session_jobtree_drain_test.go`
- Modify: `agent/session_jobtree_drain_stall_test.go`

**Interfaces:**

- Produce `isOwnedDrainJob(rec *jobstore.JobRecord, sessionID string) bool` as the single type/ownership predicate.
- Rename delegate-specific private helpers with no compatibility aliases:
  - `outstandingDelegateCount` → `outstandingDrainJobCount`
  - `outstandingDelegateIDs` → `outstandingDrainJobIDs`
  - `hasRunningDelegate` → `hasRunningDrainJob`
  - `subtreeOutstandingDelegateIDs` → `subtreeOutstandingDrainJobIDs`
- Preserve the current atomicity rule: load the durable snapshot and inspect `jm.running` under the same `jm.mu` hold.

- [ ] **Step 1: Replace the obsolete shell-ignore test with managed-job accounting tests**

In `agent/session_jobtree_drain_test.go`, delete `TestDrainJobTreeIgnoresBackgroundShell`. Add a table-driven test that inserts each of these records into `jm.running` and expects `outstandingDrainJobCount() == 1`:

```go
tests := []struct {
	name string
	rec  *jobstore.JobRecord
}{
	{name: "delegate", rec: &jobstore.JobRecord{JobID: "del-live", Type: jobstore.JobDelegate, Status: jobstore.StatusRunning}},
	{name: "explicit background shell", rec: &jobstore.JobRecord{JobID: "sh-bg", Type: jobstore.JobShell, Status: jobstore.StatusRunning, Background: true}},
	{name: "foreground-promoted shell", rec: &jobstore.JobRecord{JobID: "sh-promoted", Type: jobstore.JobShell, Status: jobstore.StatusRunning, Background: false}},
}
```

For every case, set `OwnerSessionID` to the session ID before insertion. Also retain the forwarded-descendant assertion, renamed to `TestOutstandingDrainJobCountIgnoresForwardedDescendantPending`, and run it once for `JobDelegate` and once for `JobShell`.

- [ ] **Step 2: Extend cancellation coverage to a running managed shell**

Convert `TestDrainJobTreeReturnsOnContextCancel` into a delegate/shell table. Each case inserts one owned running record, starts `DrainJobTree`, cancels a caller-owned context after the test has confirmed `outstandingDrainJobCount() == 1`, and asserts `errors.Is(err, context.Canceled)`. Use cancellation as the causal release; the timeout is only a five-second deadlock tripwire.

- [ ] **Step 3: Add the running-shell stall-watchdog regression**

In `agent/session_jobtree_drain_stall_test.go`, table-drive `TestDrainStallWatchdogSparesRunningDrainJob` over `JobDelegate` and `JobShell`. For each type, insert an owned running record, assert `drainSubtreeIsStalled() == false`, advance the fake clock to ten times `drainStallTimeout`, and use `assertDrainNotCut` to prove the drain stays alive and emits no warning.

- [ ] **Step 4: Run the focused tests and observe the shell cases fail**

```bash
go test ./agent -run 'Test(OutstandingDrainJob|DrainJobTreeReturnsOnContextCancel|DrainStallWatchdogSparesRunningDrainJob)' -count=1
```

Expected: FAIL because the generalized helper names do not exist and the current implementation excludes `JobShell`.

- [ ] **Step 5: Implement one shared owned-drain-job predicate**

Add this helper near the current outstanding-count code:

```go
func isOwnedDrainJob(rec *jobstore.JobRecord, sessionID string) bool {
	if rec == nil {
		return false
	}
	if rec.OwnerSessionID != "" && rec.OwnerSessionID != sessionID {
		return false
	}
	switch rec.Type {
	case jobstore.JobDelegate, jobstore.JobShell:
		return true
	default:
		return false
	}
}
```

Use it in both halves of `outstandingDrainJobIDs`: the live `jm.running` scan and the durable `NotifyPending` scan. Use it again in `hasRunningDrainJob`. Do not inspect `rec.Background` anywhere.

Update `treeHasOutstandingWork`, `subtreeHasLiveComponent`, the recursive ID collector, comments, and the stall warning to speak about managed jobs rather than delegates. The warning must still include the outstanding IDs, but tests must not pin its prose.

- [ ] **Step 6: Run the focused and complete drain suites**

```bash
gofmt -w agent/session_jobtree_drain.go agent/session_jobtree_drain_test.go agent/session_jobtree_drain_stall_test.go
go test ./agent -run 'Test(OutstandingDrainJob|DrainJobTree|DrainStallWatchdog)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit the generalized accounting**

```bash
git status --short
git add agent/session_jobtree_drain.go agent/session_jobtree_drain_test.go agent/session_jobtree_drain_stall_test.go
git commit -m "fix(drain): include all managed jobs in liveness"
```

The commit body must explain why `JobRecord.Background` is unusable after durable reload and why the shared predicate is used by both quiescence and stall detection.

---

### Task 2: Rematerialize and deliver durable shell notifications exactly once

**Files:**

- Modify: `agent/session_jobtree_drain.go`
- Modify: `agent/session_jobtree_drain_test.go`
- Modify: `agent/session_jobtree_drain_stall_test.go`

**Interfaces:**

- Generalize the existing durable-pending test fixture to accept a `jobstore.JobType`.
- Make `rematerializeDurablePendings` select records with `isOwnedDrainJob` so its deliverable set exactly matches outstanding accounting.
- Preserve existing already-injected deduplication and descendant drive-down behavior.

- [ ] **Step 1: Generalize the durable-pending fixture**

Replace `seedDurableOnlyPending` with this shared package test helper:

```go
func seedOwnedDurablePending(t *testing.T, jm *jobManager, jobID string, jobType jobstore.JobType) {
	t.Helper()
	started := frozenTestTime.Add(-time.Second)
	ended := frozenTestTime
	reason := "communicated"
	if jobType == jobstore.JobShell {
		reason = "exit_zero"
	}
	for _, ev := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobType, OwnerSessionID: jm.sessionID, VisibleToSession: jm.sessionID, StartedAt: &started},
		{Kind: jobstore.EventJobFinished, TS: ended, JobID: jobID, Status: jobstore.StatusCompleted, Reason: reason, EndedAt: &ended, TerminalGen: "gen-" + jobID},
		{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: jobID, TerminalGen: "gen-" + jobID},
	} {
		if err := jm.appendEvent(ev); err != nil {
			t.Fatalf("append %s: %v", ev.Kind, err)
		}
	}
}
```

Use it from both drain test files.

- [ ] **Step 2: Write a failing selection test for durable shell pendings**

Add `TestRematerializeOwnedDrainJobPendings`. Seed an owned pending delegate and shell, plus a shell whose `OwnerSessionID` is a different child session. Call `rematerializeDurablePendings` and assert the in-memory queue contains exactly the two owned jobs. Assert job IDs and types structurally; do not match rendered XML.

- [ ] **Step 3: Run the selection test and observe the shell omission**

```bash
go test ./agent -run TestRematerializeOwnedDrainJobPendings -count=1
```

Expected: FAIL because only the delegate is enqueued.

- [ ] **Step 4: Generalize durable-pending rematerialization**

Replace the delegate-only/type-and-owner checks in `rematerializeDurablePendings` with:

```go
if !isOwnedDrainJob(rec, jm.sessionID) {
	continue
}
if rec.NotifyState != jobstore.NotifyPending || rec.TerminalGen == "" {
	continue
}
```

Keep the empty-queue guard and enqueue through `jobNotificationFromRecord(rec)`. Do not append a second durable event and do not create a shell-specific notification path.

- [ ] **Step 5: Extend the existing durable delivery tests across job types**

Table-drive the following existing contracts over `JobDelegate` and `JobShell`:

- root durable-only pending reaches a notification turn and settles to `NotifyDelivered`;
- already-injected pending settles without appending a duplicate history block;
- a live descendant's durable-only pending is rematerialized and driven in the descendant session;
- the genuine-stall watchdog test remains valid when its self-heal kick is deliberately disabled.

Use distinct job IDs per table case. For the already-injected case, insert one minimal `<job-notification>` steering block containing that job ID; assert only the before/after block count, not wording.

- [ ] **Step 6: Add a batching behavior test**

Add `TestDrainJobTreeBatchesQueuedShellNotifications`. Seed two owned terminal shell records with pending notifications before calling `DrainJobTree`. Use a captured `fakeAdapter` that returns `finalResponse("batch handled")`. Assert:

- exactly one model request was made;
- that request contains both job IDs in notification content;
- both records finish with `NotifyDelivered`;
- `DrainJobTree` returns `"batch handled"`.

- [ ] **Step 7: Run the durable and stall suites**

```bash
gofmt -w agent/session_jobtree_drain.go agent/session_jobtree_drain_test.go agent/session_jobtree_drain_stall_test.go
go test ./agent -run 'Test(RematerializeOwnedDrainJobPendings|DrainSettles|DrainJobTreeBatches|DrainStallWatchdog)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit durable shell delivery**

```bash
git status --short
git add agent/session_jobtree_drain.go agent/session_jobtree_drain_test.go agent/session_jobtree_drain_stall_test.go
git commit -m "fix(drain): restore pending shell notifications"
```

The commit body must record that the durable ledger and the in-memory delivery queue now use the same eligibility predicate.

---

### Task 3: Prove one-shot roots run every required shell notification turn

**Files:**

- Modify: `cmd/evener/scripted_provider_test.go`
- Modify: `cmd/evener/run_drain_test.go`
- Modify: `cmd/evener/run.go`
- Modify: `agent/session_jobtree_drain_test.go`

- [ ] **Step 1: Add a structured shell-call fixture**

In `cmd/evener/scripted_provider_test.go`, add:

```go
func scriptedShellCall(id, command, mode string) llm.ToolCallData {
	args, _ := json.Marshal(map[string]any{
		"command": command,
		"mode":    mode,
	})
	return llm.ToolCallData{ID: id, Name: "shell", Arguments: args, Type: "function"}
}
```

This helper builds structured tool arguments; tests must not match a rendered shell script.

- [ ] **Step 2: Add root one-shot success and failure cases**

Add `TestRunDrainsManagedShellBeforeExit` as a table with exactly these cases:

```go
tests := []struct {
	name       string
	command    string
	wantStatus string
}{
	{name: "completed", command: "printf shell-ok", wantStatus: "completed"},
	{name: "failed", command: "printf shell-failed >&2; exit 7", wantStatus: "failed"},
}
```

For each case, the scripted provider must:

1. return a `shell` call with `mode: "background"`;
2. after the tool result, communicate `"waiting for shell"`;
3. only after a `<job-notification>` request, communicate `"shell notification handled"`.

Assert the one-shot `run` result prints the third message, the provider saw exactly three requests, and the third request contains a shell notification with the expected structured terminal status.

- [ ] **Step 3: Run the root cases**

```bash
go test ./cmd/evener -run TestRunDrainsManagedShellBeforeExit -count=1
```

Expected after Tasks 1–2: PASS. On the pre-change baseline these cases returned `"waiting for shell"` after two requests.

- [ ] **Step 4: Add the chained-job regression**

Add `TestRunDrainContinuesWhenNotificationTurnStartsAnotherShell`. Use five scripted requests:

1. start background shell A;
2. communicate `"waiting for A"`;
3. after A's notification, start background shell B;
4. communicate `"waiting for B"`;
5. after B's notification, communicate `"all shell work complete"`.

Use `strings.Count(requestDeliveredText(req), "<job-notification")` to distinguish the A-only and A-plus-B requests. Counting over `requestFullText` instead would include the system prompt, so a prompt section naming the frame would shift every count by one (kata zzpw). Assert five requests, two distinct notification job IDs, and final stdout containing only the post-B completion result as the run's final answer.

- [ ] **Step 5: Add the foreground-promotion regression at the agent boundary**

In `agent/session_jobtree_drain_test.go`, create a session with `agenttest.NewFakeClock()` and run a foreground shell through `runShell` with `newDelayedSuccessStreamingExecutor`. Call `clk.BlockUntil(1)` so the foreground timer is registered, then advance the fake clock past the clamped foreground wait bound and assert:

- `runShell` returns a durable running job with reason `foreground_timeout`;
- the live record has `Background == false`;
- `outstandingDrainJobCount() == 1`;
- after releasing the executor, `DrainJobTree` runs one notification turn and returns its scripted final result;
- no drain jobs or queued notifications remain.

This is the regression that prevents a future implementation from treating the requested mode or live-only `Background` bit as lifecycle state.

- [ ] **Step 6: Update the one-shot comment without changing control flow**

In `cmd/evener/run.go`, change the comment above `runDrainJobTree` from fire-and-return delegates to all session-owned managed jobs. Keep the existing rule exactly: drain only when `runProcessInput` succeeds, and replace the initial result only when the drain returns a nonempty result.

- [ ] **Step 7: Run focused root and promotion tests**

```bash
gofmt -w cmd/evener/scripted_provider_test.go cmd/evener/run_drain_test.go cmd/evener/run.go agent/session_jobtree_drain_test.go
go test ./cmd/evener -run 'TestRunDrain|TestRunDrains' -count=1
go test ./agent -run TestDrainJobTreeWaitsForForegroundPromotedShell -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the root behavior coverage**

```bash
git status --short
git add cmd/evener/scripted_provider_test.go cmd/evener/run_drain_test.go cmd/evener/run.go agent/session_jobtree_drain_test.go
git commit -m "test(run): require managed shell notification turns"
```

---

### Task 4: Keep subagents nonterminal while their owned job trees drain

**Files:**

- Modify: `agent/subagents.go`
- Create: `agent/subagent_owned_job_drain_test.go`

**Interfaces:**

- A successful subagent run invokes `a.sess.DrainJobTree(ctx)` before terminal state is recorded.
- The drain runs after the communicate nudge and `SubagentStop` continuation, because either continuation can launch managed work.
- A nonempty drain result replaces the earlier idle result; a drain error becomes the run error.
- `a.running` remains true and `a.status` remains `SubagentRunning` for the entire drain.

- [ ] **Step 1: Build a controllable child shell environment**

In the new test file, define an environment wrapper that embeds a real local execution environment and implements `StreamCommand` with causal channels:

```go
type ownedJobDrainEnvironment struct {
	execenv.ExecutionEnvironment
	started     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func newOwnedJobDrainEnvironment(dir string) *ownedJobDrainEnvironment {
	return &ownedJobDrainEnvironment{
		ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(dir),
		started:              make(chan struct{}),
		release:              make(chan struct{}),
	}
}

func (e *ownedJobDrainEnvironment) releaseJob() {
	e.releaseOnce.Do(func() { close(e.release) })
}

func (e *ownedJobDrainEnvironment) StreamCommand(_ context.Context, _, _ string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	close(e.started)
	return &execenv.StreamHandle{
		Wait: func() (int, error) {
			<-e.release
			_, _ = out.Write([]byte("child shell complete"))
			return 0, nil
		},
		Signal: e.releaseJob,
	}, nil
}
```

The test calls `releaseJob`; no sleep or polling decides when the process completes, and later session cleanup can signal idempotently.

- [ ] **Step 2: Write the failing subagent lifecycle test**

Add `TestSubagentRunDrainsOwnedShellBeforeFinalizingDelegate`. Build a parent session with the controllable environment and a three-step `fakeAdapter`:

1. child initial turn starts a background shell;
2. child communicates `"waiting for owned shell"` and signals an `idleReported` channel;
3. child notification turn communicates `"owned shell handled"`.

Create the delegate with `Background: true`. After `idleReported` but before releasing the shell, assert under the appropriate locks:

- the child `subagent` is still `running` with status `SubagentRunning`;
- the parent delegate job remains in `parent.jobManager.running`;
- the subagent `done` channel is not closed.

Release the shell, wait for the delegate job to finalize, and assert:

- the child's third provider request contains its shell notification;
- the child result is `"owned shell handled"`, not the interim message;
- the parent delegate job's stored output is `"owned shell handled"`;
- the parent receives the normal delegate terminal notification.

- [ ] **Step 3: Add retention-cap pressure to the same live interval**

Add `TestRetentionDoesNotReclaimSubagentDrainingOwnedWork`. While the child from the fixture is blocked in its drain, insert one consumed terminal synthetic child, set `maxRetainedTerminal` to one, and call `reserveSlot`. Assert the returned eviction contains only the consumed terminal child and the draining child remains tracked and open. Close the evicted session outside the manager lock, release the shell, and let the delegate finish normally.

- [ ] **Step 4: Run the tests and observe premature finalization**

```bash
go test ./agent -run 'Test(SubagentRunDrainsOwnedShellBeforeFinalizingDelegate|RetentionDoesNotReclaimSubagentDrainingOwnedWork)' -count=1
```

Expected: FAIL on the current implementation because `subagent.run` marks the child terminal immediately after the interim communicate, allowing the parent delegate to finalize before shell completion.

- [ ] **Step 5: Drain after all ordinary subagent continuations and before terminal state**

In `subagent.run`, immediately after `runSubagentStopHook` and before the final `budgetExhaustionFromError`/terminal-state block, add:

```go
if err == nil {
	drained, drainErr := a.sess.DrainJobTree(ctx)
	if drainErr != nil {
		err = drainErr
	} else if drained != "" {
		res = drained
	}
}
```

Do not set `a.driving`, create a new delegate job, close/reopen `a.done`, or change retention logic. The existing `a.running` flag already prevents the parent drive-down path and retention GC from racing the child's self-drain.

- [ ] **Step 6: Run the subagent, delegate, and drive-down suites**

```bash
gofmt -w agent/subagents.go agent/subagent_owned_job_drain_test.go
go test ./agent -run 'Test(SubagentRun|Retention|DrainJobTree|Drive|Delegate)' -count=1
```

Expected: PASS. In particular, existing drive-down tests must show no concurrent `EntryNotification` turn while `sub.running == true`.

- [ ] **Step 7: Commit subagent draining**

```bash
git status --short
git add agent/subagents.go agent/subagent_owned_job_drain_test.go
git commit -m "fix(subagent): drain owned jobs before finalizing runs"
```

The commit body must explain that retaining `SubagentRunning` fixes both delegate finalization and GC safety at their common source.

---

### Task 5: Pin cancellation, fatal-error, stop-gate, and detached boundaries

**Files:**

- Modify: `agent/session_jobtree_drain_test.go`
- Create: `cmd/evener/run_drain_error_test.go`
- Re-run unchanged: `cmd/evener/run_detached_test.go`

- [ ] **Step 1: Prove cancelled drains return the caller error and shutdown stops the process**

Add `TestCancelledManagedShellDrainStopsOnSessionClose`. Start a background shell with `newSignalCompletesStreamingExecutor`, begin `DrainJobTree` with a cancellable context, and confirm the job counts as outstanding. Cancel the context and assert `errors.Is(err, context.Canceled)`. Then call `sess.Close()` and assert the executor's `Signal` ran and its `done` channel closed. This test separates the two responsibilities cleanly: drain returns the outer context error; ordinary session shutdown owns process termination.

- [ ] **Step 2: Prove a fatal initial model error never enters the drain**

In `cmd/evener/run_drain_error_test.go`, add `TestRunSkipsDrainAfterFatalModelError`. Install a scripted provider so session construction stays offline, replace `runProcessInput` with a seam that returns a sentinel error, and replace `runDrainJobTree` with a seam that increments a counter. Restore both seams with `t.Cleanup`. Call `run` and assert:

- `errors.Is(err, sentinel)` is true;
- the returned error is the original process error, not a drain or close error;
- the drain call counter is zero;
- the scripted provider received no notification turn.

This is a control-flow test, not an error-message test.

- [ ] **Step 3: Strengthen the stop-gated descendant regression with a shell pending**

In `TestTreeHasOutstandingWorkSkipsStopGatedChild`, replace the generic queued token with an owned durable pending `JobShell` in the child. Before the parent stop gate, the root tree must report outstanding work. After appending the parent's latest `Cancelled/stopped_by_parent` delegate record, the root tree must report quiescent and neither the child provider request count nor its notification queue may change.

- [ ] **Step 4: Run boundary regressions, including detached survival**

```bash
gofmt -w agent/session_jobtree_drain_test.go cmd/evener/run_drain_error_test.go
go test ./agent -run 'Test(CancelledManagedShellDrainStopsOnSessionClose|TreeHasOutstandingWorkSkipsStopGatedChild)' -count=1
go test ./cmd/evener -run 'Test(RunSkipsDrainAfterFatalModelError|RunDetachedCommandSurvivesExit)' -count=1
```

Expected: PASS. Detached execution must still return a PID, outlive `run`, and create no managed job or notification turn.

- [ ] **Step 5: Commit the terminal-boundary coverage**

```bash
git status --short
git add agent/session_jobtree_drain_test.go cmd/evener/run_drain_error_test.go
git commit -m "test(drain): pin terminal lifecycle boundaries"
```

---

### Task 6: Complete verification and review

**Files:** All files changed in Tasks 1–5.

- [ ] **Step 1: Check formatting and the patch boundary**

```bash
gofmt -w agent/session_jobtree_drain.go agent/session_jobtree_drain_test.go agent/session_jobtree_drain_stall_test.go agent/subagents.go agent/subagent_owned_job_drain_test.go cmd/evener/run.go cmd/evener/run_drain_test.go cmd/evener/run_drain_error_test.go cmd/evener/scripted_provider_test.go
git diff --check
git status --short
```

Expected: no formatting or whitespace errors, and only the planned files are modified.

- [ ] **Step 2: Run focused packages without cache reuse**

```bash
go test ./agent ./cmd/evener -count=1
```

Expected: PASS.

- [ ] **Step 3: Run repository gates**

```bash
make lint
make test
```

Expected: PASS with no live-provider requests. If a gate exposes a real defect in the touched behavior, root-cause it and add a behavioral regression before fixing it; do not skip or weaken the gate.

- [ ] **Step 4: Review the final diff against every design invariant**

Confirm from code and tests that:

- all managed shell jobs drain regardless of requested mode;
- detached processes remain disowned immediately;
- each terminal notification forces its owner through `EntryNotification`;
- new work launched by that turn extends the drain;
- durable shell pendings rematerialize once at root and descendant levels;
- live shells cannot trip the stall watchdog;
- subagents remain running/non-reclaimable until their tree is quiescent;
- cancellation and fatal errors preserve their distinct terminal behavior;
- stop-gated descendants are never re-driven.

- [ ] **Step 5: Commit any review-only corrections, then verify a clean tree**

If review required corrections, commit only the corrected files with a detailed message after their focused tests pass. Then run:

```bash
git status --short --branch
git log --oneline -8
```

Expected: branch `wip/drain-background-jobs` is clean, with the design/plan commit followed by the implementation commits above.

---

### Task 7: Run only the five affected Terminal-Bench failures at Luna max

**Files:**

- Read: `/Users/jesse/git/prime-radiant/harbor-runner/src/harbor_runner/cli.py`
- Read: `/Users/jesse/git/prime-radiant/harbor-runner/src/harbor_runner/manifest.py`
- Read: `/Users/jesse/git/prime-radiant/harbor-runner/docs/superpowers/research/2026-08-09-codex-vs-evener-luna-trajectories.md`
- Create: `/Users/jesse/git/prime-radiant/harbor-runner/runs/tb21-luna-max-owned-drain-<UTC timestamp>/`

**Cohort:**

```text
terminal-bench/caffe-cifar-10
terminal-bench/compile-compcert
terminal-bench/hf-model-inference
terminal-bench/rstan-to-pystan
terminal-bench/sqlite-with-gcov
```

These are the five prior Evener misses whose trajectories directly showed terminal completion while finite session-owned work was still active. Do not add prior Evener passes, the already-run detached-service cohort, or unrelated failures.

- [ ] **Step 1: Build and identify the exact Linux binary**

From a clean Evener commit after Task 6:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /Users/jesse/git/prime-radiant/harbor-runner/dist/evener-linux-amd64 ./cmd/evener
shasum -a 256 /Users/jesse/git/prime-radiant/harbor-runner/dist/evener-linux-amd64
git rev-parse HEAD
git status --short
```

Record the commit and checksum before copying the binary to `magic-kingdom`. Verify the remote checksum matches exactly.

- [ ] **Step 2: Run the immutable remote preflight**

On `magic-kingdom`, verify Harbor 0.20.0, Docker health, the pinned Terminal-Bench 2.1 dataset digest, the readable Evener OAuth record, the CA bundle, free disk, and the five exact task names. Do not print or copy OAuth contents into logs or artifacts. Run the harbor-runner unit suite locally before launch:

```bash
cd /Users/jesse/git/prime-radiant/harbor-runner
uv run pytest -q
git status --short
```

Require a clean runner commit. If any identity check fails, stop before Harbor creates trials.

- [ ] **Step 3: Write the manifest and launch exactly five attempts**

Use `harbor_runner.manifest.build_manifest` and `harbor_runner.cli.harbor_argv`. Add the same immutable identity fields present in the canonical 89-task manifest (`runner_commit`, `runner_dirty`, `dataset_digest`, and `task_list_sha256`) before writing it. Launch with:

- model `openai/gpt-5.6-luna`;
- reasoning effort `max`;
- `max_rounds=100`;
- one attempt per task;
- concurrency five;
- dataset `terminal-bench/terminal-bench-2-1` at the pinned digest;
- the five-task cohort above, in that order;
- the verified binary path, OAuth record, and CA bundle;
- `--no-delete` and a new `tb21-luna-max-owned-drain-<UTC timestamp>` run ID.

Save the manifest before launch, run Harbor in a persistent remote session, and do not invoke any upload, publish, or Terminal-Bench submission command.

- [ ] **Step 4: Monitor to five terminal outcomes without broad reruns**

Wait until all five tasks have a terminal `result.json`. Do not launch a successful-path control and do not automatically retry a model failure. If Harbor itself fails before an agent attempt is established, preserve the exception and diagnose the infrastructure boundary before deciding whether that single task needs a replacement attempt.

- [ ] **Step 5: Validate and report the focused evidence**

For each task, inspect reward, verifier output, trajectory, retained Evener stdout/stderr, duration, token counts, and job-notification turns. Report:

- how many of the five converted to full passes;
- whether each run waited for every finite managed job and processed its terminal notification;
- whether any miss is still attributable to drain lifecycle versus model/task correctness;
- any new Evener exception or shutdown warning;
- the Evener commit, binary checksum, runner commit, run ID, and explicit non-submission statement.

If a task still fails, root-cause its earliest decisive divergence before proposing another generalized change. Do not infer success from a clean Evener exit when the verifier failed.

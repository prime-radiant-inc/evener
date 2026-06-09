# Job Control — Phase 2: shell jobs, notifications, restart (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the `shell` tool into a job-capable tool backed by the Phase 1 `jobstore`, with background execution, foreground→background promotion, durable terminal notifications, restart reconciliation, and the `job_read_output`/`job_list`/`job_stop` tools — the first user-visible slice of job control.

**Architecture:** A new `execenv.StreamingExecutor` optional interface streams a process's output to a per-job log and returns a wait/signal handle. A new `JobManager` in package `agent` holds the per-session `*jobstore.Store` plus an in-memory overlay of running jobs, and drives create/list/read/stop. Durable terminal notifications reuse the existing `EntryNotification` queue (`pendingNotifs` → `acceptNotificationInput`) with a new `<job-notification>` format and the `filterDeliverableNotifications` predicate re-keyed onto durable records. Restart reconciliation runs in `RestoreSessionFromMetaWithConfig`. The new job tools register alongside the legacy subagent tools (a temporary parallel surface; Phase 6 removes the legacy one).

**Tech Stack:** Go, `agent/internal/jobstore` (Phase 1), `agent/execenv`, the existing tool registry (`tool.RegisteredTool`/`Exec`), `EntryNotification`. Module: `primeradiant.com/serf/agent`.

This is **Phase 2 of 6**, implementing spec `docs/superpowers/specs/2026-06-08-job-control-design.md` §2 (seam), §5.3 (shell), §5.6/§5.7/§5.8 (read/list/stop), §6 (notifications), §7 (reconciliation). It depends on Phase 1 (`agent/internal/jobstore`) being merged.

**Conventions:** run Go commands from `/Users/jesse/prime-radiant/toil-suite/serf/agent`; package tests with `cd agent && go test ./... -run <name>`. Commit per task. Full `make test` + `make lint` from repo root before the final task.

---

## Shared contracts this phase establishes (Phases 3–5 build on these)

```go
// agent/execenv/execenv.go — NEW optional interface (not a method on ExecutionEnvironment)
type StreamingExecutor interface {
	StreamCommand(ctx context.Context, command, workingDir string, envVars map[string]string, out io.Writer) (*StreamHandle, error)
}
type StreamHandle struct {
	Pid    int
	Wait   func() (exitCode int, err error) // blocks until the process exits
	Signal func()                           // SIGTERM then SIGKILL the process group
}

// agent/jobs.go — the JobManager, one per Session
type jobManager struct {
	mu        sync.Mutex
	store     *jobstore.Store
	dir       string                  // <stateDir>/sessions/<id>
	sessionID string                  // owner/visible session id, stamped onto records
	running   map[string]*runningJob  // job_id -> live runtime overlay
	enqueue   func(jobNotification)   // arm: push a terminal notification onto the parent queue
	now       func() time.Time        // injectable clock (defaults to time.Now)
}
// NOTE for later phases: Phase 5 adds `forward func(jobstore.Event)` and `parentJobID string`
// to this struct for nested-job forwarding. `readOutput`/`list`/`stop`/`createShell`/`finalize`
// are METHODS on *jobManager. `newJobManager(stateDir, sessionID string, enqueue func(jobNotification))`
// stores sessionID on the struct.
type runningJob struct {
	rec    *jobstore.JobRecord
	output *jobstore.OutputStore
	signal func()        // stop the runtime (process group / child cancel)
	done   chan struct{} // closed when finalized
}

// jobNotification is the job-control analogue of subagentNotification (Phase 2 §job_notify).
type jobNotification struct {
	JobID, JobType, Status, Reason, TranscriptRef string
	OutputBytes                                   int64
	ExitCode                                      *int
}
```

Helper for the per-session jobs directory (used everywhere):

```go
// jobsDir returns the per-session job directory: <stateDir>/sessions/<id>. When
// stateDir is empty (persistence off), it returns a process-temp path so
// background jobs still work in-memory for the process lifetime (they won't
// survive restart, which the contract permits).
func jobsDir(stateDir, sessionID string) string {
	if strings.TrimSpace(stateDir) == "" {
		return filepath.Join(os.TempDir(), "serf-jobs", sessionID)
	}
	return filepath.Join(stateDir, "sessions", sessionID)
}
```

---

## Task 1: `StreamingExecutor` interface + handle

**Files:**
- Modify: `agent/execenv/execenv.go` (add the interface + struct near `ExecutionEnvironment`)
- Test: `agent/execenv/streaming_test.go`

- [ ] **Step 1: Write the failing test** — `agent/execenv/streaming_test.go`:

```go
package execenv

import "testing"

// Compile-time assertion that the local env implements the optional interface.
func TestLocalEnvImplementsStreamingExecutor(t *testing.T) {
	var _ StreamingExecutor = (*LocalExecutionEnvironment)(nil)
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./execenv/ -run TestLocalEnvImplementsStreamingExecutor -v`. Expected: FAIL to compile (`undefined: StreamingExecutor`, and once defined, `*LocalExecutionEnvironment does not implement StreamingExecutor`).

- [ ] **Step 3: Add the interface** in `agent/execenv/execenv.go` (add `"context"` and `"io"` imports if missing):

```go
// StreamingExecutor is an optional capability: a long-running command whose
// output streams to out as it arrives, returning a handle to wait on and signal.
// It is separate from ExecutionEnvironment so existing implementers (incl. test
// fakes) are unaffected; the job runtime type-asserts for it.
type StreamingExecutor interface {
	StreamCommand(ctx context.Context, command, workingDir string, envVars map[string]string, out io.Writer) (*StreamHandle, error)
}

// StreamHandle is a running streamed process. Wait blocks until exit and returns
// the exit code; Signal terminates the process group (SIGTERM then SIGKILL).
type StreamHandle struct {
	Pid    int
	Wait   func() (exitCode int, err error)
	Signal func()
}
```

(Leave the test failing on the `does not implement` assertion until Task 2 adds the method.)

- [ ] **Step 4: Verify partial** — `cd agent && go build ./execenv/`. Expected: build OK (interface compiles); the test still fails the assertion. That is expected — Task 2 makes it pass.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/execenv/execenv.go agent/execenv/streaming_test.go
git commit -m "feat(execenv): StreamingExecutor optional interface + handle"
```

---

## Task 2: `LocalExecutionEnvironment.StreamCommand`

**Files:**
- Modify: `agent/execenv/local.go` (add `StreamCommand`, reusing `shellCommand`, `filteredEnvWithPolicy`, `injectLocalVenvPath`, `ensureUnderRoot`, `terminateProcessGroup`/`killProcessGroup`, `runningPIDs` — all already in this file)
- Test: `agent/execenv/streaming_test.go` (extend)

- [ ] **Step 1: Write the failing test** — append to `agent/execenv/streaming_test.go`:

```go
import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

func TestStreamCommandCapturesOutputAndExit(t *testing.T) {
	env := &LocalExecutionEnvironment{RootDir: t.TempDir()}
	if err := env.Initialize(); err != nil { t.Fatal(err) }
	var mu sync.Mutex
	var buf bytes.Buffer
	h, err := env.StreamCommand(context.Background(), "printf 'a\\nb\\n'", "", nil, &lockedWriter{&mu, &buf})
	if err != nil { t.Fatalf("stream: %v", err) }
	code, err := h.Wait()
	if err != nil { t.Fatalf("wait: %v", err) }
	if code != 0 { t.Errorf("exit code = %d, want 0", code) }
	mu.Lock(); got := buf.String(); mu.Unlock()
	if got != "a\nb\n" { t.Errorf("streamed output = %q", got) }
}

func TestStreamCommandSignalStops(t *testing.T) {
	env := &LocalExecutionEnvironment{RootDir: t.TempDir()}
	_ = env.Initialize()
	h, err := env.StreamCommand(context.Background(), "sleep 30", "", nil, &bytes.Buffer{})
	if err != nil { t.Fatalf("stream: %v", err) }
	done := make(chan int, 1)
	go func() { c, _ := h.Wait(); done <- c }()
	h.Signal()
	select {
	case <-done: // process group killed; Wait returned
	case <-time.After(5 * time.Second):
		t.Fatal("Signal did not stop the process within 5s")
	}
}

type lockedWriter struct { mu *sync.Mutex; b *bytes.Buffer }
func (w *lockedWriter) Write(p []byte) (int, error) { w.mu.Lock(); defer w.mu.Unlock(); return w.b.Write(p) }
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./execenv/ -run TestStreamCommand -v`. Expected: FAIL to compile (`StreamCommand` undefined).

- [ ] **Step 3: Implement** `StreamCommand` in `agent/execenv/local.go`. Reuse the exact patterns from `ExecCommand` (see contracts: `shellCommand`, `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`, `filteredEnvWithPolicy`, `injectLocalVenvPath`, `runningPIDs.Store/Delete`, `terminateProcessGroup`/`killProcessGroup`). Differences from `ExecCommand`: stdout+stderr both go to `out` (combined), it returns immediately after `cmd.Start()`, and `Wait`/`Signal` are closures over the started `cmd`:

```go
func (e *LocalExecutionEnvironment) StreamCommand(ctx context.Context, command, workingDir string, envVars map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	dir := strings.TrimSpace(workingDir)
	if dir == "" { dir = e.RootDir }
	if !filepath.IsAbs(dir) { dir = filepath.Join(e.RootDir, dir) }
	if err := e.ensureUnderRoot(dir); err != nil {
		return nil, fmt.Errorf("working directory %w", err)
	}
	cmd := shellCommand(command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = injectLocalVenvPath(filteredEnvWithPolicy(e.EnvPolicy, envVars), []string{dir, e.RootDir})
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	e.runningPIDs.Store(pid, struct{}{})
	var once sync.Once
	wait := func() (int, error) {
		err := cmd.Wait()
		e.runningPIDs.Delete(pid)
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok { code = ee.ExitCode() } else { code = 127 }
		}
		return code, nil // streamed jobs report exit via code; err is folded into code
	}
	signal := func() {
		once.Do(func() {
			terminateProcessGroup(pid)
			go func() { time.Sleep(2 * time.Second); killProcessGroup(pid) }()
		})
	}
	return &execenv.StreamHandle{Pid: pid, Wait: wait, Signal: signal}, nil
}
```

NOTE for the implementer: the file is package `execenv`, so refer to `StreamHandle`/`StreamingExecutor` unqualified (drop the `execenv.` qualifier shown above — it's there only to name the types). Add imports `os/exec`, `sync`, `io`, `time` if missing. Confirm `exec` exit-code extraction matches the existing `ExecCommand` style in this file and reuse whatever helper it already has.

- [ ] **Step 4: Run tests to verify they pass** — `cd agent && go test ./execenv/ -run 'TestStreamCommand|TestLocalEnvImplements' -v`. Expected: PASS (all three, including the Task 1 assertion now satisfied).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/execenv/local.go agent/execenv/streaming_test.go
git commit -m "feat(execenv): LocalExecutionEnvironment.StreamCommand"
```

---

## Task 3: JobManager skeleton (create/list/read/stop over jobstore)

**Files:**
- Create: `agent/jobs.go`
- Test: `agent/jobs_test.go`

This task builds the manager with a **no-op runtime** so create/list/read/stop are testable without a process. Real shell execution is wired in Task 4.

- [ ] **Step 1: Write the failing test** — `agent/jobs_test.go`:

```go
package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func newTestJM(t *testing.T) *jobManager {
	t.Helper()
	jm, err := newJobManager(t.TempDir(), "S1", func(jobNotification) {})
	if err != nil { t.Fatalf("newJobManager: %v", err) }
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	return jm
}

func TestJobManagerCreateAndList(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "make test", Description: "tests"})
	if err != nil { t.Fatalf("create: %v", err) }
	if rec.JobID == "" || rec.Type != jobstore.JobShell || rec.Status != jobstore.StatusRunning {
		t.Fatalf("bad record: %+v", rec)
	}
	jobs := jm.list(listFilter{})
	if len(jobs) != 1 || jobs[0].JobID != rec.JobID {
		t.Fatalf("list = %+v", jobs)
	}
}

func TestJobManagerReadOutput(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.running[rec.JobID].output.Append([]byte("hello\n"))
	content, _, _, err := jm.readOutput(rec.JobID, 1024)
	if err != nil { t.Fatalf("read: %v", err) }
	if content != "hello\n" { t.Errorf("content = %q", content) }
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestJobManager -v`. Expected: FAIL to compile (`undefined: newJobManager`, `createShellOpts`, etc.).

- [ ] **Step 3: Implement** `agent/jobs.go`. Define `jobManager`, `runningJob`, `jobNotification`, `createShellOpts`, `listFilter`, `jobsDir` (from the shared-contracts section), and:
  - `newJobManager(stateDir, sessionID string, enqueue func(jobNotification)) (*jobManager, error)` — `os.MkdirAll(jobsDir, 0o755)`, `os.MkdirAll(<dir>/jobs, 0o755)`, `jobstore.Open(<dir>/jobs.jsonl)`, init `running` map and `now = time.Now`.
  - `createShell(opts) (*jobstore.JobRecord, error)` — mint `jobstore.NewJobID()`; build the record (`Type: JobShell`, `Status: StatusRunning`, `Command`, `Description`, `OwnerSessionID`/`VisibleToSession` = sessionID, `StartedAt: jm.now()`, `OutputPath: <dir>/jobs/<job_id>.log`); append `EventJobStarted`; open the `OutputStore`; register a `runningJob` with a no-op `signal` and a fresh `done`. Return the record.
  - `list(listFilter) []*jobstore.JobRecord` — `store.Load()`, overlay running records, filter by status/type, sort by `StartedAt` desc tie-broken by `JobID`.
  - `readOutput(jobID string, tailBytes int) (content string, total int64, truncated bool, err error)` — find the `OutputStore` (running); for a terminal job, look up the record (`store.Load()`) and open its **`rec.OutputPath`** directly, falling back to `<dir>/jobs/<jobID>.log` only when `OutputPath` is empty. (Honoring `OutputPath` rather than reconstructing the path from the local `dir` is what lets a parent read a forwarded **nested** job's retained output — its `OutputPath` points into the child's session dir — after the child runtime is gone; spec §3.4. Phase 5 depends on this.) `Tail(tailBytes)`.
  - `stop(jobID string) (*jobstore.JobRecord, error)` — call `running[jobID].signal()` if present; finalize is handled by the runtime in Task 4 (here, with the no-op runtime, just mark `stop_pending` or return the current record — keep minimal; Task 4 replaces the stop body).
  - `finalize(jobID string, status jobstore.Status, reason string, exitCode *int)` — write `EventJobFinished` with a minted `jobstore.NewTerminalGeneration()`, close `running[jobID].done`, remove from `running`, then **arm**: write `EventJobNotificationPending` and call `jm.enqueue(jobNotification{...})`. (The enqueue → EntryNotification wiring is Task 5.)

Use the Phase 1 jobstore API exactly (`Open`, `Append`, `Load`, `OpenOutput`, `Tail`, `NewJobID`, `NewTerminalGeneration`, `JobRecord`, the `Event*` kinds).

- [ ] **Step 4: Run tests to verify they pass** — `cd agent && go test ./ -run TestJobManager -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/jobs.go agent/jobs_test.go
git commit -m "feat(agent): JobManager skeleton over jobstore"
```

---

## Task 4: Shell job lifecycle — ephemeral / promotion / background

**Files:**
- Create: `agent/job_shell.go`
- Test: `agent/job_shell_test.go`

This is the spec §5.3 mechanism: **every** shell command streams from byte 0. `runShell` starts the stream, then waits up to `blockTimeoutMS`. If the process exits first → ephemeral (discard the temp log, no durable `job_started`). If `blockTimeoutMS` elapses first → promote (commit `job_started`, keep the log, return the `job_id`). `background=true` commits `job_started` immediately and returns after start.

- [ ] **Step 1: Write the failing test** — `agent/job_shell_test.go`. Use a real `LocalExecutionEnvironment` (it implements `StreamingExecutor`):

```go
package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
)

func newShellTestRig(t *testing.T) (*jobManager, execenv.StreamingExecutor) {
	jm := newTestJM(t)
	env := &execenv.LocalExecutionEnvironment{RootDir: t.TempDir()}
	if err := env.Initialize(); err != nil { t.Fatal(err) }
	return jm, env.(execenv.StreamingExecutor)
}

func TestRunShellForegroundEphemeral(t *testing.T) {
	jm, se := newShellTestRig(t)
	res := runShell(context.Background(), jm, se, shellArgs{Command: "printf done", BlockTimeoutMS: 5000})
	if res.JobID != "" { t.Errorf("ephemeral job must have no job_id, got %q", res.JobID) }
	if res.Status != jobstore.StatusCompleted || res.RunningInBackground {
		t.Errorf("res = %+v, want completed/foreground", res)
	}
	if len(jm.list(listFilter{})) != 0 {
		t.Errorf("ephemeral job must not appear in job_list")
	}
}

func TestRunShellPromotesOnTimeout(t *testing.T) {
	jm, se := newShellTestRig(t)
	res := runShell(context.Background(), jm, se, shellArgs{Command: "sleep 30", BlockTimeoutMS: 1000})
	if res.JobID == "" { t.Fatal("promoted job must have a job_id") }
	if res.Reason != "foreground_timeout" || !res.RunningInBackground || !res.TimedOut {
		t.Errorf("res = %+v, want foreground_timeout/background/timed_out", res)
	}
	if len(jm.list(listFilter{})) != 1 {
		t.Errorf("promoted job must appear in job_list")
	}
	_ = jm.stop(res.JobID) // cleanup
}

func TestRunShellBackgroundReturnsImmediately(t *testing.T) {
	jm, se := newShellTestRig(t)
	start := time.Now()
	res := runShell(context.Background(), jm, se, shellArgs{Command: "sleep 30", Background: true, BlockTimeoutMS: 120000})
	if time.Since(start) > 3*time.Second { t.Error("background must return promptly") }
	if res.JobID == "" || !res.RunningInBackground { t.Errorf("res = %+v", res) }
	_ = jm.stop(res.JobID)
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestRunShell -v`. Expected: FAIL to compile (`runShell`, `shellArgs`, `shellResult` undefined).

- [ ] **Step 3: Implement** `agent/job_shell.go`:
  - `shellArgs{ Command, Description string; Background bool; BlockTimeoutMS, MaxRuntimeMS int }`.
  - `shellResult{ JobID, Type, Status, Reason string; RunningInBackground, TimedOut bool; ExitCode *int; Output string; Truncated bool }`.
  - `runShell(ctx, jm, se, args) shellResult`:
    1. Clamp `BlockTimeoutMS` (default 120000, min 1000, max 600000).
    2. Create the `runningJob` **without** committing `job_started` yet: mint job_id, open a temp `OutputStore` at `<dir>/jobs/<job_id>.log`, call `se.StreamCommand(ctx, args.Command, "", nil, output)`, register `running[job_id]` with `signal = handle.Signal`.
    3. If `MaxRuntimeMS > 0`, start a timer that calls `handle.Signal()` then finalizes `stopped/run_timeout` after the deadline.
    4. Spawn `go func(){ code,_ := handle.Wait(); jm.finalizeShell(job_id, code) }()` where `finalizeShell` maps exit 0→completed/exit_zero, nonzero→failed/exit_nonzero (unless a stop/timeout already finalized — guard with the `done` channel / a `sync.Once`).
    5. If `Background`: commit `EventJobStarted` now, return `{JobID, running, RunningInBackground:true}`.
    6. Else (foreground): `select { case <-done: ...ephemeral...; case <-time.After(blockTimeout): ...promote... }`.
       - **Ephemeral** (`done` first, and it was a normal finish before timeout): read the tail for inline `Output`, then **discard** — do NOT commit `job_started`, delete the temp log, drop `running[job_id]`. Return `{Status: completed/failed from exit, RunningInBackground:false, ExitCode, Output, no JobID}`.
       - **Promote** (timeout first): commit `EventJobStarted` (with the real `StartedAt`), read current tail for `Output`, return `{JobID, running, Reason:"foreground_timeout", RunningInBackground:true, TimedOut:true, Output}`. The background `Wait` goroutine will later `finalizeShell` and arm the terminal notification.

  Key invariant: `job_started` is committed exactly once — either at promotion or at background start; ephemeral never commits it. Finalize is guarded so the process-exit goroutine, the max_runtime timer, and `stop` cannot double-finalize (use the `runningJob.done` close under the manager mutex + a status check).

- [ ] **Step 4: Run tests to verify they pass** — `cd agent && go test ./ -run TestRunShell -v`. Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_shell.go agent/job_shell_test.go
git commit -m "feat(agent): shell job lifecycle (ephemeral/promotion/background)"
```

---

## Task 5: Job-notification bridge (durable arm → EntryNotification)

**Files:**
- Create: `agent/job_notify.go`
- Modify: `agent/session.go` (add a `pendingJobNotifs` queue mirroring `pendingNotifs`, or reuse — see step 3)
- Modify: `agent/session_lifecycle.go` (`formatNotificationReminder` to also render job notifications; `filterDeliverableNotifications` keyed on durable records for jobs)
- Test: `agent/job_notify_test.go`

The cleanest approach reuses the existing `pendingNotifs`/`EntryNotification` machinery by giving the queue a job variant. Per spec §6, the deliverable filter for jobs must key on the **durable job record** (a reconstructed terminal job has no in-memory subagent), and the payload comes from the `JobRecord`.

- [ ] **Step 1: Write the failing test** — `agent/job_notify_test.go`:

```go
package agent

import (
	"strings"
	"testing"
)

func TestFormatJobNotification(t *testing.T) {
	code := 0
	block := formatJobNotificationBlock(jobNotification{
		JobID: "job_X", JobType: "shell", Status: "completed", Reason: "exit_zero",
		OutputBytes: 42, ExitCode: &code,
	})
	for _, want := range []string{`job_id="job_X"`, `event="completed"`, `job_type="shell"`, `status="completed"`, "job_read_output"} {
		if !strings.Contains(block, want) {
			t.Errorf("notification missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "subagent-notification") {
		t.Errorf("must use <job-notification>, not subagent")
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestFormatJobNotification -v`. Expected: FAIL (`formatJobNotificationBlock` undefined).

- [ ] **Step 3: Implement.**
  - `agent/job_notify.go`: `formatJobNotificationBlock(n jobNotification) string` rendering:
    ```
    <job-notification job_id="job_X" event="completed" job_type="shell" status="completed" reason="exit_zero" output_bytes="42">
    Job job_X completed. Use job_read_output to inspect output.
    </job-notification>
    ```
    The `event` attribute mirrors the lifecycle kind (the terminal status, or `running` for a promotion notice). Include `exit_code` and `transcript_ref` attributes when non-nil/non-empty.
  - `agent/session.go`: add `pendingJobNotifs []jobNotification` + `enqueueJobNotification` / `drainJobNotifications` / extend `peekNotifications` to also count job notifs (so the turn loop's `peekNotifications() > 0` check fires for jobs too). Wire `jobManager.enqueue = s.enqueueJobNotification` when the manager is created.
  - `agent/session_lifecycle.go`: in `acceptNotificationInput`, after draining subagent notifs, also drain job notifs, filter them (a job notif is deliverable iff its durable record exists and `NotifyState == pending` — load via the JobManager/store; do **not** consult `s.subagents`), render each via `formatJobNotificationBlock`, and append to the same steering reminder. After successful injection, mark each delivered (`EventJobNotificationDelivered`).

  Keep the subagent path unchanged during Phase 2 (it is removed in Phase 6). The two queues coexist.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestFormatJobNotification -v`. Expected: PASS. Then `cd agent && go test ./ -run 'TestJobManager|TestRunShell' -v` to confirm no regressions.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/job_notify.go agent/session.go agent/session_lifecycle.go agent/job_notify_test.go
git commit -m "feat(agent): durable job terminal-notification bridge"
```

---

## Task 6: Restart reconciliation wiring

**Files:**
- Modify: `agent/session_init.go` (`RestoreSessionFromMetaWithConfig` — after `s.subagents = newSubagentManager(...)`, construct the JobManager and reconcile)
- Modify: `agent/session_init.go` (`NewSession` — construct the JobManager for fresh sessions too)
- Test: `agent/job_reconcile_test.go`

- [ ] **Step 1: Write the failing test** — `agent/job_reconcile_test.go`. Seed a `jobs.jsonl` with a `running` job and no live runtime, then reconstruct and assert it is finalized `stopped/runtime_lost` with a queued notification:

```go
package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestReconcileOnRestoreFinalizesLostJob(t *testing.T) {
	dir := t.TempDir()
	// Seed a store with a running job (started, never finished).
	st, _ := jobstore.Open(dir + "/sessions/S1/jobs.jsonl") // (mkdir first in the test helper)
	start := time.Unix(1, 0).UTC()
	_ = st.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_lost", Type: jobstore.JobShell, OwnerSessionID: "S1", VisibleToSession: "S1", StartedAt: &start})
	_ = st.Close()

	var queued []jobNotification
	jm, err := newJobManager(dir+"/sessions/S1", "S1", func(n jobNotification) { queued = append(queued, n) })
	if err != nil { t.Fatal(err) }
	jm.now = func() time.Time { return time.Unix(100, 0).UTC() }

	jm.reconcileLostJobs() // finalize running-without-runtime

	recs, _ := jm.store.Load()
	if recs["job_lost"].Status != jobstore.StatusStopped || recs["job_lost"].Reason != "runtime_lost" {
		t.Fatalf("job_lost = %+v, want stopped/runtime_lost", recs["job_lost"])
	}
	if len(queued) != 1 || queued[0].JobID != "job_lost" {
		t.Fatalf("expected one queued runtime_lost notification, got %+v", queued)
	}
}
```

(The test helper must `os.MkdirAll(dir+"/sessions/S1/jobs", 0o755)` before `jobstore.Open`; fold that into `newJobManager` if it already does the mkdir.)

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestReconcileOnRestore -v`. Expected: FAIL (`reconcileLostJobs` undefined).

- [ ] **Step 3: Implement** `jobManager.reconcileLostJobs()` in `agent/jobs.go`: `recs, _ := jm.store.Load()`; build `live := map[string]bool{}` from `jm.running`; `events := jobstore.Reconcile(recs, live, jm.now())`; for each event: `jm.store.Append(event)`, then arm — `jm.store.Append(jobstore.Event{Kind: jobstore.EventJobNotificationPending, JobID: event.JobID, TerminalGen: event.TerminalGen})` and `jm.enqueue(jobNotification{JobID: event.JobID, JobType: string(rec.Type), Status: "stopped", Reason: "runtime_lost", ...})`. Then in `RestoreSessionFromMetaWithConfig` (after the subagent manager line), add:

```go
jm, err := newJobManager(s.stateDir, s.id, s.enqueueJobNotification)
if err != nil { return nil, fmt.Errorf("job manager: %w", err) }
jm.reconcileLostJobs()
s.jobs = jm
```

and add `jobs *jobManager` to the `Session` struct (`agent/session.go`). In `NewSession`, construct the JobManager the same way but without `reconcileLostJobs()` (a fresh session has no prior jobs). Confirm `s.enqueueJobNotification` exists from Task 5.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestReconcileOnRestore -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/jobs.go agent/session.go agent/session_init.go agent/job_reconcile_test.go
git commit -m "feat(agent): restart reconciliation wiring for jobs"
```

---

## Task 7: Rework `DefShell` parameters

**Files:**
- Modify: `agent/internal/tool/definitions.go` (`DefShell`)
- Test: `agent/internal/tool/definitions_test.go` (add or extend)

- [ ] **Step 1: Write the failing test** — add to `agent/internal/tool/definitions_test.go`:

```go
func TestDefShellHasJobParams(t *testing.T) {
	props := DefShell().Parameters["properties"].(map[string]any)
	for _, p := range []string{"command", "description", "background", "block_timeout_ms", "max_runtime_ms"} {
		if _, ok := props[p]; !ok { t.Errorf("DefShell missing param %q", p) }
	}
	if _, ok := props["timeout_ms"]; ok {
		t.Errorf("DefShell must not have the old timeout_ms param")
	}
}
```

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./internal/tool/ -run TestDefShellHasJobParams -v`. Expected: FAIL (`timeout_ms` still present, new params missing).

- [ ] **Step 3: Implement** — rewrite `DefShell()` per spec §5.3: params `command` (required), `description`, `background` (bool), `block_timeout_ms` (int), `max_runtime_ms` (int); drop `timeout_ms`; description string is the spec §5.3 model-facing text ("Run a shell command and return its stdout, stderr, and exit code inline. … Pass `background=true` … `block_timeout_ms` bounds only the foreground wait … `max_runtime_ms` is the separate limit on how long the process itself may run …"). Copy the exact description from spec §5.3.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./internal/tool/ -run TestDefShellHasJobParams -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/tool/definitions.go agent/internal/tool/definitions_test.go
git commit -m "feat(tool): rework DefShell for job-capable params"
```

---

## Task 8: Route the shell tool handler through the JobManager

**Files:**
- Modify: `agent/session_tools_shell.go` (the shell Exec)
- Modify: `agent/session_tool_registry.go` (`toolDeps` — add a `jobs func() *jobManager` accessor and a `streamingExec func() (execenv.StreamingExecutor, bool)`) OR pass `s` into `registerShellTools` like `registerSubagentTools(reg, s, deps)` does. Prefer passing `s` (simpler; matches the subagent precedent).
- Test: `agent/session_tools_shell_test.go` (add)

- [ ] **Step 1: Write the failing test** — add an integration test that registers tools on a real session and calls the shell tool with `background=true`, asserting a `job_id` comes back and the job appears in `job_list`. Follow the existing pattern in `agent/session_parity_test.go` / `agent/session_dod_test.go` for building a test `*Session` with a `LocalExecutionEnvironment`. Key assertion:

```go
func TestShellToolBackgroundReturnsJobID(t *testing.T) {
	s := newTestSessionWithLocalEnv(t) // reuse an existing helper; see session_dod_test.go
	out, err := s.callTool(t, "shell", map[string]any{"command": "sleep 30", "background": true})
	if err != nil { t.Fatal(err) }
	if !strings.Contains(out, "job_") { t.Errorf("expected a job_id in %q", out) }
}
```

(Use whatever tool-invocation test helper already exists; if none, call the registered `Exec` directly via the registry as the existing shell tests do.)

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestShellToolBackground -v`. Expected: FAIL (handler still buffered; no job_id).

- [ ] **Step 3: Implement** — change `registerShellTools` to `registerShellTools(reg, s, deps)` (update the call site in `registerCoreTools`), and rewrite the shell Exec: parse `command`/`description`/`background`/`block_timeout_ms`/`max_runtime_ms`; assert `env.(execenv.StreamingExecutor)` (it will be present for `LocalExecutionEnvironment`); call `runShell(ctx, s.jobs, se, shellArgs{...})`; marshal the `shellResult` to the spec §5.3 return shapes (ephemeral inline output vs `{job_id, status, running_in_background}` vs promotion shape). If the env does not implement `StreamingExecutor`, fall back to the old buffered `env.ExecCommand` path for foreground only and ignore `background` with a clear inline note (keeps non-local envs working). Return JSON per the spec shapes.

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestShellTool -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/session_tools_shell.go agent/session_tool_registry.go agent/session_tools_shell_test.go
git commit -m "feat(agent): route shell tool through JobManager"
```

---

## Task 9: `job_read_output` / `job_list` / `job_stop` tools

**Files:**
- Modify: `agent/internal/tool/definitions.go` (add `DefJobReadOutput`, `DefJobList`, `DefJobStop`)
- Create: `agent/session_tools_jobs.go` (`registerJobTools(reg, s, deps)` + the three handlers)
- Modify: `agent/session_tool_registry.go` (`registerCoreTools` calls `registerJobTools(reg, s, deps)`)
- Modify: `agent/provider/profile.go` (`toolDefinitionsForCapabilities`: add `capabilityJobControl` block adding the three Defs; add `capabilityJobControl` to the capability sets that already get `capabilityAgentControl` — anthropic/openai/gemini)
- Test: `agent/session_tools_jobs_test.go`

- [ ] **Step 1: Write the failing test** — `agent/session_tools_jobs_test.go`: register tools on a test session, start a background shell job, then `job_list` (assert it's there), `job_read_output` (assert status), `job_stop` (assert it returns `cancelled`/`stopped`). Assert the three `Def*` exist with required params (`job_id` required for read/stop).

- [ ] **Step 2: Run test to verify it fails** — `cd agent && go test ./ -run TestJobTools -v`. Expected: FAIL (Defs/handlers undefined).

- [ ] **Step 3: Implement** the three `Def*` per spec §5.6/§5.7/§5.8 (exact params, bounds, descriptions from the spec) and the handlers in `registerJobTools`, each delegating to `s.jobs`:
  - `job_read_output`: `s.jobs.readOutput(job_id, tail_bytes)` (+ `grep`/`block` — for Phase 2, implement `tail_bytes` and `grep`; `block=true` can be a single bounded wait on the running job's `done` with `block_timeout_ms`). Return the spec §5.6 shape.
  - `job_list`: `s.jobs.list(listFilter{Status, Type, Limit, IncludeNested:false})`; return the spec §5.7 shape (with the null-projection: emit `reason`/`parent_job_id`/`not_resumable_reason` as `null`, `resumable` only for delegates).
  - `job_stop`: `s.jobs.stop(job_id)`; return `{job_id, status, reason}` per §5.8.
  Add `capabilityJobControl` in `profile.go` and wire it into the same three capability sets that include `capabilityAgentControl` (see contracts §8). The legacy `capabilityAgentControl` block stays for now (Phase 6 removes it).

- [ ] **Step 4: Run test to verify it passes** — `cd agent && go test ./ -run TestJobTools -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add agent/internal/tool/definitions.go agent/session_tools_jobs.go agent/session_tool_registry.go agent/provider/profile.go agent/session_tools_jobs_test.go
git commit -m "feat(agent): job_read_output/job_list/job_stop tools"
```

---

## Task 10: Full-suite green + live smoke

**Files:** none (verification)

- [ ] **Step 1: Run the full module test + lint**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && make test && make lint`
Expected: all modules PASS; lint clean (golangci ×4 + namingcheck/internalcheck/docscheck). Fix any fallout (the `DefShell` param change and the new capability may touch parity/snapshot tests — update them to the new shell shape).

- [ ] **Step 2: Live smoke** (per `reference_serf_live_run` recipe — build a standalone binary, do NOT touch a running serve):

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
go build -o /tmp/serf ./cmd/serf
. "$PWD/.env"
# In a scratch dir, run serf and ask it to start a background sleep, then job_list / job_read_output / job_stop.
```
Expected: a background shell job returns a `job_id`; `job_list` shows it `running`; `job_stop` returns `cancelled`; a foreground command that exceeds a small `block_timeout_ms` emits a `<job-notification ... reason="foreground_timeout">`.

- [ ] **Step 3: Commit any test/lint fixups**

```bash
cd /Users/jesse/prime-radiant/toil-suite/serf
git add -A   # only after git status review
git commit -m "test(job-control): phase 2 suite + parity fixups green"
```

---

## Phase 2 self-review

- **Spec coverage:** StreamingExecutor + streaming shell (Tasks 1,2,4) ↔ §2.3/§5.3; JobManager + read/list/stop (Tasks 3,9) ↔ §5.6–§5.8; durable notifications (Task 5) ↔ §6; reconciliation (Task 6) ↔ §7; DefShell rework (Task 7) ↔ §5.3. `block=true` single bounded wait is in Task 9; `max_runtime_ms` kill→`stopped/run_timeout` is in Task 4.
- **Consistency for later phases:** `jobManager`, `runningJob`, `jobNotification`, `createShellOpts`/`listFilter`, `finalize`, `enqueueJobNotification`, `jobsDir` are the names Phases 3–5 reuse. The `delegate` runtime (Phase 3) registers a `runningJob` whose `signal` cancels the child run instead of a process group, and calls the same `jm.finalize`.
- **No legacy break:** the subagent tools/notifications are untouched; the two surfaces coexist until Phase 6.
- **Placeholder scan:** the handler bodies reference spec §5.3/§5.6–§5.8 for the exact return JSON and descriptions — the implementer copies those verbatim from the spec; no invented shapes.

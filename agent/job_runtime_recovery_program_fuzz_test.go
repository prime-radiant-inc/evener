//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
)

// FuzzJobRuntimeRecoveryProgram drives the durable shell-job lifecycle through
// three production paths that are awkward to reach with ordinary tool-call
// fuzzing: foreground max-runtime promotion, a running Session's output/read/
// stop cycle, and a restarted job manager's runtime-lost reconciliation.
//
// The stream executor is scripted at the process boundary, all timers use
// FakeClock, and the only filesystem state is each test's temp job store. The
// program never starts a subprocess, sends a provider request, or uses network
// state. Its oracles assert terminal durability, output identity, virtual-wait
// progress, restart recovery, and stable externally visible runtime results.
//
// Registry: native:agent:.:FuzzJobRuntimeRecoveryProgram::job_shell.go;jobs.go;session_tools_jobs.go
func FuzzJobRuntimeRecoveryProgram(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		{0},
		{1, 2, 3, 4, 5, 6, 7, 8},
		{255, 254, 253, 252, 251, 250, 249, 248},
		[]byte("runtime output and a longer fuzz program"),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		p := jrrpDecode(data)
		jrrpAssertStartOnlyContextContract(t)

		// A fresh replay cannot retain a job ID or terminal generation, but every
		// observable job result must still be identical.
		first := jrrpRunRuntimeTimeout(t, p)
		if replay := jrrpRunRuntimeTimeout(t, p); replay != first {
			t.Fatalf("runtime-timeout replay changed:\nfirst=%+v\nreplay=%+v", first, replay)
		}

		jrrpAssertForegroundPromotion(t, p)
		jrrpAssertFinalizationRetries(t, p)
		jrrpAssertShellFailureEdges(t, p)
		jrrpAssertSessionOutputAndStop(t, p)
		jrrpAssertPrunedOutputLifecycle(t, p)
		jrrpAssertStructuredProjection(t, p)
		jrrpAssertRestartReconciliation(t, p)
	})
}

// jrrpReader makes a finite semantic program from arbitrary fuzz bytes. Missing
// bytes are zero so appending input only extends later choices.
type jrrpReader struct {
	data []byte
	pos  int
}

func (r *jrrpReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *jrrpReader) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next()) % n
}

func (r *jrrpReader) word(max int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789_-"
	n := 1 + r.intn(max)
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.intn(len(alphabet))]
	}
	return string(b)
}

type jrrpProgram struct {
	runtimeOutput string
	liveOutput    string
	marker        string
	exitCode      int
}

func jrrpDecode(data []byte) jrrpProgram {
	r := &jrrpReader{data: data}
	marker := "needle-" + r.word(18)
	return jrrpProgram{
		runtimeOutput: "runtime-" + r.word(24) + "\n" + marker + "\n",
		liveOutput: strings.Join([]string{
			"first-" + r.word(24),
			marker,
			"middle-" + r.word(24),
			"last-" + r.word(24),
			"",
		}, "\n"),
		marker:   marker,
		exitCode: []int{0, 1, 42, 143, -1}[r.intn(5)],
	}
}

type jrrpContextKey struct{}

// jrrpAssertStartOnlyContextContract validates the context transferred to a
// process start: it inherits parent values before launch, observes a cancellation
// before detach, and deliberately stops observing cancellation after detach.
func jrrpAssertStartOnlyContextContract(t *testing.T) {
	t.Helper()
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), jrrpContextKey{}, "start-value"))
	start, detach := newStartOnlyContext(parent)
	if _, ok := start.Deadline(); ok {
		t.Fatal("start-only context unexpectedly invented a deadline")
	}
	if got, _ := start.Value(jrrpContextKey{}).(string); got != "start-value" {
		t.Fatalf("start-only context value = %q, want inherited value", got)
	}
	if start.Err() != nil {
		t.Fatalf("fresh start-only context error = %v", start.Err())
	}
	_ = start.Done()
	detach()
	start.DetachAfterStart()
	cancel()
	select {
	case <-start.Done():
		t.Fatal("start-only context observed cancellation after detach")
	default:
	}
	if start.Err() != nil {
		t.Fatalf("detached start-only context error = %v", start.Err())
	}

	preStart, preCancel := context.WithCancel(context.Background())
	pre, _ := newStartOnlyContext(preStart)
	preCancel()
	<-pre.Done()
	if pre.Err() != context.Canceled {
		t.Fatalf("pre-detach cancellation = %v, want context.Canceled", pre.Err())
	}
}

// jrrpRuntimeExecutor is a process-boundary fake. It writes a fixed bounded
// stream, then waits until runShell's signal path releases it. No host process
// can be created by this executor.
type jrrpRuntimeExecutor struct {
	output    []byte
	exitCode  int
	done      chan struct{}
	once      sync.Once
	startOnce sync.Once
	signals   atomic.Int32
	start     context.Context
	startOnly *startOnlyContext
	started   chan struct{}
	accepted  chan struct{}
	startErr  error
}

func (e *jrrpRuntimeExecutor) StreamCommand(ctx context.Context, _ string, _ string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	e.start = ctx
	e.startOnly, _ = ctx.(*startOnlyContext)
	if e.started != nil {
		e.startOnce.Do(func() { close(e.started) })
	}
	// These calls exercise the context contract at the real process boundary.
	_, _ = ctx.Deadline()
	_ = ctx.Done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = ctx.Value(jrrpContextKey{})
	if e.startErr != nil {
		return nil, e.startErr
	}
	if _, err := out.Write(e.output); err != nil {
		return nil, err
	}
	if e.accepted != nil {
		close(e.accepted)
	}
	return &execenv.StreamHandle{
		Wait: func() (int, error) {
			<-e.done
			return e.exitCode, nil
		},
		Signal: func() {
			e.signals.Add(1)
			e.once.Do(func() { close(e.done) })
		},
	}, nil
}

type jrrpRuntimeSummary struct {
	Status            string
	Reason            string
	Output            string
	TotalBytes        int64
	DroppedBytes      int64
	HasExitCode       bool
	ExitCode          int
	NotificationCount int
}

func jrrpRunRuntimeTimeout(t *testing.T, p jrrpProgram) jrrpRuntimeSummary {
	t.Helper()
	jm := newTestJM(t)
	clk := agenttest.NewFakeClock()
	jm.clock = clk
	jm.now = clk.Now
	defer func() {
		if err := jm.close(); err != nil {
			t.Fatalf("close runtime job manager: %v", err)
		}
	}()

	var notifications []jobNotification
	jm.enqueue = func(n jobNotification) { notifications = append(notifications, n) }
	exec := &jrrpRuntimeExecutor{
		output:   []byte(p.runtimeOutput),
		exitCode: p.exitCode,
		done:     make(chan struct{}),
	}
	resultCh := make(chan shellResult, 1)
	go func() {
		resultCh <- runShell(context.Background(), jm, exec, shellArgs{
			Command:        "scripted-runtime",
			BlockTimeoutMS: 5000,
			MaxRuntimeMS:   100,
			WorkingDir:     "/virtual/runtime",
		})
	}()

	// One foreground-block timer and one max-runtime timer must both be armed
	// before virtual time can advance. This avoids wall-clock coordination.
	clk.BlockUntil(2)
	clk.Advance(100 * time.Millisecond)
	res := <-resultCh
	if exec.start == nil || exec.startOnly == nil {
		t.Fatal("runtime executor did not receive a start context")
	}
	exec.startOnly.DetachAfterStart()
	if exec.start.Err() != nil {
		t.Fatalf("detached runtime start context error = %v", exec.start.Err())
	}
	if got := exec.signals.Load(); got != 1 {
		t.Fatalf("runtime signal count = %d, want 1", got)
	}
	if res.JobID == "" || res.Status != string(jobstore.StatusStopped) || res.Reason != "run_timeout" || res.RunningInBackground || res.TimedOut {
		t.Fatalf("runtime result = %+v, want durable stopped/run_timeout foreground result", res)
	}
	if res.Output != p.runtimeOutput || res.TotalBytes != int64(len(p.runtimeOutput)) || res.DroppedBytes != 0 || res.Truncated {
		t.Fatalf("runtime output result = %+v, want exact bounded output", res)
	}
	if res.ExitCode == nil || *res.ExitCode != p.exitCode {
		t.Fatalf("runtime exit code = %v, want %d", res.ExitCode, p.exitCode)
	}

	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load runtime record: %v", err)
	}
	rec := recs[res.JobID]
	if rec == nil || rec.Status != jobstore.StatusStopped || rec.Reason != "run_timeout" || rec.OutputBytes != int64(len(p.runtimeOutput)) || rec.WorkingDir != "/virtual/runtime" {
		t.Fatalf("durable runtime record = %+v", rec)
	}
	if len(notifications) != 1 || notifications[0].JobID != res.JobID || notifications[0].Status != string(jobstore.StatusStopped) {
		t.Fatalf("runtime notifications = %+v, want one terminal notification", notifications)
	}
	output, total, truncated, err := jm.readOutput(res.JobID, shellInlineOutputBytes)
	if err != nil || output != p.runtimeOutput || total != int64(len(p.runtimeOutput)) || truncated {
		t.Fatalf("durable runtime output = %q, %d, %v, %v", output, total, truncated, err)
	}
	matches, err := jm.grepOutput(res.JobID, regexp.MustCompile(regexp.QuoteMeta(p.marker)))
	if err != nil || len(matches) != 1 || matches[0].Line != p.marker {
		t.Fatalf("durable runtime grep = %+v, %v", matches, err)
	}

	return jrrpRuntimeSummary{
		Status:            res.Status,
		Reason:            res.Reason,
		Output:            res.Output,
		TotalBytes:        res.TotalBytes,
		DroppedBytes:      res.DroppedBytes,
		HasExitCode:       res.ExitCode != nil,
		ExitCode:          p.exitCode,
		NotificationCount: len(notifications),
	}
}

// jrrpAssertForegroundPromotion covers the other durable branch of a
// foreground shell: its block timeout promotes the job, exposes the live-work
// handle while it is running, and then a caller stop wins the terminal race.
func jrrpAssertForegroundPromotion(t *testing.T, p jrrpProgram) {
	t.Helper()
	jm := newTestJM(t)
	clk := agenttest.NewFakeClock()
	jm.clock = clk
	jm.now = clk.Now
	defer func() {
		if err := jm.close(); err != nil {
			t.Fatalf("close promoted job manager: %v", err)
		}
	}()

	exec := &jrrpRuntimeExecutor{
		output:   []byte(p.runtimeOutput),
		exitCode: p.exitCode,
		done:     make(chan struct{}),
	}
	resultCh := make(chan shellResult, 1)
	go func() {
		resultCh <- runShell(context.Background(), jm, exec, shellArgs{
			Command:        "scripted-promotion",
			BlockTimeoutMS: 1000,
			WorkingDir:     "/virtual/promoted",
		})
	}()
	clk.BlockUntil(1)
	clk.Advance(time.Second)
	res := <-resultCh
	if res.JobID == "" || res.Status != string(jobstore.StatusRunning) || res.Reason != "foreground_timeout" || !res.RunningInBackground || !res.TimedOut {
		t.Fatalf("foreground promotion result = %+v", res)
	}
	if res.Output != p.runtimeOutput || res.TotalBytes != int64(len(p.runtimeOutput)) || res.Truncated || res.DroppedBytes != 0 {
		t.Fatalf("foreground promotion output = %+v", res)
	}
	foundLiveWork := false
	for _, handle := range jm.liveWorkHandles() {
		if handle.dir == "/virtual/promoted" && strings.Contains(handle.handle, res.JobID) {
			foundLiveWork = true
		}
	}
	if !foundLiveWork {
		t.Fatalf("live work handles = %+v, want promoted shell %q", jm.liveWorkHandles(), res.JobID)
	}
	done, ok := jobDone(jm, res.JobID)
	if !ok {
		t.Fatalf("promoted job %q has no done channel", res.JobID)
	}
	if _, err := jm.stop(res.JobID); err != nil {
		t.Fatalf("stop promoted job: %v", err)
	}
	<-done
	if got := exec.signals.Load(); got != 1 {
		t.Fatalf("promoted job signal count = %d, want 1", got)
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load promoted job: %v", err)
	}
	if rec := recs[res.JobID]; rec == nil || rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("stopped promoted record = %+v", rec)
	}

	// A delayed shell that never reaches durable start still has to release its
	// output store and done waiters when the manager abandons that one runtime.
	delayed, err := jm.newDelayedShell(shellArgs{Command: "abandon-only"})
	if err != nil {
		t.Fatalf("new delayed shell: %v", err)
	}
	jm.abandonRunningJob(delayed.rec.JobID)
	select {
	case <-delayed.done:
	default:
		t.Fatal("abandoned delayed shell left its done channel open")
	}
	if _, ok := jobDone(jm, delayed.rec.JobID); ok {
		t.Fatalf("abandoned delayed shell %q is still running", delayed.rec.JobID)
	}
}

// jrrpAssertFinalizationRetries drives each durable retry loop with a fake
// clock. The write seam fails a bounded number of job_finished events, then
// heals; each loop must converge to one terminal record without duplicate owner
// notification for a synchronous kept shell result.
func jrrpAssertFinalizationRetries(t *testing.T, p jrrpProgram) {
	t.Helper()
	jm := newTestJM(t)
	clk := agenttest.NewFakeClock()
	jm.clock = clk
	jm.now = clk.Now
	defer func() {
		if err := jm.close(); err != nil {
			t.Fatalf("close retry job manager: %v", err)
		}
	}()

	var notifications []jobNotification
	jm.enqueue = func(n jobNotification) { notifications = append(notifications, n) }
	origAppend := jm.appendEvent
	const retryFailures = shellFinalizeAttempts - 1
	installFinishedFailures := func(n int) *int {
		attempts := 0
		jm.appendEvent = func(e jobstore.Event) error {
			if e.Kind == jobstore.EventJobFinished && attempts < n {
				attempts++
				return errors.New("scripted job_finished persistence failure")
			}
			return origAppend(e)
		}
		return &attempts
	}
	advanceRetries := func(n int) {
		for i := 0; i < n; i++ {
			clk.BlockUntil(1)
			clk.Advance(time.Second)
		}
	}

	first, err := jm.createShell(createShellOpts{Command: "retry-bounded"})
	if err != nil {
		t.Fatalf("create retry job: %v", err)
	}
	firstAttempts := installFinishedFailures(retryFailures)
	code := p.exitCode
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- jm.finalizeShellWithRetry(first.JobID, jobstore.StatusCompleted, "exit_zero", &code)
	}()
	advanceRetries(retryFailures)
	if err := <-firstDone; err != nil {
		t.Fatalf("bounded finalizer retry: %v", err)
	}
	if *firstAttempts != retryFailures {
		t.Fatalf("bounded finalizer failures = %d, want %d", *firstAttempts, retryFailures)
	}

	second, err := jm.createShell(createShellOpts{Command: "retry-until-durable"})
	if err != nil {
		t.Fatalf("create detached retry job: %v", err)
	}
	secondAttempts := installFinishedFailures(2)
	secondDone := make(chan struct{})
	go func() {
		jm.finalizeShellUntilDurable(second.JobID, jobstore.StatusCompleted, "exit_zero", &code)
		close(secondDone)
	}()
	advanceRetries(2)
	<-secondDone
	if *secondAttempts != 2 {
		t.Fatalf("detached finalizer failures = %d, want 2", *secondAttempts)
	}

	// This shell is intentionally delayed until its result is kept. Its retry
	// path must converge without arming an owner notification because the result
	// is already delivered inline to the caller.
	jm.appendEvent = origAppend
	kept, err := jm.newDelayedShell(shellArgs{Command: "retry-kept"})
	if err != nil {
		t.Fatalf("new kept delayed shell: %v", err)
	}
	if err := jm.commitDelayedShell(kept); err != nil {
		t.Fatalf("commit kept delayed shell: %v", err)
	}
	beforeKeptNotifications := len(notifications)
	keptAttempts := installFinishedFailures(2)
	keptDone := make(chan struct{})
	go func() {
		jm.finalizeKeptSyncUntilDurable(kept, jobstore.StatusCompleted, "exit_zero", &code)
		close(keptDone)
	}()
	advanceRetries(2)
	<-keptDone
	if *keptAttempts != 2 {
		t.Fatalf("kept finalizer failures = %d, want 2", *keptAttempts)
	}
	if len(notifications) != beforeKeptNotifications {
		t.Fatalf("kept shell enqueued duplicate owner notification: before=%d after=%d", beforeKeptNotifications, len(notifications))
	}

	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load retried records: %v", err)
	}
	for _, jobID := range []string{first.JobID, second.JobID, kept.rec.JobID} {
		rec := recs[jobID]
		if rec == nil || rec.Status != jobstore.StatusCompleted || rec.Reason != "exit_zero" {
			t.Fatalf("retried record %q = %+v", jobID, rec)
		}
	}
}

// jrrpAssertShellFailureEdges covers the shell paths where starting is refused,
// a foreground tool context is cancelled after the process boundary accepts it,
// durable start persistence fails, and a runtime timeout outlives its bounded
// synchronous finalization retry before the detached retry converges.
func jrrpAssertShellFailureEdges(t *testing.T, p jrrpProgram) {
	t.Helper()

	t.Run("start_error", func(t *testing.T) {
		jm := newTestJM(t)
		defer func() {
			if err := jm.close(); err != nil {
				t.Fatalf("close start-error manager: %v", err)
			}
		}()
		exec := &jrrpRuntimeExecutor{
			done:     make(chan struct{}),
			startErr: errors.New("scripted stream start error"),
		}
		res := runShell(context.Background(), jm, exec, shellArgs{Command: "start-error", BlockTimeoutMS: 1000})
		if res.Status != string(jobstore.StatusFailed) || res.Reason != "start_failed" || res.JobID != "" || res.RunningInBackground {
			t.Fatalf("start error result = %+v", res)
		}
		if jobs := jm.list(listFilter{}); len(jobs) != 0 {
			t.Fatalf("start error left durable jobs: %+v", jobs)
		}
	})

	t.Run("foreground_cancel", func(t *testing.T) {
		jm := newTestJM(t)
		clk := agenttest.NewFakeClock()
		jm.clock = clk
		jm.now = clk.Now
		defer func() {
			if err := jm.close(); err != nil {
				t.Fatalf("close cancellation manager: %v", err)
			}
		}()
		ctx, cancel := context.WithCancel(context.Background())
		exec := &jrrpRuntimeExecutor{
			output:   []byte(p.runtimeOutput),
			exitCode: p.exitCode,
			done:     make(chan struct{}),
			started:  make(chan struct{}),
			accepted: make(chan struct{}),
		}
		resultCh := make(chan shellResult, 1)
		go func() {
			resultCh <- runShell(ctx, jm, exec, shellArgs{Command: "cancel-after-start", BlockTimeoutMS: 1000})
		}()
		<-exec.started
		<-exec.accepted
		cancel()
		res := <-resultCh
		if res.Status != string(jobstore.StatusStopped) || res.Reason != "cancelled" || res.RunningInBackground || res.TimedOut || res.Output != p.runtimeOutput {
			t.Fatalf("foreground cancellation result = %+v", res)
		}
		if got := exec.signals.Load(); got != 1 {
			t.Fatalf("foreground cancellation signal count = %d, want 1", got)
		}
		if jobs := jm.list(listFilter{}); len(jobs) != 0 {
			t.Fatalf("foreground cancellation left durable jobs: %+v", jobs)
		}
	})

	t.Run("background_start_persist_error", func(t *testing.T) {
		jm := newTestJM(t)
		defer func() {
			if err := jm.close(); err != nil {
				t.Fatalf("close start-persist manager: %v", err)
			}
		}()
		origAppend := jm.appendEvent
		jm.appendEvent = func(e jobstore.Event) error {
			if e.Kind == jobstore.EventJobStarted {
				return errors.New("scripted job_started persistence error")
			}
			return origAppend(e)
		}
		exec := &jrrpRuntimeExecutor{output: []byte(p.runtimeOutput), done: make(chan struct{})}
		res := runShell(context.Background(), jm, exec, shellArgs{Command: "persist-error", Background: true})
		if res.Status != string(jobstore.StatusFailed) || res.Reason != "start_failed" || res.JobID != "" || res.RunningInBackground {
			t.Fatalf("background start persistence result = %+v", res)
		}
		if got := exec.signals.Load(); got != 1 {
			t.Fatalf("background start persistence signal count = %d, want 1", got)
		}
		if jobs := jm.list(listFilter{}); len(jobs) != 0 {
			t.Fatalf("background start persistence left durable jobs: %+v", jobs)
		}
	})

	t.Run("runtime_finalizer_exhaustion", func(t *testing.T) {
		jm := newTestJM(t)
		clk := agenttest.NewFakeClock()
		jm.clock = clk
		jm.now = clk.Now
		defer func() {
			if err := jm.close(); err != nil {
				t.Fatalf("close runtime-finalizer manager: %v", err)
			}
		}()
		origAppend := jm.appendEvent
		finishFailures := 0
		jm.appendEvent = func(e jobstore.Event) error {
			if e.Kind == jobstore.EventJobFinished && finishFailures < shellFinalizeAttempts {
				finishFailures++
				return errors.New("scripted terminal persistence error")
			}
			return origAppend(e)
		}
		exec := &jrrpRuntimeExecutor{
			output:   []byte(p.runtimeOutput),
			exitCode: p.exitCode,
			done:     make(chan struct{}),
		}
		resultCh := make(chan shellResult, 1)
		go func() {
			resultCh <- runShell(context.Background(), jm, exec, shellArgs{
				Command:        "runtime-finalizer-exhaustion",
				BlockTimeoutMS: 5000,
				MaxRuntimeMS:   100,
			})
		}()
		clk.BlockUntil(2)
		clk.Advance(100 * time.Millisecond)
		for i := 0; i < shellFinalizeAttempts-1; i++ {
			// The foreground block timer remains armed until runShell returns;
			// wait for it plus the retry sleep before advancing virtual time.
			clk.BlockUntil(2)
			clk.Advance(time.Second)
		}
		res := <-resultCh
		if res.JobID == "" || res.Status != string(jobstore.StatusFailed) || res.Reason != "finalize_failed" || !res.RunningInBackground || res.TimedOut {
			t.Fatalf("runtime finalizer exhaustion result = %+v", res)
		}
		if finishFailures != shellFinalizeAttempts {
			t.Fatalf("runtime finalizer failures = %d, want %d", finishFailures, shellFinalizeAttempts)
		}
		if done, ok := jobDone(jm, res.JobID); ok {
			<-done
		}
		recs, err := jm.store.Load()
		if err != nil {
			t.Fatalf("load detached runtime record: %v", err)
		}
		if rec := recs[res.JobID]; rec == nil || rec.Status != jobstore.StatusStopped || rec.Reason != "run_timeout" {
			t.Fatalf("detached runtime record = %+v", rec)
		}
	})
}

func jrrpStopState(t *testing.T, value any) jobStopResult {
	t.Helper()
	state, ok := value.(tooldefs.StateResult)
	if !ok {
		t.Fatalf("job_stop value type = %T, want StateResult", value)
	}
	b, err := json.Marshal(state.State)
	if err != nil {
		t.Fatalf("marshal job_stop state: %v", err)
	}
	var out jobStopResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal job_stop state: %v", err)
	}
	return out
}

func jrrpAssertSessionOutputAndStop(t *testing.T, p jrrpProgram) {
	t.Helper()
	s := newTestSession(t)
	jm := s.jobManager
	clk := agenttest.NewFakeClock()
	jm.clock = clk
	jm.now = clk.Now

	rec, err := jm.createShell(createShellOpts{Command: "scripted-session", Description: "fuzz runtime output"})
	if err != nil {
		t.Fatalf("create session job: %v", err)
	}
	jm.mu.Lock()
	run := jm.running[rec.JobID]
	jm.mu.Unlock()
	if run == nil || run.output == nil {
		t.Fatalf("session job %q has no runtime output store", rec.JobID)
	}
	if n, err := jm.appendJobOutput(rec.JobID, run.output, []byte(p.liveOutput)); err != nil || n != len(p.liveOutput) {
		t.Fatalf("append session job output = %d, %v", n, err)
	}

	statusValue, err := jobStatusTool(s, map[string]any{"job_id": rec.JobID}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("job_status: %v", err)
	}
	statusState, ok := statusValue.(tooldefs.StateResult)
	if !ok {
		t.Fatalf("job_status value type = %T, want StateResult", statusValue)
	}
	if !strings.Contains(statusState.Output, rec.JobID) {
		t.Fatalf("job_status output = %q, want job id", statusState.Output)
	}
	listValue, err := jobListTool(s, map[string]any{"limit": 10}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("job_list: %v", err)
	}
	listState, ok := listValue.(tooldefs.StateResult)
	if !ok || !strings.Contains(listState.Output, rec.JobID) {
		t.Fatalf("job_list value = %#v, want running job", listValue)
	}

	// Stop drives the public Session wrapper, its bounded done wait, and the
	// shell terminal precedence rule. The signal completes only the scripted run.
	var finalizeOnce sync.Once
	run.signal = func() {
		finalizeOnce.Do(func() {
			go func() {
				terminalStatus, terminalReason, exitCode := jm.shellTerminal(run, p.exitCode, false, nil)
				if err := jm.finalize(rec.JobID, terminalStatus, terminalReason, exitCode); err != nil {
					t.Errorf("finalize stopped session job: %v", err)
				}
			}()
		})
	}
	stopValue, err := jobStopTool(context.Background(), s, map[string]any{
		"job_id":      rec.JobID,
		"max_wait_ms": 1000,
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("job_stop: %v", err)
	}
	stop := jrrpStopState(t, stopValue)
	if stop.JobID != rec.JobID || stop.Status != string(jobstore.StatusCancelled) || stop.PreviousStatus != string(jobstore.StatusRunning) || stop.Outcome != "cancelled_by_request" {
		t.Fatalf("job_stop result = %+v", stop)
	}

	output, total, truncated, err := jm.readOutput(rec.JobID, shellInlineOutputBytes)
	if err != nil || output != p.liveOutput || total != int64(len(p.liveOutput)) || truncated {
		t.Fatalf("terminal session output = %q, %d, %v, %v", output, total, truncated, err)
	}
	head, headTotal, headTruncated, err := jm.readOutputHead(rec.JobID, shellInlineOutputBytes)
	if err != nil || head != p.liveOutput || headTotal != int64(len(p.liveOutput)) || headTruncated {
		t.Fatalf("terminal session output head = %q, %d, %v, %v", head, headTotal, headTruncated, err)
	}
	matches, err := jm.grepOutput(rec.JobID, regexp.MustCompile(regexp.QuoteMeta(p.marker)))
	if err != nil || len(matches) != 1 || matches[0].Line != p.marker {
		t.Fatalf("terminal session grep = %+v, %v", matches, err)
	}
}

// jrrpAssertPrunedOutputLifecycle runs a real OutputStore at a minimized
// retention cap. This makes the retained-tail contract cheap to fuzz while
// preserving the production job-manager and durable metadata paths.
func jrrpAssertPrunedOutputLifecycle(t *testing.T, p jrrpProgram) {
	t.Helper()
	s := newTestSession(t)
	jm := s.jobManager
	origOpenOutput := jm.openOutput
	jm.openOutput = func(path string, _ int64) (*jobstore.OutputStore, error) {
		return jobstore.OpenOutputNoSync(path, 64)
	}
	defer func() { jm.openOutput = origOpenOutput }()

	rec, err := jm.createShell(createShellOpts{Command: "retained-tail"})
	if err != nil {
		t.Fatalf("create pruned-output job: %v", err)
	}
	jm.mu.Lock()
	run := jm.running[rec.JobID]
	jm.mu.Unlock()
	if run == nil {
		t.Fatalf("pruned-output job %q is not running", rec.JobID)
	}
	payload := strings.Repeat("discard-", 24) + "retained-" + p.marker + "\n"
	if len(payload) <= 64 {
		t.Fatalf("pruned-output payload is too small: %d", len(payload))
	}
	if n, err := jm.appendJobOutput(rec.JobID, run.output, []byte(payload)); err != nil || n != len(payload) {
		t.Fatalf("append pruned-output payload = %d, %v", n, err)
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize pruned-output job: %v", err)
	}

	output, total, truncated, err := jm.readOutput(rec.JobID, shellInlineOutputBytes)
	if err != nil || total != int64(len(payload)) || !truncated || !strings.Contains(output, p.marker) {
		t.Fatalf("pruned terminal tail = %q, %d, %v, %v", output, total, truncated, err)
	}
	head, headTotal, headTruncated, err := jm.readOutputHead(rec.JobID, shellInlineOutputBytes)
	if err != nil || headTotal != int64(len(payload)) || !headTruncated || !strings.Contains(head, p.marker) {
		t.Fatalf("pruned terminal head = %q, %d, %v, %v", head, headTotal, headTruncated, err)
	}
	dropped, err := jm.outputDropped(rec.JobID)
	if err != nil || dropped <= 0 {
		t.Fatalf("pruned output dropped bytes = %d, %v", dropped, err)
	}
}

// jrrpAssertStructuredProjection verifies the schema-backed durable projection
// policy used by writeFinishJob. These are semantic persistence outcomes: a
// valid result survives, while missing, invalid, uncapturable, and oversized
// results are retained only as an explicit invalid reason.
func jrrpAssertStructuredProjection(t *testing.T, p jrrpProgram) {
	t.Helper()
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"required": []any{"value"},
	}
	value := map[string]any{"value": p.marker}
	got, valid, reason := boundedStructuredResult(value, schema, false)
	if valid == nil || !*valid || reason != "" {
		t.Fatalf("valid structured projection = %#v, %v, %q", got, valid, reason)
	}
	projected, ok := got.(map[string]any)
	if !ok || projected["value"] != p.marker {
		t.Fatalf("valid structured result = %#v", got)
	}

	for _, tc := range []struct {
		name         string
		value        any
		resultSchema any
		captureFail  bool
		reason       string
	}{
		{name: "missing", resultSchema: schema, reason: structuredResultReasonSchemaResultMissing},
		{name: "capture", resultSchema: schema, captureFail: true, reason: structuredResultReasonSchemaCaptureFailed},
		{name: "invalid value", value: map[string]any{"value": 7}, resultSchema: schema, reason: structuredResultReasonSchemaValidationFailed},
		{name: "invalid schema", value: value, resultSchema: map[string]any{"type": "not-a-json-schema-type"}, reason: structuredResultReasonSchemaValidationFailed},
		{name: "too large", value: strings.Repeat("x", maxPersistedStructuredResultJSONBytes), resultSchema: schema, reason: structuredResultReasonSchemaResultTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, valid, reason := boundedStructuredResult(tc.value, tc.resultSchema, tc.captureFail)
			if got != nil || valid == nil || *valid || reason != tc.reason {
				t.Fatalf("structured projection = %#v, %v, %q; want nil/false/%q", got, valid, reason, tc.reason)
			}
		})
	}
}

func jrrpAssertRestartReconciliation(t *testing.T, p jrrpProgram) {
	t.Helper()
	stateDir := t.TempDir()
	original, err := newJobManagerNoSync(stateDir, "RESTART", nil)
	if err != nil {
		t.Fatalf("new original job manager: %v", err)
	}
	freezeClock(original)
	rec, err := original.createShell(createShellOpts{Command: "scripted-restart"})
	if err != nil {
		t.Fatalf("create restart job: %v", err)
	}
	original.mu.Lock()
	run := original.running[rec.JobID]
	original.mu.Unlock()
	if run == nil {
		t.Fatalf("restart job %q is not running", rec.JobID)
	}
	if _, err := original.appendJobOutput(rec.JobID, run.output, []byte(p.liveOutput)); err != nil {
		t.Fatalf("append restart output: %v", err)
	}
	// Simulate a process restart: runtime state disappears without a terminal
	// event, while the durable started event and output remain on disk.
	original.abandonRunningJobs()
	if err := original.closeStoreOnly(); err != nil {
		t.Fatalf("close original job store: %v", err)
	}

	var notifications []jobNotification
	restarted, err := newJobManagerNoSync(stateDir, "RESTART", func(n jobNotification) {
		notifications = append(notifications, n)
	})
	if err != nil {
		t.Fatalf("new restarted job manager: %v", err)
	}
	freezeClock(restarted)
	defer func() {
		if err := restarted.close(); err != nil {
			t.Fatalf("close restarted job manager: %v", err)
		}
	}()
	if err := restarted.reconcileLostJobs(); err != nil {
		t.Fatalf("reconcile lost job: %v", err)
	}

	recs, err := restarted.store.Load()
	if err != nil {
		t.Fatalf("load reconciled job: %v", err)
	}
	recovered := recs[rec.JobID]
	if recovered == nil || recovered.Status != jobstore.StatusStopped || recovered.Reason != "runtime_lost" || recovered.OutputBytes != int64(len(p.liveOutput)) {
		t.Fatalf("reconciled record = %+v", recovered)
	}
	if len(notifications) != 1 || notifications[0].JobID != rec.JobID || notifications[0].Status != string(jobstore.StatusStopped) || notifications[0].OutputBytes != int64(len(p.liveOutput)) {
		t.Fatalf("reconcile notifications = %+v", notifications)
	}
	output, total, truncated, err := restarted.readOutput(rec.JobID, shellInlineOutputBytes)
	if err != nil || output != p.liveOutput || total != int64(len(p.liveOutput)) || truncated {
		t.Fatalf("reconciled output = %q, %d, %v, %v", output, total, truncated, err)
	}
	matches, err := restarted.grepOutput(rec.JobID, regexp.MustCompile(regexp.QuoteMeta(p.marker)))
	if err != nil || len(matches) != 1 || matches[0].Line != p.marker {
		t.Fatalf("reconciled grep = %+v, %v", matches, err)
	}
	if err := restarted.reconcileLostJobs(); err != nil {
		t.Fatalf("idempotent reconciliation: %v", err)
	}
	if len(notifications) != 1 {
		t.Fatalf("idempotent reconciliation re-enqueued notifications: %+v", notifications)
	}
}

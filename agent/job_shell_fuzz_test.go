//go:build serffuzz

package agent

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/fuzz/oracle"
)

// shfz_fakeExecutor is a StreamingExecutor that NEVER forks a real process. It
// writes a fuzzer-chosen sequence of output chunks to the job's OutputStore
// (synchronously, before returning the handle), then hands back a StreamHandle
// whose Wait returns a fuzzed exit code / error and whose Signal is a no-op. It
// honors the interface contract: on startErr it returns (nil, err); otherwise a
// non-nil handle with non-nil Wait/Signal (never a (nil,nil) stream).
type shfz_fakeExecutor struct {
	chunks   [][]byte
	exitCode int
	waitErr  error
	startErr error
	signaled chan struct{}
}

func (e *shfz_fakeExecutor) StreamCommand(_ context.Context, _ string, _ string, _ map[string]string, out io.Writer) (*execenv.StreamHandle, error) {
	if e.startErr != nil {
		return nil, e.startErr
	}
	// Writes flow through shellOutputWriter -> appendJobOutput -> OutputStore in
	// runShell's own goroutine, so the whole log is durable before Wait returns
	// and the foreground capture path reads a settled store.
	for _, c := range e.chunks {
		_, _ = out.Write(c)
	}
	return &execenv.StreamHandle{
		Wait: func() (int, error) { return e.exitCode, e.waitErr },
		Signal: func() {
			if e.signaled != nil {
				select {
				case <-e.signaled:
				default:
					close(e.signaled)
				}
			}
		},
	}, nil
}

// shfz_exitCodes spans the exit-code classes shellTerminal branches on: zero
// (completed), positive, a signal-ish 143, and negatives.
var shfz_exitCodes = []int{0, 1, 2, 127, 143, -1}

// shfz_foregroundInputs is the decoded fuzz program for the clean foreground
// path: a bounded output stream, an exit code, and whether Wait reports an error.
type shfz_foregroundInputs struct {
	chunks   [][]byte
	exitCode int
	waitErr  bool
}

// shfz_decodeForeground maps the fuzzer's bytes to bounded foreground inputs.
// Output is capped far below the 8 MiB retention cap so nothing is pruned and
// the captured tail equals the full stream (the faithful-output invariant).
func shfz_decodeForeground(data []byte) shfz_foregroundInputs {
	r := &seqReader{data: data}
	nChunks := r.intn(6)
	chunks := make([][]byte, 0, nChunks)
	for i := 0; i < nChunks; i++ {
		n := r.intn(200)
		b := make([]byte, n)
		for j := range b {
			b[j] = r.next()
		}
		chunks = append(chunks, b)
	}
	return shfz_foregroundInputs{
		chunks:   chunks,
		exitCode: shfz_exitCodes[r.intn(len(shfz_exitCodes))],
		waitErr:  r.next()&1 == 1,
	}
}

// shfz_summary is the comparable projection of a shellResult used by the
// determinism oracle. JobID is excluded because it is a fresh random ID per run.
type shfz_summary struct {
	Status       string
	Reason       string
	Output       string
	TotalBytes   int64
	DroppedBytes int64
	Truncated    bool
	Background   bool
	HasExit      bool
	ExitCode     int
}

func shfz_summarize(res shellResult) shfz_summary {
	s := shfz_summary{
		Status:       res.Status,
		Reason:       res.Reason,
		Output:       res.Output,
		TotalBytes:   res.TotalBytes,
		DroppedBytes: res.DroppedBytes,
		Truncated:    res.Truncated,
		Background:   res.RunningInBackground,
	}
	if res.ExitCode != nil {
		s.HasExit = true
		s.ExitCode = *res.ExitCode
	}
	return s
}

// shfz_runForeground drives one foreground runShell to completion on a fresh
// job manager and discards the ephemeral job, returning its summary. Each call
// is self-contained so it can be replayed for the determinism oracle.
func shfz_runForeground(t *testing.T, in shfz_foregroundInputs) shfz_summary {
	t.Helper()
	jm := newTestJM(t)
	defer func() { _ = jm.close() }()

	se := &shfz_fakeExecutor{chunks: in.chunks, exitCode: in.exitCode}
	if in.waitErr {
		se.waitErr = errFuzzShellWait
	}
	res := runShell(context.Background(), jm, se, shellArgs{
		Command:        "fuzz",
		BlockTimeoutMS: 60000,
	})
	if res.settle != nil {
		_ = res.settle(false)
	}
	return shfz_summarize(res)
}

// shfzShellError is a stable sentinel error class for the fake executor's
// Wait/StreamCommand failures, so the harness never depends on a real process.
type shfzShellError string

func (e shfzShellError) Error() string { return string(e) }

var errFuzzShellWait = shfzShellError("wait failed")

// FuzzShfz_RunShellForeground fuzzes the foreground shell path — the
// job-output capture/digest branch of runShell — with a fake StreamingExecutor
// that emits fuzzed output bytes and a fuzzed exit code (never a real process).
//
// Oracles:
//   - never panics (any decodable bytes);
//   - faithful bounded capture: the result's Output is exactly the concatenated
//     fake output, TotalBytes is its length, and (because output stays below the
//     retention cap) it is not truncated and drops nothing;
//   - exit-code contract: Wait error -> failed/wait_failed; else exit 0 ->
//     completed/exit_zero; else failed/exit_nonzero; ExitCode echoes the fake;
//   - deterministic: replaying the same inputs on a fresh manager yields an
//     identical result summary.
//
// Registry: native:agent:.:FuzzShfz_RunShellForeground::job_shell.go#runShell
func FuzzShfz_RunShellForeground(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 3, 'a', 'b', 'c', 0})            // one chunk, exit 0
	f.Add([]byte{2, 2, 'x', 'y', 2, 'z', 'w', 1, 1}) // two chunks, nonzero exit, wait err
	f.Add([]byte{3, 4, 1, 2, 3, 4, 0, 5, 'p', 'q', 'r', 's', 't', 3})
	f.Add([]byte{0, 3})                                                       // no chunks, exit 127
	f.Add([]byte{1, 10, 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 5}) // negative exit

	f.Fuzz(func(t *testing.T, data []byte) {
		in := shfz_decodeForeground(data)

		got := shfz_runForeground(t, in)

		want := bytes.Join(in.chunks, nil)
		if got.Output != string(want) {
			t.Fatalf("captured output not faithful: got %d bytes, want %d bytes", len(got.Output), len(want))
		}
		if got.TotalBytes != int64(len(want)) {
			t.Fatalf("TotalBytes = %d, want %d", got.TotalBytes, len(want))
		}
		if got.Truncated {
			t.Fatalf("Truncated = true for a %d-byte (sub-cap) output", len(want))
		}
		if got.DroppedBytes != 0 {
			t.Fatalf("DroppedBytes = %d, want 0 for a sub-cap output", got.DroppedBytes)
		}
		if got.Background {
			t.Fatalf("foreground within-bound result marked background: %+v", got)
		}

		wantStatus, wantReason := shfz_wantForegroundStatus(in)
		if got.Status != wantStatus || got.Reason != wantReason {
			t.Fatalf("status/reason = %q/%q, want %q/%q (exit=%d waitErr=%v)",
				got.Status, got.Reason, wantStatus, wantReason, in.exitCode, in.waitErr)
		}
		if !got.HasExit {
			t.Fatalf("foreground result missing exit code: %+v", got)
		}
		if got.ExitCode != in.exitCode {
			t.Fatalf("ExitCode = %d, want %d", got.ExitCode, in.exitCode)
		}

		// Determinism: a replay on a fresh manager reproduces the summary.
		oracle.Deterministic(t, func(_ int) shfz_summary {
			return shfz_runForeground(t, in)
		}, 0, func(a, b shfz_summary) bool { return a == b })
	})
}

func shfz_wantForegroundStatus(in shfz_foregroundInputs) (status, reason string) {
	switch {
	case in.waitErr:
		return string(jobstore.StatusFailed), "wait_failed"
	case in.exitCode == 0:
		return string(jobstore.StatusCompleted), "exit_zero"
	default:
		return string(jobstore.StatusFailed), "exit_nonzero"
	}
}

// shfz_knownStatuses is the closed set of statuses a shellResult may carry.
var shfz_knownStatuses = map[string]bool{
	string(jobstore.StatusRunning):   true,
	string(jobstore.StatusCompleted): true,
	string(jobstore.StatusFailed):    true,
	string(jobstore.StatusStopped):   true,
	string(jobstore.StatusCancelled): true,
}

// shfz_modeInputs is the decoded fuzz program for the mode/fault sweep.
type shfz_modeInputs struct {
	background bool
	startErr   bool
	cancelCtx  bool
	faultStart bool
	settleKeep bool
	fg         shfz_foregroundInputs
}

func shfz_decodeModes(data []byte) shfz_modeInputs {
	r := &seqReader{data: data}
	flags := r.next()
	m := shfz_modeInputs{
		background: flags&1 == 1,
		startErr:   flags&2 == 2,
		cancelCtx:  flags&4 == 4,
		faultStart: flags&8 == 8,
		settleKeep: flags&16 == 16,
	}
	m.fg = shfz_decodeForeground(r.data[r.pos:])
	return m
}

// FuzzShfz_RunShellModes sweeps the non-happy control paths of runShell with the
// same fake executor: background promotion, a start error (StreamCommand returns
// an error), a pre-cancelled tool context, and an injected persist fault on the
// job-started event. It never forks a process and never waits on a real timer
// (Wait returns promptly), so it stays fast and deterministic.
//
// Oracles (all under never-panic):
//   - the result is always a well-formed shellResult of the shell type with a
//     status drawn from the closed status set;
//   - a fake start error or a pre-cancelled context yields a clean
//     failed/start_failed result with no durable job — not a panic;
//   - a background start that both commits and finalizes reports a running job
//     with a job_id, and drains to a terminal record.
//
// Registry: native:agent:.:FuzzShfz_RunShellModes::job_shell.go#runShell
func FuzzShfz_RunShellModes(f *testing.F) {
	f.Add([]byte{0, 1, 3, 'a', 'b', 'c', 0})       // plain foreground
	f.Add([]byte{1, 0})                            // background, no output
	f.Add([]byte{2})                               // start error
	f.Add([]byte{4})                               // cancelled context
	f.Add([]byte{9, 1, 2, 'x', 'y', 0})            // background + fault on started event
	f.Add([]byte{16, 1, 4, 'd', 'a', 't', 'a', 0}) // foreground, settle keep
	f.Add([]byte{3})                               // background + start error

	f.Fuzz(func(t *testing.T, data []byte) {
		m := shfz_decodeModes(data)

		jm := newTestJM(t)
		defer func() { _ = jm.close() }()
		if m.faultStart {
			failAppendN(jm, jobstore.EventJobStarted, 1)
		}

		se := &shfz_fakeExecutor{
			chunks:   m.fg.chunks,
			exitCode: m.fg.exitCode,
			signaled: make(chan struct{}),
		}
		if m.fg.waitErr {
			se.waitErr = errFuzzShellWait
		}
		if m.startErr {
			se.startErr = errFuzzShellStart
		}

		ctx := context.Background()
		if m.cancelCtx {
			c, cancel := context.WithCancel(ctx)
			cancel()
			ctx = c
		}

		res := runShell(ctx, jm, se, shellArgs{
			Command:        "fuzz",
			Background:     m.background,
			BlockTimeoutMS: 60000,
		})

		if res.Type != string(jobstore.JobShell) {
			t.Fatalf("result type = %q, want shell", res.Type)
		}
		if !shfz_knownStatuses[res.Status] {
			t.Fatalf("result status = %q, not in the known status set", res.Status)
		}

		// A start error or a pre-cancelled context must fail cleanly with no
		// durable job. (A background fault on the started event is the same
		// clean start_failed shape but is checked implicitly below.)
		if m.startErr || m.cancelCtx {
			if res.Status != string(jobstore.StatusFailed) || res.Reason != "start_failed" {
				t.Fatalf("start-failure result = %+v, want failed/start_failed", res)
			}
			if res.JobID != "" || res.RunningInBackground {
				t.Fatalf("start-failure result must carry no durable job: %+v", res)
			}
			return
		}

		if m.background {
			if m.faultStart {
				if res.Status != string(jobstore.StatusFailed) || res.Reason != "start_failed" {
					t.Fatalf("background commit-fault = %+v, want failed/start_failed", res)
				}
				return
			}
			if res.JobID == "" || !res.RunningInBackground {
				t.Fatalf("background start = %+v, want running job with id", res)
			}
			shfz_drainBackground(t, jm, res.JobID)
			return
		}

		// Foreground within-bound: settle either commits (keep) or discards.
		if res.settle != nil {
			jobID := res.settle(m.settleKeep)
			if m.settleKeep && !m.faultStart && jobID == "" {
				t.Fatalf("settle(keep) returned no job_id for a healthy commit")
			}
			if !m.settleKeep && jobID != "" {
				t.Fatalf("settle(discard) returned a job_id %q", jobID)
			}
		}
	})
}

var errFuzzShellStart = shfzShellError("start failed")

// shfz_drainBackground waits for a promoted background job to reach its terminal
// done signal so the manager can be closed without abandoning a live job.
func shfz_drainBackground(t *testing.T, jm *jobManager, jobID string) {
	t.Helper()
	jm.mu.Lock()
	run := jm.running[jobID]
	var done chan struct{}
	if run != nil {
		done = run.done
	}
	jm.mu.Unlock()
	if done == nil {
		return // already finalized and reaped
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("background job %s did not finish", jobID)
	}
}
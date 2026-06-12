package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func newTestJM(t *testing.T) *jobManager {
	t.Helper()
	jm, err := newJobManager(t.TempDir(), "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	return jm
}

type feedKey struct {
	jm    *jobManager
	jobID string
}

var (
	feedOffsetsMu sync.Mutex
	feedOffsets   = map[feedKey]int64{}
)

// feedJob drives feedJobOutput with the running lifetime offset for a job,
// mirroring the production contract where each chunk's end offset is the job's
// cumulative output byte count. Tests that feed the matcher directly (without an
// OutputStore append) use this so the matcher's monotone-offset invariant holds
// across sequential feeds to the same job.
func feedJob(jm *jobManager, jobID string, chunk []byte) {
	feedOffsetsMu.Lock()
	key := feedKey{jm: jm, jobID: jobID}
	feedOffsets[key] += int64(len(chunk))
	end := feedOffsets[key]
	feedOffsetsMu.Unlock()
	jm.feedJobOutput(jobID, chunk, end)
}

func TestJobManagerCreateAndList(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "make test", Description: "tests"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.JobID == "" || rec.Type != jobstore.JobShell || rec.Status != jobstore.StatusRunning {
		t.Fatalf("bad record: %+v", rec)
	}
	jobs := jm.list(listFilter{})
	if len(jobs) != 1 || jobs[0].JobID != rec.JobID {
		t.Fatalf("list = %+v", jobs)
	}
}

// A running job's listed output_bytes must reflect the live retained output,
// not stay 0 until the job finishes — the contract's job_list example shows a
// running job with a non-zero count.
func TestJobListReportsLiveOutputBytesForRunningJob(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, jm, rec.JobID) })

	payload := []byte("live output so far\n")
	if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, payload); err != nil {
		t.Fatalf("append output: %v", err)
	}

	jobs := jm.list(listFilter{})
	listed := findListedJob(jobs, rec.JobID)
	if listed == nil {
		t.Fatalf("job %q not listed: %+v", rec.JobID, jobs)
	}
	if listed.Status != jobstore.StatusRunning {
		t.Fatalf("status = %q, want running", listed.Status)
	}
	if listed.OutputBytes != int64(len(payload)) {
		t.Fatalf("output_bytes = %d, want %d (live retained bytes)", listed.OutputBytes, len(payload))
	}
}

func TestAbandonRunningJobsClosesCapturedDoneChannels(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done, ok := jobDone(jm, rec.JobID)
	if !ok {
		t.Fatalf("jobDone(%q) not found", rec.JobID)
	}

	jm.abandonRunningJobs()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("captured done channel did not close after abandon")
	}
	if _, ok := jobDone(jm, rec.JobID); ok {
		t.Fatalf("job %q still has a running done channel after abandon", rec.JobID)
	}
}

func TestJobManagerReadOutput(t *testing.T) {
	jm := newTestJM(t)
	rec, _ := jm.createShell(createShellOpts{Command: "x"})
	_, _ = jm.running[rec.JobID].output.Append([]byte("hello\n"))
	content, _, _, err := jm.readOutput(rec.JobID, 1024)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if content != "hello\n" {
		t.Errorf("content = %q", content)
	}
}

func TestJobManagerReadOutputTerminalLog(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := jm.running[rec.JobID].output.Append([]byte("hello\nworld\n")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	content, total, truncated, err := jm.readOutput(rec.JobID, len("world\n"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if content != "world\n" || total != int64(len("hello\nworld\n")) || !truncated {
		t.Fatalf("readOutput = %q, %d, %v", content, total, truncated)
	}
}

func TestJobManagerReadOutputMissingTerminalLogReturnsError(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := jm.running[rec.JobID].output.Append([]byte("hello\n")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := os.Remove(rec.OutputPath); err != nil {
		t.Fatalf("remove output: %v", err)
	}

	content, total, truncated, err := jm.readOutput(rec.JobID, 1024)
	if err == nil {
		t.Fatalf("readOutput content=%q total=%d truncated=%v, want error", content, total, truncated)
	}
	if _, statErr := os.Stat(rec.OutputPath); !os.IsNotExist(statErr) {
		t.Fatalf("output stat after readOutput = %v, want not exist", statErr)
	}
}

func TestJobManagerTerminalOutputValidatesRetainedSidecar(t *testing.T) {
	jm := newTestJM(t)
	jobID := "job_terminal"
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	out, err := jobstore.OpenOutput(outputPath, int64(len("keep\n")))
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	appendOutput := func(s string) {
		t.Helper()
		if _, err := out.Append([]byte(s)); err != nil {
			t.Fatalf("append output %q: %v", s, err)
		}
	}
	appendOutput("drop\n")
	appendOutput("keep\n")
	if err := out.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	start := time.Unix(1, 0).UTC()
	end := time.Unix(2, 0).UTC()
	if err := jm.store.Append(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		OwnerSessionID:   "S1",
		VisibleToSession: "S1",
		StartedAt:        &start,
		OutputPath:       outputPath,
	}); err != nil {
		t.Fatalf("append start event: %v", err)
	}
	if err := jm.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		JobID:       jobID,
		Status:      jobstore.StatusCompleted,
		EndedAt:     &end,
		OutputBytes: int64(len("drop\nkeep\n")),
		TerminalGen: "GEN1",
	}); err != nil {
		t.Fatalf("append finish event: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte("more\n"), 0o644); err != nil {
		t.Fatalf("replace retained output: %v", err)
	}

	if content, total, truncated, err := jm.readOutput(jobID, 1024); err == nil {
		t.Fatalf("readOutput content=%q total=%d truncated=%v, want sidecar validation error", content, total, truncated)
	}
	if matches, err := jm.grepOutput(jobID, regexp.MustCompile(`more`)); err == nil {
		t.Fatalf("grepOutput matches=%+v, want sidecar validation error", matches)
	}
}

func TestJobManagerRunningRecordOutputUsesSidecarTotal(t *testing.T) {
	jm := newTestJM(t)
	jobID := "job_running_forwarded"
	outputPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	out, err := jobstore.OpenOutput(outputPath, maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	if _, err := out.Append([]byte("still running\n")); err != nil {
		t.Fatalf("append output: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	start := time.Unix(1, 0).UTC()
	if err := jm.store.Append(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		JobID:            jobID,
		Type:             jobstore.JobDelegate,
		Status:           jobstore.StatusRunning,
		OwnerSessionID:   "child",
		VisibleToSession: "S1",
		StartedAt:        &start,
		OutputPath:       outputPath,
	}); err != nil {
		t.Fatalf("append start event: %v", err)
	}

	content, total, truncated, err := jm.readOutput(jobID, 1024)
	if err != nil {
		t.Fatalf("readOutput returned error: %v", err)
	}
	if content != "still running\n" || total != int64(len("still running\n")) || truncated {
		t.Fatalf("readOutput = %q, %d, %v", content, total, truncated)
	}
	matches, err := jm.grepOutput(jobID, regexp.MustCompile(`running`))
	if err != nil {
		t.Fatalf("grepOutput returned error: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != "still running" {
		t.Fatalf("grepOutput matches = %+v, want retained running output", matches)
	}
}

func TestJobOutputReadLimitRemainsModelFacingCap(t *testing.T) {
	if maxJobOutputBytes != 1024*1024 {
		t.Fatalf("maxJobOutputBytes = %d, want 1 MiB", maxJobOutputBytes)
	}
	if maxJobOutputRetentionBytes != 8*1024*1024 {
		t.Fatalf("maxJobOutputRetentionBytes = %d, want 8 MiB", maxJobOutputRetentionBytes)
	}

	got, err := boundedJobBytesArg(map[string]any{"tail_bytes": maxJobOutputRetentionBytes}, "tail_bytes", defaultJobOutputBytes)
	if err != nil {
		t.Fatalf("boundedJobBytesArg: %v", err)
	}
	if got != maxJobOutputBytes {
		t.Fatalf("bounded tail_bytes = %d, want model-facing cap %d", got, maxJobOutputBytes)
	}
}

func TestJobOutputShellStoreEnforcesRetentionCap(t *testing.T) {
	jm := newTestJM(t)
	t.Cleanup(func() { _ = jm.close() })
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	assertJobOutputRetentionCap(t, jm.running[rec.JobID])
}

func TestJobOutputDelegateStoreEnforcesRetentionCap(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		running: true,
		status:  SubagentRunning,
		done:    make(chan struct{}),
	}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), "retain delegate output", sub)
	if err != nil {
		t.Fatalf("attachDelegateJob: %v", err)
	}

	assertJobOutputRetentionCap(t, run)
}

func assertJobOutputRetentionCap(t *testing.T, run *runningJob) {
	t.Helper()
	data := bytes.Repeat([]byte("x"), maxJobOutputRetentionBytes+1)
	n, err := run.output.Append(data)
	if err != nil {
		t.Fatalf("append retained output: %v", err)
	}
	if n != len(data) {
		t.Fatalf("append wrote %d bytes, want %d", n, len(data))
	}

	info, err := os.Stat(run.rec.OutputPath)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() != maxJobOutputRetentionBytes {
		t.Fatalf("retained output size = %d, want %d", info.Size(), maxJobOutputRetentionBytes)
	}
	_, total, truncated, err := run.output.Tail(maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatalf("tail retained output: %v", err)
	}
	if total != int64(len(data)) || !truncated {
		t.Fatalf("tail total=%d truncated=%v, want lifetime %d and truncated", total, truncated, len(data))
	}
}

func TestJobManagerStopMarksLiveJobCancelled(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := jm.stop(rec.JobID); err != nil {
		t.Fatalf("stop: %v", err)
	}

	run := jm.running[rec.JobID]
	if run == nil {
		t.Fatal("running job missing after stop")
	}
	status, reason, exitCode := jm.shellTerminal(run, 143, false, nil)
	if err := jm.finalize(rec.JobID, status, reason, exitCode); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := recs[rec.JobID]
	if got.Status != jobstore.StatusCancelled || got.Reason != "stopped_by_parent" {
		t.Fatalf("record = %+v, want cancelled/stopped_by_parent", got)
	}
}

func TestJobManagerCloseMarksRunningJobsCancelled(t *testing.T) {
	jm := newTestJM(t)
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	run := jm.running[rec.JobID]
	run.signal = func() {
		status, reason, exitCode := jm.shellTerminal(run, 143, false, nil)
		_ = jm.finalize(rec.JobID, status, reason, exitCode)
	}

	if err := jm.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st, err := jobstore.Open(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	recs, err := st.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := recs[rec.JobID]
	if got.Status != jobstore.StatusCancelled || got.Reason != "stopped_by_parent" {
		t.Fatalf("record = %+v, want cancelled/stopped_by_parent", got)
	}
}

func TestJobManagerCloseContinuesAfterWatchSendCleanupFailure(t *testing.T) {
	jm := newTestJM(t)
	seedCommonWatchSendTargets(t, jm)
	rec, err := jm.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := jm.configureWatch(watchArgs{
		Target:      rec.JobID,
		OutputMatch: "ready",
		Send:        &watchSendArgs{To: "job_obs", Message: "observe"},
	}); err != nil {
		t.Fatalf("configure watch: %v", err)
	}
	pendingKey := jobstore.WatchSendKey{
		VisibleSessionID:        jm.sessionID,
		WatchTarget:             rec.JobID,
		ResolvedWatchedIdentity: rec.JobID,
		ResolvedSendTo:          "job_obs",
	}
	jm.mu.Lock()
	var cfg *watchConfig
	for _, candidate := range jm.watches {
		cfg = candidate
	}
	if cfg == nil {
		jm.mu.Unlock()
		t.Fatal("configured watch not found")
	}
	pendingKey.WatchGeneration = cfg.generation
	if cfg.pending == nil {
		cfg.pending = make(map[jobstore.WatchSendKey]*jobstore.WatchSendState)
	}
	cfg.pendingOrder = append(cfg.pendingOrder, pendingKey)
	cfg.pending[pendingKey] = &jobstore.WatchSendState{
		Key:        pendingKey,
		DeliveryID: jobstore.NewWatchSendDeliveryID(),
		Message:    "observe",
	}
	jm.mu.Unlock()
	run := jm.running[rec.JobID]
	run.signal = func() {
		status, reason, exitCode := jm.shellTerminal(run, 143, false, nil)
		_ = jm.finalize(rec.JobID, status, reason, exitCode)
	}

	cleanupErr := errors.New("drop watch send failed")
	realAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventWatchSendDropped {
			return cleanupErr
		}
		return realAppend(e)
	}

	if err := jm.close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("close error = %v, want watch cleanup error", err)
	}

	st, err := jobstore.Open(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()
	recs, err := st.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := recs[rec.JobID]
	if got.Status != jobstore.StatusCancelled || got.Reason != "stopped_by_parent" {
		t.Fatalf("record = %+v, want cancelled/stopped_by_parent despite watch cleanup failure", got)
	}
}

func TestGrepOutputFileSkipsOverlongLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job_A.log")
	overlong := strings.Repeat("x", maxJobGrepLineBytes+1024) + "ready\n"
	if err := os.WriteFile(path, []byte(overlong+"later ready\n"), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}

	matches, err := grepOutputFile(path, regexp.MustCompile(`ready`), 4096, 0)
	if err != nil {
		t.Fatalf("grepOutputFile: %v", err)
	}
	if len(matches) != 1 || matches[0].Line != "later ready" {
		t.Fatalf("matches = %+v, want only bounded later line", matches)
	}
	if matches[0].ByteOffset != int64(len(overlong)) {
		t.Fatalf("byte offset = %d, want %d", matches[0].ByteOffset, len(overlong))
	}
}

func TestJobManagerCreateDoesNotPersistWhenOutputOpenFails(t *testing.T) {
	jm := newTestJM(t)
	outputDir := filepath.Join(jm.dir, "jobs")
	if err := os.RemoveAll(outputDir); err != nil {
		t.Fatalf("remove jobs dir: %v", err)
	}
	if err := os.WriteFile(outputDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("replace jobs dir: %v", err)
	}

	if _, err := jm.createShell(createShellOpts{Command: "x"}); err == nil {
		t.Fatal("createShell succeeded with invalid output dir")
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("records = %+v, want none", recs)
	}
}

func TestJobManagerListWithErrorSurfacesLoadFailure(t *testing.T) {
	jm := newTestJM(t)
	if err := jm.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	if _, err := jm.listWithError(listFilter{}); err == nil {
		t.Fatal("listWithError returned nil error")
	}
	if jobs := jm.list(listFilter{}); jobs != nil {
		t.Fatalf("list = %+v, want nil on load error", jobs)
	}
}

func TestJobManagerFinalize(t *testing.T) {
	var queued []jobNotification
	jm, err := newJobManager(t.TempDir(), "S1", func(n jobNotification) {
		queued = append(queued, n)
	})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }

	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := jm.running[rec.JobID].done
	_, _ = jm.running[rec.JobID].output.Append([]byte("hello\n"))

	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	select {
	case <-done:
	default:
		t.Fatal("done was not closed")
	}
	if _, ok := jm.running[rec.JobID]; ok {
		t.Fatal("job still running")
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := recs[rec.JobID]
	if got.Status != jobstore.StatusCompleted || got.Reason != "exit_zero" || got.OutputBytes != int64(len("hello\n")) {
		t.Fatalf("record = %+v", got)
	}
	if got.NotifyState != jobstore.NotifyPending {
		t.Fatalf("notify state = %q, want pending", got.NotifyState)
	}
	if len(queued) != 1 || queued[0].JobID != rec.JobID || queued[0].Status != string(jobstore.StatusCompleted) {
		t.Fatalf("queued = %+v", queued)
	}
}

func TestJobManagerFinalizeFinishAppendFailureKeepsRuntime(t *testing.T) {
	var queued []jobNotification
	jm, err := newJobManager(t.TempDir(), "S1", func(n jobNotification) {
		queued = append(queued, n)
	})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := jm.running[rec.JobID].done

	appendErr := errors.New("finish append failed")
	origAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobFinished {
			return appendErr
		}
		return origAppend(e)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); !errors.Is(err, appendErr) {
		t.Fatalf("finalize error = %v, want %v", err, appendErr)
	}
	select {
	case <-done:
		t.Fatal("done closed after failed terminal append")
	default:
	}
	if _, ok := jm.running[rec.JobID]; !ok {
		t.Fatal("job removed after failed terminal append")
	}
	if _, err := jm.running[rec.JobID].output.Append([]byte("still running\n")); err != nil {
		t.Fatalf("output append after failed terminal append: %v", err)
	}
	if len(queued) != 0 {
		t.Fatalf("queued = %+v, want none", queued)
	}

	jm.appendEvent = origAppend
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("done was not closed after retry")
	}
}

func TestJobManagerFinalizePendingAppendFailureCanRetryWithSameGeneration(t *testing.T) {
	var queued []jobNotification
	jm, err := newJobManager(t.TempDir(), "S1", func(n jobNotification) {
		queued = append(queued, n)
	})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := jm.running[rec.JobID].done

	appendErr := errors.New("pending append failed")
	origAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobNotificationPending {
			return appendErr
		}
		return origAppend(e)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); !errors.Is(err, appendErr) {
		t.Fatalf("finalize error = %v, want %v", err, appendErr)
	}
	select {
	case <-done:
		t.Fatal("done closed after failed notification-pending append")
	default:
	}
	if _, ok := jm.running[rec.JobID]; !ok {
		t.Fatal("job removed after failed notification-pending append")
	}
	if len(queued) != 0 {
		t.Fatalf("queued = %+v, want none", queued)
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := recs[rec.JobID]
	if got.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %q, want completed", got.Status)
	}
	if got.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("notify state = %q, want not_armed", got.NotifyState)
	}
	jobs := jm.list(listFilter{})
	if len(jobs) != 1 || jobs[0].Status != jobstore.StatusCompleted {
		t.Fatalf("list during retry window = %+v", jobs)
	}
	firstGeneration := got.TerminalGen
	if firstGeneration == "" {
		t.Fatal("terminal generation is empty")
	}

	jm.appendEvent = origAppend
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("done was not closed after retry")
	}
	if _, ok := jm.running[rec.JobID]; ok {
		t.Fatal("job still running after retry")
	}
	if len(queued) != 1 || queued[0].JobID != rec.JobID {
		t.Fatalf("queued = %+v, want one job notification", queued)
	}
	recs, err = jm.store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got = recs[rec.JobID]
	if got.NotifyState != jobstore.NotifyPending {
		t.Fatalf("notify state after retry = %q, want pending", got.NotifyState)
	}
	if got.TerminalGen != firstGeneration {
		t.Fatalf("terminal generation after retry = %q, want %q", got.TerminalGen, firstGeneration)
	}
}

func TestJobManagerFinalizeConcurrentArmDoesNotDoubleNotify(t *testing.T) {
	var queued int32
	jm, err := newJobManager(t.TempDir(), "S1", func(jobNotification) {
		atomic.AddInt32(&queued, 1)
	})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := jm.running[rec.JobID].done

	pendingStarted := make(chan struct{})
	releasePending := make(chan struct{})
	var pendingAppends int32
	origAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobNotificationPending {
			if atomic.AddInt32(&pendingAppends, 1) == 1 {
				close(pendingStarted)
				<-releasePending
			}
		}
		return origAppend(e)
	}

	errc := make(chan error, 1)
	go func() {
		errc <- jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil)
	}()

	select {
	case <-pendingStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first pending append")
	}
	waitErrc := make(chan error, 1)
	go func() {
		waitErrc <- jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil)
	}()
	select {
	case err := <-waitErrc:
		t.Fatalf("concurrent finalize returned before pending append completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if got := atomic.LoadInt32(&pendingAppends); got != 1 {
		t.Fatalf("pending appends before release = %d, want 1", got)
	}

	close(releasePending)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("first finalize: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first finalize")
	}
	select {
	case err := <-waitErrc:
		if err != nil {
			t.Fatalf("concurrent finalize: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for concurrent finalize")
	}
	select {
	case <-done:
	default:
		t.Fatal("done was not closed")
	}
	if _, ok := jm.running[rec.JobID]; ok {
		t.Fatal("job still running")
	}
	if got := atomic.LoadInt32(&pendingAppends); got != 1 {
		t.Fatalf("pending appends = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&queued); got != 1 {
		t.Fatalf("queued = %d, want 1", got)
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if recs[rec.JobID].NotifyState != jobstore.NotifyPending {
		t.Fatalf("notify state = %q, want pending", recs[rec.JobID].NotifyState)
	}
}

func TestJobManagerFinalizeConcurrentArmWaitsForPendingFailure(t *testing.T) {
	var queued int32
	jm, err := newJobManager(t.TempDir(), "S1", func(jobNotification) {
		atomic.AddInt32(&queued, 1)
	})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := jm.running[rec.JobID].done

	appendErr := errors.New("pending append failed")
	pendingStarted := make(chan struct{})
	releasePending := make(chan struct{})
	var pendingAppends int32
	origAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobNotificationPending {
			if atomic.AddInt32(&pendingAppends, 1) == 1 {
				close(pendingStarted)
				<-releasePending
				return appendErr
			}
		}
		return origAppend(e)
	}

	errc := make(chan error, 1)
	go func() {
		errc <- jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil)
	}()

	select {
	case <-pendingStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first pending append")
	}
	waitErrc := make(chan error, 1)
	go func() {
		waitErrc <- jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil)
	}()
	select {
	case err := <-waitErrc:
		t.Fatalf("concurrent finalize returned before pending append failed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releasePending)
	select {
	case err := <-errc:
		if !errors.Is(err, appendErr) {
			t.Fatalf("first finalize error = %v, want %v", err, appendErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first finalize")
	}
	select {
	case err := <-waitErrc:
		if !errors.Is(err, appendErr) {
			t.Fatalf("concurrent finalize error = %v, want %v", err, appendErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for concurrent finalize")
	}
	select {
	case <-done:
		t.Fatal("done closed after failed notification-pending append")
	default:
	}
	if _, ok := jm.running[rec.JobID]; !ok {
		t.Fatal("job removed after failed notification-pending append")
	}
	if got := atomic.LoadInt32(&pendingAppends); got != 1 {
		t.Fatalf("pending appends = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&queued); got != 0 {
		t.Fatalf("queued = %d, want 0", got)
	}
}

func TestJobManagerArmPendingTerminalNotificationsRecoversAfterRestart(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	appendErr := errors.New("pending append failed")
	origAppend := jm.appendEvent
	jm.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobNotificationPending {
			return appendErr
		}
		return origAppend(e)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); !errors.Is(err, appendErr) {
		t.Fatalf("finalize error = %v, want %v", err, appendErr)
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	firstGeneration := recs[rec.JobID].TerminalGen
	if recs[rec.JobID].NotifyState != jobstore.NotifyNotArmed || firstGeneration == "" {
		t.Fatalf("record before recovery = %+v", recs[rec.JobID])
	}

	var queued []jobNotification
	restarted, err := newJobManager(stateDir, "S1", func(n jobNotification) {
		queued = append(queued, n)
	})
	if err != nil {
		t.Fatalf("restart newJobManager: %v", err)
	}
	restarted.now = func() time.Time { return time.Unix(1001, 0).UTC() }

	if err := restarted.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("arm pending: %v", err)
	}
	recs, err = restarted.store.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := recs[rec.JobID]
	if got.NotifyState != jobstore.NotifyPending {
		t.Fatalf("notify state = %q, want pending", got.NotifyState)
	}
	if got.TerminalGen != firstGeneration {
		t.Fatalf("terminal generation = %q, want %q", got.TerminalGen, firstGeneration)
	}
	if len(queued) != 1 || queued[0].JobID != rec.JobID || queued[0].Status != string(jobstore.StatusCompleted) {
		t.Fatalf("queued = %+v", queued)
	}
}

func TestJobManagerArmPendingTerminalNotificationsEnqueuesAlreadyPending(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if recs[rec.JobID].NotifyState != jobstore.NotifyPending {
		t.Fatalf("notify state before restart = %q, want pending", recs[rec.JobID].NotifyState)
	}

	var queued []jobNotification
	restarted, err := newJobManager(stateDir, "S1", func(n jobNotification) {
		queued = append(queued, n)
	})
	if err != nil {
		t.Fatalf("restart newJobManager: %v", err)
	}
	restarted.appendEvent = func(e jobstore.Event) error {
		if e.Kind == jobstore.EventJobNotificationPending {
			t.Fatalf("unexpected pending append for already-pending record: %+v", e)
		}
		return restarted.store.Append(e)
	}

	if err := restarted.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("arm pending: %v", err)
	}
	if len(queued) != 1 || queued[0].JobID != rec.JobID || queued[0].Status != string(jobstore.StatusCompleted) {
		t.Fatalf("queued = %+v", queued)
	}
}

func TestJobManagerArmPendingTerminalNotificationsSkipsDelivered(t *testing.T) {
	stateDir := t.TempDir()
	jm, err := newJobManager(stateDir, "S1", func(jobNotification) {})
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	jm.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := jm.store.Append(jobstore.Event{
		Kind:        jobstore.EventJobNotificationDelivered,
		TS:          jm.now(),
		JobID:       rec.JobID,
		TerminalGen: recs[rec.JobID].TerminalGen,
	}); err != nil {
		t.Fatalf("append delivered: %v", err)
	}

	var queued []jobNotification
	restarted, err := newJobManager(stateDir, "S1", func(n jobNotification) {
		queued = append(queued, n)
	})
	if err != nil {
		t.Fatalf("restart newJobManager: %v", err)
	}
	if err := restarted.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("arm pending: %v", err)
	}
	if len(queued) != 0 {
		t.Fatalf("queued = %+v, want none", queued)
	}
}

func TestJobManagerCloseClosesStoreAfterRuntimeWaitTimeout(t *testing.T) {
	jm := newTestJM(t)
	run := &runningJob{
		rec:  &jobstore.JobRecord{JobID: "job_hung"},
		done: make(chan struct{}),
	}
	jm.running[run.rec.JobID] = run

	start := time.Now()
	err := jm.close()
	if err == nil {
		t.Fatal("close error = nil, want timeout")
	}
	if time.Since(start) > 6*time.Second {
		t.Fatal("close waited longer than expected")
	}
	if len(jm.running) != 0 {
		t.Fatalf("running jobs after timed-out close = %d, want 0", len(jm.running))
	}
	if _, loadErr := jm.store.Load(); !errors.Is(loadErr, jobstore.ErrStoreClosed) {
		t.Fatalf("store.Load after timed-out close err = %v, want ErrStoreClosed", loadErr)
	}
}

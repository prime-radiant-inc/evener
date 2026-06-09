package agent

import (
	"errors"
	"os"
	"path/filepath"
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

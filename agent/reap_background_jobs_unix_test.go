//go:build unix

package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/execsupport/procgroup"
)

// TestBackgroundShellJobPersistsProcessIdentity pins that a background shell
// job's durable record names the process group its command runs in and the
// group leader's start time, and that the recorded group is the job's own: it
// is alive while the job runs and gone once the job is stopped and reaped.
func TestBackgroundShellJobPersistsProcessIdentity(t *testing.T) {
	t.Parallel()
	jm, env := newShellTestRig(t)
	t.Cleanup(func() { _ = jm.close() })
	result := runShell(t.Context(), jm, env, shellArgs{Command: "sleep 30", Background: true})
	if result.JobID == "" || !result.RunningInBackground {
		t.Fatalf("background launch: %+v", result)
	}
	rec := loadShellRecord(t, jm, result.JobID)
	if !rec.Background || rec.Status != jobstore.StatusRunning {
		t.Fatalf("durable record does not describe a running background job: %+v", rec)
	}
	if rec.ProcessGroupID <= 0 {
		t.Fatalf("background job recorded no process group: %+v", rec)
	}
	if rec.ProcessStartTime == nil {
		t.Fatalf("background job recorded no process start time: %+v", rec)
	}
	live, ok := processStartedAt(rec.ProcessGroupID)
	if !ok || !live.Equal(*rec.ProcessStartTime) {
		t.Fatalf("recorded start time %v does not name the live group leader (%v, ok=%v)",
			*rec.ProcessStartTime, live, ok)
	}
	if err := syscall.Kill(-rec.ProcessGroupID, 0); err != nil {
		t.Fatalf("recorded group %d is not live while the job runs: %v", rec.ProcessGroupID, err)
	}
	if _, err := jm.stop(result.JobID); err != nil {
		t.Fatal(err)
	}
	waitForShellDone(t, jm, result.JobID)
	if err := syscall.Kill(-rec.ProcessGroupID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("recorded group %d outlived its job: %v", rec.ProcessGroupID, err)
	}
}

// TestReapSessionBackgroundJobsReapsVerifiedSurvivor proves the crash path: a
// durable running record whose process group is still alive and whose leader
// still has the recorded start time is signaled until it ends.
func TestReapSessionBackgroundJobsReapsVerifiedSurvivor(t *testing.T) {
	t.Parallel()
	stateDir, sessionID := t.TempDir(), "sess_reap_survivor"
	sleeper := startProcessGroup(t)
	writeRunningBackgroundRecord(t, stateDir, sessionID, "job_reap_survivor", sleeper.pid, sleeper.started)

	if reaped := ReapSessionBackgroundJobs(stateDir, sessionID); reaped != 1 {
		t.Fatalf("reaped %d groups, want 1", reaped)
	}
	exitErr, ended := sleeper.waitWithin(t, 10*time.Second)
	if !ended {
		t.Fatal("reaped process group is still running")
	}
	if !signalledExit(exitErr) {
		t.Fatalf("reaped process ended without a signal: %v", exitErr)
	}
}

// TestReapSessionBackgroundJobsLeavesRecycledIdentityAlone is the PID-reuse
// guard: a start time that does not match the live leader means the pid/pgid was
// recycled, and the unrelated group must survive untouched.
func TestReapSessionBackgroundJobsLeavesRecycledIdentityAlone(t *testing.T) {
	t.Parallel()
	stateDir, sessionID := t.TempDir(), "sess_reap_recycled"
	sleeper := startProcessGroup(t)
	stale := sleeper.started.Add(-time.Hour)
	writeRunningBackgroundRecord(t, stateDir, sessionID, "job_reap_recycled", sleeper.pid, stale)

	if reaped := ReapSessionBackgroundJobs(stateDir, sessionID); reaped != 0 {
		t.Fatalf("reaped %d groups, want none", reaped)
	}
	if !sleeper.running(t) {
		t.Fatal("an unrelated process group was signaled for a recycled pid/pgid")
	}
}

// TestReapSessionBackgroundJobsLeavesDetachedAndUnrecordedAlone pins the
// boundary: only a running background record with a verified group is reaped. A
// detached command leaves no record at all, a foreground job's record is not
// background, a record with no group names nothing, and each of those groups
// survives.
func TestReapSessionBackgroundJobsLeavesDetachedAndUnrecordedAlone(t *testing.T) {
	t.Parallel()
	stateDir, sessionID := t.TempDir(), "sess_reap_boundary"
	detached := startProcessGroup(t) // no durable record, like a detached command
	foreground := startProcessGroup(t)
	writeRecord(t, stateDir, sessionID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: time.Now(), JobID: "job_foreground",
		Type: jobstore.JobShell, OwnerSessionID: sessionID,
		ProcessGroupID: foreground.pid, ProcessStartTime: &foreground.started,
	})
	ungrouped := startProcessGroup(t)
	writeRecord(t, stateDir, sessionID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: time.Now(), JobID: "job_ungrouped",
		Type: jobstore.JobShell, Background: true, OwnerSessionID: sessionID,
	})

	if reaped := ReapSessionBackgroundJobs(stateDir, sessionID); reaped != 0 {
		t.Fatalf("reaped %d groups, want none", reaped)
	}
	for _, proc := range []*processGroup{detached, foreground, ungrouped} {
		if !proc.running(t) {
			t.Fatalf("process group %d was reaped but must not be", proc.pid)
		}
	}
}

// TestReapSessionBackgroundJobsUsesTheReadablePrefixOfADamagedJournal proves a
// reap of a journal whose middle is damaged: the records before the damage are
// still true statements, so a running background record among them still names
// a group to reap. Only a journal with nothing decodable reaps nothing.
func TestReapSessionBackgroundJobsUsesTheReadablePrefixOfADamagedJournal(t *testing.T) {
	t.Parallel()
	stateDir, sessionID := t.TempDir(), "sess_reap_damaged"
	sleeper := startProcessGroup(t)
	writeRunningBackgroundRecord(t, stateDir, sessionID, "job_reap_damaged", sleeper.pid, sleeper.started)
	path := filepath.Join(schema.SessionWorkDir(stateDir, sessionID), "jobs.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("this line is not a job event\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if reaped := ReapSessionBackgroundJobs(stateDir, sessionID); reaped != 1 {
		t.Fatalf("reaped %d groups from a damaged journal, want 1", reaped)
	}
	if _, ended := sleeper.waitWithin(t, 10*time.Second); !ended {
		t.Fatal("reaped process group is still running")
	}
}

// TestReapSessionBackgroundJobsToleratesMissingJournal keeps recovery quiet for a
// session that never ran a job: nothing to read is nothing to reap.
func TestReapSessionBackgroundJobsToleratesMissingJournal(t *testing.T) {
	t.Parallel()
	if reaped := ReapSessionBackgroundJobs(t.TempDir(), "sess_reap_missing"); reaped != 0 {
		t.Fatalf("reaped %d groups from a missing journal, want none", reaped)
	}
}

// processGroup is a real command in its own process group, the shape a
// background shell job takes.
type processGroup struct {
	pid     int
	started time.Time
	cmd     *exec.Cmd
}

func startProcessGroup(t *testing.T) *processGroup {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = procgroup.SysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process group: %v", err)
	}
	started, ok := processStartedAt(cmd.Process.Pid)
	if !ok {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("start time unavailable for a freshly started child")
	}
	proc := &processGroup{pid: cmd.Process.Pid, started: started, cmd: cmd}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return proc
}

// running reports whether the group leader is still alive. A reaped record's
// group is the only thing the reaper may end.
func (p *processGroup) running(t *testing.T) bool {
	t.Helper()
	return syscall.Kill(-p.pid, 0) == nil
}

// waitWithin waits for the process to end, returning its exit error and whether
// it ended at all. A process that never ends is killed so the test can report
// the failure instead of hanging.
func (p *processGroup) waitWithin(t *testing.T, d time.Duration) (*exec.ExitError, bool) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return exitErr, true
		}
		if err != nil {
			t.Fatalf("wait process group: %v", err)
		}
		return nil, true
	case <-time.After(d):
		_ = p.cmd.Process.Kill()
		<-done
		return nil, false
	}
}

// signalledExit reports whether the process ended on a signal rather than
// exiting on its own.
func signalledExit(err *exec.ExitError) bool {
	status, ok := err.Sys().(syscall.WaitStatus)
	return ok && status.Signaled()
}

// writeRunningBackgroundRecord plants the durable state a crashed daemon leaves
// behind: a running background shell job naming pid and its start time.
func writeRunningBackgroundRecord(t *testing.T, stateDir, sessionID, jobID string, pid int, started time.Time) {
	t.Helper()
	writeRecord(t, stateDir, sessionID, jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: time.Now(), JobID: jobID,
		Type: jobstore.JobShell, Background: true,
		OwnerSessionID: sessionID, VisibleToSession: sessionID,
		ProcessGroupID: pid, ProcessStartTime: &started,
	})
}

func writeRecord(t *testing.T, stateDir, sessionID string, event jobstore.Event) {
	t.Helper()
	path := filepath.Join(schema.SessionWorkDir(stateDir, sessionID), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(event); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

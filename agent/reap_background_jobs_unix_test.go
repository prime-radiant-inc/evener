//go:build unix

package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

	if reaped := ReapSessionBackgroundJobs(stateDir, sessionID, nil); reaped != 1 {
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

	if reaped := ReapSessionBackgroundJobs(stateDir, sessionID, nil); reaped != 0 {
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

	if reaped := ReapSessionBackgroundJobs(stateDir, sessionID, nil); reaped != 0 {
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

	if reaped := ReapSessionBackgroundJobs(stateDir, sessionID, nil); reaped != 1 {
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
	if reaped := ReapSessionBackgroundJobs(t.TempDir(), "sess_reap_missing", nil); reaped != 0 {
		t.Fatalf("reaped %d groups from a missing journal, want none", reaped)
	}
}

// TestReconcileLostJobsReapsTheSurvivingGroup proves the restore path: a
// restarted session that finds a running background record whose group is still
// alive and whose leader still has the recorded start time ends that group
// before the job is recorded lost, so the job does not outlive its session.
func TestReconcileLostJobsReapsTheSurvivingGroup(t *testing.T) {
	t.Parallel()
	stateDir, sessionID := t.TempDir(), "sess_restore_reap"
	sleeper := startProcessGroup(t)
	writeRunningBackgroundRecord(t, stateDir, sessionID, "job_restore_reap", sleeper.pid, sleeper.started)

	jm, err := newJobManager(stateDir, sessionID, func(jobNotification) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jm.close() })
	if err := jm.reconcileLostJobs(); err != nil {
		t.Fatalf("reconcile lost jobs: %v", err)
	}

	if exitErr, ended := sleeper.waitWithin(t, 10*time.Second); !ended {
		t.Fatal("a restarted session left its predecessor's background group running")
	} else if !signalledExit(exitErr) {
		t.Fatalf("reaped process ended without a signal: %v", exitErr)
	}
	recs, err := jm.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if rec := recs["job_restore_reap"]; rec == nil || rec.Status != jobstore.StatusStopped || rec.Reason != "runtime_lost" {
		t.Fatalf("restored record = %+v, want stopped/runtime_lost", rec)
	}
}

// TestReapSessionBackgroundJobsKillsTermIgnoringDescendants proves why the crash
// reap escalates at once instead of granting a SIGTERM grace: a grace period
// lets the leader exit on SIGTERM, frees its pid, and the verifying check before
// any escalation then refuses to signal — so a descendant that ignores SIGTERM
// keeps holding its ports. One verified SIGKILL must take the whole group.
func TestReapSessionBackgroundJobsKillsTermIgnoringDescendants(t *testing.T) {
	t.Parallel()
	stateDir, sessionID := t.TempDir(), "sess_reap_termproud"
	leader, childPid := startShellWithTermIgnoringChild(t)
	writeRunningBackgroundRecord(t, stateDir, sessionID, "job_reap_termproud", leader.pid, leader.started)

	if reaped := ReapSessionBackgroundJobs(stateDir, sessionID, nil); reaped != 1 {
		t.Fatalf("reaped %d groups, want 1", reaped)
	}
	exitErr, ended := leader.waitWithin(t, 10*time.Second)
	if !ended {
		t.Fatal("reaped process group leader is still running")
	}
	if signal := exitSignal(exitErr); signal != syscall.SIGKILL {
		t.Fatalf("crash reap ended the leader with %v, want SIGKILL at once", signal)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(childPid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(childPid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("SIGTERM-ignoring descendant %d survived the crash reap: %v", childPid, err)
	}
}

// TestReapSessionBackgroundJobsReapsNestedDelegateGroups proves the subtree walk:
// a nested delegate's start event is forwarded only to its immediate parent, so
// its group is not in the root's journal. The reap must still end it.
func TestReapSessionBackgroundJobsReapsNestedDelegateGroups(t *testing.T) {
	t.Parallel()
	const rootID = "02wMz5Txv1C3Hut0M8GCeG"
	const childID = "02wMz5Txv1C3Hut0M8GCeH"
	stateDir := t.TempDir()
	writePastDelegates(t, stateDir, rootID, pastStableDescriptor(rootID, childID, "child"))
	sleeper := startProcessGroup(t)
	writeRunningBackgroundRecord(t, stateDir, childID, "job_nested_reap", sleeper.pid, sleeper.started)

	if reaped := ReapSessionBackgroundJobs(stateDir, rootID, nil); reaped != 1 {
		t.Fatalf("reaped %d groups from the delegate subtree, want 1", reaped)
	}
	if _, ended := sleeper.waitWithin(t, 10*time.Second); !ended {
		t.Fatal("a nested delegate's background group survived the reap")
	}
}

// TestReapSessionBackgroundJobsHonorsTheSkipPredicate pins the hub's still-live
// guard: a session the predicate names is left to whoever owns it now.
func TestReapSessionBackgroundJobsHonorsTheSkipPredicate(t *testing.T) {
	t.Parallel()
	stateDir, sessionID := t.TempDir(), "sess_reap_skipped"
	sleeper := startProcessGroup(t)
	writeRunningBackgroundRecord(t, stateDir, sessionID, "job_reap_skipped", sleeper.pid, sleeper.started)

	if reaped := ReapSessionBackgroundJobs(stateDir, sessionID, func(string) bool { return true }); reaped != 0 {
		t.Fatalf("reaped %d groups for a skipped session, want none", reaped)
	}
	if !sleeper.running(t) {
		t.Fatal("a skipped session's process group was signaled")
	}
}

// TestDelegateShellRepairReapsTheSurvivingGroup proves the cold delegate repair
// path: the delegate runtime that owned a running background job is gone, so its
// process group survived, and the repair must end the group before recording the
// job stopped (#4843).
func TestDelegateShellRepairReapsTheSurvivingGroup(t *testing.T) {
	t.Parallel()
	stateDir, sessionID := t.TempDir(), "sess_repair_reap"
	const jobID = "job_repair_reap"
	sleeper := startProcessGroup(t)
	writeRunningBackgroundRecord(t, stateDir, sessionID, jobID, sleeper.pid, sleeper.started)

	err := executeDelegateShellRepair(delegateShellRepairPlan{
		delegateID:    "dlg_target",
		storePath:     filepath.Join(schema.SessionWorkDir(stateDir, sessionID), "jobs.jsonl"),
		runningJobIDs: []string{jobID},
	}, time.Unix(20, 0).UTC())
	if err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	if _, ended := sleeper.waitWithin(t, 10*time.Second); !ended {
		t.Fatal("a repaired delegate's background group survived")
	}
}

// startShellWithTermIgnoringChild starts a group leader shell whose child
// ignores SIGTERM, and returns the leader plus the child's pid. It is the shape
// a grace period cannot clean up once the leader has exited.
func startShellWithTermIgnoringChild(t *testing.T) (*processGroup, int) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// The descendant must ignore SIGTERM at the process level — a shell that
	// trapped TERM and loops, never an exec'd sleep (exec drops the trap) — and
	// the leader must die on SIGTERM at once, leaving the descendant behind. The
	// exec keeps the leader's pid and start time, so the record still names it.
	cmd := exec.Command("sh", "-c", `sh -c 'trap "" TERM; while :; do sleep 1; done' & echo $! > "$1"; exec sleep 60`, "sh", pidFile)
	cmd.SysProcAttr = procgroup.SysProcAttr()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shell with term-ignoring child: %v", err)
	}
	started, ok := processStartedAt(cmd.Process.Pid)
	if !ok {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("start time unavailable for a freshly started shell")
	}
	leader := &processGroup{pid: cmd.Process.Pid, started: started, cmd: cmd}
	var childPid int
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				childPid = pid
				break
			}
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("child pid never appeared in %s: %v", pidFile, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(childPid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return leader, childPid
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

// exitSignal reports the signal that ended the process, or 0 when it exited on
// its own.
func exitSignal(err *exec.ExitError) syscall.Signal {
	if err == nil {
		return 0
	}
	status, ok := err.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0
	}
	return status.Signal()
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

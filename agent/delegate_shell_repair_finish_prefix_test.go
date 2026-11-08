package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestDelegateShellRepairProducesWatchEndNoticeForFinishOnlyPrefix covers the
// earliest torn-batch prefix: the terminal event survived but the batch's
// notification and watch-clear records did not. A send watch must be cleared
// with the cross-session end notice the live terminal path would have sent, not
// dropped silently.
func TestDelegateShellRepairProducesWatchEndNoticeForFinishOnlyPrefix(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")

	started := time.Unix(10, 0).UTC()
	ended := time.Unix(20, 0).UTC()
	appendDelegateShellEventsAt(t, path,
		jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			TS:             started,
			JobID:          "job-shell",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_target",
			StartedAt:      &started,
		},
		jobstore.Event{
			Kind:    jobstore.EventWatchRegistered,
			TS:      started,
			WatchID: "watch-shell",
			Watch: &jobstore.WatchEvent{
				Generation:       "watch-generation",
				OwnerSessionID:   "child-dlg_target",
				VisibleSessionID: "child-dlg_target",
				Target:           "job-shell",
				SendTo:           "receiver-session",
				ConfigHash:       "hash",
				Condition:        "terminal",
			},
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-shell",
			Status:      jobstore.StatusStopped,
			Reason:      "runtime_lost",
			EndedAt:     &ended,
			TerminalGen: "terminal-finish-only",
		},
	)

	evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	plans, err := c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(plans.shellRepairs) != 1 {
		t.Fatalf("shell repair plans = %#v, want one plan for the finish-only prefix", plans.shellRepairs)
	}
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}

	records, watches := readDelegateShellStore(t, path)
	if record := records["job-shell"]; record == nil || record.Status != jobstore.StatusStopped || record.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("repaired shell from finish-only prefix = %#v, want stopped with no notification armed", record)
	}
	if watch := watches["watch-shell"]; watch == nil || watch.Active {
		t.Fatalf("terminal watch after repair = %#v, want cleared", watch)
	}
	frames := watchEndNoticeFrames(t, path)
	if len(frames) != 1 || frames[0].Key.WatchID != "watch-shell" ||
		frames[0].Key.WatchGeneration != "watch-generation" ||
		frames[0].Key.ResolvedWatchedIdentity != "job-shell" ||
		frames[0].Key.ResolvedSendTo != "receiver-session" ||
		frames[0].Frame == "" {
		t.Fatalf("watch-end notice frames = %#v, want one for watch-shell to receiver-session", frames)
	}

	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile first repair: %v", err)
	}
	evidence, err = collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence after repair: %v", err)
	}
	plans, err = c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile after repair: %v", err)
	}
	for _, plan := range plans.shellRepairs {
		if err := executeDelegateShellRepair(plan, time.Unix(31, 0).UTC()); err != nil {
			t.Fatalf("executeDelegateShellRepair after repair: %v", err)
		}
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile second repair: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("repair after a completed finish-only prefix appended again:\nfirst=%s\nsecond=%s", first, second)
	}
}

// TestDelegateShellRepairLeavesCallbackWatchForRestoreFromFinishOnlyPrefix
// covers a no-send callback watch on the same prefix. Its cancellation notice is
// routed by the child's restore, so the cold repair must leave the watch active
// rather than clear it with no notice.
func TestDelegateShellRepairLeavesCallbackWatchForRestoreFromFinishOnlyPrefix(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")
	started := time.Unix(10, 0).UTC()
	ended := time.Unix(20, 0).UTC()
	appendDelegateShellEventsAt(t, path,
		jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			TS:             started,
			JobID:          "job-shell",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_target",
			StartedAt:      &started,
		},
		jobstore.Event{
			Kind:    jobstore.EventWatchRegistered,
			TS:      started,
			WatchID: "watch-shell",
			Watch: &jobstore.WatchEvent{
				Generation:       "watch-generation",
				OwnerSessionID:   "child-dlg_target",
				VisibleSessionID: "child-dlg_target",
				Target:           "job-shell",
				ConfigHash:       "hash",
				Condition:        "terminal",
			},
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-shell",
			Status:      jobstore.StatusStopped,
			Reason:      "runtime_lost",
			EndedAt:     &ended,
			TerminalGen: "terminal-finish-only",
		},
	)
	evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	plans, err := c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(plans.shellRepairs) != 0 {
		t.Fatalf("shell repair plans = %#v, want none: a callback watch is left for the child's restore", plans.shellRepairs)
	}
	_, watches := readDelegateShellStore(t, path)
	if watch := watches["watch-shell"]; watch == nil || !watch.Active {
		t.Fatalf("callback watch after cold repair = %#v, want left active for restore", watch)
	}
}

// TestDelegateReconcileSkipsShellRepairPlanWithNoWork pins that a pending
// notification outside a stop, with no active terminal watch, does not create a
// repair plan. An empty plan would make the stop and bootstrap drains treat it
// as progress forever.
func TestDelegateReconcileSkipsShellRepairPlanWithNoWork(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")

	started := time.Unix(10, 0).UTC()
	ended := time.Unix(20, 0).UTC()
	appendDelegateShellEventsAt(t, path,
		jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			TS:             started,
			JobID:          "job-shell",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_target",
			StartedAt:      &started,
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-shell",
			Status:      jobstore.StatusStopped,
			Reason:      "runtime_lost",
			EndedAt:     &ended,
			TerminalGen: "terminal-no-watch",
		},
		jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          ended,
			JobID:       "job-shell",
			TerminalGen: "terminal-no-watch",
		},
	)
	evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	plans, err := c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(plans.shellRepairs) != 0 {
		t.Fatalf("shell repair plans = %#v, want none without running work or an active terminal watch", plans.shellRepairs)
	}
}

// TestDelegateShellRepairConsumesPendingNotificationWithoutWatch keeps the
// stop-covered repair: a pending notification inside a stop is consumed even
// when there is no watch left to clear.
func TestDelegateShellRepairConsumesPendingNotificationWithoutWatch(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	started := time.Unix(10, 0).UTC()
	ended := time.Unix(20, 0).UTC()
	appendDelegateShellEventsAt(t, path,
		jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			TS:             started,
			JobID:          "job-shell",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_target",
			StartedAt:      &started,
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-shell",
			Status:      jobstore.StatusStopped,
			Reason:      "runtime_lost",
			EndedAt:     &ended,
			TerminalGen: "terminal-covered",
		},
		jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          ended,
			JobID:       "job-shell",
			TerminalGen: "terminal-covered",
		},
	)
	plan := delegateShellRepairPlan{
		delegateID:          "dlg_target",
		storePath:           path,
		pendingNotification: []shellNotificationIdentity{{jobID: "job-shell", terminalGeneration: "terminal-covered"}},
		suppressOwnerNotify: true,
	}
	if err := executeDelegateShellRepair(plan, time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	records, _ := readDelegateShellStore(t, path)
	if record := records["job-shell"]; record == nil || record.NotifyState != jobstore.NotifyConsumed {
		t.Fatalf("covered pending notification = %#v, want consumed", record)
	}
}

// TestDelegateStopCompletesWithUnrelatedPendingNotification drives the stop
// reconcile loop with a sibling delegate that has a terminal pending
// notification and no watch. That plan must not exist: the stop driver treats a
// non-empty shell repair list as progress and would loop forever.
func TestDelegateStopCompletesWithUnrelatedPendingNotification(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerIdle(t, c, "dlg_other", "")
	started := time.Unix(10, 0).UTC()
	ended := time.Unix(20, 0).UTC()
	appendDelegateShellEventsAt(t, filepath.Join(jobsDir(c.stateDir, "child-dlg_other"), "jobs.jsonl"),
		jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			TS:             started,
			JobID:          "job-other",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_other",
			StartedAt:      &started,
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-other",
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &ended,
			TerminalGen: "terminal-other",
		},
		jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          ended,
			JobID:       "job-other",
			TerminalGen: "terminal-other",
		},
	)

	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	for pass := range 5 {
		select {
		case <-result.done:
			return
		default:
		}
		c.mu.Lock()
		stop := c.stop
		c.mu.Unlock()
		req, current := c.stopReconcileRequirements(stop)
		if !current {
			t.Fatalf("pass %d: stop requirements stale", pass)
		}
		ev, err := collectDelegateReconcileEvidence(c.stateDir, req)
		if err != nil {
			t.Fatalf("pass %d: collect evidence: %v", pass, err)
		}
		plans, err := c.Reconcile(ev)
		if err != nil {
			t.Fatalf("pass %d: Reconcile: %v", pass, err)
		}
		for _, plan := range plans.shellRepairs {
			if err := executeDelegateShellRepair(plan, time.Unix(30, 0).UTC()); err != nil {
				t.Fatalf("pass %d: execute repair: %v", pass, err)
			}
		}
		if plans.retirementRelease != nil {
			plans.retirementRelease()
		}
	}
	t.Fatalf("stop never completed after 5 reconcile passes with an unrelated pending notification")
}

// TestDelegateShellRepairRestoresBackgroundNotificationFromFinishOnlyPrefix
// covers the finish-only prefix of a background shell: it must regain the
// completion notification it was always going to send.
func TestDelegateShellRepairRestoresBackgroundNotificationFromFinishOnlyPrefix(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")
	started := time.Unix(10, 0).UTC()
	ended := time.Unix(20, 0).UTC()
	appendDelegateShellEventsAt(t, path,
		jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			TS:             started,
			JobID:          "job-background",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_target",
			Background:     true,
			StartedAt:      &started,
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-background",
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &ended,
			TerminalGen: "terminal-background",
		},
	)
	evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	plans, err := c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(plans.shellRepairs) != 1 {
		t.Fatalf("shell repair plans = %#v, want one plan for the background finish-only prefix", plans.shellRepairs)
	}
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	records, _ := readDelegateShellStore(t, path)
	if record := records["job-background"]; record == nil || record.NotifyState != jobstore.NotifyPending {
		t.Fatalf("background shell after repair = %#v, want pending notification", record)
	}

	evidence, err = collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence after repair: %v", err)
	}
	plans, err = c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile after repair: %v", err)
	}
	if len(plans.shellRepairs) != 0 {
		t.Fatalf("notification repair repeated after landing: %#v", plans.shellRepairs)
	}
}

// TestDelegateShellRepairConsumesUnnotifiedBackgroundNotification pins the
// stop-covered variant: the lost completion notification is consumed, not
// queued for an owner the stop is shutting down.
func TestDelegateShellRepairConsumesUnnotifiedBackgroundNotification(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	started := time.Unix(10, 0).UTC()
	ended := time.Unix(20, 0).UTC()
	appendDelegateShellEventsAt(t, path,
		jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			TS:             started,
			JobID:          "job-background",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_target",
			Background:     true,
			StartedAt:      &started,
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-background",
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &ended,
			TerminalGen: "terminal-background",
		},
	)
	plan := delegateShellRepairPlan{
		delegateID:          "dlg_target",
		storePath:           path,
		unnotifiedTerminal:  []shellNotificationIdentity{{jobID: "job-background", terminalGeneration: "terminal-background"}},
		suppressOwnerNotify: true,
	}
	if err := executeDelegateShellRepair(plan, time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	records, _ := readDelegateShellStore(t, path)
	if record := records["job-background"]; record == nil || record.NotifyState != jobstore.NotifyConsumed {
		t.Fatalf("suppressed background notification = %#v, want consumed", record)
	}
}

func watchEndNoticeFrames(t *testing.T, path string) []jobstore.WatchSendState {
	t.Helper()
	events, err := jobstore.ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	var frames []jobstore.WatchSendState
	for _, event := range events {
		if event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.EndNotice {
			frames = append(frames, *event.WatchSend)
		}
	}
	return frames
}

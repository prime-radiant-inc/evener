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
	if record := records["job-shell"]; record == nil || record.Status != jobstore.StatusStopped || record.NotifyState != jobstore.NotifyPending {
		t.Fatalf("repaired shell from finish-only prefix = %#v, want stopped with pending notification", record)
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
	// The terminal record is still at not_armed with no consumed marker, so the
	// cold repair must re-arm its notification. The repair must not touch the
	// no-send callback watch: only the child's restore can cancel it and tell
	// its receiver.
	if len(plans.shellRepairs) != 1 {
		t.Fatalf("shell repair plans = %#v, want one plan for the unsettled completion", plans.shellRepairs)
	}
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	records, watches := readDelegateShellStore(t, path)
	if record := records["job-shell"]; record == nil || record.NotifyState != jobstore.NotifyPending {
		t.Fatalf("terminal shell after repair = %#v, want pending notification", record)
	}
	if watch := watches["watch-shell"]; watch == nil || !watch.Active {
		t.Fatalf("callback watch after cold repair = %#v, want left active for restore", watch)
	}
}

// TestDelegateShellRepairPreservesPendingOutputMatchFrameFromFinishOnlyPrefix
// covers the torn-batch prefix where the watch already matched and put its first
// output-match frame on the journal, but the crash lost the terminal batch's
// watch clear. The cold repair must clear the watch without synthesizing an end
// notice: a second frame at the same key and UpdateSeq would fold over the
// matched output and claim the watch never fired.
func TestDelegateShellRepairPreservesPendingOutputMatchFrameFromFinishOnlyPrefix(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")

	started := time.Unix(10, 0).UTC()
	matched := time.Unix(15, 0).UTC()
	ended := time.Unix(20, 0).UTC()
	key := jobstore.WatchSendKey{
		VisibleSessionID:        "child-dlg_target",
		WatchID:                 "watch-shell",
		WatchTarget:             "job-shell",
		ResolvedWatchedIdentity: "job-shell",
		ResolvedSendTo:          runtimeMessageAliasCaller,
		WatchGeneration:         "watch-generation",
	}
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
				SendTo:           runtimeMessageAliasCaller,
				ConfigHash:       "hash",
				Condition:        "output_match",
			},
		},
		jobstore.Event{
			Kind: jobstore.EventWatchSendPending,
			TS:   matched,
			WatchSend: &jobstore.WatchSendState{
				Key:        key,
				DeliveryID: "delivery-match",
				UpdateSeq:  1,
				Message:    "output matched",
				Frame:      "matched output frame",
				CreatedAt:  matched,
				UpdatedAt:  matched,
			},
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-shell",
			Status:      jobstore.StatusStopped,
			Reason:      "runtime_lost",
			EndedAt:     &ended,
			TerminalGen: "terminal-pending-match",
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

	stored, err := jobstore.ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	pending := jobstore.FoldWatchSends(stored).Pending[key]
	if pending == nil {
		t.Fatalf("pending watch sends after repair = %#v, want the output-match frame preserved", jobstore.FoldWatchSends(stored).Pending)
	}
	if pending.EndNotice || pending.DeliveryID != "delivery-match" || pending.Message != "output matched" || pending.Frame != "matched output frame" {
		t.Fatalf("pending watch send after repair = %#v, want the original output-match frame", pending)
	}
	if frames := watchEndNoticeFrames(t, path); len(frames) != 0 {
		t.Fatalf("watch-end notice frames after repair = %#v, want none: the generation already spoke", frames)
	}
	_, watches := readDelegateShellStore(t, path)
	if watch := watches["watch-shell"]; watch == nil || watch.Active {
		t.Fatalf("terminal watch after repair = %#v, want cleared with its pending send preserved", watch)
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

// TestDelegateShellRepairRestoresForegroundRuntimeTimeoutNotificationFromFinishOnlyPrefix
// covers a foreground shell that hit its max_runtime_ms limit: that path arms a
// completion notification even though the record is not Background, so a
// finish-only prefix leaves the terminal job at not_armed. The cold repair must
// arm it like the background case, or cold bootstrap has no pending
// notification to deliver.
func TestDelegateShellRepairRestoresForegroundRuntimeTimeoutNotificationFromFinishOnlyPrefix(t *testing.T) {
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
			JobID:          "job-foreground-timeout",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_target",
			StartedAt:      &started,
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-foreground-timeout",
			Status:      jobstore.StatusStopped,
			Reason:      "run_timeout",
			EndedAt:     &ended,
			TerminalGen: "terminal-foreground-timeout",
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
		t.Fatalf("shell repair plans = %#v, want one plan for the foreground runtime-timeout finish-only prefix", plans.shellRepairs)
	}
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	records, _ := readDelegateShellStore(t, path)
	if record := records["job-foreground-timeout"]; record == nil || record.NotifyState != jobstore.NotifyPending || record.TerminalGen != "terminal-foreground-timeout" {
		t.Fatalf("foreground runtime-timeout shell after repair = %#v, want pending notification for its terminal generation", record)
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

// TestDelegateShellRepairRestoresForegroundKeptCompletionNotification covers a
// foreground shell kept as a durable job because its output could not ride
// inline: the live path settles its notification only after the tool reply is
// durable. A crash between job_finished and that settlement leaves a terminal
// not_armed record with no consumed marker; cold bootstrap must re-arm it, or
// the kept result is undisclosed until an explicit resume (#5834 review).
func TestDelegateShellRepairRestoresForegroundKeptCompletionNotification(t *testing.T) {
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
			JobID:          "job-kept",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_target",
			StartedAt:      &started,
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ended,
			JobID:       "job-kept",
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &ended,
			TerminalGen: "terminal-kept",
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
		t.Fatalf("shell repair plans = %#v, want one plan for the unsettled kept completion", plans.shellRepairs)
	}
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	records, _ := readDelegateShellStore(t, path)
	if record := records["job-kept"]; record == nil || record.NotifyState != jobstore.NotifyPending {
		t.Fatalf("kept completion after repair = %#v, want pending notification", record)
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

// TestDelegateShellRepairStopPrefixLeavesCallbackWatchForRestore covers a torn
// batch under a pending subtree stop: the notification is consumed, but the
// job's no-send callback watch must stay active for the child's restore, which
// is the only path that can cancel it and tell its receiver. Clearing it here
// would lose that ancestor receiver's cancellation notice permanently.
func TestDelegateShellRepairStopPrefixLeavesCallbackWatchForRestore(t *testing.T) {
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
			TerminalGen: "terminal-stop-prefix",
		},
		jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          ended,
			JobID:       "job-shell",
			TerminalGen: "terminal-stop-prefix",
		},
	)

	// A synthetic pending stop avoids starting the stop driver; the repair
	// only needs c.stopCoversLocked to see dlg_target in the subtree.
	c.mu.Lock()
	c.stop = &delegateStopState{members: map[string]struct{}{"dlg_target": {}}}
	c.mu.Unlock()
	evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	plans, err := c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var plan *delegateShellRepairPlan
	for i := range plans.shellRepairs {
		if plans.shellRepairs[i].delegateID == "dlg_target" {
			plan = &plans.shellRepairs[i]
			break
		}
	}
	if plan == nil || !plan.suppressOwnerNotify {
		t.Fatalf("shell repair plan = %#v, want a stop-covered plan for dlg_target", plans.shellRepairs)
	}
	if err := executeDelegateShellRepair(*plan, time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	records, watches := readDelegateShellStore(t, path)
	if record := records["job-shell"]; record == nil || record.NotifyState != jobstore.NotifyConsumed {
		t.Fatalf("stop-covered notification = %#v, want consumed", record)
	}
	if watch := watches["watch-shell"]; watch == nil || !watch.Active {
		t.Fatalf("callback watch after stop-covered repair = %#v, want left active for restore", watch)
	}
}

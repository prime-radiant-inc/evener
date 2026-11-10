package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestDelegateShellRepairClearsTerminalWatchFromDurableBatchPrefix covers the
// crash window AppendBatch documents: the terminal event and its notification
// can survive while the batch's watch-clear event does not. Restore rebuilds its
// plan from the durable records, so it must finish the torn batch from that
// prefix rather than leave the terminal watch active forever.
func TestDelegateShellRepairClearsTerminalWatchFromDurableBatchPrefix(t *testing.T) {
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
			TerminalGen: "terminal-prefix",
		},
		jobstore.Event{
			Kind:        jobstore.EventJobNotificationPending,
			TS:          ended,
			JobID:       "job-shell",
			TerminalGen: "terminal-prefix",
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
		t.Fatalf("shell repair plans = %#v, want one plan for the durable batch prefix", plans.shellRepairs)
	}
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}

	records, watches := readDelegateShellStore(t, path)
	if record := records["job-shell"]; record == nil || record.Status != jobstore.StatusStopped || record.NotifyState != jobstore.NotifyPending {
		t.Fatalf("repaired shell from prefix = %#v, want stopped with pending notification", record)
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
		t.Fatalf("repair after a completed prefix appended again:\nfirst=%s\nsecond=%s", first, second)
	}
}

// TestDelegateShellRepairLeavesCallbackWatchForRestoreFromRunningJob covers a
// running shell whose ancestor registered a no-send callback watch. Cold
// bootstrap finishes the running job; it must leave that callback watch active
// for the child's restore, which is the only path that can cancel it and route
// the receiver's cancellation notice. Clearing it here loses that notice
// permanently (#5834 review).
func TestDelegateShellRepairLeavesCallbackWatchForRestoreFromRunningJob(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")
	started := time.Unix(10, 0).UTC()
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
		t.Fatalf("shell repair plans = %#v, want one plan for the running job", plans.shellRepairs)
	}
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(30, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}

	records, watches := readDelegateShellStore(t, path)
	if record := records["job-shell"]; record == nil || !record.Status.IsTerminal() || record.NotifyState != jobstore.NotifyPending {
		t.Fatalf("repaired running shell = %#v, want terminal with pending notification", record)
	}
	if watch := watches["watch-shell"]; watch == nil || !watch.Active {
		t.Fatalf("callback watch after running-job repair = %#v, want left active for restore", watch)
	}

	var notified []jobNotification
	restartJobManager(t, c.stateDir, "child-dlg_target", func(n jobNotification) {
		notified = append(notified, n)
	})
	notices := watchChannelNotices(notified)
	if len(notices) != 1 || notices[0].Reason != callbackWatchesCancelledAtRestartMessage {
		t.Fatalf("child-restore notices = %#v, want one callback-cancellation notice", notices)
	}
}

// TestDelegateShellRepairClearsSendWatchFromRunningJob keeps the other half of
// the running-job contract: a send watch is routable, so the repair still
// clears it with the terminal end-reason. Only the no-send callback watch is
// left for restore (#5834 review).
func TestDelegateShellRepairClearsSendWatchFromRunningJob(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	started := time.Unix(10, 0).UTC()
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
	)
	if err := executeDelegateShellRepair(delegateShellRepairPlan{
		delegateID:    "dlg_target",
		storePath:     path,
		runningJobIDs: []string{"job-shell"},
	}, time.Unix(20, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	_, watches := readDelegateShellStore(t, path)
	if watch := watches["watch-shell"]; watch == nil || watch.Active || watch.EndReason != "auto_removed_terminal" {
		t.Fatalf("send watch after running-job repair = %#v, want cleared terminal", watch)
	}
}

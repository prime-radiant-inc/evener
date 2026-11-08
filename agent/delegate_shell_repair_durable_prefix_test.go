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

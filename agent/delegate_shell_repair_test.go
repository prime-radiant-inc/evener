package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestDelegateShellRepairAppendsRuntimeLostAndConsumesCoveredNotification(t *testing.T) {
	path := seedDelegateShellStore(t, true, false)
	plan := delegateShellRepairPlan{
		delegateID:          "dlg_target",
		storePath:           path,
		runningJobIDs:       []string{"job-shell"},
		suppressOwnerNotify: true,
	}
	if err := executeDelegateShellRepair(plan, time.Unix(20, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	records, watches := readDelegateShellStore(t, path)
	record := records["job-shell"]
	if record == nil || record.Status != jobstore.StatusStopped || record.Reason != "runtime_lost" || record.TerminalGen == "" || record.NotifyState != jobstore.NotifyConsumed {
		t.Fatalf("repaired shell = %#v", record)
	}
	if watch := watches["watch-shell"]; watch == nil || watch.Active || watch.EndReason != "auto_removed_terminal" {
		t.Fatalf("repaired watch = %#v", watch)
	}
}

func TestDelegateShellRepairPreservesNotificationOutsideStop(t *testing.T) {
	path := seedDelegateShellStore(t, true, false)
	if err := executeDelegateShellRepair(delegateShellRepairPlan{
		delegateID:    "dlg_target",
		storePath:     path,
		runningJobIDs: []string{"job-shell"},
	}, time.Unix(20, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	records, _ := readDelegateShellStore(t, path)
	if record := records["job-shell"]; record == nil || record.NotifyState != jobstore.NotifyPending {
		t.Fatalf("outside-stop notification = %#v, want pending", record)
	}
}

func TestDelegateShellRepairAppendFailureKeepsStopPending(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	evidence := emptyDelegateReconcileEvidence(c)
	evidence.shells["dlg_target"] = shellRuntimeLossEvidence{runningJobIDs: []string{"job-shell"}}
	plans, err := c.Reconcile(evidence)
	if err != nil || len(plans.shellRepairs) != 1 {
		t.Fatalf("Reconcile shell = plans:%#v err:%v", plans.shellRepairs, err)
	}
	plans.shellRepairs[0].storePath = t.TempDir()
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(20, 0).UTC()); err == nil {
		t.Fatal("executeDelegateShellRepair succeeded with directory store path")
	}
	select {
	case <-result.done:
		t.Fatal("shell append failure completed stop")
	default:
	}
}

func TestDelegateShellRepairIsIdempotentAfterReopen(t *testing.T) {
	path := seedDelegateShellStore(t, true, true)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}
	plan := delegateShellRepairPlan{
		delegateID:          "dlg_target",
		storePath:           path,
		runningJobIDs:       []string{"job-shell"},
		pendingNotification: []shellNotificationIdentity{{jobID: "job-terminal", terminalGeneration: "terminal-existing"}},
		suppressOwnerNotify: true,
	}
	if err := executeDelegateShellRepair(plan, time.Unix(20, 0).UTC()); err != nil {
		t.Fatalf("first executeDelegateShellRepair: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile first: %v", err)
	}
	if string(first) == string(before) {
		t.Fatal("first repair appended no durable events")
	}
	if err := executeDelegateShellRepair(plan, time.Unix(21, 0).UTC()); err != nil {
		t.Fatalf("second executeDelegateShellRepair: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile second: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("idempotent repair appended again:\nfirst=%s\nsecond=%s", first, second)
	}
}

func seedDelegateShellStore(t *testing.T, running, terminalPending bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	store, err := jobstore.OpenNoSync(path)
	if err != nil {
		t.Fatalf("OpenNoSync: %v", err)
	}
	now := time.Unix(10, 0).UTC()
	if running {
		if err := store.Append(jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			TS:             now,
			JobID:          "job-shell",
			Type:           jobstore.JobShell,
			OwnerSessionID: "child-dlg_target",
			StartedAt:      &now,
		}); err != nil {
			t.Fatalf("append running shell: %v", err)
		}
		if err := store.Append(jobstore.Event{
			Kind:    jobstore.EventWatchRegistered,
			TS:      now,
			WatchID: "watch-shell",
			Watch: &jobstore.WatchEvent{
				Generation:       "watch-generation",
				OwnerSessionID:   "child-dlg_target",
				VisibleSessionID: "child-dlg_target",
				Target:           "job-shell",
				ConfigHash:       "hash",
				Condition:        "terminal",
			},
		}); err != nil {
			t.Fatalf("append watch: %v", err)
		}
	}
	if terminalPending {
		ended := now
		for _, event := range []jobstore.Event{
			{Kind: jobstore.EventJobStarted, TS: now, JobID: "job-terminal", Type: jobstore.JobShell, OwnerSessionID: "child-dlg_target", StartedAt: &now},
			{Kind: jobstore.EventJobFinished, TS: now, JobID: "job-terminal", Status: jobstore.StatusCompleted, Reason: "exit_zero", EndedAt: &ended, TerminalGen: "terminal-existing"},
			{Kind: jobstore.EventJobNotificationPending, TS: now, JobID: "job-terminal", TerminalGen: "terminal-existing"},
		} {
			if err := store.Append(event); err != nil {
				t.Fatalf("append terminal shell: %v", err)
			}
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

func readDelegateShellStore(t *testing.T, path string) (map[string]*jobstore.JobRecord, map[string]*jobstore.WatchRecord) {
	t.Helper()
	events, err := jobstore.ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	return jobstore.Fold(events), jobstore.FoldWatches(events)
}

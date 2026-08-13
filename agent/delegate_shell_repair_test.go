package agent

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestDelegateControllerReconcileRepairsShellOutsideStop(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")
	seedDelegateShellStoreAt(t, path)
	evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
	if err != nil {
		t.Fatalf("collectDelegateReconcileEvidence: %v", err)
	}
	plans, err := c.Reconcile(evidence)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(plans.shellRepairs) != 1 || plans.shellRepairs[0].suppressOwnerNotify {
		t.Fatalf("outside-stop shell repair plans = %#v", plans.shellRepairs)
	}
	if err := executeDelegateShellRepair(plans.shellRepairs[0], time.Unix(20, 0).UTC()); err != nil {
		t.Fatalf("executeDelegateShellRepair: %v", err)
	}
	records, _ := readDelegateShellStore(t, path)
	if record := records["job-shell"]; record == nil || record.Status != jobstore.StatusStopped || record.NotifyState != jobstore.NotifyPending {
		t.Fatalf("outside-stop repaired shell = %#v", record)
	}
}

func TestDelegateControllerReconcileExcludesLiveShellEvidence(t *testing.T) {
	now := time.Unix(10, 0).UTC()
	ended := time.Unix(20, 0).UTC()
	tests := []struct {
		name                string
		committedJobID      string
		events              []jobstore.Event
		wantRunning         []string
		wantPending         []shellNotificationIdentity
		wantRepairPlanCount int
	}{
		{
			name:           "committed running job",
			committedJobID: "job-live",
			events: []jobstore.Event{
				{Kind: jobstore.EventJobStarted, TS: now, JobID: "job-live", Type: jobstore.JobShell, OwnerSessionID: "child-dlg_target", StartedAt: &now},
				{Kind: jobstore.EventJobStarted, TS: now, JobID: "job-lost", Type: jobstore.JobShell, OwnerSessionID: "child-dlg_target", StartedAt: &now},
			},
			wantRunning:         []string{"job-lost"},
			wantRepairPlanCount: 1,
		},
		{
			name:           "committed terminal notification",
			committedJobID: "job-live",
			events: []jobstore.Event{
				{Kind: jobstore.EventJobStarted, TS: now, JobID: "job-live", Type: jobstore.JobShell, OwnerSessionID: "child-dlg_target", StartedAt: &now},
				{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job-live", Status: jobstore.StatusCompleted, Reason: "exit_zero", EndedAt: &ended, TerminalGen: "terminal-live"},
				{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "job-live", TerminalGen: "terminal-live"},
				{Kind: jobstore.EventJobStarted, TS: now, JobID: "job-lost", Type: jobstore.JobShell, OwnerSessionID: "child-dlg_target", StartedAt: &now},
			},
			wantRunning:         []string{"job-lost"},
			wantRepairPlanCount: 1,
		},
		{
			name: "uncommitted receipt defers delegate",
			events: []jobstore.Event{
				{Kind: jobstore.EventJobStarted, TS: now, JobID: "job-lost", Type: jobstore.JobShell, OwnerSessionID: "child-dlg_target", StartedAt: &now},
			},
			wantRepairPlanCount: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerRunning(t, c, "dlg_target", "")
			lease := delegateLease{delegateID: "dlg_target", generation: 1}
			token, err := c.BeginShellWork(lease)
			if err != nil {
				t.Fatalf("BeginShellWork: %v", err)
			}
			if test.committedJobID != "" {
				if cancelNow, err := c.CommitShellWork(token, test.committedJobID, func() {}); err != nil || cancelNow {
					t.Fatalf("CommitShellWork = cancel:%t err:%v", cancelNow, err)
				}
			}
			path := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")
			appendDelegateShellEventsAt(t, path, test.events...)
			evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
			if err != nil {
				t.Fatalf("collectDelegateReconcileEvidence: %v", err)
			}
			plans, err := c.Reconcile(evidence)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if len(plans.shellRepairs) != test.wantRepairPlanCount {
				t.Fatalf("shell repair plans = %#v, want count %d", plans.shellRepairs, test.wantRepairPlanCount)
			}
			if test.wantRepairPlanCount == 0 {
				return
			}
			plan := plans.shellRepairs[0]
			if !reflect.DeepEqual(plan.runningJobIDs, test.wantRunning) || !reflect.DeepEqual(plan.pendingNotification, test.wantPending) {
				t.Fatalf("filtered shell plan = running:%#v pending:%#v, want running:%#v pending:%#v", plan.runningJobIDs, plan.pendingNotification, test.wantRunning, test.wantPending)
			}
		})
	}
}

func TestDelegateControllerCloseDoesNotRepairLiveShellBeforeFinalizer(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	token, err := c.BeginShellWork(lease)
	if err != nil {
		t.Fatalf("BeginShellWork: %v", err)
	}
	if cancelNow, err := c.CommitShellWork(token, "job-live", func() {}); err != nil || cancelNow {
		t.Fatalf("CommitShellWork = cancel:%t err:%v", cancelNow, err)
	}
	c.live["dlg_target"].binding.cancel = func() {}
	path := filepath.Join(jobsDir(c.stateDir, "child-dlg_target"), "jobs.jsonl")
	now := time.Unix(10, 0).UTC()
	appendDelegateShellEventsAt(t, path, jobstore.Event{
		Kind:           jobstore.EventJobStarted,
		TS:             now,
		JobID:          "job-live",
		Type:           jobstore.JobShell,
		OwnerSessionID: "child-dlg_target",
		StartedAt:      &now,
	})

	ctx := newDelegateStopWaitBarrierContext()
	closeResult := make(chan error, 1)
	go func() { closeResult <- c.Close(ctx) }()
	<-ctx.entered
	records, _ := readDelegateShellStore(t, path)
	wasRunning := records["job-live"] != nil && records["job-live"].Status == jobstore.StatusRunning

	ended := time.Unix(20, 0).UTC()
	appendDelegateShellEventsAt(t, path,
		jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: "job-live", Status: jobstore.StatusCompleted, Reason: "exit_zero", EndedAt: &ended, TerminalGen: "terminal-live"},
		jobstore.Event{Kind: jobstore.EventJobNotificationPending, TS: ended, JobID: "job-live", TerminalGen: "terminal-live"},
	)
	if _, err := c.ReportShellFinished(token, "job-live"); err != nil {
		t.Fatalf("ReportShellFinished: %v", err)
	}
	if _, err := c.FinishGeneration(lease, delegateFinish{}); err != nil {
		t.Fatalf("FinishGeneration: %v", err)
	}
	ctx.cancel()
	if err := <-closeResult; err != nil {
		t.Fatalf("Close after exact finalizer: %v", err)
	}
	if !wasRunning {
		t.Fatal("close repaired the exact live shell as runtime_lost before its finalizer")
	}
	records, _ = readDelegateShellStore(t, path)
	if record := records["job-live"]; record == nil || record.Status != jobstore.StatusCompleted || record.Reason != "exit_zero" || record.NotifyState != jobstore.NotifyConsumed {
		t.Fatalf("shell after finalizer and covered cleanup = %#v", record)
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

func appendDelegateShellEventsAt(t *testing.T, path string, events ...jobstore.Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll shell store: %v", err)
	}
	store, err := jobstore.OpenNoSync(path)
	if err != nil {
		t.Fatalf("OpenNoSync shell store: %v", err)
	}
	for _, event := range events {
		if err := store.Append(event); err != nil {
			_ = store.Close()
			t.Fatalf("append shell event %s for %s: %v", event.Kind, event.JobID, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close shell store: %v", err)
	}
}

func readDelegateShellStore(t *testing.T, path string) (map[string]*jobstore.JobRecord, map[string]*jobstore.WatchRecord) {
	t.Helper()
	events, err := jobstore.ReadEvents(path)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	return jobstore.Fold(events), jobstore.FoldWatches(events)
}

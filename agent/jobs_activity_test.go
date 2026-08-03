package agent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
)

func TestProjectActivitySession_GroupsDelegateTurnsOnce(t *testing.T) {
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", Label: "Root",
		Jobs: []*jobstore.JobRecord{
			{JobID: "job_a", Type: jobstore.JobShell, OwnerSessionID: "root", Status: jobstore.StatusCompleted, StartedAt: time.Unix(1, 0)},
			{JobID: "job_d1", Type: jobstore.JobDelegate, DelegateID: "dlg_1", OwnerSessionID: "root", Status: jobstore.StatusCompleted, StartedAt: time.Unix(2, 0)},
			{JobID: "job_d2", Type: jobstore.JobDelegate, DelegateID: "dlg_1", OwnerSessionID: "root", Status: jobstore.StatusRunning, StartedAt: time.Unix(3, 0)},
		},
		Delegates: map[string]*jobstore.DelegateRecord{"dlg_1": {DelegateID: "dlg_1", ChildSessionID: "child", TranscriptRef: "local:child"}},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if len(got.Entries) != 2 {
		t.Fatalf("entries=%d, want shell + one delegate", len(got.Entries))
	}
	if got.Entries[0].Kind != "shell" || got.Entries[0].Job == nil || got.Entries[0].Job.JobID != "job_a" {
		t.Fatalf("first entry=%+v, want shell job_a", got.Entries[0])
	}
	if got.Entries[1].Kind != "delegate" || got.Entries[1].Delegate == nil {
		t.Fatalf("second entry=%+v, want delegate", got.Entries[1])
	}
	if turns := got.Entries[1].Delegate.Turns; len(turns) != 2 || turns[1].JobID != "job_d2" {
		t.Fatalf("turns=%+v", turns)
	}
}

func TestProjectActivitySession_AnchorsDelegateAtEarliestTurn(t *testing.T) {
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", Label: "Root",
		Jobs: []*jobstore.JobRecord{
			{JobID: "job_late", Type: jobstore.JobDelegate, DelegateID: "dlg_1", OwnerSessionID: "root", Status: jobstore.StatusCompleted, StartedAt: time.Unix(3, 0)},
			{JobID: "job_shell", Type: jobstore.JobShell, OwnerSessionID: "root", Status: jobstore.StatusCompleted, StartedAt: time.Unix(2, 0)},
			{JobID: "job_early", Type: jobstore.JobDelegate, DelegateID: "dlg_1", OwnerSessionID: "root", Status: jobstore.StatusRunning, StartedAt: time.Unix(1, 0)},
		},
		Delegates: map[string]*jobstore.DelegateRecord{"dlg_1": {DelegateID: "dlg_1", ChildSessionID: "child", TranscriptRef: "local:child"}},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if len(got.Entries) != 2 || got.Entries[0].Job == nil || got.Entries[1].Delegate == nil {
		t.Fatalf("entries=%+v, want shell then delegate at earliest turn's retained position", got.Entries)
	}
	turns := got.Entries[1].Delegate.Turns
	if len(turns) != 2 || turns[0].JobID != "job_early" || turns[1].JobID != "job_late" {
		t.Fatalf("turns=%+v, want earliest-to-latest order", turns)
	}
}

func TestActivityOutcome(t *testing.T) {
	tests := []struct {
		name     string
		status   jobstore.Status
		terminal bool
		outcome  string
	}{
		{name: "running", status: jobstore.StatusRunning},
		{name: "failed", status: jobstore.StatusFailed, terminal: true, outcome: "failure"},
		{name: "exhausted", status: jobstore.StatusExhausted, terminal: true, outcome: "failure"},
		{name: "completed", status: jobstore.StatusCompleted, terminal: true, outcome: "success"},
		{name: "cancelled", status: jobstore.StatusCancelled, terminal: true, outcome: "neutral"},
		{name: "stopped", status: jobstore.StatusStopped, terminal: true, outcome: "neutral"},
		{name: "unknown non-terminal", status: jobstore.Status("waiting")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			terminal, outcome := activityOutcome(tt.status)
			if terminal != tt.terminal || outcome != tt.outcome {
				t.Fatalf("activityOutcome(%q)=(%v, %q), want (%v, %q)", tt.status, terminal, outcome, tt.terminal, tt.outcome)
			}
		})
	}
}

func TestAggregateActivity_CountsWorkUnitsRecursively(t *testing.T) {
	job := func(id, status, outcome string, terminal bool) appwire.JobActivityJob {
		return appwire.JobActivityJob{JobID: id, Status: status, Outcome: outcome, Terminal: terminal}
	}
	child := appwire.JobActivitySession{
		Counts: appwire.JobActivityCounts{Active: 1, Failed: 1, Completed: 2, Complete: true},
	}
	entries := []appwire.JobActivityEntry{
		{Kind: "shell", Job: ptrActivityJob(job("job_running", "running", "", false))},
		{Kind: "shell", Job: ptrActivityJob(job("job_unknown", "waiting", "", false))},
		{Kind: "delegate", Delegate: &appwire.JobActivityDelegate{
			Turns: []appwire.JobActivityJob{
				job("job_failed", "failed", "failure", true),
				job("job_completed", "completed", "success", true),
				job("job_cancelled", "cancelled", "neutral", true),
				job("job_stopped", "stopped", "neutral", true),
			},
			Child: &child,
		}},
	}
	got, aggregate := aggregateActivity(entries, appwire.JobActivityBranchState{})
	want := appwire.JobActivityCounts{Active: 3, Failed: 2, Completed: 5, Complete: true}
	if got != want || aggregate != "working" {
		t.Fatalf("counts=%+v aggregate=%q, want %+v working", got, aggregate, want)
	}
}

func TestAggregateActivity_Precedence(t *testing.T) {
	terminal := func(outcome string) []appwire.JobActivityEntry {
		return []appwire.JobActivityEntry{{Kind: "shell", Job: &appwire.JobActivityJob{Terminal: true, Outcome: outcome}}}
	}
	active := []appwire.JobActivityEntry{{Kind: "shell", Job: &appwire.JobActivityJob{Status: "unknown", Terminal: false}}}
	unavailable := appwire.JobActivityBranchState{Error: "child unavailable"}
	tests := []struct {
		name      string
		entries   []appwire.JobActivityEntry
		branch    appwire.JobActivityBranchState
		aggregate string
		complete  bool
	}{
		{name: "active before unavailable", entries: active, branch: unavailable, aggregate: "working"},
		{name: "failure before unavailable", entries: terminal("failure"), branch: unavailable, aggregate: "failed"},
		{name: "unavailable before ended", entries: terminal("success"), branch: unavailable, aggregate: "unavailable"},
		{name: "truncated is unavailable", entries: terminal("neutral"), branch: appwire.JobActivityBranchState{Truncated: true}, aggregate: "unavailable"},
		{name: "ended", entries: terminal("success"), aggregate: "ended", complete: true},
		{name: "idle", aggregate: "idle", complete: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			counts, aggregate := aggregateActivity(tt.entries, tt.branch)
			if aggregate != tt.aggregate || counts.Complete != tt.complete {
				t.Fatalf("aggregate=%q counts=%+v, want %q complete=%v", aggregate, counts, tt.aggregate, tt.complete)
			}
		})
	}
}

func TestProjectActivitySession_ProjectsOwnerRefsAndChildren(t *testing.T) {
	child := &activitySessionSnapshot{
		SessionID: "child", Ref: "local:child", Label: "Child",
		Jobs: []*jobstore.JobRecord{{JobID: "job_child", Type: jobstore.JobShell, OwnerSessionID: "child", Status: jobstore.StatusCompleted}},
	}
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", Label: "Root",
		Jobs: []*jobstore.JobRecord{
			{JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: "root", Status: jobstore.StatusRunning},
			{JobID: "job_delegate", Type: jobstore.JobDelegate, DelegateID: "dlg_1", OwnerSessionID: "root", Status: jobstore.StatusCompleted, Task: "inspect child"},
		},
		Delegates: map[string]*jobstore.DelegateRecord{"dlg_1": {DelegateID: "dlg_1", ChildSessionID: "child", TranscriptRef: "local:child"}},
		Children:  map[string]*activitySessionSnapshot{"child": child},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if got.SessionID != "root" || got.Ref != "local:root" || got.Label != "Root" {
		t.Fatalf("root identity=%+v", got)
	}
	if got.Entries[0].Job.OwnerSessionID != "root" || got.Entries[0].Job.OwnerRef != "local:root" {
		t.Fatalf("root owner=%+v", got.Entries[0].Job)
	}
	delegate := got.Entries[1].Delegate
	if delegate.ChildSessionID != "child" || delegate.ChildRef != "local:child" || delegate.Mandate != "inspect child" || delegate.Child == nil {
		t.Fatalf("delegate=%+v", delegate)
	}
	childJob := delegate.Child.Entries[0].Job
	if childJob.OwnerSessionID != "child" || childJob.OwnerRef != "local:child" {
		t.Fatalf("child owner=%+v", childJob)
	}
}

func TestProjectActivitySession_UnavailableChildPreservesDelegate(t *testing.T) {
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root",
		Jobs:      []*jobstore.JobRecord{{JobID: "job_delegate", Type: jobstore.JobDelegate, DelegateID: "dlg_1", OwnerSessionID: "root", Status: jobstore.StatusCompleted}},
		Delegates: map[string]*jobstore.DelegateRecord{"dlg_1": {DelegateID: "dlg_1", ChildSessionID: "child", TranscriptRef: "local:child"}},
		Errors:    map[string]error{"child": errors.New("history unavailable")},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if len(got.Entries) != 1 || got.Entries[0].Delegate == nil {
		t.Fatalf("entries=%+v, want retained delegate leaf", got.Entries)
	}
	delegate := got.Entries[0].Delegate
	if delegate.Child != nil || delegate.Branch.Error != "history unavailable" {
		t.Fatalf("delegate branch=%+v child=%+v", delegate.Branch, delegate.Child)
	}
	if got.Aggregate != "unavailable" || got.Counts.Complete {
		t.Fatalf("root aggregate=%q counts=%+v", got.Aggregate, got.Counts)
	}
}

func TestProjectActivitySession_RejectsMalformedDelegateLinks(t *testing.T) {
	tests := []struct {
		name      string
		job       *jobstore.JobRecord
		delegates map[string]*jobstore.DelegateRecord
	}{
		{name: "missing job delegate id", job: &jobstore.JobRecord{JobID: "job_1", Type: jobstore.JobDelegate}},
		{name: "missing delegate record", job: &jobstore.JobRecord{JobID: "job_1", Type: jobstore.JobDelegate, DelegateID: "dlg_1"}},
		{name: "mismatched delegate record", job: &jobstore.JobRecord{JobID: "job_1", Type: jobstore.JobDelegate, DelegateID: "dlg_1"}, delegates: map[string]*jobstore.DelegateRecord{"dlg_1": {DelegateID: "dlg_other", ChildSessionID: "child", TranscriptRef: "local:child"}}},
		{name: "missing child identity", job: &jobstore.JobRecord{JobID: "job_1", Type: jobstore.JobDelegate, DelegateID: "dlg_1"}, delegates: map[string]*jobstore.DelegateRecord{"dlg_1": {DelegateID: "dlg_1"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectActivitySession(activitySessionSnapshot{SessionID: "root", Ref: "local:root", Jobs: []*jobstore.JobRecord{tt.job}, Delegates: tt.delegates}, newActivityBudget())
			var branch appwire.JobActivityBranchState
			if tt.job.DelegateID == "" {
				branch = got.Branch
			} else {
				if len(got.Entries) != 1 || got.Entries[0].Delegate == nil {
					t.Fatalf("entries=%+v, want malformed delegate leaf", got.Entries)
				}
				branch = got.Entries[0].Delegate.Branch
			}
			if branch.Error == "" || got.Counts.Complete {
				t.Fatalf("branch=%+v counts=%+v aggregate=%q", branch, got.Counts, got.Aggregate)
			}
		})
	}
}

func TestMergeActivityRecords_LiveOnlyInsertion(t *testing.T) {
	durable := []*jobstore.JobRecord{
		{JobID: "job_a", Status: jobstore.StatusCompleted, StartedAt: time.Unix(1, 0)},
		{JobID: "job_d", Status: jobstore.StatusCompleted, StartedAt: time.Unix(4, 0)},
	}
	live := map[string]*jobstore.JobRecord{
		"job_c": {JobID: "job_c", Status: jobstore.StatusRunning, StartedAt: time.Unix(3, 0)},
		"job_b": {JobID: "job_b", Status: jobstore.StatusRunning, StartedAt: time.Unix(3, 0)},
	}
	got := mergeActivityRecords(durable, live)
	if ids := activityRecordIDs(got); !reflect.DeepEqual(ids, []string{"job_a", "job_b", "job_c", "job_d"}) {
		t.Fatalf("ids=%v", ids)
	}
	got[0].Status = jobstore.StatusFailed
	if durable[0].Status != jobstore.StatusCompleted {
		t.Fatal("merge returned a durable record without cloning it")
	}
	got[1].Status = jobstore.StatusFailed
	if live["job_b"].Status != jobstore.StatusRunning {
		t.Fatal("merge returned a live record without cloning it")
	}
}

func TestMergeActivityRecords_DurableReconciliationHasNoDuplicate(t *testing.T) {
	durable := []*jobstore.JobRecord{
		{JobID: "job_b", Status: jobstore.StatusRunning, StartedAt: time.Unix(2, 0)},
		{JobID: "job_a", Status: jobstore.StatusCompleted, StartedAt: time.Unix(1, 0)},
	}
	live := map[string]*jobstore.JobRecord{
		"job_b": {JobID: "job_b", Status: jobstore.StatusCompleted, StartedAt: time.Unix(2, 0)},
	}
	got := mergeActivityRecords(durable, live)
	if ids := activityRecordIDs(got); !reflect.DeepEqual(ids, []string{"job_b", "job_a"}) {
		t.Fatalf("durable order or de-duplication lost: ids=%v", ids)
	}
	if got[0].Status != jobstore.StatusCompleted {
		t.Fatalf("live overlay not applied: %+v", got[0])
	}
}

func TestJobActivityTree_LiveTraversalIncludesClosedDurableGrandchildAndGroupsTurnsOnce(t *testing.T) {
	stateDir := t.TempDir()
	root := newActivityTestSession(t, stateDir)
	child := newActivityTestSession(t, stateDir)
	grand := newActivityTestSession(t, stateDir)
	freezeClockAt(root.jobManager, time.Unix(10, 0).UTC())
	freezeClockAt(child.jobManager, time.Unix(20, 0).UTC())
	freezeClockAt(grand.jobManager, time.Unix(30, 0).UTC())

	rootShell, err := root.jobManager.createShell(createShellOpts{Command: "root", Description: "root shell"})
	if err != nil {
		t.Fatalf("root createShell: %v", err)
	}
	if err := root.jobManager.finalize(rootShell.JobID, jobstore.StatusCompleted, "done", nil); err != nil {
		t.Fatalf("root finalize: %v", err)
	}

	childSub, childRun := linkActivityChild(t, root, child, "inspect child")
	if _, err := root.attachDelegateJobFromWatchWithDelegate(root.jobManager, child.ID(), "inspect child again", childSub, childRun.rec.DelegateID, nil, nil, false, nil); err != nil {
		t.Fatalf("attach repeated child delegate: %v", err)
	}
	childShell, err := child.jobManager.createShell(createShellOpts{Command: "child", Description: "child shell"})
	if err != nil {
		t.Fatalf("child createShell: %v", err)
	}
	if err := child.jobManager.finalize(childShell.JobID, jobstore.StatusCompleted, "done", nil); err != nil {
		t.Fatalf("child finalize: %v", err)
	}

	grandSub, _ := linkActivityChild(t, child, grand, "inspect grandchild")
	grandShell, err := grand.jobManager.createShell(createShellOpts{Command: "grand", Description: "grand shell"})
	if err != nil {
		t.Fatalf("grand createShell: %v", err)
	}
	if err := grand.jobManager.finalize(grandShell.JobID, jobstore.StatusCompleted, "done", nil); err != nil {
		t.Fatalf("grand finalize: %v", err)
	}
	grandSub.mu.Lock()
	grandSub.closed = true
	grandSub.mu.Unlock()

	saveActivityMeta(t, stateDir, root)
	saveActivityMeta(t, stateDir, child)
	saveActivityMeta(t, stateDir, grand)

	got, err := root.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root.SessionID != root.ID() {
		t.Fatalf("root session=%q", got.Root.SessionID)
	}
	if len(got.Root.Entries) != 2 {
		t.Fatalf("root entries=%d, want shell + delegate", len(got.Root.Entries))
	}
	delegate := got.Root.Entries[1].Delegate
	if delegate == nil {
		t.Fatalf("root second entry=%+v, want delegate", got.Root.Entries[1])
	}
	if len(delegate.Turns) != 2 {
		t.Fatalf("delegate turns=%+v, want one grouped row with two turns", delegate.Turns)
	}
	if delegate.Child == nil {
		t.Fatalf("delegate child missing: %+v", delegate)
	}
	childTree := delegate.Child
	if childTree.SessionID != child.ID() {
		t.Fatalf("child session=%q, want %q", childTree.SessionID, child.ID())
	}
	if childTree.Entries[0].Job == nil || childTree.Entries[0].Job.OwnerSessionID != child.ID() {
		t.Fatalf("child shell owner=%+v", childTree.Entries[0])
	}
	grandDelegate := findDelegateEntry(t, *childTree, grand.ID())
	if grandDelegate.Child == nil || grandDelegate.Branch.Error != "" {
		t.Fatalf("grandchild branch=%+v child=%+v", grandDelegate.Branch, grandDelegate.Child)
	}
	if grandDelegate.Child.Entries[0].Job == nil || grandDelegate.Child.Entries[0].Job.OwnerSessionID != grand.ID() {
		t.Fatalf("grandchild owner=%+v", grandDelegate.Child.Entries[0])
	}
}

func TestJobActivityTree_TruncatesWithScopedContinuation(t *testing.T) {
	s := buildActivityTreeWithJobs(t, activityMaxWorkUnits+1)
	got, err := s.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	branch := firstTruncatedBranch(t, got.Root)
	if !branch.Truncated || branch.Continuation == "" {
		t.Fatalf("branch=%+v", branch)
	}
	if _, err := decodeActivityContinuation(branch.Continuation, "other-root"); err == nil {
		t.Fatal("continuation accepted for another root")
	}
}

func TestJobActivityTree_TruncatesAtDepth33(t *testing.T) {
	stateDir := t.TempDir()
	root := newActivityTestSession(t, stateDir)
	current := root
	for i := 0; i < activityMaxNewDepth+1; i++ {
		child := newActivityTestSession(t, stateDir)
		_, _ = linkActivityChild(t, current, child, fmt.Sprintf("child-%02d", i))
		saveActivityMeta(t, stateDir, current)
		saveActivityMeta(t, stateDir, child)
		current = child
	}
	got, err := root.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	branch := firstTruncatedBranch(t, got.Root)
	if !branch.Truncated || branch.Continuation == "" {
		t.Fatalf("branch=%+v", branch)
	}
	if depth := maxActivityDepth(got.Root); depth != activityMaxNewDepth {
		t.Fatalf("depth=%d, want %d", depth, activityMaxNewDepth)
	}
}

func TestJobActivityTree_TruncatesUnderEncodedBytePressure(t *testing.T) {
	s := buildActivityTreeWithJobs(t, 64)
	for i := 0; i < 64; i++ {
		rec := &jobstore.JobRecord{
			JobID:          fmt.Sprintf("payload_%02d", i),
			Type:           jobstore.JobShell,
			Status:         jobstore.StatusCompleted,
			OwnerSessionID: s.ID(),
			StartedAt:      time.Unix(int64(2000+i), 0).UTC(),
			Description:    strings.Repeat("x", 96*1024),
		}
		if err := s.jobManager.store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: rec.StartedAt, JobID: rec.JobID, Type: rec.Type, Description: rec.Description, OwnerSessionID: rec.OwnerSessionID, VisibleToSession: s.ID(), StartedAt: &rec.StartedAt}); err != nil {
			t.Fatalf("append payload job %d: %v", i, err)
		}
		ended := rec.StartedAt.Add(time.Second)
		if err := s.jobManager.store.Append(jobstore.Event{Kind: jobstore.EventJobFinished, TS: ended, JobID: rec.JobID, Status: jobstore.StatusCompleted, EndedAt: &ended}); err != nil {
			t.Fatalf("append payload finish %d: %v", i, err)
		}
	}
	got, err := s.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > activityMaxEncodedBytes {
		t.Fatalf("encoded bytes=%d, want <= %d", len(raw), activityMaxEncodedBytes)
	}
	branch := firstTruncatedBranch(t, got.Root)
	if !branch.Truncated || branch.Continuation == "" {
		t.Fatalf("branch=%+v", branch)
	}
}

func TestJobActivityTree_CycleDetected(t *testing.T) {
	stateDir := t.TempDir()
	root := newActivityTestSession(t, stateDir)
	child := newActivityTestSession(t, stateDir)
	_, _ = linkActivityChild(t, root, child, "child")
	rootAsGrandchild, _ := linkActivityChild(t, child, root, "cycle")
	t.Cleanup(func() {
		child.subagents.remove(root.ID())
	})
	rootAsGrandchild.mu.Lock()
	rootAsGrandchild.closed = false
	rootAsGrandchild.mu.Unlock()
	saveActivityMeta(t, stateDir, root)
	saveActivityMeta(t, stateDir, child)
	got, err := root.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	childDelegate := findDelegateEntry(t, got.Root, child.ID())
	cycleDelegate := findDelegateEntry(t, *childDelegate.Child, root.ID())
	if cycleDelegate.Branch.Error != "cycle detected" {
		t.Fatalf("cycle branch=%+v", cycleDelegate.Branch)
	}
}

func TestJobActivityTree_MalformedRefRetainsUnavailableDelegate(t *testing.T) {
	stateDir := t.TempDir()
	s := newActivityTestSession(t, stateDir)
	now := time.Unix(100, 0).UTC()
	if err := s.jobManager.store.AppendBatch([]jobstore.Event{
		{
			Kind:       jobstore.EventDelegateCreated,
			TS:         now,
			DelegateID: "dlg_bad",
			Delegate: &jobstore.DelegateEvent{
				ChildSessionID:   "child",
				TranscriptRef:    "local:bad.ref",
				OwnerSessionID:   s.ID(),
				VisibleSessionID: s.ID(),
				Generation:       "gen_1",
				Resumable:        true,
			},
		},
		{
			Kind:             jobstore.EventJobStarted,
			TS:               now,
			JobID:            "job_bad_ref",
			Type:             jobstore.JobDelegate,
			OwnerSessionID:   s.ID(),
			VisibleToSession: s.ID(),
			DelegateID:       "dlg_bad",
			StartedAt:        &now,
		},
	}); err != nil {
		t.Fatalf("append malformed delegate: %v", err)
	}
	saveActivityMeta(t, stateDir, s)
	got, err := s.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Root.Entries) != 1 || got.Root.Entries[0].Delegate == nil {
		t.Fatalf("entries=%+v", got.Root.Entries)
	}
	if !strings.Contains(got.Root.Entries[0].Delegate.Branch.Error, "transcript ref") {
		t.Fatalf("branch=%+v", got.Root.Entries[0].Delegate.Branch)
	}
}

func TestJobActivityTree_UnavailableDescendantRetainsDelegate(t *testing.T) {
	stateDir := t.TempDir()
	s := newActivityTestSession(t, stateDir)
	now := time.Unix(100, 0).UTC()
	if err := s.jobManager.store.AppendBatch([]jobstore.Event{
		{
			Kind:       jobstore.EventDelegateCreated,
			TS:         now,
			DelegateID: "dlg_missing",
			Delegate: &jobstore.DelegateEvent{
				ChildSessionID:   "missing",
				TranscriptRef:    encodeRef("", "missing"),
				OwnerSessionID:   s.ID(),
				VisibleSessionID: s.ID(),
				Generation:       "gen_1",
				Resumable:        true,
			},
		},
		{
			Kind:             jobstore.EventJobStarted,
			TS:               now,
			JobID:            "job_missing",
			Type:             jobstore.JobDelegate,
			OwnerSessionID:   s.ID(),
			VisibleToSession: s.ID(),
			DelegateID:       "dlg_missing",
			StartedAt:        &now,
		},
	}); err != nil {
		t.Fatalf("append missing delegate: %v", err)
	}
	saveActivityMeta(t, stateDir, s)
	got, err := s.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	delegate := got.Root.Entries[0].Delegate
	if delegate == nil || delegate.Child != nil || delegate.Branch.Error == "" {
		t.Fatalf("delegate=%+v", delegate)
	}
}

func TestDecodeActivityContinuation_Validation(t *testing.T) {
	valid := encodeActivityContinuation(activityContinuation{Version: 1, RootID: "root", SessionID: "root", Path: []string{"dlg_1"}})
	if got, err := decodeActivityContinuation(valid, "root"); err != nil || got.SessionID != "root" || !reflect.DeepEqual(got.Path, []string{"dlg_1"}) {
		t.Fatalf("decode valid=(%+v,%v)", got, err)
	}
	tooLong := strings.Repeat("a", 16*1024+1)
	cases := []struct {
		name  string
		token string
	}{
		{name: "too long", token: tooLong},
		{name: "bad base64", token: "%%%"},
		{name: "bad version", token: base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"root":"root","session":"root"}`))},
		{name: "duplicate path", token: base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"root":"root","session":"root","path":["dlg_1","dlg_1"]}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeActivityContinuation(tc.token, "root"); err == nil {
				t.Fatalf("decode accepted %q", tc.name)
			}
		})
	}
}

func TestJobActivityTree_ContinuationGraftEnvelope(t *testing.T) {
	stateDir := t.TempDir()
	root := newActivityTestSession(t, stateDir)
	child := newActivityTestSession(t, stateDir)
	_, _ = linkActivityChild(t, root, child, "child")
	saveActivityMeta(t, stateDir, root)
	saveActivityMeta(t, stateDir, child)
	for i := 0; i < activityMaxWorkUnits+4; i++ {
		started := time.Unix(int64(1000+i), 0).UTC()
		jobID := fmt.Sprintf("job_child_%04d", i)
		if err := child.jobManager.store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell, Description: "child work", OwnerSessionID: child.ID(), VisibleToSession: child.ID(), StartedAt: &started}); err != nil {
			t.Fatalf("append child job %d: %v", i, err)
		}
	}
	initial, err := root.JobActivityTree(appwire.JobsListParams{})
	if err != nil {
		t.Fatal(err)
	}
	childDelegate := findDelegateEntry(t, initial.Root, child.ID())
	if childDelegate.Child == nil {
		t.Fatalf("child branch missing: %+v", childDelegate)
	}
	if !childDelegate.Child.Branch.Truncated || childDelegate.Child.Branch.Continuation == "" {
		t.Fatalf("child branch=%+v", childDelegate.Child.Branch)
	}
	continued, err := root.JobActivityTree(appwire.JobsListParams{Continuation: childDelegate.Child.Branch.Continuation})
	if err != nil {
		t.Fatal(err)
	}
	if continued.Root.SessionID != root.ID() || continued.Root.Ref != encodeRef("", root.ID()) {
		t.Fatalf("continued root=%+v", continued.Root)
	}
	if len(continued.Root.Entries) != 1 || continued.Root.Entries[0].Delegate == nil {
		t.Fatalf("continued entries=%+v, want only delegate path envelope", continued.Root.Entries)
	}
	if continued.Root.Entries[0].Delegate.Child == nil || continued.Root.Entries[0].Delegate.Child.SessionID != child.ID() {
		t.Fatalf("continued child=%+v", continued.Root.Entries[0].Delegate)
	}
}

func ptrActivityJob(job appwire.JobActivityJob) *appwire.JobActivityJob { return &job }

func activityRecordIDs(records []*jobstore.JobRecord) []string {
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		ids = append(ids, rec.JobID)
	}
	return ids
}

func newActivityTestSession(t *testing.T, stateDir string) *Session {
	t.Helper()
	return newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 8,
		NoProjectPrompts: true,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
}

func linkActivityChild(t *testing.T, parent, child *Session, task string) (*subagent, *runningJob) {
	t.Helper()
	sub := &subagent{id: child.ID(), sess: child, status: SubagentRunning, done: make(chan struct{})}
	parent.subagents.track(sub)
	run, err := parent.attachDelegateJob(parent.jobManager, child.ID(), task, sub)
	if err != nil {
		t.Fatalf("attachDelegateJob(%s->%s): %v", parent.ID(), child.ID(), err)
	}
	child.jobManager.forward = parent.jobManager.forwardEvent
	child.jobManager.setParentJobID(run.rec.JobID)
	return sub, run
}

func saveActivityMeta(t *testing.T, stateDir string, s *Session) {
	t.Helper()
	if err := schema.SaveSessionMeta(stateDir, s.Meta()); err != nil {
		t.Fatalf("SaveSessionMeta(%s): %v", s.ID(), err)
	}
}

func buildActivityTreeWithJobs(t *testing.T, count int) *Session {
	t.Helper()
	stateDir := t.TempDir()
	s := newActivityTestSession(t, stateDir)
	for i := 0; i < count; i++ {
		started := time.Unix(int64(i+1), 0).UTC()
		jobID := fmt.Sprintf("job_tree_%04d", i)
		if err := s.jobManager.store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, TS: started, JobID: jobID, Type: jobstore.JobShell, Description: "tree job", OwnerSessionID: s.ID(), VisibleToSession: s.ID(), StartedAt: &started}); err != nil {
			t.Fatalf("append job %d: %v", i, err)
		}
	}
	saveActivityMeta(t, stateDir, s)
	return s
}

func firstTruncatedBranch(t *testing.T, root appwire.JobActivitySession) appwire.JobActivityBranchState {
	t.Helper()
	if root.Branch.Truncated {
		return root.Branch
	}
	for _, entry := range root.Entries {
		if entry.Delegate == nil {
			continue
		}
		if entry.Delegate.Branch.Truncated {
			return entry.Delegate.Branch
		}
		if entry.Delegate.Child != nil {
			if branch := firstTruncatedBranch(t, *entry.Delegate.Child); branch.Truncated {
				return branch
			}
		}
	}
	t.Fatalf("no truncated branch in %+v", root)
	return appwire.JobActivityBranchState{}
}

func maxActivityDepth(root appwire.JobActivitySession) int {
	maxDepth := 0
	var walk func(appwire.JobActivitySession, int)
	walk = func(node appwire.JobActivitySession, depth int) {
		if depth > maxDepth {
			maxDepth = depth
		}
		for _, entry := range node.Entries {
			if entry.Delegate != nil && entry.Delegate.Child != nil {
				walk(*entry.Delegate.Child, depth+1)
			}
		}
	}
	walk(root, 0)
	return maxDepth
}

func findDelegateEntry(t *testing.T, root appwire.JobActivitySession, childID string) appwire.JobActivityDelegate {
	t.Helper()
	for _, entry := range root.Entries {
		if entry.Delegate == nil {
			continue
		}
		if entry.Delegate.ChildSessionID == childID {
			return *entry.Delegate
		}
	}
	t.Fatalf("no delegate for child %q in %+v", childID, root.Entries)
	return appwire.JobActivityDelegate{}
}

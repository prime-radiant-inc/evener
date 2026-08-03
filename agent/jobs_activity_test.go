package agent

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
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

func ptrActivityJob(job appwire.JobActivityJob) *appwire.JobActivityJob { return &job }

func activityRecordIDs(records []*jobstore.JobRecord) []string {
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		ids = append(ids, rec.JobID)
	}
	return ids
}

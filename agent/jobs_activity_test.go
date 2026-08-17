package agent

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/appwire"
)

func TestProjectActivitySession_ProjectsStableDelegateAndShell(t *testing.T) {
	t.Parallel()
	child := &activitySessionSnapshot{
		SessionID: "child", Ref: "local:child", Label: "Child",
		Jobs: []*jobstore.JobRecord{{JobID: "job_child", Type: jobstore.JobShell, OwnerSessionID: "child", Status: jobstore.StatusCompleted}},
	}
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", Label: "Root", RootID: "root",
		Jobs: []*jobstore.JobRecord{{JobID: "job_root", Type: jobstore.JobShell, OwnerSessionID: "root", Status: jobstore.StatusRunning}},
		StableDelegates: map[string]delegateSnapshot{
			"dlg_1": stableActivitySnapshot("dlg_1", "root", "child", "inspect child"),
		},
		Children: map[string]*activitySessionSnapshot{"child": child},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if len(got.Entries) != 2 || got.Entries[0].Job == nil || got.Entries[1].Delegate == nil {
		t.Fatalf("entries = %+v, want shell plus stable delegate", got.Entries)
	}
	delegate := got.Entries[1].Delegate
	if delegate.DelegateID != "dlg_1" || delegate.ChildSessionID != "child" || delegate.ChildRef != "local:child" || delegate.Mandate != "inspect child" || delegate.Child == nil {
		t.Fatalf("delegate = %+v", delegate)
	}
	if len(delegate.Turns) != 0 {
		t.Fatalf("stable delegate exposed legacy JobRecord turns: %+v", delegate.Turns)
	}
	if delegate.Child.Entries[0].Job.OwnerSessionID != "child" {
		t.Fatalf("child owner = %+v", delegate.Child.Entries[0].Job)
	}
}

func TestProjectActivitySession_UnavailableStableChildPreservesDelegate(t *testing.T) {
	t.Parallel()
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{
			"dlg_1": stableActivitySnapshot("dlg_1", "root", "child", "inspect child"),
		},
		Errors: map[string]error{"child": errActivityHistoryUnavailable},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if len(got.Entries) != 1 || got.Entries[0].Delegate == nil {
		t.Fatalf("entries = %+v, want retained delegate", got.Entries)
	}
	delegate := got.Entries[0].Delegate
	if delegate.Child != nil || delegate.Branch.Error == "" || got.Aggregate != "working" || got.Counts.Active != 1 || got.Counts.Complete {
		t.Fatalf("delegate = %+v aggregate=%q counts=%+v", delegate, got.Aggregate, got.Counts)
	}
}

func TestActivityChildSessionForStableRejectsMalformedLinks(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		row  delegateSnapshot
	}{
		{name: "missing child", row: delegateSnapshot{id: "dlg_1", descriptor: delegatestore.Descriptor{TranscriptRef: "local:child"}}},
		{name: "missing ref", row: delegateSnapshot{id: "dlg_1", descriptor: delegatestore.Descriptor{ChildSessionID: "child"}}},
		{name: "mismatched ref", row: delegateSnapshot{id: "dlg_1", descriptor: delegatestore.Descriptor{ChildSessionID: "child", TranscriptRef: "local:other"}}},
		{name: "cross boundary", row: delegateSnapshot{id: "dlg_1", descriptor: delegatestore.Descriptor{ChildSessionID: "child", TranscriptRef: "project:child"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := activityChildSessionForStable(test.row); err == nil {
				t.Fatalf("malformed row accepted: %#v", test.row)
			}
		})
	}
}

func TestActivityOutcome(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status   jobstore.Status
		terminal bool
		outcome  string
	}{
		{status: jobstore.StatusRunning},
		{status: jobstore.StatusFailed, terminal: true, outcome: "failure"},
		{status: jobstore.StatusExhausted, terminal: true, outcome: "failure"},
		{status: jobstore.StatusCompleted, terminal: true, outcome: "success"},
		{status: jobstore.StatusCancelled, terminal: true, outcome: "neutral"},
		{status: jobstore.StatusStopped, terminal: true, outcome: "neutral"},
	} {
		terminal, outcome := activityOutcome(test.status)
		if terminal != test.terminal || outcome != test.outcome {
			t.Fatalf("activityOutcome(%q) = (%t,%q), want (%t,%q)", test.status, terminal, outcome, test.terminal, test.outcome)
		}
	}
}

func TestAggregateActivity_CountsWorkUnitsRecursively(t *testing.T) {
	t.Parallel()
	job := func(id, status, outcome string, terminal bool) appwire.JobActivityJob {
		return appwire.JobActivityJob{JobID: id, Status: status, Outcome: outcome, Terminal: terminal}
	}
	// Type, not Kind, is what aggregateActivity switches on to count a stable
	// delegate as a work unit in its own right. Kind is the ENTRY discriminator;
	// an entry whose Type is unset takes the legacy-turn branch, which iterates
	// Turns and counts the delegate itself as nothing.
	entries := []appwire.JobActivityEntry{
		{Kind: "shell", Job: ptrActivityJob(job("job_running", "running", "", false))},
		{Kind: "delegate", Delegate: &appwire.JobActivityDelegate{
			Type: "delegate", Outcome: "failed", Terminal: true,
			Child: &appwire.JobActivitySession{Counts: appwire.JobActivityCounts{Active: 1, Failed: 1, Completed: 2, Complete: true}},
		}},
	}
	// Failed is 2: the child's one failure, plus the failed stable delegate
	// itself. Active outranks it in the aggregate, so the verdict is "working".
	got, aggregate := aggregateActivity(entries, appwire.JobActivityBranchState{})
	if got.Active != 2 || got.Failed != 2 || got.Completed != 2 || !got.Complete || aggregate != "working" {
		t.Fatalf("counts=%+v aggregate=%q", got, aggregate)
	}
}

func TestMergeActivityRecords_LiveOverlayHasNoDuplicate(t *testing.T) {
	t.Parallel()
	durable := []*jobstore.JobRecord{
		{JobID: "job_b", Status: jobstore.StatusRunning, StartedAt: time.Unix(2, 0)},
		{JobID: "job_a", Status: jobstore.StatusCompleted, StartedAt: time.Unix(1, 0)},
	}
	live := map[string]*jobstore.JobRecord{
		"job_b": {JobID: "job_b", Status: jobstore.StatusCompleted, StartedAt: time.Unix(2, 0)},
		"job_c": {JobID: "job_c", Status: jobstore.StatusRunning, StartedAt: time.Unix(3, 0)},
	}
	got := mergeActivityRecords(durable, live)
	if ids := activityRecordIDs(got); !reflect.DeepEqual(ids, []string{"job_b", "job_a", "job_c"}) {
		t.Fatalf("ids=%v", ids)
	}
	if got[0].Status != jobstore.StatusCompleted {
		t.Fatalf("live overlay not applied: %+v", got[0])
	}
	got[0].Status = jobstore.StatusFailed
	if live["job_b"].Status != jobstore.StatusCompleted {
		t.Fatal("merge returned an un-cloned live record")
	}
}

func TestProjectStableLiveActivityTree_RejectsSnapshotAfterBoundedRevisionChurn(t *testing.T) {
	t.Parallel()
	clock := newJobActivityClock("root")
	loads := 0
	load := func() (*activitySessionSnapshot, int, error) {
		loads++
		clock.revision.Add(1)
		return &activitySessionSnapshot{SessionID: "root", Ref: "local:root", RootID: "root"}, 0, nil
	}
	if _, err := projectStableLiveActivityTree(clock, "root", load); err == nil {
		t.Fatal("revision churn produced an inconsistent snapshot")
	}
	if loads != 8 {
		t.Fatalf("snapshot loads=%d, want 8", loads)
	}
}

func TestProjectActivitySession_TruncatesStableRowsWithScopedContinuation(t *testing.T) {
	t.Parallel()
	delegates := make(map[string]delegateSnapshot, activityMaxWorkUnits+1)
	for i := range activityMaxWorkUnits + 1 {
		id := "dlg_" + strings.Repeat("x", i%3) + time.Unix(int64(i+1), 0).UTC().Format("150405")
		delegates[id] = stableActivitySnapshot(id, "root", "child_"+id, "task")
	}
	snap := activitySessionSnapshot{SessionID: "root", Ref: "local:root", RootID: "root", StableDelegates: delegates}
	tree, err := projectBoundedActivityTree(snap, "root", 0, 1, time.Unix(1, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !tree.Root.Branch.Truncated || tree.Root.Branch.Continuation == "" {
		t.Fatalf("branch=%+v", tree.Root.Branch)
	}
	if _, err := decodeActivityContinuation(tree.Root.Branch.Continuation, "other-root"); err == nil {
		t.Fatal("continuation accepted for another root")
	}
}

func TestDecodeActivityContinuation_Validation(t *testing.T) {
	t.Parallel()
	valid := encodeActivityContinuation(activityContinuation{Version: 1, RootID: "root", SessionID: "root", Path: []string{"dlg_1"}})
	if got, err := decodeActivityContinuation(valid, "root"); err != nil || got.SessionID != "root" || !reflect.DeepEqual(got.Path, []string{"dlg_1"}) {
		t.Fatalf("decode valid=(%+v,%v)", got, err)
	}
	for _, token := range []string{
		strings.Repeat("a", 16*1024+1),
		"%%%",
		base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"root":"root","session":"root"}`)),
		base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"root":"root","session":"root","path":["dlg_1","dlg_1"]}`)),
	} {
		if _, err := decodeActivityContinuation(token, "root"); err == nil {
			t.Fatalf("invalid continuation accepted: %q", token)
		}
	}
}

func TestProjectActivityJobStampsLastOutputAt(t *testing.T) {
	last := time.Date(2026, 8, 5, 15, 2, 11, 0, time.UTC)
	job := projectActivityJob(&jobstore.JobRecord{
		JobID: "job_live", Type: jobstore.JobShell, Status: jobstore.StatusRunning,
		Description: "make test-web", StartedAt: last.Add(-4 * time.Minute), LastActivity: &last,
	}, "ref_root")
	if job.LastOutputAt != "2026-08-05T15:02:11Z" {
		t.Fatalf("LastOutputAt=%q", job.LastOutputAt)
	}
}

func TestProjectStableActivityDelegateCopiesChildUsage(t *testing.T) {
	want := &appwire.SerfUsage{InputTokens: 41200, OutputTokens: 6100}
	child := &activitySessionSnapshot{SessionID: "child", Ref: "local:child", Usage: want}
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": stableActivitySnapshot("dlg_1", "root", "child", "inspect")},
		Children:        map[string]*activitySessionSnapshot{"child": child},
	}
	delegate := projectStableActivityDelegate(snap, snap.StableDelegates["dlg_1"], newActivityBudget(), 0, nil)
	if delegate.Usage == nil || *delegate.Usage != *want || delegate.Usage == want {
		t.Fatalf("delegate usage=%+v want copy of %+v", delegate.Usage, want)
	}
}

func stableActivitySnapshot(id, ownerSessionID, childSessionID, task string) delegateSnapshot {
	return delegateSnapshot{
		id:        id,
		lifecycle: delegateLifecycleIdle,
		phase:     delegatestore.PhaseIdle,
		resumable: true,
		descriptor: delegatestore.Descriptor{
			ChildSessionID: childSessionID,
			TranscriptRef:  encodeRef("", childSessionID),
			OwnerSessionID: ownerSessionID,
			Task:           task,
			AgentType:      "general",
			Resumable:      true,
		},
	}
}

var errActivityHistoryUnavailable = &activityTestError{"history unavailable"}

type activityTestError struct{ message string }

func (e *activityTestError) Error() string { return e.message }

func ptrActivityJob(job appwire.JobActivityJob) *appwire.JobActivityJob { return &job }

func activityRecordIDs(records []*jobstore.JobRecord) []string {
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		ids = append(ids, rec.JobID)
	}
	return ids
}

func TestActivityTreeJSONOmitsLegacyDelegateTurns(t *testing.T) {
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": stableActivitySnapshot("dlg_1", "root", "child", "inspect")},
	}
	got := projectActivitySession(snap, newActivityBudget())
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"jobId":"job_delegate`) {
		t.Fatalf("activity JSON exposed a legacy delegate job turn: %s", raw)
	}
}

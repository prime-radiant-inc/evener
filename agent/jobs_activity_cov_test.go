package agent

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
)

// TestSortedStableActivityDelegateIDs covers sortedStableActivityDelegateIDs
// (jobs_activity.go:425-432).
func TestSortedStableActivityDelegateIDs(t *testing.T) {
	t.Parallel()
	records := map[string]delegateSnapshot{
		"dlg_c": {},
		"dlg_a": {},
		"dlg_b": {},
	}
	got := sortedStableActivityDelegateIDs(records)
	if len(got) != 3 || got[0] != "dlg_a" || got[1] != "dlg_b" || got[2] != "dlg_c" {
		t.Fatalf("sortedStableActivityDelegateIDs = %v, want [dlg_a dlg_b dlg_c]", got)
	}
	// Empty map.
	if got := sortedStableActivityDelegateIDs(nil); len(got) != 0 {
		t.Fatalf("empty map: %v", got)
	}
}

// TestSortedActivityChildIDs covers sortedActivityChildIDs
// (jobs_activity.go:504-511).
func TestSortedActivityChildIDs(t *testing.T) {
	t.Parallel()
	children := map[string]*activitySessionSnapshot{
		"child_z": {},
		"child_a": {},
		"child_m": {},
	}
	got := sortedActivityChildIDs(children)
	if len(got) != 3 || got[0] != "child_a" || got[1] != "child_m" || got[2] != "child_z" {
		t.Fatalf("sortedActivityChildIDs = %v, want [child_a child_m child_z]", got)
	}
	// Empty map.
	if got := sortedActivityChildIDs(nil); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}

// TestCloneActivityVisited covers cloneActivityVisited
// (jobs_activity.go:434-438).
func TestCloneActivityVisited(t *testing.T) {
	t.Parallel()
	visited := map[string]bool{"a": true, "b": false, "c": true}
	clone := cloneActivityVisited(visited)
	if len(clone) != 3 || !clone["a"] || clone["b"] || !clone["c"] {
		t.Fatalf("clone = %v", clone)
	}
	// Mutating clone should not affect original.
	clone["d"] = true
	if visited["d"] {
		t.Fatal("mutating clone affected original")
	}
	// Empty map.
	empty := cloneActivityVisited(nil)
	if len(empty) != 0 {
		t.Fatalf("empty: %v", empty)
	}
}

// TestActivityRecordBefore covers activityRecordBefore
// (jobs_activity.go:600-605).
func TestActivityRecordBefore(t *testing.T) {
	t.Parallel()
	t1 := time.Unix(1000, 0)
	t2 := time.Unix(2000, 0)
	// Different StartedAt: earlier wins.
	left := &jobstore.JobRecord{JobID: "job_a", StartedAt: t1}
	right := &jobstore.JobRecord{JobID: "job_b", StartedAt: t2}
	if !activityRecordBefore(left, right) {
		t.Fatal("earlier StartedAt should come before later")
	}
	if activityRecordBefore(right, left) {
		t.Fatal("later StartedAt should not come before earlier")
	}
	// Same StartedAt: JobID is the tiebreaker.
	left2 := &jobstore.JobRecord{JobID: "job_a", StartedAt: t1}
	right2 := &jobstore.JobRecord{JobID: "job_b", StartedAt: t1}
	if !activityRecordBefore(left2, right2) {
		t.Fatal("job_a should come before job_b at same time")
	}
	if activityRecordBefore(right2, left2) {
		t.Fatal("job_b should not come before job_a at same time")
	}
}

// TestActivityCurrentRootRevision covers activityCurrentRootRevision
// (jobs_activity.go:184-189) including the nil-clock guard.
func TestActivityCurrentRootRevision(t *testing.T) {
	t.Parallel()
	// Nil clock.
	if got := activityCurrentRootRevision(nil); got != 0 {
		t.Fatalf("nil clock: %d, want 0", got)
	}
}

// TestActivityCurrentRootID covers activityCurrentRootID
// (jobs_activity.go:191-196).
func TestActivityCurrentRootID(t *testing.T) {
	t.Parallel()
	// Nil clock uses fallback.
	if got := activityCurrentRootID(nil, "fallback"); got != "fallback" {
		t.Fatalf("nil clock: %q, want fallback", got)
	}
	// Empty fallback.
	if got := activityCurrentRootID(nil, ""); got != "" {
		t.Fatalf("nil clock empty fallback: %q", got)
	}
}

// TestMergeActivityRecords covers mergeActivityRecords
// (jobs_activity.go:516-568) including overlay and live-only insertion.
func TestMergeActivityRecords(t *testing.T) {
	t.Parallel()
	t1 := time.Unix(1000, 0)
	t2 := time.Unix(2000, 0)
	t3 := time.Unix(3000, 0)
	durable := []*jobstore.JobRecord{
		{JobID: "job_1", StartedAt: t1, Status: jobstore.StatusCompleted},
		{JobID: "job_2", StartedAt: t2, Status: jobstore.StatusCompleted},
	}
	live := map[string]*jobstore.JobRecord{
		"job_2": {JobID: "job_2", StartedAt: t2, Status: jobstore.StatusRunning},
		"job_3": {JobID: "job_3", StartedAt: t3, Status: jobstore.StatusRunning},
	}
	merged := mergeActivityRecords(durable, live)
	if len(merged) != 3 {
		t.Fatalf("merged count = %d, want 3: %+v", len(merged), merged)
	}
	// job_1 from durable, job_2 from live (overlay), job_3 from live-only.
	byID := make(map[string]*jobstore.JobRecord)
	for _, rec := range merged {
		byID[rec.JobID] = rec
	}
	if rec := byID["job_1"]; rec == nil || rec.Status != jobstore.StatusCompleted {
		t.Fatalf("job_1 = %+v, want completed from durable", rec)
	}
	if rec := byID["job_2"]; rec == nil || rec.Status != jobstore.StatusRunning {
		t.Fatalf("job_2 = %+v, want running from live overlay", rec)
	}
	if rec := byID["job_3"]; rec == nil || rec.Status != jobstore.StatusRunning {
		t.Fatalf("job_3 = %+v, want running from live-only", rec)
	}
	// Durable order preserved: job_1 before job_2.
	if merged[0].JobID != "job_1" || merged[1].JobID != "job_2" {
		t.Fatalf("order = %v %v, want job_1 then job_2", merged[0].JobID, merged[1].JobID)
	}
	// Live-only job_3 inserted last.
	if merged[2].JobID != "job_3" {
		t.Fatalf("last = %v, want job_3", merged[2].JobID)
	}
}

// TestMergeActivityRecordsNilEntries covers the nil-entry skip path.
func TestMergeActivityRecordsNilEntries(t *testing.T) {
	t.Parallel()
	durable := []*jobstore.JobRecord{nil, {JobID: "", StartedAt: time.Unix(0, 0)}}
	live := map[string]*jobstore.JobRecord{
		"": nil,
	}
	merged := mergeActivityRecords(durable, live)
	if len(merged) != 0 {
		t.Fatalf("nil/empty entries: merged = %d, want 0", len(merged))
	}
}

// TestNewActivityBudget covers newActivityBudget (jobs_activity.go:74-76).
func TestNewActivityBudget(t *testing.T) {
	t.Parallel()
	b := newActivityBudget()
	if b == nil || b.visiting == nil {
		t.Fatal("newActivityBudget should return non-nil with initialized visiting map")
	}
}

// TestNewBoundedActivityBudget covers newBoundedActivityBudget
// (jobs_activity.go:78-87).
func TestNewBoundedActivityBudget(t *testing.T) {
	t.Parallel()
	now := time.Unix(5000, 0)
	b := newBoundedActivityBudget("root", now)
	if b == nil || !b.bounded || b.rootID != "root" || b.maxWorkUnits != activityMaxWorkUnits || b.maxDepth != activityMaxNewDepth {
		t.Fatalf("bounded budget = %+v", b)
	}
	if !b.now.Equal(now) {
		t.Fatalf("now = %v, want %v", b.now, now)
	}
}

// TestLiveActivitySessionLabel covers liveActivitySessionLabel
// (jobs_activity.go:355-371) including the nil-session guard.
func TestLiveActivitySessionLabel(t *testing.T) {
	t.Parallel()
	// Nil session.
	if got := liveActivitySessionLabel(nil); got != "" {
		t.Fatalf("nil: %q", got)
	}
	// Real session uses id as label when no name/prompt.
	s := newTestSession(t)
	label := liveActivitySessionLabel(s)
	if label == "" {
		t.Fatal("label should not be empty for a real session")
	}
}

// TestActivitySessionLabel covers activitySessionLabel
// (jobs_activity.go:400-405).
func TestActivitySessionLabelCov(t *testing.T) {
	t.Parallel()
	// With display name.
	meta := schema.SessionMeta{ID: "sess_1", Name: "My Session"}
	if got := activitySessionLabel(meta); got != "My Session" {
		t.Fatalf("with name: %q", got)
	}
	// Without name, uses ID.
	meta = schema.SessionMeta{ID: "sess_1"}
	if got := activitySessionLabel(meta); got != "sess_1" {
		t.Fatalf("without name: %q, want sess_1", got)
	}
	// Empty meta.
	meta = schema.SessionMeta{}
	if got := activitySessionLabel(meta); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

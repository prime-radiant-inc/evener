package agent

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
)

// TestSortedStableActivityDelegateIDs covers sortedStableActivityDelegateIDs.
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

// TestSortedActivityChildIDs covers sortedActivityChildIDs.
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

// TestCloneActivityVisited covers cloneActivityVisited.
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

// TestActivityCurrentRootRevision covers activityCurrentRootRevision including the nil-clock guard.
func TestActivityCurrentRootRevision(t *testing.T) {
	t.Parallel()
	// Nil clock.
	if got := activityCurrentRootRevision(nil); got != 0 {
		t.Fatalf("nil clock: %d, want 0", got)
	}
}

// TestActivityCurrentRootID covers activityCurrentRootID.
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

// TestActivityJobListLiveOverlay covers the live overlay the activity read
// applies to a folded list: a running job's record replaces the durable record
// at the same position, the list keeps its durable order, and a record with no
// durable counterpart adds no row — a live entry only exists once its
// job_started event is committed, so the folded list already carries it.
func TestActivityJobListLiveOverlay(t *testing.T) {
	t.Parallel()
	t1 := time.Unix(1000, 0)
	t2 := time.Unix(2000, 0)
	durable := newActivityJobList([]*jobstore.JobRecord{
		{JobID: "job_1", StartedAt: t1, Status: jobstore.StatusCompleted},
		{JobID: "job_2", StartedAt: t2, Status: jobstore.StatusCompleted},
	})
	live := map[string]*jobstore.JobRecord{
		"job_2": {JobID: "job_2", Status: jobstore.StatusRunning},
		"job_3": {JobID: "job_3", StartedAt: t2, Status: jobstore.StatusRunning},
	}
	overlaid := durable.withLive(live)
	if got := overlaid.len(); got != 2 {
		t.Fatalf("overlaid list length = %d, want the durable 2", got)
	}
	if got := durable.len(); got != 2 {
		t.Fatalf("base list length changed to %d; withLive must not mutate it", got)
	}
	byID := make(map[string]*jobstore.JobRecord)
	var order []string
	for rec := range overlaid.all() {
		byID[rec.JobID] = rec
		order = append(order, rec.JobID)
	}
	if !reflect.DeepEqual(order, []string{"job_1", "job_2"}) {
		t.Fatalf("order = %v, want the durable order [job_1 job_2]", order)
	}
	if rec := byID["job_1"]; rec == nil || rec.Status != jobstore.StatusCompleted {
		t.Fatalf("job_1 = %+v, want completed from durable", rec)
	}
	if rec := byID["job_2"]; rec == nil || rec.Status != jobstore.StatusRunning {
		t.Fatalf("job_2 = %+v, want running from the live overlay", rec)
	}
	// The overlay hands back the live record, not a copy of it: the caller
	// owns the live snapshot (jobManager.liveJobRecords already cloned it).
	if got := byID["job_2"]; got != live["job_2"] {
		t.Fatalf("job_2 = %p, want the live record %p", got, live["job_2"])
	}
	// The base list still reports its own, durable record.
	for rec := range durable.all() {
		if rec.JobID == "job_2" && rec.Status != jobstore.StatusCompleted {
			t.Fatalf("base list job_2 = %+v, want the durable completed record", rec)
		}
	}
}

// TestActivityJobListLiveOverlayWithoutLiveRecords pins the no-overlay case:
// with no live records the list is returned as-is, so a quiet session serves
// its page from the fold with no extra work.
func TestActivityJobListLiveOverlayWithoutLiveRecords(t *testing.T) {
	t.Parallel()
	durable := newActivityJobList([]*jobstore.JobRecord{
		{JobID: "job_1", StartedAt: time.Unix(1000, 0), Status: jobstore.StatusCompleted},
	})
	if got := durable.withLive(nil); got != durable {
		t.Fatal("withLive(nil) allocated a new list, want the fold's own list")
	}
	var nilList *activityJobList
	if got := nilList.withLive(map[string]*jobstore.JobRecord{"job_1": {JobID: "job_1"}}); got != nil {
		t.Fatal("a nil list gained an overlay")
	}
}

// TestNewActivityBudget covers newActivityBudget.
func TestNewActivityBudget(t *testing.T) {
	t.Parallel()
	b := newActivityBudget()
	if b == nil || b.visiting == nil {
		t.Fatal("newActivityBudget should return non-nil with initialized visiting map")
	}
}

// TestNewBoundedActivityBudget covers newBoundedActivityBudget.
func TestNewBoundedActivityBudget(t *testing.T) {
	t.Parallel()
	now := time.Unix(5000, 0)
	b := newBoundedActivityBudget("root", now, 17)
	if b == nil || !b.bounded || b.rootID != "root" || b.maxWorkUnits != activityMaxWorkUnits || b.maxDepth != activityMaxNewDepth || b.revision != 17 {
		t.Fatalf("bounded budget = %+v", b)
	}
	if !b.now.Equal(now) {
		t.Fatalf("now = %v, want %v", b.now, now)
	}
}

// TestLiveActivitySessionLabel covers liveActivitySessionLabel including the nil-session guard.
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

// TestActivitySessionLabel covers activitySessionLabel.
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

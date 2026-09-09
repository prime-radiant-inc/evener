package schema

import (
	"testing"
	"time"
)

// TestLiveWaitsSkipsFired pins the wire-projection filter: claim-consumed
// leases (FiredEpoch != 0) never project to the wire wait list.
func TestLiveWaitsSkipsFired(t *testing.T) {
	t.Parallel()
	now := time.Now()
	live := LiveWaits([]GoalWaitSnapshot{
		{WaitID: "wait_1", Label: "a", Deadline: now},
		{WaitID: "wait_2", Label: "b", Deadline: now, FiredEpoch: 1},
	})
	if len(live) != 1 || live[0].WaitID != "wait_1" {
		t.Fatalf("LiveWaits = %+v, want only wait_1", live)
	}
	if got := LiveWaits(nil); len(got) != 0 {
		t.Fatalf("LiveWaits(nil) = %+v, want empty", got)
	}
}

// TestNearestWaitEarliestDeadlineTieBreaksOnWaitID pins the spec §6 chip
// rule shared by every wire converter: earliest deadline wins, ties break
// on the smallest wait_id. Nil when no live wait stands.
func TestNearestWaitEarliestDeadlineTieBreaksOnWaitID(t *testing.T) {
	t.Parallel()
	now := time.Now()
	later := now.Add(time.Hour)
	waits := []GoalWaitSnapshot{
		{WaitID: "wait_2", Label: "beta", Deadline: now},
		{WaitID: "wait_1", Label: "alpha", Deadline: now},
		{WaitID: "wait_3", Label: "late", Deadline: later},
		{WaitID: "wait_9", Label: "fired", Deadline: now.Add(-time.Hour), FiredEpoch: 1},
	}
	nearest, ok := NearestWait(waits)
	if !ok || nearest.WaitID != "wait_1" || nearest.Label != "alpha" {
		t.Fatalf("NearestWait = %+v ok=%v, want wait_1/alpha (tie breaks on smallest id)", nearest, ok)
	}
	// Earliest deadline beats registration order.
	early := []GoalWaitSnapshot{
		{WaitID: "wait_1", Label: "later", Deadline: later},
		{WaitID: "wait_2", Label: "earlier", Deadline: now},
	}
	if nearest, ok := NearestWait(early); !ok || nearest.WaitID != "wait_2" {
		t.Fatalf("NearestWait = %+v ok=%v, want wait_2 (earliest deadline wins)", nearest, ok)
	}
	if _, ok := NearestWait(nil); ok {
		t.Fatal("NearestWait(nil) = ok, want false")
	}
	if _, ok := NearestWait([]GoalWaitSnapshot{{WaitID: "wait_1", FiredEpoch: 1}}); ok {
		t.Fatal("NearestWait(all fired) = ok, want false")
	}
}

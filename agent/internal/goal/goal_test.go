package goal

import (
	"testing"
	"time"
)

// clock returns a fixed time for deterministic tests.
func clock() time.Time { return time.Unix(0, 0).UTC() }

func TestShouldContinue(t *testing.T) {
	cases := []struct {
		name string
		snap Snapshot
		want bool
	}{
		{
			"active under caps",
			Snapshot{Status: StatusActive, Iterations: 0, NoProgressStreak: 0},
			true,
		},
		{
			"complete stops",
			Snapshot{Status: StatusComplete, Iterations: 0},
			false,
		},
		{
			"blocked stops",
			Snapshot{Status: StatusBlocked, Iterations: 0},
			false,
		},
		{
			"iteration cap stops",
			Snapshot{Status: StatusActive, Iterations: DefaultMaxIterations},
			false,
		},
		{
			"no-progress cap stops",
			Snapshot{Status: StatusActive, NoProgressStreak: NoProgressLimit},
			false,
		},
		{
			"one below both caps continues",
			Snapshot{
				Status:           StatusActive,
				Iterations:       DefaultMaxIterations - 1,
				NoProgressStreak: NoProgressLimit - 1,
			},
			true,
		},
		{
			"both caps tripped simultaneously stops",
			Snapshot{
				Status:           StatusActive,
				Iterations:       DefaultMaxIterations,
				NoProgressStreak: NoProgressLimit,
			},
			false,
		},
		{
			"noProgressStreak greater than limit stops",
			Snapshot{
				Status:           StatusActive,
				Iterations:       0,
				NoProgressStreak: NoProgressLimit + 1,
			},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShouldContinue(c.snap); got != c.want {
				t.Fatalf("ShouldContinue(%+v) = %v, want %v", c.snap, got, c.want)
			}
		})
	}
}

func TestStoreSetGetClear(t *testing.T) {
	s := NewStore()
	if _, ok := s.Snapshot(); ok {
		t.Fatal("empty store should report no goal")
	}
	s.Set("make tests pass", clock())
	snap, ok := s.Snapshot()
	if !ok || snap.Status != StatusActive || snap.Objective != "make tests pass" {
		t.Fatalf("after Set: %+v ok=%v", snap, ok)
	}
	s.Clear()
	if _, ok := s.Snapshot(); ok {
		t.Fatal("after Clear should report no goal")
	}
}

func TestRecordContinuationNoProgressGrace(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	// Pre-progress reads must NOT accrue the streak (grace period).
	for i := 0; i < NoProgressLimit+2; i++ {
		s.RecordContinuation(false /*progressed*/, clock())
		snap, _ := s.Snapshot()
		if snap.Status != StatusActive {
			t.Fatalf("grace: turn %d should stay active, got %v", i, snap.Status)
		}
	}
	// First progressed turn starts the clock; resets streak.
	s.RecordContinuation(true, clock())
	// Now NoProgressLimit consecutive no-progress turns must block.
	for i := 0; i < NoProgressLimit; i++ {
		s.RecordContinuation(false, clock())
	}
	snap, _ := s.Snapshot()
	if snap.Status != StatusBlocked || snap.StopReason != "no progress" {
		t.Fatalf("expected blocked/no-progress, got %v/%q", snap.Status, snap.StopReason)
	}
}

func TestSetTerminal(t *testing.T) {
	s := NewStore()
	s.Set("obj", clock())
	if !s.SetTerminal(StatusComplete, "", clock()) {
		t.Fatal("SetTerminal on active should succeed")
	}
	snap, _ := s.Snapshot()
	if snap.Status != StatusComplete {
		t.Fatalf("want complete, got %v", snap.Status)
	}
	// Second call on non-active goal is a no-op returning false.
	if s.SetTerminal(StatusBlocked, "x", clock()) {
		t.Fatal("SetTerminal on non-active should be no-op")
	}
}

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

// TestFixWave15_NearestWaitNumericTieBreak pins the round-15 LOW: the
// deadline tie-break is numeric registration order, not lexicographic
// wait_id order — wait_2 (registered 2nd) beats wait_10 (registered 10th)
// on equal deadlines. Lexicographic compare misorders "wait_10" < "wait_2".
func TestFixWave15_NearestWaitNumericTieBreak(t *testing.T) {
	t.Parallel()
	now := time.Now()
	waits := []GoalWaitSnapshot{
		{WaitID: "wait_10", Label: "tenth", Deadline: now, RegisteredAt: now.Add(-time.Minute)},
		{WaitID: "wait_2", Label: "second", Deadline: now, RegisteredAt: now.Add(-time.Hour)},
	}
	nearest, ok := NearestWait(waits)
	if !ok || nearest.WaitID != "wait_2" {
		t.Fatalf("NearestWait = %+v ok=%v, want wait_2 (numeric registration order, not lexicographic)", nearest, ok)
	}
	// Without RegisteredAt the numeric id suffix still decides.
	bare := []GoalWaitSnapshot{
		{WaitID: "wait_10", Label: "tenth", Deadline: now},
		{WaitID: "wait_2", Label: "second", Deadline: now},
	}
	if nearest, ok := NearestWait(bare); !ok || nearest.WaitID != "wait_2" {
		t.Fatalf("NearestWait = %+v ok=%v, want wait_2 (numeric suffix fallback)", nearest, ok)
	}
	// Nonconforming ids fall back to string compare, deterministically.
	odd := []GoalWaitSnapshot{
		{WaitID: "deadline", Label: "synthetic", Deadline: now},
		{WaitID: "wait_2", Label: "second", Deadline: now},
	}
	first, ok := NearestWait(odd)
	if !ok {
		t.Fatal("NearestWait(mixed ids) must still resolve")
	}
	second, ok := NearestWait([]GoalWaitSnapshot{odd[1], odd[0]})
	if !ok || second.WaitID != first.WaitID {
		t.Fatalf("NearestWait order-dependent: %q vs %q (want input-order independent)", second.WaitID, first.WaitID)
	}
}

// TestFixWave16_ParseWaitSeqOverflow pins the round-16 LOW: a wait_N suffix
// too large to represent must return -1 (falls back to string compare in
// waitOrderLess), never a wrapped-around value. Before the fix the digit
// accumulation wrapped on huge suffixes.
func TestFixWave16_ParseWaitSeqOverflow(t *testing.T) {
	t.Parallel()
	if got := parseWaitSeq("wait_99999999999999999999999"); got != -1 {
		t.Fatalf("parseWaitSeq(huge suffix) = %d, want -1 (non-representable)", got)
	}
	if got := parseWaitSeq("wait_12"); got != 12 {
		t.Fatalf("parseWaitSeq(wait_12) = %d, want 12", got)
	}
}

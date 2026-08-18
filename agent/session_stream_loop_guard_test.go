package agent

import "testing"

// These tests drive the pure loop-guard detector directly (kata d74b): no
// Session, no stream plumbing. The stream-loop integration (forcing a legal
// stop mid-consumeModelStream) is covered separately in
// session_stream_loop_guard_integration_test.go, matching the five documented
// runaway shapes.

// TestStreamLoopGuard_CycleDetection_TripsAtFifteenCalls pins the kata's
// primary fixture: (manage_worktree, task_list, communicate) repeating, a
// cycle of length k=3 that must trip once it has repeated 5 times (15 calls
// in), not merely be present somewhere in a long response.
func TestStreamLoopGuard_CycleDetection_TripsAtFifteenCalls(t *testing.T) {
	g := newStreamLoopGuard()
	calls := []struct{ name, args string }{
		{"manage_worktree", `{"op":"list"}`},
		{"task_list", `{}`},
		{"communicate", `{"message":"still working"}`},
	}
	var trip *loopTrip
	n := 0
	for trip == nil {
		c := calls[n%3]
		trip = g.observeToolCall(c.name, c.args)
		n++
		if n > 30 {
			t.Fatalf("cycle never tripped after %d calls", n)
		}
	}
	if n != 15 {
		t.Fatalf("cycle tripped after %d calls, want exactly 15 (k=3, R=5)", n)
	}
	if trip.Kind != loopTripCycle {
		t.Fatalf("trip.Kind = %v, want loopTripCycle", trip.Kind)
	}
}

// TestStreamLoopGuard_CycleDetection_DoesNotTripBeforeFiveRepeats checks the
// boundary directly: four repeats of a 3-call cycle (12 calls) must not trip,
// only the fifth repeat (the 15th call) does.
func TestStreamLoopGuard_CycleDetection_DoesNotTripBeforeFiveRepeats(t *testing.T) {
	g := newStreamLoopGuard()
	calls := []struct{ name, args string }{
		{"manage_worktree", `{"op":"list"}`},
		{"task_list", `{}`},
		{"communicate", `{"message":"still working"}`},
	}
	for i := range 12 {
		c := calls[i%3]
		if trip := g.observeToolCall(c.name, c.args); trip != nil {
			t.Fatalf("call %d: tripped early (%+v), want no trip before 15 calls", i+1, trip)
		}
	}
}

// TestStreamLoopGuard_CycleDetection_SingleCallRepeatedFiveTimes covers k=1:
// the same call five times in a row trips as a cycle of length 1, the cheap
// fast path the corrected writeup calls for alongside the general k=1..5 scan.
func TestStreamLoopGuard_CycleDetection_SingleCallRepeatedFiveTimes(t *testing.T) {
	g := newStreamLoopGuard()
	var trip *loopTrip
	for range 5 {
		trip = g.observeToolCall("read_file", `{"path":"/x"}`)
	}
	if trip == nil {
		t.Fatal("expected a trip after 5 identical calls")
	}
	if trip.Kind != loopTripCycle {
		t.Fatalf("trip.Kind = %v, want loopTripCycle", trip.Kind)
	}
}

// TestStreamLoopGuard_EighteenDistinctCalls_NoTrip pins odysseus #3185's
// false positive (kata comment): a legitimate round of ~18 DISTINCT tool
// calls must never trip, neither the cycle detector (no repetition at all)
// nor the raw ceiling (well under it).
func TestStreamLoopGuard_EighteenDistinctCalls_NoTrip(t *testing.T) {
	g := newStreamLoopGuard()
	for i := range 18 {
		name := distinctToolName(i)
		if trip := g.observeToolCall(name, `{"n":`+itoaTest(i)+`}`); trip != nil {
			t.Fatalf("call %d (%s): unexpected trip %+v", i+1, name, trip)
		}
	}
}

// TestStreamLoopGuard_RawCeiling_TripsWithoutAnyCycle reconstructs the
// kata's second measured shape -- 83 calls, 48 distinct signatures, max
// repeat 12, deliberately built with NO period-1..5 pattern repeated 5
// times consecutively anywhere -- and pins which detector catches it: the
// ceiling, not the cycle detector (kata: "record honestly which detector
// catches it; likely the ceiling").
func TestStreamLoopGuard_RawCeiling_TripsWithoutAnyCycle(t *testing.T) {
	sigs := buildEightyThreeCallNoCycleFixture(t)

	// Self-check the fixture generator's own claim before trusting the
	// detector's answer: assert directly (via the guard's own cycle
	// checker) that no k=1..5/R=5 cycle exists anywhere in the full 83-call
	// sequence, offline, before feeding it through observeToolCall.
	assertNoShortCycleAnywhere(t, sigs)

	g := newStreamLoopGuard()
	var trip *loopTrip
	n := 0
	for i, sig := range sigs {
		trip = g.observeToolCall(sig.name, sig.args)
		n = i + 1
		if trip != nil {
			break
		}
	}
	if trip == nil {
		t.Fatalf("expected a trip within %d calls (ceiling=%d), got none", len(sigs), loopGuardRawCeiling)
	}
	if trip.Kind != loopTripCeiling {
		t.Fatalf("trip.Kind = %v, want loopTripCeiling (the fixture has no short cycle by construction)", trip.Kind)
	}
	if n != loopGuardRawCeiling {
		t.Fatalf("ceiling tripped at call %d, want exactly %d", n, loopGuardRawCeiling)
	}
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func distinctToolName(i int) string {
	names := []string{
		"read_file", "write_file", "search_files", "manage_worktree", "task_list",
		"communicate", "run_shell", "grep", "list_dir", "web_fetch",
		"create_job", "watch", "ask_user", "compact", "delegate",
		"note", "job_status", "cancel_job",
	}
	return names[i%len(names)]
}

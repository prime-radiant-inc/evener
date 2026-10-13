package agent

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

// stableSupervisionReportDeadline bounds how long the timeout report may take
// while a source lock is held. The report is best-effort and never waits, so two
// seconds is generous; the point is to fail a report that blocks instead of
// letting the test hang inside it.
const stableSupervisionReportDeadline = 2 * time.Second

// stableSupervisionRefusalDeadline bounds how long the quiescence predicate may
// take while a source lock is held. After the fix the predicate is a
// non-blocking read, so it returns at once; the deadline fails a predicate that
// blocks on a runtime lock instead of letting the test hang inside it, the same
// discipline stableSupervisionReportDeadline applies to the timeout report.
const stableSupervisionRefusalDeadline = 2 * time.Second

// stableSupervisionRefusalHeldWindow is how long the quiescence-wait test holds
// a source lock while polling the predicate. It only needs to outlast a few
// poll intervals; the assertion is that every read refuses, not how long it ran.
const stableSupervisionRefusalHeldWindow = 100 * time.Millisecond

// stableSupervisionReportFixture warms one stable delegate so the timeout report
// has a child session and controller to render.
func stableSupervisionReportFixture(t *testing.T) (*Session, *subagent) {
	t.Helper()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm result") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	return root, sub
}

// stableSupervisionStateWithin runs the timeout report on its own goroutine and
// fails if it does not return before the deadline, which is exactly what a
// report that waits on a runtime lock does.
func stableSupervisionStateWithin(t *testing.T, root *Session, sub *subagent) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- stableSupervisionState(root, sub) }()
	select {
	case got := <-done:
		return got
	case <-time.After(stableSupervisionReportDeadline):
		t.Fatalf("stableSupervisionState blocked for %s while a source lock was held", stableSupervisionReportDeadline)
		return ""
	}
}

// uncontendedStableSupervisionState retries the report until no source is busy,
// so a caller that pins the whole uncontended output reads it with every field
// present. Paths outside the run, such as the child's session namer, can hold a
// source lock briefly right after it quiesces; the report never waits for them.
func uncontendedStableSupervisionState(t *testing.T, root *Session, sub *subagent) string {
	t.Helper()
	deadline := time.Now().Add(supervisionSnapshotUncontendedTripwire)
	for {
		state := stableSupervisionState(root, sub)
		if !strings.Contains(state, "_lock_busy") {
			return state
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("report sources still busy after %s: %q", supervisionSnapshotUncontendedTripwire, state)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestStableSupervisionStatePrintsWhileControllerLockHeld pins the timeout
// report's reason to exist. The report runs after the quiescence bound has
// already fired, so it must print the state it can read instead of waiting on a
// runtime lock the stalled production code holds. Before the fix the report
// blocks on c.mu.Lock; now it returns at once, marks the controller busy, and
// still prints every other source.
func TestStableSupervisionStatePrintsWhileControllerLockHeld(t *testing.T) {
	t.Parallel()
	root, sub := stableSupervisionReportFixture(t)
	root.delegateController.mu.Lock()
	defer root.delegateController.mu.Unlock()
	state := stableSupervisionStateWithin(t, root, sub)
	if !strings.Contains(state, "controller_lock_busy") {
		t.Fatalf("report with the controller lock held = %q, want a controller_lock_busy marker", state)
	}
	for _, want := range []string{"rootRetry{active=", "sub{running=", "root{state=", "child{state="} {
		if !strings.Contains(state, want) {
			t.Errorf("report with the controller lock held omitted %q: %q", want, state)
		}
	}
}

// TestStableSupervisionStateMarksEveryBusySource pins that the report stays
// non-blocking for every source it reads: holding any single source lock still
// returns the report, with that source's marker in place of its fields. It also
// holds the subagent registry lock, which main reached through the fatal-run
// gate's getSub and the report no longer touches.
func TestStableSupervisionStateMarksEveryBusySource(t *testing.T) {
	t.Parallel()
	root, sub := stableSupervisionReportFixture(t)
	cases := []struct {
		name   string
		marker string
		hold   func() func()
	}{
		{"controller", "controller_lock_busy", func() func() { root.delegateController.mu.Lock(); return root.delegateController.mu.Unlock }},
		{"root attention", "attention_lock_busy", func() func() { root.attentionMu.Lock(); return root.attentionMu.Unlock }},
		{"subagent", "subagent_lock_busy", func() func() { sub.mu.Lock(); return sub.mu.Unlock }},
		{"root", "root_lock_busy", func() func() { root.mu.Lock(); return root.mu.Unlock }},
		{"child session", "session_lock_busy", func() func() { sub.sess.mu.Lock(); return sub.sess.mu.Unlock }},
		{"drain state", "drain_lock_busy", func() func() { root.drainAbandonedMu.Lock(); return root.drainAbandonedMu.Unlock }},
		{"send start", "send_start_lock_busy", func() func() { root.childCommittedSendMu.Lock(); return root.childCommittedSendMu.Unlock }},
		// The main path reached this lock through childFatalRunGated -> getSub. The
		// report reads the passed sub directly, so holding the registry lock must
		// neither block the report nor add a marker.
		{"subagent registry", "", func() func() { root.subagents.mu.Lock(); return root.subagents.mu.Unlock }},
	}
	for _, tc := range cases {
		func() {
			release := tc.hold()
			defer release()
			state := stableSupervisionStateWithin(t, root, sub)
			if tc.marker != "" && !strings.Contains(state, tc.marker) {
				t.Errorf("report with the %s lock held = %q, want marker %q", tc.name, state, tc.marker)
			}
		}()
	}
}

// TestStableSupervisionStatePrintsTheFullStateWhenIdle pins the whole
// uncontended report as a golden. Making the report non-blocking must drop no
// field: with no source busy it renders every token the timeout report printed
// before, in the same order.
func TestStableSupervisionStatePrintsTheFullStateWhenIdle(t *testing.T) {
	t.Parallel()
	root, sub := stableSupervisionReportFixture(t)
	const want = "aggregate{gen=1 phase=idle open=false needs=false} " +
		"live{binding=false finalizing=false recovery=false} " +
		"reservations=0 eligible=true delivery=false " +
		"rootRetry{active=false delay=0s} " +
		"sub{running=false driving=false finalizing=false dispose=false closed=false fatal=false status=completed} " +
		"root{state=idle} child{state=idle} " +
		"childGates{stop=false fatalRun=false drainAbandoned=false drainGrace=false committedSend=false}"
	if got := uncontendedStableSupervisionState(t, root, sub); got != want {
		t.Fatalf("idle report = %q, want %q", got, want)
	}
}

// stableSupervisionRefusalWithin runs the quiescence predicate on its own
// goroutine and fails if it does not return before the deadline, which is
// exactly what a predicate that waits on a held runtime lock does. A hang is
// not a t.Fatalf on the predicate itself, so running it off the test goroutine
// is how a blocking read is made a testable failure.
func stableSupervisionRefusalWithin(t *testing.T, sub *subagent) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- stableSupervisionRefusal(sub) }()
	select {
	case got := <-done:
		return got
	case <-time.After(stableSupervisionRefusalDeadline):
		t.Fatalf("stableSupervisionRefusal blocked for %s while a source lock was held", stableSupervisionRefusalDeadline)
		return ""
	}
}

// TestStableSupervisionRefusalNeverBlocksWhileASourceLockIsHeld pins the
// predicate's reason to be non-blocking. waitForCondition calls it inline under
// a 30s bound, so a predicate that waits on a runtime lock production holds
// keeps the bound from ever firing and the timeout report from #4513 from ever
// running. Holding any single source lock must still return the predicate, and
// the return must not read quiesced: a busy source is not quiesced, so a wait
// that read a busy source as quiesced would pass early and hide the very stall
// it waits for. It asserts that behaviour, not the refusal wording (the busy
// refusal is log copy; see docs/developing-evener/testing.md).
func TestStableSupervisionRefusalNeverBlocksWhileASourceLockIsHeld(t *testing.T) {
	t.Parallel()
	root, sub := stableSupervisionReportFixture(t)
	cases := []struct {
		name string
		hold func() func()
	}{
		{"subagent", func() func() { sub.mu.Lock(); return sub.mu.Unlock }},
		{"child session", func() func() { sub.sess.mu.Lock(); return sub.sess.mu.Unlock }},
		{"child attention", func() func() { sub.sess.attentionMu.Lock(); return sub.sess.attentionMu.Unlock }},
		{"controller", func() func() { root.delegateController.mu.Lock(); return root.delegateController.mu.Unlock }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := tc.hold()
			defer release()
			if got := stableSupervisionRefusalWithin(t, sub); got == "" {
				t.Fatalf("predicate with the %s lock held read quiesced; a busy source is not quiesced", tc.name)
			}
		})
	}
}

// TestStableSupervisionRefusalReturnsQuiescentWhenEverySourceIsFree pins the
// other half of the mapping: with no source busy the predicate still reaches
// quiesced promptly, so marking a busy source "not yet quiesced" does not keep
// a genuinely quiet run waiting. The fixture's quiescence wait does not join
// every background consumer (the root wake consumer, a namer or watchdog tail),
// so a source can be held for a tick right after it returns and the predicate
// then correctly refuses; the test retries so it pins "free reads quiesced"
// rather than "no consumer ran during one read".
func TestStableSupervisionRefusalReturnsQuiescentWhenEverySourceIsFree(t *testing.T) {
	t.Parallel()
	_, sub := stableSupervisionReportFixture(t)
	// TRIPWIRE: the fixture is quiesced, so a free source reads quiesced at
	// once; this only bounds a regression that never reads quiesced.
	deadline := time.Now().Add(stableSupervisionRefusalDeadline)
	for {
		if got := stableSupervisionRefusalWithin(t, sub); got == "" {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatal("predicate never read quiesced with every source free")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestStableSupervisionQuiescenceWaitKeepsWaitingWhileASourceLockIsHeld pins
// the mapping the predicate must keep rather than only that it returns: a busy
// source keeps the wait waiting, never deciding either way. It polls the
// predicate across a window with sub.mu held and requires every read to refuse;
// releasing the lock lets the same poll read quiesced, so it is the held lock
// (not an over-refusing predicate) that kept it waiting.
func TestStableSupervisionQuiescenceWaitKeepsWaitingWhileASourceLockIsHeld(t *testing.T) {
	t.Parallel()
	_, sub := stableSupervisionReportFixture(t)
	// The lock is released by defer, inside this scope, so a failing assertion
	// in the held window (t.Fatal runs the goroutine's defers) still unlocks
	// sub.mu instead of leaving the predicate goroutine blocked on it.
	func() {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		held := time.Now().Add(stableSupervisionRefusalHeldWindow)
		for time.Now().Before(held) {
			if got := stableSupervisionRefusalWithin(t, sub); got == "" {
				t.Fatal("the wait read quiesced while sub.mu was held")
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	// TRIPWIRE: the fixture is quiesced and every source is free, so the same
	// predicate reads quiesced at once; this only bounds a regression.
	deadline := time.Now().Add(stableSupervisionRefusalDeadline)
	for {
		if got := stableSupervisionRefusalWithin(t, sub); got == "" {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the wait never read quiesced after sub.mu was released")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// stableSupervisionEntryRefusalWithin runs the wait's entry precondition on its
// own goroutine and fails if it does not return before the deadline, which is
// exactly what a blocking sub.mu read does. A hang is not a t.Fatalf on the
// helper itself, so running it off the test goroutine is how a blocking read is
// made a testable failure.
func stableSupervisionEntryRefusalWithin(t *testing.T, sub *subagent, childID string) string {
	t.Helper()
	done := make(chan string, 1)
	go func() { done <- stableSupervisionEntryRefusal(sub, childID) }()
	select {
	case got := <-done:
		return got
	case <-time.After(stableSupervisionRefusalDeadline):
		t.Fatalf("stableSupervisionEntryRefusal blocked for %s while sub.mu was held", stableSupervisionRefusalDeadline)
		return ""
	}
}

// TestStableSupervisionEntryNeverBlocksWhileChildLockIsHeld pins the wait's
// entry precondition to be non-blocking and to keep waiting, not decide, while
// a source is held. waitForStableSupervisionRun reads the child's completion
// channel before it enters its bounded poll; a blocking read there would wait
// on a stall that holds sub.mu, keeping the wait from ever reaching its poll —
// and so from ever firing the 30s bound and the #4513 timeout report that runs
// only after it, the same class #4548 removed from the poll predicate.
//
// The child never ran, so its channel is genuinely nil and the entry reads a
// refusal with the lock free. Holding sub.mu across a controlled window must
// still return the read (a blocking read fails the 2s deadline instead), and
// every held read must be empty: a busy lock observes neither presence nor
// absence, so it defers to the poll rather than deciding. Releasing the lock
// restores the true state the busy hold suppressed, so the empties are the busy
// lock's deferral and not a helper that answers "" for everything.
func TestStableSupervisionEntryNeverBlocksWhileChildLockIsHeld(t *testing.T) {
	t.Parallel()
	sub := &subagent{}
	const childID = "child-never-ran"
	if got := stableSupervisionEntryRefusalWithin(t, sub, childID); got == "" {
		t.Fatal("entry read a missing channel as present with sub.mu free")
	}
	func() {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		held := time.Now().Add(stableSupervisionRefusalHeldWindow)
		for time.Now().Before(held) {
			if got := stableSupervisionEntryRefusalWithin(t, sub, childID); got != "" {
				t.Fatalf("entry decided %q while sub.mu was held; a busy lock must keep the wait waiting", got)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	if got := stableSupervisionEntryRefusalWithin(t, sub, childID); got == "" {
		t.Fatal("entry did not read the missing channel after sub.mu was released")
	}
}

// TestStableSupervisionEntryRefusalReadsTheChannelState pins the present
// branch and the busy mapping together. A healthy child's channel is present,
// so the wait proceeds; behind a busy lock the same present channel still
// decides nothing, so a busy lock cannot be turned into a missing channel (the
// missing-channel and deferral halves are the other test). It asserts the
// decision (empty vs a refusal), not the refusal wording, which is test-helper
// copy (docs/developing-evener/testing.md, Copy Is Not a Test Oracle).
func TestStableSupervisionEntryRefusalReadsTheChannelState(t *testing.T) {
	t.Parallel()
	_, sub := stableSupervisionReportFixture(t)
	if got := stableSupervisionEntryRefusalWithin(t, sub, sub.id); got != "" {
		t.Fatalf("entry refusal for a healthy child = %q, want empty", got)
	}
	present := &subagent{done: make(chan struct{})}
	present.mu.Lock()
	defer present.mu.Unlock()
	if got := stableSupervisionEntryRefusalWithin(t, present, "child-busy-present"); got != "" {
		t.Fatalf("entry refusal for a present channel behind a busy lock = %q, want empty", got)
	}
}

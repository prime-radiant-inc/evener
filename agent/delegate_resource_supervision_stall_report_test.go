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

package hub

import (
	"slices"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestReapCrashedSessionJobsVisitsEachCrashedSessionOnce pins the hub's startup
// sweep: a daemon that died while the hub was down is reported crashed and is
// reaped once, while a live session and an entry naming no state are left alone.
// The sweep asks for no still-live guard: a crashed entry is by definition
// nobody's live daemon.
func TestReapCrashedSessionJobsVisitsEachCrashedSessionOnce(t *testing.T) {
	t.Parallel()
	live := []hubcore.LiveEntry{
		{PID: 1, StateDir: "/state", SessionID: "sess_running"},
		{PID: 2, StateDir: "/state", SessionID: "sess_crashed", Crashed: true},
		{PID: 4, StateDir: "/other", SessionID: "sess_crashed_other", Crashed: true},
		{PID: 5, SessionID: "sess_without_state", Crashed: true},
	}
	var got [][2]string
	reaped := reapCrashedSessionJobs(live, nil, func(stateDir, sessionID string, skip func(string) bool) int {
		if skip != nil {
			t.Errorf("sweep for %s carried a still-live predicate, want nil", sessionID)
		}
		got = append(got, [2]string{stateDir, sessionID})
		return 1
	})
	want := [][2]string{{"/state", "sess_crashed"}, {"/other", "sess_crashed_other"}}
	if reaped != len(want) || !slices.Equal(got, want) {
		t.Fatalf("reaped %d groups from %v, want %d from %v", reaped, got, len(want), want)
	}
}

// TestReapDepartedSessionReapsOnlySessionsStillGone pins the daemon-exit path:
// the roster's announcement for a departed session reaches the reap with the
// roster's still-live predicate, which the reap applies to the session and to
// its delegate subtree, and an announcement naming no session reaps nothing.
func TestReapDepartedSessionReapsOnlySessionsStillGone(t *testing.T) {
	t.Parallel()
	var got [][2]string
	reap := func(stateDir, sessionID string, skip func(string) bool) int {
		got = append(got, [2]string{stateDir, sessionID})
		if skip == nil {
			t.Fatalf("reap for %s carried no still-live predicate", sessionID)
		}
		if !skip("sess_taken_back") || skip("sess_gone") {
			t.Errorf("reap for %s carried a predicate that does not answer for the roster", sessionID)
		}
		return 1
	}
	takenBack := func(sessionID string) bool { return sessionID == "sess_taken_back" }
	gone := hubcore.LiveEntry{StateDir: "/state", SessionID: "sess_gone"}
	reapDepartedSession(takenBack, reap, gone)
	reapDepartedSession(takenBack, reap, hubcore.LiveEntry{StateDir: "/state"})
	want := [][2]string{{"/state", "sess_gone"}}
	if !slices.Equal(got, want) {
		t.Fatalf("reaped %v, want %v", got, want)
	}
	// A hub wired without the seam reaps nothing and must not panic.
	reapDepartedSession(nil, nil, gone)
}

package hub

import (
	"slices"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestReapCrashedSessionJobsVisitsEachCrashedSessionOnce pins the hub's startup
// sweep: a daemon that died while the hub was down is reported crashed and is
// reaped once, while a live session and an entry naming no state are left alone.
func TestReapCrashedSessionJobsVisitsEachCrashedSessionOnce(t *testing.T) {
	t.Parallel()
	live := []hubcore.LiveEntry{
		{PID: 1, StateDir: "/state", SessionID: "sess_running"},
		{PID: 2, StateDir: "/state", SessionID: "sess_crashed", Crashed: true},
		{PID: 3, StateDir: "/state", SessionID: "sess_crashed", Crashed: true},
		{PID: 4, StateDir: "/other", SessionID: "sess_crashed_other", Crashed: true},
		{PID: 5, SessionID: "sess_without_state", Crashed: true},
	}
	var got [][2]string
	reaped := reapCrashedSessionJobs(live, func(stateDir, sessionID string) int {
		got = append(got, [2]string{stateDir, sessionID})
		return 1
	})
	want := [][2]string{{"/state", "sess_crashed"}, {"/other", "sess_crashed_other"}}
	if reaped != len(want) || !slices.Equal(got, want) {
		t.Fatalf("reaped %d groups from %v, want %d from %v", reaped, got, len(want), want)
	}
}

// TestReapDepartedSessionReapsOnlySessionsStillGone pins the daemon-exit path:
// the roster's announcement for a departed session reaps that session, an
// announcement naming no session reaps nothing, and a session a new daemon has
// taken back is left to that daemon's own restore.
func TestReapDepartedSessionReapsOnlySessionsStillGone(t *testing.T) {
	t.Parallel()
	var got [][2]string
	reap := func(stateDir, sessionID string) int {
		got = append(got, [2]string{stateDir, sessionID})
		return 1
	}
	gone := hubcore.LiveEntry{StateDir: "/state", SessionID: "sess_gone"}
	notLive := func(string) bool { return false }
	reapDepartedSession(notLive, reap, gone)
	reapDepartedSession(notLive, reap, hubcore.LiveEntry{StateDir: "/state"})
	reapDepartedSession(func(string) bool { return true }, reap, gone)
	want := [][2]string{{"/state", "sess_gone"}}
	if !slices.Equal(got, want) {
		t.Fatalf("reaped %v, want %v", got, want)
	}
	// A hub wired without the seam reaps nothing and must not panic.
	reapDepartedSession(nil, nil, gone)
}

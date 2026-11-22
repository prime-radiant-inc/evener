package hub

import "primeradiant.com/evener/cmd/evener-hub/internal/hubcore"

// reapDepartedSession reaps the background job process groups a session's
// daemon left running when it exited without teardown (SIGKILL, crash, OOM).
// A departing daemon leaves those groups adopted by init, and the roster is the
// process that sees it go (#4843). The reap covers the session's delegate
// subtree too: a nested delegate's job record never reaches the root's journal.
//
// stillLive guards a session that came back under a new daemon between the
// announcement and this reap: that daemon's own restore reaps the old groups,
// and signaling here could end one of its live jobs. It never fails — it is
// cleanup, and the reap itself only signals a group whose start time still
// matches the durable job record. Nil seams are no-ops so the announcement path
// needs no branches.
func reapDepartedSession(stillLive func(sessionID string) bool, reap func(stateDir, sessionID string, skip func(sessionID string) bool) int, gone hubcore.LiveEntry) {
	if reap == nil || gone.SessionID == "" || gone.StateDir == "" {
		return
	}
	reap(gone.StateDir, gone.SessionID, stillLive)
}

// sessionStillLive reports whether the roster holds a daemon that is not
// crashed for the session.
func sessionStillLive(roster *hubcore.Roster) func(sessionID string) bool {
	return func(sessionID string) bool {
		live, listed := roster.Find(sessionID)
		return listed && !live.Crashed
	}
}

// reapCrashedSessionJobs reaps the background job groups of every session the
// roster holds as crashed. It covers the daemons that died while the hub was
// down: no session-gone announcement fires for them, because the first roster
// publication after a hub start has no earlier listing to leave. List already
// holds one entry per session, so no session is reaped twice. It reports how
// many groups were signaled.
func reapCrashedSessionJobs(live []hubcore.LiveEntry, stillLive func(sessionID string) bool, reap func(stateDir, sessionID string, skip func(sessionID string) bool) int) int {
	if reap == nil {
		return 0
	}
	reaped := 0
	for _, entry := range live {
		if !entry.Crashed || entry.SessionID == "" || entry.StateDir == "" {
			continue
		}
		reaped += reap(entry.StateDir, entry.SessionID, stillLive)
	}
	return reaped
}

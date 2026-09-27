package main

import "testing"

// Serve installs the root session's subagent tally when it bridges the session,
// and again for the replacement a thread/clear installs (S3): like the live
// watches beside it, the tally has to follow the current session.
func TestServeInstallsTheRootsSubagentTallyAndFollowsClear(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	var installs int
	var answered bool
	obs := runClearAttempt(t, deps, state, args, func(*clearObservation) {
		installs = state.srv.subagentTallyInstalls
		if state.srv.subagentTally != nil {
			_, answered = state.srv.subagentTally()
		}
	})
	if obs.clearErr != nil {
		t.Fatalf("thread/clear: %v", obs.clearErr)
	}
	if installs != 2 {
		t.Fatalf("tally installs = %d, want one at startup and one for the clear's replacement", installs)
	}
	if !answered {
		t.Fatal("the installed tally does not answer for the current root session")
	}
}

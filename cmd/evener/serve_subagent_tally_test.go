package main

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// Serve installs the root session's subagent tally when it bridges the session,
// and again for the replacement a thread/clear installs (S3): like the live
// watches beside it, the tally has to follow the current session.
//
// A fresh session with no subagents always answers with an empty tally, so
// the OLD session and the replacement would look identical if left alone -
// this test could pass even if the seam stayed pointed at the old session.
// SubagentTallyForTest gives the old session a tally the replacement could
// never share, so the assertion below can tell whose seam is actually
// installed.
func TestServeInstallsTheRootsSubagentTallyAndFollowsClear(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	var installs int
	var answered bool
	var currentTally appwire.SubagentTally
	obs := runClearAttempt(t, deps, state, args, func(*clearObservation) {
		if oldSess := state.session(0); oldSess != nil {
			oldSess.SubagentTallyForTest()
		}
		installs = state.srv.subagentTallyInstalls
		if state.srv.subagentTally != nil {
			currentTally, answered = state.srv.subagentTally()
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
	if currentTally != (appwire.SubagentTally{}) {
		t.Fatalf("installed tally = %+v, want the replacement's own empty tally - the old session's seam is still installed", currentTally)
	}
}

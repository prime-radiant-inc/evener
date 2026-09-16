package hub

import (
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// TestSignaledForceStopKeepsFenceAcrossRestart pins the discriminator the
// medium fix depends on: a force stop that may have delivered a termination
// signal must stay fenced across a restart even when no alias is claimed,
// because a failed termination is not the graceful exit that removes a
// rendezvous marker. Only an unsignaled interruption may be settled.
func TestSignaledForceStopKeepsFenceAcrossRestart(t *testing.T) {
	root, sessionID := t.TempDir(), hubtest.SessionID(t)
	locks, err := hubcore.NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	aliases := []string{sessionID}
	finish := locks.BeginForceStop(aliases)
	if err := locks.PersistForceStop(aliases, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := locks.MarkForceStopSignaled(aliases, sessionID); err != nil {
		t.Fatal(err)
	}
	finish(true)

	runDir := t.TempDir()
	web := NewWebServer(hubcore.WebConfig{HubStateRoot: root, RunDir: runDir, Roster: hubcore.NewRoster(runDir, nil)})
	restarted := web.cfg.ResumeLocks
	if restarted == nil {
		t.Fatal("restarted hub has no recovery registry")
	}
	state := restarted.RecoveryState(sessionID)
	if !state.ResumeRequired || state.ExitConfirmed || !state.SignalAttempted {
		t.Fatalf("signaled force-stop recovery = %+v, want the fence retained", state)
	}
	next, err := restarted.RegisterResume(t.Context(), sessionID, aliases, map[string]uint64{sessionID: restarted.RecoveryState(sessionID).Epoch})
	if err != nil {
		t.Fatal(err)
	}
	if err := next.BeforeLaunch(); err == nil {
		t.Fatal("recovery cleared the fence of a force stop that may have signaled")
	}
	next.Complete(nil)
}

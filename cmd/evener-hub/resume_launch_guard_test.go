package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

// TestUnregisteredResumeLaunchInvalidatesPriorExitProof is the retirement-path
// regression: resumeThreadLocked launches without a registered ActiveResume, so
// it must perform BeforeLaunch's durable invalidation through a LaunchGuard
// instead of skipping it. A launch that starts while the previous owner's exit
// proof still stands lets that proof describe the new child; a launch that
// fails must put the proof back so the session is not left fenced.
//
// The retirement path's own admission fence (retirementAdmissionRecoveryError)
// refuses a recovering session before any launch, so the observable defect is
// in the launch helper the retirement path calls; this drives that helper with
// the same resolved target and ownership aliases.
func TestUnregisteredResumeLaunchInvalidatesPriorExitProof(t *testing.T) {
	root, runDir := t.TempDir(), t.TempDir()
	locks, err := hubcore.NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := hubtest.SessionID(t)
	aliases := []string{sessionID}
	finish := locks.BeginForceStop(aliases)
	if err := locks.PersistForceStop(aliases, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := locks.ConfirmForceStop(sessionID); err != nil {
		t.Fatal(err)
	}
	finish(true)
	if !locks.RecoveryState(sessionID).ExitConfirmed {
		t.Fatal("fixture did not establish a confirmed exit proof")
	}

	launched := false
	cfg := hubcore.WebConfig{
		RunDir:      runDir,
		Roster:      hubcore.NewRoster(runDir, nil),
		ResumeLocks: locks,
		Spawner: &fakeRPCSpawner{resume: func(_ context.Context, req hubcore.ResumeRequest) (rendezvous.Entry, error) {
			launched = true
			if req.ActiveResume != nil {
				t.Error("unregistered launch carried an active resume lifetime")
			}
			// The durable record a restart would read must already describe the
			// launch, not the replaced proof.
			reopened, err := hubcore.NewPersistentResumeLocks(root)
			if err != nil {
				t.Errorf("reopen recovery state: %v", err)
				return rendezvous.Entry{}, err
			}
			if state := reopened.RecoveryState(sessionID); state.ExitConfirmed {
				t.Error("launch started while the previous owner's exit proof still described the new child")
			}
			return rendezvous.Entry{}, errors.New("launcher observed")
		}},
	}
	_, err = resumeThreadLocked(t.Context(), cfg, nil, appwire.ThreadResumeParams{Ref: "local:" + sessionID, Session: sessionID}, aliases)
	if !launched {
		t.Fatalf("fixture never reached the launcher: %v", err)
	}
	if err == nil {
		t.Fatal("launcher failure was not reported")
	}
	if !locks.RecoveryState(sessionID).ExitConfirmed {
		t.Fatal("failed unregistered launch did not restore the prior exit proof")
	}
}

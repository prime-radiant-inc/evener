package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// A resume issued from parked state (an automatic retry) names the hub-owned
// recovery epoch it armed against. A Stop from another client or device
// advances that epoch while the retry waits - invisible to the retrying page's
// Stop generations and its IndexedDB epochs - and the retry's fresh connection
// is admitted under the post-Stop epoch, so the connection's own fence cannot
// see it. Only the caller's expected epoch can refuse the restart.
func TestResumeRefusesEpochSupersededByANewerStop(t *testing.T) {
	sessionID := "epoch-superseded"
	locks, err := hubcore.NewPersistentResumeLocks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spawned := 0
	cfg := hubcore.WebConfig{
		ResumeLocks: locks,
		Roster:      liveClaimRoster(t.TempDir(), &fakeProber{}),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			spawned++
			return rendezvous.Entry{}, errors.New("spawn sentinel")
		}},
	}
	armedEpoch := locks.RecoveryState(sessionID).Epoch
	// Another client or device force-stops the session while the retry waits.
	fence := locks.BeginForceStop([]string{sessionID})
	fence.Finish(true)
	if locks.RecoveryState(sessionID).Epoch == armedEpoch {
		t.Fatal("setup did not advance the recovery epoch")
	}
	_, err = hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{
		Session:               sessionID,
		ExpectedRecoveryEpoch: &armedEpoch,
	})
	if err == nil {
		t.Fatal("resume restarted a session a newer Stop superseded")
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("refusal is not a wire error: %v", err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorSessionRecoveryCanceled {
		t.Fatalf("refusal data = %#v, want the superseded recovery discriminant", wire.Data)
	}
	if spawned != 0 {
		t.Fatalf("superseded resume reached the spawner %d times", spawned)
	}
}

// The same resume against the epoch it was actually issued under proceeds: the
// fence refuses supersession, not resumes.
func TestResumeAcceptsTheEpochItWasIssuedAgainst(t *testing.T) {
	sessionID := "epoch-current"
	locks, err := hubcore.NewPersistentResumeLocks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("spawn sentinel")
	spawned := 0
	cfg := hubcore.WebConfig{
		ResumeLocks: locks,
		Roster:      liveClaimRoster(t.TempDir(), &fakeProber{}),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			spawned++
			return rendezvous.Entry{}, sentinel
		}},
	}
	current := locks.RecoveryState(sessionID).Epoch
	_, err = hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{
		Session:               sessionID,
		ExpectedRecoveryEpoch: &current,
	})
	if spawned != 1 {
		t.Fatalf("current-epoch resume did not reach the spawner: spawned=%d err=%v", spawned, err)
	}
}

// Every thread/read carries the current recovery epoch, fenced or not, so a
// client arming a retry can hand it back with its next resume.
func TestThreadReadExposesTheRecoveryEpoch(t *testing.T) {
	sessionID := "epoch-exposed"
	locks, err := hubcore.NewPersistentResumeLocks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fence := locks.BeginForceStop([]string{sessionID})
	fence.Finish(true)
	want := locks.RecoveryState(sessionID).Epoch
	cfg := hubcore.WebConfig{ResumeLocks: locks}
	thread := applyThreadResumeRequirement(context.Background(), cfg, "", sessionID, appwire.Thread{ID: sessionID})
	if thread.Evener.RecoveryEpoch != want {
		t.Fatalf("thread read recovery epoch = %d, want %d", thread.Evener.RecoveryEpoch, want)
	}
}

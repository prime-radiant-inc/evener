package hub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	data, ok := wire.Data.(appwire.RecoverySupersededErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorSessionRecoveryCanceled {
		t.Fatalf("refusal data = %#v, want the superseded recovery discriminant", wire.Data)
	}
	if want := locks.RecoveryState(sessionID).Epoch; data.RecoveryEpoch != want {
		t.Fatalf("refusal named epoch %d, want the current %d", data.RecoveryEpoch, want)
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
	thread := applyThreadResumeRequirement(context.Background(), cfg, "local:"+sessionID, sessionID, appwire.Thread{ID: sessionID})
	if thread.Evener.RecoveryEpoch == nil || *thread.Evener.RecoveryEpoch != want {
		t.Fatalf("thread read recovery epoch = %d, want %d", thread.Evener.RecoveryEpoch, want)
	}
}

// A fresh session's epoch is zero, and zero must reach the wire: a client that
// never records it (a restored hub's sessions start there) would arm retries
// with no expectation at all, so a foreign Stop's zero-to-one advance could
// not fence them on their fresh connection.
func TestRecoveryEpochZeroReachesTheWire(t *testing.T) {
	zeroEpoch := uint64(0)
	raw, err := json.Marshal(appwire.ThreadReadResponse{Thread: appwire.Thread{
		Evener: appwire.EvenerThread{RecoveryEpoch: &zeroEpoch},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"recoveryEpoch":0`) {
		t.Fatalf("zero recovery epoch dropped from the read response: %s", raw)
	}
	zero := uint64(0)
	raw, err = json.Marshal(appwire.ThreadResumeParams{Ref: "local:x", ExpectedRecoveryEpoch: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"expectedRecoveryEpoch":0`) {
		t.Fatalf("zero expected epoch dropped from the resume request: %s", raw)
	}
}

// A remote session's epoch belongs to its own controller: the local stamp must
// not overwrite a remote reply's value with this hub's empty recovery state,
// and a local session's epoch comes from the locks.
func TestThreadReadPreservesRemoteRecoveryEpoch(t *testing.T) {
	locks, err := hubcore.NewPersistentResumeLocks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{ResumeLocks: locks}
	four := uint64(4)
	remote := applyThreadResumeRequirement(context.Background(), cfg, "hub2:th", "th", appwire.Thread{
		ID:     "th",
		Evener: appwire.EvenerThread{RecoveryEpoch: &four},
	})
	if remote.Evener.RecoveryEpoch == nil || *remote.Evener.RecoveryEpoch != 4 {
		t.Fatalf("remote epoch = %v, want the reply's 4 preserved", remote.Evener.RecoveryEpoch)
	}
	local := applyThreadResumeRequirement(context.Background(), cfg, "local:th", "th", appwire.Thread{ID: "th"})
	if want := locks.RecoveryState("th").Epoch; local.Evener.RecoveryEpoch == nil || *local.Evener.RecoveryEpoch != want {
		t.Fatalf("local epoch = %v, want the lock's %d", local.Evener.RecoveryEpoch, want)
	}
}

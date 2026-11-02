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

func epochFixture(t *testing.T, sessionID string) (hubcore.WebConfig, *hubcore.ResumeLocks) {
	t.Helper()
	locks, err := hubcore.NewPersistentResumeLocks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return hubcore.WebConfig{
		ResumeLocks: locks,
		Roster:      liveClaimRoster(t.TempDir(), &fakeProber{}),
	}, locks
}

// A resume issued from parked state (an automatic retry) names the hub-owned
// recovery token it armed against. A Stop from another client or device
// advances the token while the retry waits - invisible to the retrying page's
// Stop generations and its IndexedDB epochs - and the retry's fresh connection
// is admitted under the post-Stop token, so the connection's own fence cannot
// see it. Only the caller's expected token can refuse the restart.
func TestResumeRefusesTokenSupersededByANewerStop(t *testing.T) {
	sessionID := "token-superseded"
	cfg, locks := epochFixture(t, sessionID)
	spawned := 0
	cfg.Spawner = &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		spawned++
		return rendezvous.Entry{}, errors.New("spawn sentinel")
	}}
	armedToken := locks.RecoveryToken(sessionID)
	// Another client or device force-stops the session while the retry waits.
	fence := locks.BeginForceStop([]string{sessionID})
	fence.Finish(true)
	if locks.RecoveryToken(sessionID) == armedToken {
		t.Fatal("setup did not advance the recovery token")
	}
	_, err := hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{
		Session:               sessionID,
		ExpectedRecoveryToken: armedToken,
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
	if want := locks.RecoveryToken(sessionID); data.RecoveryToken != want {
		t.Fatalf("refusal named token %q, want the current %q", data.RecoveryToken, want)
	}
	if spawned != 0 {
		t.Fatalf("superseded resume reached the spawner %d times", spawned)
	}
}

// A token minted before a hub recreation cannot match the restored state: the
// per-session epochs start at zero again, so a Stop landed before the restart
// would otherwise be invisible to a retry armed at the same zero.
func TestResumeTokenSupersededByHubRecreation(t *testing.T) {
	sessionID := "token-recreated"
	stateRoot := t.TempDir()
	locks, err := hubcore.NewPersistentResumeLocks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	armedToken := locks.RecoveryToken(sessionID)
	// Another device Stops the session; the hub is then recreated from disk.
	fence := locks.BeginForceStop([]string{sessionID})
	fence.Finish(true)
	recreated, err := hubcore.NewPersistentResumeLocks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if recreated.RecoveryToken(sessionID) == armedToken {
		t.Fatal("a pre-restart token matched the recreated hub's state")
	}
	cfg := hubcore.WebConfig{
		ResumeLocks: recreated,
		Roster:      liveClaimRoster(t.TempDir(), &fakeProber{}),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			t.Fatal("a retry armed before the hub recreation reached the spawner")
			return rendezvous.Entry{}, nil
		}},
	}
	_, err = hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{
		Session:               sessionID,
		ExpectedRecoveryToken: armedToken,
	})
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("recreated-hub refusal is not a wire error: %v", err)
	}
	if data, ok := wire.Data.(appwire.RecoverySupersededErrorData); !ok || data.EvenerErrorInfo != appwire.ErrorSessionRecoveryCanceled {
		t.Fatalf("refusal data = %#v, want the superseded recovery discriminant", wire.Data)
	}
}

// The same resume against the token it was actually issued under proceeds: the
// fence refuses supersession, not resumes.
func TestResumeAcceptsTheTokenItWasIssuedAgainst(t *testing.T) {
	sessionID := "token-current"
	cfg, locks := epochFixture(t, sessionID)
	spawned := 0
	cfg.Spawner = &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
		spawned++
		return rendezvous.Entry{}, errors.New("spawn sentinel")
	}}
	_, err := hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{
		Session:               sessionID,
		ExpectedRecoveryToken: locks.RecoveryToken(sessionID),
	})
	if spawned != 1 {
		t.Fatalf("current-token resume did not reach the spawner: spawned=%d err=%v", spawned, err)
	}
}

// Every thread/read carries the hub-owned token for a local session - a fresh
// session included, whose epoch is zero but whose token is never empty - so a
// client arming a retry always has an expectation to hand back.
func TestThreadReadExposesTheRecoveryToken(t *testing.T) {
	sessionID := "token-exposed"
	cfg, locks := epochFixture(t, sessionID)
	fence := locks.BeginForceStop([]string{sessionID})
	fence.Finish(true)
	thread := applyThreadResumeRequirement(context.Background(), cfg, "local:"+sessionID, sessionID, appwire.Thread{ID: sessionID})
	if want := locks.RecoveryToken(sessionID); thread.Evener.RecoveryToken != want || want == "" {
		t.Fatalf("thread read recovery token = %q, want %q", thread.Evener.RecoveryToken, want)
	}
	fresh := epochFixture2(t)
	if token := fresh.RecoveryToken(sessionID); token == "" {
		t.Fatal("a fresh session's recovery token is empty")
	}
}

func epochFixture2(t *testing.T) *hubcore.ResumeLocks {
	t.Helper()
	locks, err := hubcore.NewPersistentResumeLocks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return locks
}

// The token round-trips through both wire shapes, so a client that read it can
// hand it back and the hub can compare it verbatim.
func TestRecoveryTokenReachesTheWire(t *testing.T) {
	raw, err := json.Marshal(appwire.ThreadReadResponse{Thread: appwire.Thread{
		Evener: appwire.EvenerThread{RecoveryToken: "inc:0"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"recoveryToken":"inc:0"`) {
		t.Fatalf("recovery token dropped from the read response: %s", raw)
	}
	raw, err = json.Marshal(appwire.ThreadResumeParams{Ref: "local:x", ExpectedRecoveryToken: "inc:0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"expectedRecoveryToken":"inc:0"`) {
		t.Fatalf("expected token dropped from the resume request: %s", raw)
	}
}

// A remote session's token belongs to its own controller: the local stamp must
// not overwrite a remote reply's value with this hub's token, and a local
// session's token comes from the locks.
func TestThreadReadPreservesRemoteRecoveryToken(t *testing.T) {
	cfg, locks := epochFixture(t, "th")
	remote := applyThreadResumeRequirement(context.Background(), cfg, "hub2:th", "th", appwire.Thread{
		ID:     "th",
		Evener: appwire.EvenerThread{RecoveryToken: "hub2:tok"},
	})
	if remote.Evener.RecoveryToken != "hub2:tok" {
		t.Fatalf("remote token = %q, want the reply's preserved", remote.Evener.RecoveryToken)
	}
	local := applyThreadResumeRequirement(context.Background(), cfg, "local:th", "th", appwire.Thread{ID: "th"})
	if want := locks.RecoveryToken("th"); local.Evener.RecoveryToken != want {
		t.Fatalf("local token = %q, want the lock's %q", local.Evener.RecoveryToken, want)
	}
}

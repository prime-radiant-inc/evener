package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// The Medium RoboRev reported against resumeThreadLockedLaunch's ResumeRequired
// && !ExitConfirmed refusal: a hub that died between PersistForceStop and
// ConfirmForceStop left the requirement set with no way out — Resume (the only
// action that clears ResumeRequired) refused, force stop had no entry left to
// verify, and ConfirmForceStop was the only ExitConfirmed writer. These tests
// pin the discovery-based escape: with no live or unverified claim on any
// recovery alias the exit is proven, so Resume confirms it and proceeds; a
// live or unverifiable claim keeps the refusal.

// persistedUnconfirmedRecovery returns resume locks holding the authority a
// force stop persisted before the hub died — ResumeRequired set, exit never
// confirmed — plus the state root the authority is durable in.
func persistedUnconfirmedRecovery(t *testing.T, sessionID string) (*hubcore.ResumeLocks, string) {
	t.Helper()
	stateRoot := t.TempDir()
	locks, err := hubcore.NewPersistentResumeLocks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := locks.PersistForceStopWithOwner([]string{sessionID}, sessionID, daemonprocess.Target{PID: 4242, SessionID: sessionID, StateDir: t.TempDir(), StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	state := locks.RecoveryState(sessionID)
	if !state.ResumeRequired || state.ExitConfirmed {
		t.Fatalf("setup did not stage the persisted-unconfirmed state: %+v", state)
	}
	return locks, stateRoot
}

func TestResumeConfirmsExitWhenRecoveryHasNoClaim(t *testing.T) {
	runDir := t.TempDir()
	roster := liveClaimRoster(runDir, &fakeProber{})
	sessionID := "stuck-owner"
	locks, stateRoot := persistedUnconfirmedRecovery(t, sessionID)
	spawned := false
	sentinel := errors.New("spawn sentinel")
	cfg := hubcore.WebConfig{
		Roster:      roster,
		ResumeLocks: locks,
		DaemonProcesses: forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
			// The persisted owner is verified gone — the Stop route's own
			// proof class — so the escape may confirm and proceed.
			return nil, daemonprocess.ErrExited
		}),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			spawned = true
			return rendezvous.Entry{}, sentinel
		}},
	}
	_, err := hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{Session: sessionID})
	if !spawned {
		t.Fatalf("resume refused a replacement launch though discovery proves the owner exited: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("resume did not reach the spawner: %v", err)
	}
	if strings.Contains(err.Error(), "resume owner exit is unconfirmed") {
		t.Fatalf("claimless recovery still refused: %v", err)
	}
	if !cfg.ResumeLocks.RecoveryState(sessionID).ExitConfirmed {
		t.Fatal("claimless recovery exit was not confirmed")
	}
	restored, err := hubcore.NewPersistentResumeLocks(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.RecoveryState(sessionID).ExitConfirmed {
		t.Fatal("exit confirmation did not reach durable storage")
	}
}

func TestResumeStillRefusesUnconfirmedExitWithLiveClaim(t *testing.T) {
	runDir := t.TempDir()
	sessionID := "claimed-owner"
	// A live but protocol-mismatched owner still claims the session: it cannot
	// be reused, and Resume must not spawn past it while its exit is
	// unconfirmed.
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, SessionID: sessionID, ThreadID: sessionID, Protocol: "evener-appwire-v3", Endpoint: "ws://127.0.0.1:1/rpc"})
	roster := liveClaimRoster(runDir, &fakeProber{sessionID: sessionID, status: "active"})
	spawned := false
	cfg := hubcore.WebConfig{
		Roster:      roster,
		ResumeLocks: func() *hubcore.ResumeLocks { locks, _ := persistedUnconfirmedRecovery(t, sessionID); return locks }(),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			spawned = true
			return rendezvous.Entry{}, errors.New("spawn sentinel")
		}},
	}
	_, err := hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{Session: sessionID})
	if spawned {
		t.Fatal("resume launched a replacement while a live claim held the session")
	}
	if err == nil || !strings.Contains(err.Error(), "resume owner exit is unconfirmed") {
		t.Fatalf("live-claimed recovery did not refuse: %v", err)
	}
	if cfg.ResumeLocks.RecoveryState(sessionID).ExitConfirmed {
		t.Fatal("live claim was treated as proof of exit")
	}
}

func TestResumeStillRefusesUnconfirmedExitWithUnverifiedClaim(t *testing.T) {
	runDir := t.TempDir()
	sessionID := "unverified-owner"
	forkAlias := "unverified-fork"
	// A live process whose daemon identity cannot be established claims the
	// recovery group's OTHER alias: the resumed session's own protocol check
	// passes, so the refusal is the escape site's to keep — a claim whose
	// ownership cannot be verified is not proof of exit.
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1002, SessionID: forkAlias, ThreadID: forkAlias, Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc"})
	roster := liveClaimRoster(runDir, &fakeProber{shouldFail: true})
	roster.Refresh()
	if len(roster.UnconfirmedEntries()) != 1 {
		t.Fatal("setup did not stage an unconfirmed claim")
	}
	locks, _ := persistedUnconfirmedRecovery(t, sessionID)
	if err := locks.PersistForceStop([]string{forkAlias, sessionID}, sessionID); err != nil {
		t.Fatal(err)
	}
	spawned := false
	cfg := hubcore.WebConfig{
		Roster:      roster,
		ResumeLocks: locks,
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			spawned = true
			return rendezvous.Entry{}, errors.New("spawn sentinel")
		}},
	}
	_, err := hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{Session: sessionID})
	if spawned {
		t.Fatal("resume launched a replacement while an unverified process claimed the session group")
	}
	if err == nil || !strings.Contains(err.Error(), "resume owner exit is unconfirmed") {
		t.Fatalf("unverified-claim recovery did not refuse: %v", err)
	}
	if cfg.ResumeLocks.RecoveryState(sessionID).ExitConfirmed {
		t.Fatal("unverified claim was treated as proof of exit")
	}
}

// A markerless owner is not a dead owner: with no rendezvous claim anywhere
// but the process controller still reporting the persisted identity alive
// (the signal-denied / failed-wait class), the refusal must stand.
func TestResumeRefusesClaimlessRecoveryWhileOwnerProcessLives(t *testing.T) {
	runDir := t.TempDir()
	roster := liveClaimRoster(runDir, &fakeProber{})
	sessionID := "markerless-live-owner"
	locks, _ := persistedUnconfirmedRecovery(t, sessionID)
	spawned := false
	cfg := hubcore.WebConfig{
		Roster:      roster,
		ResumeLocks: locks,
		DaemonProcesses: forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
			return &durableRecoveryProcess{}, nil
		}),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			spawned = true
			return rendezvous.Entry{}, errors.New("spawn sentinel")
		}},
	}
	_, err := hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{Session: sessionID})
	if spawned {
		t.Fatal("resume launched a replacement while the persisted owner process still lives")
	}
	if err == nil || !strings.Contains(err.Error(), "resume owner exit is unconfirmed") {
		t.Fatalf("live markerless owner did not refuse: %v", err)
	}
	if cfg.ResumeLocks.RecoveryState(sessionID).ExitConfirmed {
		t.Fatal("a live process was treated as proof of exit")
	}
}

// Authority persisted without an owner identity (a record from before the
// identity was carried, or a direct PersistForceStop) has no exit proof to
// verify: the refusal stands until the Stop route establishes one.
func TestResumeRefusesClaimlessRecoveryWithoutOwnerProof(t *testing.T) {
	runDir := t.TempDir()
	roster := liveClaimRoster(runDir, &fakeProber{})
	sessionID := "proofless-owner"
	locks, err := hubcore.NewPersistentResumeLocks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
		t.Fatal(err)
	}
	spawned := false
	cfg := hubcore.WebConfig{
		Roster:      roster,
		ResumeLocks: locks,
		DaemonProcesses: forceStopControllerFunc(func(target daemonprocess.Target) (daemonprocess.Process, error) {
			return nil, daemonprocess.ErrExited
		}),
		Spawner: &fakeRPCSpawner{resume: func(context.Context, hubcore.ResumeRequest) (rendezvous.Entry, error) {
			spawned = true
			return rendezvous.Entry{}, errors.New("spawn sentinel")
		}},
	}
	_, err = hubThreadResume(context.Background(), cfg, nil, appwire.ThreadResumeParams{Session: sessionID})
	if spawned {
		t.Fatal("resume launched a replacement with no owner exit proof on record")
	}
	if err == nil || !strings.Contains(err.Error(), "resume owner exit is unconfirmed") {
		t.Fatalf("proofless recovery did not refuse: %v", err)
	}
}

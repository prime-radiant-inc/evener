package hub

import (
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

// interruptedLaunchState leaves the durable recovery record in the exact state
// a hub crash produces between BeforeLaunch and the child's rendezvous entry:
// an explicit Stop committed a confirmed exit proof, a resume durably
// invalidated that proof for the replacement, and then the hub died before the
// child started or published a claim. Nothing completes the launch lifetime.
func interruptedLaunchState(t *testing.T) (root, sessionID string) {
	t.Helper()
	root, sessionID = t.TempDir(), hubtest.SessionID(t)
	locks, err := hubcore.NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	finish := locks.BeginForceStop([]string{sessionID})
	if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := locks.ConfirmForceStop(sessionID); err != nil {
		t.Fatal(err)
	}
	finish(true)
	active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
	if err != nil {
		t.Fatal(err)
	}
	if err := active.BeforeLaunch(); err != nil {
		t.Fatalf("BeforeLaunch: %v", err)
	}
	// The crash: no ChildReaped, no LaunchFinished, no Complete, no rendezvous.
	return root, sessionID
}

// TestInterruptedLaunchDoesNotStrandSessionAcrossRestart is the High
// regression: a hub that dies after BeforeLaunch durably clears the previous
// exit proof, but before the child starts or publishes a rendezvous entry,
// must not leave the session rejected by both resume and force-stop forever.
// Startup recovery restores the prior proof when no claim or child exists.
func TestInterruptedLaunchDoesNotStrandSessionAcrossRestart(t *testing.T) {
	root, sessionID := interruptedLaunchState(t)
	runDir := t.TempDir()
	// A restarted hub constructs its recovery registry (and runs startup
	// recovery) exactly as runMain does.
	web := NewWebServer(hubcore.WebConfig{HubStateRoot: root, RunDir: runDir, Roster: hubcore.NewRoster(runDir, nil)})
	restarted := web.cfg.ResumeLocks
	if restarted == nil {
		t.Fatal("restarted hub has no recovery registry")
	}

	// Force-stop must reach its confirmed-stopped no-op instead of demanding a
	// live process claim that cannot exist.
	cfg := hubcore.WebConfig{
		RunDir:      runDir,
		ResumeLocks: restarted,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			t.Error("recovered session reached process control")
			return nil, errors.New("unexpected process control")
		}),
	}
	if err := forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil); err != nil {
		t.Fatalf("restarted hub could not force stop the stranded session: %v", err)
	}

	// Resume must be admitted again: this is the fence BeforeLaunch installs.
	next, err := restarted.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: restarted.RecoveryState(sessionID).Epoch})
	if err != nil {
		t.Fatalf("restarted hub refused to register a resume for the stranded session: %v", err)
	}
	if err := next.BeforeLaunch(); err != nil {
		t.Fatalf("restarted hub refused to launch a replacement for the stranded session: %v", err)
	}
	next.Complete(nil)
}

// unconfirmedForceStopState leaves the durable recovery record in the exact
// state a hub crash produces between PersistForceStop and ConfirmForceStop: an
// explicit Stop committed a new recovery group with no exit proof, and then the
// hub died before the daemon's exit was recorded. The daemon exits gracefully
// and removes its rendezvous claim, so at restart no alias is claimed.
func unconfirmedForceStopState(t *testing.T) (root, sessionID string) {
	t.Helper()
	root, sessionID = t.TempDir(), hubtest.SessionID(t)
	locks, err := hubcore.NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	finish := locks.BeginForceStop([]string{sessionID})
	if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
		t.Fatal(err)
	}
	// The crash: the daemon exits and drops its marker, but the hub dies before
	// ConfirmForceStop records the confirmed exit.
	finish(true)
	return root, sessionID
}

// TestUnconfirmedForceStopDoesNotStrandSessionAcrossRestart is the Medium
// regression: a hub that dies between the durable force-stop intent and its
// confirmation, while the daemon then exits gracefully and drops its marker,
// leaves a session with ResumeRequired && !ExitConfirmed, no launch intent, no
// signal attempt, and no claim. Explicit resume cannot clear it (the exit is
// unconfirmed) and forceStopEntry cannot confirm it (no claim), so startup
// recovery must settle an unsignaled, unclaimed, unconfirmed group.
func TestUnconfirmedForceStopDoesNotStrandSessionAcrossRestart(t *testing.T) {
	root, sessionID := unconfirmedForceStopState(t)
	runDir := t.TempDir()
	web := NewWebServer(hubcore.WebConfig{HubStateRoot: root, RunDir: runDir, Roster: hubcore.NewRoster(runDir, nil)})
	restarted := web.cfg.ResumeLocks
	if restarted == nil {
		t.Fatal("restarted hub has no recovery registry")
	}

	state := restarted.RecoveryState(sessionID)
	if !state.ExitConfirmed || state.LaunchPending {
		t.Fatalf("stranded force-stop recovery = %+v, want the exit proof reconciled", state)
	}
	persisted, err := hubcore.NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	if durable := persisted.RecoveryState(sessionID); !durable.ExitConfirmed || durable.LaunchPending {
		t.Fatalf("recovery was not durable: %+v", durable)
	}

	// The ordinary resume path runs again.
	next, err := restarted.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: restarted.RecoveryState(sessionID).Epoch})
	if err != nil {
		t.Fatal(err)
	}
	if err := next.BeforeLaunch(); err != nil {
		t.Fatalf("restarted hub refused to launch a replacement for the stranded session: %v", err)
	}
	next.Complete(nil)
}

// TestUnconfirmedForceStopKeepsFenceForPublishedClaim guards the safety
// invariant: recovery must never settle an unconfirmed group while any alias is
// claimed, so a live child can never be described by a restored exit proof.
func TestUnconfirmedForceStopKeepsFenceForPublishedClaim(t *testing.T) {
	root, sessionID := unconfirmedForceStopState(t)
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID: 4242, SessionID: sessionID, ThreadID: sessionID, StateDir: t.TempDir(), StartedAt: time.Now(),
	})
	web := NewWebServer(hubcore.WebConfig{HubStateRoot: root, RunDir: runDir, Roster: hubcore.NewRoster(runDir, nil)})
	restarted := web.cfg.ResumeLocks
	if restarted == nil {
		t.Fatal("restarted hub has no recovery registry")
	}
	if state := restarted.RecoveryState(sessionID); state.ExitConfirmed {
		t.Fatalf("recovery confirmed a group with a published claim: %+v", state)
	}
	next, err := restarted.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: restarted.RecoveryState(sessionID).Epoch})
	if err != nil {
		t.Fatal(err)
	}
	if err := next.BeforeLaunch(); err == nil {
		t.Fatal("recovery let a replacement launch over a published claim")
	}
	next.Complete(nil)
}

// TestInterruptedLaunchKeepsFenceForPublishedClaim guards the BeforeLaunch
// invariant: a previous process's exit proof must never describe a new child.
// A hub that died mid-launch must keep the fence whenever any alias is still
// claimed, so the next attempt takes the ordinary verified-process path.
func TestInterruptedLaunchKeepsFenceForPublishedClaim(t *testing.T) {
	root, sessionID := interruptedLaunchState(t)
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       4242,
		SessionID: sessionID,
		ThreadID:  sessionID,
		StateDir:  t.TempDir(),
		StartedAt: time.Now(),
	})
	web := NewWebServer(hubcore.WebConfig{HubStateRoot: root, RunDir: runDir, Roster: hubcore.NewRoster(runDir, nil)})
	restarted := web.cfg.ResumeLocks
	if restarted == nil {
		t.Fatal("restarted hub has no recovery registry")
	}
	next, err := restarted.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: restarted.RecoveryState(sessionID).Epoch})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Complete(nil)
	if err := next.BeforeLaunch(); err == nil {
		t.Fatal("an interrupted launch's stale proof authorized a replacement over a published child claim")
	}
}

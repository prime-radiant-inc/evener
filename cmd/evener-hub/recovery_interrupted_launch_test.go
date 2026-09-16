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

package hubcore

import (
	"os"
	"path/filepath"
	"testing"
)

// interruptedLaunch leaves one durable launch intent on disk: a confirmed exit
// proof that BeforeLaunch then invalidated for a replacement, without the child
// ever publishing a claim. It returns the registry and the state root.
func interruptedLaunch(t *testing.T) (*ResumeLocks, string) {
	t.Helper()
	root := t.TempDir()
	locks, err := NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	aliases := []string{"owner"}
	if err := locks.PersistForceStop(aliases, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := locks.ConfirmForceStop("owner"); err != nil {
		t.Fatal(err)
	}
	active, err := locks.RegisterResume(t.Context(), "owner", aliases, resumeEpochs(locks, aliases...))
	if err != nil {
		t.Fatal(err)
	}
	if err := active.BeforeLaunch(); err != nil {
		t.Fatal(err)
	}
	return locks, root
}

// TestBeforeLaunchDurablyRecordsLaunchIntent pins the persisted marker a
// restarted hub reads: BeforeLaunch clears the prior exit proof and records
// that a launch was in progress in the same durable write.
func TestBeforeLaunchDurablyRecordsLaunchIntent(t *testing.T) {
	_, root := interruptedLaunch(t)
	reopened, err := NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	state := reopened.RecoveryState("owner")
	if state.ExitConfirmed || !state.LaunchPending {
		t.Fatalf("interrupted launch durability = %+v, want a cleared proof with a launch intent", state)
	}
}

// TestRecoverInterruptedLaunchesRestoresUnclaimedProof proves startup recovery
// puts the prior exit proof back when no alias is claimed, so the ordinary
// resume and force-stop paths run again instead of rejecting the session.
func TestRecoverInterruptedLaunchesRestoresUnclaimedProof(t *testing.T) {
	_, root := interruptedLaunch(t)
	restarted, err := NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RecoverInterruptedLaunches(func([]string) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	state := restarted.RecoveryState("owner")
	if !state.ExitConfirmed || state.LaunchPending || !state.ResumeRequired {
		t.Fatalf("recovered state = %+v, want the confirmed-stopped proof restored", state)
	}
	reopened, err := NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	if durable := reopened.RecoveryState("owner"); !durable.ExitConfirmed || durable.LaunchPending {
		t.Fatalf("recovery was not durable: %+v", durable)
	}
}

// TestRecoverInterruptedLaunchesLeavesClaimedGroupFenced guards the
// BeforeLaunch invariant at the persistence boundary: while any alias is
// claimed, the old proof must not describe a possible new child.
func TestRecoverInterruptedLaunchesLeavesClaimedGroupFenced(t *testing.T) {
	_, root := interruptedLaunch(t)
	restarted, err := NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RecoverInterruptedLaunches(func(aliases []string) (bool, error) {
		return len(aliases) != 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	state := restarted.RecoveryState("owner")
	if state.ExitConfirmed || !state.LaunchPending {
		t.Fatalf("claimed launch intent = %+v, want it left fenced", state)
	}
	reopened, err := NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatal(err)
	}
	if durable := reopened.RecoveryState("owner"); durable.ExitConfirmed || !durable.LaunchPending {
		t.Fatalf("claimed recovery changed durable state: %+v", durable)
	}
}

// TestRecoveryStateReadsOlderVersionWithoutLaunchIntents pins the upgrade path:
// a snapshot an older hub wrote still loads, with no launch intent to recover.
// An older hub reading the newer snapshot fails closed on the version check,
// which TestPersistentRecoveryRejectsCorruptAuthority pins for versions 1 and 2.
func TestRecoveryStateReadsOlderVersionWithoutLaunchIntents(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "recovery"), 0700); err != nil {
		t.Fatal(err)
	}
	older := `{"version":3,"records":[{"alias":"owner","group":"one","session_id":"owner","exit_confirmed":true}]}`
	if err := os.WriteFile(filepath.Join(root, "recovery", "state.json"), []byte(older), 0600); err != nil {
		t.Fatal(err)
	}
	locks, err := NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatalf("older snapshot did not load: %v", err)
	}
	if state := locks.RecoveryState("owner"); !state.ExitConfirmed || state.LaunchPending {
		t.Fatalf("older snapshot state = %+v, want the exit proof with no launch intent", state)
	}
	// The commit path upgrades the file to the current version.
	if err := locks.ConfirmForceStop("owner"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "recovery", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatalf("upgraded snapshot did not load: %v", err)
	}
	if reopened.RecoveryState("owner").LaunchPending {
		t.Fatalf("upgraded snapshot %s invented a launch intent", raw)
	}
}

// TestRecoveryStateReadsVersion4Snapshot pins that a version-4 snapshot this
// hub itself wrote before the signal-attempt marker was added still loads, with
// its launch intent intact and no invented signal attempt.
func TestRecoveryStateReadsVersion4Snapshot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "recovery"), 0700); err != nil {
		t.Fatal(err)
	}
	older := `{"version":4,"records":[{"alias":"owner","group":"one","session_id":"owner","exit_confirmed":false,"launch_pending":true}]}`
	if err := os.WriteFile(filepath.Join(root, "recovery", "state.json"), []byte(older), 0600); err != nil {
		t.Fatal(err)
	}
	locks, err := NewPersistentResumeLocks(root)
	if err != nil {
		t.Fatalf("version-4 snapshot did not load: %v", err)
	}
	state := locks.RecoveryState("owner")
	if state.ExitConfirmed || !state.LaunchPending || state.SignalAttempted {
		t.Fatalf("version-4 snapshot state = %+v, want the launch intent and no signal attempt", state)
	}
}

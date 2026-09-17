package hub

import (
	"errors"
	"path/filepath"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// confirmedStoppedResumeFixture establishes durable confirmed-stopped authority
// for one session and registers an in-flight explicit Resume on it, so
// forceStopThread's top-level confirmedStoppedWithoutClaim shortcut is actually
// reachable. The previous regression test never called
// PersistForceStop/ConfirmForceStop, so the shortcut returned early and the
// cancellation ordering it was meant to pin was never exercised.
func confirmedStoppedResumeFixture(t *testing.T) (*hubcore.ResumeLocks, string, *hubcore.ActiveResume) {
	t.Helper()
	locks := hubcore.NewResumeLocks()
	sessionID := hubtest.SessionID(t)
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
	return locks, sessionID, active
}

// TestForceStopConfirmedStoppedValidatesIdentityBeforeCancelingResume is the
// Medium regression: the confirmed-stopped shortcut's stopResumes branch cancels
// an in-flight Resume inside confirmedStoppedWithoutClaim before the caller
// validates a caller-rendered daemon identity. A force stop addressed to a
// daemon that no longer exists must be refused without aborting the Resume.
func TestForceStopConfirmedStoppedValidatesIdentityBeforeCancelingResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks, sessionID, active := confirmedStoppedResumeFixture(t)
		stale := appwire.DaemonIdentity{Ref: "local:" + sessionID, PID: 4242, StartedAt: "2000-01-01T00:00:00Z", Generation: "stale-generation"}
		cfg := hubcore.WebConfig{
			RunDir:      t.TempDir(),
			ResumeLocks: locks,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				t.Error("confirmed-stopped force stop reached process control")
				return nil, errors.New("unexpected process control")
			}),
		}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID, ExpectedDaemon: &stale}, nil)
		}()
		synctest.Wait()
		select {
		case err := <-stopped:
			if err == nil {
				t.Fatal("force stop with a stale daemon identity succeeded against a confirmed-stopped session")
			}
		default:
			active.Complete(nil)
			<-stopped
			t.Fatal("force stop canceled the in-flight Resume before validating the caller's daemon identity")
		}
		if active.Context().Err() != nil {
			t.Fatal("force stop canceled the in-flight Resume before validating the caller's daemon identity")
		}
		if !locks.HasActiveResume([]string{sessionID}) {
			t.Fatal("force stop removed the in-flight Resume")
		}
	})
}

// TestForceStopConfirmedStoppedValidatesDeletionBeforeCancelingResume is the
// same regression through the caller's deletion fence: a force stop naming a
// deleted target must be refused before the confirmed-stopped shortcut's
// stopResumes branch aborts the in-flight Resume.
func TestForceStopConfirmedStoppedValidatesDeletionBeforeCancelingResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks, sessionID, active := confirmedStoppedResumeFixture(t)
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Begin(filepath.Base(hubtest.ProjectDir(t, t.TempDir(), "deleted")), []hubcore.DeletionTarget{{Ref: "local:" + sessionID, ThreadID: sessionID}}); err != nil {
			t.Fatal(err)
		}
		cfg := hubcore.WebConfig{
			RunDir:        t.TempDir(),
			ResumeLocks:   locks,
			DeletionStore: store,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				t.Error("deleted-target force stop reached process control")
				return nil, errors.New("unexpected process control")
			}),
		}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		synctest.Wait()
		select {
		case err := <-stopped:
			if err == nil {
				t.Fatal("force stop against a deleted target succeeded")
			}
		default:
			active.Complete(nil)
			<-stopped
			t.Fatal("force stop canceled the in-flight Resume before validating the deletion fence")
		}
		if active.Context().Err() != nil {
			t.Fatal("force stop canceled the in-flight Resume before validating the deletion fence")
		}
	})
}

// TestForceStopConfirmedStoppedValidatesSiblingDeletionBeforeCancelingResume
// closes the same Medium finding inside the confirmed-stopped helper itself:
// the request names a live alias, but a sibling alias in the same ownership
// group is deleted. The helper's own per-alias deletion check ran only after it
// had canceled the in-flight Resume, so a refused request still aborted it.
func TestForceStopConfirmedStoppedValidatesSiblingDeletionBeforeCancelingResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		stable, current := hubtest.SessionID(t), hubtest.SessionID(t)
		finish := locks.BeginForceStop([]string{stable, current})
		if err := locks.PersistForceStop([]string{stable, current}, current); err != nil {
			t.Fatal(err)
		}
		if err := locks.ConfirmForceStop(current); err != nil {
			t.Fatal(err)
		}
		finish(true)
		epochs := map[string]uint64{
			stable:  locks.RecoveryState(stable).Epoch,
			current: locks.RecoveryState(current).Epoch,
		}
		active, err := locks.RegisterResume(t.Context(), stable, []string{stable, current}, epochs)
		if err != nil {
			t.Fatal(err)
		}
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Begin(filepath.Base(hubtest.ProjectDir(t, t.TempDir(), "deleted")), []hubcore.DeletionTarget{{Ref: "local:" + current, ThreadID: current}}); err != nil {
			t.Fatal(err)
		}
		cfg := hubcore.WebConfig{
			RunDir:        t.TempDir(),
			ResumeLocks:   locks,
			DeletionStore: store,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				t.Error("sibling-deleted force stop reached process control")
				return nil, errors.New("unexpected process control")
			}),
		}
		stopped := make(chan error, 1)
		go func() {
			// The request names the live alias; only the sibling is deleted.
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + stable}, nil)
		}()
		synctest.Wait()
		select {
		case err := <-stopped:
			if err == nil {
				t.Fatal("force stop with a deleted sibling alias succeeded")
			}
		default:
			active.Complete(nil)
			<-stopped
			t.Fatal("force stop canceled the in-flight Resume before validating a deleted sibling alias")
		}
		if active.Context().Err() != nil {
			t.Fatal("force stop canceled the in-flight Resume before validating a deleted sibling alias")
		}
	})
}

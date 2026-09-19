package hub

import (
	"errors"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// TestConfirmedStoppedShortcutChecksEveryAliasDeletionFence is the Medium
// regression: the confirmed-stopped shortcut must recheck the deletion fence
// for every resolved alias of the ownership group, not only for the session id
// the request named. A deletion record for another alias in the same group was
// bypassed and the request reported success.
func TestConfirmedStoppedShortcutChecksEveryAliasDeletionFence(t *testing.T) {
	stable, current := hubtest.SessionID(t), hubtest.SessionID(t)
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(filepath.Base(hubtest.ProjectDir(t, t.TempDir(), "deleted")), []hubcore.DeletionTarget{{Ref: "local:" + current, ThreadID: current}}); err != nil {
		t.Fatal(err)
	}
	locks := hubcore.NewResumeLocks()
	finish := locks.BeginForceStop([]string{stable, current})
	if err := locks.PersistForceStop([]string{stable, current}, current); err != nil {
		t.Fatal(err)
	}
	if err := locks.ConfirmForceStop(current); err != nil {
		t.Fatal(err)
	}
	finish.Finish(true)
	cfg := hubcore.WebConfig{
		RunDir:        t.TempDir(),
		ResumeLocks:   locks,
		DeletionStore: store,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			t.Error("confirmed-stopped Stop attempted process control")
			return nil, errors.New("unexpected process control")
		}),
	}
	err = forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + stable}, nil)
	if err == nil {
		t.Fatal("confirmed-stopped shortcut bypassed the deletion fence of another alias in the group")
	}
	if !isTargetDeletedError(err) {
		t.Fatalf("force stop error = %v, want the target-deleted refusal", err)
	}
}

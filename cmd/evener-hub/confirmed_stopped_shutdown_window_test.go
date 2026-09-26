package hub

import (
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
)

// TestConfirmedStoppedShutdownHoldsReservationsAcrossFinalChecksAndSuccess
// stages the trace the fresh RoboRev review reported at
// app_force_stop.go:517: that the confirmed-stopped shutdown path
// (stopResumes=false) skips the alias reservations and then invalidates
// admission unsynchronized, so a concurrent Resume could register and launch
// between the final checks and shutdown success. The no-op is paused inside
// exactly that window here — blocked at the under-reservation deletion
// recheck, after acquiring every resolved alias — and the registration must
// not be able to land in the window: RegisterResume parks on the held
// reservations and re-admits on the post-decision snapshot the invalidation
// publishes while they are still held. The window is closed by the
// !reservationsHeld reacquisition immediately before the final checks, which
// runs for the shutdown path precisely because the tryLock above it is gated
// on stopResumes.
func TestConfirmedStoppedShutdownHoldsReservationsAcrossFinalChecksAndSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		sessionID, workspaceID := hubtest.SessionID(t), hubtest.SessionID(t)
		group := []string{sessionID, workspaceID}
		slices.Sort(group)
		finish := locks.BeginForceStop(group)
		if err := locks.PersistForceStop(group, sessionID); err != nil {
			t.Fatal(err)
		}
		if err := locks.ConfirmForceStop(sessionID); err != nil {
			t.Fatal(err)
		}
		finish.Finish(true)
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// Pause the no-op at the under-reservation deletion recheck — the
		// first final check after the resolved aliases are reserved — so the
		// checks-to-success window is observably open while it waits.
		entered, release := make(chan struct{}), make(chan struct{})
		blocked := false
		original := deletionTargetState
		defer func() { deletionTargetState = original }()
		deletionTargetState = func(*hubcore.DeletionStore, string, string) (hubcore.DeletionState, bool) {
			if !blocked {
				blocked = true
				close(entered)
				<-release
			}
			return "", false
		}
		cfg := hubcore.WebConfig{RunDir: t.TempDir(), ResumeLocks: locks, DeletionStore: store}
		epochs := make(map[string]uint64, len(group))
		for _, alias := range group {
			epochs[alias] = locks.RecoveryState(alias).Epoch
		}
		type noopResult struct {
			stopped bool
			err     error
		}
		noop := make(chan noopResult, 1)
		go func() {
			stopped, err := confirmedStoppedWithoutClaim(t.Context(), cfg, sessionID, false, nil)
			noop <- noopResult{stopped: stopped, err: err}
		}()
		<-entered // the no-op holds every resolved alias inside its final-check window
		registered := make(chan error, 1)
		go func() {
			_, err := locks.RegisterResume(t.Context(), sessionID, group, epochs)
			registered <- err
		}()
		synctest.Wait() // the registration is parked on the held reservations
		close(release)
		result := <-noop
		if result.err != nil {
			t.Fatalf("confirmed-stopped no-op: %v", result.err)
		}
		if !result.stopped {
			t.Fatal("the parked registration leaked into the no-op's final checks; shutdown was not proven stopped")
		}
		if err := <-registered; !errors.Is(err, hubcore.ErrResumeInvalidated) {
			t.Fatalf("registration attempting to land in the checks-to-success window = %v, want ErrResumeInvalidated", err)
		}
		// The decision was published under the reservations: a fresh
		// admission snapshot may register and launch afterwards.
		fresh := make(map[string]uint64, len(group))
		for _, alias := range group {
			fresh[alias] = locks.RecoveryState(alias).Epoch
		}
		active, err := locks.RegisterResume(t.Context(), sessionID, group, fresh)
		if err != nil {
			t.Fatal(err)
		}
		if err := active.AcquireOwnership(t.Context()); err != nil {
			t.Fatal(err)
		}
		active.ReleaseOwnership()
		active.Complete(nil)
	})
}

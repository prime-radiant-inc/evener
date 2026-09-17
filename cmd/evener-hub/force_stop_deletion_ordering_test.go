package hub

import (
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

// TestForceStopValidatesDeletionUnderReservationBeforeCancelingResume pins the
// same ordering invariant on the verified-process path that
// TestForceStopConfirmedStoppedValidatesDeletionUnderReservationBeforeCancelingResume
// pins on the confirmed-stopped shortcut: a deletion published after the
// request's entry fence check must be refused before the in-flight Resume that
// registered during process discovery is canceled. The per-alias reservations
// are taken before cancelActiveResumes whenever they are free, and the deletion
// re-check under them is final, because deletion publication takes the same
// reservations.
func TestForceStopValidatesDeletionUnderReservationBeforeCancelingResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		runDir, sessionID := t.TempDir(), hubtest.SessionID(t)
		writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: sessionID, StartedAt: time.Now()})
		original := deletionTargetState
		calls := 0
		// The request's entry deletion check passes; the deletion is published
		// before the validation that runs under the alias reservations.
		deletionTargetState = func(*hubcore.DeletionStore, string, string) (hubcore.DeletionState, bool) {
			calls++
			return hubcore.DeletionStateDeleting, calls > 1
		}
		defer func() { deletionTargetState = original }()
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		resumed := make(chan *hubcore.ActiveResume, 1)
		var events []string
		cfg := hubcore.WebConfig{
			RunDir:        runDir,
			ResumeLocks:   locks,
			DeletionStore: store,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				events = append(events, "open")
				// A Resume registers while force stop's process discovery is
				// running — the window cancelActiveResumes exists to close.
				active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
				if err != nil {
					return nil, err
				}
				resumed <- active
				return &forceStopProcess{events: &events}, nil
			}),
		}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		// Let force stop reach its cancellation and deletion validation. A
		// cancel-then-validate order blocks here waiting for the Resume handler
		// with the Resume already aborted; the fixed order refuses the request
		// under its reservations before canceling at all.
		synctest.Wait()
		active := <-resumed
		select {
		case err := <-stopped:
			if err == nil {
				t.Fatal("force stop with a racing deletion succeeded")
			}
			if !isTargetDeletedError(err) {
				t.Fatalf("force stop error = %v, want the target-deleted refusal", err)
			}
		default:
			active.Complete(nil)
			<-stopped
			t.Fatal("force stop canceled the in-flight Resume before validating the racing deletion")
		}
		if active.Context().Err() != nil {
			t.Fatal("force stop canceled the in-flight Resume before validating the racing deletion")
		}
		if slices.Contains(events, "kill") {
			t.Fatalf("a refused force stop reached process control: %v", events)
		}
	})
}

// TestForceStopValidatesDeletionUnderReservationFallback mirrors the
// cancel-then-acquire fallback: when an in-flight launch already holds an alias
// reservation, deletion publication is blocked by that launch itself, so the
// request cancels, drains, acquires, and only then re-checks the deletion
// authoritatively — and must still refuse rather than report success.
func TestForceStopValidatesDeletionUnderReservationFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		runDir, sessionID := t.TempDir(), hubtest.SessionID(t)
		writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: sessionID, StartedAt: time.Now()})
		original := deletionTargetState
		calls := 0
		deletionTargetState = func(*hubcore.DeletionStore, string, string) (hubcore.DeletionState, bool) {
			calls++
			return hubcore.DeletionStateDeleting, calls > 1
		}
		defer func() { deletionTargetState = original }()
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		allowCleanup, cleaned := make(chan struct{}), make(chan struct{})
		var events []string
		cfg := hubcore.WebConfig{
			RunDir:        runDir,
			ResumeLocks:   locks,
			DeletionStore: store,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				events = append(events, "open")
				active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: locks.RecoveryState(sessionID).Epoch})
				if err != nil {
					return nil, err
				}
				// Hold the alias reservation the way an in-flight launch does,
				// so the force stop's TryLock falls back to cancel-then-acquire.
				locks.For(sessionID).Lock()
				go func() {
					<-active.Context().Done()
					<-allowCleanup
					locks.For(sessionID).Unlock()
					close(cleaned)
					active.Complete(nil)
				}()
				return &forceStopProcess{events: &events}, nil
			}),
		}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		synctest.Wait()
		select {
		case err := <-stopped:
			t.Fatalf("force stop returned before the raced launch released ownership: %v", err)
		default:
		}
		close(allowCleanup)
		<-cleaned
		if err := <-stopped; err == nil {
			t.Fatal("force stop with a racing deletion succeeded")
		} else if !isTargetDeletedError(err) {
			t.Fatalf("force stop error = %v, want the target-deleted refusal", err)
		}
		if slices.Contains(events, "kill") {
			t.Fatalf("a refused force stop reached process control: %v", events)
		}
	})
}

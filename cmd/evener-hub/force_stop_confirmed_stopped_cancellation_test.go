package hub

import (
	"errors"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
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

// TestForceStopConfirmedStoppedRechecksIdentityBeforeCancelingResume pins the
// remaining identity window: the caller validates a caller-rendered daemon
// identity before the confirmed-stopped shortcut, but a replacement claim can
// appear between that validation and the shortcut's cancellation. The shortcut
// must recheck the current rendezvous identity immediately before
// cancelActiveResumes, so the stale request is refused without aborting the
// in-flight Resume it can no longer address.
func TestForceStopConfirmedStoppedRechecksIdentityBeforeCancelingResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks, sessionID, active := confirmedStoppedResumeFixture(t)
		runDir := t.TempDir()
		resident := rendezvous.Entry{
			PID: 4301, SessionID: sessionID, ThreadID: sessionID, WorkspaceRef: "local:" + sessionID,
			Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now(),
		}
		writeRendezvous(t, runDir, resident)
		expected := daemonIdentity(resident)
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// The caller's identity validation sees the resident it was rendered
		// from; the replacement claim appears before the shortcut's
		// cancellation, on the shortcut's first deletion check.
		original := deletionTargetState
		calls := 0
		deletionTargetState = func(s *hubcore.DeletionStore, ref, threadID string) (hubcore.DeletionState, bool) {
			calls++
			if calls == 2 {
				if err := rendezvous.Remove(runDir, resident.PID); err != nil {
					t.Errorf("remove resident claim: %v", err)
				}
				replacement := resident
				replacement.PID = 4302
				replacement.StartedAt = resident.StartedAt.Add(time.Second)
				if _, err := rendezvous.Write(runDir, replacement); err != nil {
					t.Errorf("write replacement claim: %v", err)
				}
			}
			return original(s, ref, threadID)
		}
		defer func() { deletionTargetState = original }()
		cfg := hubcore.WebConfig{
			RunDir:        runDir,
			ResumeLocks:   locks,
			DeletionStore: store,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				t.Error("replacement-claim force stop reached process control")
				return nil, errors.New("unexpected process control")
			}),
		}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID, ExpectedDaemon: &expected}, nil)
		}()
		synctest.Wait()
		select {
		case err := <-stopped:
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
				t.Fatalf("stale force stop error = %v, want conflict", err)
			}
		default:
			active.Complete(nil)
			<-stopped
			t.Fatal("force stop canceled the in-flight Resume before rechecking the current daemon identity")
		}
		if active.Context().Err() != nil {
			t.Fatal("force stop canceled the in-flight Resume before rechecking the current daemon identity")
		}
		if !locks.HasActiveResume([]string{sessionID}) {
			t.Fatal("force stop removed the in-flight Resume")
		}
		active.Complete(nil)
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

// TestForceStopConfirmedStoppedValidatesDeletionUnderReservationBeforeCancelingResume
// pins the remaining ordering gap: a deletion published between the
// pre-cancellation deletion check and the authoritative re-check must still be
// refused before the in-flight Resume is aborted. The free-reservation path
// takes the per-alias reservations first, so its re-check is final and runs
// before cancelActiveResumes; a request that will be refused never cancels the
// Resume it can no longer address.
func TestForceStopConfirmedStoppedValidatesDeletionUnderReservationBeforeCancelingResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks, sessionID, active := confirmedStoppedResumeFixture(t)
		original := deletionTargetState
		calls := 0
		// The caller's deletion check and the helper's pre-check pass; the
		// deletion is published before the final validation under the reservation.
		deletionTargetState = func(*hubcore.DeletionStore, string, string) (hubcore.DeletionState, bool) {
			calls++
			return hubcore.DeletionStateDeleting, calls > 2
		}
		defer func() { deletionTargetState = original }()
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		cfg := hubcore.WebConfig{
			RunDir:        t.TempDir(),
			ResumeLocks:   locks,
			DeletionStore: store,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				t.Error("deletion-racing force stop reached process control")
				return nil, errors.New("unexpected process control")
			}),
		}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		// Let force stop reach its cancellation and deletion validation. A
		// cancel-then-validate order blocks here waiting for the Resume handler;
		// the fixed order returns before canceling at all.
		synctest.Wait()
		// Complete cancels the operation context itself, so sample it first.
		canceledBeforeComplete := active.Context().Err() != nil
		active.Complete(nil)
		if err := <-stopped; err == nil {
			t.Fatal("force stop with a racing deletion succeeded")
		}
		if canceledBeforeComplete {
			t.Fatal("force stop canceled the in-flight Resume before the final deletion validation under its reservation")
		}
	})
}

// TestForceStopConfirmedStoppedRefusalKeepsResumeCompletionValid is the Medium
// regression RoboRev reported against the refusal window
// TestForceStopConfirmedStoppedRechecksIdentityBeforeCancelingResume pins: the
// shortcut's BeginForceStop advances the recovery admission epochs before the
// under-fence identity recheck, and a request refused there by a replacement
// claim used to leave them advanced. The in-flight Resume the refusal
// deliberately preserved was admitted under the pre-fence epoch, so its
// ExplicitResumeCompleted silently no-opped and the durable resume
// requirement stayed set even though the Resume succeeded against the
// replacement daemon. A refusal that canceled nothing must be epoch-neutral.
func TestForceStopConfirmedStoppedRefusalKeepsResumeCompletionValid(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stateRoot := t.TempDir()
		locks, err := hubcore.NewPersistentResumeLocks(stateRoot)
		if err != nil {
			t.Fatal(err)
		}
		sessionID := hubtest.SessionID(t)
		finish := locks.BeginForceStop([]string{sessionID})
		if err := locks.PersistForceStop([]string{sessionID}, sessionID); err != nil {
			t.Fatal(err)
		}
		if err := locks.ConfirmForceStop(sessionID); err != nil {
			t.Fatal(err)
		}
		finish(true)
		resumeEpoch := locks.RecoveryState(sessionID).Epoch
		active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID}, map[string]uint64{sessionID: resumeEpoch})
		if err != nil {
			t.Fatal(err)
		}
		runDir := t.TempDir()
		resident := rendezvous.Entry{
			PID: 4301, SessionID: sessionID, ThreadID: sessionID, WorkspaceRef: "local:" + sessionID,
			Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now(),
		}
		writeRendezvous(t, runDir, resident)
		expected := daemonIdentity(resident)
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// The caller's identity validation sees the resident it was rendered
		// from; the replacement claim appears on the shortcut's first deletion
		// check, after the admission fence already advanced the epochs.
		original := deletionTargetState
		calls := 0
		deletionTargetState = func(s *hubcore.DeletionStore, ref, threadID string) (hubcore.DeletionState, bool) {
			calls++
			if calls == 2 {
				if err := rendezvous.Remove(runDir, resident.PID); err != nil {
					t.Errorf("remove resident claim: %v", err)
				}
				replacement := resident
				replacement.PID = 4302
				replacement.StartedAt = resident.StartedAt.Add(time.Second)
				if _, err := rendezvous.Write(runDir, replacement); err != nil {
					t.Errorf("write replacement claim: %v", err)
				}
			}
			return original(s, ref, threadID)
		}
		defer func() { deletionTargetState = original }()
		cfg := hubcore.WebConfig{
			RunDir:        runDir,
			ResumeLocks:   locks,
			DeletionStore: store,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				t.Error("refused force stop reached process control")
				return nil, errors.New("unexpected process control")
			}),
		}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID, ExpectedDaemon: &expected}, nil)
		}()
		synctest.Wait()
		select {
		case err := <-stopped:
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
				t.Fatalf("stale force stop error = %v, want conflict", err)
			}
		default:
			active.Complete(nil)
			<-stopped
			t.Fatal("force stop canceled the in-flight Resume before rechecking the current daemon identity")
		}
		if active.Context().Err() != nil {
			t.Fatal("force stop canceled the in-flight Resume it refused to address")
		}
		if got := locks.RecoveryState(sessionID).Epoch; got != resumeEpoch {
			t.Fatalf("refused force stop advanced the recovery admission epoch: got %d, want %d", got, resumeEpoch)
		}
		// The in-flight Resume finishes against the replacement daemon exactly
		// as resumeThread's defer does after a successful launch.
		active.Complete(nil)
		if err := locks.ExplicitResumeCompleted(sessionID, resumeEpoch); err != nil {
			t.Fatal(err)
		}
		state := locks.RecoveryState(sessionID)
		if state.ResumeRequired {
			t.Fatal("completed in-flight Resume left the resume requirement set: the refusal was not epoch-neutral")
		}
		if state.ResumeSessionID != "" {
			t.Fatalf("completed in-flight Resume left recovery authority %q set", state.ResumeSessionID)
		}
		reloaded, err := hubcore.NewPersistentResumeLocks(stateRoot)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.RecoveryState(sessionID).ResumeRequired {
			t.Fatal("completed in-flight Resume left the durable resume requirement set: the refusal was not epoch-neutral")
		}
	})
}

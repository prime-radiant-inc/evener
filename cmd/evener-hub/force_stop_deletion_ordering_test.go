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
		// The stub is alias-aware: it reports the deletion only when the alias
		// the fence names is the one being queried, so a check that skips an
		// alias cannot observe the fence through another alias's answer.
		deletionTargetState = func(_ *hubcore.DeletionStore, _, threadID string) (hubcore.DeletionState, bool) {
			calls++
			return hubcore.DeletionStateDeleting, calls > 1 && threadID == sessionID
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

// TestForceStopValidatesSiblingDeletionUnderReservationFallback pins the
// sibling-alias half of the fallback: the persisted fence names ONLY the
// sibling alias while a held reservation forces cancel-then-acquire. A
// post-acquisition re-check that queries only the requested alias bypasses
// the fence and kills the daemon, so the re-check must loop over every alias
// the way the reservationsHeld branch and confirmedStoppedWithoutClaim do.
func TestForceStopValidatesSiblingDeletionUnderReservationFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		runDir, sessionID, siblingID := t.TempDir(), hubtest.SessionID(t), hubtest.SessionID(t)
		writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: siblingID, StartedAt: time.Now()})
		original := deletionTargetState
		// The fence names only the sibling alias; the requested alias is never
		// deleted, so the request's entry fence check passes.
		deletionTargetState = func(_ *hubcore.DeletionStore, _, threadID string) (hubcore.DeletionState, bool) {
			return hubcore.DeletionStateDeleting, threadID == siblingID
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
				// Hold the requested alias's reservation the way an in-flight
				// launch does, so the force stop's TryLock falls back to
				// cancel-then-acquire.
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
			t.Fatal("force stop with a sibling-alias deletion fence succeeded")
		} else if !isTargetDeletedError(err) {
			t.Fatalf("force stop error = %v, want the target-deleted refusal", err)
		}
		if slices.Contains(events, "kill") {
			t.Fatalf("a refused force stop reached process control: %v", events)
		}
	})
}

// TestForceStopValidatesSiblingDeletionBeforeCancelingActiveResume pins the
// entry-drain half of the ordering invariant: the top-level Stop used to
// cancel an already-active Resume before discovering the full alias group or
// consulting any sibling alias's deletion fence, so a request that the
// sibling fence would refuse aborted the Resume first. Every ownership alias
// of the Resume the drain would cancel must be validated before the abort,
// under the per-alias reservations whenever they are free.
func TestForceStopValidatesSiblingDeletionBeforeCancelingActiveResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		runDir := t.TempDir()
		sessionID, siblingID := hubtest.SessionID(t), hubtest.SessionID(t)
		writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: siblingID, StartedAt: time.Now()})
		original := deletionTargetState
		// The fence names only the sibling alias, so the request's entry check
		// on the requested alias passes.
		deletionTargetState = func(_ *hubcore.DeletionStore, _, threadID string) (hubcore.DeletionState, bool) {
			return hubcore.DeletionStateDeleting, threadID == siblingID
		}
		defer func() { deletionTargetState = original }()
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// A Resume on the full ownership group is already active when the
		// request arrives — the Resume the entry drain would abort before the
		// sibling fence is ever consulted.
		active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID, siblingID}, map[string]uint64{
			sessionID: locks.RecoveryState(sessionID).Epoch,
			siblingID: locks.RecoveryState(siblingID).Epoch,
		})
		if err != nil {
			t.Fatal(err)
		}
		var events []string
		cfg := hubcore.WebConfig{
			RunDir:        runDir,
			ResumeLocks:   locks,
			DeletionStore: store,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				events = append(events, "open")
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
			if err == nil {
				t.Fatal("force stop with a sibling-alias deletion fence succeeded")
			}
			if !isTargetDeletedError(err) {
				t.Fatalf("force stop error = %v, want the target-deleted refusal", err)
			}
		default:
			// A handler still draining the canceled Resume means the request
			// aborted the Resume before the sibling fence refused it.
			active.Complete(nil)
			<-stopped
			t.Fatal("force stop canceled the in-flight Resume before validating the sibling deletion fence")
		}
		if active.Context().Err() != nil {
			t.Fatal("force stop canceled the in-flight Resume before validating the sibling deletion fence")
		}
		active.Complete(nil)
		if len(events) != 0 {
			t.Fatalf("a refused force stop reached process control: %v", events)
		}
	})
}

// TestForceStopRefusedAfterUncanceledDrainRollsBackFence is the Medium RoboRev
// reported against the post-ownership refusals: after BeginForceStop and
// cancelActiveResumes, a refusal returned without Reject, so Epoch and
// LastRecoverySequence stayed advanced even though the drain canceled no
// Resume and the stop was refused. A drain that stopped nothing leaves the
// refusal admission-neutral, the way the pre-cancellation refusals already
// are; only a drain that actually canceled a Resume keeps the advance.
func TestForceStopRefusedAfterUncanceledDrainRollsBackFence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		runDir, sessionID := t.TempDir(), hubtest.SessionID(t)
		writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: sessionID, StartedAt: time.Now()})
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		original := deletionTargetState
		calls := 0
		// The request's entry fence check passes; the deletion record is
		// published before the post-ownership re-validation that refuses.
		deletionTargetState = func(*hubcore.DeletionStore, string, string) (hubcore.DeletionState, bool) {
			calls++
			return hubcore.DeletionStateDeleting, calls > 1
		}
		defer func() { deletionTargetState = original }()
		var events []string
		cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DeletionStore: store,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				events = append(events, "open")
				return &forceStopProcess{events: &events}, nil
			})}
		before := locks.RecoveryState(sessionID)
		// Hold the alias reservation the way an unrelated action does, so the
		// request's TryLock falls back to cancel-then-acquire and the deletion
		// validation runs only after ownership. No Resume is active, so the
		// drain below the fence cancels nothing.
		locks.For(sessionID).Lock()
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		synctest.Wait() // the request is waiting for the held reservation
		locks.For(sessionID).Unlock()
		err = <-stopped
		if err == nil {
			t.Fatal("force stop with a racing deletion succeeded")
		}
		if !isTargetDeletedError(err) {
			t.Fatalf("force stop error = %v, want the target-deleted refusal", err)
		}
		if after := locks.RecoveryState(sessionID); after != before {
			t.Fatalf("refused force stop left the admission fence applied: before=%+v, after=%+v", before, after)
		}
		if slices.Contains(events, "kill") {
			t.Fatalf("a refused force stop reached process control: %v", events)
		}
	})
}

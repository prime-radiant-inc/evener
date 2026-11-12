package hub

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

// forceStopExitFixture is one verified-exit force stop: a live rendezvous entry,
// persistent recovery locks, and a deletion store, so the stop can reach its
// confirmation write the way production does.
type forceStopExitFixture struct {
	runDir, stateRoot, sessionID string
	entry                        rendezvous.Entry
	locks                        *hubcore.ResumeLocks
	cfg                          hubcore.WebConfig
	events                       []string
	commits                      int
}

func newForceStopExitFixture(t *testing.T) *forceStopExitFixture {
	t.Helper()
	f := &forceStopExitFixture{
		runDir:    t.TempDir(),
		stateRoot: t.TempDir(),
		sessionID: hubtest.SessionID(t),
	}
	f.entry = rendezvous.Entry{
		PID: 4301, SessionID: f.sessionID, ThreadID: f.sessionID,
		WorkspaceRef: "local:" + f.sessionID, Protocol: appwire.ProtocolVersion,
		Endpoint: "ws://127.0.0.1:1/rpc", StartedAt: time.Now(),
	}
	writeRendezvous(t, f.runDir, f.entry)
	locks, err := hubcore.NewPersistentResumeLocks(f.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	f.locks = locks
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = hubcore.WebConfig{
		RunDir: f.runDir, HubStateRoot: f.stateRoot, ResumeLocks: locks, DeletionStore: store,
	}
	return f
}

// failSecondRecoveryCommit arms the recovery store so its second write fails:
// the force stop's PersistForceStopWithOwner commits first, and the
// ConfirmForceStop the stop ends with is the write this refusal describes.
func (f *forceStopExitFixture) failSecondRecoveryCommit(boom error) {
	f.locks.SetRecoveryStoreFaultsForTest(func() error {
		f.commits++
		if f.commits == 2 {
			return boom
		}
		return nil
	}, nil)
}

func (f *forceStopExitFixture) run(t *testing.T) error {
	t.Helper()
	return forceStopThread(t.Context(), f.cfg, appwire.ThreadForceStopParams{Ref: "local:" + f.sessionID}, nil)
}

// TestForceStopConfirmFailureOnTheKilledPath drives the stop through signal and
// wait and fails its trailing ConfirmForceStop write. The exit was observed, but
// the durable confirmation did not land, so the request reports the persistence
// failure and the recovery obligation stays unconfirmed for the explicit Resume
// to answer.
func TestForceStopConfirmFailureOnTheKilledPath(t *testing.T) {
	f := newForceStopExitFixture(t)
	f.cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		return &forceStopProcess{events: &f.events}, nil
	})
	boom := errors.New("confirm-force-stop write refused")
	f.failSecondRecoveryCommit(boom)

	err := f.run(t)
	assertForceStopUnavailable(t, err, "ConfirmForceStop failure")
	if f.commits != 2 {
		t.Fatalf("recovery commits = %d, want the force-stop persist then its confirmation", f.commits)
	}
	if !slices.Contains(f.events, "kill") || !slices.Contains(f.events, "wait") {
		t.Fatalf("events = %v, want the killed path to signal and wait", f.events)
	}
	state := f.locks.RecoveryState(f.sessionID)
	if !state.ResumeRequired || state.ExitConfirmed {
		t.Fatalf("state = %+v, want the stop obligation left unconfirmed", state)
	}
}

// TestForceStopConfirmFailureOnTheExitedPath is the same refusal on the arm
// that found the process already gone: no signal is sent, but the trailing
// confirmation write still fails and must surface the same way.
func TestForceStopConfirmFailureOnTheExitedPath(t *testing.T) {
	f := newForceStopExitFixture(t)
	f.cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		return nil, daemonprocess.ErrExited
	})
	boom := errors.New("confirm-force-stop write refused")
	f.failSecondRecoveryCommit(boom)

	err := f.run(t)
	assertForceStopUnavailable(t, err, "ConfirmForceStop failure")
	if f.commits != 2 {
		t.Fatalf("recovery commits = %d, want the force-stop persist then its confirmation", f.commits)
	}
	if slices.Contains(f.events, "kill") {
		t.Fatal("the exited path signaled a process that was already gone")
	}
	state := f.locks.RecoveryState(f.sessionID)
	if !state.ResumeRequired || state.ExitConfirmed {
		t.Fatalf("state = %+v, want the stop obligation left unconfirmed", state)
	}
}

// assertForceStopUnavailable checks the wire class the stop's persistence
// refusals answer with, without asserting on the message copy.
func assertForceStopUnavailable(t *testing.T, err error, what string) {
	t.Helper()
	var wire appwire.WireError
	if err == nil || !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("%s = %v, want an Unavailable wire error", what, err)
	}
}

// TestForceStopCancelAfterOwnershipConfirmation covers the request-context
// cancellation that lands after every ownership check passed and before
// recovery persistence: confirmOwnership's final ctx check is the only thing
// that can stop the stop there. Nothing was signaled and no recovery obligation
// was installed, so the refusal stays admission-neutral.
func TestForceStopCancelAfterOwnershipConfirmation(t *testing.T) {
	f := newForceStopExitFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	opens := 0
	f.cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		opens++
		if opens == 2 {
			// The second open is confirmOwnership's exit re-check: cancel after
			// the last phase boundary, while it still believes the stop can go on.
			cancel()
		}
		return nil, daemonprocess.ErrExited
	})
	before := f.locks.RecoveryState(f.sessionID)

	err := forceStopThread(ctx, f.cfg, appwire.ThreadForceStopParams{Ref: "local:" + f.sessionID}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stop error = %v, want context.Canceled", err)
	}
	if opens != 2 {
		t.Fatalf("process opens = %d, want open then the ownership re-check", opens)
	}
	if f.commits != 0 {
		t.Fatalf("recovery commits = %d, want none: the cancel landed before persistence", f.commits)
	}
	after := f.locks.RecoveryState(f.sessionID)
	if after.ResumeRequired || after.ExitConfirmed || after.Stopping != 0 {
		t.Fatalf("canceled stop installed a recovery obligation: before=%+v after=%+v", before, after)
	}
}

// TestForceStopCancelAfterProcessVerification covers the earlier phase
// boundary: the process was opened and verified, then the request context
// canceled before the descendant scan. Nothing was signaled and no recovery
// obligation was installed.
func TestForceStopCancelAfterProcessVerification(t *testing.T) {
	f := newForceStopExitFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	f.cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		cancel()
		return &forceStopProcess{events: &f.events}, nil
	})
	before := f.locks.RecoveryState(f.sessionID)

	err := forceStopThread(ctx, f.cfg, appwire.ThreadForceStopParams{Ref: "local:" + f.sessionID}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stop error = %v, want context.Canceled", err)
	}
	if slices.Contains(f.events, "kill") || slices.Contains(f.events, "wait") {
		t.Fatalf("events = %v, want a stop that canceled before signaling", f.events)
	}
	if f.commits != 0 {
		t.Fatalf("recovery commits = %d, want none: the cancel landed before persistence", f.commits)
	}
	after := f.locks.RecoveryState(f.sessionID)
	if after.ResumeRequired || after.ExitConfirmed || after.Stopping != 0 {
		t.Fatalf("canceled stop installed a recovery obligation: before=%+v after=%+v", before, after)
	}
}

// TestForceStopExpectedDaemonConflictAfterValidation reaches the early
// expectedDaemonConflict arm that follows forceStopEntry in openProcess. The
// caller's identity passes validateForceStopRequest, then the entry-active
// Resume drain pauses the stop long enough for a replacement daemon to publish
// under the same session. The stop must refuse the replacement before opening
// any process; validation alone cannot see a claim that lands after it.
func TestForceStopExpectedDaemonConflictAfterValidation(t *testing.T) {
	f := newForceStopExitFixture(t)
	f.cfg.DaemonProcesses = forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
		t.Error("openProcess opened a process after the addressed daemon was replaced")
		return nil, daemonprocess.ErrExited
	})
	expected := daemonIdentity(f.entry)
	active, err := f.locks.RegisterResume(t.Context(), f.sessionID, []string{f.sessionID},
		map[string]uint64{f.sessionID: f.locks.RecoveryState(f.sessionID).Epoch})
	if err != nil {
		t.Fatalf("RegisterResume: %v", err)
	}
	stopDone := make(chan struct{})
	defer close(stopDone)
	setupErr := make(chan error, 1)
	go func() {
		select {
		case <-active.Context().Done():
		case <-stopDone:
			return
		}
		if err := rendezvous.Remove(f.runDir, f.entry.PID); err != nil {
			setupErr <- err
			active.Complete(nil)
			return
		}
		replacement := f.entry
		replacement.PID = f.entry.PID + 1
		replacement.StartedAt = f.entry.StartedAt.Add(time.Second)
		if _, err := rendezvous.Write(f.runDir, replacement); err != nil {
			setupErr <- err
			active.Complete(nil)
			return
		}
		setupErr <- nil
		active.Complete(nil)
	}()

	err = forceStopThread(t.Context(), f.cfg, appwire.ThreadForceStopParams{
		Ref: "local:" + f.sessionID, ExpectedDaemon: &expected,
	}, nil)
	select {
	case replaceErr := <-setupErr:
		if replaceErr != nil {
			t.Fatalf("replace rendezvous entry: %v", replaceErr)
		}
	case <-time.After(time.Second):
		t.Fatal("the stop never reached the resume drain, so it never read the replacement entry")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("replaced-daemon stop error = %v, want the identity Conflict", err)
	}
}

package hub

import (
	"context"
	"errors"
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

// stageHeldGroupAlias stages a force stop whose entry-active Resume owns a
// group alias that the daemon's own entry does not claim, and holds that
// alias's reservation the way an unrelated in-flight action does. The request
// cannot take its reservations up front and must fall back to waiting for the
// held alias.
func stageHeldGroupAlias(t *testing.T) (cfg hubcore.WebConfig, locks *hubcore.ResumeLocks, sessionID, groupAliasID string, events *[]string) {
	t.Helper()
	locks = hubcore.NewResumeLocks()
	runDir := t.TempDir()
	sessionID, entryThreadID, groupAliasID := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 4243, SessionID: sessionID, ThreadID: entryThreadID, StartedAt: time.Now()})
	active, err := locks.RegisterResume(t.Context(), sessionID, []string{sessionID, groupAliasID}, map[string]uint64{
		sessionID:    locks.RecoveryState(sessionID).Epoch,
		groupAliasID: locks.RecoveryState(groupAliasID).Epoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-active.Context().Done()
		active.Complete(nil)
	}()
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	events = new([]string)
	cfg = hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DeletionStore: store,
		DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
			return &forceStopProcess{events: events}, nil
		})}
	locks.For(groupAliasID).Lock()
	return cfg, locks, sessionID, groupAliasID, events
}

// A group alias held by another action does not refuse the stop: the request
// waits for it, takes the complete alias set, and stops the daemon once the
// holder lets go.
func TestForceStopWaitsForAHeldGroupAliasReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg, locks, sessionID, groupAliasID, events := stageHeldGroupAlias(t)
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		synctest.Wait()
		select {
		case err := <-stopped:
			t.Fatalf("force stop finished while the group alias was held: %v", err)
		default:
		}
		if slices.Contains(*events, "kill") {
			t.Fatal("daemon was signaled before the held group alias was reserved")
		}
		locks.For(groupAliasID).Unlock()
		if err := <-stopped; err != nil {
			t.Fatalf("force stop after the holder released: %v", err)
		}
		if !slices.Contains(*events, "kill") {
			t.Fatal("daemon was not signaled once the group alias was reserved")
		}
		if !locks.For(groupAliasID).TryLock() {
			t.Fatal("force stop kept the group alias reserved after returning")
		}
	})
}

// A request abandoned while it waits for a held alias refuses without
// signaling the daemon, persists no recovery obligation, and releases the
// reservations it had taken. The Resume it already canceled stays canceled, so
// the fence publishes its epoch advance for that cancellation.
func TestForceStopCanceledWhileWaitingForAliasReservationSignalsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg, locks, sessionID, groupAliasID, events := stageHeldGroupAlias(t)
		ctx, cancel := context.WithCancel(t.Context())
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(ctx, cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		synctest.Wait()
		cancel()
		if err := <-stopped; !errors.Is(err, context.Canceled) {
			t.Fatalf("abandoned force stop = %v, want the cancellation", err)
		}
		if slices.Contains(*events, "kill") {
			t.Fatal("abandoned force stop still signaled the daemon")
		}
		if after := locks.RecoveryState(sessionID); after.Stopping != 0 || after.ResumeRequired {
			t.Fatalf("abandoned force stop persisted a recovery obligation: %+v", after)
		}
		locks.For(groupAliasID).Unlock()
		if !locks.For(sessionID).TryLock() {
			t.Fatal("abandoned force stop kept its reservations")
		}
	})
}

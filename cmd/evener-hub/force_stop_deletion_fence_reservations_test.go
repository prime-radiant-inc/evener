package hub

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

// killPauseProcess pauses the force-stop signal inside Kill: the request has
// already passed its final deletion-fence check and committed recovery
// authority, so a test can stage a deletion publication between the check and
// the signal — exactly the window the deletion-fence reservations must close.
type killPauseProcess struct {
	events  *[]string
	atKill  chan struct{}
	proceed chan struct{}
}

func (p *killPauseProcess) Kill() error {
	close(p.atKill)
	<-p.proceed
	*p.events = append(*p.events, "kill")
	return nil
}

func (p *killPauseProcess) Wait(context.Context) error {
	*p.events = append(*p.events, "wait")
	return nil
}

func (p *killPauseProcess) Close() error {
	*p.events = append(*p.events, "close")
	return nil
}

// TestForceStopReservesDeletionFenceAliasesThroughProcessControl is the Medium
// RoboRev reported against the deletion-fence reservations on PR 1392 head
// b8cb9c3 (cmd/evener-hub/app_force_stop.go:201-220): deletionFenceAliases may
// include aliases from the entry-active Resume's ownership group that are not
// in the daemon's aliases, but only the latter were reserved, so a deletion
// could publish on an extra alias after the final fence check and race the
// force-stop signal. The request is paused inside its process-control
// operation here — past the final fence check, before the signal takes
// effect — while a deletion publication takes the group alias's reservation
// the way a real publication does. The request must reserve the complete
// deduplicated deletion-fence set across that window: the group alias's
// reservation is held while the signal is about to fire, so the publication
// cannot make its record visible until the request has finished.
func TestForceStopReservesDeletionFenceAliasesThroughProcessControl(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks := hubcore.NewResumeLocks()
		runDir := t.TempDir()
		sessionID, entryThreadID, groupAliasID := hubtest.SessionID(t), hubtest.SessionID(t), hubtest.SessionID(t)
		// The daemon entry claims only sessionID and entryThreadID, while the
		// entry-active Resume's ownership group adds groupAliasID: the request
		// resolves a deletion-fence set of three aliases but a daemon alias
		// set of two, and the group alias is the extra one only the fence
		// check consults.
		writeRendezvous(t, runDir, rendezvous.Entry{PID: 4242, SessionID: sessionID, ThreadID: entryThreadID, StartedAt: time.Now()})
		store, err := hubcore.NewDeletionStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
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
		// The deletion record becomes visible on the group alias only once
		// the racing publication has taken the alias's reservation, the way a
		// real publication does.
		published := make(chan struct{})
		original := deletionTargetState
		defer func() { deletionTargetState = original }()
		deletionTargetState = func(_ *hubcore.DeletionStore, _, threadID string) (hubcore.DeletionState, bool) {
			select {
			case <-published:
				return hubcore.DeletionStateDeleting, threadID == groupAliasID
			default:
				return hubcore.DeletionStateDeleting, false
			}
		}
		atKill, proceed := make(chan struct{}), make(chan struct{})
		var events []string
		process := &killPauseProcess{events: &events, atKill: atKill, proceed: proceed}
		cfg := hubcore.WebConfig{RunDir: runDir, ResumeLocks: locks, DeletionStore: store,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				return process, nil
			})}
		stopped := make(chan error, 1)
		go func() {
			stopped <- forceStopThread(t.Context(), cfg, appwire.ThreadForceStopParams{Ref: "local:" + sessionID}, nil)
		}()
		<-atKill // the request has passed the final fence check; the signal is about to take effect
		// The complete deduplicated deletion-fence set must be reserved across
		// the authoritative recheck and this operation: the group alias's
		// reservation is held by the request now.
		extraReserved := false
		if locks.For(groupAliasID).TryLock() {
			locks.For(groupAliasID).Unlock()
		} else {
			extraReserved = true
		}
		// A deletion publication takes the target alias's reservation before
		// its record becomes visible.
		publication := make(chan struct{})
		go func() {
			locks.For(groupAliasID).Lock()
			close(published)
			locks.For(groupAliasID).Unlock()
			close(publication)
		}()
		synctest.Wait() // the publication completes inside the window or parks on the held reservation
		visibleBeforeSignal := false
		select {
		case <-published:
			visibleBeforeSignal = true
		default:
		}
		close(proceed)
		err = <-stopped
		<-publication
		if !extraReserved {
			t.Fatal("force stop reached process control without reserving the entry-active Resume's group alias from its deletion-fence set")
		}
		if visibleBeforeSignal {
			t.Fatal("a deletion record became visible on the ownership-group alias between the final fence check and the force-stop signal")
		}
		if err != nil {
			t.Fatalf("force stop refused although the deletion could not publish inside the window: %v", err)
		}
	})
}

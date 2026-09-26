package hub

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/daemonprocess"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// aliasWaitFixture builds one rendezvous claim whose sorted ownership aliases
// are "a-thread", "b-session", and "c-workspace", and holds the middle one so a
// caller must acquire the earlier alias first and then block. It returns the
// registry and a release func for the held alias.
func aliasWaitFixture(t *testing.T) (*hubcore.ResumeLocks, rendezvous.Entry, string, func()) {
	t.Helper()
	runDir := t.TempDir()
	entry := rendezvous.Entry{
		PID:          4242,
		SourceID:     "local",
		Protocol:     appwire.ProtocolVersion,
		ThreadID:     "a-thread",
		SessionID:    "b-session",
		WorkspaceRef: "local:c-workspace",
		StateDir:     t.TempDir(),
		StartedAt:    time.Now(),
	}
	writeRendezvous(t, runDir, entry)
	locks := hubcore.NewResumeLocks()
	blocked := locks.For("b-session")
	blocked.Lock()
	return locks, entry, runDir, blocked.Unlock
}

// TestDaemonRetireCanceledAliasWaitReleasesAcquiredPrefix is the Medium
// regression for the retire path: it is context-aware, so it must acquire each
// alias with LockContext and release the prefix it already holds when the
// request is canceled, instead of blocking on the later alias and retaining the
// earlier one.
func TestDaemonRetireCanceledAliasWaitReleasesAcquiredPrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		locks, entry, runDir, release := aliasWaitFixture(t)
		defer release()
		cfg := hubcore.WebConfig{
			RunDir:      runDir,
			ResumeLocks: locks,
			DaemonProcesses: forceStopControllerFunc(func(daemonprocess.Target) (daemonprocess.Process, error) {
				t.Error("canceled retire reached process control")
				return nil, errors.New("unexpected process control")
			}),
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		completed := make(chan error, 1)
		go func() {
			_, err := retireDaemon(ctx, cfg, nil, appwire.DaemonRetireParams{Identity: daemonIdentity(entry)})
			completed <- err
		}()
		synctest.Wait()
		if locks.For("a-thread").TryLock() {
			locks.For("a-thread").Unlock()
			t.Fatal("retire did not acquire the earlier alias before blocking")
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-completed:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled retire error = %v, want context.Canceled", err)
			}
		default:
			t.Fatal("canceled retire still waits for the later alias")
		}
		if !locks.For("a-thread").TryLock() {
			t.Error("canceled retire retained its acquired prefix")
		} else {
			locks.For("a-thread").Unlock()
		}
		if locks.For("b-session").TryLock() {
			locks.For("b-session").Unlock()
			t.Error("canceled retire released another owner's alias")
		}
	})
}

// TestRetirementResumeCanceledAliasWaitReleasesAcquiredPrefix is the Medium
// regression for the retirement resume path. It holds the ownership locks
// around the shared spawn half, so a canceled request must stop waiting on the
// later alias and release the aliases it already acquired.
func TestRetirementResumeCanceledAliasWaitReleasesAcquiredPrefix(t *testing.T) {
	prevRefresh := hubRosterRefresh
	hubRosterRefresh = func(context.Context, *hubcore.Roster) error { return nil }
	t.Cleanup(func() { hubRosterRefresh = prevRefresh })

	synctest.Test(t, func(t *testing.T) {
		locks, _, runDir, release := aliasWaitFixture(t)
		defer release()
		cfg := hubcore.WebConfig{
			RunDir:      runDir,
			Roster:      hubcore.NewRoster(runDir, nil),
			ResumeLocks: locks,
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		completed := make(chan error, 1)
		go func() {
			completed <- resumeAfterConfirmedRetirement(ctx, cfg, nil, appwire.TurnStartParams{Ref: "local:a-thread"})
		}()
		synctest.Wait()
		if locks.For("a-thread").TryLock() {
			locks.For("a-thread").Unlock()
			t.Fatal("retirement resume did not acquire the earlier alias before blocking")
		}
		cancel()
		synctest.Wait()
		select {
		case err := <-completed:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled retirement resume error = %v, want context.Canceled", err)
			}
		default:
			t.Fatal("canceled retirement resume still waits for the later alias")
		}
		if !locks.For("a-thread").TryLock() {
			t.Error("canceled retirement resume retained its acquired prefix")
		} else {
			locks.For("a-thread").Unlock()
		}
		if locks.For("b-session").TryLock() {
			locks.For("b-session").Unlock()
			t.Error("canceled retirement resume released another owner's alias")
		}
	})
}

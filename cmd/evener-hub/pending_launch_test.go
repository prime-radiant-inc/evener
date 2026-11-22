package hub

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/rendezvous"
)

// fakePendingChild is the launch boundary for a resume whose child stays alive.
// kill ends its wait; allow lets a test retire it on its own.
type fakePendingChild struct {
	waiting, killed, allow chan struct{}
	killOnce               sync.Once
}

func newFakePendingChild() *fakePendingChild {
	return &fakePendingChild{waiting: make(chan struct{}), killed: make(chan struct{}), allow: make(chan struct{})}
}

func (c *fakePendingChild) start(*exec.Cmd) (resumeChild, error) {
	return resumeChild{pid: 4242, kill: func() error {
		c.killOnce.Do(func() { close(c.killed) })
		return nil
	}, wait: func() error {
		close(c.waiting)
		select {
		case <-c.killed:
		case <-c.allow:
		}
		return nil
	}}, nil
}

func (c *fakePendingChild) install(t *testing.T) {
	t.Helper()
	original := startResumeChild
	t.Cleanup(func() { startResumeChild = original })
	startResumeChild = c.start
}

// retire lets the test's child finish without caring whether the launch already
// killed it. synctest bubbles wait for every goroutine, so a failing assertion
// must not leave the child waiting forever.
func (c *fakePendingChild) retire() {
	c.killOnce.Do(func() { close(c.allow) })
}

// A caller that walks away mid-restore must leave its healthy child and log
// alone: the claim now owns them, and the child is not reaped by the departure.
func TestResumeDetachKeepsHealthyChildAndLog(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runDir, sessionID := t.TempDir(), hubtest.SessionID(t)
		locks := hubcore.NewResumeLocks()
		defer locks.Close()
		claim, created := locks.ClaimPendingLaunch(sessionID, time.Minute)
		if !created {
			t.Fatal("claim was not created")
		}
		child := newFakePendingChild()
		child.install(t)
		defer child.retire()

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := resumeDaemonWithProviderConfig(ctx, "fixture-evener", runDir, hubcore.ResumeRequest{
				SessionID: sessionID, PendingLaunch: claim, Caller: ctx,
			}, 0, io.Discard, nil)
			done <- err
		}()
		<-child.waiting
		cancel()
		err := <-done
		if !errors.Is(err, errResumeStillStarting) {
			t.Fatalf("detached resume error = %v, want errResumeStillStarting", err)
		}
		if errors.Is(err, errRendezvousCanceled) {
			t.Fatal("detached resume kept the canceled classification")
		}
		select {
		case <-child.killed:
			t.Fatal("departing caller killed the healthy child")
		default:
		}
		if _, ok := locks.PendingLaunch(sessionID); !ok {
			t.Fatal("claim was dropped with the caller")
		}

		time.Sleep(time.Millisecond)
		writeRendezvous(t, runDir, rendezvous.Entry{
			PID: 4242, SessionID: sessionID, ThreadID: sessionID, StartedAt: time.Now(),
		})
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		entry, awaitErr := claim.Await(t.Context())
		if awaitErr != nil || entry.SessionID != sessionID {
			t.Fatalf("published launch = %+v, %v", entry, awaitErr)
		}
		if _, ok := locks.PendingLaunch(sessionID); ok {
			t.Fatal("claim survived publication")
		}
		select {
		case <-child.killed:
			t.Fatal("publication killed the healthy child")
		default:
		}
		logPath := filepath.Join(runDir, daemonLogDirName, daemonLogName(sessionID))
		if _, statErr := os.Stat(logPath); statErr != nil {
			t.Fatalf("restoring daemon log was discarded: %v", statErr)
		}
		child.retire()
		synctest.Wait()
	})
}

// The claim's deadline is the hung-child ceiling: a child that never registers
// is reaped there, just as the caller path reaps it today.
func TestPendingLaunchDeadlineReapsHungChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runDir, sessionID := t.TempDir(), hubtest.SessionID(t)
		locks := hubcore.NewResumeLocks()
		defer locks.Close()
		claim, _ := locks.ClaimPendingLaunch(sessionID, 100*time.Millisecond)
		child := newFakePendingChild()
		child.install(t)
		defer child.retire()

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := resumeDaemonWithProviderConfig(ctx, "fixture-evener", runDir, hubcore.ResumeRequest{
				SessionID: sessionID, PendingLaunch: claim, Caller: ctx,
			}, 0, io.Discard, nil)
			done <- err
		}()
		<-child.waiting
		cancel()
		<-done
		select {
		case <-child.killed:
			t.Fatal("departing caller killed the child before the claim deadline")
		default:
		}

		time.Sleep(300 * time.Millisecond)
		synctest.Wait()
		select {
		case <-child.killed:
		default:
			t.Fatal("hung claimed child survived the claim deadline")
		}
		if _, err := claim.Await(t.Context()); !errors.Is(err, errRendezvousTimeout) {
			t.Fatalf("deadline outcome = %v, want errRendezvousTimeout", err)
		}
		if _, ok := locks.PendingLaunch(sessionID); ok {
			t.Fatal("reaped child left its claim behind")
		}
	})
}

// A claimed child that exits on its own clears the claim without a caller.
func TestPendingLaunchChildExitClearsClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runDir, sessionID := t.TempDir(), hubtest.SessionID(t)
		locks := hubcore.NewResumeLocks()
		defer locks.Close()
		claim, _ := locks.ClaimPendingLaunch(sessionID, time.Minute)
		child := newFakePendingChild()
		child.install(t)
		defer child.retire()

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := resumeDaemonWithProviderConfig(ctx, "fixture-evener", runDir, hubcore.ResumeRequest{
				SessionID: sessionID, PendingLaunch: claim, Caller: ctx,
			}, 0, io.Discard, nil)
			done <- err
		}()
		<-child.waiting
		cancel()
		<-done
		select {
		case <-child.killed:
			t.Fatal("departing caller killed the child before it exited on its own")
		default:
		}

		child.retire()
		synctest.Wait()
		if _, err := claim.Await(t.Context()); !errors.Is(err, errExitedBeforeRendezvous) {
			t.Fatalf("child exit outcome = %v, want errExitedBeforeRendezvous", err)
		}
		if _, ok := locks.PendingLaunch(sessionID); ok {
			t.Fatal("exited child left its claim behind")
		}
	})
}

// Hub shutdown cancels the claim, which reaps the detached child and lets
// Close return without leaking the supervisor goroutine.
func TestPendingLaunchCloseReapsDetachedChild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runDir, sessionID := t.TempDir(), hubtest.SessionID(t)
		locks := hubcore.NewResumeLocks()
		claim, _ := locks.ClaimPendingLaunch(sessionID, time.Minute)
		child := newFakePendingChild()
		child.install(t)
		defer child.retire()

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := resumeDaemonWithProviderConfig(ctx, "fixture-evener", runDir, hubcore.ResumeRequest{
				SessionID: sessionID, PendingLaunch: claim, Caller: ctx,
			}, 0, io.Discard, nil)
			done <- err
		}()
		<-child.waiting
		cancel()
		<-done
		select {
		case <-child.killed:
			t.Fatal("departing caller killed the child before hub shutdown")
		default:
		}

		closed := make(chan struct{})
		go func() {
			locks.Close()
			close(closed)
		}()
		synctest.Wait()
		select {
		case <-closed:
		default:
			t.Fatal("Close returned before the detached child was reaped")
		}
		select {
		case <-child.killed:
		default:
			t.Fatal("Close did not reap the detached child")
		}
		if _, ok := locks.PendingLaunch(sessionID); ok {
			t.Fatal("Close left the claim behind")
		}
	})
}

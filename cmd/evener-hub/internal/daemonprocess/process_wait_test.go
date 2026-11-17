package daemonprocess

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// signalingProcess is a handle whose OS reports the exit as a signal, the shape
// a Linux pidfd has. It counts how many times the waiter asked, which is the
// property under test: a waiter parked on the signal asks once.
type signalingProcess struct {
	kernelProcess
	waits atomic.Int32
	gone  atomic.Bool
	// release is closed to report the exit.
	release chan struct{}
	// parked is closed once waitExit is entered, so a test can prove the waiter
	// is blocked on the signal rather than between ticks.
	parked chan struct{}
}

func (s *signalingProcess) waitExit(ctx context.Context) error {
	s.waits.Add(1)
	close(s.parked)
	select {
	case <-s.release:
		s.gone.Store(true)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *signalingProcess) exited() (bool, error) { return s.gone.Load(), nil }

// TestWaitParksOnTheExitSignal proves that a waiter on a handle whose OS can
// report exit does not poll. The old waiter asked every 20 ms for the whole
// wait; this requires exactly one question, entered before the exit is
// reported, so a ticker-based waiter cannot pass it.
func TestWaitParksOnTheExitSignal(t *testing.T) {
	t.Parallel()
	k := &signalingProcess{release: make(chan struct{}), parked: make(chan struct{})}
	k.facts = validIdentity()
	p, err := (controller{bind: func(int) (processHandle, error) { return k, nil }}).Open(validTarget())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	done := make(chan error, 1)
	go func() { done <- p.Wait(context.Background()) }()

	select {
	case <-k.parked:
	case <-time.After(5 * time.Second): // TRIPWIRE: proves the waiter parks; only fires on a genuine hang.
		t.Fatal("Wait never parked on the exit signal")
	}
	// Still unanswered: the waiter must be parked, not spinning. A polling
	// waiter cannot make progress past its first question while the exit is
	// unreported, so the count after the exit is the evidence — no sleep needed
	// to prove the absence of a spin (see docs/developing-evener/testing.md).
	close(k.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(5 * time.Second): // TRIPWIRE: the signal was delivered; only fires on a genuine hang.
		t.Fatal("Wait did not return when the exit signal arrived")
	}
	if got := k.waits.Load(); got != 1 {
		t.Fatalf("waiter asked %d times for a single exit, want 1", got)
	}
}

// TestWaitReportsCancellationWhileParked keeps the context contract the polling
// waiter had: a cancelled wait returns promptly and says why.
func TestWaitReportsCancellationWhileParked(t *testing.T) {
	t.Parallel()
	k := &signalingProcess{release: make(chan struct{}), parked: make(chan struct{})}
	k.facts = validIdentity()
	p, err := (controller{bind: func(int) (processHandle, error) { return k, nil }}).Open(validTarget())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Wait(ctx) }()
	select {
	case <-k.parked:
	case <-time.After(5 * time.Second): // TRIPWIRE: proves the waiter parks; only fires on a genuine hang.
		t.Fatal("Wait never parked on the exit signal")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait after cancel: %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second): // TRIPWIRE: cancellation is immediate; only fires on a genuine hang.
		t.Fatal("Wait did not return after its context was cancelled")
	}
}

// TestCloseEndsAnActiveWait covers the Close that races a parked waiter. Close
// releases the descriptor the waiter is parked on, so the waiter must be woken
// by the Close rather than left polling a number a later open could reuse for a
// descriptor that is not this daemon's.
func TestCloseEndsAnActiveWait(t *testing.T) {
	t.Parallel()
	k := &signalingProcess{release: make(chan struct{}), parked: make(chan struct{})}
	k.facts = validIdentity()
	p, err := (controller{bind: func(int) (processHandle, error) { return k, nil }}).Open(validTarget())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- p.Wait(context.Background()) }()
	select {
	case <-k.parked:
	case <-time.After(5 * time.Second): // TRIPWIRE: proves the waiter parks; only fires on a genuine hang.
		t.Fatal("Wait never parked on the exit signal")
	}
	// Close while the waiter is parked, and never report the exit: the only way
	// the wait can end is the Close.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an active wait ended successfully on a Close that reported no exit")
		}
	case <-time.After(5 * time.Second): // TRIPWIRE: Close must wake the waiter; only fires on a genuine hang.
		t.Fatal("Close did not end the active wait")
	}
}

// TestWaitRefusesAClosedProcess keeps the plain case: a wait started after Close
// is refused rather than answered.
func TestWaitRefusesAClosedProcess(t *testing.T) {
	t.Parallel()
	k := &signalingProcess{release: make(chan struct{}), parked: make(chan struct{})}
	k.facts = validIdentity()
	p, err := (controller{bind: func(int) (processHandle, error) { return k, nil }}).Open(validTarget())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(context.Background()); err == nil {
		t.Fatal("closed process treated as exited")
	}
}

package hubcore

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/rendezvous"
)

// One session has one launch in flight: a second caller gets the same claim and
// the claim disappears the moment its outcome is published.
func TestPendingLaunchClaimDeduplicatesUntilSettled(t *testing.T) {
	locks := NewResumeLocks()
	claim, created := locks.ClaimPendingLaunch("session-a", time.Minute)
	if !created || claim == nil {
		t.Fatalf("first claim = %v created=%t", claim, created)
	}
	second, created := locks.ClaimPendingLaunch("session-a", time.Minute)
	if created || second != claim {
		t.Fatalf("second claim = %v created=%t, want the first claim", second, created)
	}
	if !locks.HasPendingLaunch("session-a") {
		t.Fatal("claim is not visible while in flight")
	}

	want := rendezvous.Entry{PID: 42, SessionID: "session-a"}
	claim.Settle(want, nil)
	got, err := claim.Await(t.Context())
	if err != nil || got != want {
		t.Fatalf("Await = %+v, %v", got, err)
	}
	if _, ok := locks.PendingLaunch("session-a"); ok {
		t.Fatal("settled claim still named the session")
	}
}

// Close is what bounds a hub shutdown: it cancels every in-flight launch and
// returns only after each one settled, and it refuses later claims.
func TestPendingLaunchCloseCancelsInFlightAndRefusesNewClaims(t *testing.T) {
	locks := NewResumeLocks()
	claim, created := locks.ClaimPendingLaunch("session-a", time.Minute)
	if !created {
		t.Fatal("claim was not created")
	}
	// Settle closes claim.done, which is what unblocks Close, so anything the
	// goroutine does after Settle returns can run after Close returned. Record
	// that settling started before Settle instead: Close's wait makes that
	// store visible here without depending on how the goroutine is scheduled.
	var settling atomic.Bool
	go func() {
		<-claim.Context().Done()
		settling.Store(true)
		claim.Settle(rendezvous.Entry{}, claim.Context().Err())
	}()
	locks.Close()
	if !settling.Load() {
		t.Fatal("Close returned before the in-flight claim settled")
	}
	if _, ok := locks.PendingLaunch("session-a"); ok {
		t.Fatal("closed registry retained a claim")
	}
	late, created := locks.ClaimPendingLaunch("session-b", time.Minute)
	if created {
		t.Fatal("closed registry created a new claim")
	}
	if _, err := late.Await(t.Context()); !errors.Is(err, ErrResumeLocksClosed) {
		t.Fatalf("late claim Await = %v, want ErrResumeLocksClosed", err)
	}
}

// A waiter that gives up must not settle or otherwise consume the launch: the
// child still belongs to whoever holds the claim.
func TestPendingLaunchAwaitHonorsCallerContextWithoutSettling(t *testing.T) {
	locks := NewResumeLocks()
	claim, _ := locks.ClaimPendingLaunch("session-a", time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := claim.Await(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Await = %v, want context.Canceled", err)
	}
	if _, ok := locks.PendingLaunch("session-a"); !ok {
		t.Fatal("caller cancellation consumed the claim")
	}
	claim.Settle(rendezvous.Entry{}, nil)
}

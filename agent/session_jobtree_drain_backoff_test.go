package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestDrainIdleBackoffKickCadence pins the adaptive kick schedule added for
// perf(drain-dirty): a drain that stays quiet (outstanding work remains but
// nothing moves) kicks every pass for the first drainIdleBackoffFullRatePasses
// passes, then throttles to every drainIdleBackoffSlowEvery-th pass, then to
// every drainIdleBackoffDeepEvery-th pass past drainIdleBackoffDeepAfter. The
// watchdog must stay unreachable throughout (live residue suppresses it), so a
// missing kick shows up as a kick-count shortfall, not a verdict change.
func TestDrainIdleBackoffKickCadence(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	// Live, never-settling residue: outstanding forever, never stalled, so the
	// loop parks in waitDrainWake after every pass until the test's recheck
	// tick releases it.
	seedWatchSendResidue(t, sess)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recheck := make(chan time.Time)
	var kicks atomic.Int32
	kick := func(context.Context) error {
		kicks.Add(1)
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = sess.drainJobTreeWith(ctx, recheck, kick, stallProcess)
	}()

	// Drive exactly drainIdleBackoffDeepAfter+2*drainIdleBackoffDeepEvery quiet
	// passes and count the kicks the schedule allowed.
	const passes = drainIdleBackoffDeepAfter + 2*drainIdleBackoffDeepEvery
	for i := 0; i < passes; i++ {
		select {
		case recheck <- time.Now():
		case <-done:
			t.Fatalf("drain returned after %d quiet passes; want it to keep waiting on live residue", i)
		}
		// Let the released pass run its kick (or skip it) and park again.
		// A skipped kick parks immediately; an executed one still returns at
		// once (the test kick never blocks). Poll for the park by feeding the
		// next tick only once this one is consumed: the send above blocks
		// until the loop receives, which happens after the kick gate ran.
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("drain did not return after context cancel")
	}

	// Expected kicks: full-rate prefix (one per pass) + slow tier (passes
	// 16..47 kick when quietPasses%4==0) + deep tier (passes 48..87 kick when
	// quietPasses%20==0). quietPasses equals the pass index (0-based) at the
	// gate, so expected = 16 + (passes 16..47 with i%4==0) + (passes 48..87
	// with i%20==0).
	want := int32(drainIdleBackoffFullRatePasses)
	for i := drainIdleBackoffFullRatePasses; i < passes; i++ {
		every := drainIdleBackoffSlowEvery
		if i >= drainIdleBackoffDeepAfter {
			every = drainIdleBackoffDeepEvery
		}
		if i%every == 0 {
			want++
		}
	}
	if got := kicks.Load(); got != want {
		t.Fatalf("kicks over %d quiet passes = %d, want %d (full-rate %d + throttled cadence)", passes, got, want, drainIdleBackoffFullRatePasses)
	}
}

// TestDrainIdleBackoffWakeResetsToFullRate pins the other half of the schedule:
// a wake edge (new work arriving mid-backoff) returns the loop to full-rate
// kicks immediately instead of letting a completion wait out the throttle.
func TestDrainIdleBackoffWakeResetsToFullRate(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	seedWatchSendResidue(t, sess)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recheck := make(chan time.Time)
	var kicks atomic.Int32
	kick := func(context.Context) error {
		kicks.Add(1)
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = sess.drainJobTreeWith(ctx, recheck, kick, stallProcess)
	}()

	// Push past the full-rate prefix into the throttled tier.
	for i := 0; i < drainIdleBackoffFullRatePasses+2; i++ {
		select {
		case recheck <- time.Now():
		case <-done:
			t.Fatalf("drain returned during backoff entry (pass %d)", i)
		}
	}
	before := kicks.Load()
	if before != int32(drainIdleBackoffFullRatePasses)+1 {
		t.Fatalf("kicks entering backoff = %d, want %d (prefix full-rate plus first slow-tier kick)", before, drainIdleBackoffFullRatePasses+1)
	}

	// New work: a wake edge. The next pass must kick despite the backoff.
	sess.notify()
	select {
	case recheck <- time.Now():
	case <-done:
		t.Fatal("drain returned after wake; want another pass")
	}
	// The wake pass runs a notification turn only if something is queued; a
	// bare notify edge resets the streak at the top of the loop regardless.
	// Give the loop one more tick so the post-wake pass's kick is observable.
	select {
	case recheck <- time.Now():
	case <-done:
		t.Fatal("drain returned after post-wake tick")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("drain did not return after context cancel")
	}
	if got := kicks.Load(); got < before+1 {
		t.Fatalf("kicks after wake = %d, want at least %d: a wake must return the loop to full-rate kicks", got, before+1)
	}
}

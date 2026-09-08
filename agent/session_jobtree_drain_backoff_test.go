package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
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
	for range passes {
		select {
		case recheck <- time.Now():
		case <-done:
			t.Fatal("drain returned early; want it to keep waiting on live residue")
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
	// TRIPWIRE: awaits done, drainJobTreeWith's own return signal; cancel()
	// above guarantees it. 30s only fires on a genuine hang.
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
	for range drainIdleBackoffFullRatePasses + 2 {
		select {
		case recheck <- time.Now():
		case <-done:
			t.Fatal("drain returned during backoff entry")
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
	// TRIPWIRE: awaits done, drainJobTreeWith's own return signal; cancel()
	// above guarantees it. 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("drain did not return after context cancel")
	}
	if got := kicks.Load(); got < before+1 {
		t.Fatalf("kicks after wake = %d, want at least %d: a wake must return the loop to full-rate kicks", got, before+1)
	}
}

// TestDrainBackoffFinalKickDeliversUnreachableChildPending is the regression
// test for the backoff's skipped-kick hole: the quiescence scan
// (treeHasOutstandingWork) deliberately excludes forwarded terminal/watch
// records for unreachable children, and those surface only via
// renderUnreachableChildPendings inside the kick (kickDriveTree ->
// drainPendingWatchSends -> driveChildrenWithUndeliveredAttention). A pass
// that skipped its kick could therefore return quiescent while such work
// stayed undelivered. The fix runs one final unthrottled kick on the pass
// that would return quiescent, then re-scans.
//
// The fixture pairs that work with long-lived outstanding residue
// (seedWatchSendResidue): the residue holds the drain open through enough
// quiet passes to reach a throttled (skipped-kick) pass, then goes away —
// and the forwarded pending for the unreachable child is already seeded by
// then. The next pass scans quiescent (the scan excludes forwarded copies by
// design) with its kick skipped: without the fix it returns early with the
// work undelivered; with the fix the final kick renders it and the loop runs
// exactly one notification turn for it.
func TestDrainBackoffFinalKickDeliversUnreachableChildPending(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	seedWatchSendResidue(t, sess)
	jm := sess.jobManager
	if outstanding, err := sess.treeHasOutstandingWork(); err != nil || !outstanding {
		t.Fatalf("precondition: residue must hold the drain open, got outstanding=%v err=%v", outstanding, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recheck := make(chan time.Time)
	var kicks atomic.Int32
	var turns atomic.Int32
	kick := func(ctx context.Context) error {
		kicks.Add(1)
		return sess.kickDriveTree(ctx)
	}
	// The process stub consumes the rendered pending (as a notification
	// turn's accept path would) and settles its durable copy, so the pass
	// after the final kick sees genuine quiescence.
	process := func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		turns.Add(1)
		for _, n := range sess.drainJobNotifications() {
			if n.TerminalGen == "" {
				continue
			}
			if err := jm.appendEvent(jobstore.Event{
				Kind: jobstore.EventJobNotificationDelivered, TS: jm.now(),
				JobID: n.JobID, TerminalGen: n.TerminalGen,
			}); err != nil {
				t.Errorf("settle rendered pending: %v", err)
			}
		}
		return "", nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = sess.drainJobTreeWith(ctx, recheck, kick, process)
	}()

	// Phase 1: drive passes 0..drainIdleBackoffFullRatePasses (17 sends).
	// Pass 16 is the last full-rate pass; pass 17 (quietPasses=17, 17%4!=0)
	// is the first pass that SKIPS its kick. Each send blocks until the
	// loop receives it in waitDrainWake at the end of a pass, so the send
	// count equals the completed pass count and the quiet streak is exact.
	for range drainIdleBackoffFullRatePasses + 1 {
		select {
		case recheck <- time.Now():
		case <-done:
			t.Fatal("drain returned before reaching a skipped-kick pass")
		}
	}
	// Phase 2: while the loop is parked awaiting pass 17, seed a forwarded
	// terminal pending owned by a child that is nowhere in the live tree —
	// not a live direct subagent, no delegate descriptor, so childResumable
	// is false and the next kick renders it onto this session's rail — and
	// clear the residue. Both are pure store/flag writes (appendEvent is
	// store.Append: no enqueue, no wake), so the wake edge stays clear and
	// pass 17 still skips its kick deterministically.
	const childID = "child-gone"
	now := jm.now()
	gen := "gen-job_unreachable_tail"
	for _, event := range []jobstore.Event{
		{
			Kind: jobstore.EventJobStarted, TS: now, JobID: "job_unreachable_tail",
			Type: jobstore.JobShell, OwnerSessionID: childID, VisibleToSession: jm.sessionID,
			StartedAt: &now, Command: "true", Description: "unreachable child shell",
		},
		{Kind: jobstore.EventJobFinished, TS: now, JobID: "job_unreachable_tail", Status: jobstore.StatusCompleted, Reason: "completed", EndedAt: &now, TerminalGen: gen},
		{Kind: jobstore.EventJobNotificationPending, TS: now, JobID: "job_unreachable_tail", TerminalGen: gen},
	} {
		if err := jm.appendEvent(event); err != nil {
			t.Fatalf("seed unreachable-child pending: %v", err)
		}
	}
	jm.mu.Lock()
	jm.stableWatchSettlementRetrying = false
	jm.mu.Unlock()
	// The scan excludes the forwarded copy by design, so the tree now reads
	// quiescent apart from the kick-only render — the hole under test.
	if outstanding, err := sess.treeHasOutstandingWork(); err != nil || outstanding {
		t.Fatalf("precondition: tree must read quiescent after residue clear, got outstanding=%v err=%v", outstanding, err)
	}
	// Phase 3: release pass 17. Without the fix it returns quiescent with
	// the pending undelivered (turns stays 0); with the fix the final kick
	// renders it and the loop runs exactly one notification turn.
	select {
	case recheck <- time.Now():
	case <-done:
		t.Fatal("drain returned without running its skipped-kick pass")
	}
	deadline := time.Now().Add(30 * time.Second)
	for turns.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("notification turns = %d, want 1: the final kick must surface the unreachable child's pending before the drain may return", turns.Load())
		}
		select {
		case recheck <- time.Now():
		case <-done:
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
		// TRIPWIRE: awaits done, drainJobTreeWith's own return signal; cancel()
		// above guarantees it. 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("drain did not return after context cancel")
	}
	if got := turns.Load(); got != 1 {
		t.Fatalf("notification turns = %d, want exactly 1", got)
	}
	if got := sess.peekNotifications(); got != 0 {
		t.Fatalf("queued notifications after drain = %d, want 0: the turn must have consumed the render", got)
	}
}

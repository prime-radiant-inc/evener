package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// waitDrainBackoffKicks blocks until the drain's kick stub has run want times
// (failing if the drain returns first or the deadline passes). A recheck send
// completing proves only that waitDrainWake consumed the tick, not that the
// released pass ran its kick gate — so tests must synchronize on this
// observable count before asserting or cancelling, never straight after the
// send loop.
func waitDrainBackoffKicks(t *testing.T, done <-chan struct{}, kicks *atomic.Int32, want int32, what string) {
	t.Helper()
	// TRIPWIRE: awaits the kick count, the test's real completion signal;
	// 30s only fires on a genuine hang.
	deadline := time.Now().Add(30 * time.Second)
	for kicks.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%s: kicks = %d, want at least %d after 30s: the loop never ran its kick gate", what, kicks.Load(), want)
		}
		select {
		case <-done:
			t.Fatalf("%s: drain returned early with kicks = %d, want at least %d", what, kicks.Load(), want)
		// TRIPWIRE: not a completion-signal wait — the kick count above is the
		// signal; 10ms only yields instead of busy-spinning.
		case <-time.After(10 * time.Millisecond):
		}
	}
}

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
	for range passes {
		select {
		case recheck <- time.Now():
		case <-done:
			t.Fatal("drain returned early; want it to keep waiting on live residue")
		}
		// Each send releases exactly one parked pass and the next send blocks
		// until that pass parks again, so the send count equals the completed
		// pass count. A completed send proves only that the tick was consumed,
		// not that the released pass ran its kick gate — the final count is
		// awaited on kicks below instead of assumed here.
	}
	// Synchronize on the observable kick count before cancelling: asserting
	// straight after the send loop can miss the last pass's kick gate.
	waitDrainBackoffKicks(t, done, &kicks, want, "kick cadence")
	cancel()
	select {
	case <-done:
	// TRIPWIRE: awaits done, drainJobTreeWith's own return signal; cancel()
	// above guarantees it. 30s only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("drain did not return after context cancel")
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

	// Push past the full-rate prefix into the throttled tier. Each send
	// releases one parked pass, so the send count equals the completed pass
	// count — but a completed send proves only that the tick was consumed, not
	// that the released pass ran its kick gate. Wait for the observable count
	// before reading it.
	wantEntering := int32(drainIdleBackoffFullRatePasses) + 1
	for range drainIdleBackoffFullRatePasses + 2 {
		select {
		case recheck <- time.Now():
		case <-done:
			t.Fatal("drain returned during backoff entry")
		}
	}
	waitDrainBackoffKicks(t, done, &kicks, wantEntering, "backoff entry")
	before := kicks.Load()
	if before != wantEntering {
		t.Fatalf("kicks entering backoff = %d, want %d (prefix full-rate plus first slow-tier kick)", before, wantEntering)
	}

	// New work: a wake edge. Fire it with no recheck in flight — the wake
	// channel is buffered, so the edge waits until the loop's next park (or
	// its next top-edge read) instead of racing a concurrent recheck send in
	// waitDrainWake's select. The next pass must kick despite the backoff.
	sess.notify()
	// Release further passes until the wake-triggered full-rate kick is
	// observable. A released pass either consumes the edge at the top (kicking
	// at full rate) or parks into it and carries it to the next pass, so the
	// edge cannot be lost; the deadline only guards a genuine hang.
	wakeDeadline := time.Now().Add(30 * time.Second)
	for kicks.Load() < before+1 {
		if time.Now().After(wakeDeadline) {
			t.Fatalf("kicks after wake = %d, want at least %d: a wake must return the loop to full-rate kicks", kicks.Load(), before+1)
		}
		select {
		case recheck <- time.Now():
		case <-done:
			t.Fatal("drain returned after wake; want another pass")
		// TRIPWIRE: not a completion-signal wait — kicks reaching before+1 is
		// the signal (checked above); 10ms only yields while a pass runs with
		// the recheck send unconsumed instead of busy-spinning.
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
	// The drain runs its first pass (pass 0) before parking, so 17 sends
	// release passes 1..17: passes 0..16 kick at full rate (17 kicks) and
	// pass 17 (quietPasses=17, 17%4!=0) is the first pass that SKIPS its
	// kick. A completed send proves only that the tick was consumed, not
	// that the released pass ran its kick gate — so the streak below is
	// pinned on the observable kick count, not the send count.
	for range drainIdleBackoffFullRatePasses + 1 {
		select {
		case recheck <- time.Now():
		case <-done:
			t.Fatal("drain returned before reaching a skipped-kick pass")
		}
	}
	// Await the 17 full-rate kicks before seeding: this proves passes 0..16
	// all ran their kick gates, so the loop is parked awaiting pass 17 (or
	// running it) and Phase 2's seeding cannot land in an earlier pass's
	// scan. Without the fix this wait still terminates — the prefix always
	// kicks — and the turn assertion below still fails.
	waitDrainBackoffKicks(t, done, &kicks, int32(drainIdleBackoffFullRatePasses)+1, "full-rate prefix")
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
	// quiescent apart from the kick-only render — the hole under test. Poll
	// read-only (no recheck ticks: every tick releases another parked pass
	// and disturbs the kick count asserted at the tail) until the quiet scan
	// is observable. A quiet scan here cannot be a stale read racing pass
	// 17's own verdict: the loop is parked awaiting it, and the seeding
	// above raised no wake, so the next pass still skips its kick.
	quietDeadline := time.Now().Add(30 * time.Second)
	for {
		outstanding, err := sess.treeHasOutstandingWork()
		if err != nil {
			t.Fatalf("quiescence poll: %v", err)
		}
		if !outstanding {
			break
		}
		if time.Now().After(quietDeadline) {
			t.Fatalf("precondition: tree must read quiescent after residue clear, got outstanding=true after 30s")
		}
		// TRIPWIRE: not a completion-signal wait — a quiet scan above is
		// the signal; 10ms only yields instead of busy-spinning.
		select {
		case <-done:
			t.Fatalf("drain returned during quiescence poll with turns = %d, want the skipped-kick pass to run first", turns.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Phase 3: release pass 17. Without the fix it returns quiescent with
	// the pending undelivered (turns stays 0 and done closes); with the fix
	// the final kick renders it onto the rail and the loop runs exactly one
	// notification turn for it.
	//
	// The render itself raises no wake (enqueueJobNotification queues
	// without notifying), so after a skipped pass's re-kick the loop parks
	// with the work queued but no edge set. A recheck tick is what releases
	// the confirming pass that runs the turn — and a test tick sent while
	// the loop is still inside the re-kick waits for that park instead of
	// racing it (the recheck channel is unbuffered). Keep sending until the
	// turn is observable; the turn count, not the send count, is the signal.
	// If done closes first with turns==0, that IS the bug under test: the
	// drain returned quiescent on a skipped kick without rendering.
	deadline := time.Now().Add(30 * time.Second)
	for turns.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("notification turns = %d, want 1: the final kick must surface the unreachable child's pending before the drain may return", turns.Load())
		}
		select {
		case recheck <- time.Now():
		case <-done:
			if turns.Load() != 1 {
				t.Fatalf("drain returned quiescent with notification turns = %d, want 1: a throttled pass stranded the unreachable child's pending", turns.Load())
			}
		// TRIPWIRE: not a completion-signal wait -- this is the poll tick inside
		// a loop whose real completion signal is turns==1 (the 30s deadline
		// above is the hang guard). 10ms only yields while the recheck send
		// is unconsumed instead of busy-spinning; it is not a budget for
		// work to finish.
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
	// At least the full-rate prefix (17) plus the skipped pass's re-kick ran:
	// without the fix the drain returns quiescent with turns==0 and exactly
	// the prefix count, so a shortfall here and the turn assertion above both
	// fail. No exact upper bound: release ticks sent while the loop parks
	// each run one more pass, so the confirming turn pass and any further
	// re-kicks only add kicks past this floor.
	if got, want := kicks.Load(), int32(drainIdleBackoffFullRatePasses)+2; got < want {
		t.Fatalf("kicks = %d, want at least %d (full-rate prefix plus the skipped pass's re-kick)", got, want)
	}
}

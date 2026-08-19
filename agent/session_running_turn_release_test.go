package agent

import (
	"errors"
	"fmt"
	"testing"
)

// TestReleaseRunningTurnIDRecoversFromARefusedWrite is kata fbmy's strand. A
// release the client mutation store refuses leaves ActiveTurnID naming a turn
// that is already dead, and every later turn on the session is refused against
// it: mintRunningTurnID answers turnNameHeld and AcceptClientMutationStart
// answers Conflict("turn is already active"). Nothing outside a daemon restart
// clears it — forgetRunningTurnNoOneOwns runs at load and nowhere else — so a
// single failed write wedges the session for the life of the process.
//
// The recovery has to re-attempt the WRITE. Release runs once, in the unwind of
// the turn that held the name, and nothing re-enters it: waking the serve loop
// (the acquire side's answer, kata ajg5) re-runs mint, never release.
func TestReleaseRunningTurnIDRecoversFromARefusedWrite(t *testing.T) {
	h := newStandDownHarness(t)
	turnID, refusal := h.sess.mintRunningTurnID()
	if refusal != turnNameMinted || turnID == "" {
		t.Fatalf("mintRunningTurnID = (%q, %v), want a name for this turn to release", turnID, refusal)
	}
	h.failWrites(t)

	timersBefore := h.clock.BlockedCount()
	h.sess.releaseRunningTurnID(turnID)

	if got := h.sess.clientMutations.snapshot().ActiveTurnID; got != turnID {
		t.Fatalf("ActiveTurnID = %q after a refused release, want the name %q still held: the strand is this test's premise", got, turnID)
	}
	if got := h.clock.BlockedCount(); got != timersBefore+1 {
		t.Fatalf("a refused release write armed %d retries, want the baseline %d plus exactly one: nothing else re-attempts the write, and the name it left behind refuses every later turn",
			got, timersBefore)
	}

	// The store recovers, and the retry lands the write it lost.
	h.sess.clientMutations.faults.BeforeEffectSnapshotRename = nil
	h.clock.Advance(jobNotificationRetryInitialDelay)
	<-h.woken

	if got := h.sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q once the store took writes again, want the retry to have cleared it", got)
	}
	// The acceptance the kata asks for: a later turn on this session, named
	// from live state with no reload in between.
	next, refusal := h.sess.mintRunningTurnID()
	if refusal != turnNameMinted || next == "" {
		t.Fatalf("the turn after the failed release was named (%q, %v), want a fresh name: the session is still wedged", next, refusal)
	}
}

// TestReleaseRunningTurnIDGivesUpLoudlyWhenTheStoreNeverRecovers bounds the
// other end of the retry. A state dir that will never be writable — a
// read-only mount, a full disk, a deleted directory — cannot hand the name
// back at any pace, and a session whose name can never be released is a
// session that can never start another turn. Retrying that forever hides it
// behind a timer; saying so once and stopping is the honest failure.
func TestReleaseRunningTurnIDGivesUpLoudlyWhenTheStoreNeverRecovers(t *testing.T) {
	h := newStandDownHarness(t)
	turnID, refusal := h.sess.mintRunningTurnID()
	if refusal != turnNameMinted || turnID == "" {
		t.Fatalf("mintRunningTurnID = (%q, %v), want a name for this turn to release", turnID, refusal)
	}

	// Every durable write announces itself, which is how this test paces the
	// retries: each re-attempt runs on the clock's own callback goroutine, and
	// the next timer is armed only after the write it just lost.
	injected := errors.New("injected client mutation write failure")
	writes := make(chan struct{}, 64)
	h.sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		writes <- struct{}{}
		return injected
	}

	timersBefore := h.clock.BlockedCount()
	if got := h.sess.releaseRunningTurnID(turnID); got != turnNameReleaseStoreFailed {
		t.Fatalf("release against a store refusing writes = %v, want turnNameReleaseStoreFailed", got)
	}
	<-writes
	if got := h.clock.BlockedCount(); got != timersBefore+1 {
		t.Fatalf("a refused release write armed %d retries, want the baseline %d plus exactly one", got, timersBefore)
	}

	for range runningTurnReleaseRetryLimit {
		h.clock.BlockUntil(timersBefore + 1)
		h.clock.Advance(jobNotificationRetryMaxDelay)
		<-writes
	}

	h.awaitWarning(t, "could not be released")

	if got := h.clock.BlockedCount(); got != timersBefore {
		t.Fatalf("armed timers after the budget ran out = %d, want the baseline %d: a name that can never be released must stop retrying, not retry forever",
			got, timersBefore)
	}
	if got := len(writes); got != 0 {
		t.Fatalf("%d release writes beyond the budget of %d attempts", got, runningTurnReleaseRetryLimit)
	}
	if got := h.warningsMatching("could not be released"); got != 1 {
		t.Fatalf("terminal release warnings = %d, want exactly 1: exhausting the budget is one event, and it fires the user's Notification hook", got)
	}
	// The message has to report what actually happened (the real attempt
	// count, not a placeholder) and name the daemon reload -- the only thing
	// that clears a stranded name -- rather than a "session restart", which
	// isn't a real operation on a session.
	if got := h.warningsMatching(fmt.Sprintf("could not be released after %d attempts", runningTurnReleaseRetryLimit)); got != 1 {
		t.Fatalf("terminal warning with the true attempt count %d = %d matches, want exactly 1", runningTurnReleaseRetryLimit, got)
	}
	if got := h.warningsMatching("until it restarts"); got != 0 {
		t.Fatalf("terminal warning still claims a bare session restart clears the name; only the daemon reloading (forgetRunningTurnNoOneOwns, at load) does")
	}
	if got := h.warningsMatching("daemon"); got != 1 {
		t.Fatalf("terminal warning does not mention the daemon reload/load path that actually clears the stranded name")
	}
	// The per-failure diagnostic is latched to the store's unhealthy episode,
	// like mint's. Warning per re-attempt would cost a hook subprocess apiece.
	if got := h.warningsMatching("release running turn failed"); got != 1 {
		t.Fatalf("store-failure warnings across %d re-attempts = %d, want 1: one unhealthy episode is one diagnostic",
			runningTurnReleaseRetryLimit, got)
	}
	// The name is still held, and the session says so rather than pretending
	// otherwise: nothing but a restart clears a write the store never took.
	if got := h.sess.clientMutations.snapshot().ActiveTurnID; got != turnID {
		t.Fatalf("ActiveTurnID = %q, want the unreleasable %q: the in-memory slot must not diverge from what the store holds", got, turnID)
	}
}

// TestReleaseRunningTurnIDResetsRetryBudgetOnExhaustion is issue #150's first
// residual. The exhaustion branch used to leave runningTurnReleaseRetry at its
// limit, so a later episode -- the slot cleared by the interrupt path (or any
// path that lands a write), then a new turn's release hits a failing store --
// inherited the prior episode's exhaustion: zero retries and an immediate
// terminal warning whose text was then inaccurate. The exhaustion branch now
// resets the budget, so the next episode gets its full retry count.
//
// Deleting the reset makes this test red: the second episode arms no retry and
// fires the terminal warning immediately.
func TestReleaseRunningTurnIDResetsRetryBudgetOnExhaustion(t *testing.T) {
	h := newStandDownHarness(t)
	turnID, refusal := h.sess.mintRunningTurnID()
	if refusal != turnNameMinted || turnID == "" {
		t.Fatalf("mintRunningTurnID = (%q, %v), want a name for this turn to release", turnID, refusal)
	}

	// Episode 1: exhaust the budget against a store that never recovers.
	injected := errors.New("injected client mutation write failure")
	writes := make(chan struct{}, 64)
	h.sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		writes <- struct{}{}
		return injected
	}
	timersBefore := h.clock.BlockedCount()
	if got := h.sess.releaseRunningTurnID(turnID); got != turnNameReleaseStoreFailed {
		t.Fatalf("release against a store refusing writes = %v, want turnNameReleaseStoreFailed", got)
	}
	<-writes
	for range runningTurnReleaseRetryLimit {
		h.clock.BlockUntil(timersBefore + 1)
		h.clock.Advance(jobNotificationRetryMaxDelay)
		<-writes
	}
	h.awaitWarning(t, "could not be released")
	if got := h.warningsMatching("could not be released"); got != 1 {
		t.Fatalf("episode 1 terminal warnings = %d, want 1", got)
	}
	if got := h.clock.BlockedCount(); got != timersBefore {
		t.Fatalf("armed timers after episode 1 exhaustion = %d, want the baseline %d", got, timersBefore)
	}

	// The store recovers, and the slot is cleared -- the interrupt path lands a
	// write that drops ActiveTurnID, the same thing forgetRunningTurnNoOneOwns
	// does at load. Modelled directly: the point is the slot is free again.
	h.sess.clientMutations.faults.BeforeEffectSnapshotRename = nil
	if err := h.sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.ActiveTurnID = ""
		return nil
	}); err != nil {
		t.Fatalf("clear the stranded slot after the store recovered: %v", err)
	}

	// A new turn is named from the now-free slot.
	next, refusal := h.sess.mintRunningTurnID()
	if refusal != turnNameMinted || next == "" {
		t.Fatalf("mintRunningTurnID after the slot cleared = (%q, %v), want a fresh name", next, refusal)
	}

	// Episode 2: the store fails again, and the new turn's release hits it.
	h.sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error { return injected }

	timersBefore2 := h.clock.BlockedCount()
	if got := h.sess.releaseRunningTurnID(next); got != turnNameReleaseStoreFailed {
		t.Fatalf("episode 2 release against a failing store = %v, want turnNameReleaseStoreFailed", got)
	}

	// The reset is what this test is for: episode 2 gets its full retry budget,
	// not the zero retries of an inherited exhaustion. A single retry is armed
	// (the baseline plus one), and no terminal warning fires yet -- the budget
	// is nowhere near spent.
	if got := h.clock.BlockedCount(); got != timersBefore2+1 {
		t.Fatalf("episode 2 armed %d retries, want the baseline %d plus exactly one: the budget was not reset after episode 1's exhaustion, so a later release inherited a spent budget and got zero retries",
			got, timersBefore2)
	}
	if got := h.warningsMatching("could not be released"); got != 1 {
		t.Fatalf("terminal warnings after episode 2's first failure = %d, want 1 (only episode 1's): an unreset budget emits the exhaustion warning immediately on the first failure of the next episode",
			got)
	}
}

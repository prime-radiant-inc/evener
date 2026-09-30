package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// A start or restore that fails for a transient reason (the target was busy,
// retirement closed admission) is retried as it stands; any other failure is
// permanent for the purposes of giving up. The attention give-up is its one
// caller today.
func TestIsTransientStartFailure(t *testing.T) {
	t.Parallel()
	for _, err := range []error{errDelegateTargetBusy, ErrRetirementUnavailable, fmt.Errorf("restore: %w", errDelegateTargetBusy)} {
		if !isTransientStartFailure(err) {
			t.Errorf("isTransientStartFailure(%v) = false, want true", err)
		}
	}
	for _, err := range []error{nil, errors.New("load committed delegate session metadata: no such file"), errDelegateNotControllable} {
		if isTransientStartFailure(err) {
			t.Errorf("isTransientStartFailure(%v) = true, want false", err)
		}
	}
}

// An unfenced grandchild owes attention, but its cold restore can never
// succeed: its session metadata is gone. After the give-up limit of counted
// failures the drive stops retrying it and hands the attention to the root,
// the way fenced attention is escalated: the root holds the original message
// under its original identity, and the source is resolved.
func TestUnrestorableDelegateAttentionIsHandedToTheRoot(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root, fixture := fenced.root, fenced.fixture
	root.cfg.testOnly.delegateAttentionGiveUpAfter = 3
	if err := os.Remove(filepath.Join(fixture.stateDir, sessionsSubdir, fenced.grandchildSessionID+".meta.json")); err != nil {
		t.Fatalf("remove grandchild session meta: %v", err)
	}

	for range 3 {
		root.drivePendingStableDelegateAttention()
	}

	rootFold, err := readDelegateAttentionFold(transcriptPath(fixture.stateDir, fixture.meta.ID), fixture.meta.ID)
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if got := rootFold.content[fenced.attentionID].Text(); got != "undelivered grandchild message" {
		t.Fatalf("root attention content = %q, want the grandchild's original message handed over", got)
	}
	grandchildFold, err := readDelegateAttentionFold(transcriptPath(fixture.stateDir, fenced.grandchildSessionID), fenced.grandchildSessionID)
	if err != nil {
		t.Fatalf("read grandchild attention fold: %v", err)
	}
	if got := grandchildFold.resolutions[fenced.attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("grandchild attention resolution = %q, want discarded", got)
	}
}

// Before the limit, a counted failure keeps retrying: nothing is handed over.
func TestDelegateAttentionIsNotHandedOverBeforeTheGiveUpLimit(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root, fixture := fenced.root, fenced.fixture
	root.cfg.testOnly.delegateAttentionGiveUpAfter = 3
	if err := os.Remove(filepath.Join(fixture.stateDir, sessionsSubdir, fenced.grandchildSessionID+".meta.json")); err != nil {
		t.Fatalf("remove grandchild session meta: %v", err)
	}

	for range 2 {
		root.drivePendingStableDelegateAttention()
	}

	rootFold, err := readDelegateAttentionFold(transcriptPath(fixture.stateDir, fixture.meta.ID), fixture.meta.ID)
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if _, handed := rootFold.content[fenced.attentionID]; handed {
		t.Fatal("attention handed to the root before the give-up limit")
	}
}

// When the hand-over fails too (here the grandchild's transcript can't be
// read), the delegate is parked: it says so once, at every level, the loop
// stops retrying it, and new attention for it unparks it.
func TestUndeliverableDelegateAttentionParksUntilNewAttention(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root, fixture := fenced.root, fenced.fixture
	root.cfg.testOnly.delegateAttentionGiveUpAfter = 2
	if err := os.Remove(filepath.Join(fixture.stateDir, sessionsSubdir, fenced.grandchildSessionID+".meta.json")); err != nil {
		t.Fatalf("remove grandchild session meta: %v", err)
	}
	path := transcriptPath(fixture.stateDir, fenced.grandchildSessionID)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove grandchild transcript: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("replace grandchild transcript with a directory: %v", err)
	}
	eventsDone := captureSessionEvents(root)

	for range 4 {
		root.drivePendingStableDelegateAttention()
	}
	controller := root.delegateController
	if controller.hasPendingDelegateAttention() {
		t.Fatal("a parked delegate still reads as pending attention, so the retry keeps spinning")
	}
	if delegateID, _, pending := controller.nextIdleDelegateAttention(); pending {
		t.Fatalf("the drive still selects parked delegate %s", delegateID)
	}

	if _, err := controller.openDelegateAttention(fenced.grandchildDelegateID, "delegate:fresh-attention"); err != nil {
		t.Fatalf("open fresh attention: %v", err)
	}
	if delegateID, _, pending := controller.nextIdleDelegateAttention(); !pending || delegateID != fenced.grandchildDelegateID {
		t.Fatalf("after fresh attention: selected %q pending=%t, want the unparked grandchild", delegateID, pending)
	}
	root.Close()

	var parked []events.WarningData
	for _, warning := range warningEvents(<-eventsDone) {
		if warning.Code == events.WarningCodeDelegateAttentionUndeliverable {
			parked = append(parked, warning)
		}
	}
	if len(parked) != 1 {
		t.Fatalf("undeliverable warnings = %+v, want exactly one", parked)
	}
	// With several subagents stuck, each warning must say which one.
	if parked[0].DelegateID != fenced.grandchildDelegateID {
		t.Fatalf("undeliverable warning names delegate %q, want %q", parked[0].DelegateID, fenced.grandchildDelegateID)
	}
}

// A transient failure (the target was busy, retirement closed admission)
// clears on its own and never counts toward giving up.
func TestTransientRestoreFailuresDoNotCountTowardGivingUp(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root := fenced.root
	root.cfg.testOnly.delegateAttentionGiveUpAfter = 2
	for range 5 {
		root.countDelegateAttentionRestoreFailure(fenced.grandchildDelegateID, errDelegateTargetBusy)
		root.countDelegateAttentionRestoreFailure(fenced.grandchildDelegateID, ErrRetirementUnavailable)
	}
	c := root.delegateController
	c.mu.Lock()
	count := c.attentionRestoreFailures[fenced.grandchildDelegateID]
	_, parked := c.attentionParked[fenced.grandchildDelegateID]
	c.mu.Unlock()
	if count != 0 || parked {
		t.Fatalf("after ten transient failures: count=%d parked=%t, want nothing counted", count, parked)
	}
}

// A successful restore ends the run of counted failures.
func TestASuccessfulRestoreResetsTheFailureCount(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root := fenced.root
	c := root.delegateController
	c.recordDelegateAttentionRestoreFailure(fenced.grandchildDelegateID)
	c.recordDelegateAttentionRestoreFailure(fenced.grandchildDelegateID)

	root.drivePendingStableDelegateAttention()

	c.mu.Lock()
	count, counted := c.attentionRestoreFailures[fenced.grandchildDelegateID]
	c.mu.Unlock()
	if counted {
		t.Fatalf("after a successful restore the failure count is still %d, want it cleared", count)
	}
}

// A delegate that stops owing attention, whether its attention is forgotten
// or replaced wholesale from a transcript fold, leaves no parked state or
// failure count behind to shadow attention owed later.
func TestLeavingThePendingSetClearsParkedAndCountedState(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	for _, id := range []string{"dlg_forgotten", "dlg_replaced"} {
		seedDelegateControllerIdle(t, c, id, "")
		if !c.noteDelegateAttention(id, "delegate:"+id) {
			t.Fatalf("note %s attention", id)
		}
		c.recordDelegateAttentionRestoreFailure(id)
		c.parkDelegateAttention(id)
		c.recordDelegateAttentionRestoreFailure(id)
	}
	c.forgetDelegateAttention("dlg_forgotten", "delegate:dlg_forgotten")
	c.mu.Lock()
	c.replaceDelegateAttentionLocked("dlg_replaced", []string{"delegate:dlg_replaced-again"})
	for _, id := range []string{"dlg_forgotten", "dlg_replaced"} {
		if _, parked := c.attentionParked[id]; parked {
			t.Errorf("%s still parked after leaving the pending set", id)
		}
		if count := c.attentionRestoreFailures[id]; count != 0 {
			t.Errorf("%s still counts %d failures after leaving the pending set", id, count)
		}
	}
	c.mu.Unlock()
}

// A delegate whose transcript is gone has nothing to hand over: the
// attention must not be forgotten as though it had been delivered. Giving up
// parks it and says so.
func TestGivingUpOnAMissingTranscriptParksInsteadOfDropping(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root, fixture := fenced.root, fenced.fixture
	root.cfg.testOnly.delegateAttentionGiveUpAfter = 2
	for _, path := range []string{
		filepath.Join(fixture.stateDir, sessionsSubdir, fenced.grandchildSessionID+".meta.json"),
		transcriptPath(fixture.stateDir, fenced.grandchildSessionID),
	} {
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove %s: %v", path, err)
		}
	}
	eventsDone := captureSessionEvents(root)

	for range 2 {
		root.drivePendingStableDelegateAttention()
	}
	c := root.delegateController
	c.mu.Lock()
	_, parked := c.attentionParked[fenced.grandchildDelegateID]
	_, stillOwed := c.attentionWakeIDs[fenced.grandchildDelegateID][fenced.attentionID]
	c.mu.Unlock()
	root.Close()
	if !parked || !stillOwed {
		t.Fatalf("after giving up on a missing transcript: parked=%t owed=%t, want the attention kept and the delegate parked", parked, stillOwed)
	}
	undeliverable := 0
	for _, warning := range warningEvents(<-eventsDone) {
		if warning.Code == events.WarningCodeDelegateAttentionUndeliverable {
			undeliverable++
		}
	}
	if undeliverable != 1 {
		t.Fatalf("undeliverable warnings = %d, want one", undeliverable)
	}
}

// Overlapping passes that both give up on the same delegate park it once and
// say so once.
func TestParkingTwiceWarnsOnce(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root, fixture := fenced.root, fenced.fixture
	path := transcriptPath(fixture.stateDir, fenced.grandchildSessionID)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove grandchild transcript: %v", err)
	}
	eventsDone := captureSessionEvents(root)
	busy := errors.New("load committed delegate session metadata: gone")
	root.giveUpDelegateAttention(fenced.grandchildDelegateID, busy)
	root.giveUpDelegateAttention(fenced.grandchildDelegateID, busy)
	root.Close()
	undeliverable := 0
	for _, warning := range warningEvents(<-eventsDone) {
		if warning.Code == events.WarningCodeDelegateAttentionUndeliverable {
			undeliverable++
		}
	}
	if undeliverable != 1 {
		t.Fatalf("undeliverable warnings = %d, want one for the one park", undeliverable)
	}
}

// A nil error is no failure: it neither counts nor gives up.
func TestANilRestoreErrorIsNotCounted(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root := fenced.root
	root.cfg.testOnly.delegateAttentionGiveUpAfter = 1
	root.countDelegateAttentionRestoreFailure(fenced.grandchildDelegateID, nil)
	c := root.delegateController
	c.mu.Lock()
	count := c.attentionRestoreFailures[fenced.grandchildDelegateID]
	_, parked := c.attentionParked[fenced.grandchildDelegateID]
	c.mu.Unlock()
	if count != 0 || parked {
		t.Fatalf("after a nil error: count=%d parked=%t, want nothing recorded", count, parked)
	}
}

// A park stops the drive's cold restores; it does not strand attention a
// closed ancestor has fenced off for good. The fenced scan escalates a parked
// delegate too, and reads its transcript strictly: present, the message goes
// to the root and the park ends with the attention; gone, nothing is
// forgotten as never durable, and the delegate stays parked and owed.
func TestAParkedDelegateIsEscalatedStrictlyWhenItsAncestorCloses(t *testing.T) {
	closeParent := func(t *testing.T, fenced fencedGrandchildAttention) {
		t.Helper()
		root := fenced.root
		plans, err := root.delegateController.CloseResumability(rootDelegateActor(root.ID()), fenced.fixture.delegateID, "turn_budget_exhausted")
		if err != nil {
			t.Fatalf("close parent resumability: %v", err)
		}
		if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Fatalf("publish parent closure: %v", err)
		}
	}
	owedAndParked := func(fenced fencedGrandchildAttention) (owed, parked bool) {
		c := fenced.root.delegateController
		c.mu.Lock()
		defer c.mu.Unlock()
		_, owed = c.attentionWakeIDs[fenced.grandchildDelegateID][fenced.attentionID]
		_, parked = c.attentionParked[fenced.grandchildDelegateID]
		return owed, parked
	}

	t.Run("transcript present: handed to the root", func(t *testing.T) {
		fenced := newFencedGrandchildAttention(t)
		fenced.root.delegateController.parkDelegateAttention(fenced.grandchildDelegateID)
		closeParent(t, fenced)

		fenced.root.drivePendingStableDelegateAttention()

		rootFold, err := readDelegateAttentionFold(transcriptPath(fenced.fixture.stateDir, fenced.fixture.meta.ID), fenced.fixture.meta.ID)
		if err != nil {
			t.Fatalf("read root attention fold: %v", err)
		}
		if got := rootFold.content[fenced.attentionID].Text(); got != "undelivered grandchild message" {
			t.Fatalf("root attention content = %q, want the parked delegate's message escalated", got)
		}
		if owed, parked := owedAndParked(fenced); owed || parked {
			t.Fatalf("after escalation: owed=%t parked=%t, want neither", owed, parked)
		}
	})

	t.Run("transcript gone: stays parked and owed", func(t *testing.T) {
		fenced := newFencedGrandchildAttention(t)
		fenced.root.delegateController.parkDelegateAttention(fenced.grandchildDelegateID)
		closeParent(t, fenced)
		if err := os.Remove(transcriptPath(fenced.fixture.stateDir, fenced.grandchildSessionID)); err != nil {
			t.Fatalf("remove grandchild transcript: %v", err)
		}

		fenced.root.drivePendingStableDelegateAttention()

		if owed, parked := owedAndParked(fenced); !owed || !parked {
			t.Fatalf("after a failed strict escalation: owed=%t parked=%t, want the attention kept and parked", owed, parked)
		}
	})
}

// A park stops the drive's cold restores of a delegate whose runtime could
// not be restored or handed over. Once the delegate has a live runtime again
// (the user steered it, say), nothing about that failure stands in the way:
// its own runtime can take the owed attention, so the park must not refuse
// the reservation.
func TestAParkedDelegateWithALiveRuntimeCanReserveItsAttention(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_parked", "")
	const attentionID = "delegate:owed-before-the-park"
	if !c.noteDelegateAttention("dlg_parked", attentionID) {
		t.Fatal("note attention")
	}
	c.parkDelegateAttention("dlg_parked")
	runtime := &Session{}
	c.mu.Lock()
	c.live["dlg_parked"] = &delegateLiveState{runtime: runtime}
	c.mu.Unlock()

	if _, err := c.ReserveAttention(runtime, attentionID); err != nil {
		t.Fatalf("ReserveAttention for a parked delegate that is resident again = %v, want the reservation", err)
	}
}

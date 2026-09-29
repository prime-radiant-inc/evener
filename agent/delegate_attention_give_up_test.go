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
// permanent for the purposes of giving up. One predicate, shared by the
// attention give-up and the owed-start abandon path, so the two agree.
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
	c.countDelegateAttentionRestoreFailure(fenced.grandchildDelegateID)
	c.countDelegateAttentionRestoreFailure(fenced.grandchildDelegateID)

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
		c.countDelegateAttentionRestoreFailure(id)
		c.parkDelegateAttention(id)
		c.countDelegateAttentionRestoreFailure(id)
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

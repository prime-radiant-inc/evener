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

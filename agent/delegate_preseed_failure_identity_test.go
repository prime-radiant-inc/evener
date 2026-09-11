package agent

import (
	"context"
	"strings"
	"testing"
)

// preseedInput writes the delegate's opening entry before the run that
// executes it exists, so the id it mints belongs to a turn that has not
// started. Minting is not starting: every return after the mint can fail, and
// the runtime is then RETAINED without a launch
// (retainAdoptedWithoutLaunch), so a name adopted at mint time would stay on
// an idle child forever. activeTurnOwner prefers directTurnID, so the next
// thing that child publishes — an idle compaction fold, a recovery record —
// would be attributed to a turn that never ran.
func TestDelegatePreseedFailureLeavesNoExecutingTurnName(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	var childID string
	// Close the child's transcript writer just before the preseed appends, so
	// the preseed fails after the id has been minted. A closed writer takes
	// the append without reporting, so this lands on the strict readback —
	// the last of the returns that follow the mint, and the one furthest from
	// it.
	root.cfg.testOnly.delegateInitialInputAppend = func(child *Session) {
		childID = child.id
		child.mu.Lock()
		writer := child.transcript
		child.mu.Unlock()
		if writer == nil {
			t.Fatal("restored child transcript is unavailable")
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close child transcript: %v", err)
		}
	}

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "preseed that cannot persist", 0)
	if outcome.result.Err == nil {
		t.Fatal("preseed with a closed transcript reported success")
	}
	if !strings.Contains(outcome.result.Err.Error(), "child input transcript") {
		t.Fatalf("preseed failure = %v, want a failure from the preseed itself", outcome.result.Err)
	}
	if childID == "" {
		t.Fatal("preseed never reported the child session")
	}
	sub := root.subagents.get(childID)
	if sub == nil {
		t.Fatal("the failed runtime was not retained; this test needs the retained-without-launch path")
	}
	sub.mu.Lock()
	running, driving := sub.running, sub.driving
	child := sub.sess
	sub.mu.Unlock()
	if running || driving {
		t.Fatalf("child is running=%v driving=%v; the failed preseed should have launched no run", running, driving)
	}
	if child == nil {
		t.Fatal("retained runtime has no session")
	}
	child.mu.Lock()
	directTurnID := child.directTurnID
	child.mu.Unlock()
	if directTurnID != "" {
		t.Fatalf("failed preseed left the executing-turn name %q on an idle child; activeTurnOwner would attribute its next record to a turn that never ran", directTurnID)
	}
	if owner := child.activeTurnOwner(); owner != "" {
		t.Fatalf("idle child after a failed preseed owns turn %q", owner)
	}
}

package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// pickDelegateAttention runs one drive selection and releases its restore
// hold, returning the delegate picked.
func pickDelegateAttention(t *testing.T, c *delegateTreeController) string {
	t.Helper()
	id, _, pending := c.selectDelegateAttentionWake()
	if !pending {
		t.Fatal("no delegate selected")
	}
	c.releaseAttentionRestoreHold(id)
	return id
}

// Delegates owing attention take turns: the drive picks one it has not
// picked yet, else the one it picked longest ago, so a delegate whose
// restore keeps failing cannot hold every sibling's attention behind it.
func TestAFailingDelegateDoesNotStarveItsSiblings(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	for _, id := range []string{"dlg_a", "dlg_b", "dlg_c"} {
		seedDelegateControllerIdle(t, c, id, "")
		if !c.noteDelegateAttention(id, "delegate:"+id) {
			t.Fatalf("note %s attention", id)
		}
	}
	var picks []string
	for range 5 {
		picks = append(picks, pickDelegateAttention(t, c))
	}
	if want := []string{"dlg_a", "dlg_b", "dlg_c", "dlg_a", "dlg_b"}; !slices.Equal(picks, want) {
		t.Fatalf("picks = %v, want each delegate in turn %v", picks, want)
	}
	// A delegate that stops owing attention leaves the line; owing again, it
	// is new and goes first.
	c.forgetDelegateAttention("dlg_a", "delegate:dlg_a")
	if !c.noteDelegateAttention("dlg_a", "delegate:dlg_a-again") {
		t.Fatal("note dlg_a attention again")
	}
	if id, _, _ := c.nextIdleDelegateAttention(); id != "dlg_a" {
		t.Fatalf("after dlg_a owes afresh, next = %s, want dlg_a", id)
	}
}

// The drive's selection takes the delegate's turn, failed restore or not.
func TestAFailedRestoreTakesTheDelegatesTurn(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root, fixture := fenced.root, fenced.fixture
	if err := os.Remove(filepath.Join(fixture.stateDir, sessionsSubdir, fenced.grandchildSessionID+".meta.json")); err != nil {
		t.Fatalf("remove grandchild session meta: %v", err)
	}
	root.drivePendingStableDelegateAttention()
	c := root.delegateController
	c.mu.Lock()
	state := c.attention[fenced.grandchildDelegateID]
	taken := state != nil && state.driveTurn != 0
	c.mu.Unlock()
	if !taken {
		t.Fatal("a failed restore left the delegate at the front of the line")
	}
}

// Fairness can't depend on how a pass ends. A delegate whose restore
// succeeds but whose attention stays owed (its drive declined: the child was
// busy) would, if success put it back at the front, be picked every pass
// and starve a sibling whose restore failed, which then never reaches the
// give-up limit either. Every selection moves the picked delegate to the back
// of the line, so eligible delegates take turns whatever each pass did.
func TestEverySelectionTakesItsTurnSoNoDelegateMonopolizesTheDrive(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	for _, id := range []string{"dlg_a", "dlg_b"} {
		seedDelegateControllerIdle(t, c, id, "")
		if !c.noteDelegateAttention(id, "delegate:"+id) {
			t.Fatalf("note %s attention", id)
		}
	}
	var picks []string
	for range 4 {
		id := pickDelegateAttention(t, c)
		if id == "dlg_b" {
			// dlg_b restores fine but its attention stays owed.
			c.delegateAttentionRestored(id)
		}
		picks = append(picks, id)
	}
	if want := []string{"dlg_a", "dlg_b", "dlg_a", "dlg_b"}; !slices.Equal(picks, want) {
		t.Fatalf("picks = %v, want the two to alternate %v", picks, want)
	}
}

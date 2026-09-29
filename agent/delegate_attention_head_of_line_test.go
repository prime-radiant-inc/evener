package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// The drive always took the lowest-sorted eligible delegate, so one whose
// restore kept failing was picked again every pass and every sibling's
// attention waited behind it forever. A delegate whose restore failed goes
// to the back of the line: the drive picks a sibling that has not failed
// first, and among failed ones the one that failed longest ago, so every
// delegate keeps getting its turn.
func TestAFailingDelegateDoesNotStarveItsSiblings(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	for _, id := range []string{"dlg_a", "dlg_b", "dlg_c"} {
		seedDelegateControllerIdle(t, c, id, "")
		if !c.noteDelegateAttention(id, "delegate:"+id) {
			t.Fatalf("note %s attention", id)
		}
	}
	next := func() string {
		t.Helper()
		id, _, pending := c.nextIdleDelegateAttention()
		if !pending {
			t.Fatal("no delegate selected")
		}
		return id
	}
	if got := next(); got != "dlg_a" {
		t.Fatalf("first pick = %s, want dlg_a", got)
	}
	c.deferDelegateAttentionRestore("dlg_a")
	if got := next(); got != "dlg_b" {
		t.Fatalf("after dlg_a failed, pick = %s, want its sibling dlg_b", got)
	}
	c.deferDelegateAttentionRestore("dlg_b")
	c.deferDelegateAttentionRestore("dlg_c")
	if got := next(); got != "dlg_a" {
		t.Fatalf("with every delegate failed, pick = %s, want dlg_a, whose failure is oldest", got)
	}
	c.deferDelegateAttentionRestore("dlg_a")
	if got := next(); got != "dlg_b" {
		t.Fatalf("after dlg_a failed again, pick = %s, want dlg_b", got)
	}
	c.clearDelegateAttentionRestoreDeferral("dlg_c")
	if got := next(); got != "dlg_c" {
		t.Fatalf("after dlg_c restored, pick = %s, want dlg_c back at the front", got)
	}
}

// The drive puts a delegate whose restore failed at the back of the line.
func TestAFailedRestoreDefersTheDelegate(t *testing.T) {
	fenced := newFencedGrandchildAttention(t)
	root, fixture := fenced.root, fenced.fixture
	if err := os.Remove(filepath.Join(fixture.stateDir, sessionsSubdir, fenced.grandchildSessionID+".meta.json")); err != nil {
		t.Fatalf("remove grandchild session meta: %v", err)
	}
	root.drivePendingStableDelegateAttention()
	c := root.delegateController
	c.mu.Lock()
	_, deferred := c.attentionRestoreDeferred[fenced.grandchildDelegateID]
	c.mu.Unlock()
	if !deferred {
		t.Fatal("a failed restore left the delegate at the front of the line")
	}
}

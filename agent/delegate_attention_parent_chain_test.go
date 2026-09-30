package agent

import "testing"

// A child's cold restore first makes each non-resident ancestor resident, and
// it can do that only for an idle one (idleDelegateRestoreCommit). A parent
// that is running without a resident runtime (after a restart, until its
// generation is recovered) makes every restore of the child fail target_busy.
// The drive used to pick such a child anyway, pass after pass: selection
// checked that its ancestors were resumable and not stopping, and nothing
// about whether the chain could be restored. Now a child whose chain can't be
// made resident waits, not selected and not runnable, until its parent is
// resident or idle.
func TestAChildWaitsWhileItsParentIsRunningWithoutARuntime(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	if !c.noteDelegateAttention("dlg_child", "delegate:dlg_child") {
		t.Fatal("note child attention")
	}

	if delegateID, _, pending := c.nextIdleDelegateAttention(); pending {
		t.Fatalf("selected %s while its parent runs with no resident runtime; its restore can only fail", delegateID)
	}
	if c.hasRunnableDelegateAttention() {
		t.Fatal("attention under a non-resident running parent reads as runnable, so the drive keeps attempting it")
	}
	if !c.hasPendingDelegateAttention() {
		t.Fatal("the child's attention stopped reading as pending; the retry must keep waiting for the parent")
	}

	// Once the parent's runtime is resident, the child restores under it.
	c.mu.Lock()
	c.live["dlg_parent"].runtime = &Session{}
	c.mu.Unlock()
	if delegateID, _, pending := c.nextIdleDelegateAttention(); !pending || delegateID != "dlg_child" {
		t.Fatalf("with the parent resident: selected %q pending=%t, want dlg_child", delegateID, pending)
	}
}

// An idle, non-resident parent is no obstacle: the child's restore restores it
// first.
func TestAChildUnderAnIdleColdParentIsSelected(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	if !c.noteDelegateAttention("dlg_child", "delegate:dlg_child") {
		t.Fatal("note child attention")
	}
	if delegateID, _, pending := c.nextIdleDelegateAttention(); !pending || delegateID != "dlg_child" {
		t.Fatalf("under an idle cold parent: selected %q pending=%t, want dlg_child", delegateID, pending)
	}
}

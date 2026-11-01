package agent

import "testing"

// A child's cold restore first makes each non-resident ancestor resident, and
// it can do that only for an idle one (idleDelegateRestoreCommit). A parent
// that is running without a resident runtime (after a restart, until its
// generation is recovered) makes every restore of the child fail target_busy.
// The production ancestor gate must wait without losing pending membership.
func TestAChildWaitsWhileItsParentIsRunningWithoutARuntime(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerRunning(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	if !c.noteDelegateAttention("dlg_child", "delegate:dlg_child") {
		t.Fatal("note child attention")
	}

	c.mu.Lock()
	ready, closed := c.attentionWakeAncestorGateLocked("dlg_child")
	c.mu.Unlock()
	if ready || closed != "" {
		t.Fatalf("running cold parent gate: ready=%t closed=%q, want temporary refusal", ready, closed)
	}
	if !c.hasPendingDelegateAttention() {
		t.Fatal("the child's attention stopped reading as pending; the retry must keep waiting for the parent")
	}

	// Once the parent's runtime is resident, the child restores under it.
	c.mu.Lock()
	c.live["dlg_parent"].runtime = &Session{}
	ready, closed = c.attentionWakeAncestorGateLocked("dlg_child")
	c.mu.Unlock()
	if !ready || closed != "" {
		t.Fatalf("resident parent gate: ready=%t closed=%q", ready, closed)
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
	c.mu.Lock()
	ready, closed := c.attentionWakeAncestorGateLocked("dlg_child")
	c.mu.Unlock()
	if !ready || closed != "" {
		t.Fatalf("idle cold parent gate: ready=%t closed=%q", ready, closed)
	}
}

// The walk continues past an idle ancestor: an idle parent can be restored,
// but not under a grandparent that is running without a resident runtime, so
// the grandchild waits for the grandparent the same way. Once the
// grandparent is resident, the chain restores and the grandchild is selected.
func TestAGrandchildWaitsWhileItsGrandparentIsRunningWithoutARuntime(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerRunning(t, c, "dlg_grandparent", "")
	seedDelegateControllerIdle(t, c, "dlg_parent", "dlg_grandparent")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	if !c.noteDelegateAttention("dlg_child", "delegate:dlg_child") {
		t.Fatal("note child attention")
	}

	c.mu.Lock()
	ready, closed := c.attentionWakeAncestorGateLocked("dlg_child")
	c.mu.Unlock()
	if ready || closed != "" {
		t.Fatalf("running cold grandparent gate: ready=%t closed=%q, want temporary refusal", ready, closed)
	}
	if !c.hasPendingDelegateAttention() {
		t.Fatal("the child's attention stopped reading as pending; the retry must keep waiting")
	}

	c.mu.Lock()
	c.live["dlg_grandparent"].runtime = &Session{}
	ready, closed = c.attentionWakeAncestorGateLocked("dlg_child")
	c.mu.Unlock()
	if !ready || closed != "" {
		t.Fatalf("resident grandparent gate: ready=%t closed=%q", ready, closed)
	}
}

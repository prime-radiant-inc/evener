package agent

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// assertChildrenIndexMatchesDurable fails unless the controller's maintained
// children index equals a fresh derivation from its durable state: the index
// is a pure function of the journal, so any divergence is a maintenance gap.
func assertChildrenIndexMatchesDurable(t *testing.T, c *delegateTreeController) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	want := deriveDelegateChildrenIndex(c.durable)
	if !reflect.DeepEqual(c.delegateChildren, want) {
		t.Fatalf("children index diverged from the durable state:\n got %v\nwant %v", c.delegateChildren, want)
	}
}

// TestDelegateChildrenIndex_TracksDurableMutations drives the controller's
// real mutation paths and asserts the children index never diverges from the
// durable state: creation inserts edges, and no other event kind may
// disturb them.
func TestDelegateChildrenIndex_TracksDurableMutations(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	assertChildrenIndexMatchesDurable(t, c)

	seedDelegateControllerIdle(t, c, "dlg_root", "")
	assertChildrenIndexMatchesDurable(t, c)
	seedDelegateControllerRunning(t, c, "dlg_mid", "dlg_root")
	assertChildrenIndexMatchesDurable(t, c)
	seedDelegateReclaimRuntime(t, c, "dlg_leaf", "dlg_mid", time.Unix(10, 0).UTC(), true, true)
	assertChildrenIndexMatchesDurable(t, c)
	seedDelegateReclaimRuntime(t, c, "dlg_leaf2", "dlg_mid", time.Unix(20, 0).UTC(), false, false)
	assertChildrenIndexMatchesDurable(t, c)
	seedDelegateControllerRunning(t, c, "dlg_root2", "")
	assertChildrenIndexMatchesDurable(t, c)

	// A resumability close appends a different event kind; the index must be
	// untouched but still consistent.
	if _, err := c.CloseResumability(rootDelegateActor("root-session"), "dlg_root", "index-consistency"); err != nil {
		t.Fatalf("CloseResumability: %v", err)
	}
	assertChildrenIndexMatchesDurable(t, c)
}

// fixedPointSubtreeMembers is the pre-index membership algorithm, kept as the
// reference the children-index walk must agree with: one full durable scan
// per tree level until the member set stops growing.
func fixedPointSubtreeMembers(c *delegateTreeController, targetID string) map[string]struct{} {
	members := map[string]struct{}{targetID: {}}
	changed := true
	for changed {
		changed = false
		for id, aggregate := range c.durable {
			if aggregate == nil {
				continue
			}
			if _, included := members[id]; included {
				continue
			}
			if _, parentIncluded := members[aggregate.Descriptor.ParentDelegateID]; parentIncluded {
				members[id] = struct{}{}
				changed = true
			}
		}
	}
	return members
}

func assertSubtreeMembersMatchReference(t *testing.T, c *delegateTreeController, targetID string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	want := fixedPointSubtreeMembers(c, targetID)
	got := c.subtreeMembersLocked(targetID)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subtreeMembers(%s) = %v, want the fixed-point closure %v", targetID, got, want)
	}
}

// TestDelegateChildrenIndex_SubtreeMembersAgreeWithFixedPoint pins the
// index-driven membership walk against the scan-based reference across the
// seeded tree shapes: fan-out, chains, and a running delegate mid-tree.
func TestDelegateChildrenIndex_SubtreeMembersAgreeWithFixedPoint(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	seedDelegateControllerRunning(t, c, "dlg_root", "")
	endedAt := time.Unix(10, 0).UTC()
	seedDelegateReclaimRuntime(t, c, "dlg_fan0", "dlg_root", endedAt, true, true)
	seedDelegateReclaimRuntime(t, c, "dlg_fan1", "dlg_root", endedAt, true, true)
	seedDelegateReclaimRuntime(t, c, "dlg_link0", "dlg_root", endedAt, true, true)
	seedDelegateReclaimRuntime(t, c, "dlg_link1", "dlg_link0", endedAt, true, true)
	seedDelegateReclaimRuntime(t, c, "dlg_link2", "dlg_link1", endedAt, true, true)
	seedDelegateControllerRunning(t, c, "dlg_mid", "dlg_root")
	seedDelegateReclaimRuntime(t, c, "dlg_leaf", "dlg_mid", endedAt, true, true)
	for _, target := range []string{"dlg_root", "dlg_fan0", "dlg_fan1", "dlg_link0", "dlg_link1", "dlg_link2", "dlg_mid", "dlg_leaf"} {
		assertSubtreeMembersMatchReference(t, c, target)
	}
}

// TestDelegateChildrenIndex_HandlesCorruptJournals covers the state only a
// cold restore of a corrupt journal can build: a parent loop and a nil
// record. The constructor's derive is the only production path that sees
// such a durable state, and membership must stay cycle-safe and agree with
// the reference on it.
func TestDelegateChildrenIndex_HandlesCorruptJournals(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 8, 4)
	c.mu.Lock()
	c.durable = delegatestore.State{
		"dlg_a":    {Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_b"}},
		"dlg_b":    {Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_a"}},
		"dlg_c":    {Descriptor: delegatestore.Descriptor{ParentDelegateID: "dlg_a"}},
		"dlg_nil":  nil,
		"dlg_root": {Descriptor: delegatestore.Descriptor{}},
	}
	c.delegateChildren = deriveDelegateChildrenIndex(c.durable)
	c.mu.Unlock()
	for _, target := range []string{"dlg_a", "dlg_b", "dlg_c", "dlg_nil", "dlg_root"} {
		assertSubtreeMembersMatchReference(t, c, target)
	}
	// Root-level delegates contribute no edge even when their records exist.
	c.mu.Lock()
	if _, rooted := c.delegateChildren[""]; rooted {
		c.mu.Unlock()
		t.Fatalf("children index keyed a root edge: %v", c.delegateChildren[""])
	}
	c.mu.Unlock()
}

package agent

import (
	"slices"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// A subagent's open thread lists its subtree, so a change to a delegate below
// it has to reach that thread too: the grandchild's updates are emitted on the
// middle subagent's own stream as well as the root's, carrying the sessions
// whose threads list the row.
func TestStableDelegateUpdate_ReachesEachLiveAncestorSubagentThread(t *testing.T) {
	t.Parallel()
	var recorder sessionEventRecorder
	tree := newRealDelegateTree(t, recorder.record)
	middleID := tree.parent.ChildSessionID
	var middleRevisions []uint64
	for _, event := range recorder.snapshot() {
		data, ok := event.Data.(events.DelegateUpdatedData)
		if !ok {
			continue
		}
		switch {
		case event.SessionID == middleID && data.DelegateID == tree.grandchildID:
			middleRevisions = append(middleRevisions, data.ProjectionRevision)
			if data.OwnerSessionID != tree.s.ID() || !slices.Equal(data.AncestorSessionIDs, []string{middleID}) {
				t.Fatalf("grandchild update on the middle stream = owner %q ancestors %v, want owner %q ancestors [%s]", data.OwnerSessionID, data.AncestorSessionIDs, tree.s.ID(), middleID)
			}
		case event.SessionID == middleID || event.SessionID == tree.grandchildSessionID:
			t.Fatalf("an update reached a thread that does not list it: %s on %s's stream", data.DelegateID, event.SessionID)
		}
	}
	if len(middleRevisions) == 0 {
		t.Fatal("no grandchild delegate update reached the middle subagent's stream")
	}
	// The root's stream still carries each grandchild update exactly once, with
	// the same ancestry, so the root thread and the middle thread see the same
	// revisions.
	var rootRevisions []uint64
	for _, data := range drainRootDelegateUpdates(tree.s, tree.grandchildID) {
		if !slices.Equal(data.AncestorSessionIDs, []string{middleID}) {
			t.Fatalf("grandchild update on the root stream has ancestors %v, want [%s]", data.AncestorSessionIDs, middleID)
		}
		rootRevisions = append(rootRevisions, data.ProjectionRevision)
	}
	if !slices.Equal(rootRevisions, middleRevisions) {
		t.Fatalf("grandchild revisions on the root stream %v, on the middle stream %v; want the same updates on both", rootRevisions, middleRevisions)
	}
}

// delegateUpdateCount is how many updates for delegateID the stream of
// sessionID carried in recorded.
func delegateUpdateCount(recorded []events.SessionEvent, sessionID, delegateID string) int {
	n := 0
	for _, event := range recorded {
		if data, ok := event.Data.(events.DelegateUpdatedData); ok && event.SessionID == sessionID && data.DelegateID == delegateID {
			n++
		}
	}
	return n
}

// drainRootDelegateUpdates empties the root's buffered stream and returns its
// updates for delegateID.
func drainRootDelegateUpdates(s *Session, delegateID string) []events.DelegateUpdatedData {
	var out []events.DelegateUpdatedData
	for _, event := range drainPendingEvents(s) {
		if data, ok := event.Data.(events.DelegateUpdatedData); ok && data.DelegateID == delegateID {
			out = append(out, data)
		}
	}
	return out
}

// emitGrandchildRow publishes the grandchild's current row through the
// controller's update path, after letting edit adjust the captured row, and
// returns the descendant events it produced.
func emitGrandchildRow(tree realDelegateTree, recorder *sessionEventRecorder, edit func(*delegateSnapshot)) []events.SessionEvent {
	tree.c.mu.Lock()
	plan := tree.c.capturedPlanLocked(tree.grandchildID)
	tree.c.mu.Unlock()
	if edit != nil {
		edit(&plan.rows[0])
	}
	from := len(recorder.snapshot())
	tree.c.emitDelegateUpdate(plan)
	return recorder.snapshot()[from:]
}

// A released ancestor has no stream to carry the update: the grandchild's row
// published after the middle subagent's runtime is released reaches the
// root's thread only, still naming the middle subagent as an ancestor so its
// next thread read lists the row.
func TestStableDelegateUpdate_ReleasedAncestorGetsNone(t *testing.T) {
	t.Parallel()
	var recorder sessionEventRecorder
	tree := newRealDelegateTree(t, recorder.record)
	middleID := tree.parent.ChildSessionID
	tree.releaseMiddle(t)
	drainRootDelegateUpdates(tree.s, tree.grandchildID)
	emitted := emitGrandchildRow(tree, &recorder, nil)
	if n := delegateUpdateCount(emitted, middleID, tree.grandchildID); n != 0 {
		t.Fatalf("released middle subagent's stream carried %d grandchild updates, want none", n)
	}
	root := drainRootDelegateUpdates(tree.s, tree.grandchildID)
	if len(root) != 1 || !slices.Equal(root[0].AncestorSessionIDs, []string{middleID}) {
		t.Fatalf("root's grandchild updates = %+v, want one with ancestors [%s]", root, middleID)
	}
}

// Current delegates are all owned by the root, so the owner stream and an
// ancestor stream never coincide. A row whose owner is its parent subagent's
// session (the shape runtimeForDelegateOwnerLocked still serves) makes them
// the same stream, which must carry the update once.
func TestStableDelegateUpdate_ParentOwnedRowReachesItsOwnerOnce(t *testing.T) {
	t.Parallel()
	var recorder sessionEventRecorder
	tree := newRealDelegateTree(t, recorder.record)
	middleID := tree.parent.ChildSessionID
	emitted := emitGrandchildRow(tree, &recorder, func(row *delegateSnapshot) {
		row.descriptor.OwnerSessionID = middleID
	})
	if n := delegateUpdateCount(emitted, middleID, tree.grandchildID); n != 1 {
		t.Fatalf("parent-owned grandchild update reached its owner's stream %d times, want once", n)
	}
}

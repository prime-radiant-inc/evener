package agent

import (
	"slices"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
)

// A subagent's open thread lists its subtree, so a change to a delegate below
// it has to reach that thread too: the grandchild's updates are emitted on the
// middle subagent's own stream as well as the root's, carrying the sessions
// whose threads list the row.
func TestStableDelegateUpdate_ReachesEachLiveAncestorSubagentThread(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []events.SessionEvent
	tree := newRealDelegateTree(t, func(event events.SessionEvent) {
		if event.Kind == events.EventDelegateUpdated {
			mu.Lock()
			seen = append(seen, event)
			mu.Unlock()
		}
	})
	middleID := tree.parent.ChildSessionID
	mu.Lock()
	defer mu.Unlock()
	var middleSawItself, leafSawAnything bool
	var middleRevisions []uint64
	for _, event := range seen {
		data, ok := event.Data.(events.DelegateUpdatedData)
		if !ok {
			t.Fatalf("delegate update data = %T", event.Data)
		}
		switch event.SessionID {
		case middleID:
			if data.DelegateID == tree.parent.DelegateID {
				middleSawItself = true
			}
			if data.DelegateID == tree.grandchildID {
				middleRevisions = append(middleRevisions, data.ProjectionRevision)
				if data.OwnerSessionID != tree.s.ID() || !slices.Equal(data.AncestorSessionIDs, []string{middleID}) {
					t.Fatalf("grandchild update on the middle stream = owner %q ancestors %v, want owner %q ancestors [%s]", data.OwnerSessionID, data.AncestorSessionIDs, tree.s.ID(), middleID)
				}
			}
		case tree.grandchildSessionID:
			leafSawAnything = true
		}
	}
	// The root's stream still carries each grandchild update exactly once, with
	// the same ancestry, so the root thread and the middle thread see the same
	// revisions.
	var rootRevisions []uint64
drain:
	for {
		select {
		case event := <-tree.s.events:
			data, ok := event.Data.(events.DelegateUpdatedData)
			if !ok || data.DelegateID != tree.grandchildID {
				continue
			}
			if event.SessionID != tree.s.ID() || !slices.Equal(data.AncestorSessionIDs, []string{middleID}) {
				t.Fatalf("grandchild update on the root stream = session %q ancestors %v", event.SessionID, data.AncestorSessionIDs)
			}
			rootRevisions = append(rootRevisions, data.ProjectionRevision)
		default:
			break drain
		}
	}
	if len(rootRevisions) == 0 || !slices.Equal(rootRevisions, middleRevisions) {
		t.Fatalf("grandchild revisions on the root stream %v, on the middle stream %v; want the same updates on both", rootRevisions, middleRevisions)
	}
	if len(middleRevisions) == 0 {
		t.Fatalf("no grandchild delegate update reached the middle subagent's stream; descendant updates seen: %d", len(seen))
	}
	if middleSawItself || leafSawAnything {
		t.Fatalf("an update reached a thread that does not list it: middle saw its own row %t, leaf saw a row %t", middleSawItself, leafSawAnything)
	}
}

// delegateUpdateRecorder keeps the delegate updates a tree's subagent sessions
// emit, as the daemon's AppWire bridge sees them.
type delegateUpdateRecorder struct {
	mu   sync.Mutex
	seen []events.SessionEvent
}

func (r *delegateUpdateRecorder) record(event events.SessionEvent) {
	if event.Kind == events.EventDelegateUpdated {
		r.mu.Lock()
		r.seen = append(r.seen, event)
		r.mu.Unlock()
	}
}

// count is how many updates for delegateID the stream of sessionID carried
// at or after the recorder's position from.
func (r *delegateUpdateRecorder) count(from int, sessionID, delegateID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, event := range r.seen[from:] {
		if data, ok := event.Data.(events.DelegateUpdatedData); ok && event.SessionID == sessionID && data.DelegateID == delegateID {
			n++
		}
	}
	return n
}

func (r *delegateUpdateRecorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// drainRootDelegateUpdates empties the root's buffered stream and returns its
// updates for delegateID.
func drainRootDelegateUpdates(s *Session, delegateID string) []events.DelegateUpdatedData {
	var out []events.DelegateUpdatedData
	for {
		select {
		case event := <-s.events:
			if data, ok := event.Data.(events.DelegateUpdatedData); ok && data.DelegateID == delegateID {
				out = append(out, data)
			}
		default:
			return out
		}
	}
}

// A released ancestor has no stream to carry the update: a settled change to
// the grandchild after the middle subagent's runtime is released reaches the
// root's thread only, still naming the middle subagent as an ancestor so its
// next thread read lists the row.
func TestStableDelegateUpdate_ReleasedAncestorGetsNone(t *testing.T) {
	t.Parallel()
	recorder := &delegateUpdateRecorder{}
	tree := newRealDelegateTree(t, recorder.record)
	c, middleID := tree.c, tree.parent.ChildSessionID
	claim := claimSettledIdleSubtree(t, c, tree.parent.DelegateID, tree.parentRuntime, tree.grandchildRuntime)
	if err := c.AbortRuntimeReclamation(claim); err != nil {
		t.Fatal(err)
	}
	if !tree.parentRuntime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("real idle subtree release refused")
	}
	drainRootDelegateUpdates(tree.s, tree.grandchildID)
	from := recorder.len()
	// Only the middle subagent, holding a live lease, may close its child, and
	// a released middle holds none. Journal the same closure CloseResumability
	// would, without that authorization, and publish it as it does.
	c.mu.Lock()
	plan, err := c.appendResumabilityClosureLocked(tree.grandchildID, delegatestore.Event{
		Kind:               delegatestore.EventDelegateResumabilityClosed,
		DelegateID:         tree.grandchildID,
		ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "test closed"},
	})
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("close grandchild resumability: %v", err)
	}
	c.emitDelegateUpdate(plan)
	if n := recorder.count(from, middleID, tree.grandchildID); n != 0 {
		t.Fatalf("released middle subagent's stream carried %d grandchild updates, want none", n)
	}
	root := drainRootDelegateUpdates(tree.s, tree.grandchildID)
	if len(root) == 0 {
		t.Fatal("the grandchild's closure did not reach the root's stream")
	}
	if last := root[len(root)-1]; last.Resumable || !slices.Equal(last.AncestorSessionIDs, []string{middleID}) {
		t.Fatalf("root's grandchild update = resumable %t ancestors %v, want closed with ancestors [%s]", last.Resumable, last.AncestorSessionIDs, middleID)
	}
}

// Current delegates are all owned by the root, so the owner stream and an
// ancestor stream never coincide. A row whose owner is its parent subagent's
// session (the shape runtimeForDelegateOwnerLocked still serves) makes them
// the same stream, which must carry the update once.
func TestStableDelegateUpdate_ParentOwnedRowReachesItsOwnerOnce(t *testing.T) {
	t.Parallel()
	recorder := &delegateUpdateRecorder{}
	tree := newRealDelegateTree(t, recorder.record)
	c, middleID := tree.c, tree.parent.ChildSessionID
	c.mu.Lock()
	c.durable[tree.grandchildID].Descriptor.OwnerSessionID = middleID
	plan := c.capturedPlanLocked(tree.grandchildID)
	c.mu.Unlock()
	from := recorder.len()
	tree.s.emitStableDelegateUpdate(plan)
	if n := recorder.count(from, middleID, tree.grandchildID); n != 1 {
		t.Fatalf("parent-owned grandchild update reached its owner's stream %d times, want once", n)
	}
}

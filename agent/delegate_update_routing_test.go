package agent

import (
	"slices"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
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
	var middleSawGrandchild, middleSawItself, leafSawAnything bool
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
				middleSawGrandchild = true
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
	var middleRevisions, rootRevisions []uint64
	for _, event := range seen {
		if data := event.Data.(events.DelegateUpdatedData); event.SessionID == middleID && data.DelegateID == tree.grandchildID {
			middleRevisions = append(middleRevisions, data.ProjectionRevision)
		}
	}
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
	if !middleSawGrandchild {
		t.Fatalf("no grandchild delegate update reached the middle subagent's stream; descendant updates seen: %d", len(seen))
	}
	if middleSawItself || leafSawAnything {
		t.Fatalf("an update reached a thread that does not list it: middle saw its own row %t, leaf saw a row %t", middleSawItself, leafSawAnything)
	}
}

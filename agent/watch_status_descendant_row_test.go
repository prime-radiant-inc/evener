package agent

import (
	"path/filepath"
	"testing"
)

// A subagent session's own live watches appear on no row today: the daemon
// projects only the root session's status, so the child's row carries none.
// LiveWatchesForDescendant resolves a descendant session by ID and returns
// exactly the rows that child's own projection would show, so the appwire thread
// row can carry them. This is the parent/sibling isolation the hubcore watch
// test pins: a root-receiver watch held by the child stays on the root's row,
// and a child's own keyless watch never leaks onto the root's.
func TestLiveWatchesForDescendantReturnsChildsOwnWatch(t *testing.T) {
	root := newDescendantWatchSession(t)
	child := newDescendantWatchSession(t)
	registerDescendantSession(t, root, child)

	child.jobManager.mu.Lock()
	child.jobManager.watches[watchKey{Target: "job_child"}] = &watchConfig{
		id: "watch-child", watchID: "watch-child", sourcePublic: "self", target: "job_child",
		createdAt: frozenTestTime,
	}
	child.jobManager.mu.Unlock()

	got := root.LiveWatchesForDescendant(child.ID())
	if len(got) != 1 || got[0].ID != "watch-child" {
		t.Fatalf("descendant watches = %+v, want the child's own watch", got)
	}
	// The child's watch belongs to the child: the root's row must not claim it.
	if rows := root.liveWatchStatuses(); len(rows) != 0 {
		t.Fatalf("root projection = %+v, want no keyless descendant watch", rows)
	}
	// The root is not a descendant of itself; the seam must not answer for it.
	if rows := root.LiveWatchesForDescendant(root.ID()); rows != nil {
		t.Fatalf("root lookup through the descendant accessor = %+v, want nil", rows)
	}
}

// A nested descendant is reached by recursing through its own parent. The lookup
// snapshots the live child set once and reuses it for both the direct-match and
// recursion loops; this pins the recursion still finds a grandchild's watch.
func TestLiveWatchesForDescendantFindsNestedDescendant(t *testing.T) {
	root := newDescendantWatchSession(t)
	child := newDescendantWatchSession(t)
	grandchild := newDescendantWatchSession(t)
	registerDescendantSession(t, root, child)
	registerDescendantSession(t, child, grandchild)

	grandchild.jobManager.mu.Lock()
	grandchild.jobManager.watches[watchKey{Target: "job_grandchild"}] = &watchConfig{
		id: "watch-grandchild", watchID: "watch-grandchild", sourcePublic: "self", target: "job_grandchild",
		createdAt: frozenTestTime,
	}
	grandchild.jobManager.mu.Unlock()

	got := root.LiveWatchesForDescendant(grandchild.ID())
	if len(got) != 1 || got[0].ID != "watch-grandchild" {
		t.Fatalf("nested descendant watches = %+v, want the grandchild's own watch", got)
	}
}

// A receiver watch targeting a descendant's job lives in that descendant's
// manager with the root recorded as the receiver. It belongs on the root's row,
// so the descendant accessor must not surface it on the child's row.
func TestLiveWatchesForDescendantExcludesRootReceiverWatch(t *testing.T) {
	root := newDescendantWatchSession(t)
	child := newDescendantWatchSession(t)
	registerDescendantSession(t, root, child)

	child.jobManager.mu.Lock()
	child.jobManager.watches[watchKey{Target: "job_child", ReceiverSessionID: root.ID()}] = &watchConfig{
		id: "watch-for-root", watchID: "watch-for-root", sourcePublic: "self", target: "job_child",
		receiverSessionID: root.ID(), createdAt: frozenTestTime,
	}
	child.jobManager.mu.Unlock()

	if rows := root.LiveWatchesForDescendant(child.ID()); len(rows) != 0 {
		t.Fatalf("descendant watches = %+v, want none (the receiver is the root)", rows)
	}
	// The root's own projection aggregates the descendant manager (the same set
	// the #655 stop inventory scans), and there the receiver watch belongs.
	rows := aggregateWatchStatuses(root.ID(), []*jobManager{root.jobManager, child.jobManager})
	if len(rows) != 1 || rows[0].ID != "watch-for-root" {
		t.Fatalf("root projection = %+v, want the root-receiver watch held by the child", rows)
	}
}

func newDescendantWatchSession(t *testing.T) *Session {
	t.Helper()
	return newSession(t, withConfig(SessionConfig{
		NoProjectPrompts: true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
	}))
}

// registerDescendantSession installs child as a live direct child of root in the
// manager the daemon reaches children through, removing it at cleanup.
func registerDescendantSession(t *testing.T, root, child *Session) {
	t.Helper()
	root.subagents.mu.Lock()
	root.subagents.subs[child.ID()] = &subagent{id: child.ID(), sess: child, running: true}
	root.subagents.mu.Unlock()
	t.Cleanup(func() {
		root.subagents.mu.Lock()
		delete(root.subagents.subs, child.ID())
		root.subagents.mu.Unlock()
	})
}

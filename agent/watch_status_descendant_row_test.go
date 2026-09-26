package agent

import (
	"path/filepath"
	"testing"
)

// The thread LIST path holds every row ID before it samples any of them, so it
// resolves the page in one call: one walk of the live tree answers the root, a
// descendant that has rows, and a descendant that has none. An ID this session
// cannot answer for is absent, which is the single-ID resolvers' nil; a known
// descendant with no watches is present with a non-nil empty slice, which is how
// its row sheds a cleared watch. No IDs means no answer map at all.
func TestLiveWatchRowsForSessionsAnswersThePageInOneWalk(t *testing.T) {
	t.Parallel()
	root := newDescendantWatchSession(t)
	child := newDescendantWatchSession(t)
	grandchild := newDescendantWatchSession(t)
	registerDescendantSession(t, root, child)
	registerDescendantSession(t, child, grandchild)

	root.jobManager.mu.Lock()
	root.jobManager.watches[watchKey{Target: "job_root"}] = &watchConfig{
		id: "watch-root", watchID: "watch-root", sourcePublic: "self", target: "job_root",
		createdAt: frozenTestTime,
	}
	root.jobManager.mu.Unlock()
	grandchild.jobManager.mu.Lock()
	grandchild.jobManager.watches[watchKey{Target: "job_grandchild"}] = &watchConfig{
		id: "watch-grandchild", watchID: "watch-grandchild", sourcePublic: "self", target: "job_grandchild",
		createdAt: frozenTestTime,
	}
	grandchild.jobManager.mu.Unlock()

	page := root.LiveWatchRowsForSessions([]string{root.ID(), child.ID(), grandchild.ID(), "nobody", ""})
	if len(page) != 3 {
		t.Fatalf("page answers = %+v, want exactly the root and its two live descendants", page)
	}
	if rows := page[root.ID()]; len(rows) != 1 || rows[0].ID != "watch-root" {
		t.Fatalf("root answer = %+v, want its own watch", rows)
	}
	childRows, present := page[child.ID()]
	if !present || childRows == nil || len(childRows) != 0 {
		t.Fatalf("known descendant with no watches = %+v (present %v), want a present non-nil empty answer", childRows, present)
	}
	if rows := page[grandchild.ID()]; len(rows) != 1 || rows[0].ID != "watch-grandchild" {
		t.Fatalf("nested descendant answer = %+v, want its own watch", rows)
	}
	if _, present := page["nobody"]; present {
		t.Fatalf("unknown ID was answered: %+v", page)
	}
	if _, present := page[""]; present {
		t.Fatalf("empty ID was answered: %+v", page)
	}
	if got := root.LiveWatchRowsForSessions(nil); got != nil {
		t.Fatalf("no IDs = %+v, want nil", got)
	}
}

// A subagent session's own live watches appear on no row today: the daemon
// projects only the root session's status, so the child's row carries none.
// LiveWatchesForDescendant resolves a descendant session by ID and returns
// exactly the rows that child's own projection would show, so the appwire thread
// row can carry them. This is the parent/sibling isolation the hubcore watch
// test pins: a root-receiver watch held by the child stays on the root's row,
// and a child's own keyless watch never leaks onto the root's.
func TestLiveWatchesForDescendantReturnsChildsOwnWatch(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// LiveWatchesForSession is the list path's seam. Unlike the narrower
// LiveWatchesForDescendant it answers for the root's own row as well as a
// descendant's, so a watch armed or cleared on the root since the last
// diagnostics refresh shows up on the next list read. An unknown ID yields nil
// (leave the cached projection alone); the known root with no watches yields a
// non-nil empty answer (the root really has none now).
func TestLiveWatchesForSessionAnswersForRootAndDescendant(t *testing.T) {
	t.Parallel()
	root := newDescendantWatchSession(t)
	child := newDescendantWatchSession(t)
	registerDescendantSession(t, root, child)

	root.jobManager.mu.Lock()
	root.jobManager.watches[watchKey{Target: "job_root"}] = &watchConfig{
		id: "watch-root", watchID: "watch-root", sourcePublic: "self", target: "job_root",
		createdAt: frozenTestTime,
	}
	root.jobManager.mu.Unlock()
	child.jobManager.mu.Lock()
	child.jobManager.watches[watchKey{Target: "job_child"}] = &watchConfig{
		id: "watch-child", watchID: "watch-child", sourcePublic: "self", target: "job_child",
		createdAt: frozenTestTime,
	}
	child.jobManager.mu.Unlock()

	rootRows := root.LiveWatchesForSession(root.ID())
	if len(rootRows) != 1 || rootRows[0].ID != "watch-root" {
		t.Fatalf("root live watches = %+v, want the root's own watch", rootRows)
	}
	childRows := root.LiveWatchesForSession(child.ID())
	if len(childRows) != 1 || childRows[0].ID != "watch-child" {
		t.Fatalf("descendant live watches = %+v, want the child's own watch", childRows)
	}
	if rows := root.LiveWatchesForSession("nobody"); rows != nil {
		t.Fatalf("unknown session = %+v, want nil", rows)
	}
	if rows := root.LiveWatchesForSession(""); rows != nil {
		t.Fatalf("empty session = %+v, want nil", rows)
	}
}

// The root with no watches answers with a non-nil empty slice, not nil: the list
// path must be able to tell "the root has no watches now" from "this ID is
// unknown", or a watch cleared since the last refresh could never leave the row.
func TestLiveWatchesForSessionEmptyRootIsNotEmptyAnswer(t *testing.T) {
	t.Parallel()
	root := newDescendantWatchSession(t)
	rows := root.LiveWatchesForSession(root.ID())
	if rows == nil || len(rows) != 0 {
		t.Fatalf("empty root live watches = %+v, want a non-nil empty answer", rows)
	}
}

// A KNOWN descendant with no watches must answer with a non-nil empty slice, not
// nil. The list path merges only a non-nil sample, so nil can only mean "this ID
// is not a descendant I can see"; a descendant that just lost its last watch
// would otherwise keep showing stale rows forever. The root, an empty ID, and an
// unknown ID stay nil, and a nested known descendant with no watches is
// non-nil empty too, so the recursion still distinguishes "found, empty" from
// "not found".
func TestLiveWatchesForDescendantKnownEmptyIsNotEmptyAnswer(t *testing.T) {
	t.Parallel()
	root := newDescendantWatchSession(t)
	child := newDescendantWatchSession(t)
	grandchild := newDescendantWatchSession(t)
	registerDescendantSession(t, root, child)
	registerDescendantSession(t, child, grandchild)

	childRows := root.LiveWatchesForDescendant(child.ID())
	if childRows == nil || len(childRows) != 0 {
		t.Fatalf("known descendant with no watches = %+v, want a non-nil empty answer", childRows)
	}
	nestedRows := root.LiveWatchesForDescendant(grandchild.ID())
	if nestedRows == nil || len(nestedRows) != 0 {
		t.Fatalf("known nested descendant with no watches = %+v, want a non-nil empty answer", nestedRows)
	}
	if rows := root.LiveWatchesForDescendant(root.ID()); rows != nil {
		t.Fatalf("root lookup through the descendant accessor = %+v, want nil", rows)
	}
	if rows := root.LiveWatchesForDescendant("nobody"); rows != nil {
		t.Fatalf("unknown descendant = %+v, want nil", rows)
	}
	if rows := root.LiveWatchesForDescendant(""); rows != nil {
		t.Fatalf("empty descendant ID = %+v, want nil", rows)
	}
}

// liveWatchStatuses answers for a KNOWN session with no watches with the same
// non-nil empty slice its public siblings use. nil there means "this session
// cannot be answered for", never "it has no watches now", so a caller following
// the documented contract cannot mis-handle the empty case.
func TestLiveWatchStatusesKnownEmptyIsNotEmptyAnswer(t *testing.T) {
	t.Parallel()
	root := newDescendantWatchSession(t)
	rows := root.liveWatchStatuses()
	if rows == nil || len(rows) != 0 {
		t.Fatalf("known empty session = %+v, want a non-nil empty answer", rows)
	}
}

// The sampled envelope facet carries only what THIS session's manager owns; the
// receiver rollup that reaches into a descendant's manager is sampled by the
// thread LIST path instead. The split is the sampling contract's: a method on
// the envelope surface runs on the event bridge, where taking another session's
// manager lock could block event consumption (see session_envelope_sampling.go),
// so the rollup has to leave the bridge -- and it does, because the LIST path
// answers the same question outside it.
func TestDetailedStatusWatchesStayOffTheDescendantManagers(t *testing.T) {
	t.Parallel()
	root := newDescendantWatchSession(t)
	child := newDescendantWatchSession(t)
	registerDescendantSession(t, root, child)

	root.jobManager.mu.Lock()
	root.jobManager.watches[watchKey{Target: "job_root"}] = &watchConfig{
		id: "watch-root", watchID: "watch-root", sourcePublic: "self", target: "job_root",
		createdAt: frozenTestTime,
	}
	root.jobManager.mu.Unlock()
	child.jobManager.mu.Lock()
	child.jobManager.watches[watchKey{Target: "job_child", ReceiverSessionID: root.ID()}] = &watchConfig{
		id: "watch-for-root", watchID: "watch-for-root", sourcePublic: "self", target: "job_child",
		receiverSessionID: root.ID(), createdAt: frozenTestTime,
	}
	child.jobManager.mu.Unlock()

	ds := root.DetailedStatus()
	if len(ds.Watches) != 1 || ds.Watches[0].ID != "watch-root" {
		t.Fatalf("DetailedStatus.Watches = %+v, want only the root manager's own row", ds.Watches)
	}
	// The LIST path still carries the receiver row, from the same walk it always
	// used -- off the bridge.
	page := root.LiveWatchRowsForSessions([]string{root.ID()})
	rows := page[root.ID()]
	if len(rows) != 2 {
		t.Fatalf("list page rows = %+v, want the root's own row and the receiver row", rows)
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

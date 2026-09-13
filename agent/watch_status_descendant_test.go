package agent

import "testing"

// A receiver watch targeting a descendant's job is installed in that
// descendant's job manager with the receiver session recorded. The status
// projection used to read only the session's own manager, so the watch was
// invisible to BOTH sessions: the owner's projection filtered it out on the
// receiver key, and the receiver never held the config. aggregateWatchStatuses
// is the seam that scans descendant managers the way the stop inventory does.
func TestAggregateWatchStatusesReachesDescendantManager(t *testing.T) {
	t.Parallel()
	root := newTestJM(t) // owns testOwnerSessionID
	child, err := newJobManagerNoSync(t.TempDir(), testChildSessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child job manager: %v", err)
	}

	child.mu.Lock()
	child.watches[watchKey{Target: "job_child", ReceiverSessionID: testOwnerSessionID}] = &watchConfig{
		id: "watch-child", watchID: "watch-child", sourcePublic: "self", target: "job_child",
		receiverSessionID: testOwnerSessionID, createdAt: frozenTestTime,
	}
	child.mu.Unlock()

	managers := []*jobManager{root, child}
	got := aggregateWatchStatuses(testOwnerSessionID, managers)
	if len(got) != 1 || got[0].ID != "watch-child" {
		t.Fatalf("root aggregation = %+v, want the receiver watch held by the descendant manager", got)
	}
	// The descendant's own projection must not claim it: the receiver is the root.
	if rows := aggregateWatchStatuses(testChildSessionID, managers); len(rows) != 0 {
		t.Fatalf("child aggregation = %+v, want none (the receiver is the root)", rows)
	}
}

// A keyless watch belongs to the manager that owns it. Aggregating descendant
// managers must not surface a descendant's own watch on an ancestor's summary.
func TestAggregateWatchStatusesDoesNotLeakKeylessDescendantWatch(t *testing.T) {
	t.Parallel()
	root := newTestJM(t)
	child, err := newJobManagerNoSync(t.TempDir(), testChildSessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new child job manager: %v", err)
	}
	child.mu.Lock()
	child.watches[watchKey{Target: "job_child"}] = &watchConfig{
		id: "watch-keyless", watchID: "watch-keyless", sourcePublic: "self", target: "job_child",
		createdAt: frozenTestTime,
	}
	child.mu.Unlock()

	managers := []*jobManager{root, child}
	if rows := aggregateWatchStatuses(testOwnerSessionID, managers); len(rows) != 0 {
		t.Fatalf("root aggregation = %+v, want no keyless descendant watch", rows)
	}
	got := aggregateWatchStatuses(testChildSessionID, managers)
	if len(got) != 1 || got[0].ID != "watch-keyless" {
		t.Fatalf("child aggregation = %+v, want its own keyless watch", got)
	}
}

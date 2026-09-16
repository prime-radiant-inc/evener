package agent

import (
	"reflect"
	"testing"
	"time"
)

// sessionVisibleWatch and sessionVisibleHistory are the predicates the
// session-facing list and inspect projections use; the receiver-facing ones
// substitute the receiver match. They are named here so the collector tests
// exercise the same closures the production methods pass in.
func sessionVisibleWatch(jm *jobManager) func(*watchConfig) bool {
	return func(cfg *watchConfig) bool { return watchConfigVisibleToSession(cfg, jm.sessionID) }
}

func sessionVisibleHistory(jm *jobManager) func(watchHistoryEntry) bool {
	return func(h watchHistoryEntry) bool { return watchHistoryVisibleToSession(h, jm.sessionID) }
}

// seedWatchProjectionState installs one live config (with a delivery ring so a
// shared backing array would be detectable), one detached terminalFlush config,
// and one history entry, so a collector has something to gather from all three
// sources.
func seedWatchProjectionState(jm *jobManager) {
	live := &watchConfig{
		id: "watch-one", watchID: "watch-one", sourcePublic: "self", target: "job_one",
		createdAt: frozenTestTime, deliveries: 3,
		deliveryTimes: []time.Time{frozenTestTime, frozenTestTime.Add(time.Second)},
	}
	detached := &watchConfig{
		id: "watch-two", watchID: "watch-two", sourcePublic: "self", target: "job_two",
		createdAt: frozenTestTime, deliveries: 1,
		deliveryTimes: []time.Time{frozenTestTime},
	}
	jm.mu.Lock()
	if jm.terminalFlush == nil {
		jm.terminalFlush = make(map[*watchConfig]bool)
	}
	jm.watches[watchKey{Target: "job_one"}] = live
	jm.terminalFlush[detached] = true
	jm.watchHistory = append(jm.watchHistory, watchHistoryEntry{
		id: "watch-old", source: "self", target: "job_old",
		condition: "after_seconds: 600", deliveries: 2,
		endReason: "fired", endedAt: frozenTestTime,
	})
	jm.mu.Unlock()
}

// TestWatchInspectSnapshotsCollectUnderTheLock pins the list projection's new
// seam without timing anything. Both the session-visible and the
// receiver-matched lists are built from watchInspectSnapshots, so after it
// returns jm.mu must be free -- a non-blocking TryLock proves formatting does
// not run under the lock -- and the configs it hands back must be private
// copies, not the live configs whose delivery ring the delivery path rewrites in
// place under jm.mu.
func TestWatchInspectSnapshotsCollectUnderTheLock(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchProjectionState(jm)

	pending, history := jm.watchInspectSnapshots(sessionVisibleWatch(jm), sessionVisibleHistory(jm))
	if len(pending) != 2 {
		t.Fatalf("watchInspectSnapshots returned %d configs, want the 1 live + 1 detached", len(pending))
	}
	if len(history) != 1 || history[0].id != "watch-old" {
		t.Fatalf("watchInspectSnapshots returned history %+v, want the 1 visible entry", history)
	}
	if !jm.mu.TryLock() {
		t.Fatal("watchInspectSnapshots returned with jm.mu still held: the formatting that follows would run under the lock")
	}
	jm.mu.Unlock()

	jm.mu.Lock()
	var live *watchConfig
	for _, cfg := range jm.watches {
		live = cfg
	}
	jm.mu.Unlock()
	if live == nil {
		t.Fatal("test setup: the installed watch left jm.watches")
	}
	for i := range pending {
		if len(pending[i].cfg.deliveryTimes) == 0 {
			continue
		}
		if &pending[i].cfg.deliveryTimes[0] == &live.deliveryTimes[0] {
			t.Fatalf("snapshot %d shares the live config's delivery ring; an at-cap delivery rewrites it in place", i)
		}
	}
}

// TestWatchInspectSnapshotsPreserveRowsAndOrder pins that the collector plus the
// pure formatter reproduce exactly what watchListToolResult and
// watchListToolResultForReceiver returned before the split: the same rows in
// the same (source, id) order, over the same three sources.
func TestWatchInspectSnapshotsPreserveRowsAndOrder(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchProjectionState(jm)
	jm.mu.Lock()
	jm.watches[watchKey{Target: "job_recv", ReceiverSessionID: "RS", ReceiverDelegateID: "RD"}] = &watchConfig{
		id: "watch-recv", watchID: "watch-recv", sourcePublic: "self", target: "job_recv",
		receiverSessionID: "RS", receiverDelegateID: "RD", createdAt: frozenTestTime,
	}
	jm.mu.Unlock()

	sessionPending, sessionHistory := jm.watchInspectSnapshots(sessionVisibleWatch(jm), sessionVisibleHistory(jm))
	wantSession := formatWatchListInspectResult(sessionPending, sessionHistory)
	if got := jm.watchListToolResult(); !reflect.DeepEqual(got, wantSession) {
		t.Fatalf("watchListToolResult = %+v, want the pure formatter's %+v", got, wantSession)
	}
	// The receiver row is invisible to the session listing but visible to the
	// receiver listing; the receiver list also carries no history entry.
	if wantSession.Count != 2 || len(wantSession.RecentWatches) != 1 {
		t.Fatalf("session list = %+v, want 2 watches (1 live + 1 detached) and 1 recent", wantSession)
	}

	recvVisible := func(cfg *watchConfig) bool {
		return watchConfigMatchesReceiver(cfg, "RS", "RD")
	}
	recvHistory := func(h watchHistoryEntry) bool {
		return watchHistoryMatchesReceiver(h, "RS", "RD")
	}
	recvPending, recvEntries := jm.watchInspectSnapshots(recvVisible, recvHistory)
	wantReceiver := formatWatchListInspectResult(recvPending, recvEntries)
	if got := jm.watchListToolResultForReceiver("RS", "RD"); !reflect.DeepEqual(got, wantReceiver) {
		t.Fatalf("watchListToolResultForReceiver = %+v, want the pure formatter's %+v", got, wantReceiver)
	}
	if wantReceiver.Count != 1 || len(wantReceiver.RecentWatches) != 0 {
		t.Fatalf("receiver list = %+v, want just the receiver-matching live watch", wantReceiver)
	}
}

// TestFindWatchInspectSnapshotCollectsUnderTheLock pins the inspect path's
// seam. The finder copies the matched config under jm.mu and returns with the
// lock free; the copy, not the live config, is what the caller formats.
func TestFindWatchInspectSnapshotCollectsUnderTheLock(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchProjectionState(jm)

	match, ok := jm.findWatchInspectSnapshot("watch-one", sessionVisibleWatch(jm), sessionVisibleHistory(jm))
	if !ok || match.fromHistory || match.detached {
		t.Fatalf("findWatchInspectSnapshot = %+v, %t, want the live watch-one config", match, ok)
	}
	if !jm.mu.TryLock() {
		t.Fatal("findWatchInspectSnapshot returned with jm.mu still held")
	}
	jm.mu.Unlock()

	jm.mu.Lock()
	var live *watchConfig
	for _, cfg := range jm.watches {
		live = cfg
	}
	jm.mu.Unlock()
	if len(match.cfg.deliveryTimes) != 2 || &match.cfg.deliveryTimes[0] == &live.deliveryTimes[0] {
		t.Fatalf("finder's ring = %v shares the live ring %v", match.cfg.deliveryTimes, live.deliveryTimes)
	}
	got := match.result()
	if !got.Watching || got.WatchID != "watch-one" || got.Deliveries != 3 {
		t.Fatalf("formatted match = %+v, want the live watch-one row", got)
	}
	if want := jm.inspectWatchByID("watch-one"); !reflect.DeepEqual(got, want) {
		t.Fatalf("finder+format = %+v, want inspectWatchByID's %+v", got, want)
	}

	// The detached and history legs resolve through the same finder, and a
	// missing id reports no match.
	if detached, ok := jm.findWatchInspectSnapshot("watch-two", sessionVisibleWatch(jm), sessionVisibleHistory(jm)); !ok || !detached.detached {
		t.Fatalf("detached finder = %+v, %t, want the terminalFlush watch", detached, ok)
	}
	if historical, ok := jm.findWatchInspectSnapshot("watch-old", sessionVisibleWatch(jm), sessionVisibleHistory(jm)); !ok || !historical.fromHistory {
		t.Fatalf("history finder = %+v, %t, want the ring entry", historical, ok)
	}
	if _, ok := jm.findWatchInspectSnapshot("nope", sessionVisibleWatch(jm), sessionVisibleHistory(jm)); ok {
		t.Fatal("finder reported a match for a missing id")
	}
}

// TestWatchConfigSnapshotsWhereCollectsUnderTheLock pins the receiver-summary
// collector: it selects by the supplied predicate, returns private copies, and
// leaves jm.mu free. liveWatchSummariesForReceiver now formats those copies with
// the same pure formatter liveWatchSummaries uses.
func TestWatchConfigSnapshotsWhereCollectsUnderTheLock(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchProjectionState(jm)
	jm.mu.Lock()
	jm.watches[watchKey{Target: "job_recv", ReceiverSessionID: "RS", ReceiverDelegateID: "RD"}] = &watchConfig{
		id: "watch-recv", watchID: "watch-recv", sourcePublic: "self", target: "job_recv",
		receiverSessionID: "RS", receiverDelegateID: "RD", createdAt: frozenTestTime,
	}
	jm.mu.Unlock()

	cfgs := jm.watchConfigSnapshotsWhere(func(cfg *watchConfig) bool {
		return watchConfigMatchesReceiver(cfg, "RS", "RD")
	})
	if len(cfgs) != 1 || cfgs[0].id != "watch-recv" {
		t.Fatalf("watchConfigSnapshotsWhere = %+v, want just the receiver-matching config", cfgs)
	}
	if !jm.mu.TryLock() {
		t.Fatal("watchConfigSnapshotsWhere returned with jm.mu still held")
	}
	jm.mu.Unlock()

	got := formatWatchSummaries(cfgs)
	want := jm.liveWatchSummariesForReceiver("RS", "RD")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("formatWatchSummaries(snapshots) = %+v, want liveWatchSummariesForReceiver's %+v", got, want)
	}
}

// TestVisibleWatchHistorySnapshotsCollectsUnderTheLock pins the history ring's
// collector: latest first, only the entries visible to the session, private
// values, and jm.mu free on return.
func TestVisibleWatchHistorySnapshotsCollectsUnderTheLock(t *testing.T) {
	t.Parallel()
	jm := newTestJM(t)
	seedWatchProjectionState(jm)
	jm.mu.Lock()
	jm.watchHistory = append(jm.watchHistory, watchHistoryEntry{
		id: "watch-hidden", source: "self", target: "job_hidden",
		receiverSessionID: "somebody_else", endReason: "cleared", endedAt: frozenTestTime,
	})
	jm.mu.Unlock()

	got := jm.visibleWatchHistorySnapshots(jm.sessionID)
	if len(got) != 1 || got[0].id != "watch-old" {
		t.Fatalf("visibleWatchHistorySnapshots = %+v, want only the session-visible entry", got)
	}
	if !jm.mu.TryLock() {
		t.Fatal("visibleWatchHistorySnapshots returned with jm.mu still held")
	}
	jm.mu.Unlock()

	want := formatRecentWatchSummaries(got)
	if live := jm.recentWatchSummaries(); !reflect.DeepEqual(live, want) {
		t.Fatalf("recentWatchSummaries = %+v, want the pure formatter's %+v", live, want)
	}
}

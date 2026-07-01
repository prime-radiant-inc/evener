package agent

import (
	"testing"
	"time"
)

// watchListToolResult enumerates the session-visible live watches, then the
// detached (terminal-flush) configs whose watch id is not already active, then
// the visible history — ordered by source then watch id. A config whose
// receiverSessionID differs from the manager's own session is invisible and is
// skipped in every rail.
func TestW2Watch_watchListToolResultVisibleToSession(t *testing.T) {
	jm := newTestJM(t) // sessionID "S1"

	// Two live watches share a source so the sort tie-breaks on watch id.
	liveA := &watchConfig{id: "wa", watchID: "wa", sourcePublic: "same", target: "job_a"}
	liveB := &watchConfig{id: "wb", watchID: "wb", sourcePublic: "same", target: "job_b"}
	// A watch owned by another receiver session is invisible.
	hidden := &watchConfig{id: "wh", watchID: "wh", sourcePublic: "aaa", target: "job_h", receiverSessionID: "OTHER"}

	jm.mu.Lock()
	jm.watches[watchKey{Target: "job_a"}] = liveA
	jm.watches[watchKey{Target: "job_b"}] = liveB
	jm.watches[watchKey{Target: "job_h", ReceiverSessionID: "OTHER"}] = hidden

	// terminalFlush: a visible detached config with a fresh watch id is included;
	// one whose watch id is already active is skipped; one with an empty watch id
	// is skipped; one owned by another session is skipped by visibility.
	flushFresh := &watchConfig{id: "wf", watchID: "wf", sourcePublic: "zzz", target: "job_f"}
	flushDup := &watchConfig{id: "wa", watchID: "wa", sourcePublic: "same", target: "job_a"}
	flushBlank := &watchConfig{id: "", watchID: "", sourcePublic: "blank", target: "job_x"}
	flushHidden := &watchConfig{id: "wy", watchID: "wy", sourcePublic: "yyy", target: "job_y", receiverSessionID: "OTHER"}
	jm.terminalFlush = map[*watchConfig]bool{flushFresh: true, flushDup: true, flushBlank: true, flushHidden: true}

	jm.watchHistory = []watchHistoryEntry{
		{id: "hv", source: "hist", target: "job_hv", endReason: "cleared", endedAt: time.Unix(2000, 0)},
		{id: "ho", source: "hist", target: "job_ho", receiverSessionID: "OTHER", endReason: "cleared", endedAt: time.Unix(2000, 0)},
	}
	jm.mu.Unlock()

	got := jm.watchListToolResult()

	// Two visible live watches plus one detached fresh flush make three.
	if got.Count != 3 {
		t.Fatalf("watch count = %d, want 3 (%+v)", got.Count, got.Watches)
	}
	// Ordered by source; the two "same"-source watches tie-break on watch id.
	if got.Watches[0].WatchID != "wa" || got.Watches[1].WatchID != "wb" {
		t.Fatalf("same-source watches not ordered by watch id: %+v", got.Watches)
	}
	if got.Watches[len(got.Watches)-1].Source != "zzz" {
		t.Fatalf("detached config not ordered last by source: %+v", got.Watches)
	}
	if len(got.RecentWatches) != 1 {
		t.Fatalf("recent watches = %d, want 1 visible (%+v)", len(got.RecentWatches), got.RecentWatches)
	}
}

// watchListToolResultForReceiver orders its live set by source then watch id;
// two matching watches that share a source must tie-break on watch id (the
// remaining uncovered comparator arm).
func TestW2Watch_watchListToolResultForReceiverWatchIDTieBreak(t *testing.T) {
	jm := newTestJM(t)
	const rsID, rdID = "RS", "RD"

	a := &watchConfig{id: "wa", watchID: "wa", sourcePublic: "same", target: "job_a", receiverSessionID: rsID, receiverDelegateID: rdID}
	b := &watchConfig{id: "wb", watchID: "wb", sourcePublic: "same", target: "job_b", receiverSessionID: rsID, receiverDelegateID: rdID}
	jm.mu.Lock()
	jm.watches[watchKey{Target: "job_a", ReceiverSessionID: rsID, ReceiverDelegateID: rdID}] = a
	jm.watches[watchKey{Target: "job_b", ReceiverSessionID: rsID, ReceiverDelegateID: rdID}] = b
	jm.mu.Unlock()

	got := jm.watchListToolResultForReceiver(rsID, rdID)
	if got.Count != 2 {
		t.Fatalf("watch count = %d, want 2 (%+v)", got.Count, got.Watches)
	}
	if got.Watches[0].WatchID != "wa" || got.Watches[1].WatchID != "wb" {
		t.Fatalf("same-source watches not ordered by watch id: %+v", got.Watches)
	}
}

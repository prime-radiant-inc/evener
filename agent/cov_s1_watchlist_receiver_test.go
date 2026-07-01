package agent

import (
	"testing"
	"time"
)

// watchListToolResultForReceiver enumerates only the live, terminal-flush, and
// historical watches that belong to the given receiver, skipping non-matching
// ones, and orders the live set by source then watch id.
func TestS1Cov_watchListToolResultForReceiver(t *testing.T) {
	jm := newTestJM(t)

	const rsID, rdID = "RS", "RD"

	// Empty receiver session id yields an empty result before any scan.
	if got := jm.watchListToolResultForReceiver("", ""); got.Count != 0 || len(got.Watches) != 0 {
		t.Fatalf("empty receiver → %+v, want empty", got)
	}

	matchA := &watchConfig{id: "wa", watchID: "wa", sourcePublic: "aaa", target: "job_a", receiverSessionID: rsID, receiverDelegateID: rdID}
	matchB := &watchConfig{id: "wb", watchID: "wb", sourcePublic: "bbb", target: "job_b", receiverSessionID: rsID, receiverDelegateID: rdID}
	other := &watchConfig{id: "wo", watchID: "wo", sourcePublic: "ccc", target: "job_c", receiverSessionID: "OTHER", receiverDelegateID: rdID}

	jm.mu.Lock()
	jm.watches[watchKey{Target: "job_a", ReceiverSessionID: rsID, ReceiverDelegateID: rdID}] = matchA
	jm.watches[watchKey{Target: "job_b", ReceiverSessionID: rsID, ReceiverDelegateID: rdID}] = matchB
	jm.watches[watchKey{Target: "job_c", ReceiverSessionID: "OTHER", ReceiverDelegateID: rdID}] = other

	// terminalFlush: a matching detached config (distinct watchID) is appended;
	// a non-matching one and one whose watchID is already active are skipped.
	flushMatch := &watchConfig{id: "wf", watchID: "wf", sourcePublic: "zzz", target: "job_f", receiverSessionID: rsID, receiverDelegateID: rdID}
	flushDup := &watchConfig{id: "wa", watchID: "wa", sourcePublic: "aaa", target: "job_a", receiverSessionID: rsID, receiverDelegateID: rdID}
	flushOther := &watchConfig{id: "wx", watchID: "wx", sourcePublic: "yyy", target: "job_x", receiverSessionID: "OTHER", receiverDelegateID: rdID}
	jm.terminalFlush = map[*watchConfig]bool{flushMatch: true, flushDup: true, flushOther: true}

	// watchHistory: one matching, one not.
	jm.watchHistory = []watchHistoryEntry{
		{id: "hm", source: "hist", target: "job_h", receiverSessionID: rsID, receiverDelegateID: rdID, endReason: "cleared", endedAt: time.Unix(2000, 0)},
		{id: "ho", source: "hist", target: "job_o", receiverSessionID: "OTHER", receiverDelegateID: rdID, endReason: "cleared", endedAt: time.Unix(2000, 0)},
	}
	jm.mu.Unlock()

	got := jm.watchListToolResultForReceiver(rsID, rdID)
	// matchA, matchB (live) + flushMatch (detached) = 3 watches, ordered by source.
	if got.Count != 3 {
		t.Fatalf("watch count = %d, want 3 (%+v)", got.Count, got.Watches)
	}
	if got.Watches[0].Source != "aaa" || got.Watches[len(got.Watches)-1].Source != "zzz" {
		t.Fatalf("watches not ordered by source: %+v", got.Watches)
	}
	if len(got.RecentWatches) != 1 {
		t.Fatalf("recent watches = %d, want 1 matching (%+v)", len(got.RecentWatches), got.RecentWatches)
	}
}

package agent

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
)

func equalRosterEvent(a, b appwire.RosterEvent) bool {
	return a.Kind == b.Kind && appwire.EqualAttentionEntry(a.AttentionEntry, b.AttentionEntry)
}

func TestHubRosterWatchCarriesPendingAskSnapshot(t *testing.T) {
	t.Parallel()
	root := newTestSession(t)
	jm := root.jobManager
	jm.enqueue = root.enqueueJobNotification
	watch, err := jm.configureHubRosterWatch(t.Context(), "local:manager", "")
	if err != nil {
		t.Fatal(err)
	}
	complete, truncated := false, true
	entry := appwire.AttentionEntry{Ref: "local:worker", ID: "thread-token", RosterInstanceID: "instance-token", Level: "needs_you", AskPending: true,
		PendingQuestion:   &appwire.PendingQuestion{Question: "q-token", Count: 3, TurnID: "turn-token", CallID: "call-token", Complete: &complete, Truncated: &truncated},
		AskBootGeneration: "boot-token", AskRevision: 4, CompositeList: []appwire.PendingAskCall{{TurnID: "turn-token", CallID: "call-token"}}, CompositeTotal: 2, CalllessCount: 1, AskPageable: true, PublishedAt: 1700000000123}
	if err := jm.onHubRoster(watch.WatchID, rosterUpdate(1, true, appwire.RosterEvent{AttentionEntry: entry, Kind: "needs_you"})); err != nil {
		t.Fatal(err)
	}
	sends := rosterTestEvents(t, jm, watch.WatchID)
	if len(sends) != 1 {
		t.Fatalf("pending sends=%d", len(sends))
	}
	rows := rosterFrameEntries(t, sends[0].Frame)
	if len(rows) != 1 || !appwire.EqualAttentionEntry(rows[0].AttentionEntry, entry) {
		t.Fatalf("frame lost ask snapshot: %+v", rows)
	}
}

func TestHubRosterQuestionListUsesCompositeCountAndCapability(t *testing.T) {
	t.Parallel()
	jm, id := rosterTestWatch(t)
	rows := []appwire.RosterEvent{
		{AttentionEntry: appwire.AttentionEntry{Ref: "local:many-questions", Level: "needs_you", AskPending: true, PendingQuestion: &appwire.PendingQuestion{Question: "q-token", Count: 30}, CompositeList: []appwire.PendingAskCall{{TurnID: "turn-token", CallID: "call-token"}}, CompositeTotal: 1}, Kind: "needs_you"},
		{AttentionEntry: appwire.AttentionEntry{Ref: "remote:old-daemon", Level: "needs_you", AskPending: true, CompositeList: []appwire.PendingAskCall{{TurnID: "other-turn", CallID: "other-call"}}, CompositeTotal: 11}, Kind: "needs_you"},
		{AttentionEntry: appwire.AttentionEntry{Ref: "local:flags-only", Level: "needs_you", AskPending: true}, Kind: "needs_you"},
	}
	if err := jm.onHubRoster(id, rosterUpdate(1, false, rows...)); err != nil {
		t.Fatal(err)
	}
	frame := rosterTestEvents(t, jm, id)[0].Frame
	var got []struct {
		AskPending      bool                     `json:"askPending"`
		PendingQuestion *appwire.PendingQuestion `json:"pendingQuestion"`
		TruncatedList   bool                     `json:"truncatedList"`
		ListReason      string                   `json:"listReason"`
	}
	if err := json.Unmarshal([]byte(rosterFrameField(frame, "roster_events: ")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].PendingQuestion == nil || got[0].PendingQuestion.Count != 30 || got[0].TruncatedList || got[0].ListReason != "" || !got[1].TruncatedList || got[1].ListReason != "unavailable" || !got[2].AskPending || got[2].PendingQuestion != nil {
		t.Fatalf("per-lane list facts=%+v", got)
	}
}

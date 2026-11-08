package agent

import (
	"encoding/json"
	"reflect"
	"strconv"
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
	var rawRows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rosterFrameField(frame, "roster_events: ")), &rawRows); err != nil {
		t.Fatal(err)
	}
	got := make([]struct {
		AskPending      bool
		PendingQuestion *appwire.PendingQuestion
		TruncatedList   bool
		ListReason      string
	}, len(rawRows))
	for i, fields := range rawRows {
		got[i].AskPending = rosterJSONFieldForTest[bool](t, fields, "askPending")
		got[i].PendingQuestion = rosterJSONFieldForTest[*appwire.PendingQuestion](t, fields, "pendingQuestion")
		got[i].TruncatedList = rosterJSONFieldForTest[bool](t, fields, "truncatedList")
		got[i].ListReason = rosterJSONFieldForTest[string](t, fields, "listReason")
	}
	if len(got) != 3 || got[0].PendingQuestion == nil || got[0].PendingQuestion.Count != 30 || got[0].TruncatedList || got[0].ListReason != "" || !got[1].TruncatedList || got[1].ListReason != "unavailable" || !got[2].AskPending || got[2].PendingQuestion != nil {
		t.Fatalf("per-lane list facts=%+v", got)
	}
}

func TestHubRosterLegacyCallListDeclaresUnavailableRemainder(t *testing.T) {
	t.Parallel()
	var calls []appwire.PendingAskCall
	for i := range appwire.MaxPendingAskComposites + 1 {
		calls = append(calls, appwire.PendingAskCall{TurnID: "turn-legacy", CallID: "call-" + strconv.Itoa(i)})
	}
	legacy := map[string]any{"version": 1, "instanceId": "instance-legacy", "revision": uint64(3), "calls": calls}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var pending appwire.PendingAskState
	if err := json.Unmarshal(raw, &pending); err != nil {
		t.Fatal(err)
	}
	if pending.CompositeTotal != 0 || pending.BootGeneration != "" || pending.CallsTruncated || len(pending.Calls) != appwire.MaxPendingAskComposites+1 {
		t.Fatalf("fixture is not a legacy complete list: %+v", pending)
	}
	jm, id := rosterTestWatch(t)
	entry := appwire.AttentionEntry{
		Ref: "remote:old-daemon", ID: "thread-legacy", RosterInstanceID: "instance-legacy",
		Level: "needs_you", AskPending: true, AskRevision: pending.Revision,
		PendingQuestion: &appwire.PendingQuestion{Question: "question-token", Count: 2 * len(pending.Calls)},
		CompositeList:   pending.Calls, CompositeTotal: pending.CompositeTotal,
	}
	if err := jm.onHubRoster(id, rosterUpdate(1, false, appwire.RosterEvent{AttentionEntry: entry, Kind: "needs_you"})); err != nil {
		t.Fatal(err)
	}
	sends := rosterTestEvents(t, jm, id)
	if len(sends) != 1 {
		t.Fatalf("roster sends=%d, want 1", len(sends))
	}
	rows := rosterFrameEntries(t, sends[0].Frame)
	var rawRows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rosterFrameField(sends[0].Frame, "roster_events: ")), &rawRows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("roster rows=%d, want 1", len(rows))
	}
	got := struct {
		appwire.AttentionEntry
		TruncatedList bool
		ListReason    string
	}{rows[0].AttentionEntry, rosterJSONFieldForTest[bool](t, rawRows[0], "truncatedList"), rosterJSONFieldForTest[string](t, rawRows[0], "listReason")}
	if !reflect.DeepEqual(got.CompositeList, calls[:appwire.MaxPendingAskComposites]) || got.CompositeTotal != len(calls) || !got.TruncatedList || got.ListReason != "unavailable" || got.AskPageable {
		t.Errorf("legacy remainder lost: total=%d calls=%d truncated=%v reason=%q pageable=%v", got.CompositeTotal, len(got.CompositeList), got.TruncatedList, got.ListReason, got.AskPageable)
	}
	if len(entry.CompositeList) != len(calls) || entry.CompositeTotal != 0 {
		t.Errorf("frame changed its input: %+v", entry)
	}
}

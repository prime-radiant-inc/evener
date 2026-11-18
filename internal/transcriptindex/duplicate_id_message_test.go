package transcriptindex

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/apptranscript"
)

// TestAReplacedAwaitingEntryAgreesOnTheResultItem covers the seam a duplicate
// provider call id opens between the two readers. An inherited assistant entry
// records a command and then a communicate under ONE id. A later assistant
// entry takes over as the turn's awaiting entry (stampFold advances it on every
// entry recording a call), so by the time the unnamed result arrives the index
// no longer resolves it from the announcing entry — it falls back to the
// pending-communicate state and indexes a MESSAGE. The whole-file read instead
// replays the announcing entry, where the first-part rule registers the
// command, and shows a command result.
//
// Whichever answer is right, both readers must give it: "what does a duplicate
// id mean here" cannot have one answer for the index and another for replay.
func TestAReplacedAwaitingEntryAgreesOnTheResultItem(t *testing.T) {
	inherited := func(turn schema.Turn) schema.Turn {
		turn.Authorship = &schema.TranscriptAuthorship{
			Author:    schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "parent"},
			Inherited: true,
		}
		return turn
	}
	fx := fixture{
		name:   "duplicate id replaced awaiting",
		header: transcript.Header{SessionID: "dup_replaced", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(opens("turn_dup", schema.TurnSpanExecution, user("go"))),
			// The announcing entry: a command, then a communicate, one id.
			entryLine(inTurn("turn_dup", inherited(assistant(
				call("call_dup", "read_file", `{"path":"a"}`),
				call("call_dup", "communicate", `{"message":"hi"}`),
			)))),
			// A later assistant entry takes over as the awaiting entry without
			// recording that id itself.
			entryLine(inTurn("turn_dup", assistant(call("call_other", "shell", `{"cmd":"true"}`)))),
			// The unnamed result for the reused id arrives after the takeover.
			entryLine(inTurn("turn_dup", results(result("call_dup", "", "payload")))),
			entryLine(completion("turn_dup", schema.TurnCompleted, 99, 10)),
		},
	}
	path := writeFixture(t, fx)
	indexed := turnItemsOf(t, allCandidates(t, openIndex(t, path, t.TempDir())), "turn_dup")
	replayed := replayedTurnOf(t, path, "turn_dup")

	indexedItems := itemShapes(indexed)
	replayedItems := itemShapes(replayed.Items)
	if len(indexedItems) != len(replayedItems) {
		t.Fatalf("item counts differ: index %v, replay %v", indexedItems, replayedItems)
	}
	for i := range indexedItems {
		if indexedItems[i] != replayedItems[i] {
			t.Fatalf("item %d differs: index %s, replay %s", i, indexedItems[i], replayedItems[i])
		}
	}
}

// turnItemsOf gathers one turn's items in page order. A candidate carries the
// turn shell plus its OWN item, so a turn's items do not live in any one
// candidate.
func turnItemsOf(t testing.TB, candidates []appitempaging.TranscriptItemCandidate, turnID string) []appwire.ThreadItem {
	t.Helper()
	var items []appwire.ThreadItem
	for _, c := range candidates {
		if c.TurnID == turnID {
			items = append(items, c.Item)
		}
	}
	if len(items) == 0 {
		t.Fatalf("no item of turn %s", turnID)
	}
	return items
}

// replayedTurnOf projects the file the way the hub's whole-file fallback does:
// ProjectTurn through ItemTurnsFromFile, flushed as every production caller
// flushes it.
func replayedTurnOf(t testing.TB, path, turnID string) appwire.Turn {
	t.Helper()
	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(path, testMaxLineBytes, func(turn schema.Turn, id string, entryIndex int) []appwire.ThreadItem {
		return apptranscript.ProjectTurn(id, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	apptranscript.FlushUnpairedCommunicates(&turns, reg)
	for _, turn := range turns {
		if turn.ID == turnID {
			return turn
		}
	}
	t.Fatalf("whole-file read has no turn %q: %s", turnID, dump(turns))
	return appwire.Turn{}
}

// itemShapes is the comparable identity of an item: what it claims to BE, not
// its wording, so a difference in type or name shows and a difference in
// formatting does not.
func itemShapes(items []appwire.ThreadItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Type+"/"+item.ToolName+"/"+item.CallID+"/"+item.Status)
	}
	return out
}

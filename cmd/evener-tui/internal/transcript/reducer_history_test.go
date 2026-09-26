package transcript

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// ApplyHistoryItem is the entry point for history/updated items: always the
// final, recorded form of an entry (the in-progress form, if any, lives in
// the overlay), and merged by version so a stale replay is dropped.

// A user row carries its item's TranscriptKey (the read model's fork
// divergence identity, appwire.ThreadForkParams' eventual replacement for the
// entry-index SourceTurnID), stamped the same way TranscriptEntryIndex is.

func TestApplyThreadItemStampsTranscriptKeyOnUserMessage(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type: "userMessage", ID: "item_1", TurnID: "turn_1", Text: "hi", TranscriptKey: "apptranscript-item-v2:turn_1:0:0",
	}, 1, true)

	if len(r.messages) != 1 || r.messages[0].TranscriptKey != "apptranscript-item-v2:turn_1:0:0" {
		t.Fatalf("messages = %+v", r.messages)
	}
}

func TestApplyHistoryItemAddsAgentMessage(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyHistoryItem(appwire.ThreadItem{
		Type: "agentMessage", ID: "item_1", TurnID: "turn_1", Text: "hello", Version: 3,
	}, 1)

	if len(r.messages) != 1 || r.messages[0].Kind != MsgAssistant || r.messages[0].Text != "hello" {
		t.Fatalf("messages = %+v", r.messages)
	}
}

func TestApplyHistoryItemDropsStaleVersion(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyHistoryItem(appwire.ThreadItem{Type: "agentMessage", ID: "item_1", TurnID: "turn_1", Text: "final", Version: 5}, 1)
	r.ApplyHistoryItem(appwire.ThreadItem{Type: "agentMessage", ID: "item_1", TurnID: "turn_1", Text: "STALE REPLAY", Version: 2}, 1)

	if len(r.messages) != 1 || r.messages[0].Text != "final" {
		t.Fatalf("stale version should be dropped: %+v", r.messages)
	}
}

func TestApplyHistoryItemAppliesNewerVersion(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyHistoryItem(appwire.ThreadItem{Type: "commandExecution", ID: "item_1", CallID: "call_1", TurnID: "turn_1", ToolName: "shell", Status: appwire.TurnStatusInProgress, Version: 2}, 1)
	r.ApplyHistoryItem(appwire.ThreadItem{Type: "commandExecution", ID: "item_1", CallID: "call_1", TurnID: "turn_1", ToolName: "shell", Output: "done", Status: appwire.TurnStatusCompleted, Version: 4}, 1)

	if len(r.messages) != 1 {
		t.Fatalf("want one merged tool row, got %+v", r.messages)
	}
	if r.messages[0].Tool == nil || r.messages[0].Tool.Output != "done" || !r.messages[0].Tool.Done {
		t.Fatalf("newer version should have applied: %+v", r.messages[0].Tool)
	}
}

// ApplyOverlayItem (overlay/upserted): stream, preview, tool and notice kinds.

func TestApplyOverlayItemStreamAddsAssistantRow(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "stream:round_1/0:agentMessage", Kind: appwire.OverlayStream,
		TurnID: "turn_1", RoundID: "round_1", StreamID: "round_1/0",
		Item: appwire.ThreadItem{Type: "agentMessage", ID: "stream:round_1/0:agentMessage", Text: "thinking out loud"},
	})

	if len(r.messages) != 1 || r.messages[0].Kind != MsgAssistant || r.messages[0].Text != "thinking out loud" {
		t.Fatalf("messages = %+v", r.messages)
	}
	if r.messages[0].OverlayKind != appwire.OverlayStream || r.messages[0].RoundID != "round_1" || r.messages[0].StreamID != "round_1/0" {
		t.Fatalf("overlay bookkeeping not stamped: %+v", r.messages[0])
	}
}

func TestApplyOverlayItemStreamReasoningAddsReasoningRow(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "stream:round_1/0:reasoning", Kind: appwire.OverlayStream,
		TurnID: "turn_1", RoundID: "round_1", StreamID: "round_1/0",
		Item: appwire.ThreadItem{Type: "reasoning", ID: "stream:round_1/0:reasoning", Text: "pondering"},
	})

	if len(r.messages) != 1 || r.messages[0].Kind != MsgReasoning || r.messages[0].Text != "pondering" {
		t.Fatalf("messages = %+v", r.messages)
	}
}

func TestApplyOverlayItemToolMergesOntoHistoryRowByCallID(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	// The tool overlay item arrives first, running.
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "tool:key1", Kind: appwire.OverlayTool, TurnID: "turn_1", RoundID: "round_1", CallID: "call_1",
		Item: appwire.ThreadItem{Type: "commandExecution", ID: "tool:key1", CallID: "call_1", ToolName: "shell", Status: appwire.TurnStatusInProgress},
	})
	if len(r.messages) != 1 || r.messages[0].Tool == nil || r.messages[0].Tool.Done {
		t.Fatalf("expected one running tool row: %+v", r.messages)
	}
	// The real history item, once recorded, has a different real item id but the same call id.
	r.ApplyHistoryItem(appwire.ThreadItem{
		Type: "commandExecution", ID: "real_item_9", CallID: "call_1", ToolName: "shell",
		Output: "ok", Status: appwire.TurnStatusCompleted, Version: 7,
	}, 1)

	if len(r.messages) != 1 {
		t.Fatalf("history item should merge onto the overlay's row, not add a second one: %+v", r.messages)
	}
	if !r.messages[0].Tool.Done || r.messages[0].Tool.Output != "ok" {
		t.Fatalf("history completion should apply: %+v", r.messages[0].Tool)
	}
}

func TestApplyOverlayItemNoticeAddsSystemMessage(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "notice:0", Kind: appwire.OverlayNotice, TurnID: "turn_1",
		Item: appwire.ThreadItem{Type: "systemMessage", ID: "notice:0", Text: "context compacted"},
	})

	if len(r.messages) != 1 || r.messages[0].Kind != MsgSystem || r.messages[0].Text != "context compacted" {
		t.Fatalf("messages = %+v", r.messages)
	}
}

// ApplyOverlayDelta (overlay/delta): incremental text/output chunks.

func TestApplyOverlayDeltaAppendsStreamText(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "stream:round_1/0:agentMessage", Kind: appwire.OverlayStream, TurnID: "turn_1", RoundID: "round_1", StreamID: "round_1/0",
		Item: appwire.ThreadItem{Type: "agentMessage", ID: "stream:round_1/0:agentMessage", Text: "hel"},
	})
	r.ApplyOverlayDelta("stream:round_1/0:agentMessage", appwire.OverlayDeltaText, "lo")

	if r.messages[0].Text != "hello" {
		t.Fatalf("delta should append: %q", r.messages[0].Text)
	}
}

func TestApplyOverlayDeltaAppendsToolOutput(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "tool:key1", Kind: appwire.OverlayTool, TurnID: "turn_1", CallID: "call_1",
		Item: appwire.ThreadItem{Type: "commandExecution", ID: "tool:key1", CallID: "call_1", ToolName: "shell", Status: appwire.TurnStatusInProgress},
	})
	r.ApplyOverlayDelta("tool:key1", appwire.OverlayDeltaOutput, "line1\n")
	r.ApplyOverlayDelta("tool:key1", appwire.OverlayDeltaOutput, "line2\n")

	if r.messages[0].Tool.Output != "line1\nline2\n" {
		t.Fatalf("tool output = %q", r.messages[0].Tool.Output)
	}
}

// ApplyOverlayReset (overlay/reset): discard one attempt's stream/preview.

func TestApplyOverlayResetDiscardsStreamAndPreview(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "stream:round_1/0:agentMessage", Kind: appwire.OverlayStream, RoundID: "round_1", StreamID: "round_1/0",
		Item: appwire.ThreadItem{Type: "agentMessage", ID: "stream:round_1/0:agentMessage", Text: "partial"},
	})
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "preview:call_1", Kind: appwire.OverlayPreview, RoundID: "round_1", StreamID: "round_1/0", CallID: "call_1",
		Item: appwire.ThreadItem{Type: "agentMessage", ID: "preview:call_1", Text: "prev"},
	})
	if len(r.messages) != 2 {
		t.Fatalf("setup: want 2 messages, got %+v", r.messages)
	}

	r.ApplyOverlayReset("round_1/0")

	if len(r.messages) != 0 {
		t.Fatalf("reset should discard both rows of the attempt: %+v", r.messages)
	}
}

func TestApplyOverlayResetLeavesOtherStreamsAlone(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "stream:round_1/0:agentMessage", Kind: appwire.OverlayStream, RoundID: "round_1", StreamID: "round_1/0",
		Item: appwire.ThreadItem{Type: "agentMessage", ID: "stream:round_1/0:agentMessage", Text: "kept"},
	})

	r.ApplyOverlayReset("round_2/0")

	if len(r.messages) != 1 || r.messages[0].Text != "kept" {
		t.Fatalf("unrelated stream should survive: %+v", r.messages)
	}
}

// ApplyOverlayEnd (overlay/end): drop any leftover stream/preview rows for
// the round (already covered by a recorded item, or by the interrupted
// notice the server sends ahead of overlay/end).

func TestApplyOverlayEndDropsLeftoverStreamRows(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "stream:round_1/0:agentMessage", Kind: appwire.OverlayStream, RoundID: "round_1", StreamID: "round_1/0",
		Item: appwire.ThreadItem{Type: "agentMessage", ID: "stream:round_1/0:agentMessage", Text: "gone"},
	})

	r.ApplyOverlayEnd("round_1")

	if len(r.messages) != 0 {
		t.Fatalf("overlay/end should drop the leftover row: %+v", r.messages)
	}
}

func TestApplyOverlayEndLeavesToolRowsAlone(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "tool:key1", Kind: appwire.OverlayTool, RoundID: "round_1", CallID: "call_1",
		Item: appwire.ThreadItem{Type: "commandExecution", ID: "tool:key1", CallID: "call_1", ToolName: "shell", Status: appwire.TurnStatusInProgress},
	})

	r.ApplyOverlayEnd("round_1")

	if len(r.messages) != 1 {
		t.Fatalf("a tool row is never overlay-tagged and must survive round end: %+v", r.messages)
	}
}

// ApplyHistoryItem proactively drops a covered stream row (the round's
// ASSISTANT/salvage entry recorded, so the overlay's copy is superseded)
// without waiting for overlay/end, so the transcript never shows both at once.

func TestApplyHistoryItemDropsCoveredStreamRow(t *testing.T) {
	r := NewTranscriptReducer(nil, nil, nil)
	r.ApplyOverlayItem(appwire.OverlayItem{
		Key: "stream:round_1/0:agentMessage", Kind: appwire.OverlayStream, RoundID: "round_1", StreamID: "round_1/0",
		Item: appwire.ThreadItem{Type: "agentMessage", ID: "stream:round_1/0:agentMessage", Text: "streamed so far"},
	})
	r.ApplyHistoryItem(appwire.ThreadItem{
		Type: "agentMessage", ID: "real_item_1", TurnID: "turn_1", RoundID: "round_1", Text: "streamed so far, in full", Version: 9,
	}, 1)

	if len(r.messages) != 1 || r.messages[0].ItemID != "real_item_1" {
		t.Fatalf("the recorded item should replace the overlay stream row: %+v", r.messages)
	}
}

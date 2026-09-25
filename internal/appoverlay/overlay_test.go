package appoverlay

import (
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func newOverlay() *Overlay {
	return New(NewBudget(DefaultBudgetBytes))
}

func textPart(text string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentText, Text: text}
}

func callPart(id, name string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: id, Name: name}}
}

func assistantRecord(ordinal uint64, turnID, roundID string, parts ...llm.ContentPart) transcript.Record {
	return transcript.Record{Recorded: true, Ordinal: ordinal, Turn: schema.Turn{
		Kind:    schema.TurnAssistant,
		TurnID:  turnID,
		RoundID: roundID,
		Message: llm.Message{Role: llm.RoleAssistant, Content: parts},
	}}
}

func toolResultsRecord(ordinal uint64, turnID string, callIDs ...string) transcript.Record {
	parts := make([]llm.ContentPart, 0, len(callIDs))
	for _, id := range callIDs {
		parts = append(parts, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: id, Content: "ok"}})
	}
	return transcript.Record{Recorded: true, Ordinal: ordinal, Turn: schema.Turn{
		Kind:    schema.TurnToolResults,
		TurnID:  turnID,
		Message: llm.Message{Role: llm.RoleTool, Content: parts},
	}}
}

func communicateRecord(ordinal uint64, turnID, callID string) transcript.Record {
	return transcript.Record{Recorded: true, Ordinal: ordinal, Turn: schema.Turn{
		Kind:        schema.TurnCommunicate,
		TurnID:      turnID,
		Communicate: &schema.CommunicateInfo{CallID: callID, Message: "hi"},
	}}
}

func upserted(t *testing.T, change Change) appwire.OverlayItem {
	t.Helper()
	params, ok := change.Params.(appwire.OverlayUpsertedParams)
	if change.Method != appwire.NotifyOverlayUpserted || !ok {
		t.Fatalf("change = %s %T, want %s", change.Method, change.Params, appwire.NotifyOverlayUpserted)
	}
	if params.ThreadID != "" || params.Ref != "" {
		t.Fatalf("upsert carries thread %q ref %q; the server stamps them", params.ThreadID, params.Ref)
	}
	return params.Item
}

func delta(t *testing.T, change Change) appwire.OverlayDeltaParams {
	t.Helper()
	params, ok := change.Params.(appwire.OverlayDeltaParams)
	if change.Method != appwire.NotifyOverlayDelta || !ok {
		t.Fatalf("change = %s %T, want %s", change.Method, change.Params, appwire.NotifyOverlayDelta)
	}
	return params
}

func one(t *testing.T, changes []Change) Change {
	t.Helper()
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want exactly one", changes)
	}
	return changes[0]
}

func none(t *testing.T, changes []Change) {
	t.Helper()
	if len(changes) != 0 {
		t.Fatalf("changes = %+v, want none", changes)
	}
}

func snapshotKeys(o *Overlay) []string {
	var keys []string
	for _, item := range o.Snapshot() {
		keys = append(keys, item.Key)
	}
	return keys
}

func TestStreamDeltasUpsertTheAttemptThenAppend(t *testing.T) {
	o := newOverlay()
	none(t, o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"})))
	none(t, o.Event(events.New(events.RoundStartedData{RoundID: "r_1"})))

	item := upserted(t, one(t, o.Event(events.New(events.AssistantTextDeltaData{Delta: "he"}))))
	want := appwire.OverlayItem{
		Key:      "stream:r_1/1:agentMessage",
		Kind:     appwire.OverlayStream,
		TurnID:   "t_1",
		RoundID:  "r_1",
		StreamID: "r_1/1",
		Item:     appwire.ThreadItem{Type: "agentMessage", ID: "stream:r_1/1:agentMessage", TurnID: "t_1", RoundID: "r_1", Text: "he", Status: appwire.TurnStatusInProgress},
	}
	if item.Key != want.Key || item.Kind != want.Kind || item.TurnID != want.TurnID || item.RoundID != want.RoundID || item.StreamID != want.StreamID ||
		item.Item.Type != want.Item.Type || item.Item.ID != want.Item.ID || item.Item.TurnID != want.Item.TurnID ||
		item.Item.RoundID != want.Item.RoundID || item.Item.Text != want.Item.Text || item.Item.Status != want.Item.Status {
		t.Fatalf("first delta upserted %+v, want %+v", item, want)
	}

	got := delta(t, one(t, o.Event(events.New(events.AssistantTextDeltaData{Delta: "llo"}))))
	if got != (appwire.OverlayDeltaParams{Key: "stream:r_1/1:agentMessage", Field: appwire.OverlayDeltaText, Delta: "llo"}) {
		t.Fatalf("second delta = %+v", got)
	}

	reasoning := upserted(t, one(t, o.Event(events.New(events.ReasoningSummaryDeltaData{Delta: "think"}))))
	if reasoning.Key != "stream:r_1/1:reasoning" || reasoning.Item.Type != "reasoning" || reasoning.StreamID != "r_1/1" {
		t.Fatalf("reasoning delta upserted %+v", reasoning)
	}

	snapshot := o.Snapshot()
	if len(snapshot) != 2 || snapshot[0].Item.Text != "hello" || snapshot[1].Item.Text != "think" {
		t.Fatalf("snapshot = %+v, want the text stream holding hello then the reasoning stream", snapshot)
	}
}

func TestResetDiscardsTheAttemptAndItsPreviewsThenStreamsTheNextAttempt(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "first"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_1"}))

	change := one(t, o.Event(events.New(events.AssistantTextResetData{})))
	if params, ok := change.Params.(appwire.OverlayResetParams); change.Method != appwire.NotifyOverlayReset || !ok || params.StreamID != "r_1/1" {
		t.Fatalf("reset change = %s %+v, want overlay/reset of r_1/1", change.Method, change.Params)
	}
	if keys := snapshotKeys(o); len(keys) != 0 {
		t.Fatalf("snapshot after reset = %v, want empty", keys)
	}

	item := upserted(t, one(t, o.Event(events.New(events.AssistantTextDeltaData{Delta: "second"}))))
	if item.Key != "stream:r_1/2:agentMessage" || item.StreamID != "r_1/2" || item.Item.Text != "second" {
		t.Fatalf("next attempt upserted %+v, want a new stream r_1/2", item)
	}
}

func TestResetWithNothingStreamedEmitsNothingButStillStartsANewAttempt(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	none(t, o.Event(events.New(events.AssistantTextResetData{})))
	item := upserted(t, one(t, o.Event(events.New(events.AssistantTextDeltaData{Delta: "x"}))))
	if item.StreamID != "r_1/2" {
		t.Fatalf("stream after an empty reset = %q, want r_1/2", item.StreamID)
	}
}

func TestTheRoundsAssistantRecordCoversItsStreamsAndLaterDeltas(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "hello"}))
	o.Event(events.New(events.ReasoningSummaryDeltaData{Delta: "think"}))

	o.Recorded(assistantRecord(3, "t_1", "r_1", textPart("hello")))

	if o.Contains("stream:r_1/1:agentMessage") || o.Contains("stream:r_1/1:reasoning") {
		t.Fatal("the round's streams survived its ASSISTANT record")
	}
	none(t, o.Event(events.New(events.AssistantTextDeltaData{Delta: "late"})))
	none(t, o.Event(events.New(events.ReasoningSummaryDeltaData{Delta: "late"})))
	if keys := snapshotKeys(o); len(keys) != 0 {
		t.Fatalf("snapshot = %v, want empty", keys)
	}
}

func TestARecordOfAnotherRoundLeavesTheStreamAlone(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_2"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "hello"}))
	o.Recorded(assistantRecord(3, "t_1", "r_1", textPart("earlier")))
	if !o.Contains("stream:r_2/1:agentMessage") {
		t.Fatal("a record of round r_1 covered round r_2's stream")
	}
}

func TestAnUnrecordedRecordChangesNothing(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "hello"}))
	record := assistantRecord(3, "t_1", "r_1", textPart("hello"))
	record.Recorded = false
	o.Recorded(record)
	if !o.Contains("stream:r_1/1:agentMessage") {
		t.Fatal("a record that is not in the file covered the stream")
	}
	item := upserted(t, one(t, o.Event(events.New(events.LoopDetectionData{Message: "loop"}))))
	if item.Anchor.Entry != 0 {
		t.Fatalf("anchor = %+v; an unrecorded record must not advance it", item.Anchor)
	}
}

func TestAPreviewSurvivesTheAssistantRecordUntilItsCommunicate(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"}))
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))

	item := upserted(t, one(t, o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_1"}))))
	if item.Key != "preview:c_1" || item.Kind != appwire.OverlayPreview || item.CallID != "c_1" || item.RoundID != "r_1" ||
		item.StreamID != "r_1/1" || item.TurnID != "t_1" || item.Item.Type != "agentMessage" || item.Item.CallID != "c_1" ||
		item.Item.ID != "preview:c_1" || item.Item.Status != appwire.TurnStatusInProgress {
		t.Fatalf("preview upserted %+v", item)
	}
	got := delta(t, one(t, o.Event(events.New(events.CommunicatePreviewDeltaData{CallID: "c_1", Delta: "hi"}))))
	if got != (appwire.OverlayDeltaParams{Key: "preview:c_1", Field: appwire.OverlayDeltaText, Delta: "hi"}) {
		t.Fatalf("preview delta = %+v", got)
	}

	o.Recorded(assistantRecord(3, "t_1", "r_1", callPart("c_1", "communicate")))
	snapshot := o.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Key != "preview:c_1" || snapshot[0].Item.Text != "hi" {
		t.Fatalf("snapshot after the ASSISTANT record = %+v, want the preview", snapshot)
	}

	o.Recorded(communicateRecord(4, "t_1", "c_1"))
	if o.Contains("preview:c_1") {
		t.Fatal("the COMMUNICATE record left its preview")
	}
	none(t, o.Event(events.New(events.CommunicatePreviewDeltaData{CallID: "c_1", Delta: "late"})))
	none(t, o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_1"})))
}

func TestAPreviewResetRemovesItWithoutANotification(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_1"}))
	none(t, o.Event(events.New(events.CommunicatePreviewResetData{CallID: "c_1"})))
	if o.Contains("preview:c_1") {
		t.Fatal("the reset preview is still in the overlay")
	}
}

func TestToolStateIsKeyedByTheHistoryKeyTheAssistantRecordTeaches(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"}))
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Recorded(assistantRecord(4, "t_1", "r_1", textPart("running"), callPart("c_1", "shell")))
	historyKey := transcriptindex.ItemKey("t_1", appwire.ThreadItemPosition{Entry: 5, Item: 1})

	item := upserted(t, one(t, o.Event(events.New(events.ToolCallStartData{ToolName: "shell", CallID: "c_1", ArgumentsJSON: `{"cmd":"ls"}`}))))
	if item.Key != "tool:"+historyKey || item.Kind != appwire.OverlayTool || item.HistoryKey != historyKey || item.CallID != "c_1" ||
		item.RoundID != "r_1" || item.TurnID != "t_1" || item.Item.Type != "commandExecution" || item.Item.CallID != "c_1" ||
		item.Item.Status != appwire.TurnStatusInProgress {
		t.Fatalf("tool start upserted %+v", item)
	}

	got := delta(t, one(t, o.Event(events.New(events.ToolCallOutputDeltaData{ToolName: "shell", CallID: "c_1", Delta: "out"}))))
	if got != (appwire.OverlayDeltaParams{Key: "tool:" + historyKey, Field: appwire.OverlayDeltaOutput, Delta: "out"}) {
		t.Fatalf("output delta = %+v", got)
	}

	ended := upserted(t, one(t, o.Event(events.New(events.ToolCallEndData{ToolName: "shell", CallID: "c_1", Output: "out", Error: "exit 1"}))))
	if ended.Key != "tool:"+historyKey || ended.Item.Status != apptranscript.SettledToolStatus(true) || ended.Item.Error != "exit 1" || ended.Item.Output != "out" {
		t.Fatalf("tool end upserted %+v", ended)
	}

	o.Recorded(toolResultsRecord(5, "t_1", "c_1"))
	if o.Contains("tool:" + historyKey) {
		t.Fatal("TOOL_RESULTS left the call's tool state")
	}
	none(t, o.Event(events.New(events.ToolCallOutputDeltaData{ToolName: "shell", CallID: "c_1", Delta: "late"})))
	none(t, o.Event(events.New(events.ToolCallEndData{ToolName: "shell", CallID: "c_1"})))
}

func TestAToolWithNoKnownHistoryKeyIsKeyedByItsCallID(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	item := upserted(t, one(t, o.Event(events.New(events.ToolCallStartData{ToolName: "shell", CallID: "c_9"}))))
	if item.Key != "tool:call:c_9" || item.HistoryKey != "" || item.RoundID != "r_1" {
		t.Fatalf("tool start upserted %+v, want tool:call:c_9", item)
	}
}

func TestCommunicateCallsHaveNoToolState(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	none(t, o.Event(events.New(events.ToolCallStartData{ToolName: "communicate", CallID: "c_1"})))
	none(t, o.Event(events.New(events.ToolCallEndData{ToolName: "communicate", CallID: "c_1"})))
}

func TestHeldToolResultImagesArriveWhenPersisted(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.ToolCallStartData{ToolName: "read_file", CallID: "c_1"}))
	images := []events.OutputImage{
		{Source: events.OutputImageSourceToolResult, SHA: "sha1", MediaType: "image/png"},
		{Source: "file", URL: "/doc/image/a.png", SHA: "sha2"},
	}
	ended := upserted(t, one(t, o.Event(events.New(events.ToolCallEndData{ToolName: "read_file", CallID: "c_1", OutputImages: images}))))
	if len(ended.Item.OutputImages) != 1 || ended.Item.OutputImages[0].SHA != "sha2" {
		t.Fatalf("settled images = %+v, want only the fetchable one", ended.Item.OutputImages)
	}

	none(t, o.Event(events.New(events.ToolResultImagesPersistedData{CallIDs: []string{"c_other"}})))
	released := upserted(t, one(t, o.Event(events.New(events.ToolResultImagesPersistedData{CallIDs: []string{"c_1"}}))))
	if len(released.Item.OutputImages) != 2 || released.Item.OutputImages[0].SHA != "sha1" {
		t.Fatalf("released images = %+v, want both", released.Item.OutputImages)
	}
	none(t, o.Event(events.New(events.ToolResultImagesPersistedData{CallIDs: []string{"c_1"}})))
}

func TestRunningOutputKeepsTheLast256KBAtARuneBoundary(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.ToolCallStartData{ToolName: "shell", CallID: "c_1"}))

	// 4097-byte chunks of three-byte runes, so the cut lands inside a rune.
	chunk := strings.Repeat("€", 1365) + "ab"
	var full strings.Builder
	trimmed := false
	for full.Len() < 1<<20 {
		full.WriteString(chunk)
		change := one(t, o.Event(events.New(events.ToolCallOutputDeltaData{ToolName: "shell", CallID: "c_1", Delta: chunk})))
		if full.Len() <= maxRunningOutputBytes {
			delta(t, change)
			continue
		}
		item := upserted(t, change)
		if len(item.Item.Output) > maxRunningOutputBytes || !utf8.ValidString(item.Item.Output) {
			t.Fatalf("trimmed output is %d bytes, valid UTF-8 %v", len(item.Item.Output), utf8.ValidString(item.Item.Output))
		}
		trimmed = true
	}
	if !trimmed {
		t.Fatal("1 MB of output never trimmed")
	}
	output := o.Snapshot()[0].Item.Output
	if len(output) > maxRunningOutputBytes || len(output) < maxRunningOutputBytes-utf8.UTFMax || !utf8.ValidString(output) {
		t.Fatalf("held output is %d bytes (valid %v), want the last 256 KB", len(output), utf8.ValidString(output))
	}
	if !strings.HasSuffix(full.String(), output) {
		t.Fatal("held output is not the tail of what streamed")
	}
}

func TestSettledOutputIsCappedToo(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	ended := upserted(t, one(t, o.Event(events.New(events.ToolCallEndData{ToolName: "shell", CallID: "c_1", Output: strings.Repeat("x", 1<<20)}))))
	if len(ended.Item.Output) != maxRunningOutputBytes {
		t.Fatalf("settled output is %d bytes, want %d", len(ended.Item.Output), maxRunningOutputBytes)
	}
}

func TestARoundEndingWithUnrecordedContentCollapsesIntoOneInterruptedNotice(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"}))
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.ReasoningSummaryDeltaData{Delta: "thinking"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "partial"}))
	o.Event(events.New(events.ToolCallStartData{ToolName: "shell", CallID: "c_1"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_2"}))

	changes := o.Event(events.New(events.RoundEndedData{RoundID: "r_1"}))
	if len(changes) != 2 {
		t.Fatalf("round end changes = %+v, want an interrupted notice then overlay/end", changes)
	}
	notice := upserted(t, changes[0])
	if notice.Kind != appwire.OverlayNotice || notice.Item.EventKind != appwire.ThreadItemEventKindInterrupted || notice.Item.Type != "systemMessage" || notice.TurnID != "t_1" {
		t.Fatalf("notice = %+v, want an interrupted systemMessage", notice)
	}
	if params, ok := changes[1].Params.(appwire.OverlayEndParams); changes[1].Method != appwire.NotifyOverlayEnd || !ok || params.RoundID != "r_1" {
		t.Fatalf("second change = %s %+v, want overlay/end r_1", changes[1].Method, changes[1].Params)
	}
	if keys := snapshotKeys(o); len(keys) != 1 || keys[0] != notice.Key {
		t.Fatalf("snapshot after round end = %v, want only the notice", keys)
	}
}

func TestARoundEndingWithEverythingRecordedOnlyEnds(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "text"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_2"}))
	o.Recorded(assistantRecord(0, "t_1", "r_1", textPart("text"), callPart("c_1", "shell"), callPart("c_2", "communicate")))
	o.Event(events.New(events.ToolCallStartData{ToolName: "shell", CallID: "c_1"}))
	o.Recorded(toolResultsRecord(1, "t_1", "c_1", "c_2"))

	change := one(t, o.Event(events.New(events.RoundEndedData{RoundID: "r_1"})))
	if params, ok := change.Params.(appwire.OverlayEndParams); change.Method != appwire.NotifyOverlayEnd || !ok || params.RoundID != "r_1" {
		t.Fatalf("round end = %s %+v, want only overlay/end r_1", change.Method, change.Params)
	}
	if keys := snapshotKeys(o); len(keys) != 0 {
		t.Fatalf("snapshot after round end = %v, want empty (the uncommunicated preview is dropped)", keys)
	}
}

func TestARoundEndingWithOnlyAResetAttemptOnlyEnds(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "discarded"}))
	o.Event(events.New(events.AssistantTextResetData{}))
	one(t, o.Event(events.New(events.RoundEndedData{RoundID: "r_1"})))
}

func TestExecutionEventsSetTheRunningTurn(t *testing.T) {
	o := newOverlay()
	if o.RunningTurnID() != "" {
		t.Fatalf("running turn = %q before any execution", o.RunningTurnID())
	}
	o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"}))
	if o.RunningTurnID() != "t_1" {
		t.Fatalf("running turn = %q, want t_1", o.RunningTurnID())
	}
	o.Event(events.New(events.ExecutionEndedData{TurnID: "t_1", Status: "completed"}))
	if o.RunningTurnID() != "" {
		t.Fatalf("running turn = %q after its end", o.RunningTurnID())
	}
}

func TestEventsTheOverlayDoesNotHoldReturnNothing(t *testing.T) {
	o := newOverlay()
	for _, data := range []events.EventData{
		events.QueueChangedData{Depth: 1},
		events.UserInputData{Text: "hi"},
		events.AssistantTextEndData{Text: "done"},
		events.ErrorData{Error: "recorded failure", Recorded: true},
	} {
		none(t, o.Event(events.New(data)))
	}
	// Streams need a round to belong to.
	none(t, o.Event(events.New(events.AssistantTextDeltaData{Delta: "orphan"})))
	if keys := snapshotKeys(o); len(keys) != 0 {
		t.Fatalf("snapshot = %v, want empty", keys)
	}
}

func TestSnapshotHoldsStreamsPreviewsToolsAndNotices(t *testing.T) {
	o := newOverlay()
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "text"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_1"}))
	o.Event(events.New(events.ToolCallStartData{ToolName: "shell", CallID: "c_2"}))
	o.Event(events.New(events.WarningData{Message: "careful"}))

	kinds := map[appwire.OverlayKind]string{}
	for _, item := range o.Snapshot() {
		kinds[item.Kind] = item.Key
		if !o.Contains(item.Key) {
			t.Fatalf("Contains(%q) = false for a snapshot item", item.Key)
		}
	}
	if len(kinds) != 4 {
		t.Fatalf("snapshot kinds = %v, want stream, preview, tool and notice", kinds)
	}
	if o.Contains("stream:r_9/1:agentMessage") {
		t.Fatal("Contains reports a key the overlay never held")
	}
}

func TestRecordedAndEventRunConcurrently(t *testing.T) {
	o := newOverlay()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 200 {
			o.Recorded(assistantRecord(uint64(i), "t_1", "r_1", textPart("x"), callPart("c_1", "shell")))
			o.Recorded(toolResultsRecord(uint64(i), "t_1", "c_1"))
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
			o.Event(events.New(events.AssistantTextDeltaData{Delta: "x"}))
			o.Event(events.New(events.ToolCallStartData{ToolName: "shell", CallID: "c_1"}))
			o.Event(events.New(events.ToolCallOutputDeltaData{ToolName: "shell", CallID: "c_1", Delta: "y"}))
			o.Event(events.New(events.LoopDetectionData{Message: "loop"}))
			o.Snapshot()
			o.Event(events.New(events.RoundEndedData{RoundID: "r_1"}))
		}
	}()
	wg.Wait()
}

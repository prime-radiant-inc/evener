package appoverlay

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestQuietCommunicateRetiresCallPreviewWithoutDisturbingRoundCoverage(t *testing.T) {
	t.Parallel()
	o := newOverlay()
	t.Cleanup(o.Close)
	quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true}`)

	o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"}))
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.ReasoningSummaryDeltaData{Delta: "thought"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "companion prose"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_other"}))
	o.Event(events.New(events.CommunicatePreviewDeltaData{CallID: "c_other", Delta: "keep me"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_q"}))
	o.Event(events.New(events.CommunicatePreviewDeltaData{CallID: "c_q", Delta: "drop preview"}))

	// Runtime persists TurnAssistant before execToolBatch runs communicate.
	o.Recorded(assistantRecord(0, "t_1", "r_1", textPart("companion prose"), llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought"}}, callPart("c_other", "communicate"), callPart("c_q", "communicate")))

	// Late assistant text delta in the covered round must be dropped.
	none(t, o.Event(events.New(events.AssistantTextDeltaData{Delta: "late delta"})))

	o.Event(events.New(events.ToolCallStartData{CallID: "c_q", ToolName: "communicate", IsResultTool: true}))
	none(t, o.Event(events.New(events.ToolCallEndData{
		CallID:       "c_q",
		ToolName:     "communicate",
		IsResultTool: true,
		ToolState:    quietState,
	})))

	var sawOther bool
	for _, item := range o.Snapshot() {
		if item.CallID == "c_q" || item.Kind == appwire.OverlayStream {
			t.Fatalf("snapshot kept retired stream or quiet preview: %+v", item)
		}
		if item.Kind == appwire.OverlayPreview && item.CallID == "c_other" {
			sawOther = true
		}
	}
	if !sawOther {
		t.Fatalf("snapshot dropped unrelated preview c_other: %+v", o.Snapshot())
	}
	// A late preview delta for the retired quiet call creates nothing.
	none(t, o.Event(events.New(events.CommunicatePreviewDeltaData{CallID: "c_q", Delta: "late"})))

	o.Event(events.New(events.CommunicatePreviewResetData{CallID: "c_other"}))
	o.Recorded(toolResultsRecord(1, "t_1", "c_other"))
	o.Recorded(toolResultsRecord(1, "t_1", "c_q"))
	end := one(t, o.Event(events.New(events.RoundEndedData{RoundID: "r_1"})))
	if end.Method != appwire.NotifyOverlayEnd {
		t.Fatalf("round end = %+v, want only overlay/end", end)
	}
}

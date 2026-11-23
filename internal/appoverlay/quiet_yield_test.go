package appoverlay

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestQuietCommunicateClearsCompanionStreamAndPreservesReasoning(t *testing.T) {
	t.Parallel()
	o := newOverlay()
	t.Cleanup(o.Close)
	quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true}`)

	o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"}))
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.ReasoningSummaryDeltaData{Delta: "thought"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "companion prose"}))
	o.Event(events.New(events.ToolCallStartData{CallID: "c_q", ToolName: "communicate", IsResultTool: true}))

	changes := o.Event(events.New(events.ToolCallEndData{
		CallID:       "c_q",
		ToolName:     "communicate",
		IsResultTool: true,
		ToolState:    quietState,
	}))

	sawReset := false
	sawReasoning := false
	for _, change := range changes {
		switch change.Method {
		case appwire.NotifyOverlayReset:
			params, ok := change.Params.(appwire.OverlayResetParams)
			if !ok || params.StreamID != "r_1/1" {
				t.Fatalf("unexpected reset params: %+v", change.Params)
			}
			sawReset = true
		case appwire.NotifyOverlayUpserted:
			item := upserted(t, change)
			if item.Item.Type == "agentMessage" {
				t.Fatalf("quiet communicate re-upserted companion agentMessage: %+v", item)
			}
			if item.Item.Type == "reasoning" {
				sawReasoning = true
			}
		}
	}
	if !sawReset || !sawReasoning {
		t.Fatalf("changes=%+v, want overlay/reset for r_1/1 and reasoning re-upserted", changes)
	}

	// Late assistant text delta in the quiet round must be dropped.
	none(t, o.Event(events.New(events.AssistantTextDeltaData{Delta: "late delta"})))

	for _, item := range o.Snapshot() {
		if item.Item.Type == "agentMessage" {
			t.Fatalf("snapshot still holds companion agentMessage after quiet communicate: %+v", item)
		}
	}
	o.Recorded(assistantRecord(0, "t_1", "r_1", textPart("companion prose"), llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought"}}, callPart("c_q", "communicate")))
	o.Recorded(toolResultsRecord(1, "t_1", "c_q"))
	end := one(t, o.Event(events.New(events.RoundEndedData{RoundID: "r_1"})))
	if end.Method != appwire.NotifyOverlayEnd {
		t.Fatalf("round end = %+v, want only overlay/end", end)
	}
}

package appoverlay

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
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
	endChanges := o.Event(events.New(events.ToolCallEndData{
		CallID:       "c_q",
		ToolName:     "communicate",
		IsResultTool: true,
		ToolState:    quietState,
	}))
	var sawReset, sawOtherUpsert bool
	for _, ch := range endChanges {
		if ch.Method == appwire.NotifyOverlayReset {
			if p, ok := ch.Params.(appwire.OverlayResetParams); ok && p.StreamID == "r_1/1" {
				sawReset = true
			}
		}
		if ch.Method == appwire.NotifyOverlayUpserted {
			if item := upserted(t, ch); item.CallID == "c_other" && item.Kind == appwire.OverlayPreview {
				sawOtherUpsert = true
			}
		}
	}
	if !sawReset || !sawOtherUpsert {
		t.Fatalf("endChanges=%+v, want overlay/reset(r_1/1) + re-upserted c_other preview", endChanges)
	}

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

func TestQuietPreviewResetRetiresOnlyQuietCallAndPreservesAttempt(t *testing.T) {
	t.Parallel()
	o := newOverlay()
	t.Cleanup(o.Close)

	o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"}))
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.ReasoningSummaryDeltaData{Delta: "thought"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "companion prose"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_other"}))
	o.Event(events.New(events.CommunicatePreviewDeltaData{CallID: "c_other", Delta: "keep me"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_quiet"}))
	o.Event(events.New(events.CommunicatePreviewDeltaData{CallID: "c_quiet", Delta: "waiting on job"}))

	changes := o.Event(events.New(events.CommunicatePreviewResetData{CallID: "c_quiet", Quiet: true}))

	var sawReset bool
	upsertedTexts := map[string]string{}
	for _, ch := range changes {
		switch ch.Method {
		case appwire.NotifyOverlayReset:
			if p, ok := ch.Params.(appwire.OverlayResetParams); ok && p.StreamID == "r_1/1" {
				sawReset = true
			}
		case appwire.NotifyOverlayUpserted:
			item := upserted(t, ch)
			if item.Key == "preview:c_quiet" {
				t.Fatalf("quiet preview reset re-upserted c_quiet: %+v", item)
			}
			upsertedTexts[item.Key] = item.Item.Text
		default:
			t.Fatalf("unexpected change %+v", ch)
		}
	}
	if !sawReset {
		t.Fatalf("changes = %+v, want overlay/reset(r_1/1)", changes)
	}
	wantTexts := map[string]string{
		"stream:r_1/1:reasoning":    "thought",
		"stream:r_1/1:agentMessage": "companion prose",
		"preview:c_other":           "keep me",
	}
	for key, want := range wantTexts {
		if got := upsertedTexts[key]; got != want {
			t.Fatalf("re-upserted %s = %q, want %q (all=%+v)", key, got, want, upsertedTexts)
		}
	}
	if o.Contains("preview:c_quiet") {
		t.Fatalf("overlay retained preview:c_quiet after quiet reset; snapshot=%+v", o.Snapshot())
	}
	for key := range wantTexts {
		if !o.Contains(key) {
			t.Fatalf("overlay dropped surviving slot %s; snapshot=%+v", key, o.Snapshot())
		}
	}
	if _, discarded := o.discardedRounds.get("r_1"); discarded {
		t.Fatal("quiet preview reset marked round r_1 discarded")
	}

	// The attempt remains open so subsequent deltas still append to r_1/1.
	d := delta(t, one(t, o.Event(events.New(events.AssistantTextDeltaData{Delta: " more"}))))
	if d.Key != "stream:r_1/1:agentMessage" || d.Delta != " more" {
		t.Fatalf("post-quiet delta = %+v, want append to stream:r_1/1:agentMessage", d)
	}

	o.Recorded(assistantRecord(0, "t_1", "r_1", textPart("companion prose more"), llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought"}}, callPart("c_other", "communicate"), callPart("c_quiet", "communicate")))
	o.Recorded(communicateRecord(1, "t_1", "c_other"))
	o.Recorded(toolResultsRecord(2, "t_1", "c_quiet"))
	end := one(t, o.Event(events.New(events.RoundEndedData{RoundID: "r_1"})))
	if end.Method != appwire.NotifyOverlayEnd {
		t.Fatalf("round end = %+v, want only overlay/end", end)
	}
}

func TestNonQuietPreviewResetStillDiscardsAttemptAndMarksRoundDiscarded(t *testing.T) {
	t.Parallel()
	o := newOverlay()
	t.Cleanup(o.Close)

	o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"}))
	o.Event(events.New(events.RoundStartedData{RoundID: "r_1"}))
	o.Event(events.New(events.ReasoningSummaryDeltaData{Delta: "thought"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "partial"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_other"}))
	o.Event(events.New(events.CommunicatePreviewDeltaData{CallID: "c_other", Delta: "sibling"}))
	o.Event(events.New(events.CommunicatePreviewStartData{CallID: "c_failed"}))
	o.Event(events.New(events.CommunicatePreviewDeltaData{CallID: "c_failed", Delta: "failed"}))

	reset := one(t, o.Event(events.New(events.CommunicatePreviewResetData{CallID: "c_failed", Quiet: false})))
	if p, ok := reset.Params.(appwire.OverlayResetParams); reset.Method != appwire.NotifyOverlayReset || !ok || p.StreamID != "r_1/1" {
		t.Fatalf("non-quiet reset = %+v, want overlay/reset(r_1/1)", reset)
	}
	if snap := o.Snapshot(); len(snap) != 0 {
		t.Fatalf("snapshot after non-quiet reset = %+v, want empty", snap)
	}
	if _, discarded := o.discardedRounds.get("r_1"); !discarded {
		t.Fatal("non-quiet preview reset did not mark round r_1 discarded")
	}

	changes := o.Event(events.New(events.RoundEndedData{RoundID: "r_1"}))
	if len(changes) != 2 || upserted(t, changes[0]).Item.EventKind != appwire.ThreadItemEventKindInterrupted || changes[1].Method != appwire.NotifyOverlayEnd {
		t.Fatalf("round end = %+v, want interrupted notice then overlay/end", changes)
	}
}

func TestBareTextQuietParkCoversRoundStreamWithoutInterrupt(t *testing.T) {
	t.Parallel()
	o := newOverlay()
	t.Cleanup(o.Close)

	o.Event(events.New(events.ExecutionStartedData{TurnID: "t_1"}))
	o.Event(events.New(events.RoundStartedData{RoundID: "r_bare"}))
	o.Event(events.New(events.ReasoningSummaryDeltaData{Delta: "thinking"}))
	o.Event(events.New(events.AssistantTextDeltaData{Delta: "Waiting on subagent"}))

	o.Recorded(transcript.Record{
		Recorded: true,
		Ordinal:  0,
		Turn: schema.Turn{
			TurnID: "t_1", RoundID: "r_bare", Kind: schema.TurnAssistant, QuietYield: true,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "Waiting on subagent"}}},
		},
	})
	none(t, o.Event(events.New(events.AssistantTextDeltaData{Delta: "late delta"})))
	if len(o.Snapshot()) != 0 {
		t.Fatalf("snapshot retained covered stream: %+v", o.Snapshot())
	}
	end := one(t, o.Event(events.New(events.RoundEndedData{RoundID: "r_bare"})))
	if end.Method != appwire.NotifyOverlayEnd {
		t.Fatalf("round end = %+v, want only overlay/end", end)
	}
}

package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// TestProjector_ReasoningResetDiscardsInProgressItem pins the #641 fix: a
// stream retry after partial output emits EventAssistantTextReset, and the
// projector must discard the in-progress REASONING item too — not only the
// assistant item. Without it, the reset left the reasoning item open, so the
// retry's reasoning deltas carried the SAME item id as the failed attempt's:
// the live view kept the old text and appended the retry's onto it, and N
// retries rendered the same words N+1 times — the "what what what… the the
// the…" mangled live view the issue reports. The durable transcript never
// applied the failed attempts' deltas, which is why reload rendered correctly.
func TestProjector_ReasoningResetDiscardsInProgressItem(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hi"}})
	p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "gpt-5"}})

	// Attempt 1 streams reasoning, then the stream fails mid-flight. Capture
	// the item id the live view is accumulating text under.
	attemptOneItem := reasoningDeltaItem(t, p.Project(events.SessionEvent{
		Kind: events.EventReasoningSummaryDelta, SessionID: "th_1",
		Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "what "},
	}))
	p.Project(events.SessionEvent{
		Kind: events.EventReasoningSummaryDelta, SessionID: "th_1",
		Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "the "},
	})

	// The retry loop's OnReset fires before attempt 2. The reset must name
	// the in-progress reasoning item so the live view discards it.
	resetOut := p.Project(events.SessionEvent{Kind: events.EventAssistantTextReset, SessionID: "th_1", Data: events.AssistantTextResetData{}})
	resetNamed := map[string]bool{}
	for _, n := range resetOut {
		if n.Method != appwire.NotifyAgentMessageReset {
			continue
		}
		params, ok := n.Params.(appwire.AgentMessageResetParams)
		if !ok {
			continue
		}
		resetNamed[params.ItemID] = true
	}
	if !resetNamed[attemptOneItem] {
		t.Fatalf("EventAssistantTextReset did not reset the in-progress reasoning item %q: %+v", attemptOneItem, resetOut)
	}
	if p.reasoningItem != "" {
		t.Fatalf("reasoning item survived the reset: %q", p.reasoningItem)
	}

	// Attempt 2 re-streams the same words. Its deltas must open a FRESH
	// item — a new id the live view has no text under — so nothing the
	// failed attempt streamed survives into the retry's rendering.
	attemptTwoItem := reasoningDeltaItem(t, p.Project(events.SessionEvent{
		Kind: events.EventReasoningSummaryDelta, SessionID: "th_1",
		Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "what "},
	}))
	if attemptTwoItem == attemptOneItem {
		t.Fatalf("retry's reasoning delta reused the discarded item id %q — the live view would append the retry's text onto the failed attempt's", attemptOneItem)
	}
}

// TestProjector_ResetStillDiscardsAssistantItem pins the assistant half of
// the same reset: the retry replacement contract that existed before this fix
// must hold unchanged for assistant text.
func TestProjector_ResetStillDiscardsAssistantItem(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hi"}})
	p.Project(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_1", Data: events.AssistantTextStartData{Model: "gpt-5"}})

	// An assistant item opens on the first delta.
	var assistantItem string
	for _, n := range p.Project(events.SessionEvent{Kind: events.EventAssistantTextDelta, SessionID: "th_1", Data: events.AssistantTextDeltaData{Delta: "partial"}}) {
		if n.Method == appwire.NotifyAgentMessageDelta {
			assistantItem = n.Params.(appwire.AgentMessageDeltaParams).ItemID
		}
	}
	if assistantItem == "" {
		t.Fatal("no assistant item was opened by the first delta")
	}

	resetOut := p.Project(events.SessionEvent{Kind: events.EventAssistantTextReset, SessionID: "th_1", Data: events.AssistantTextResetData{}})
	if len(resetOut) != 1 {
		t.Fatalf("assistant-only reset emitted %d notifications, want 1: %+v", len(resetOut), resetOut)
	}
	params, ok := resetOut[0].Params.(appwire.AgentMessageResetParams)
	if !ok || params.ItemID != assistantItem {
		t.Fatalf("reset did not name the assistant item %q: %+v", assistantItem, resetOut)
	}
	if p.assistantItem != "" {
		t.Fatalf("assistant item survived the reset: %q", p.assistantItem)
	}
}

// reasoningDeltaItem returns the item id the next reasoning delta carries —
// the id the live view is accumulating text under.
func reasoningDeltaItem(t *testing.T, out []AppNotification) string {
	t.Helper()
	for _, n := range out {
		if n.Method != appwire.NotifyReasoningSummaryDelta {
			continue
		}
		params, ok := n.Params.(appwire.ReasoningSummaryDeltaParams)
		if !ok {
			continue
		}
		if params.ItemID == "" {
			t.Fatalf("reasoning delta carried no item id: %+v", n)
		}
		return params.ItemID
	}
	t.Fatalf("no reasoning delta in %+v", out)
	return ""
}

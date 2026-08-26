package appprojector

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func TestCommunicatePreviewIsResetWhenItsCallFails(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventUserInput, Data: events.UserInputData{Text: "hello"}})
	p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: "call-1"}})
	p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewDelta, Data: events.CommunicatePreviewDeltaData{CallID: "call-1", Delta: "rejected"}})

	out := p.Project(events.SessionEvent{Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{
		ToolName: "communicate", CallID: "call-1", Error: "invalid output", PrevalOnly: true,
	}})
	var reset appwire.AgentMessageResetParams
	resetSeen := false
	for _, notification := range out {
		if notification.Method == appwire.NotifyAgentMessageReset {
			reset = notification.Params.(appwire.AgentMessageResetParams)
			resetSeen = true
		}
		if notification.Method == appwire.NotifyItemCompleted {
			item := notification.Params.(appwire.ItemLifecycleParams).Item
			if item.Type == "agentMessage" {
				t.Fatalf("failed preview completed as agent message: %+v", item)
			}
		}
	}
	if !resetSeen {
		t.Fatalf("failure notifications = %+v, want reset", out)
	}
	if reset.ItemID == "" {
		t.Fatal("reset omitted provisional item id")
	}
}

func TestCommunicatePreviewCommitsOnceOnMatchingSuccess(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: "call-1"}})
	p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewDelta, Data: events.CommunicatePreviewDeltaData{CallID: "call-1", Delta: "delivered"}})

	out := p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{
		CallID: "call-1", Message: "delivered",
	}})
	if len(out) != 1 || out[0].Method != appwire.NotifyItemCompleted {
		t.Fatalf("success notifications = %+v, want one completion", out)
	}
	item := out[0].Params.(appwire.ItemLifecycleParams).Item
	if item.Status != appwire.TurnStatusCompleted || item.Text != "delivered" {
		t.Fatalf("committed item = %+v", item)
	}
	if out := p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{
		CallID: "call-1", Message: "delivered",
	}}); len(out) != 0 {
		t.Fatalf("duplicate success notifications = %+v", out)
	}
}

func TestCommunicatePreviewRealOrderPreservesItemIdentity(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	started := p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: "call-real"}})
	p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewDelta, Data: events.CommunicatePreviewDeltaData{CallID: "call-real", Delta: "preview"}})
	start := p.Project(events.SessionEvent{Kind: events.EventToolCallStart, Data: events.ToolCallStartData{ToolName: "communicate", CallID: "call-real"}})
	completed := p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{CallID: "call-real", Message: "final"}})
	_ = p.Project(events.SessionEvent{Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: "communicate", CallID: "call-real"}})
	var startedID, completedID string
	for _, n := range append(started, start...) {
		if n.Method == appwire.NotifyItemStarted {
			startedID = n.Params.(appwire.ItemLifecycleParams).Item.ID
		}
	}
	for _, n := range completed {
		if n.Method == appwire.NotifyItemCompleted {
			completedID = n.Params.(appwire.ItemLifecycleParams).Item.ID
		}
	}
	if startedID == "" || completedID != startedID {
		t.Fatalf("real-order identity started=%q completed=%q start=%+v completed=%+v", startedID, completedID, start, completed)
	}
}

func TestCommunicatePreviewTurnCloseResetsLiveItems(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: "live"}})
	out := p.Project(events.SessionEvent{Kind: events.EventUserInput, Data: events.UserInputData{Text: "next"}})
	resets := 0
	for _, n := range out {
		if n.Method == appwire.NotifyAgentMessageReset {
			resets++
			if n.Params.(appwire.AgentMessageResetParams).TurnID == "" {
				t.Fatal("turn-close reset omitted turn ID")
			}
		}
	}
	if resets != 1 {
		t.Fatalf("turn close reset notifications = %d, want 1: %+v", resets, out)
	}
}

func TestCommunicatePreviewDuplicateStartIsIdempotent(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	start := events.SessionEvent{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: "dup"}}
	first := p.Project(start)
	second := p.Project(start)
	if len(first) != 2 || first[1].Method != appwire.NotifyItemStarted || len(second) != 0 {
		t.Fatalf("duplicate preview start first=%+v second=%+v", first, second)
	}
	reset := p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewReset, Data: events.CommunicatePreviewResetData{CallID: "dup"}})
	if len(reset) != 1 || reset[0].Method != appwire.NotifyAgentMessageReset {
		t.Fatalf("duplicate start stranded preview: reset=%+v", reset)
	}
}

func TestCommunicatePreviewReuseAfterToolEndStartsNewGeneration(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventToolCallStart, Data: events.ToolCallStartData{ToolName: "communicate", CallID: "reuse"}})
	first := p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{CallID: "reuse", Message: "one"}})
	p.Project(events.SessionEvent{Kind: events.EventToolCallEnd, Timestamp: time.Now(), Data: events.ToolCallEndData{ToolName: "communicate", CallID: "reuse"}})
	p.Project(events.SessionEvent{Kind: events.EventToolCallStart, Data: events.ToolCallStartData{ToolName: "communicate", CallID: "reuse"}})
	second := p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{CallID: "reuse", Message: "two"}})
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("reuse completions first=%+v second=%+v", first, second)
	}
}

func TestCommunicatePreviewDuplicateCommittedStartEndDoesNotCreateToolItem(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	p.Project(events.SessionEvent{Kind: events.EventToolCallStart, Data: events.ToolCallStartData{ToolName: "communicate", CallID: "dup-tool"}})
	p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{CallID: "dup-tool", Message: "done"}})
	if out := p.Project(events.SessionEvent{Kind: events.EventToolCallStart, Data: events.ToolCallStartData{ToolName: "communicate", CallID: "dup-tool"}}); len(out) != 0 {
		t.Fatalf("duplicate committed start emitted notifications: %+v", out)
	}
	if out := p.Project(events.SessionEvent{Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: "communicate", CallID: "dup-tool"}}); len(out) != 0 {
		t.Fatalf("duplicate committed end emitted notifications: %+v", out)
	}
}

func TestCommunicatePreviewResetOrderIsStableAcrossInterleavedCalls(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	for _, callID := range []string{"b", "a", "c"} {
		p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: callID}})
	}
	out := p.Project(events.SessionEvent{Kind: events.EventUserInput, Data: events.UserInputData{Text: "next"}})
	var got []string
	for _, n := range out {
		if n.Method == appwire.NotifyAgentMessageReset {
			got = append(got, n.Params.(appwire.AgentMessageResetParams).ItemID)
		}
	}
	want := []string{"item_communicate_preview_2", "item_communicate_preview_1", "item_communicate_preview_3"}
	if len(got) != len(want) {
		t.Fatalf("reset count=%d output=%+v", len(got), out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reset order=%v want=%v", got, want)
		}
	}
}

func TestCommunicatePreviewMalformedOrderingDoesNotCreateOrLeakItems(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	for _, event := range []events.SessionEvent{
		{Kind: events.EventCommunicatePreviewDelta, Data: events.CommunicatePreviewDeltaData{CallID: "missing", Delta: "ignored"}},
		{Kind: events.EventCommunicatePreviewReset, Data: events.CommunicatePreviewResetData{CallID: "missing"}},
		{Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: "communicate", CallID: "missing"}},
	} {
		if out := p.Project(event); len(out) != 0 {
			t.Fatalf("malformed event %+v emitted %+v", event, out)
		}
	}
	p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: "fail"}})
	p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: "success"}})
	if out := p.Project(events.SessionEvent{Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: "communicate", CallID: "fail", Error: "failed"}}); len(out) != 1 {
		t.Fatalf("failed interleaved call output=%+v", out)
	}
	out := p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{CallID: "success", Message: "ok"}})
	if len(out) != 1 || out[0].Method != appwire.NotifyItemCompleted {
		t.Fatalf("successful interleaved call output=%+v", out)
	}
}

func TestCommunicatePreviewInterleavedFailureAndSuccessKeepIdentity(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	started := map[string]string{}
	for _, callID := range []string{"fail", "success"} {
		out := p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: callID}})
		for _, n := range out {
			if n.Method == appwire.NotifyItemStarted {
				started[callID] = n.Params.(appwire.ItemLifecycleParams).Item.ID
			}
		}
		p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewDelta, Data: events.CommunicatePreviewDeltaData{CallID: callID, Delta: callID}})
		p.Project(events.SessionEvent{Kind: events.EventToolCallStart, Data: events.ToolCallStartData{ToolName: "communicate", CallID: callID}})
	}
	failed := p.Project(events.SessionEvent{Kind: events.EventToolCallEnd, Data: events.ToolCallEndData{ToolName: "communicate", CallID: "fail", Error: "failed"}})
	succeeded := p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{CallID: "success", Message: "corrected final"}})
	if len(failed) != 1 || failed[0].Method != appwire.NotifyAgentMessageReset || failed[0].Params.(appwire.AgentMessageResetParams).ItemID != started["fail"] {
		t.Fatalf("failed identity start=%v output=%+v", started["fail"], failed)
	}
	if len(succeeded) != 1 || succeeded[0].Method != appwire.NotifyItemCompleted || succeeded[0].Params.(appwire.ItemLifecycleParams).Item.ID != started["success"] {
		t.Fatalf("success identity start=%v output=%+v", started["success"], succeeded)
	}
}

func TestCommunicateReplayCommitsWithoutLivePreview(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	out := p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{
		CallID: "call-replayed", Message: "replayed",
	}})
	var item appwire.ThreadItem
	completed := 0
	for _, notification := range out {
		if notification.Method == appwire.NotifyItemCompleted {
			completed++
			item = notification.Params.(appwire.ItemLifecycleParams).Item
		}
	}
	if completed != 1 {
		t.Fatalf("replay notifications = %+v, want one completion", out)
	}
	if item.Type != "agentMessage" || item.Text != "replayed" || item.Status != appwire.TurnStatusCompleted {
		t.Fatalf("replayed item = %+v", item)
	}
}

func TestCommunicatePreviewSameCallIDStartsNewGeneration(t *testing.T) {
	p := NewAppEventProjector("th_1", "local:th_1")
	commit := func(message string) int {
		p.Project(events.SessionEvent{Kind: events.EventToolCallStart, Data: events.ToolCallStartData{
			ToolName: "communicate", CallID: "reused",
		}})
		p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: "reused"}})
		p.Project(events.SessionEvent{Kind: events.EventCommunicatePreviewDelta, Data: events.CommunicatePreviewDeltaData{CallID: "reused", Delta: message}})
		return len(p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{CallID: "reused", Message: message}}))
	}
	if got := commit("first"); got != 1 {
		t.Fatalf("first commit notifications = %d, want 1", got)
	}
	if got := commit("second"); got != 1 {
		t.Fatalf("same-ID new-generation notifications = %d, want 1", got)
	}
	if got := len(p.Project(events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{CallID: "reused", Message: "second"}})); got != 0 {
		t.Fatalf("duplicate same-generation notifications = %d, want 0", got)
	}
}

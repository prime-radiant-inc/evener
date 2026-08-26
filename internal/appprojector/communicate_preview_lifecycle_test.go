package appprojector

import (
	"testing"

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

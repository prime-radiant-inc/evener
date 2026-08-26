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

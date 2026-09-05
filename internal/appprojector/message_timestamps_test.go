package appprojector

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func TestMessageTimestamps(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	for _, event := range []events.SessionEvent{
		{Kind: events.EventUserInput, Data: events.UserInputData{Text: "hello"}},
		{Kind: events.EventAssistantTextEnd, Data: events.AssistantTextEndData{Text: "reply"}},
		{Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "reply"}},
		{Kind: events.EventCommunicate, Data: events.CommunicateData{CallID: "call", Message: "reply"}},
	} {
		t.Run(string(event.Kind), func(t *testing.T) {
			for _, stamp := range []time.Time{at, {}} {
				p := NewAppEventProjector("thread", "ref")
				event.Timestamp = stamp
				item := notificationThreadItem(t, p.Project(event), appwire.NotifyItemCompleted)
				if stamp.IsZero() {
					if item.StartedAt != nil {
						t.Fatalf("missing timestamp became %v", *item.StartedAt)
					}
				} else if item.StartedAt == nil || *item.StartedAt != at.UnixMilli() {
					t.Fatalf("message timestamp = %v, want %d", item.StartedAt, at.UnixMilli())
				}
			}
		})
	}
}

func TestStreamingMessageKeepsTimestampOnCompletion(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	for _, pair := range [][2]events.SessionEvent{
		{{Kind: events.EventAssistantTextDelta, Data: events.AssistantTextDeltaData{Delta: "reply"}}, {Kind: events.EventAssistantTextEnd, Data: events.AssistantTextEndData{Text: "reply"}}},
		{{Kind: events.EventCommunicatePreviewStart, Data: events.CommunicatePreviewStartData{CallID: "call"}}, {Kind: events.EventCommunicate, Data: events.CommunicateData{CallID: "call", Message: "reply"}}},
	} {
		t.Run(string(pair[0].Kind), func(t *testing.T) {
			p := NewAppEventProjector("thread", "ref")
			pair[0].Timestamp = at
			pair[1].Timestamp = at.Add(time.Minute)
			started := notificationThreadItem(t, p.Project(pair[0]), appwire.NotifyItemStarted)
			completed := notificationThreadItem(t, p.Project(pair[1]), appwire.NotifyItemCompleted)
			if started.StartedAt == nil || completed.StartedAt == nil || *started.StartedAt != at.UnixMilli() || *completed.StartedAt != at.UnixMilli() {
				t.Fatalf("stream timestamp changed: start=%v complete=%v", started.StartedAt, completed.StartedAt)
			}
		})
	}
}

func TestUserSteeringTimestamp(t *testing.T) {
	p := NewAppEventProjector("thread", "ref")
	out := p.Project(events.SessionEvent{Kind: events.EventSteeringInjected, Timestamp: time.Unix(1700000000, 0), Data: events.SteeringInjectedData{Text: "adjust", Source: "user"}})
	params := out[0].Params.(map[string]any)
	if params["startedAt"] != int64(1700000000000) {
		t.Fatalf("steering timestamp = %v", params["startedAt"])
	}
}

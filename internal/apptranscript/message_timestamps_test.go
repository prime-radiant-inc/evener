package apptranscript

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestMessageTimestampsFromTranscript(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	for _, turn := range []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("hello")},
		{Kind: schema.TurnSteering, SteeringSource: "user", Message: llm.User("adjust")},
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "reply"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call", Name: "communicate", Arguments: []byte(`{"message":"another reply"}`)}},
		}}},
	} {
		for _, stamp := range []time.Time{at, {}} {
			turn.Timestamp = stamp
			items := ProjectTurn("turn", 1, turn, nil, nil, nil)
			if len(items) == 0 {
				t.Fatal("no messages projected")
			}
			for _, item := range items {
				if stamp.IsZero() {
					if item.StartedAt != nil {
						t.Fatalf("missing timestamp became %v", *item.StartedAt)
					}
				} else if item.StartedAt == nil || *item.StartedAt != at.UnixMilli() {
					t.Fatalf("%s timestamp = %v, want %d", item.Type, item.StartedAt, at.UnixMilli())
				}
			}
		}
	}
}

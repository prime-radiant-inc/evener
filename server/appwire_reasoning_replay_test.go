package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func TestServerAppWireReadReplaysProjectedReasoningWithTerminalIdentity(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_reasoning")
	client := dialServerAppWire(t, srv)
	ctx := context.Background()
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_reasoning", Data: events.UserInputData{Text: "question", StableTurnID: "turn_stable_reasoning"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_reasoning", Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "first "}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_reasoning", Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "thought"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_reasoning", Data: events.AssistantTextEndData{Text: "answer"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_reasoning", Data: events.SessionEndData{Reason: "input_complete", State: "idle"}})
	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_reasoning", IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Thread.Turns) != 1 {
		t.Fatalf("turns=%+v", read.Thread.Turns)
	}
	turn := read.Thread.Turns[0]
	if turn.ID != "turn_stable_reasoning" || turn.Status != appwire.TurnStatusCompleted {
		t.Fatalf("turn=(id %q, status %q), want stable identity and completed", turn.ID, turn.Status)
	}
	if len(turn.Items) != 3 {
		t.Fatalf("items=%+v", turn.Items)
	}
	var reasoningCount, answerCount int
	for _, item := range turn.Items {
		switch item.Type {
		case "reasoning":
			reasoningCount++
			if item.TurnID != turn.ID || item.ID == "" || item.Text != "first thought" || item.Status != appwire.TurnStatusCompleted {
				t.Fatalf("reasoning=%+v", item)
			}
		case "agentMessage":
			answerCount++
			if item.TurnID != turn.ID || item.ID == "" || item.Text != "answer" || item.Status != appwire.TurnStatusCompleted {
				t.Fatalf("answer=%+v", item)
			}
		}
	}
	if reasoningCount != 1 || answerCount != 1 {
		t.Fatalf("counts reasoning=%d answer=%d", reasoningCount, answerCount)
	}
}

package server

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
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

func TestServerAppWireSnapshotPreservesReasoningAcrossReservedTurn(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_reasoning_boundary")
	srv.SetCostLookupFunc(func(string) *registry.Cost {
		return &registry.Cost{Input: 5, Output: 25}
	})
	client := dialServerAppWire(t, srv)
	ctx := context.Background()
	oldStart := time.Unix(100, 0)
	oldEnd := oldStart.Add(4200 * time.Millisecond)
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_reasoning_boundary", Timestamp: oldStart, Data: events.UserInputData{Text: "question", StableTurnID: "turn_old"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_reasoning_boundary", Timestamp: oldStart.Add(time.Second), Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "old reasoning"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_reasoning_boundary", Timestamp: oldStart.Add(2 * time.Second), Data: events.AssistantTextEndData{Text: "old answer", Usage: llm.Usage{InputTokens: 1000, OutputTokens: 500}}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_reasoning_boundary", Timestamp: oldStart.Add(3 * time.Second), Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "unfinished reasoning"}})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "th_reasoning_boundary", Timestamp: oldEnd, Data: events.TurnEndedData{TurnDurationMS: 4200}})
	srv.SetProcessingTurn("turn_new")
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventAssistantTextStart, SessionID: "th_reasoning_boundary", Timestamp: oldEnd.Add(time.Second)})
	srv.RecordAppEvent(events.SessionEvent{Kind: events.EventReasoningSummaryDelta, SessionID: "th_reasoning_boundary", Timestamp: oldEnd.Add(2 * time.Second), Data: events.ReasoningSummaryDeltaData{SummaryIndex: 0, Delta: "new reasoning"}})

	read, err := client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:th_reasoning_boundary", IncludeTurns: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Thread.Turns) != 2 {
		t.Fatalf("turns=%+v, want completed old and in-progress new turn", read.Thread.Turns)
	}
	old, current := read.Thread.Turns[0], read.Thread.Turns[1]
	if old.ID != "turn_old" || old.Status != appwire.TurnStatusCompleted {
		t.Fatalf("old turn=(id %q, status %q), want completed turn_old", old.ID, old.Status)
	}
	if old.Usage == nil || old.Usage.InputTokens != 1000 || old.Usage.OutputTokens != 500 {
		t.Fatalf("old usage=%+v, want input 1000/output 500", old.Usage)
	}
	if old.Cost != "~$0.02" {
		t.Fatalf("old cost=%q, want ~$0.02", old.Cost)
	}
	if old.DurationMS == nil || *old.DurationMS != 4200 || old.CompletedAt == nil || *old.CompletedAt != oldEnd.UnixMilli() {
		t.Fatalf("old timing=(duration %v, completed %v), want (4200, %d)", old.DurationMS, old.CompletedAt, oldEnd.UnixMilli())
	}
	reasoningTexts := make(map[string]bool)
	for _, item := range old.Items {
		if item.Type != "reasoning" {
			continue
		}
		if item.TurnID != "turn_old" || item.Status != appwire.TurnStatusCompleted {
			t.Fatalf("old reasoning=%+v, want completed item owned by turn_old", item)
		}
		reasoningTexts[item.Text] = true
	}
	if len(reasoningTexts) != 2 || !reasoningTexts["old reasoning"] || !reasoningTexts["unfinished reasoning"] {
		t.Fatalf("old reasoning text=%v, want both retained reasoning rounds", reasoningTexts)
	}
	if current.ID != "turn_new" || current.Status != appwire.TurnStatusInProgress {
		t.Fatalf("current turn=(id %q, status %q), want in-progress turn_new", current.ID, current.Status)
	}
	if len(current.Items) != 1 || current.Items[0].Type != "reasoning" || current.Items[0].Text != "new reasoning" || current.Items[0].TurnID != "turn_new" {
		t.Fatalf("current items=%+v, want new reasoning on turn_new", current.Items)
	}
}

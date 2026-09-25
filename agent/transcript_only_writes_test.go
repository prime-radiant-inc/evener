package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func respond(resp llm.Response) func(context.Context) (llm.Response, error) {
	return func(context.Context) (llm.Response, error) { return resp, nil }
}

func entriesOfKind(turns []schema.Turn, kind schema.TurnKind) []schema.Turn {
	var out []schema.Turn
	for _, turn := range turns {
		if turn.Kind == kind {
			out = append(out, turn)
		}
	}
	return out
}

// communicate records its message before it announces it: when the event
// reaches a consumer, the entry is already in the file, in the running
// execution and before the round's tool results.
func TestCommunicateIsRecordedBeforeItIsAnnounced(t *testing.T) {
	s, adapter := newExecutionSession(t)
	adapter.script(respond(toolCallResponse(communicateCallArgs("comm-1", map[string]any{"message": "halfway", "end_turn": false}))))
	var mu sync.Mutex
	recordedAtEmit := map[string]bool{}
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		if ev.Kind != events.EventCommunicate {
			return
		}
		data := ev.Data.(events.CommunicateData)
		found := false
		for _, turn := range entriesOfKind(transcriptTurnsOf(t, s), schema.TurnCommunicate) {
			found = found || turn.Communicate.CallID == data.CallID
		}
		mu.Lock()
		recordedAtEmit[data.CallID] = found
		mu.Unlock()
	}, func() { close(drained) })
	if _, err := s.ProcessInput(context.Background(), "talk", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	<-drained
	mu.Lock()
	defer mu.Unlock()
	if len(recordedAtEmit) != 2 || !recordedAtEmit["comm-1"] || !recordedAtEmit["communicate_test_call"] {
		t.Fatalf("communicate events and whether their entry was recorded first: %v", recordedAtEmit)
	}
	turns := transcriptTurnsOf(t, s)
	communicates := entriesOfKind(turns, schema.TurnCommunicate)
	first := communicates[0]
	if first.Communicate.Message != "halfway" || first.Communicate.EndTurn || first.TurnKind != "" {
		t.Fatalf("first communicate entry = %+v", first)
	}
	for i, turn := range turns {
		if turn.Kind == schema.TurnCommunicate && turn.Communicate.CallID == "comm-1" {
			if next := turns[i+1]; next.Kind != schema.TurnToolResults || next.TurnID != turn.TurnID {
				t.Fatalf("the communicate entry is followed by %s in %q, want its round's tool results", next.Kind, next.TurnID)
			}
		}
	}
}

// A communicate call that fails validation delivers nothing and records
// nothing.
func TestAFailedCommunicateRecordsNothing(t *testing.T) {
	s, adapter := newExecutionSession(t)
	adapter.script(respond(toolCallResponse(communicateCallArgs("comm-empty", map[string]any{"message": ""}))))
	if _, err := s.ProcessInput(context.Background(), "talk", nil); err != nil {
		t.Fatal(err)
	}
	communicates := entriesOfKind(transcriptTurnsOf(t, s), schema.TurnCommunicate)
	if len(communicates) != 1 || communicates[0].Communicate.CallID == "comm-empty" {
		t.Fatalf("communicate entries = %+v, want only the later delivered one", communicates)
	}
}

// A session with no state directory delivers communicate exactly as before.
func TestCommunicateWithoutATranscriptStillDelivers(t *testing.T) {
	s := newSession(t, withSteps(func(llm.Request) llm.Response { return communicateResponse(true, "done") }))
	events := drainEvents(s)
	if _, err := s.ProcessInput(context.Background(), "talk", nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	delivered := 0
	for _, ev := range events() {
		if ev.Kind == "COMMUNICATE" {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("%d communicate events, want 1", delivered)
	}
}

func TestToolRepairIsRecordedAsANotice(t *testing.T) {
	s, adapter := newExecutionSession(t)
	s.RegisterTool("widget", "does a thing", widgetSchema(), func(context.Context, any) (any, error) { return "done", nil })
	adapter.script(respond(toolCallResponse(llm.ToolCallData{ID: "w1", Name: "widget", Arguments: json.RawMessage(`{"path":"/x"}`)})))
	if _, err := s.ProcessInput(context.Background(), "call widget", nil); err != nil {
		t.Fatal(err)
	}
	notices := entriesOfKind(transcriptTurnsOf(t, s), schema.TurnNotice)
	if len(notices) != 1 || notices[0].Notice.Kind != schema.NoticeToolRepair || notices[0].Notice.ToolRepair.CallID != "w1" || len(notices[0].Notice.ToolRepair.Changes) == 0 {
		t.Fatalf("notices = %+v", notices)
	}
}

func TestTurnLimitIsRecordedAsANotice(t *testing.T) {
	s, adapter := newExecutionSession(t)
	s.cfg.MaxToolRoundsPerInput = 1
	s.RegisterTool("widget", "does a thing", widgetSchema(), func(context.Context, any) (any, error) { return "done", nil })
	adapter.script(respond(toolCallResponse(llm.ToolCallData{ID: "w1", Name: "widget", Arguments: json.RawMessage(`{"file_path":"/x"}`)})))
	if _, err := s.ProcessInput(context.Background(), "loop", nil); err == nil {
		t.Fatal("the tool-round cap did not end the input")
	}
	notices := entriesOfKind(transcriptTurnsOf(t, s), schema.TurnNotice)
	if len(notices) != 1 || notices[0].Notice.Kind != schema.NoticeTurnLimit || notices[0].Notice.TurnLimit.MaxToolRoundsPerInput != 1 {
		t.Fatalf("notices = %+v", notices)
	}
	for _, turn := range entriesOfKind(transcriptTurnsOf(t, s), schema.TurnFailure) {
		t.Fatalf("a TURN_FAILURE already covers the limit: %+v", turn)
	}
}

func TestTranscriptOnlyWritesNeverEnterHistory(t *testing.T) {
	s, adapter := newExecutionSession(t)
	adapter.script(respond(toolCallResponse(communicateCallArgs("comm-1", map[string]any{"message": "halfway", "end_turn": false}))))
	if _, err := s.ProcessInput(context.Background(), "talk", nil); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.history {
		if turn.Kind.TranscriptOnly() {
			t.Fatalf("%s entered history", turn.Kind)
		}
	}
}

// Tool timing is history: each TOOL_RESULTS part keeps its DurationMS in the
// persisted form the transcript records.
func TestPersistedToolResultsKeepTheirDuration(t *testing.T) {
	calls := []llm.ToolCallData{{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{}`)}}
	parts := []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c1", Name: "read_file", Content: "ok", DurationMS: 37}}}
	persisted := projectToolResultsForTranscript(calls, nil, parts)
	if persisted[0].ToolResult.DurationMS != 37 {
		t.Fatalf("persisted duration = %d, want 37", persisted[0].ToolResult.DurationMS)
	}
}

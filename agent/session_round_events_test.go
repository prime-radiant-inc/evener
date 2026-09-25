package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// roundAndExecutionEvents renders the round and execution events of evs, in
// order, as "KIND id" lines (EXECUTION_ENDED adds its status).
func roundAndExecutionEvents(t *testing.T, evs []events.SessionEvent) []string {
	t.Helper()
	var out []string
	for _, ev := range evs {
		switch data := ev.Data.(type) {
		case events.RoundStartedData:
			out = append(out, fmt.Sprintf("%s %s", ev.Kind, data.RoundID))
		case events.RoundEndedData:
			out = append(out, fmt.Sprintf("%s %s", ev.Kind, data.RoundID))
		case events.ExecutionStartedData:
			out = append(out, fmt.Sprintf("%s %s", ev.Kind, data.TurnID))
		case events.ExecutionEndedData:
			out = append(out, fmt.Sprintf("%s %s %s", ev.Kind, data.TurnID, data.Status))
		}
	}
	return out
}

func userInputTurnID(t *testing.T, s *Session) string {
	t.Helper()
	inputs := entriesOfKind(transcriptTurnsOf(t, s), schema.TurnUserInput)
	if len(inputs) != 1 {
		t.Fatalf("%d USER_INPUT entries, want 1", len(inputs))
	}
	return inputs[0].TurnID
}

// A turn of two rounds announces its execution around both rounds, and each
// round ends before the next one starts.
func TestRoundEventsBracketEachRoundOfAnExecution(t *testing.T) {
	s, adapter := newExecutionSession(t)
	served := serveFailClosedSession(s)
	args, _ := json.Marshal(map[string]any{"file_path": "missing.txt"})
	adapter.script(func(context.Context) (llm.Response, error) {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "read-1", Name: "read_file", Arguments: args, Type: "function"}},
		}}}, nil
	})
	if _, err := s.ProcessInput(context.Background(), "two rounds", nil); err != nil {
		t.Fatal(err)
	}
	evs := served.settle(s)
	assistants := assistantEntries(t, s)
	requireDistinctRoundIDs(t, assistants, 2)
	turnID := userInputTurnID(t, s)
	r1, r2 := assistants[0].RoundID, assistants[1].RoundID
	want := []string{
		"EXECUTION_STARTED " + turnID,
		"ROUND_STARTED " + r1,
		"ROUND_ENDED " + r1,
		"ROUND_STARTED " + r2,
		"ROUND_ENDED " + r2,
		"EXECUTION_ENDED " + turnID + " completed",
	}
	if got := roundAndExecutionEvents(t, evs); !slices.Equal(got, want) {
		t.Fatalf("round and execution events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A request retried after a 503 stays in its round: one ROUND_STARTED.
func TestRoundEventsARetriedRoundStartsOnce(t *testing.T) {
	s, adapter := newExecutionSession(t)
	served := serveFailClosedSession(s)
	adapter.script(func(context.Context) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 503, "overloaded", nil, nil)
	})
	if _, err := s.ProcessInput(context.Background(), "retry", nil); err != nil {
		t.Fatal(err)
	}
	evs := served.settle(s)
	assistants := assistantEntries(t, s)
	requireDistinctRoundIDs(t, assistants, 1)
	turnID := userInputTurnID(t, s)
	want := []string{
		"EXECUTION_STARTED " + turnID,
		"ROUND_STARTED " + assistants[0].RoundID,
		"ROUND_ENDED " + assistants[0].RoundID,
		"EXECUTION_ENDED " + turnID + " completed",
	}
	if got := roundAndExecutionEvents(t, evs); !slices.Equal(got, want) {
		t.Fatalf("round and execution events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The execution-started func learns the execution's TurnID before any of its
// entries is in the file.
func TestRoundEventsExecutionStartedFuncRunsBeforeTheFirstEntry(t *testing.T) {
	s, _ := newExecutionSession(t)
	var mu sync.Mutex
	var started []string
	var recordedAtStart []string
	s.SetExecutionStartedFunc(func(turnID string) {
		var already []string
		for _, turn := range transcriptTurnsOf(t, s) {
			if turn.TurnID == turnID {
				already = append(already, string(turn.Kind))
			}
		}
		mu.Lock()
		defer mu.Unlock()
		started = append(started, turnID)
		recordedAtStart = append(recordedAtStart, already...)
	})
	if _, err := s.ProcessInput(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{userInputTurnID(t, s)}; !slices.Equal(started, want) {
		t.Fatalf("execution-started func saw %v, want %v", started, want)
	}
	if len(recordedAtStart) != 0 {
		t.Fatalf("entries of the execution already recorded when it started: %v", recordedAtStart)
	}
}

// The error of a turn failure, which the session records as TURN_FAILURE, is
// marked recorded.
func TestRoundEventsATurnFailureErrorIsMarkedRecorded(t *testing.T) {
	s, adapter := newExecutionSession(t)
	served := serveFailClosedSession(s)
	adapter.script(func(context.Context) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil)
	})
	if _, err := s.ProcessInput(context.Background(), "fail", nil); err == nil {
		t.Fatal("the failing request succeeded")
	}
	evs := served.settle(s)
	failures := entriesOfKind(transcriptTurnsOf(t, s), schema.TurnFailure)
	if len(failures) != 1 {
		t.Fatalf("%d TURN_FAILURE entries, want 1", len(failures))
	}
	var errs []events.ErrorData
	for _, ev := range evs {
		if data, ok := ev.Data.(events.ErrorData); ok {
			errs = append(errs, data)
		}
	}
	if len(errs) != 1 || !errs[0].Recorded {
		t.Fatalf("error events = %+v, want one marked recorded", errs)
	}
	turnID := userInputTurnID(t, s)
	if got := roundAndExecutionEvents(t, evs); got[len(got)-1] != "EXECUTION_ENDED "+turnID+" failed" {
		t.Fatalf("round and execution events end with %q, want the failed execution", got[len(got)-1])
	}
}

// The fail-closed diagnostic is recorded nowhere: it is not marked recorded.
func TestRoundEventsTheFailClosedDiagnosticIsNotMarkedRecorded(t *testing.T) {
	s, _ := newFailClosedSession(t, func(point string) error {
		if point == "new_transcript" {
			return errors.New("disk full")
		}
		return nil
	})
	served := serveFailClosedSession(s)
	if _, err := s.ProcessInput(context.Background(), "hello", nil); !errors.Is(err, errTranscriptFailedClosed) {
		t.Fatalf("input on a failed-closed session = %v, want the fail-closed refusal", err)
	}
	evs := served.settle(s)
	if failClosedDiagnostics(evs) != 1 {
		t.Fatal("no fail-closed diagnostic")
	}
	for _, ev := range evs {
		if data, ok := ev.Data.(events.ErrorData); ok && data.Recorded {
			t.Fatalf("error %+v is marked recorded", data)
		}
	}
}

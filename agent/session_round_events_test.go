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

// requirePairedRounds checks that got, the round and execution events of one
// execution, is that execution's start, then rounds that each end before the
// next starts, then its end with status; it returns the rounds' ids.
func requirePairedRounds(t *testing.T, got []string, turnID, status string) []string {
	t.Helper()
	if len(got) < 2 || got[0] != "EXECUTION_STARTED "+turnID || got[len(got)-1] != "EXECUTION_ENDED "+turnID+" "+status {
		t.Fatalf("round and execution events:\n%s\nwant execution %s bracketing them, ending %s", strings.Join(got, "\n"), turnID, status)
	}
	rounds := got[1 : len(got)-1]
	if len(rounds)%2 != 0 {
		t.Fatalf("unpaired round events:\n%s", strings.Join(rounds, "\n"))
	}
	var ids []string
	for i := 0; i < len(rounds); i += 2 {
		roundID, ok := strings.CutPrefix(rounds[i], "ROUND_STARTED ")
		if !ok || rounds[i+1] != "ROUND_ENDED "+roundID || slices.Contains(ids, roundID) {
			t.Fatalf("round events are not start/end pairs of distinct rounds:\n%s", strings.Join(rounds, "\n"))
		}
		ids = append(ids, roundID)
	}
	return ids
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
	if rounds := requirePairedRounds(t, roundAndExecutionEvents(t, evs), userInputTurnID(t, s), "failed"); len(rounds) != 1 {
		t.Fatalf("the failing execution ran rounds %v, want one", rounds)
	}
}

// A turn failure whose TURN_FAILURE entry is not recorded is not marked
// recorded: only the live error shows it.
func TestRoundEventsAnUnrecordedTurnFailureErrorIsNotMarkedRecorded(t *testing.T) {
	s, adapter := newExecutionSession(t)
	served := serveFailClosedSession(s)
	refuseEntries(t, s, schema.TurnFailure)
	adapter.script(func(context.Context) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil)
	})
	if _, err := s.ProcessInput(context.Background(), "fail", nil); err == nil {
		t.Fatal("the failing request succeeded")
	}
	evs := served.settle(s)
	if failures := entriesOfKind(transcriptTurnsOf(t, s), schema.TurnFailure); len(failures) != 0 {
		t.Fatalf("setup: %d TURN_FAILURE entries recorded, want none", len(failures))
	}
	var errs []events.ErrorData
	for _, ev := range evs {
		if data, ok := ev.Data.(events.ErrorData); ok {
			errs = append(errs, data)
		}
	}
	if len(errs) != 1 || errs[0].Recorded {
		t.Fatalf("error events = %+v, want one not marked recorded", errs)
	}
}

// An interrupted execution ends its one round, once, and ends interrupted.
func TestRoundEventsAnInterruptedExecutionEndsItsRound(t *testing.T) {
	s, adapter := newExecutionSession(t)
	served := serveFailClosedSession(s)
	ctx, cancel := context.WithCancel(context.Background())
	adapter.script(func(reqCtx context.Context) (llm.Response, error) {
		cancel()
		<-reqCtx.Done()
		return llm.Response{}, reqCtx.Err()
	})
	if _, err := s.ProcessInput(ctx, "interrupt me", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted input returned %v", err)
	}
	evs := served.settle(s)
	if rounds := requirePairedRounds(t, roundAndExecutionEvents(t, evs), userInputTurnID(t, s), "interrupted"); len(rounds) != 1 {
		t.Fatalf("the interrupted execution ran rounds %v, want one", rounds)
	}
}

// The bare-text retry opens a round of its own, which ends when the next one
// opens.
func TestRoundEventsTheBareTextRetryIsARoundOfItsOwn(t *testing.T) {
	s, adapter := newExecutionSession(t)
	served := serveFailClosedSession(s)
	adapter.script(respond(llm.Response{Message: llm.Assistant("thinking out loud")}))
	if _, err := s.ProcessInput(context.Background(), "bare text first", nil); err != nil {
		t.Fatal(err)
	}
	evs := served.settle(s)
	var want []string
	for _, assistant := range assistantEntries(t, s) {
		want = append(want, assistant.RoundID)
	}
	if len(want) < 2 {
		t.Fatalf("%d assistant entries; the bare text was not recorded before the retry", len(want))
	}
	if got := requirePairedRounds(t, roundAndExecutionEvents(t, evs), userInputTurnID(t, s), "completed"); !slices.Equal(got, want) {
		t.Fatalf("rounds %v, want the assistant entries' rounds %v", got, want)
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

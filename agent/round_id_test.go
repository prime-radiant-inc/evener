package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func assistantEntries(t *testing.T, s *Session) []schema.Turn {
	t.Helper()
	var out []schema.Turn
	for _, turn := range transcriptTurnsOf(t, s) {
		if turn.Kind == schema.TurnAssistant {
			out = append(out, turn)
		}
	}
	return out
}

func requireDistinctRoundIDs(t *testing.T, assistants []schema.Turn, want int) {
	t.Helper()
	if len(assistants) != want {
		t.Fatalf("%d assistant entries, want %d", len(assistants), want)
	}
	seen := map[string]bool{}
	for _, a := range assistants {
		if !strings.HasPrefix(a.RoundID, "r_") || seen[a.RoundID] {
			t.Fatalf("assistant round ids %q are not distinct r_ ids", a.RoundID)
		}
		seen[a.RoundID] = true
	}
}

func TestEachRecordedAssistantEntryHasItsOwnRoundID(t *testing.T) {
	s, adapter := newExecutionSession(t)
	args, _ := json.Marshal(map[string]any{"file_path": "missing.txt"})
	adapter.script(func(context.Context) (llm.Response, error) {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "read-1", Name: "read_file", Arguments: args, Type: "function"}},
		}}}, nil
	})
	if _, err := s.ProcessInput(context.Background(), "two rounds", nil); err != nil {
		t.Fatal(err)
	}
	requireDistinctRoundIDs(t, assistantEntries(t, s), 2)
}

func TestARetriedRequestKeepsItsRound(t *testing.T) {
	s, adapter := newExecutionSession(t)
	adapter.script(func(context.Context) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 503, "overloaded", nil, nil)
	})
	if _, err := s.ProcessInput(context.Background(), "retry", nil); err != nil {
		t.Fatal(err)
	}
	requireDistinctRoundIDs(t, assistantEntries(t, s), 1)
}

func TestTheBareTextRetryStartsANewRound(t *testing.T) {
	s, adapter := newExecutionSession(t)
	adapter.script(func(context.Context) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant("thinking out loud")}, nil
	})
	if _, err := s.ProcessInput(context.Background(), "bare text first", nil); err != nil {
		t.Fatal(err)
	}
	assistants := assistantEntries(t, s)
	if len(assistants) < 2 {
		t.Fatalf("%d assistant entries; the bare text was not recorded before the retry", len(assistants))
	}
	requireDistinctRoundIDs(t, assistants, len(assistants))
}

func TestTheNextTurnStartsANewRound(t *testing.T) {
	s, _ := newExecutionSession(t)
	for _, input := range []string{"first", "second"} {
		if _, err := s.ProcessInput(context.Background(), input, nil); err != nil {
			t.Fatal(err)
		}
	}
	requireDistinctRoundIDs(t, assistantEntries(t, s), 2)
}

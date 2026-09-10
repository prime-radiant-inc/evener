package agent

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestRoundTimingsDoNotBlockResponsesContinuationDelta(t *testing.T) {
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("question")),
		responsesContinuationEligibleAssistantTurn("response-1"),
		schema.NewTurn(schema.TurnRoundTimings, llm.System("timing")),
		schema.NewTurn(schema.TurnUserInput, llm.User("next")),
	}
	candidate, decision := selectResponsesContinuationAnchorCandidate(SessionConfig{}, history)
	if decision.HistoryMode != llm.HistoryModeResponsesDelta {
		t.Fatalf("continuation decision = %+v, want responses delta", decision)
	}
	if len(candidate.Delta) != 1 || candidate.Delta[0].Kind != schema.TurnUserInput {
		t.Fatalf("continuation delta = %v, want only next user turn", turnKinds(candidate.Delta))
	}
}

func TestRoundTimingsDoNotCountAsElicitationRecentTurn(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	s.contextMgr.CheckpointThreshold = 0
	s.contextMgr.PreserveRecentTurns = 1
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("foldable")),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent")),
		schema.NewTurn(schema.TurnRoundTimings, llm.System("timing")),
	}
	var got []schema.Turn
	s.elicitNoteFn = func(_ context.Context, foldable []schema.Turn) (string, error) {
		got = foldable
		return "", nil
	}
	s.maybeElicitNoteBeforeCompaction(context.Background(), history, 0)
	if len(got) != 1 || got[0].Message.Text() != "foldable" {
		t.Fatalf("elicitation prefix = %v, want only foldable turn", turnKinds(got))
	}
}

func TestRoundTimingsRepairKeepsCompletedToolPair(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call-1", Name: "lookup", Arguments: []byte(`{}`)}}}}},
		schema.NewTurn(schema.TurnRoundTimings, llm.System("timing")),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResult("call-1", "result", false)),
	}
	got, repairs := repairOrphanedToolResults(history)
	if repairs != 0 {
		t.Fatalf("round timing between tool call and result caused %d repair(s)", repairs)
	}
	if !reflect.DeepEqual(got, history) {
		t.Fatalf("repaired history = %v, want original completed tool pair", turnKinds(got))
	}
}

func TestRoundTimingsDelegateSnapshotKeepsCompletedToolPair(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	completedCall := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "completed-call", Name: "lookup", Arguments: []byte(`{"key":"opaque"}`)}}}}
	completedResult := llm.ToolResult("completed-call", "completed-result-sentinel", false)
	for _, turn := range []struct {
		kind schema.TurnKind
		msg  llm.Message
	}{
		{schema.TurnUserInput, llm.User("completed question")},
		{schema.TurnAssistant, completedCall},
		{schema.TurnRoundTimings, llm.System("timing marker")},
		{schema.TurnToolResults, completedResult},
		{schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "pending-call", Name: "pending", Arguments: []byte(`{}`)}}}}},
		{schema.TurnRoundTimings, llm.System("pending timing marker")},
	} {
		if err := s.appendTurnWithDurableTranscriptMessage(turn.kind, turn.msg, turn.msg); err != nil {
			t.Fatalf("append %s: %v", turn.kind, err)
		}
	}

	entries, err := s.snapshotDelegateContext()
	if err != nil {
		t.Fatalf("snapshotDelegateContext: %v", err)
	}
	var got []schema.Turn
	for _, entry := range entries {
		got = append(got, entry.Turn)
	}
	if len(got) != 3 || got[0].Kind != schema.TurnUserInput || got[1].Kind != schema.TurnAssistant || got[2].Kind != schema.TurnToolResults {
		t.Fatalf("snapshot turns = %v, want completed user/assistant/tool-result prefix", turnKinds(got))
	}
	if len(got[2].Message.Content) != 1 || got[2].Message.Content[0].ToolResult == nil || got[2].Message.Content[0].ToolResult.Content != "completed-result-sentinel" {
		t.Fatalf("completed tool result = %#v, want sentinel", got[2].Message.Content)
	}
	for _, turn := range got {
		if turn.Kind == schema.TurnRoundTimings {
			t.Fatal("round timing marker entered delegate context")
		}
	}
}

func TestRoundTimingsDoNotEnterEvaluationRequests(t *testing.T) {
	before := schema.NewTurn(schema.TurnUserInput, llm.User("input sentinel"))
	after := schema.NewTurn(schema.TurnAssistant, llm.Assistant("output sentinel"))
	marker := schema.NewTurn(schema.TurnRoundTimings, llm.System("diagnostic sentinel"))
	if got, want := turnsToMessages([]schema.Turn{before, marker, after}), turnsToMessages([]schema.Turn{before, after}); !reflect.DeepEqual(got, want) {
		t.Fatalf("evaluation request contains timing metadata: %#v", got)
	}
}

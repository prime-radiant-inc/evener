package agent

import (
	"context"
	"reflect"
	"strings"
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
	const foldable = "opaque-fold-4b7d"
	const recent = "opaque-recent-a1c8"
	const note = "opaque-note-55bc"
	calls := 0
	s := newTestSessionForEnvctx(t, withSteps(func(req llm.Request) llm.Response {
		calls++
		var received strings.Builder
		for _, message := range req.Messages {
			received.WriteString(message.Text())
		}
		if !strings.Contains(received.String(), foldable) || strings.Contains(received.String(), recent) {
			t.Error("elicitation request did not preserve the foldable versus recent input boundary")
		}
		return llm.Response{Message: llm.Assistant(note)}
	}))
	s.contextMgr.SetProfile(NewOpenAIProfile("gpt-test"))
	s.contextMgr.CheckpointThreshold = 0
	s.contextMgr.PreserveRecentTurns = 1
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(foldable)),
		schema.NewTurn(schema.TurnUserInput, llm.User(recent)),
		schema.NewTurn(schema.TurnRoundTimings, llm.System("timing")),
	}
	s.maybeElicitNoteBeforeCompaction(context.Background(), history, 0)
	if calls != 1 || s.PinnedNote() != note {
		t.Fatalf("elicitation calls=%d, pinned note=%q", calls, s.PinnedNote())
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

func TestRoundTimingsAloneDoNotTriggerNoteElicitation(t *testing.T) {
	for _, preserveRecent := range []int{0, 1} {
		s := newTestSessionForEnvctx(t, withSteps(func(llm.Request) llm.Response {
			t.Error("timing metadata alone triggered a model call")
			return llm.Response{Message: llm.Assistant("unexpected note")}
		}))
		s.contextMgr.SetProfile(NewOpenAIProfile("gpt-test"))
		s.contextMgr.CheckpointThreshold = 0
		s.contextMgr.PreserveRecentTurns = preserveRecent
		s.maybeElicitNoteBeforeCompaction(context.Background(), []schema.Turn{schema.NewTurn(schema.TurnRoundTimings, llm.System("timing"))}, 0)
		if s.PinnedNote() != "" {
			t.Errorf("preserveRecent=%d: unexpected pinned note", preserveRecent)
		}
	}
}

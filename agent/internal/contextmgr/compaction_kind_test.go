package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestCheckpoint_UsesTurnCheckpointKind(t *testing.T) {
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug in login.go")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"login.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", "1 | package main\n", false)},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c2", "edit_file", `{"file_path":"login.go","old_string":"old","new_string":"new"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c2", "edit_file", "OK", false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("done")},
	}

	result := checkpoint(history, 2, nil, "communicate")

	if len(result) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(result))
	}
	if result[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("checkpoint turn kind = %q, want %q", result[0].Kind, schema.TurnCheckpoint)
	}
	// The text content should still have [CONTEXT CHECKPOINT] header.
	text := result[0].Message.Text()
	if !strings.Contains(text, "[CONTEXT CHECKPOINT]") {
		t.Fatalf("checkpoint missing header: %q", text)
	}
}

func TestSummarizeWithLLM_UsesTurnSummaryKind(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("Summary: fixed auth bug")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("I'll fix it")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	result, err := cm.summarizeWithLLM(context.Background(), history, 2)
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}

	if len(result) < 1 {
		t.Fatalf("expected at least 1 turn, got %d", len(result))
	}
	if result[0].Kind != schema.TurnSummary {
		t.Fatalf("summary turn kind = %q, want %q", result[0].Kind, schema.TurnSummary)
	}
	// The text content should still have [CONTEXT SUMMARY] header.
	text := result[0].Message.Text()
	if !strings.Contains(text, "[CONTEXT SUMMARY]") {
		t.Fatalf("summary missing header: %q", text)
	}
}

func TestMaybeCompact_CallsOnCompactionTurn(t *testing.T) {
	// Use a tiny context window to force checkpoint (L3).
	profile := testProfile("openai", "test", 500)
	cm := NewManager(profile, nil)
	cm.PreserveRecentTurns = 2

	// Use assistant text (not tool results) so observation masking can't reduce pressure.
	// Need >80% of 500 = 400 tokens.
	history := []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")}}
	for estimateTokens(history) < 425 {
		history = append(history,
			schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("analysis ", 50))},
		)
	}
	history = append(history,
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var callbackTurns []schema.Turn
	cm.OnCompactionTurn = func(turn schema.Turn) {
		callbackTurns = append(callbackTurns, turn)
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	if len(callbackTurns) == 0 {
		t.Fatal("expected OnCompactionTurn callback to be called")
	}
	if callbackTurns[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("callback turn kind = %q, want %q", callbackTurns[0].Kind, schema.TurnCheckpoint)
	}
	if !strings.Contains(callbackTurns[0].Message.Text(), "[CONTEXT CHECKPOINT]") {
		t.Fatalf("callback turn missing checkpoint text: %q", callbackTurns[0].Message.Text())
	}
}

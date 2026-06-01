package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/llm"
)

func TestObsMaskStrategy_SatisfiesInterface(t *testing.T) {
	var _ contextStrategy = (*obsMaskStrategy)(nil)
}

func TestObsMaskStrategy_Name(t *testing.T) {
	s := &obsMaskStrategy{}
	if s.Name() != "obs-mask" {
		t.Errorf("expected name %q, got %q", "obs-mask", s.Name())
	}
}

func TestObsMaskStrategy_Tools_ReturnsNil(t *testing.T) {
	s := &obsMaskStrategy{}
	if tools := s.Tools(); tools != nil {
		t.Errorf("expected nil tools, got %v", tools)
	}
}

func TestObsMaskStrategy_AfterAction_Noop(t *testing.T) {
	s := &obsMaskStrategy{}
	err := s.AfterAction(context.Background(), nil, nil)
	if err != nil {
		t.Errorf("AfterAction should be no-op, got error: %v", err)
	}
}

func TestObsMaskStrategy_ManageContext_NoCompactionBelowThreshold(t *testing.T) {
	client := llm.NewClient()
	profile := testOpenAIProfileWithContextWindow(1000)
	cm := newContextManager(profile, client)
	s := newObsMaskStrategy(cm)

	history := []Turn{
		NewTurn(TurnUserInput, llm.User("hello")),
		NewTurn(TurnAssistant, llm.Assistant("hi")),
	}

	emitted := false
	emitFn := func(kind events.EventKind, data events.EventData) { emitted = true }

	err := s.ManageContext(context.Background(), &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}
	if emitted {
		t.Error("did not expect compaction event for small history")
	}
	if len(history) != 2 {
		t.Errorf("expected 2 turns, got %d", len(history))
	}
}

func TestAggressiveMaskObservations_MasksToolOutput(t *testing.T) {
	history := []Turn{
		NewTurn(TurnUserInput, llm.User("read the file")),
		NewTurn(TurnAssistant, llm.Assistant("I'll read it")),
		{Kind: TurnToolResults, Message: llm.Message{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					Name:    "read_file",
					Content: "This is a very long file content that should be masked entirely by the aggressive masking strategy because it is not needed.",
				}},
			},
		}},
		NewTurn(TurnAssistant, llm.Assistant("I see the content")),
		// These last 2 turns are "recent" and should be preserved.
		NewTurn(TurnUserInput, llm.User("now edit it")),
		NewTurn(TurnAssistant, llm.Assistant("editing now")),
	}

	aggressiveMaskObservations(history, 2) // preserve last 2

	// The tool result at index 2 should be masked.
	tr := history[2].Message.Content[0].ToolResult
	content, ok := tr.Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", tr.Content)
	}
	if !strings.HasPrefix(content, "[read_file: OK]") {
		t.Errorf("expected masked content, got: %s", content)
	}
}

func TestAggressiveMaskObservations_PreservesErrors(t *testing.T) {
	history := []Turn{
		NewTurn(TurnUserInput, llm.User("do something")),
		{Kind: TurnToolResults, Message: llm.Message{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					Name:    "shell",
					Content: "error: command not found",
					IsError: true,
				}},
			},
		}},
		NewTurn(TurnAssistant, llm.Assistant("handling error")),
		NewTurn(TurnUserInput, llm.User("next")),
	}

	aggressiveMaskObservations(history, 1)

	// Error result should NOT be masked.
	tr := history[1].Message.Content[0].ToolResult
	content, ok := tr.Content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", tr.Content)
	}
	if content != "error: command not found" {
		t.Errorf("error content should be preserved, got: %s", content)
	}
}

func TestAggressiveMaskObservations_SkipsAlreadyMasked(t *testing.T) {
	history := []Turn{
		NewTurn(TurnUserInput, llm.User("do something")),
		{Kind: TurnToolResults, Message: llm.Message{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					Name:    "read_file",
					Content: "[read_file: OK]",
				}},
			},
		}},
		NewTurn(TurnAssistant, llm.Assistant("done")),
		NewTurn(TurnUserInput, llm.User("next")),
	}

	aggressiveMaskObservations(history, 1)

	tr := history[1].Message.Content[0].ToolResult
	content, _ := tr.Content.(string)
	if content != "[read_file: OK]" {
		t.Errorf("already-masked content should be unchanged, got: %s", content)
	}
}

func TestObsMaskStrategy_ManageContext_FiresOnCompactionTurn_Checkpoint(t *testing.T) {
	client := llm.NewClient()
	profile := testOpenAIProfileWithContextWindow(1000)
	cm := newContextManager(profile, client)
	// Set thresholds so both layers fire.
	cm.ObservationMaskThreshold = 0.0001
	cm.CheckpointThreshold = 0.0001
	cm.PreserveRecentTurns = 2

	var callbackTurns []Turn
	cm.OnCompactionTurn = func(t Turn) {
		callbackTurns = append(callbackTurns, t)
	}

	s := newObsMaskStrategy(cm)

	history := []Turn{
		NewTurn(TurnUserInput, llm.User("fix the bug in auth.go")),
		NewTurn(TurnAssistant, llm.Assistant("I'll fix it")),
		NewTurn(TurnUserInput, llm.User("also fix tests")),
		NewTurn(TurnAssistant, llm.Assistant("fixing tests")),
		NewTurn(TurnUserInput, llm.User("what's the status")),
		NewTurn(TurnAssistant, llm.Assistant("almost done")),
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}

	err := s.ManageContext(context.Background(), &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Callback should have been fired with the checkpoint turn.
	if len(callbackTurns) != 1 {
		t.Fatalf("expected 1 callback turn, got %d", len(callbackTurns))
	}
	if callbackTurns[0].Kind != TurnCheckpoint {
		t.Errorf("expected TurnCheckpoint, got %s", callbackTurns[0].Kind)
	}
	if !strings.Contains(callbackTurns[0].Message.Text(), "[CONTEXT CHECKPOINT]") {
		t.Errorf("expected checkpoint content, got: %s", callbackTurns[0].Message.Text()[:80])
	}
}

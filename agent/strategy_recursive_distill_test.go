package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestRecursiveDistillStrategy_SatisfiesInterface(t *testing.T) {
	var _ contextStrategy = (*recursiveDistillStrategy)(nil)
}

func TestRecursiveDistillStrategy_Name(t *testing.T) {
	s := &recursiveDistillStrategy{}
	if s.Name() != "recursive-distill" {
		t.Errorf("expected name %q, got %q", "recursive-distill", s.Name())
	}
}

func TestRecursiveDistillStrategy_Tools_ReturnsNil(t *testing.T) {
	s := &recursiveDistillStrategy{}
	if tools := s.Tools(); tools != nil {
		t.Errorf("expected nil tools, got %v", tools)
	}
}

func TestRecursiveDistillStrategy_AfterAction_NoMicroBelowThreshold(t *testing.T) {
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")
	cm := newContextManager(profile, client)
	s := newRecursiveDistillStrategy(cm)

	// 5 turns — not enough for micro-summary (needs 10).
	history := make([]Turn, 5)
	for i := range history {
		history[i] = NewTurn(TurnAssistant, llm.Assistant("turn"))
	}

	err := s.AfterAction(context.Background(), history, client)
	if err != nil {
		t.Fatalf("AfterAction returned error: %v", err)
	}
	if len(s.microSummaries) != 0 {
		t.Errorf("expected 0 micro-summaries, got %d", len(s.microSummaries))
	}
}

func TestRecursiveDistillStrategy_AfterAction_MicroAt10Turns(t *testing.T) {
	client := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Model:   "gpt-4.1-mini",
					Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
					Message: llm.Assistant("Read config files and identified database connection issue."),
				}
			},
		},
	}
	client.Register(f)

	profile := NewOpenAIProfile("gpt-5.2")
	cm := newContextManager(profile, client)
	s := newRecursiveDistillStrategy(cm)

	// 10 turns — should trigger micro-summary.
	history := make([]Turn, 10)
	for i := range history {
		history[i] = NewTurn(TurnAssistant, llm.Assistant("working on step"))
	}

	err := s.AfterAction(context.Background(), history, client)
	if err != nil {
		t.Fatalf("AfterAction returned error: %v", err)
	}
	if len(s.microSummaries) != 1 {
		t.Fatalf("expected 1 micro-summary, got %d", len(s.microSummaries))
	}
	if s.microSummaries[0] == "" {
		t.Error("expected non-empty micro-summary")
	}
	if s.lastMicroAt != 10 {
		t.Errorf("expected lastMicroAt=10, got %d", s.lastMicroAt)
	}
}

func TestRecursiveDistillStrategy_AfterAction_MacroAt50Turns(t *testing.T) {
	client := llm.NewClient()
	callIndex := 0
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Micro-summary LLM call.
			func(req llm.Request) llm.Response {
				callIndex++
				return llm.Response{
					Model:   "gpt-4.1-mini",
					Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
					Message: llm.Assistant("Latest micro-summary of actions."),
				}
			},
			// Macro-summary LLM call.
			func(req llm.Request) llm.Response {
				callIndex++
				return llm.Response{
					Model:   "gpt-4.1-mini",
					Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
					Message: llm.Assistant("Overall: investigated bug, applied fix, tests pass."),
				}
			},
		},
	}
	client.Register(f)

	profile := NewOpenAIProfile("gpt-5.2")
	cm := newContextManager(profile, client)
	s := newRecursiveDistillStrategy(cm)

	// Pre-populate with 4 micro-summaries (simulating turns 10-40).
	s.microSummaries = []string{
		"Read files and understood the bug.",
		"Tried a fix that didn't work.",
		"Found the root cause in parser.go.",
		"Applied fix and ran linter.",
	}
	s.lastMicroAt = 40

	// 50 turns — should trigger both micro (5th one) AND macro.
	history := make([]Turn, 50)
	for i := range history {
		history[i] = NewTurn(TurnAssistant, llm.Assistant("working"))
	}

	err := s.AfterAction(context.Background(), history, client)
	if err != nil {
		t.Fatalf("AfterAction returned error: %v", err)
	}

	// Micro-summaries should have been folded into macro.
	if len(s.macroSummaries) != 1 {
		t.Fatalf("expected 1 macro-summary, got %d", len(s.macroSummaries))
	}
	if s.macroSummaries[0] == "" {
		t.Error("expected non-empty macro-summary")
	}
	// Micro-summaries should be reset after folding.
	if s.microSummaries != nil {
		t.Errorf("expected nil micro-summaries after macro fold, got %d", len(s.microSummaries))
	}
	if callIndex != 2 {
		t.Errorf("expected 2 LLM calls (micro + macro), got %d", callIndex)
	}
}

func TestRecursiveDistillStrategy_InjectDistilledContext(t *testing.T) {
	cm := newContextManager(NewOpenAIProfile("gpt-5.2"), nil)
	s := newRecursiveDistillStrategy(cm)
	s.macroSummaries = []string{"Phase 1: investigated and found root cause."}
	s.microSummaries = []string{"Applied fix to auth.go.", "Running tests."}

	history := []Turn{
		NewTurn(TurnUserInput, llm.User("task")),
		NewTurn(TurnAssistant, llm.Assistant("working")),
	}

	s.injectDistilledContext(&history)

	if len(history) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(history))
	}

	last := history[2]
	if last.Kind != TurnSteering {
		t.Errorf("expected TurnSteering, got %v", last.Kind)
	}
	text := last.Message.Text()
	if !strings.Contains(text, "[DISTILLED MEMORY]") {
		t.Error("expected distilled memory marker")
	}
	if !strings.Contains(text, "Session overview:") {
		t.Error("expected session overview section (from macro-summaries)")
	}
	if !strings.Contains(text, "Recent actions:") {
		t.Error("expected recent actions section (from micro-summaries)")
	}
	if !strings.Contains(text, "Applied fix to auth.go") {
		t.Error("expected micro-summary content")
	}
}

func TestRecursiveDistillStrategy_InjectDistilledContext_RemovesOld(t *testing.T) {
	cm := newContextManager(NewOpenAIProfile("gpt-5.2"), nil)
	s := newRecursiveDistillStrategy(cm)
	s.microSummaries = []string{"new summary"}

	history := []Turn{
		NewTurn(TurnUserInput, llm.User("task")),
		NewTurn(TurnSteering, llm.User("[DISTILLED MEMORY]\nold stuff\n[END DISTILLED MEMORY]")),
		NewTurn(TurnAssistant, llm.Assistant("working")),
	}

	s.injectDistilledContext(&history)

	distillCount := 0
	for _, t := range history {
		if t.Kind == TurnSteering && strings.Contains(t.Message.Text(), "[DISTILLED MEMORY]") {
			distillCount++
		}
	}
	if distillCount != 1 {
		t.Errorf("expected exactly 1 distilled memory turn, got %d", distillCount)
	}
}

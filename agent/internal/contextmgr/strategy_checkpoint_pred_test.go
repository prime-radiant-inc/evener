package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// Compile-time assertion: CheckpointPredStrategy must satisfy Strategy.
var _ Strategy = (*CheckpointPredStrategy)(nil)

func TestCheckpointPredStrategy_Name(t *testing.T) {
	s := &CheckpointPredStrategy{}
	if s.Name() != "checkpoint-pred" {
		t.Errorf("expected name %q, got %q", "checkpoint-pred", s.Name())
	}
}

func TestCheckpointPredStrategy_Tools_ReturnsNil(t *testing.T) {
	s := &CheckpointPredStrategy{}
	if tools := s.Tools(); tools != nil {
		t.Errorf("expected nil tools, got %v", tools)
	}
}

func TestCheckpointPredStrategy_AfterAction_Noop(t *testing.T) {
	s := &CheckpointPredStrategy{}
	err := s.AfterAction(context.Background(), nil, nil)
	if err != nil {
		t.Errorf("AfterAction should be no-op, got error: %v", err)
	}
}

func TestCheckpointPredStrategy_ManageContext_NoCompactionBelowThreshold(t *testing.T) {
	client := llm.NewClient()
	profile := testOpenAIProfileWithContextWindow(1000)
	cm := NewManager(profile, client)
	s := NewCheckpointPredStrategy(cm)

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("hi")),
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
}

func TestCheckpointPredStrategy_PredictiveCheckpoint_FallbackOnError(t *testing.T) {
	// When no LLM client is available, predictive checkpoint should fall back
	// to deterministic checkpoint.
	client := llm.NewClient()
	profile := testOpenAIProfileWithContextWindow(1000)
	cm := NewManager(profile, client)
	// Set very low thresholds so compaction fires.
	cm.ObservationMaskThreshold = 0.0001
	cm.ThinkingClearThreshold = 0.0001
	cm.CheckpointThreshold = 0.0001
	cm.SummarizeThreshold = 2.0 // Disable layer 4.
	cm.PreserveRecentTurns = 2

	// No adapter registered = LLM calls will fail.
	s := NewCheckpointPredStrategy(cm)

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug in auth.go")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnUserInput, llm.User("also fix tests")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("fixing tests")),
		schema.NewTurn(schema.TurnUserInput, llm.User("what's the status")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("almost done")),
	}

	var layers []string
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if cd, ok := data.(events.ContextCompactionData); ok {
				layers = append(layers, cd.Layer)
			}
		}
	}

	err := s.ManageContext(context.Background(), &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Should have fallen back to checkpoint.
	found := false
	for _, l := range layers {
		if l == "checkpoint_pred" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected checkpoint_pred layer event, got: %v", layers)
	}

	// History should be compacted (checkpoint + preserved recent).
	if len(history) > 4 {
		t.Errorf("expected compacted history, got %d turns", len(history))
	}

	// First turn should be a checkpoint.
	first := history[0].Message.Text()
	if !strings.HasPrefix(first, "[CONTEXT CHECKPOINT]") {
		t.Errorf("expected deterministic checkpoint fallback, got: %s", first[:min(50, len(first))])
	}
}

func TestCheckpointPredStrategy_PredictiveCheckpoint_WithLLM(t *testing.T) {
	client := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Predictive checkpoint LLM call.
			func(req llm.Request) llm.Response {
				return llm.Response{
					Model:   "gpt-4.1-mini",
					Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
					Message: llm.Assistant("Task: Fix auth bug in auth.go. Progress: Read the file, identified the issue. Next: Apply fix and run tests."),
				}
			},
		},
	}
	client.Register(f)

	profile := testOpenAIProfileWithContextWindow(1000)
	cm := NewManager(profile, client)
	cm.ObservationMaskThreshold = 0.0001
	cm.ThinkingClearThreshold = 0.0001
	cm.CheckpointThreshold = 0.0001
	cm.SummarizeThreshold = 2.0
	cm.PreserveRecentTurns = 2

	s := NewCheckpointPredStrategy(cm)

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug in auth.go")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnUserInput, llm.User("also fix tests")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("fixing tests")),
		schema.NewTurn(schema.TurnUserInput, llm.User("what's the status")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("almost done")),
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}

	err := s.ManageContext(context.Background(), &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// First turn should be predictive checkpoint.
	first := history[0].Message.Text()
	if !strings.Contains(first, "[CONTEXT CHECKPOINT - PREDICTIVE]") {
		t.Errorf("expected predictive checkpoint, got: %s", first[:min(80, len(first))])
	}
	if !strings.Contains(first, "Fix auth bug") {
		t.Errorf("expected LLM-generated checkpoint content, got: %s", first)
	}
}

func TestCheckpointPredStrategy_PredictiveCheckpoint_TurnKind(t *testing.T) {
	// predictiveCheckpoint() should create TurnCheckpoint, not TurnUserInput.
	client := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Model:   "gpt-4.1-mini",
					Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
					Message: llm.Assistant("Predicted checkpoint content."),
				}
			},
		},
	}
	client.Register(f)

	profile := testOpenAIProfileWithContextWindow(1000)
	cm := NewManager(profile, client)
	cm.ObservationMaskThreshold = 0.0001
	cm.ThinkingClearThreshold = 0.0001
	cm.CheckpointThreshold = 0.0001
	cm.SummarizeThreshold = 2.0
	cm.PreserveRecentTurns = 2

	s := NewCheckpointPredStrategy(cm)

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnUserInput, llm.User("also fix tests")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("fixing tests")),
		schema.NewTurn(schema.TurnUserInput, llm.User("status")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("almost done")),
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}

	err := s.ManageContext(context.Background(), &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// First turn should be TurnCheckpoint, not TurnUserInput.
	if history[0].Kind != schema.TurnCheckpoint {
		t.Errorf("expected TurnCheckpoint for predictive checkpoint, got %s", history[0].Kind)
	}
}

func TestCheckpointPredStrategy_FiresOnCompactionTurn_FallbackCheckpoint(t *testing.T) {
	// When predictiveCheckpoint fails and falls back to deterministic checkpoint,
	// the callback should fire with the checkpoint turn.
	client := llm.NewClient()
	profile := testOpenAIProfileWithContextWindow(1000)
	cm := NewManager(profile, client)
	cm.ObservationMaskThreshold = 0.0001
	cm.ThinkingClearThreshold = 0.0001
	cm.CheckpointThreshold = 0.0001
	cm.SummarizeThreshold = 2.0
	cm.PreserveRecentTurns = 2

	var callbackTurns []schema.Turn
	cm.OnCompactionTurn = func(t schema.Turn) {
		callbackTurns = append(callbackTurns, t)
	}

	// No adapter registered = LLM calls fail → fallback to deterministic checkpoint.
	s := NewCheckpointPredStrategy(cm)

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnUserInput, llm.User("also fix tests")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("fixing tests")),
		schema.NewTurn(schema.TurnUserInput, llm.User("status")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("almost done")),
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}

	err := s.ManageContext(context.Background(), &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Callback should have fired with the checkpoint turn.
	if len(callbackTurns) != 1 {
		t.Fatalf("expected 1 callback turn for fallback checkpoint, got %d", len(callbackTurns))
	}
	if callbackTurns[0].Kind != schema.TurnCheckpoint {
		t.Errorf("expected TurnCheckpoint, got %s", callbackTurns[0].Kind)
	}
}

func TestCheckpointPredStrategy_FiresOnCompactionTurn_PredictiveCheckpoint(t *testing.T) {
	// When predictiveCheckpoint succeeds, the callback should fire with the checkpoint turn.
	client := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Model:   "gpt-4.1-mini",
					Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
					Message: llm.Assistant("Predicted checkpoint content."),
				}
			},
		},
	}
	client.Register(f)

	profile := testOpenAIProfileWithContextWindow(1000)
	cm := NewManager(profile, client)
	cm.ObservationMaskThreshold = 0.0001
	cm.ThinkingClearThreshold = 0.0001
	cm.CheckpointThreshold = 0.0001
	cm.SummarizeThreshold = 2.0
	cm.PreserveRecentTurns = 2

	var callbackTurns []schema.Turn
	cm.OnCompactionTurn = func(t schema.Turn) {
		callbackTurns = append(callbackTurns, t)
	}

	s := NewCheckpointPredStrategy(cm)

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnUserInput, llm.User("also fix tests")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("fixing tests")),
		schema.NewTurn(schema.TurnUserInput, llm.User("status")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("almost done")),
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}

	err := s.ManageContext(context.Background(), &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Callback should have fired with the predictive checkpoint turn.
	if len(callbackTurns) != 1 {
		t.Fatalf("expected 1 callback turn for predictive checkpoint, got %d", len(callbackTurns))
	}
	if callbackTurns[0].Kind != schema.TurnCheckpoint {
		t.Errorf("expected TurnCheckpoint, got %s", callbackTurns[0].Kind)
	}
	if !strings.Contains(callbackTurns[0].Message.Text(), "[CONTEXT CHECKPOINT - PREDICTIVE]") {
		t.Errorf("expected predictive checkpoint content, got: %s", callbackTurns[0].Message.Text()[:80])
	}
}

func TestCheckpointPredStrategy_FiresOnCompactionTurn_Summarize(t *testing.T) {
	// When LLM summarization runs (layer 4), the callback should fire with the summary turn.
	client := llm.NewClient()
	callCount := 0
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// First call: predictive checkpoint.
			func(req llm.Request) llm.Response {
				callCount++
				return llm.Response{
					Model:   "gpt-4.1-mini",
					Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
					Message: llm.Assistant("Predicted checkpoint."),
				}
			},
			// Second call: LLM summarization.
			func(req llm.Request) llm.Response {
				callCount++
				return llm.Response{
					Model:   "gpt-4.1-mini",
					Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
					Message: llm.Assistant("Summary of the conversation so far."),
				}
			},
		},
	}
	client.Register(f)

	profile := testOpenAIProfileWithContextWindow(1000)
	cm := NewManager(profile, client)
	// All thresholds very low so all layers fire.
	cm.ObservationMaskThreshold = 0.0001
	cm.ThinkingClearThreshold = 0.0001
	cm.CheckpointThreshold = 0.0001
	cm.SummarizeThreshold = 0.0001
	cm.PreserveRecentTurns = 2

	var callbackTurns []schema.Turn
	cm.OnCompactionTurn = func(t schema.Turn) {
		callbackTurns = append(callbackTurns, t)
	}

	s := NewCheckpointPredStrategy(cm)

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnUserInput, llm.User("also fix tests")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("fixing tests")),
		schema.NewTurn(schema.TurnUserInput, llm.User("status")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("almost done")),
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}

	err := s.ManageContext(context.Background(), &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Should have 2 callback turns: checkpoint + summary.
	if len(callbackTurns) != 2 {
		t.Fatalf("expected 2 callback turns (checkpoint+summary), got %d", len(callbackTurns))
	}
	if callbackTurns[0].Kind != schema.TurnCheckpoint {
		t.Errorf("expected first callback to be TurnCheckpoint, got %s", callbackTurns[0].Kind)
	}
	if callbackTurns[1].Kind != schema.TurnSummary {
		t.Errorf("expected second callback to be TurnSummary, got %s", callbackTurns[1].Kind)
	}
}

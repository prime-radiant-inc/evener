package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestMemoryCrystalsStrategy_SatisfiesInterface(t *testing.T) {
	var _ Strategy = (*MemoryCrystalsStrategy)(nil)
}

func TestMemoryCrystalsStrategy_Name(t *testing.T) {
	s := &MemoryCrystalsStrategy{}
	if s.Name() != "memory-crystals" {
		t.Errorf("expected name %q, got %q", "memory-crystals", s.Name())
	}
}

func TestMemoryCrystalsStrategy_Tools_ReturnsNil(t *testing.T) {
	s := &MemoryCrystalsStrategy{}
	if tools := s.Tools(); tools != nil {
		t.Errorf("expected nil tools, got %v", tools)
	}
}

func TestMemoryCrystalsStrategy_AfterAction_SkipsNonThirdTurn(t *testing.T) {
	client := llm.NewClient()
	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewManager(profile, client)
	s := NewMemoryCrystalsStrategy(cm)

	// 2 turns — not a multiple of 3, so no crystallization.
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("hi")),
	}

	err := s.AfterAction(context.Background(), history, client)
	if err != nil {
		t.Fatalf("AfterAction returned error: %v", err)
	}
	if len(s.crystals) != 0 {
		t.Errorf("expected 0 crystals, got %d", len(s.crystals))
	}
}

func TestMemoryCrystalsStrategy_AfterAction_CrystallizesEveryThird(t *testing.T) {
	client := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{
					Model:   "gpt-4.1-mini",
					Finish:  llm.FinishReason{Reason: llm.FinishReasonStop},
					Message: llm.Assistant("Modified auth.go:42, fixed nil pointer, tests pass"),
				}
			},
		},
	}
	client.Register(f)

	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewManager(profile, client)
	s := NewMemoryCrystalsStrategy(cm)

	// 3 turns — multiple of 3, should trigger crystallization.
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("fixing")),
		{Kind: schema.TurnToolResults, Message: llm.Message{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
					Name:    "edit_file",
					Content: "OK",
				}},
			},
		}},
	}

	err := s.AfterAction(context.Background(), history, client)
	if err != nil {
		t.Fatalf("AfterAction returned error: %v", err)
	}
	if len(s.crystals) != 1 {
		t.Fatalf("expected 1 crystal, got %d", len(s.crystals))
	}
	if s.crystals[0].Facts == "" {
		t.Error("expected non-empty facts")
	}
	if s.crystals[0].Action != "edit_file" {
		t.Errorf("expected action 'edit_file', got %q", s.crystals[0].Action)
	}
}

func TestMemoryCrystalsStrategy_InjectCrystals(t *testing.T) {
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil)
	s := NewMemoryCrystalsStrategy(cm)
	s.crystals = []MemoryCrystal{
		{Turn: 3, Action: "read_file", Facts: "Read auth.go, 200 lines"},
		{Turn: 6, Action: "edit_file", Facts: "Fixed nil check at line 42"},
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
	}

	s.injectCrystals(&history)

	// Should have 3 turns: original 2 + crystal steering.
	if len(history) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(history))
	}

	// Crystal should be the last turn.
	last := history[2]
	if last.Kind != schema.TurnSteering {
		t.Errorf("expected TurnSteering, got %v", last.Kind)
	}
	text := last.Message.Text()
	if !strings.Contains(text, "[MEMORY CRYSTALS]") {
		t.Error("expected crystal marker in steering message")
	}
	if !strings.Contains(text, "Read auth.go") {
		t.Error("expected crystal fact about reading auth.go")
	}
	if !strings.Contains(text, "Fixed nil check") {
		t.Error("expected crystal fact about fixing nil check")
	}
}

func TestMemoryCrystalsStrategy_InjectCrystals_RemovesOld(t *testing.T) {
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil)
	s := NewMemoryCrystalsStrategy(cm)
	s.crystals = []MemoryCrystal{
		{Turn: 3, Action: "test", Facts: "new crystal"},
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("task")),
		schema.NewTurn(schema.TurnSteering, llm.User("[MEMORY CRYSTALS]\nold crystal\n[END CRYSTALS]")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
	}

	s.injectCrystals(&history)

	// Old crystal should be removed, new one appended.
	crystalCount := 0
	for _, t := range history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), "[MEMORY CRYSTALS]") {
			crystalCount++
		}
	}
	if crystalCount != 1 {
		t.Errorf("expected exactly 1 crystal turn, got %d", crystalCount)
	}

	// New crystal should contain "new crystal".
	last := history[len(history)-1]
	if !strings.Contains(last.Message.Text(), "new crystal") {
		t.Error("expected new crystal content")
	}
}

func TestMemoryCrystalsStrategy_PruneOldCrystals(t *testing.T) {
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil)
	s := NewMemoryCrystalsStrategy(cm)

	// Add 25 crystals.
	for i := 0; i < 25; i++ {
		s.crystals = append(s.crystals, MemoryCrystal{Turn: i, Action: "test", Facts: "fact"})
	}

	s.pruneOldCrystals()

	if len(s.crystals) != 20 {
		t.Errorf("expected 20 crystals after pruning, got %d", len(s.crystals))
	}
	// Oldest should be pruned (kept last 20).
	if s.crystals[0].Turn != 5 {
		t.Errorf("expected first crystal to be turn 5, got %d", s.crystals[0].Turn)
	}
}

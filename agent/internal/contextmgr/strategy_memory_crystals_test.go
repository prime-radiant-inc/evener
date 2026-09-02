package contextmgr

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
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
	// spy records all Complete calls so we can assert the guard suppresses them.
	spy := &fakeAdapter{name: "openai"}
	client.Register(spy)

	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewManager(profile, client, cheapmodel.New(client))
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
	// The modulus guard must prevent any LLM call; spy proves it.
	if got := len(spy.Requests()); got != 0 {
		t.Errorf("expected no LLM calls (modulus guard should skip), got %d", got)
	}
	if len(s.crystals) != 0 {
		t.Errorf("expected 0 crystals, got %d", len(s.crystals))
	}
}

func TestMemoryCrystalsStrategy_AttentionResolutionDoesNotAdvanceCadence(t *testing.T) {
	client := llm.NewClient()
	spy := &fakeAdapter{name: "openai"}
	client.Register(spy)
	s := NewMemoryCrystalsStrategy(NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client)))
	marker := schema.NewTurn(schema.TurnAttentionResolution, llm.System("private marker"))
	marker.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "private", Disposition: "consumed"}
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hello")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("hi")),
		marker,
	}

	if err := s.AfterAction(context.Background(), history, client); err != nil {
		t.Fatalf("AfterAction: %v", err)
	}
	if got := len(spy.Requests()); got != 0 {
		t.Fatalf("private marker advanced crystallization cadence: requests=%d", got)
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
	cm := NewManager(profile, client, cheapmodel.New(client))
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
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil, cheapmodel.New(nil))
	s := NewMemoryCrystalsStrategy(cm)
	s.crystals = []MemoryCrystal{
		{Turn: 3, Action: "read_file", Facts: "Read auth.go, 200 lines"},
		{Turn: 6, Action: "edit_file", Facts: "Fixed nil check at line 42"},
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
	}

	var reported int
	var reportedCalled bool
	ctx := WithPostFoldInjectionCallback(context.Background(), func(n int) { reported = n; reportedCalled = true })
	s.injectCrystals(ctx, &history)

	// Should have 3 turns: original 2 + crystal steering.
	if len(history) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(history))
	}
	// No pre-existing crystal turn to remove: net +1 turn appended, and the
	// caller (compactionEmitFunc, issue #634) needs that reported so it can
	// add it back into the fold-removal delta it measures.
	if !reportedCalled || reported != 1 {
		t.Errorf("expected post-fold injection report of 1, got %d (called=%v)", reported, reportedCalled)
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
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil, cheapmodel.New(nil))
	s := NewMemoryCrystalsStrategy(cm)
	s.crystals = []MemoryCrystal{
		{Turn: 3, Action: "test", Facts: "new crystal"},
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("task")),
		schema.NewTurn(schema.TurnSteering, llm.User("[MEMORY CRYSTALS]\nold crystal\n[END CRYSTALS]")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
	}

	var reportedCalled bool
	ctx := WithPostFoldInjectionCallback(context.Background(), func(int) { reportedCalled = true })
	s.injectCrystals(ctx, &history)

	// Steady state (one marker replaced by another): net turn-count delta is
	// 0, so nothing should be reported — reportPostFoldInjection no-ops on a
	// zero delta exactly like shrinkTurnHistoryBaseline no-ops on shrink<=0.
	if reportedCalled {
		t.Error("net-zero injection (old marker removed, new one added) must not report — nothing for the caller to correct")
	}

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

// TestMemoryCrystalsStrategy_InjectCrystals_MultipleMarkersBeforeBoundary
// pins per-removal boundary correction: the marker-removal loop must count
// every pre-boundary removal rather than record only the LAST matching
// marker's index, since a scenario with two crystal markers -- an earlier one
// before the N4 boundary and the LAST one sitting exactly at (or after) it --
// would otherwise see only the last one, find it >= boundary, and skip the
// correction entirely, even though the earlier removal also shifted every
// in-flight turn left by one. Markers are normally replaced, not accumulated
// (in practice at most one exists at a time), so this constructs the
// atypical case directly rather than trying to reach it through AfterAction/
// ManageContext's normal one-marker discipline.
//
// Ground truth (hand-derived): boundary=3, so the marker at index 1 is
// before it (removal shifts left) and the marker at index 3 is AT it
// (removal is boundary-neutral, per the same "at or after" semantics the
// single-marker case already uses). A last-match-only count (index 3 >=
// boundary) would report -1 (no correction applied to the -1 raw delta from
// removing 2 markers and adding 1) and the caller's shrink math would then
// place the boundary one position too far right; counting exactly the one
// pre-boundary removal reports 0.
func TestMemoryCrystalsStrategy_InjectCrystals_MultipleMarkersBeforeBoundary(t *testing.T) {
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil, cheapmodel.New(nil))
	s := NewMemoryCrystalsStrategy(cm)
	s.crystals = []MemoryCrystal{
		{Turn: 9, Action: "test", Facts: "new crystal"},
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("task")),                                            // index 0
		schema.NewTurn(schema.TurnSteering, llm.User("[MEMORY CRYSTALS]\nold crystal 1\n[END CRYSTALS]")), // index 1: before the boundary
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),                                    // index 2
		schema.NewTurn(schema.TurnSteering, llm.User("[MEMORY CRYSTALS]\nold crystal 2\n[END CRYSTALS]")), // index 3: AT the boundary
		schema.NewTurn(schema.TurnUserInput, llm.User("in-flight")),                                       // index 4
	}
	const boundary = 3

	var reported int
	var reportedCalled bool
	ctx := WithBaselineQuery(context.Background(), func() (int, bool) { return boundary, true })
	ctx = WithPostFoldInjectionCallback(ctx, func(n int) { reported = n; reportedCalled = true })
	s.injectCrystals(ctx, &history)

	// The correct correction is exactly 0 (raw delta -1 from removing 2
	// markers and adding 1, plus 1 for the single pre-boundary removal at
	// index 1), which reportPostFoldInjection's own n==0 guard turns into no
	// callback at all — the same no-op-on-net-zero semantics
	// InjectCrystals_RemovesOld already pins for the single-marker case. Old
	// (last-match-only) code computes -1 instead (no correction applied,
	// since the last match's index 3 is not < boundary 3) and WOULD have
	// fired the callback with that wrong value.
	if reportedCalled {
		t.Errorf("expected no post-fold injection report (net correction is exactly 0), got %d — old last-match-only code would report -1 here", reported)
	}

	// Ground truth: with only one net turn actually needing to move (two
	// markers removed, one appended, exactly one of the removals before the
	// boundary), the surviving in-flight turn must land exactly one position
	// left of its original index.
	idx := -1
	for i, t := range history {
		if t.Message.Text() == "in-flight" {
			idx = i
		}
	}
	if idx != 2 {
		t.Fatalf("in-flight turn landed at index %d, want 2 (original index 4, shifted left by the 2 removals ahead of it)", idx)
	}
}

func TestMemoryCrystalsStrategy_PruneOldCrystals(t *testing.T) {
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil, cheapmodel.New(nil))
	s := NewMemoryCrystalsStrategy(cm)

	// Add 25 crystals.
	for i := range 25 {
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

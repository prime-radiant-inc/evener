package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// recursiveDistillTurns builds n visible assistant turns.
func recursiveDistillTurns(n int) []schema.Turn {
	turns := make([]schema.Turn, n)
	for i := range turns {
		turns[i] = schema.NewTurn(schema.TurnAssistant, llm.Assistant("working on step"))
	}
	return turns
}

// recursiveDistillGrowTokens appends large assistant turns until history is over
// the checkpoint compaction threshold for a 500-token window.
func recursiveDistillGrowTokens(history []schema.Turn) []schema.Turn {
	estimator := NewManager(testProfile("openai", "gpt-5.2", 1_000_000), nil, cheapmodel.New(nil))
	for estimator.estimateTokens(history) < 425 {
		history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("analysis ", 50))))
	}
	return history
}

func TestRecursiveDistillStrategy_SatisfiesInterface(t *testing.T) {
	var _ Strategy = (*RecursiveDistillStrategy)(nil)
}

func TestRecursiveDistillStrategy_Name(t *testing.T) {
	s := &RecursiveDistillStrategy{}
	if s.Name() != "recursive-distill" {
		t.Errorf("expected name %q, got %q", "recursive-distill", s.Name())
	}
}

func TestRecursiveDistillStrategy_Tools_ReturnsNil(t *testing.T) {
	s := &RecursiveDistillStrategy{}
	if tools := s.Tools(); tools != nil {
		t.Errorf("expected nil tools, got %v", tools)
	}
}

func TestRecursiveDistillStrategy_AfterAction_NoMicroBelowThreshold(t *testing.T) {
	client := llm.NewClient()
	// Register a counting stub so that any LLM call is visible, not silently
	// swallowed by a missing-adapter error. The >= 10 action guard must prevent
	// any call when we are below the threshold.
	f := &fakeAdapter{name: "openai"}
	client.Register(f)

	profile := NewOpenAIProfile("gpt-5.2")
	cm := NewManager(profile, client, cheapmodel.New(client))
	s := NewRecursiveDistillStrategy(cm)

	// Nine completed actions — one short of the ten-action micro cadence.
	history := recursiveDistillTurns(3)
	for i := range 9 {
		if err := s.AfterAction(context.Background(), history, client); err != nil {
			t.Fatalf("AfterAction %d: %v", i, err)
		}
	}
	// The >= 10 guard must not have triggered: no LLM call and no micro-summary.
	if got := len(f.Requests()); got != 0 {
		t.Errorf("expected 0 LLM calls below threshold, got %d", got)
	}
	if len(s.microSummaries) != 0 {
		t.Errorf("expected 0 micro-summaries, got %d", len(s.microSummaries))
	}
}

func TestRecursiveDistillStrategy_AttentionResolutionDoesNotAdvanceCadence(t *testing.T) {
	client := llm.NewClient()
	spy := &fakeAdapter{name: "openai"}
	client.Register(spy)
	s := NewRecursiveDistillStrategy(NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client)))
	history := []schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Assistant("visible action"))}
	marker := schema.NewTurn(schema.TurnAttentionResolution, llm.System("private marker"))
	marker.AttentionResolution = &schema.AttentionResolutionInfo{AttentionID: "private", Disposition: "consumed"}
	history = append(history, marker)

	// Nine actions carrying a hidden marker each: the markers are stripped and
	// must not advance the cadence to the ten-action boundary.
	for i := range 9 {
		if err := s.AfterAction(context.Background(), history, client); err != nil {
			t.Fatalf("AfterAction %d: %v", i, err)
		}
	}
	if got := len(spy.Requests()); got != 0 {
		t.Fatalf("private marker advanced distillation cadence: requests=%d", got)
	}
	if err := s.AfterAction(context.Background(), history, client); err != nil {
		t.Fatalf("AfterAction: %v", err)
	}
	if got := len(spy.Requests()); got != 1 {
		t.Fatalf("tenth action did not distill: requests=%d", got)
	}
}

func TestRecursiveDistillStrategy_AfterAction_MicroAt10Actions(t *testing.T) {
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
	cm := NewManager(profile, client, cheapmodel.New(client))
	s := NewRecursiveDistillStrategy(cm)

	// Ten completed actions — should trigger one micro-summary at the tenth.
	history := recursiveDistillTurns(10)
	for i := range 10 {
		if err := s.AfterAction(context.Background(), history, client); err != nil {
			t.Fatalf("AfterAction %d: %v", i, err)
		}
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
	if s.actions != 10 {
		t.Errorf("expected actions=10, got %d", s.actions)
	}
}

func TestRecursiveDistillStrategy_AfterAction_MacroAt50Actions(t *testing.T) {
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
	cm := NewManager(profile, client, cheapmodel.New(client))
	s := NewRecursiveDistillStrategy(cm)

	// Pre-populate with 4 micro-summaries (simulating actions 10-40).
	s.microSummaries = []string{
		"Read files and understood the bug.",
		"Tried a fix that didn't work.",
		"Found the root cause in parser.go.",
		"Applied fix and ran linter.",
	}
	s.lastMicroAt = 40
	s.actions = 49

	// The 50th completed action should trigger both micro (5th one) AND macro.
	history := recursiveDistillTurns(50)

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
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil, cheapmodel.New(nil))
	s := NewRecursiveDistillStrategy(cm)
	s.macroSummaries = []string{"Phase 1: investigated and found root cause."}
	s.microSummaries = []string{"Applied fix to auth.go.", "Running tests."}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
	}

	var reported int
	var reportedCalled bool
	ctx := WithPostFoldInjectionCallback(context.Background(), func(n int) { reported = n; reportedCalled = true })
	s.injectDistilledContext(ctx, &history)

	if len(history) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(history))
	}
	// No pre-existing distilled turn to remove: net +1 turn appended.
	if !reportedCalled || reported != 1 {
		t.Errorf("expected post-fold injection report of 1, got %d (called=%v)", reported, reportedCalled)
	}

	last := history[2]
	if last.Kind != schema.TurnSteering {
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
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil, cheapmodel.New(nil))
	s := NewRecursiveDistillStrategy(cm)
	s.microSummaries = []string{"new summary"}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("task")),
		schema.NewTurn(schema.TurnSteering, llm.User("[DISTILLED MEMORY]\nold stuff\n[END DISTILLED MEMORY]")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
	}

	var reportedCalled bool
	ctx := WithPostFoldInjectionCallback(context.Background(), func(int) { reportedCalled = true })
	s.injectDistilledContext(ctx, &history)

	// Steady state (one marker replaced by another): net delta 0, nothing to report.
	if reportedCalled {
		t.Error("net-zero injection (old marker removed, new one added) must not report")
	}

	distillCount := 0
	for _, t := range history {
		if t.Kind == schema.TurnSteering && strings.Contains(t.Message.Text(), "[DISTILLED MEMORY]") {
			distillCount++
		}
	}
	if distillCount != 1 {
		t.Errorf("expected exactly 1 distilled memory turn, got %d", distillCount)
	}
}

// TestRecursiveDistillStrategy_CompactionShrinkDoesNotSuppressCadence pins the
// CORE-09 invariant: the distillation budget counts completed actions, not the
// length of a history that compaction can shrink. A forced mid-stream compaction
// must not move the ten-action boundary.
func TestRecursiveDistillStrategy_CompactionShrinkDoesNotSuppressCadence(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	profile := testProfile("openai", "test", 500)
	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	s := NewRecursiveDistillStrategy(cm)
	ctx := context.Background()

	// Nine completed actions — no micro yet.
	history := recursiveDistillTurns(9)
	for i := range 9 {
		if err := s.AfterAction(ctx, history, client); err != nil {
			t.Fatalf("AfterAction %d: %v", i, err)
		}
	}
	if len(s.microSummaries) != 0 {
		t.Fatalf("micro fired before the ten-action boundary: %d", len(s.microSummaries))
	}

	// Force a compaction mid-stream, then run the tenth completed action: the
	// shrink must not suppress (or advance) the action cadence.
	history = recursiveDistillGrowTokens(history)
	grown := len(history)
	s.ManageContext(ctx, &history, 0, noopEmit)
	if len(history) >= grown {
		t.Fatalf("compaction did not shrink history: %d -> %d", grown, len(history))
	}
	if err := s.AfterAction(ctx, history, client); err != nil {
		t.Fatalf("AfterAction after compaction: %v", err)
	}
	if got := len(s.microSummaries); got != 1 {
		t.Fatalf("post-compaction micro-summaries: got %d, want 1 (a shrink must not suppress the cadence)", got)
	}
	if s.actions != 10 {
		t.Fatalf("actions=%d, want 10", s.actions)
	}
}

// TestRecursiveDistillStrategy_RefusedFoldDoesNotAdvanceCadence pins that a fold
// publish the session refuses cannot advance the distillation cadence. Under the
// old history-length clock, ManageContext re-anchored its baseline to the
// compacted candidate, so the next AfterAction against the unchanged live
// history jumped the clock by the whole compaction delta and distilled early.
func TestRecursiveDistillStrategy_RefusedFoldDoesNotAdvanceCadence(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	profile := testProfile("openai", "test", 500)
	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	s := NewRecursiveDistillStrategy(cm)
	ctx := context.Background()

	// Nine completed actions over an unchanged live history — no micro yet.
	live := recursiveDistillTurns(9)
	for i := range 9 {
		if err := s.AfterAction(ctx, live, client); err != nil {
			t.Fatalf("AfterAction %d: %v", i, err)
		}
	}
	if len(s.microSummaries) != 0 {
		t.Fatalf("nine actions distilled: %d", len(s.microSummaries))
	}

	// Three rounds where ManageContext is handed a candidate that compaction
	// shrinks, but whose publish the session refuses, so the live history is
	// unchanged. The cadence advances one action per round, so exactly the tenth
	// completed action distills.
	for round := range 3 {
		candidate := recursiveDistillGrowTokens(recursiveDistillTurns(1))
		grown := len(candidate)
		if err := s.ManageContext(ctx, &candidate, 0, noopEmit); err != nil {
			t.Fatalf("ManageContext %d: %v", round, err)
		}
		if len(candidate) >= grown {
			t.Fatalf("round %d: candidate was not compacted (%d -> %d)", round, grown, len(candidate))
		}
		if err := s.AfterAction(ctx, live, client); err != nil {
			t.Fatalf("AfterAction %d: %v", round, err)
		}
	}
	if got := len(s.microSummaries); got != 1 {
		t.Fatalf("refused fold advanced the cadence: micros=%d, want 1", got)
	}
	if s.actions != 12 {
		t.Fatalf("actions=%d, want 12", s.actions)
	}
}

// TestRecursiveDistillStrategy_InjectionOnlyRoundsDoNotAdvanceCadence pins that
// the distilled-context injection writes to history but never to the action
// clock. Under the old turn-clock baseline, an injection-only round left a stale
// baseline and the next AfterAction counted the injected turn, firing early.
func TestRecursiveDistillStrategy_InjectionOnlyRoundsDoNotAdvanceCadence(t *testing.T) {
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))
	s := NewRecursiveDistillStrategy(cm)
	// A seeded macro-summary makes ManageContext inject the distilled banner on
	// an unshrunken history.
	s.macroSummaries = []string{"prior overview"}
	ctx := context.Background()
	history := recursiveDistillTurns(9)

	// Five injection-only rounds: ManageContext injects the banner but no
	// compaction runs. Each round is one completed action, so five actions must
	// not cross the ten-action boundary.
	for round := range 5 {
		if err := s.ManageContext(ctx, &history, 0, noopEmit); err != nil {
			t.Fatalf("ManageContext %d: %v", round, err)
		}
		if err := s.AfterAction(ctx, history, client); err != nil {
			t.Fatalf("AfterAction %d: %v", round, err)
		}
	}
	if got := len(s.microSummaries); got != 0 {
		t.Fatalf("injection-only rounds advanced the cadence: micros=%d after 5 actions", got)
	}

	// The cadence still fires at the ten-action boundary.
	for round := range 5 {
		if err := s.ManageContext(ctx, &history, 0, noopEmit); err != nil {
			t.Fatalf("ManageContext %d: %v", round, err)
		}
		if err := s.AfterAction(ctx, history, client); err != nil {
			t.Fatalf("AfterAction %d: %v", round, err)
		}
	}
	if got := len(s.microSummaries); got != 1 {
		t.Fatalf("ten actions did not distill: micros=%d, want 1", got)
	}
}

// TestRecursiveDistillStrategy_MicroSummaryCoversCadenceSpan pins that a
// micro-summary distills every turn across the ten-action cadence span. A
// completed action appends at least an assistant turn and a tool-results turn,
// so a fixed last-ten-turn window would drop the earlier half of each period
// once compaction removes it — a turn from the first half must appear in the
// summary prompt.
func TestRecursiveDistillStrategy_MicroSummaryCoversCadenceSpan(t *testing.T) {
	client := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	client.Register(f)

	cm := NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client))
	s := NewRecursiveDistillStrategy(cm)
	ctx := context.Background()

	history := make([]schema.Turn, 0, 24)
	for action := 1; action <= 10; action++ {
		mark := fmt.Sprintf("ACTION-MARK-%02d", action)
		history = append(history,
			schema.NewTurn(schema.TurnAssistant, llm.Assistant(mark+" assistant")),
			schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(fmt.Sprintf("t%d", action), "shell", mark+" result", false)),
		)
		if err := s.AfterAction(ctx, history, client); err != nil {
			t.Fatalf("AfterAction %d: %v", action, err)
		}
	}
	if got := len(s.microSummaries); got != 1 {
		t.Fatalf("micro-summaries = %d, want 1", got)
	}

	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("micro-summary requests = %d, want 1", len(reqs))
	}
	prompt := reqs[0].Messages[0].Text()
	// The span is twenty turns; the earlier half must survive into the prompt.
	for _, want := range []string{"ACTION-MARK-01", "ACTION-MARK-05", "ACTION-MARK-10"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("micro-summary prompt dropped %q from the cadence span:\n%s", want, prompt)
		}
	}
}

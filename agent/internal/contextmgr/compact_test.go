package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// makeBigHistory creates a history where EstimateTokens returns approximately targetTokens.
func makeBigHistory(targetTokens int) []schema.Turn {
	cm := NewManager(testProfile("openai", "gpt-5.2", 1_000_000), nil, cheapmodel.New(nil))
	turns := []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")}}
	for cm.estimateTokens(turns) < targetTokens {
		id := fmt.Sprintf("c%d", len(turns))
		turns = append(turns,
			schema.Turn{Kind: schema.TurnAssistant, Message: assistantWithToolCall(id, "read_file", `{"file_path":"file.go"}`)},
			schema.Turn{Kind: schema.TurnTool, Message: llm.ToolResultNamed(id, "read_file", strings.Repeat("x", 400), false)},
		)
	}
	return turns
}

func TestMaybeCompact_NoCompactionBelow80Percent(t *testing.T) {
	// At 70% pressure, no compaction should fire. Compaction starts at 80% (checkpoint).
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil, cheapmodel.New(nil))
	cm.PreserveRecentTurns = 2

	// Create history filling ~70% of 1000 tokens = 700 tokens via tool results.
	history := makeBigHistory(700)
	history = append(history,
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	for _, e := range evs {
		if e.Kind == events.EventContextCompaction {
			t.Fatalf("no compaction should fire at 70%% pressure, got event: %+v", e.Data)
		}
	}
}

func TestMaybeCompact_BelowThreshold_NoAction(t *testing.T) {
	cm := NewManager(NewOpenAIProfile("gpt-5.2"), nil, cheapmodel.New(nil))

	// Small history, well below any threshold.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("ok")},
	}

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 100, emitFn)
	// No compaction events should have been emitted.
	for _, e := range evs {
		if e.Kind == events.EventContextCompaction {
			t.Fatalf("unexpected CONTEXT_COMPACTION event below threshold")
		}
	}
	// History should be unchanged.
	if len(history) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(history))
	}
}

func TestMaybeCompact_CheckpointThreshold(t *testing.T) {
	profile := testProfile("openai", "test", 500)
	cm := NewManager(profile, nil, cheapmodel.New(nil))
	cm.PreserveRecentTurns = 2

	// Each assistant turn ~400 chars = 100 tokens. Need 85% of 500 = 425 tokens.
	history := []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User("Fix the auth bug")}}
	for testEstimateTokens(t, history) < 425 {
		history = append(history,
			schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("analysis ", 50))},
		)
	}
	history = append(history,
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// At 85%, checkpoint should trigger (threshold is 80%).
	foundCheckpoint := false
	for _, e := range evs {
		if d, ok := e.Data.(events.ContextCompactionData); ok && d.Layer == "checkpoint" {
			foundCheckpoint = true
		}
	}
	if !foundCheckpoint {
		t.Fatalf("expected checkpoint compaction event; got events: %+v", evs)
	}

	// History should have been replaced with checkpoint + recent.
	if len(history) > 5 {
		t.Fatalf("expected compacted history, got %d turns", len(history))
	}
}

func TestMaybeCompact_EmitsEvents(t *testing.T) {
	profile := testProfile("openai", "test", 500)
	cm := NewManager(profile, nil, cheapmodel.New(nil))
	cm.PreserveRecentTurns = 2

	// Fill ~85% = 425 tokens (above 80% checkpoint threshold).
	history := makeBigHistory(425)
	history = append(history,
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// Should have at least one compaction event.
	compactionCount := 0
	for _, e := range evs {
		if d, ok := e.Data.(events.ContextCompactionData); ok {
			compactionCount++
			// Each event should carry a layer and token counts.
			if d.Layer == "" {
				t.Fatalf("compaction event missing 'layer': %+v", e.Data)
			}
			if d.EstTokensBefore == 0 {
				t.Fatalf("compaction event missing 'est_tokens_before': %+v", e.Data)
			}
		}
	}
	if compactionCount == 0 {
		t.Fatalf("expected compaction events")
	}
}

func TestMaybeCompact_RespectsSysPromptSize(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil, cheapmodel.New(nil))
	cm.PreserveRecentTurns = 2

	// Small history, but giant system prompt.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: assistantWithToolCall("c1", "read_file", `{"file_path":"a.go"}`)},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", strings.Repeat("x", 400), false)},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	}

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	// sys prompt is 2800 chars ≈ 700 tokens + history ~100 tokens = 800/1000 = 80%
	cm.MaybeCompact(context.Background(), &history, 2800, emitFn)

	// With sys prompt, we're at ~80%, should trigger checkpoint.
	foundCheckpoint := false
	for _, e := range evs {
		if d, ok := e.Data.(events.ContextCompactionData); ok && d.Layer == "checkpoint" {
			foundCheckpoint = true
		}
	}
	if !foundCheckpoint {
		t.Fatalf("expected checkpoint compaction event when sys prompt pushes over threshold; got events: %+v", evs)
	}
}

func TestContextManager_ResetsAfterCompaction(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil, cheapmodel.New(nil))
	cm.PreserveRecentTurns = 2

	// Record high token count.
	cm.RecordInputTokens(700, 5)

	// After compaction modifies history, the measurement should reset.
	// Build a history that's big enough to trigger observation masking.
	history := makeBigHistory(650)
	history = append(history,
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent1")},
		schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("recent2")},
	)

	var evs []events.SessionEvent
	emitFn := func(kind events.EventKind, data events.EventData) {
		evs = append(evs, events.SessionEvent{Kind: kind, Data: data})
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	// After compaction, lastInputTokens should be reset.
	cm.mu.Lock()
	lit := cm.lastInputTokens
	cm.mu.Unlock()
	if lit != 0 {
		t.Fatalf("lastInputTokens should be reset after compaction, got %d", lit)
	}
}

// TestMaybeCompact_SummarizeThreshold verifies summarize fires through the orchestrator.
func TestMaybeCompact_SummarizeThreshold(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("## Progress\nSummary: tests were run and passed")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	// The smallest window compaction summarizes on. Checkpoint replaces old
	// history with a checkpoint message, but the preserved recent turns are
	// large enough to keep pressure above 90% after checkpoint, forcing
	// summarize.
	profile := testProfile("openai", "test", minSummarizingWindow)
	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 1

	// After checkpoint, result = [checkpoint_msg, recent_turn].
	// The recent turn alone needs to be ~90% of 50,000 tokens = ~45,000
	// tokens = ~180,000 chars; this is a little over.
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User(strings.Repeat("task ", 50))},
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("work ", 30))},
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("recent content ", 14_000))},
	}

	var layers []string
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if cd, ok := data.(events.ContextCompactionData); ok {
				layers = append(layers, cd.Layer)
			}
		}
	}

	cm.MaybeCompact(context.Background(), &history, 0, emitFn)

	foundSummarize := false
	for _, l := range layers {
		if l == "summarize" {
			foundSummarize = true
		}
	}
	if !foundSummarize {
		t.Fatalf("expected summarize layer; got %v", layers)
	}

	// First turn should be the LLM summary.
	text := history[0].Message.Text()
	if !strings.Contains(text, "[CONTEXT SUMMARY]") {
		t.Fatalf("expected summary in first turn, got: %q", text)
	}
}

func TestForceCompact_RunsAllLayers(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("## Progress\n[CONTEXT SUMMARY] forced")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	profile := testProfile("openai", "test", 100_000)
	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working on it")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	var layers []string
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if d, ok := data.(events.ContextCompactionData); ok {
				layers = append(layers, d.Layer)
			}
		}
	}

	cm.ForceCompact(context.Background(), &history, "", emitFn)

	// Both layers should fire: checkpoint then summarize.
	if len(layers) != 2 {
		t.Fatalf("expected 2 layers to fire, got %d: %v", len(layers), layers)
	}
	expected := []string{"checkpoint", "summarize"}
	for i, want := range expected {
		if layers[i] != want {
			t.Errorf("layer[%d] = %q, want %q (all: %v)", i, layers[i], want, layers)
		}
	}
}

func TestForceCompact_FiresOnCompactionTurn_Checkpoint(t *testing.T) {
	// ForceCompact should fire OnCompactionTurn for TurnCheckpoint (L3).
	profile := testProfile("openai", "test", 100_000)
	cm := NewManager(profile, nil, cheapmodel.New(nil)) // no LLM client → L4 skipped
	cm.PreserveRecentTurns = 2

	var callbackTurns []schema.TurnKind
	cm.OnCompactionTurn = func(t schema.Turn) {
		callbackTurns = append(callbackTurns, t.Kind)
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("analysis done")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}
	cm.ForceCompact(context.Background(), &history, "", emitFn)

	// L3 creates a checkpoint turn. OnCompactionTurn should have been called.
	found := false
	for _, k := range callbackTurns {
		if k == schema.TurnCheckpoint {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected OnCompactionTurn to be called with TurnCheckpoint; got %v", callbackTurns)
	}
}

func TestForceCompact_FiresOnCompactionTurn_Summary(t *testing.T) {
	// ForceCompact should fire OnCompactionTurn for TurnSummary (L4).
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("## Progress\n[CONTEXT SUMMARY] forced summary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	profile := testProfile("openai", "test", 100_000)
	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2

	var callbackTurns []schema.TurnKind
	cm.OnCompactionTurn = func(t schema.Turn) {
		callbackTurns = append(callbackTurns, t.Kind)
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("fix the bug")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I'll fix it")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("analysis done")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	emitFn := func(kind events.EventKind, data events.EventData) {}
	cm.ForceCompact(context.Background(), &history, "", emitFn)

	// Both L3 (checkpoint) and L4 (summary) should fire callbacks.
	foundCheckpoint, foundSummary := false, false
	for _, k := range callbackTurns {
		if k == schema.TurnCheckpoint {
			foundCheckpoint = true
		}
		if k == schema.TurnSummary {
			foundSummary = true
		}
	}
	if !foundCheckpoint {
		t.Fatalf("expected OnCompactionTurn for TurnCheckpoint; got %v", callbackTurns)
	}
	if !foundSummary {
		t.Fatalf("expected OnCompactionTurn for TurnSummary; got %v", callbackTurns)
	}
}

func TestForceCompact_BelowThreshold(t *testing.T) {
	// Even with tiny history well below auto-compact thresholds,
	// ForceCompact should still run the layers.
	profile := testProfile("openai", "test", 1_000_000)
	cm := NewManager(profile, nil, cheapmodel.New(nil)) // no LLM client → summarize skipped
	cm.PreserveRecentTurns = 2

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hi")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working on it")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	var layers []string
	emitFn := func(kind events.EventKind, data events.EventData) {
		if kind == events.EventContextCompaction {
			if d, ok := data.(events.ContextCompactionData); ok {
				layers = append(layers, d.Layer)
			}
		}
	}

	cm.ForceCompact(context.Background(), &history, "", emitFn)

	// Checkpoint should fire. Summarize skipped (no client).
	if len(layers) != 1 || layers[0] != "checkpoint" {
		t.Fatalf("expected [checkpoint], got %v", layers)
	}
}

func TestForceCompact_ReportsSummarized(t *testing.T) {
	// With a nil client there is no summary layer, so ForceCompact should
	// return false regardless of history length.
	profile := testProfile("openai", "test", 100_000)
	cm := NewManager(profile, nil, cheapmodel.New(nil)) // nil client → no summary layer
	cm.PreserveRecentTurns = 2

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("question one")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer one")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
	}
	emitFn := func(events.EventKind, events.EventData) {}
	summarized := cm.ForceCompact(context.Background(), &history, "", emitFn)
	if summarized {
		t.Fatal("no client and short history → no summary; should report false")
	}
}

func TestForceCompact_ReportsGeneratedSummary(t *testing.T) {
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant("## Progress\nsummary")}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	profile := testProfile("openai", "test", 100_000)

	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working on it")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent one")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent two")),
	}
	if !cm.ForceCompact(context.Background(), &history, "", func(events.EventKind, events.EventData) {}) {
		t.Fatal("ForceCompact returned false after generating a summary")
	}

	// A no-op must not be mistaken for a generated summary merely because the
	// short input already begins with a summary turn.
	short := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nprior\n[END SUMMARY]")),
		{Kind: schema.TurnAttentionResolution, Message: llm.System("private marker"), AttentionResolution: &schema.AttentionResolutionInfo{AttentionID: "private", Disposition: "consumed"}},
		schema.NewTurn(schema.TurnUserInput, llm.User("recent")),
	}
	shortCM := NewManager(profile, client, cheapmodel.New(client))
	shortCM.PreserveRecentTurns = 2
	if shortCM.ForceCompact(context.Background(), &short, "", func(events.EventKind, events.EventData) {}) {
		t.Fatal("ForceCompact reported a pre-existing short summary as newly generated")
	}
}

// TestUnknownWindowCompactsUnderAssumedDefault pins issue #4564's rule for a
// row whose window the registry does not know: 0 still means unknown, never
// tiny — but unknown now budgets against a conservative assumed window rather
// than disabling management, so pressure and the metrics read against the
// assumption, MaybeCompact folds a history that has outgrown it, and the
// assumption is disclosed as exactly one warning per manager, not per round.
// This test superseded the pre-#4564 pin that MaybeCompact never ran without
// a known window.
func TestUnknownWindowCompactsUnderAssumedDefault(t *testing.T) {
	t.Parallel()
	cm := NewManager(unknownGatewayProfile(), nil, cheapmodel.New(nil))

	// Ten turns at ~15K tokens each: ~150K tokens, past the checkpoint
	// threshold against the 128K assumption. The summarize layer stays out —
	// no client.
	history := bulkHistory(10, 10_000)
	before := len(history)

	if got := cm.Pressure(history, 0); got < cm.CheckpointThreshold {
		t.Fatalf("Pressure = %v, want ≥ %v against the assumed window", got, cm.CheckpointThreshold)
	}
	metrics := cm.EstimateUsage(history, 0)
	if metrics.Window != AssumedContextWindow {
		t.Fatalf("EstimateUsage window = %d, want the assumed %d", metrics.Window, AssumedContextWindow)
	}
	if metrics.Used <= 0 {
		t.Fatalf("EstimateUsage used = %d, want the estimated bulk", metrics.Used)
	}

	spy := &emitSpy{}
	cm.MaybeCompact(context.Background(), &history, 0, spy.fn)
	if len(history) >= before {
		t.Fatalf("MaybeCompact left %d turns (had %d): unknown window folded nothing", len(history), before)
	}
	if len(spy.compaction) == 0 || history[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("no checkpoint fold: %d turns, %d compaction events", len(history), len(spy.compaction))
	}
	if n := spy.disclosures(); n != 1 {
		t.Fatalf("assumed-window disclosures = %d, want exactly 1", n)
	}

	// The disclosure is once per manager: a second pass under the same unknown
	// window stays quiet about the assumption.
	spy2 := &emitSpy{}
	cm.MaybeCompact(context.Background(), &history, 0, spy2.fn)
	if n := spy2.disclosures(); n != 0 {
		t.Fatalf("second pass disclosed the assumption %d times, want 0", n)
	}
}

package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
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

func TestMaybeCompact_NoCompactionBelowTrigger(t *testing.T) {
	// At 70% pressure, no compaction should fire. Compaction starts at 90% (checkpoint).
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

	// sys prompt is 3200 chars ≈ 800 tokens + history ~100 tokens = 900/1000 = 90%
	cm.MaybeCompact(context.Background(), &history, 3200, emitFn)

	// With sys prompt, we are at ~90%, should trigger checkpoint.
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
	// Build a history that's big enough to trigger compaction.
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
	adapter := newFakeAdapter("openai", []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("## Progress\nSummary: tests were run and passed")}
		},
	})
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
	adapter := newFakeAdapter("openai", []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("## Progress\n[CONTEXT SUMMARY] forced")}
		},
	})
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
	adapter := newFakeAdapter("openai", []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("## Progress\n[CONTEXT SUMMARY] forced summary")}
		},
	})
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
	adapter := newFakeAdapter("openai", []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("## Progress\nsummary")}
		},
	})
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

func withCompactionThreshold(p *provider.Profile, threshold int) *provider.Profile {
	res := p.Resolved()
	if threshold > 0 {
		res.Caps.CompactionThreshold = &threshold
	} else {
		res.Caps.CompactionThreshold = nil
	}
	return p.WithResolved(res)
}

func TestMaybeCompact_CompactionThresholdLayersAndStrategies(t *testing.T) {
	t.Parallel()
	prof := withCompactionThreshold(testProfile("openai", "test", 1_000_000), 250_000)
	cm := NewManager(prof, nil, cheapmodel.New(nil))
	cm.PreserveRecentTurns = 2

	th := cm.thresholdsFor(prof)
	if got := cm.WarnThresholdFor(prof); got != 0.20 || th.warn != 0.20 {
		t.Fatalf("WarnThresholdFor = %v (th.warn=%v), want 0.20", got, th.warn)
	}
	if got := cm.CheckpointThresholdFor(prof); got != 0.25 || th.checkpoint != 0.25 {
		t.Fatalf("CheckpointThresholdFor = %v (th.checkpoint=%v), want 0.25", got, th.checkpoint)
	}
	wantSummarize := 0.25 * (summarizeBackstopAt / DefaultCompactAt)
	if th.summarize != wantSummarize {
		t.Fatalf("th.summarize = %v, want %v", th.summarize, wantSummarize)
	}
	if 263_888.0/1_000_000.0 >= th.summarize || 263_889.0/1_000_000.0 < th.summarize {
		t.Fatalf("summarize threshold %v should sit between 263888 and 263889 tokens on 1M window", th.summarize)
	}

	for _, tc := range []struct {
		name           string
		wantCheckpoint string
		run            func(t *testing.T, cm *Manager, history *[]schema.Turn, emitFn func(events.EventKind, events.EventData))
	}{
		{
			name:           "MaybeCompact",
			wantCheckpoint: "checkpoint",
			run: func(_ *testing.T, cm *Manager, history *[]schema.Turn, emitFn func(events.EventKind, events.EventData)) {
				cm.MaybeCompact(context.Background(), history, 0, emitFn)
			},
		},
		{
			name:           "SessionLogStrategy",
			wantCheckpoint: "session_log_checkpoint",
			run: func(t *testing.T, cm *Manager, history *[]schema.Turn, emitFn func(events.EventKind, events.EventData)) {
				s, _ := newTestSessionLogStrategy(t, cm)
				if err := s.ManageContext(context.Background(), history, 0, emitFn); err != nil {
					t.Fatalf("SessionLogStrategy.ManageContext: %v", err)
				}
			},
		},
		{
			name:           "OODAStrategy",
			wantCheckpoint: "session_log_checkpoint",
			run: func(t *testing.T, cm *Manager, history *[]schema.Turn, emitFn func(events.EventKind, events.EventData)) {
				sls, _ := newTestSessionLogStrategy(t, cm)
				s := &OODAStrategy{SessionLogStrategy: sls}
				if err := s.ManageContext(context.Background(), history, 0, emitFn); err != nil {
					t.Fatalf("OODAStrategy.ManageContext: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := llm.NewClient()
			client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
				return llm.Response{Message: llm.Assistant("## Progress\n[CONTEXT SUMMARY] condensed")}, nil
			}})
			mgr := NewManager(prof, client, cheapmodel.New(client))
			mgr.PreserveRecentTurns = 1

			// At 240,000 tokens (below 250,000), no fold occurs.
			hist240 := bulkHistory(6, 100)
			mgr.RecordInputTokens(240_000, len(hist240))
			spy240 := &emitSpy{}
			tc.run(t, mgr, &hist240, spy240.fn)
			if len(spy240.compaction) != 0 || len(hist240) != 6 {
				t.Fatalf("at 240000 tokens: got %d compactions and %d turns, want 0 and 6", len(spy240.compaction), len(hist240))
			}

			// At 250,000 tokens with a small preserved tail, Layer 1 checkpoint fires without Layer 2 summarize.
			hist250 := bulkHistory(6, 100)
			mgr.RecordInputTokens(250_000, len(hist250))
			spy250 := &emitSpy{}
			tc.run(t, mgr, &hist250, spy250.fn)
			if len(spy250.compaction) != 1 || spy250.compaction[0].Layer != tc.wantCheckpoint {
				t.Fatalf("at 250000 tokens: compactions = %+v, want [%s]", spy250.compaction, tc.wantCheckpoint)
			}

			// When post-checkpoint pressure remains >= 263,889 tokens, Layer 2 summarize fires.
			bigTail := strings.Repeat("x", 264_000*4)
			histSummarize := []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("initial task")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("step one")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant(bigTail)),
			}
			spySum := &emitSpy{}
			tc.run(t, mgr, &histSummarize, spySum.fn)
			if len(spySum.compaction) != 2 || spySum.compaction[0].Layer != tc.wantCheckpoint || spySum.compaction[1].Layer != "summarize" {
				t.Fatalf("above summarize backstop: compactions = %+v, want [%s summarize]", spySum.compaction, tc.wantCheckpoint)
			}
		})
	}
}

func TestMaybeCompact_CompactionThresholdSmallAndAboveDefault(t *testing.T) {
	t.Parallel()
	// Small threshold edge case: 4096 on 1,048,576.
	smallProf := withCompactionThreshold(testProfile("openai", "test", 1_048_576), 4096)
	smallCM := NewManager(smallProf, nil, cheapmodel.New(nil))
	smallTh := smallCM.thresholdsFor(smallProf)
	if !(0 < smallTh.warn && smallTh.warn < smallTh.checkpoint && smallTh.checkpoint <= smallTh.summarize && smallTh.summarize < 1.0) {
		t.Fatalf("small threshold ordering broken: %+v", smallTh)
	}
	if p0 := smallCM.Pressure(nil, 0); p0 >= smallCM.WarnThresholdFor(smallProf) {
		t.Fatalf("zero-token pressure %v >= WarnThresholdFor %v", p0, smallCM.WarnThresholdFor(smallProf))
	}

	// Threshold above DefaultCompactAt: 920,000 on 1,000,000 with no explicitCompactAt.
	highProf := withCompactionThreshold(testProfile("openai", "test", 1_000_000), 920_000)
	highCM := NewManager(highProf, nil, cheapmodel.New(nil))
	highCM.PreserveRecentTurns = 2
	highTh := highCM.thresholdsFor(highProf)
	if highTh.checkpoint != 0.92 || highTh.summarize != 0.95 {
		t.Fatalf("920k threshold gates = %+v, want checkpoint=0.92 summarize=0.95", highTh)
	}
	if usage := highCM.EstimateUsage(nil, 0); usage.CompactionThreshold != 920_000 {
		t.Fatalf("EstimateUsage.CompactionThreshold = %d, want 920000", usage.CompactionThreshold)
	}

	hist910 := bulkHistory(6, 100)
	highCM.RecordInputTokens(910_000, len(hist910))
	spy910 := &emitSpy{}
	highCM.MaybeCompact(context.Background(), &hist910, 0, spy910.fn)
	if len(spy910.compaction) != 0 {
		t.Fatalf("at 910000 tokens (below 920000): got %d compactions, want 0", len(spy910.compaction))
	}

	hist920 := bulkHistory(6, 100)
	highCM.RecordInputTokens(920_000, len(hist920))
	spy920 := &emitSpy{}
	highCM.MaybeCompact(context.Background(), &hist920, 0, spy920.fn)
	if len(spy920.compaction) != 1 || spy920.compaction[0].Layer != "checkpoint" {
		t.Fatalf("at 920000 tokens: compactions = %+v, want [checkpoint]", spy920.compaction)
	}

	// Explicit session CompactAtPercent = 20 (0.20 = 200,000) is tighter than 920,000 and wins.
	highCM.SetExplicitCompactAt(0.20)
	if got := highCM.CheckpointThresholdFor(highProf); got != 0.20 {
		t.Fatalf("with SetExplicitCompactAt(0.20), CheckpointThresholdFor = %v, want 0.20", got)
	}
	if usage := highCM.EstimateUsage(nil, 0); usage.CompactionThreshold != 200_000 {
		t.Fatalf("with SetExplicitCompactAt(0.20), EstimateUsage.CompactionThreshold = %d, want 200000", usage.CompactionThreshold)
	}

	// ApplyThresholdScale scales thresholdsFor(prof) with floor 0.20.
	scaledCM := NewManager(highProf, nil, cheapmodel.New(nil))
	ApplyThresholdScale(scaledCM, 0.5)
	if got := scaledCM.CheckpointThresholdFor(highProf); !approxFloat(got, 0.46) {
		t.Fatalf("scaled CheckpointThresholdFor = %v, want 0.46", got)
	}
}

func approxFloat(a, b float64) bool {
	return a-b < 1e-9 && b-a < 1e-9
}

func TestMaybeCompact_CompactionThresholdUnknownWindow(t *testing.T) {
	t.Parallel()
	prof := withCompactionThreshold(unknownGatewayProfile(), 250_000)
	window, assumed := EffectiveWindow(prof)
	if window != 277_778 || !assumed {
		t.Fatalf("EffectiveWindow = (%d, %v), want (277778, true)", window, assumed)
	}

	cm := NewManager(prof, nil, cheapmodel.New(nil))
	cm.PreserveRecentTurns = 2

	if usage := cm.EstimateUsage(nil, 0); usage.Window != 277_778 || !usage.Assumed || usage.CompactionThreshold != 250_000 {
		t.Fatalf("EstimateUsage = %+v, want Window=277778 Assumed=true CompactionThreshold=250000", usage)
	}

	// At 210,000 tokens (above UnknownWindowInputTripwire=200,000, below 250,000):
	// neither the tripwire nor the assumed-window disclosure fires.
	hist210 := bulkHistory(6, 100)
	cm.RecordInputTokens(210_000, len(hist210))
	spy210 := &emitSpy{}
	cm.MaybeCompact(context.Background(), &hist210, 0, spy210.fn)
	if len(spy210.compaction) != 0 {
		t.Fatalf("at 210000 tokens: got %d compactions, want 0", len(spy210.compaction))
	}
	if n := spy210.disclosures(); n != 0 {
		t.Fatalf("at 210000 tokens: assumed-window disclosures = %d, want 0", n)
	}

	// At 250,000 tokens: checkpoint fires, still without WarningCodeContextWindowAssumed.
	hist250 := bulkHistory(6, 100)
	cm.RecordInputTokens(250_000, len(hist250))
	spy250 := &emitSpy{}
	cm.MaybeCompact(context.Background(), &hist250, 0, spy250.fn)
	if len(spy250.compaction) != 1 || spy250.compaction[0].Layer != "checkpoint" {
		t.Fatalf("at 250000 tokens: compactions = %+v, want [checkpoint]", spy250.compaction)
	}
	if n := spy250.disclosures(); n != 0 {
		t.Fatalf("at 250000 tokens: assumed-window disclosures = %d, want 0", n)
	}

	// Toggling CompactionThreshold on the same unknown-window target resets assumedWindowDisclosed.
	cm.mu.Lock()
	cm.assumedWindowDisclosed = true
	cm.mu.Unlock()
	cm.SetProfile(unknownGatewayProfile())
	histUnthresh := bulkHistory(10, 10_000)
	spyUnthresh := &emitSpy{}
	cm.MaybeCompact(context.Background(), &histUnthresh, 0, spyUnthresh.fn)
	if n := spyUnthresh.disclosures(); n != 1 {
		t.Fatalf("after clearing CompactionThreshold on same unknown-window target, disclosures = %d, want 1", n)
	}
}

func TestForceCompact_InstructionsSeeToolOutputs(t *testing.T) {
	t.Parallel()
	var promptSeen string
	adapter := newFakeAdapter("openai", []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			promptSeen = req.Messages[0].Text()
			return llm.Response{Message: llm.Assistant("## Progress\n[CONTEXT SUMMARY] preserved details")}
		},
	})
	client := llm.NewClient()
	client.Register(adapter)
	profile := testProfile("openai", "test", 100_000)
	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2

	const toolOutputSentinel = "SENTINEL_OUTPUT_TEST_FAIL_OR_DIFF_9871"
	var longOutput strings.Builder
	for i := range 25 {
		fmt.Fprintf(&longOutput, "=== RUN   TestSubsystem_ComponentStep%d\n    logger.go:42: verbose diagnostic output line %d with details\n", i, i)
	}
	fmt.Fprintf(&longOutput, "--- FAIL: TestSubsystem_ComponentStep24 (0.12s)\n    panic: %s\nFAIL\n", toolOutputSentinel)
	toolOutputText := longOutput.String()
	if len(toolOutputText) < 1000 {
		t.Fatalf("test output length %d < 1000 bytes", len(toolOutputText))
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("run the tests")),
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        "c1",
						Name:      "shell",
						Arguments: []byte(`{"command":"go test ./..."}`),
						Type:      "function",
					},
				},
			},
		}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("c1", "shell", toolOutputText, true)),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	cm.ForceCompact(context.Background(), &history, "preserve the test error output", func(events.EventKind, events.EventData) {})

	if !strings.Contains(promptSeen, toolOutputSentinel) {
		t.Fatalf("summarizer prompt did not receive tool output sentinel; prompt:\n%s", promptSeen)
	}
}

func TestForceCompact_InstructionsSummaryFailureKeepsCheckpoint(t *testing.T) {
	t.Parallel()
	failStep := func(req llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("")} // empty reply fails checkSummaryReply
	}
	adapter := newFakeAdapter("openai", []func(req llm.Request) llm.Response{failStep, failStep, failStep, failStep})
	client := llm.NewClient()
	client.Register(adapter)
	profile := testProfile("openai", "test", 100_000)
	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("do work")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("done")),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	summarized := cm.ForceCompact(context.Background(), &history, "preserve plan", func(events.EventKind, events.EventData) {})
	if summarized {
		t.Fatal("ForceCompact reported summarized when summarizer failed")
	}
	if len(history) == 0 || history[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("expected fallback to TurnCheckpoint, got: %v", history[0].Kind)
	}
}

func TestForceCompact_InstructionsKeepLaterCheckpointWritesAfterLargeOlderToolOutputs(t *testing.T) {
	t.Parallel()
	var promptSeen string
	adapter := newFakeAdapter("openai", []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			promptSeen = req.Messages[0].Text()
			return llm.Response{Message: llm.Assistant("## Progress\n[CONTEXT SUMMARY] preserved details")}
		},
	})
	client := llm.NewClient()
	client.Register(adapter)
	profile := testProfile("openai", "test", 200_000)
	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2

	history := make([]schema.Turn, 0, 60)
	history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User("initial task")))

	// 25 early turns, each with a 4 KB tool output (~100 KB total, exceeding 80 KB).
	for i := range 25 {
		history = append(history,
			schema.NewTurn(schema.TurnAssistant, llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{
						Kind: llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{
							ID:        fmt.Sprintf("old-c%d", i),
							Name:      "read_file",
							Arguments: []byte(fmt.Sprintf(`{"file_path":"old_%d.txt"}`, i)),
							Type:      "function",
						},
					},
				},
			}),
			schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(fmt.Sprintf("old-c%d", i), "read_file", strings.Repeat("A", 4000), false)),
		)
	}

	// Later folded write_file tool call and result.
	const newerSentinel = "SENTINEL_NEWEST_TOOL_OUTPUT_9918"
	history = append(history,
		schema.NewTurn(schema.TurnAssistant, llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        "c-later",
						Name:      "write_file",
						Arguments: []byte(`{"file_path":"later/important.go","content":"package later\n"}`),
						Type:      "function",
					},
				},
			},
		}),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("c-later", "write_file", newerSentinel, false)),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	)

	cm.ForceCompact(context.Background(), &history, "preserve recent tool findings and file writes", func(events.EventKind, events.EventData) {})

	if !strings.Contains(promptSeen, "later/important.go") {
		t.Fatalf("summarizer prompt missing later file write 'later/important.go'; prompt length: %d", len(promptSeen))
	}
	if !strings.Contains(promptSeen, newerSentinel) {
		t.Fatalf("summarizer prompt missing newest tool output sentinel %q", newerSentinel)
	}
	if len(promptSeen) > 90_000 {
		t.Fatalf("summarizer prompt length %d exceeded 90_000 bound", len(promptSeen))
	}
}

func TestForceCompact_InstructionsBoundSingleBatchedToolResultsTurn(t *testing.T) {
	t.Parallel()
	var promptSeen string
	adapter := newFakeAdapter("openai", []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			promptSeen = req.Messages[0].Text()
			return llm.Response{Message: llm.Assistant("## Progress\n[CONTEXT SUMMARY] preserved")}
		},
	})
	client := llm.NewClient()
	client.Register(adapter)
	profile := testProfile("openai", "test", 50_000)
	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2

	// Single TurnToolResults turn with 60 parts of 4 KB each (240 KB total).
	toolResultsParts := make([]llm.ContentPart, 60)
	for i := range 60 {
		toolResultsParts[i] = llm.ContentPart{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: fmt.Sprintf("call-%d", i),
				Name:       "read_file",
				Content:    strings.Repeat("X", 4000),
			},
		}
	}

	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("start task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("calling tools")),
		schema.NewTurn(schema.TurnToolResults, llm.Message{
			Role:    llm.RoleTool,
			Content: toolResultsParts,
		}),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}

	summarized := cm.ForceCompact(context.Background(), &history, "keep critical findings", func(events.EventKind, events.EventData) {})
	if !summarized {
		t.Fatalf("ForceCompact failed to summarize batched tool results turn (exceeded budget?)")
	}
	if len(promptSeen) > 90_000 {
		t.Fatalf("prompt length %d > 90_000", len(promptSeen))
	}
}

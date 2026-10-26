package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

func TestNewEvalCollector_SetsInitialFields(t *testing.T) {
	t.Parallel()
	c := newEvalCollector("compact", "gpt-4", "write tests")
	m := c.Metrics()
	if m.Strategy != "compact" {
		t.Errorf("expected strategy %q, got %q", "compact", m.Strategy)
	}
	if m.Model != "gpt-4" {
		t.Errorf("expected model %q, got %q", "gpt-4", m.Model)
	}
	if m.Task != "write tests" {
		t.Errorf("expected task %q, got %q", "write tests", m.Task)
	}
	if m.TurnCount != 0 {
		t.Errorf("expected 0 turns, got %d", m.TurnCount)
	}
	if m.Completed {
		t.Error("expected Completed false before any terminal boundary")
	}
}

func TestEvalCollector_CountsTurns(t *testing.T) {
	t.Parallel()
	c := newEvalCollector("compact", "gpt-4", "task")

	// Two assistant text end events = 2 turns.
	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: events.AssistantTextEndData{
			Text:  "hello",
			Usage: llm.Usage{InputTokens: 10, OutputTokens: 5},
		},
	})
	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: events.AssistantTextEndData{
			Text:  "world",
			Usage: llm.Usage{InputTokens: 20, OutputTokens: 8},
		},
	})

	m := c.Metrics()
	if m.TurnCount != 2 {
		t.Errorf("expected 2 turns, got %d", m.TurnCount)
	}
}

func TestEvalCollector_AccumulatesTokens(t *testing.T) {
	t.Parallel()
	c := newEvalCollector("compact", "gpt-4", "task")

	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: events.AssistantTextEndData{
			Usage: llm.Usage{InputTokens: 100, OutputTokens: 50},
		},
	})
	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: events.AssistantTextEndData{
			Usage: llm.Usage{InputTokens: 200, OutputTokens: 30},
		},
	})

	m := c.Metrics()
	if m.TotalInputTokens != 300 {
		t.Errorf("expected 300 input tokens, got %d", m.TotalInputTokens)
	}
	if m.TotalOutputTokens != 80 {
		t.Errorf("expected 80 output tokens, got %d", m.TotalOutputTokens)
	}
}

func TestEvalCollector_AccumulatesCacheTokens(t *testing.T) {
	t.Parallel()
	c := newEvalCollector("compact", "gpt-4", "task")

	// Two events with cache pointers set: both should accumulate.
	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: events.AssistantTextEndData{
			Usage: llm.Usage{
				InputTokens:      10,
				CacheReadTokens:  new(100),
				CacheWriteTokens: new(40),
			},
		},
	})
	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: events.AssistantTextEndData{
			Usage: llm.Usage{
				InputTokens:      10,
				CacheReadTokens:  new(50),
				CacheWriteTokens: new(10),
			},
		},
	})
	// Event with nil cache pointers: the nil guard must skip it without
	// panicking and must not change the cache totals.
	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: events.AssistantTextEndData{
			Usage: llm.Usage{InputTokens: 10},
		},
	})

	m := c.Metrics()
	if m.CacheReadTokens != 150 {
		t.Errorf("expected 150 cache read tokens, got %d", m.CacheReadTokens)
	}
	if m.CacheWriteTokens != 50 {
		t.Errorf("expected 50 cache write tokens, got %d", m.CacheWriteTokens)
	}
}

func TestEvalCollector_CountsCompactionEvents(t *testing.T) {
	t.Parallel()
	c := newEvalCollector("compact", "gpt-4", "task")

	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventContextCompaction,
		Data: events.ContextCompactionData{Layer: "aggressive"},
	})
	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventContextCompaction,
		Data: events.ContextCompactionData{Layer: "moderate"},
	})

	m := c.Metrics()
	if m.CompactionEvents != 2 {
		t.Errorf("expected 2 compaction events, got %d", m.CompactionEvents)
	}
	if len(m.CompactionLayers) != 2 {
		t.Fatalf("expected 2 layers, got %d", len(m.CompactionLayers))
	}
	if m.CompactionLayers[0] != "aggressive" {
		t.Errorf("expected layer %q, got %q", "aggressive", m.CompactionLayers[0])
	}
	if m.CompactionLayers[1] != "moderate" {
		t.Errorf("expected layer %q, got %q", "moderate", m.CompactionLayers[1])
	}
}

func TestEvalCollector_IgnoresUnrelatedEvents(t *testing.T) {
	t.Parallel()
	c := newEvalCollector("compact", "gpt-4", "task")

	c.ProcessEvent(events.SessionEvent{Kind: events.EventSessionStart, Data: events.SessionStartData{Profile: "openai", Model: "gpt-4"}})
	c.ProcessEvent(events.SessionEvent{Kind: events.EventUserInput, Data: events.UserInputData{Text: "hello"}})
	c.ProcessEvent(events.SessionEvent{Kind: events.EventAssistantTextDelta, Data: events.AssistantTextDeltaData{Delta: "hi"}})
	c.ProcessEvent(events.SessionEvent{Kind: events.EventSessionEnd, Data: events.SessionEndData{Reason: "done"}})

	m := c.Metrics()
	if m.TurnCount != 0 {
		t.Errorf("expected 0 turns from unrelated events, got %d", m.TurnCount)
	}
	if m.CompactionEvents != 0 {
		t.Errorf("expected 0 compaction events, got %d", m.CompactionEvents)
	}
}

func TestEvalCollector_TotalTokensComputed(t *testing.T) {
	t.Parallel()
	c := newEvalCollector("compact", "gpt-4", "task")

	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: events.AssistantTextEndData{
			Usage: llm.Usage{InputTokens: 100, OutputTokens: 50},
		},
	})
	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: events.AssistantTextEndData{
			Usage: llm.Usage{InputTokens: 200, OutputTokens: 30},
		},
	})

	m := c.Metrics()
	if m.TotalTokens != 380 {
		t.Errorf("expected TotalTokens 380, got %d", m.TotalTokens)
	}
	// Granular fields should still be populated.
	if m.TotalInputTokens != 300 {
		t.Errorf("expected 300 input tokens, got %d", m.TotalInputTokens)
	}
	if m.TotalOutputTokens != 80 {
		t.Errorf("expected 80 output tokens, got %d", m.TotalOutputTokens)
	}
}

func TestEvalCollector_CountsForkSummaryCalls(t *testing.T) {
	t.Parallel()
	c := newEvalCollector("session-log", "gpt-4", "task")

	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventForkSummary,
		Data: events.ForkSummaryData{Turn: 3},
	})
	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventForkSummary,
		Data: events.ForkSummaryData{Turn: 7},
	})

	m := c.Metrics()
	if m.ForkSummaryCalls != 2 {
		t.Errorf("expected 2 fork summary calls, got %d", m.ForkSummaryCalls)
	}
}

func TestEvalCollector_RetentionScoreDefaultsToZero(t *testing.T) {
	t.Parallel()
	c := newEvalCollector("compact", "gpt-4", "task")
	m := c.Metrics()
	if m.RetentionScore != 0.0 {
		t.Errorf("expected RetentionScore 0.0, got %f", m.RetentionScore)
	}
}

func TestEvalCollector_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	c := newEvalCollector("compact", "gpt-4", "task")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			c.ProcessEvent(events.SessionEvent{
				Kind: events.EventAssistantTextEnd,
				Data: events.AssistantTextEndData{
					Usage: llm.Usage{InputTokens: 1, OutputTokens: 1},
				},
			})
		}
	}()

	// Read concurrently.
	for range 50 {
		_ = c.Metrics()
	}
	<-done

	m := c.Metrics()
	if m.TurnCount != 100 {
		t.Errorf("expected 100 turns, got %d", m.TurnCount)
	}
}

func evalSessionEnd(reason string) events.SessionEvent {
	return events.SessionEvent{Kind: events.EventSessionEnd, Data: events.SessionEndData{Reason: reason, State: "idle"}}
}

func TestEvalCollector_CompletedRequiresSuccessfulInputBoundary(t *testing.T) {
	t.Parallel()

	activity := []events.SessionEvent{
		{Kind: events.EventSessionStart, Data: events.SessionStartData{Profile: "openai", Model: "gpt-4"}},
		{Kind: events.EventAssistantTextEnd, Data: events.AssistantTextEndData{Usage: llm.Usage{InputTokens: 3, OutputTokens: 2}}},
	}
	untypedEnd := events.SessionEvent{Kind: events.EventSessionEnd, Data: events.UserInputData{Text: "wrong-type"}}
	communicateEndTurn := events.SessionEvent{Kind: events.EventCommunicate, Data: events.CommunicateData{EndTurn: true}}

	tests := []struct {
		name   string
		events []events.SessionEvent
		want   bool
	}{
		{"input complete", []events.SessionEvent{evalSessionEnd("input_complete")}, true},
		{"turn failed", []events.SessionEvent{evalSessionEnd("turn_failed")}, false},
		{"interrupted", []events.SessionEvent{evalSessionEnd("interrupted")}, false},
		{"session closed", []events.SessionEvent{evalSessionEnd("session_closed")}, false},
		{"no terminal event", nil, false},
		{"untyped payload", []events.SessionEvent{untypedEnd}, false},
		{"communicate end turn is not completion", []events.SessionEvent{communicateEndTurn}, false},
		{
			"session teardown does not erase completion",
			[]events.SessionEvent{evalSessionEnd("input_complete"), evalSessionEnd("session_closed")},
			true,
		},
		{
			"untyped payload does not erase completion",
			[]events.SessionEvent{evalSessionEnd("input_complete"), untypedEnd},
			true,
		},
		{
			"turn failed clears an earlier completion",
			[]events.SessionEvent{evalSessionEnd("input_complete"), evalSessionEnd("turn_failed")},
			false,
		},
		{
			"interrupted clears an earlier completion",
			[]events.SessionEvent{evalSessionEnd("input_complete"), evalSessionEnd("interrupted")},
			false,
		},
		{
			"a later completion wins",
			[]events.SessionEvent{evalSessionEnd("turn_failed"), evalSessionEnd("input_complete")},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			collector := newEvalCollector("compact", "gpt-4", "task")
			for _, ev := range activity {
				collector.ProcessEvent(ev)
			}
			for _, ev := range tt.events {
				collector.ProcessEvent(ev)
			}
			if got := collector.Metrics().Completed; got != tt.want {
				t.Fatalf("Completed = %v after %s, want %v", got, tt.name, tt.want)
			}
		})
	}
}

func TestEvalCollector_ScriptedProviderEndTurnSetsCompleted(t *testing.T) {
	t.Parallel()

	sess := newSession(t,
		withDir(t.TempDir()),
		withSteps(func(llm.Request) llm.Response {
			return wrapCommunicateResponse(llm.Response{
				Message: llm.Assistant("finished"),
				Finish:  llm.FinishReason{Reason: "stop"},
			})
		}),
	)
	seen, stop := captureEvents(sess)
	t.Cleanup(stop)

	if _, err := sess.ProcessInput(t.Context(), "finish the task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	stop()

	collector := newEvalCollector("memory-pair", "scripted", "stage")
	for _, ev := range *seen {
		collector.ProcessEvent(ev)
	}
	if !collector.Metrics().Completed {
		t.Fatal("Completed = false after a successful scripted input settled")
	}
}

func TestEvalCollector_ScriptedProviderBudgetTerminationNotCompleted(t *testing.T) {
	t.Parallel()

	loop := func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "loop-1", Name: "eval_loop_tool", Arguments: []byte(`{}`), Type: "function"})
	}
	sess := newSession(t,
		withDir(t.TempDir()),
		withConfig(SessionConfig{MaxToolRoundsPerInput: 1}),
		withSteps(loop),
	)
	sess.RegisterTool("eval_loop_tool", "loops", map[string]any{"type": "object"}, func(context.Context, any) (any, error) {
		return "ok", nil
	})
	seen, stop := captureEvents(sess)
	t.Cleanup(stop)

	_, err := sess.ProcessInput(t.Context(), "loop", nil)
	requireBudgetExhaustion(t, err, exhaustedBudgetToolRounds, 1, true)
	stop()

	if !hasEventKind(*seen, events.EventSessionEnd) {
		t.Fatal("budget-terminated input emitted no session terminal event")
	}
	collector := newEvalCollector("memory-pair", "scripted", "stage")
	for _, ev := range *seen {
		collector.ProcessEvent(ev)
	}
	if collector.Metrics().Completed {
		t.Fatal("Completed = true after tool-round budget exhaustion")
	}
}

//go:fix inline

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/llm"
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
				CacheReadTokens:  intPtr(100),
				CacheWriteTokens: intPtr(40),
			},
		},
	})
	c.ProcessEvent(events.SessionEvent{
		Kind: events.EventAssistantTextEnd,
		Data: events.AssistantTextEndData{
			Usage: llm.Usage{
				InputTokens:      10,
				CacheReadTokens:  intPtr(50),
				CacheWriteTokens: intPtr(10),
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
		for i := 0; i < 100; i++ {
			c.ProcessEvent(events.SessionEvent{
				Kind: events.EventAssistantTextEnd,
				Data: events.AssistantTextEndData{
					Usage: llm.Usage{InputTokens: 1, OutputTokens: 1},
				},
			})
		}
	}()

	// Read concurrently.
	for i := 0; i < 50; i++ {
		_ = c.Metrics()
	}
	<-done

	m := c.Metrics()
	if m.TurnCount != 100 {
		t.Errorf("expected 100 turns, got %d", m.TurnCount)
	}
}

func intPtr(v int) *int { return &v }

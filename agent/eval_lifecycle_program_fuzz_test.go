//go:build evenerfuzz

package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/llm"
)

// FuzzEvalLifecycleProgram drives evaluation metrics collection across session
// events.
//
// Oracles:
//   - collected token/event totals equal the supplied event stream and returned
//     snapshots do not alias internal slices.
func FuzzEvalLifecycleProgram(f *testing.F) {
	for _, seed := range []struct {
		program  []byte
		strategy string
		model    string
		task     string
	}{
		{[]byte{0}, "compact", "gpt-eval", "verify context"},
		{[]byte{1}, "session-log", "gpt-cheap", "retain decisions"},
		{[]byte{2}, "", "", ""},
		{[]byte{3}, strings.Repeat("s", 200), "m", strings.Repeat("t", 200)},
	} {
		f.Add(seed.program, seed.strategy, seed.model, seed.task)
	}

	f.Fuzz(func(t *testing.T, program []byte, strategy, model, task string) {
		strategy = evalFuzzText(strategy)
		model = evalFuzzText(model)
		task = evalFuzzText(task)
		selector := byte(0)
		if len(program) > 0 {
			selector = program[0]
		}

		cacheRead := int(selector) + 3
		cacheWrite := int(selector) + 5
		collector := newEvalCollector(strategy, model, task)
		collector.ProcessEvent(events.SessionEvent{
			Kind: events.EventAssistantTextEnd,
			Data: events.AssistantTextEndData{Usage: llm.Usage{
				InputTokens:      11,
				OutputTokens:     7,
				CacheReadTokens:  &cacheRead,
				CacheWriteTokens: &cacheWrite,
			}},
		})
		// Events normally use events.New, but collectors must also tolerate a
		// manually reconstructed event whose payload does not match its kind.
		collector.ProcessEvent(events.SessionEvent{Kind: events.EventAssistantTextEnd, Data: events.UserInputData{Text: "wrong-type"}})
		collector.ProcessEvent(events.SessionEvent{Kind: events.EventContextCompaction, Data: events.ContextCompactionData{Layer: "first"}})
		collector.ProcessEvent(events.SessionEvent{Kind: events.EventContextCompaction, Data: events.ContextCompactionData{}})
		collector.ProcessEvent(events.SessionEvent{Kind: events.EventContextCompaction, Data: events.UserInputData{Text: "wrong-type"}})
		collector.ProcessEvent(events.SessionEvent{Kind: events.EventForkSummary, Data: events.ForkSummaryData{Turn: 1}})
		collector.ProcessEvent(events.SessionEvent{Kind: events.EventUserInput, Data: events.UserInputData{Text: task}})

		metrics := collector.Metrics()
		if metrics.Strategy != strategy || metrics.Model != model || metrics.Task != task {
			t.Fatalf("collector metadata = %#v", metrics)
		}
		if metrics.TurnCount != 2 || metrics.TotalInputTokens != 11 || metrics.TotalOutputTokens != 7 || metrics.TotalTokens != 18 {
			t.Fatalf("collector totals = %#v", metrics)
		}
		if metrics.CacheReadTokens != cacheRead || metrics.CacheWriteTokens != cacheWrite || metrics.CompactionEvents != 3 || metrics.ForkSummaryCalls != 1 {
			t.Fatalf("collector event accounting = %#v", metrics)
		}
		if len(metrics.CompactionLayers) != 1 || metrics.CompactionLayers[0] != "first" {
			t.Fatalf("collector layers = %#v", metrics.CompactionLayers)
		}
		metrics.CompactionLayers[0] = "mutated"
		if next := collector.Metrics(); len(next.CompactionLayers) != 1 || next.CompactionLayers[0] != "first" {
			t.Fatalf("metric snapshot aliases collector state: %#v", next.CompactionLayers)
		}
	})
}

func evalFuzzText(value string) string {
	if len(value) > 256 {
		value = value[:256]
	}
	value = strings.ToValidUTF8(value, "?")
	if value == "" {
		return "eval"
	}
	return value
}

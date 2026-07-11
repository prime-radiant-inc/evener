//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzEvalLifecycleProgram drives evaluation metrics and retention probes through
// their real scripted-provider boundary. It never invokes a live provider.
//
// Oracles:
//   - collected token/event totals equal the supplied event stream and returned
//     snapshots do not alias internal slices;
//   - a scripted YES/NO judge determines the exact retention score; and
//   - agent and judge call failures are reported without partial results.
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

		evalFuzzRetentionContracts(t, selector, task)
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

func evalFuzzRetentionContracts(t *testing.T, selector byte, task string) {
	t.Helper()
	if score, results, err := runRetentionProbes(context.Background(), nil, nil, nil, nil); err != nil || score != 0 || results != nil {
		t.Fatalf("empty retention probes = (%v, %#v, %v)", score, results, err)
	}

	profile := WithCheapModel(NewOpenAIProfile("gpt-main"), "gpt-judge")
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("initial " + task)},
		{Kind: schema.TurnSteering, Message: llm.User("steer " + task)},
		{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call-1", Name: "read_file", Content: "result"}},
			{Kind: llm.ContentText, Text: "ignored"},
		}}},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("assistant " + task)},
	}
	probes := []probeQuestion{
		{Question: "first " + task, Expected: "one", Difficulty: "easy", Type: "factual"},
		{Question: "second " + task, Expected: "two", Difficulty: "hard", Type: "factual"},
	}
	success := &evalFuzzProbeAdapter{responses: []llm.Response{
		{Message: llm.Assistant("one")},
		{Message: llm.Assistant("YES\nexplanation")},
		{Message: llm.Assistant("two")},
		{Message: llm.Assistant("NO")},
	}}
	client := llm.NewClient()
	client.Register(success)
	score, results, err := runRetentionProbes(context.Background(), client, profile, probes, history)
	if err != nil || score != 0.5 || len(results) != 2 || !results[0].Correct || results[1].Correct {
		t.Fatalf("retention score = (%v, %#v, %v)", score, results, err)
	}
	requests := success.Requests()
	if len(requests) != 4 || requests[0].Model != "gpt-main" || requests[1].Model != "gpt-judge" || requests[2].Model != "gpt-main" || requests[3].Model != "gpt-judge" {
		t.Fatalf("retention request routing = %#v", requests)
	}
	if len(requests[0].Messages) <= len(history) || !strings.Contains(requests[1].Messages[0].Text(), "Expected answer: one") {
		t.Fatalf("retention prompt composition is incomplete")
	}

	for raw, want := range map[string]bool{
		"YES":                    true,
		" yes ":                  true,
		"YES\nextra":             true,
		"NO":                     false,
		"maybe":                  false,
		string([]byte{selector}): false,
	} {
		if got := parseBinaryJudge(raw); got != want {
			t.Fatalf("parseBinaryJudge(%q) = %v, want %v", raw, got, want)
		}
	}
	if prompt := buildBinaryJudgePrompt("q", "expected", "response"); !strings.Contains(prompt, "Expected answer: expected") || !strings.Contains(prompt, "Agent's response: response") {
		t.Fatalf("binary judge prompt = %q", prompt)
	}

	agentFailure := &evalFuzzProbeAdapter{errors: map[int]error{0: errors.New("agent failure")}}
	agentClient := llm.NewClient()
	agentClient.Register(agentFailure)
	if score, results, err := runRetentionProbes(context.Background(), agentClient, profile, probes[:1], history); err == nil || score != 0 || results != nil || !strings.Contains(err.Error(), "agent call") {
		t.Fatalf("agent failure result = (%v, %#v, %v)", score, results, err)
	}

	judgeFailure := &evalFuzzProbeAdapter{responses: []llm.Response{{Message: llm.Assistant("answer")}}, errors: map[int]error{1: errors.New("judge failure")}}
	judgeClient := llm.NewClient()
	judgeClient.Register(judgeFailure)
	if score, results, err := runRetentionProbes(context.Background(), judgeClient, profile, probes[:1], history); err == nil || score != 0 || results != nil || !strings.Contains(err.Error(), "judge call") {
		t.Fatalf("judge failure result = (%v, %#v, %v)", score, results, err)
	}
}

type evalFuzzProbeAdapter struct {
	responses []llm.Response
	errors    map[int]error
	requests  []llm.Request
}

func (a *evalFuzzProbeAdapter) Name() string { return "openai" }

func (a *evalFuzzProbeAdapter) Complete(_ context.Context, request llm.Request) (llm.Response, error) {
	index := len(a.requests)
	a.requests = append(a.requests, request)
	if err := a.errors[index]; err != nil {
		return llm.Response{}, err
	}
	if index >= len(a.responses) {
		return llm.Response{Message: llm.Assistant("")}, nil
	}
	response := a.responses[index]
	response.Provider = "openai"
	response.Model = request.Model
	return response, nil
}

func (a *evalFuzzProbeAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *evalFuzzProbeAdapter) Requests() []llm.Request {
	return append([]llm.Request(nil), a.requests...)
}

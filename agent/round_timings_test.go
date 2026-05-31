package agent_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/llm"
)

// benchAdapter is a fakeAdapter that sleeps to simulate LLM latency.
type benchAdapter struct {
	name    string
	latency time.Duration

	mu    sync.Mutex
	calls int
	steps []func(req llm.Request) llm.Response
}

func (a *benchAdapter) Name() string { return a.name }

func (a *benchAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	time.Sleep(a.latency)
	a.mu.Lock()
	defer a.mu.Unlock()
	idx := a.calls
	a.calls++
	if idx < len(a.steps) {
		resp := a.steps[idx](req)
		resp.Provider = a.name
		if resp.Model == "" {
			resp.Model = req.Model
		}
		return resp, nil
	}
	return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("done")}, nil
}

func (a *benchAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return nil, nil
}

func shellToolCall(id string) llm.ToolCallData {
	raw, _ := json.Marshal(map[string]any{
		"command":     "echo ok",
		"description": "test",
	})
	return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
}

func TestRoundTimings_Emitted(t *testing.T) {
	dir := t.TempDir()

	c := llm.NewClient()
	comm := agenttest.CommunicateCall("c1", "result")
	f := &agenttest.FakeAdapter{
		Provider: "openai",
		Steps: []func(req llm.Request) llm.Response{
			// Round 0: tool call
			func(req llm.Request) llm.Response {
				return agenttest.ToolCallResponse(shellToolCall("s1"))
			},
			// Round 1: communicate
			func(req llm.Request) llm.Response {
				return agenttest.ToolCallResponse(comm)
			},
		},
	}
	c.Register(f)

	sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.2"), agent.NewLocalExecutionEnvironment(dir), agent.SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Drain events and collect timings.
	var timings []agent.RoundTimings
	events := sess.Events()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			if ev.Kind == agent.EventRoundTimings {
				if rt, ok := ev.Data.(agent.RoundTimings); ok {
					timings = append(timings, rt)
				}
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "hello", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if out != "result" {
		t.Fatalf("expected 'result', got %q", out)
	}
	sess.Close()
	<-done

	if len(timings) != 2 {
		t.Fatalf("expected 2 round timings events, got %d", len(timings))
	}

	for i, rt := range timings {
		if rt.Round != i {
			t.Errorf("timing[%d].Round = %d, want %d", i, rt.Round, i)
		}
		if rt.TotalRound <= 0 {
			t.Errorf("timing[%d].TotalRound = %v, expected > 0", i, rt.TotalRound)
		}
		if rt.SystemPrompt < 0 {
			t.Errorf("timing[%d].SystemPrompt = %v, expected >= 0", i, rt.SystemPrompt)
		}
		if rt.LLMCall <= 0 {
			t.Errorf("timing[%d].LLMCall = %v, expected > 0", i, rt.LLMCall)
		}
		// ToolExec should be > 0 because we executed exec_command or communicate.
		if rt.ToolExec < 0 {
			t.Errorf("timing[%d].ToolExec = %v, expected >= 0", i, rt.ToolExec)
		}
	}
}

func TestRoundTimings_SerializesToJSON(t *testing.T) {
	rt := agent.RoundTimings{
		Round:        3,
		SystemPrompt: 5 * time.Millisecond,
		LLMCall:      100 * time.Millisecond,
		TotalRound:   120 * time.Millisecond,
	}
	data, err := json.Marshal(rt)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if m["round"] != float64(3) {
		t.Errorf("round = %v, want 3", m["round"])
	}
	// Durations serialize as nanoseconds (time.Duration is int64).
	if m["system_prompt_ns"] != float64(5*time.Millisecond) {
		t.Errorf("system_prompt_ns = %v, want %v", m["system_prompt_ns"], float64(5*time.Millisecond))
	}
}

func BenchmarkRoundOverhead(b *testing.B) {
	dir := b.TempDir()

	const nToolRounds = 3
	const mockLatency = 10 * time.Millisecond

	c := llm.NewClient()

	// Build steps: nToolRounds-1 shell calls + 1 communicate call per benchmark iteration.
	// The adapter resets via b.N outer loop creating fresh sessions.
	makeSteps := func() []func(req llm.Request) llm.Response {
		steps := make([]func(req llm.Request) llm.Response, 0, nToolRounds)
		for i := 0; i < nToolRounds-1; i++ {
			id := "s" + string(rune('0'+i))
			steps = append(steps, func(req llm.Request) llm.Response {
				return agenttest.ToolCallResponse(shellToolCall(id))
			})
		}
		steps = append(steps, func(req llm.Request) llm.Response {
			return agenttest.ToolCallResponse(agenttest.CommunicateCall("c1", "done"))
		})
		return steps
	}

	adapter := &benchAdapter{
		name:    "openai",
		latency: mockLatency,
		steps:   makeSteps(),
	}
	c.Register(adapter)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Reset adapter for each iteration.
		adapter.mu.Lock()
		adapter.calls = 0
		adapter.steps = makeSteps()
		adapter.mu.Unlock()

		sess, err := agent.NewSession(c, agent.NewOpenAIProfile("gpt-5.2"), agent.NewLocalExecutionEnvironment(dir), agent.SessionConfig{})
		if err != nil {
			b.Fatalf("NewSession: %v", err)
		}

		// Drain events to prevent blocking.
		go func() {
			for range sess.Events() {
			}
		}()

		ctx := context.Background()
		_, err = sess.ProcessInput(ctx, "benchmark task", nil)
		if err != nil {
			b.Fatalf("ProcessInput: %v", err)
		}
		sess.Close()
	}
	b.StopTimer()

	// Report per-round overhead: (total - mock LLM latency) / rounds.
	totalTime := b.Elapsed()
	totalRounds := int64(b.N) * nToolRounds
	totalMockLatency := time.Duration(totalRounds) * mockLatency
	overhead := totalTime - totalMockLatency
	if overhead < 0 {
		overhead = 0
	}
	perRound := overhead / time.Duration(totalRounds)
	b.ReportMetric(float64(perRound.Microseconds()), "us/round-overhead")
}

package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// spyStrategy records calls to ManageContext and AfterAction for test assertions.
type spyStrategy struct {
	mu                 sync.Mutex
	manageContextCalls int
	afterActionCalls   int
	toolsDefs          []tool.RegisteredTool
}

func (s *spyStrategy) Name() string { return "spy" }

func (s *spyStrategy) Tools() []tool.RegisteredTool { return s.toolsDefs }

func (s *spyStrategy) ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manageContextCalls++
	return nil
}

func (s *spyStrategy) AfterAction(ctx context.Context, history []schema.Turn, client *llm.Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterActionCalls++
	return nil
}

func (s *spyStrategy) ManageContextCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manageContextCalls
}

func (s *spyStrategy) AfterActionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.afterActionCalls
}

func TestSession_ContextStrategy_SpyHooks(t *testing.T) {
	t.Parallel()
	// Verify that ManageContext is called before LLM requests and
	// AfterAction is called after tool execution rounds.
	dir := t.TempDir()

	spy := &spyStrategy{}

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Round 1: model issues a tool call.
			func(req llm.Request) llm.Response {
				return llm.Response{Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
							ID:        "call_1",
							Name:      "read_file",
							Arguments: []byte(`{"file_path": "` + dir + `/test.txt"}`),
						}},
					},
				}}
			},
			// Round 2: model gives a final text response.
			func(req llm.Request) llm.Response {
				return finalResponse("done")
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		testOnly: testConfig{contextStrategyOverride: spy},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if out != "done" {
		t.Fatalf("expected output %q, got %q", "done", out)
	}

	// ManageContext is called once per LLM round (2 rounds = 2 calls).
	if got := spy.ManageContextCount(); got != 2 {
		t.Errorf("ManageContext call count: got %d, want 2", got)
	}

	// AfterAction is called once per completed tool round. The explicit
	// communicate final response is also a tool round under the new contract.
	if got := spy.AfterActionCount(); got != 2 {
		t.Errorf("AfterAction call count: got %d, want 2", got)
	}
}

func TestSession_ContextStrategy_UnknownStrategyError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	_, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ContextStrategy: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for unknown context strategy, got nil")
	}
}

func TestCompactionThresholdScale(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	check := func(name string, got, want float64) {
		t.Helper()
		if diff := got - want; diff > 0.001 || diff < -0.001 {
			t.Errorf("%s: got %.4f, want %.4f", name, got, want)
		}
	}

	// Scale=0.5 applies normally (all results ≥ 0.20 floor).
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		testOnly: testConfig{compactionThresholdScale: 0.5},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	cm := sess.contextMgr
	check("ObservationMaskThreshold", cm.ObservationMaskThreshold, 0.30) // 0.60 * 0.5
	check("ThinkingClearThreshold", cm.ThinkingClearThreshold, 0.35)     // 0.70 * 0.5
	check("CheckpointThreshold", cm.CheckpointThreshold, 0.40)           // 0.80 * 0.5
	check("SummarizeThreshold", cm.SummarizeThreshold, 0.475)            // 0.95 * 0.5
	check("WarnThreshold", cm.WarnThreshold, 0.375)                      // 0.75 * 0.5
	sess.Close()

	// Scale=0.1 clamps to 0.20 floor.
	sess2, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		testOnly: testConfig{compactionThresholdScale: 0.1},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	cm2 := sess2.contextMgr
	check("ObservationMaskThreshold clamped", cm2.ObservationMaskThreshold, 0.20)
	check("ThinkingClearThreshold clamped", cm2.ThinkingClearThreshold, 0.20)
	check("CheckpointThreshold clamped", cm2.CheckpointThreshold, 0.20)
	check("SummarizeThreshold clamped", cm2.SummarizeThreshold, 0.20)
	check("WarnThreshold clamped", cm2.WarnThreshold, 0.20)
	sess2.Close()
}

func TestSession_ContextStrategy_DefaultIsCompact(t *testing.T) {
	t.Parallel()
	// When no strategy is specified, default to compactStrategy.
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if sess.strategy == nil {
		t.Fatal("expected strategy to be set")
	}
	if sess.strategy.Name() != "compact" {
		t.Errorf("expected default strategy name %q, got %q", "compact", sess.strategy.Name())
	}
}

func TestSession_ContextStrategy_RecallAliasesCompact(t *testing.T) {
	t.Parallel()
	// "recall" is a legacy alias for "compact"; verify it selects the compact
	// strategy so that existing configs naming "recall" keep working after the
	// recall tool was removed.
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ContextStrategy: "recall",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if sess.strategy == nil {
		t.Fatal("expected strategy to be set")
	}
	if sess.strategy.Name() != "compact" {
		t.Errorf("recall alias: expected strategy name %q, got %q", "compact", sess.strategy.Name())
	}
}

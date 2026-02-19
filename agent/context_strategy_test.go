package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

// spyStrategy records calls to ManageContext and AfterAction for test assertions.
type spyStrategy struct {
	mu                  sync.Mutex
	manageContextCalls  int
	afterActionCalls    int
	toolsDefs           []RegisteredTool
}

func (s *spyStrategy) Name() string { return "spy" }

func (s *spyStrategy) Tools() []RegisteredTool { return s.toolsDefs }

func (s *spyStrategy) ManageContext(ctx context.Context, history *[]Turn, sysPromptChars int, emitFn func(EventKind, any)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manageContextCalls++
	return nil
}

func (s *spyStrategy) AfterAction(ctx context.Context, history []Turn, client *llm.Client) error {
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

func TestContextStrategyInterface(t *testing.T) {
	// Verify that CompactStrategy satisfies the ContextStrategy interface.
	var _ ContextStrategy = (*CompactStrategy)(nil)
}

func TestCompactStrategyName(t *testing.T) {
	cs := &CompactStrategy{}
	if cs.Name() != "compact" {
		t.Errorf("expected name %q, got %q", "compact", cs.Name())
	}
}

func TestCompactStrategyTools(t *testing.T) {
	cs := &CompactStrategy{}
	if len(cs.Tools()) != 0 {
		t.Errorf("compact strategy should register no tools, got %d", len(cs.Tools()))
	}
}

func TestCompactStrategyManageContext_Delegation(t *testing.T) {
	// Create a context manager with a small window so history exceeds
	// the 80% checkpoint threshold.
	client := llm.NewClient()
	profile := &baseProfile{id: "openai", model: "test", contextWindow: 500}
	cm := NewContextManager(profile, client)
	cm.PreserveRecentTurns = 2

	cs := NewCompactStrategy(cm)

	// ~425 tokens (85% of 500) to exceed checkpoint threshold.
	history := []Turn{
		{Kind: TurnUserInput, Message: llm.User("read some files")},
		{Kind: TurnAssistant, Message: llm.Assistant(strings.Repeat("analysis ", 200))},
		{Kind: TurnAssistant, Message: llm.Assistant("recent1")},
		{Kind: TurnAssistant, Message: llm.Assistant("recent2")},
	}

	// Track emitted events.
	var emittedEvent EventKind
	emitFn := func(kind EventKind, data any) {
		emittedEvent = kind
	}

	// ManageContext should delegate to ContextManager.MaybeCompact, which should
	// emit a compaction event because pressure exceeds the threshold.
	ctx := context.Background()
	err := cs.ManageContext(ctx, &history, 0, emitFn)
	if err != nil {
		t.Fatalf("ManageContext returned error: %v", err)
	}

	// Verify that a compaction event was emitted.
	if emittedEvent != EventContextCompaction {
		t.Fatalf("expected EventContextCompaction to be emitted, got %v", emittedEvent)
	}

	// Verify checkpoint replaced old history.
	if history[0].Kind != TurnCheckpoint {
		t.Fatalf("expected checkpoint turn, got %v", history[0].Kind)
	}
}

func TestSession_ContextStrategy_SpyHooks(t *testing.T) {
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
				return llm.Response{Message: llm.Assistant("done")}
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		ContextStrategyOverride: spy,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := sess.ProcessInput(ctx, "hi")
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

	// AfterAction is called once per completed tool round (1 tool round = 1 call).
	if got := spy.AfterActionCount(); got != 1 {
		t.Errorf("AfterAction call count: got %d, want 1", got)
	}
}

func TestSession_ContextStrategy_UnknownStrategyError(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	_, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		ContextStrategy: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for unknown context strategy, got nil")
	}
}

func TestCompactionThresholdScale(t *testing.T) {
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
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		CompactionThresholdScale: 0.5,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	cm := sess.contextMgr
	check("ObservationMaskThreshold", cm.ObservationMaskThreshold, 0.30) // 0.60 * 0.5
	check("ThinkingClearThreshold", cm.ThinkingClearThreshold, 0.35)     // 0.70 * 0.5
	check("CheckpointThreshold", cm.CheckpointThreshold, 0.40)          // 0.80 * 0.5
	check("SummarizeThreshold", cm.SummarizeThreshold, 0.45)            // 0.90 * 0.5
	sess.Close()

	// Scale=0.1 clamps to 0.20 floor.
	sess2, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		CompactionThresholdScale: 0.1,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	cm2 := sess2.contextMgr
	check("ObservationMaskThreshold clamped", cm2.ObservationMaskThreshold, 0.20)
	check("ThinkingClearThreshold clamped", cm2.ThinkingClearThreshold, 0.20)
	check("CheckpointThreshold clamped", cm2.CheckpointThreshold, 0.20)
	check("SummarizeThreshold clamped", cm2.SummarizeThreshold, 0.20)
	sess2.Close()
}

func TestSession_ContextStrategy_DefaultIsCompact(t *testing.T) {
	// When no strategy is specified, default to CompactStrategy.
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
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

package contextmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestCov_LastInputTokens(t *testing.T) {
	cm := NewManager(testOpenAIProfileWithContextWindow(1000), llm.NewClient())
	if got := cm.LastInputTokens(); got != 0 {
		t.Errorf("fresh manager LastInputTokens = %d, want 0", got)
	}
	cm.RecordInputTokens(1234, 3)
	if got := cm.LastInputTokens(); got != 1234 {
		t.Errorf("LastInputTokens = %d, want 1234", got)
	}
}

func TestCov_CompactStrategy_AfterActionNoop(t *testing.T) {
	cm := NewManager(testOpenAIProfileWithContextWindow(1000), llm.NewClient())
	s := NewCompactStrategy(cm)
	if err := s.AfterAction(context.Background(), nil, llm.NewClient()); err != nil {
		t.Errorf("CompactStrategy.AfterAction should be a nil no-op, got %v", err)
	}
	if s.Name() != "compact" {
		t.Errorf("name = %q, want compact", s.Name())
	}
}

// NewOODAStrategy builds the log path from the host's StateDir/ID; a fake host
// with a real temp StateDir exercises the constructor end-to-end.
func TestCov_NewOODAStrategyConstructor(t *testing.T) {
	profile := testOpenAIProfileWithContextWindow(1000)
	host := &fakeStrategyHost{stateDir: t.TempDir(), id: "OODA-1", profile: profile}
	ooda, err := NewOODAStrategy(NewManager(profile, llm.NewClient()), host)
	if err != nil {
		t.Fatalf("NewOODAStrategy: %v", err)
	}
	if ooda.Name() != "ooda" {
		t.Errorf("name = %q, want ooda", ooda.Name())
	}
}

// MemoryCrystalsStrategy.ManageContext runs base compaction, then injects the
// crystal bank when crystals are present.
func TestCov_MemoryCrystals_ManageContextInjects(t *testing.T) {
	cm := NewManager(testOpenAIProfileWithContextWindow(1_000_000), llm.NewClient())
	s := NewMemoryCrystalsStrategy(cm)

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("hi")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("hello")},
	}
	// Empty bank: no steering turn injected.
	if err := s.ManageContext(context.Background(), &history, 0, noopEmit); err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("empty crystal bank should not inject, got %d turns", len(history))
	}

	// With crystals: a steering turn carrying the bank is appended.
	s.crystals = append(s.crystals, MemoryCrystal{Turn: 1, Action: "shell", Facts: "built ok"})
	if err := s.ManageContext(context.Background(), &history, 0, noopEmit); err != nil {
		t.Fatal(err)
	}
	last := history[len(history)-1]
	if last.Kind != schema.TurnSteering || !strings.Contains(last.Message.Text(), "[MEMORY CRYSTALS]") {
		t.Errorf("crystal bank should be injected as steering turn, got %+v", last)
	}
	if !strings.Contains(last.Message.Text(), "built ok") {
		t.Errorf("crystal facts missing from injected turn: %s", last.Message.Text())
	}
}

func TestCov_ShouldFallbackSummarizationModel(t *testing.T) {
	if shouldFallbackSummarizationModel(context.Background(), nil) {
		t.Error("nil error should not fall back")
	}
	// A cancelled context never triggers a model fallback.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldFallbackSummarizationModel(cctx, errors.New("boom")) {
		t.Error("cancelled ctx should not fall back")
	}
	if shouldFallbackSummarizationModel(context.Background(), context.Canceled) {
		t.Error("context.Canceled error should not fall back")
	}
	// A permanent 404 (model not found) is a fallback-worthy permanent error.
	notFound := llm.ErrorFromHTTPStatus("openai", 404, "model not found", nil, nil)
	if !shouldFallbackSummarizationModel(context.Background(), notFound) {
		t.Error("404 not-found should trigger a model fallback")
	}
	// A retryable 503 is neither fallback nor permanent → no model fallback.
	retryable := llm.ErrorFromHTTPStatus("openai", 503, "overloaded", nil, nil)
	if shouldFallbackSummarizationModel(context.Background(), retryable) {
		t.Error("retryable 503 should not trigger a model fallback")
	}
}

func TestCov_RecursiveDistill_MicroSummarize(t *testing.T) {
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("  did work; nothing left  ")} },
	}}
	client := llm.NewClient()
	client.Register(adapter)

	cm := NewManager(testOpenAIProfileWithContextWindow(1000), client)
	s := NewRecursiveDistillStrategy(cm)

	history := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant("ran the build")},
		{Kind: schema.TurnToolResults, Message: llm.ToolResultNamed("c1", "shell", "PASS", false)},
	}
	got, err := s.microSummarize(context.Background(), client, history)
	if err != nil {
		t.Fatalf("microSummarize: %v", err)
	}
	if got != "did work; nothing left" {
		t.Errorf("microSummarize = %q, want trimmed summary", got)
	}
	// The cheap-model request must include the tool result in its prompt.
	reqs := adapter.Requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0].Messages[0].Text(), "Tool(shell)") {
		t.Errorf("prompt should include the tool result, got %+v", reqs)
	}
}

// RecursiveDistillStrategy.ManageContext injects the distilled hierarchy when
// micro/macro summaries exist.
func TestCov_RecursiveDistill_ManageContextInjects(t *testing.T) {
	cm := NewManager(testOpenAIProfileWithContextWindow(1_000_000), llm.NewClient())
	s := NewRecursiveDistillStrategy(cm)
	s.microSummaries = append(s.microSummaries, "did a thing")

	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("hi")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("hello")},
	}
	if err := s.ManageContext(context.Background(), &history, 0, noopEmit); err != nil {
		t.Fatal(err)
	}
	last := history[len(history)-1]
	if last.Kind != schema.TurnSteering || !strings.Contains(last.Message.Text(), "[DISTILLED MEMORY]") {
		t.Errorf("distilled hierarchy should be injected, got %+v", last)
	}
	if s.Name() != "recursive-distill" {
		t.Errorf("name = %q, want recursive-distill", s.Name())
	}
}

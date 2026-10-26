package contextmgr

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/llm"
)

func TestCov_LastInputTokens(t *testing.T) {
	cm := NewManager(testOpenAIProfileWithContextWindow(1000), llm.NewClient(), cheapmodel.New(llm.NewClient()))
	if got := cm.LastInputTokens(); got != 0 {
		t.Errorf("fresh manager LastInputTokens = %d, want 0", got)
	}
	cm.RecordInputTokens(1234, 3)
	if got := cm.LastInputTokens(); got != 1234 {
		t.Errorf("LastInputTokens = %d, want 1234", got)
	}
}

func TestCov_CompactStrategy_AfterActionNoop(t *testing.T) {
	cm := NewManager(testOpenAIProfileWithContextWindow(1000), llm.NewClient(), cheapmodel.New(llm.NewClient()))
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
	ooda, err := NewOODAStrategy(NewManager(profile, llm.NewClient(), cheapmodel.New(llm.NewClient())), host)
	if err != nil {
		t.Fatalf("NewOODAStrategy: %v", err)
	}
	if ooda.Name() != "ooda" {
		t.Errorf("name = %q, want ooda", ooda.Name())
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

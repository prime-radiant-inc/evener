package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/llm"
)

type liveModelMetadataAdapter struct {
	fakeAdapter

	models    []llm.ModelInfo
	listCalls int
}

func (a *liveModelMetadataAdapter) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	_ = ctx
	a.listCalls++
	return append([]llm.ModelInfo(nil), a.models...), nil
}

func TestNewSessionAppliesLiveOpenAIModelContextWindow(t *testing.T) {
	adapter := &liveModelMetadataAdapter{
		fakeAdapter: fakeAdapter{name: "openai"},
		models: []llm.ModelInfo{{
			ID:            "gpt-5.5",
			Provider:      "openai",
			ContextWindow: 1_000_000,
		}},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if adapter.listCalls == 0 {
		t.Fatal("ListModels was not called")
	}
	if got := sess.profile.ContextWindowSize(); got != 1_000_000 {
		t.Fatalf("profile ContextWindowSize() = %d, want live 1000000", got)
	}
	if got := sess.contextMgr.profile.ContextWindowSize(); got != 1_000_000 {
		t.Fatalf("context manager ContextWindowSize() = %d, want live 1000000", got)
	}
}

func TestSessionSetModelAppliesLiveOpenAIModelContextWindow(t *testing.T) {
	adapter := &liveModelMetadataAdapter{
		fakeAdapter: fakeAdapter{name: "openai"},
		models: []llm.ModelInfo{
			{ID: "gpt-5.4", Provider: "openai", ContextWindow: 400_000},
			{ID: "gpt-5.5", Provider: "openai", ContextWindow: 1_000_000},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	sess.SetModel("gpt-5.5")

	if got := sess.profile.Model(); got != "gpt-5.5" {
		t.Fatalf("Model() = %q, want gpt-5.5", got)
	}
	if got := sess.profile.ContextWindowSize(); got != 1_000_000 {
		t.Fatalf("profile ContextWindowSize() = %d, want live 1000000", got)
	}
	if got := sess.contextMgr.profile.ContextWindowSize(); got != 1_000_000 {
		t.Fatalf("context manager ContextWindowSize() = %d, want live 1000000", got)
	}
}

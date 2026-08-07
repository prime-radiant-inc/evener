package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
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
	t.Parallel()
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

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if adapter.listCalls == 0 {
		t.Fatal("ListModels was not called")
	}
	if got := sess.profile.ContextWindowSize(); got != 1_000_000 {
		t.Fatalf("profile ContextWindowSize() = %d, want live 1000000", got)
	}
	if got := sess.ContextMetrics().Window; got != 1_000_000 {
		t.Fatalf("context manager window via ContextMetrics = %d, want live 1000000", got)
	}
}

func TestSessionSetModelAppliesLiveOpenAIModelContextWindow(t *testing.T) {
	t.Parallel()
	adapter := &liveModelMetadataAdapter{
		fakeAdapter: fakeAdapter{name: "openai"},
		models: []llm.ModelInfo{
			{ID: "gpt-5.4", Provider: "openai", ContextWindow: 400_000},
			{ID: "gpt-5.5", Provider: "openai", ContextWindow: 1_000_000},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
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
	if got := sess.ContextMetrics().Window; got != 1_000_000 {
		t.Fatalf("context manager window via ContextMetrics = %d, want live 1000000", got)
	}
}

// TestSessionSetModelFetchesModelListExactlyOnce locks in the TOCTOU fix:
// SetModel fetches the live model list a single time and reuses it for both
// metadata fill and the membership preflight, rather than calling ListModels
// twice (which could observe two different catalogs mid-switch).
func TestSessionSetModelFetchesModelListExactlyOnce(t *testing.T) {
	t.Parallel()
	adapter := &liveModelMetadataAdapter{
		fakeAdapter: fakeAdapter{name: "openai"},
		models: []llm.ModelInfo{
			{ID: "gpt-5.4", Provider: "openai", ContextWindow: 400_000},
			{ID: "gpt-5.5", Provider: "openai", ContextWindow: 1_000_000},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Discard the init-time fetch; count only the fetches SetModel triggers.
	adapter.listCalls = 0
	if err := sess.SetModel("gpt-5.5"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	if adapter.listCalls != 1 {
		t.Fatalf("ListModels called %d times during SetModel, want exactly 1", adapter.listCalls)
	}
}

// TestNewSession_RejectsModelAbsentFromEnumerableInstance verifies that
// NewSession (Task 3: resolveLiveModelProfileValidated) fails closed when the
// requested profile's model is absent from a successfully-enumerated live
// model list, naming the requested model and a live alternative.
func TestNewSession_RejectsModelAbsentFromEnumerableInstance(t *testing.T) {
	t.Parallel()
	adapter := &liveModelMetadataAdapter{
		fakeAdapter: fakeAdapter{name: "openai"},
		models:      []llm.ModelInfo{{ID: "gpt-5.5", Provider: "openai"}},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.9-does-not-exist"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err == nil {
		t.Fatal("NewSession with a model absent from the live list = nil error, want non-nil")
	}
	if sess != nil {
		t.Fatalf("NewSession returned a non-nil session alongside an error: %#v", sess)
	}
	if !strings.Contains(err.Error(), "gpt-5.9-does-not-exist") {
		t.Fatalf("error = %q, want it to name the requested model", err.Error())
	}
	if !strings.Contains(err.Error(), "gpt-5.5") {
		t.Fatalf("error = %q, want it to name a live alternative %q", err.Error(), "gpt-5.5")
	}
}

// TestNewSession_EnumerationFailure_FailsOpen verifies that NewSession still
// succeeds when the client can't enumerate live models (no client registered
// for the profile's instance) — the fail-open path from before Task 3 must
// remain unaffected by the new membership check.
func TestNewSession_EnumerationFailure_FailsOpen(t *testing.T) {
	t.Parallel()
	client := llm.NewClient() // no adapter registered: ListModels fails to find the instance

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.9-does-not-exist"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession with unenumerable instance should fail open, got error: %v", err)
	}
	if sess == nil {
		t.Fatal("NewSession returned a nil session with a nil error")
	}
}

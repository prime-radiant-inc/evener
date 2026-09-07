package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// liveModelMetadataAdapter is an override that can list models. Its rows
// settle membership; the model facts a session picks up come from the
// registry, which is the only thing that speaks for an instance (spec §5).
type liveModelMetadataAdapter struct {
	fakeAdapter

	models    []registry.Model
	listCalls int
	observe   func(context.Context)
}

func (a *liveModelMetadataAdapter) LiveModels(ctx context.Context) ([]registry.Model, error) {
	if a.observe != nil {
		a.observe(ctx)
	}
	a.listCalls++
	return append([]registry.Model(nil), a.models...), nil
}

// Every session-owned enumeration must forward the session policy while retaining
// its independent caller enumeration deadline.
func TestSessionListingsForwardIdlePolicy(t *testing.T) {
	adapter := &liveModelMetadataAdapter{models: []registry.Model{{ID: "gpt-5.4"}, {ID: "gpt-5.5"}}}
	adapter.name = "openai"
	client := registryClient(t, map[string]registry.Provider{"openai": {Base: "openai", APIKey: "k"}}, adapter)
	phase := "startup"
	wantBudget := liveModelMetadataTimeout
	calls := 0
	adapter.observe = func(ctx context.Context) {
		calls++
		want := llm.AdapterTimeout{Connect: 10 * time.Second, StreamRead: 37 * time.Second}
		if got := *llm.ModelListingTimeout(ctx); got != want {
			t.Errorf("%s policy=%+v want %+v", phase, got, want)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > wantBudget {
			t.Errorf("%s lost caller enumeration budget %v", phase, wantBudget)
		}
	}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{ProviderIdleTimeout: "37s"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if calls != 1 {
		t.Fatalf("startup calls=%d want 1", calls)
	}
	phase, wantBudget = "model-switch", modelSwitchEnumerationTimeout
	if err := sess.SetModel("gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("switch calls=%d want 2", calls)
	}
	phase, wantBudget = "plugin", liveModelMetadataTimeout
	result := sess.resolvePluginAgentModel(context.Background(), sess.profile, "gpt-5.4")
	if result.reason != "" {
		t.Fatalf("plugin reason=%s", result.reason)
	}
	if calls != 3 {
		t.Fatalf("plugin calls=%d want 3", calls)
	}
	phase, wantBudget = "explicit-subagent", modelSwitchEnumerationTimeout
	sess.delegationAllowance = 1
	if _, err := sess.selectSubagentModel(context.Background(), "gpt-5.4", ""); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("subagent calls=%d want 4", calls)
	}
	phase, wantBudget = "availability", liveModelMetadataTimeout
	sess.delegationAllowance = 1
	other := &liveModelMetadataAdapter{models: adapter.models, observe: adapter.observe}
	other.name = "other"
	client.Register(other)
	sess.captureModelAvailability(liveModelEnumeration{listing: llm.ModelListing{Live: true}})
	if other.listCalls != 1 {
		t.Fatalf("availability calls=%d want 1", other.listCalls)
	}
	phase, wantBudget = "restore", liveModelMetadataTimeout
	before := adapter.listCalls
	meta := schema.SessionMeta{ID: "01RESTORELISTPOLICY000001", ProfileID: "openai", Model: "gpt-5.4", Config: schema.ConfigSnapshot{ProviderIdleTimeout: "37s"}}
	restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if adapter.listCalls != before+1 {
		t.Fatalf("restore calls=%d want %d", adapter.listCalls, before+1)
	}
}

func TestNewSessionAppliesRegistryModelContextWindow(t *testing.T) {
	t.Parallel()
	adapter := &liveModelMetadataAdapter{models: []registry.Model{{ID: "gpt-5.5"}}}
	adapter.name = "openai"
	client := registryClient(t, map[string]registry.Provider{
		"openai": {Base: "openai", APIKey: "k", Models: map[string]registry.Model{
			"gpt-5.5": {Caps: registry.Caps{ContextWindow: new(1_000_000)}},
		}},
	}, adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if adapter.listCalls == 0 {
		t.Fatal("the instance was never listed")
	}
	if got := sess.profile.ContextWindowSize(); got != 1_000_000 {
		t.Fatalf("profile ContextWindowSize() = %d, want the registry's 1000000", got)
	}
	if got := sess.ContextMetrics().Window; got != 1_000_000 {
		t.Fatalf("context manager window via ContextMetrics = %d, want 1000000", got)
	}
}

func TestSessionSetModelAppliesRegistryModelContextWindow(t *testing.T) {
	t.Parallel()
	adapter := &liveModelMetadataAdapter{models: []registry.Model{{ID: "gpt-5.4"}, {ID: "gpt-5.5"}}}
	adapter.name = "openai"
	client := registryClient(t, map[string]registry.Provider{
		"openai": {Base: "openai", APIKey: "k", Models: map[string]registry.Model{
			"gpt-5.4": {Caps: registry.Caps{ContextWindow: new(400_000)}},
			"gpt-5.5": {Caps: registry.Caps{ContextWindow: new(1_000_000)}},
		}},
	}, adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := sess.SetModel("gpt-5.5"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	if got := sess.profile.Model(); got != "gpt-5.5" {
		t.Fatalf("Model() = %q, want gpt-5.5", got)
	}
	if got := sess.profile.ContextWindowSize(); got != 1_000_000 {
		t.Fatalf("profile ContextWindowSize() = %d, want the registry's 1000000", got)
	}
	if got := sess.ContextMetrics().Window; got != 1_000_000 {
		t.Fatalf("context manager window via ContextMetrics = %d, want 1000000", got)
	}
}

// TestSessionSetModelListsExactlyOnce locks in the TOCTOU fix: SetModel lists
// the instance a single time and reuses the listing for both metadata fill and
// the membership preflight, rather than listing twice (which could observe two
// different catalogs mid-switch).
func TestSessionSetModelListsExactlyOnce(t *testing.T) {
	t.Parallel()
	adapter := &liveModelMetadataAdapter{models: []registry.Model{{ID: "gpt-5.4"}, {ID: "gpt-5.5"}}}
	adapter.name = "openai"
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// Discard the init-time listing; count only the ones SetModel triggers.
	adapter.listCalls = 0
	if err := sess.SetModel("gpt-5.5"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	if adapter.listCalls != 1 {
		t.Fatalf("the instance was listed %d times during SetModel, want exactly 1", adapter.listCalls)
	}
}

// TestNewSession_RejectsModelAbsentFromEnumerableInstance verifies that
// NewSession (resolveLiveModelProfileValidated) fails closed when the
// requested profile's model is absent from a successfully-fetched live
// listing, naming the requested model and a live alternative.
func TestNewSession_RejectsModelAbsentFromEnumerableInstance(t *testing.T) {
	t.Parallel()
	adapter := &liveModelMetadataAdapter{models: []registry.Model{{ID: "gpt-5.5"}}}
	adapter.name = "openai"
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
// succeeds when the instance cannot be listed at all (an override that serves
// no listing) — the fail-open path must remain unaffected by the membership
// check.
func TestNewSession_EnumerationFailure_FailsOpen(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"}) // no scripted listing: cannot list

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.9-does-not-exist"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession with an unlistable instance should fail open, got error: %v", err)
	}
	if sess == nil {
		t.Fatal("NewSession returned a nil session with a nil error")
	}
}

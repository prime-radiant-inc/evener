package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

type pluginModelListAdapter struct {
	fakeAdapter

	mu     sync.Mutex
	models []llm.ModelInfo
	err    error
	calls  int
}

func (a *pluginModelListAdapter) ListModels(context.Context) ([]llm.ModelInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return append([]llm.ModelInfo(nil), a.models...), a.err
}

func (a *pluginModelListAdapter) listCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *pluginModelListAdapter) resetListCalls() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = 0
}

func TestSelectSubagentModel_PluginAvailabilityPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		parentModel        string
		pluginModel        string
		explicitModel      string
		liveModels         []llm.ModelInfo
		listErr            error
		wantRequestedModel string
		wantProfileID      string
		wantModel          string
		wantWarning        bool
		wantReason         string
		wantResolverCalls  int
		wantListCalls      int
	}{
		{
			name:               "available exact plugin model wins",
			parentModel:        "gpt-5.2",
			pluginModel:        "gpt-5.3",
			explicitModel:      "anthropic/boom",
			liveModels:         []llm.ModelInfo{{ID: "gpt-5.2"}, {ID: "gpt-5.3", ContextWindow: 753_001}},
			wantRequestedModel: "gpt-5.3",
			wantProfileID:      "openai",
			wantModel:          "gpt-5.3",
			wantWarning:        false,
			wantResolverCalls:  0,
			wantListCalls:      1,
		},
		{
			// The explicit fallback model must itself be live-enumerable
			// (Task 3: unwiring the explicit-override call site through
			// resolveModelSwitchTarget), so gpt-5.3 is included alongside
			// gpt-5.2 here.
			name:               "unavailable plugin model uses explicit fallback",
			parentModel:        "gpt-5.2",
			pluginModel:        "gpt-4.1-nano",
			explicitModel:      "gpt-5.3",
			liveModels:         []llm.ModelInfo{{ID: "gpt-5.2"}, {ID: "gpt-5.3"}},
			wantRequestedModel: "gpt-5.3",
			wantProfileID:      "openai",
			wantModel:          "gpt-5.3",
			wantWarning:        true,
			wantReason:         "unavailable",
			wantResolverCalls:  0,
			wantListCalls:      2,
		},
		{
			name:               "unenumerable plugin model inherits parent",
			parentModel:        "gpt-5.2",
			pluginModel:        "gpt-5.3",
			listErr:            errors.New("models endpoint disabled"),
			wantRequestedModel: "",
			wantProfileID:      "openai",
			wantModel:          "gpt-5.2",
			wantWarning:        true,
			wantReason:         "unverified",
			wantResolverCalls:  0,
			wantListCalls:      1,
		},
		{
			// Both the launch profile's own model (validated at NewSession)
			// and the explicit override (validated by the Task 3 call site)
			// must be live-enumerable here.
			name:               "inherit uses explicit model",
			parentModel:        "gpt-5.2",
			pluginModel:        "inherit",
			explicitModel:      "gpt-5.3",
			liveModels:         []llm.ModelInfo{{ID: "gpt-5.2"}, {ID: "gpt-5.3"}},
			wantRequestedModel: "gpt-5.3",
			wantProfileID:      "openai",
			wantModel:          "gpt-5.3",
			wantResolverCalls:  0,
			wantListCalls:      1,
		},
		{
			name:               "inherit without explicit model uses parent",
			parentModel:        "gpt-5.2",
			pluginModel:        "inherit",
			liveModels:         []llm.ModelInfo{{ID: "gpt-5.2"}},
			wantRequestedModel: "",
			wantProfileID:      "openai",
			wantModel:          "gpt-5.2",
			wantResolverCalls:  0,
			wantListCalls:      0,
		},
		{
			name:               "plugin model identical to parent skips enumeration",
			parentModel:        "gpt-5.2",
			pluginModel:        "gpt-5.2",
			liveModels:         []llm.ModelInfo{{ID: "gpt-5.2"}},
			wantRequestedModel: "gpt-5.2",
			wantProfileID:      "openai",
			wantModel:          "gpt-5.2",
			wantResolverCalls:  0,
			wantListCalls:      0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			adapter := &pluginModelListAdapter{
				fakeAdapter: fakeAdapter{name: "openai"},
				models:      tc.liveModels,
				err:         tc.listErr,
			}
			resolverCalls := 0
			sess := newSession(t,
				withProfile(NewOpenAIProfile(tc.parentModel)),
				withAdapter(adapter),
				withConfig(SessionConfig{
					MaxSubagentDepth: 1,
					ResolveProfile: func(string) (*provider.Profile, error) {
						resolverCalls++
						return nil, errors.New("explicit fallback must not be resolved")
					},
					testOnly: testConfig{skipGitSnapshot: true},
				}),
			)
			adapter.resetListCalls()
			sess.pluginAgents = map[string]plugin.Agent{
				"reviewer": {
					Name:       "reviewer",
					Model:      tc.pluginModel,
					PluginName: "test-plugin",
				},
			}

			got, err := sess.selectSubagentModel(context.Background(), tc.explicitModel, "reviewer")
			if err != nil {
				t.Fatalf("selectSubagentModel: %v", err)
			}
			if got.requestedModel != tc.wantRequestedModel {
				t.Errorf("requestedModel = %q, want %q", got.requestedModel, tc.wantRequestedModel)
			}
			if got.profile.ID() != tc.wantProfileID {
				t.Errorf("profile ID = %q, want %q", got.profile.ID(), tc.wantProfileID)
			}
			if got.profile.Model() != tc.wantModel {
				t.Errorf("profile model = %q, want %q", got.profile.Model(), tc.wantModel)
			}
			if (got.warning != nil) != tc.wantWarning {
				t.Fatalf("warning present = %t, want %t", got.warning != nil, tc.wantWarning)
			}
			if got.warning != nil {
				if got.warning.Source != "plugin" ||
					got.warning.Title != "plugin agent model unavailable" ||
					got.warning.PluginName != "test-plugin" {
					t.Errorf("warning = %#v, want plugin fallback warning", got.warning)
				}
				if !strings.Contains(got.warning.Message, tc.wantReason) {
					t.Errorf("warning message = %q, want reason %q", got.warning.Message, tc.wantReason)
				}
			}
			if resolverCalls != tc.wantResolverCalls {
				t.Errorf("resolver calls = %d, want %d", resolverCalls, tc.wantResolverCalls)
			}
			if adapter.listCalls() != tc.wantListCalls {
				t.Errorf("ListModels calls = %d, want %d", adapter.listCalls(), tc.wantListCalls)
			}
			if tc.name == "available exact plugin model wins" && got.profile.ContextWindowSize() != 753_001 {
				t.Errorf("profile context window = %d, want advertised live metadata 753001", got.profile.ContextWindowSize())
			}
		})
	}
}

func TestSelectSubagentModel_RenamedAnthropicAliasStaysOnInstance(t *testing.T) {
	t.Parallel()

	base := WithProviderID(newAnthropicProfile("claude-opus-4-6"), "work")
	adapter := &pluginModelListAdapter{
		fakeAdapter: fakeAdapter{name: "work"},
		models: []llm.ModelInfo{
			// The launch profile's own model must be live-enumerable too:
			// NewSession now validates it (Task 3).
			{ID: "claude-opus-4-6"},
			{
				ID:                    "claude-sonnet-4-6",
				ContextWindow:         764_002,
				SupportsReasoning:     true,
				ReasoningEffortLevels: []string{"low", "high"},
			},
		},
	}
	sess := newPluginModelSelectionSession(t, base, adapter, "sonnet", nil)

	got, err := sess.selectSubagentModel(context.Background(), "", "reviewer")
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	assertSelectedPluginModel(t, got, "sonnet", "work", "claude-sonnet-4-6", 764_002)
	if got.profile.BehaviorTag() != "anthropic" {
		t.Errorf("behavior tag = %q, want anthropic", got.profile.BehaviorTag())
	}
	if adapter.listCalls() != 1 {
		t.Errorf("ListModels calls = %d, want 1", adapter.listCalls())
	}
}

func TestSelectSubagentModel_KimiRejectsAnthropicAliasWithoutEnumeration(t *testing.T) {
	t.Parallel()

	adapter := &pluginModelListAdapter{
		fakeAdapter: fakeAdapter{name: "kimi-anthropic"},
		// "k3" (the launch profile's own model, and here also the explicit
		// override) must be live-enumerable: NewSession validates it, and so
		// does the explicit-override call site (Task 3).
		models: []llm.ModelInfo{{ID: "claude-sonnet-4-6"}, {ID: "k3"}},
	}
	sess := newPluginModelSelectionSession(t, newKimiAnthropicProfile("k3"), adapter, "sonnet", nil)

	got, err := sess.selectSubagentModel(context.Background(), "k3", "reviewer")
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	assertPluginFallback(t, got, "k3", "kimi-anthropic", "k3", "cross-provider")
	// The plugin model ("sonnet") is rejected as cross-provider without any
	// enumeration; the one call here is the explicit override "k3"'s
	// membership validation (resolveModelSwitchTarget).
	if adapter.listCalls() != 1 {
		t.Errorf("ListModels calls = %d, want 1", adapter.listCalls())
	}
}

func TestSelectSubagentModel_QualifiedPluginRefNeverUsesSessionResolver(t *testing.T) {
	t.Parallel()

	resolverCalls := 0
	adapter := &pluginModelListAdapter{
		fakeAdapter: fakeAdapter{name: "kimi-anthropic"},
		// "k3" (the launch profile's own model, and here also the explicit
		// override) must be live-enumerable: NewSession validates it, and so
		// does the explicit-override call site (Task 3).
		models: []llm.ModelInfo{{ID: "anthropic/claude-sonnet-4-6"}, {ID: "k3"}},
	}
	sess := newPluginModelSelectionSession(
		t,
		newKimiAnthropicProfile("k3"),
		adapter,
		"anthropic/claude-sonnet-4-6",
		func(string) (*provider.Profile, error) {
			resolverCalls++
			return newAnthropicProfile("claude-sonnet-4-6"), nil
		},
	)

	got, err := sess.selectSubagentModel(context.Background(), "k3", "reviewer")
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	assertPluginFallback(t, got, "k3", "kimi-anthropic", "k3", "cross-provider")
	if resolverCalls != 0 {
		t.Errorf("resolver calls = %d, want 0", resolverCalls)
	}
	// The plugin model is rejected as cross-provider without any
	// enumeration; the one call here is the explicit override "k3"'s
	// membership validation (resolveModelSwitchTarget).
	if adapter.listCalls() != 1 {
		t.Errorf("ListModels calls = %d, want 1", adapter.listCalls())
	}
}

func TestSelectSubagentModel_OpenRouterAliasKeepsUpstreamNamespace(t *testing.T) {
	t.Parallel()

	base := newOpenRouterAnthropicProfile("anthropic/claude-opus-4-6")
	adapter := &pluginModelListAdapter{
		fakeAdapter: fakeAdapter{name: "openrouter-anthropic"},
		models: []llm.ModelInfo{{
			ID:            "anthropic/claude-sonnet-4-6",
			ContextWindow: 775_003,
		}},
	}
	sess := newPluginModelSelectionSession(t, base, adapter, "sonnet", nil)

	got, err := sess.selectSubagentModel(context.Background(), "", "reviewer")
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	assertSelectedPluginModel(
		t,
		got,
		"sonnet",
		"openrouter-anthropic",
		"anthropic/claude-sonnet-4-6",
		775_003,
	)
}

func TestResolvePluginAgentModel_CustomAndExactMembership(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		requested  string
		models     []llm.ModelInfo
		wantModel  string
		wantCtx    int
		wantReason string
	}{
		{
			name:      "unknown exact custom ID is accepted when advertised",
			requested: "company-special-v9",
			// The launch profile's own model (claude-opus-4-6) must also be
			// live-enumerable: NewSession validates it (Task 3).
			models:    []llm.ModelInfo{{ID: "claude-opus-4-6"}, {ID: "company-special-v9", ContextWindow: 786_004}},
			wantModel: "company-special-v9",
			wantCtx:   786_004,
		},
		{
			name:       "dated exact ID does not match undated family",
			requested:  "claude-sonnet-4-6-20260729",
			models:     []llm.ModelInfo{{ID: "claude-opus-4-6"}, {ID: "claude-sonnet-4-6", ContextWindow: 797_005}},
			wantReason: "unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			adapter := &pluginModelListAdapter{
				fakeAdapter: fakeAdapter{name: "anthropic"},
				models:      tc.models,
			}
			sess := newPluginModelSelectionSession(
				t,
				newAnthropicProfile("claude-opus-4-6"),
				adapter,
				"",
				nil,
			)

			got := sess.resolvePluginAgentModel(context.Background(), sess.currentProfile(), tc.requested)
			if got.reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.reason, tc.wantReason)
			}
			if tc.wantReason != "" {
				if got.profile != nil {
					t.Errorf("profile = %#v, want nil on failed membership", got.profile)
				}
				return
			}
			if got.profile == nil {
				t.Fatal("profile = nil, want successful custom model")
			}
			if got.profile.Model() != tc.wantModel {
				t.Errorf("model = %q, want %q", got.profile.Model(), tc.wantModel)
			}
			if got.profile.ContextWindowSize() != tc.wantCtx {
				t.Errorf("context window = %d, want advertised live metadata %d", got.profile.ContextWindowSize(), tc.wantCtx)
			}
		})
	}
}

func TestResolvePluginAgentModel_NormalizedMatchFreezesAdvertisedWireID(t *testing.T) {
	t.Parallel()

	reasoningOff := false
	base := resolveTestProfile(providercfg.InstanceConfig{
		Name:     "work",
		Type:     "openai",
		APIStyle: providercfg.StyleChatCompletions,
		Models: map[string]providercfg.ModelConfig{
			"gpt-5.3": {
				ContextWindow: 654_321,
				Reasoning:     &reasoningOff,
			},
		},
	}, "gpt-5.2")
	base = WithAllowedDecisions(base, []string{"keep_config"})
	wantCommunicate := subagentModelCommunicateDefinition(t, base)
	adapter := &pluginModelListAdapter{
		fakeAdapter: fakeAdapter{name: "work"},
		models: []llm.ModelInfo{
			// The launch profile's own model (gpt-5.2) must also be
			// live-enumerable: NewSession validates it (Task 3).
			{ID: "gpt-5.2"},
			{
				ID:                    "GpT-5.3",
				ContextWindow:         808_006,
				SupportsReasoning:     true,
				ReasoningEffortLevels: []string{"low", "high"},
			},
		},
	}
	sess := newPluginModelSelectionSession(
		t,
		base,
		adapter,
		"",
		nil,
	)

	got := sess.resolvePluginAgentModel(context.Background(), sess.currentProfile(), "GPT-5.3")
	if got.reason != "" {
		t.Fatalf("reason = %q, want success", got.reason)
	}
	if got.profile == nil {
		t.Fatal("profile = nil, want successful normalized match")
	}
	if got.profile.ID() != "work" {
		t.Errorf("profile ID = %q, want provider-local work", got.profile.ID())
	}
	if got.profile.Model() != "GpT-5.3" {
		t.Errorf("model = %q, want advertised wire ID %q", got.profile.Model(), "GpT-5.3")
	}
	if got.profile.ContextWindowSize() != 654_321 {
		t.Errorf(
			"context window = %d, want configured value 654321",
			got.profile.ContextWindowSize(),
		)
	}
	if got.profile.SupportsReasoning() {
		t.Error("SupportsReasoning = true, want configured reasoning=false")
	}
	if levels := got.profile.ReasoningEffortLevels(); len(levels) != 0 {
		t.Errorf("reasoning effort levels = %v, want none for configured reasoning=false", levels)
	}
	if gotCommunicate := subagentModelCommunicateDefinition(t, got.profile); !reflect.DeepEqual(gotCommunicate, wantCommunicate) {
		t.Errorf("communicate override changed after advertised-ID freeze")
	}
	if adapter.listCalls() != 1 {
		t.Errorf("ListModels calls = %d, want 1", adapter.listCalls())
	}
}

func TestResolvePluginAgentModel_AmbiguousCatalogAliasFailsClosed(t *testing.T) {
	t.Parallel()

	catalog := &llm.ModelCatalog{Models: []llm.ModelInfo{
		{ID: "model-a", Provider: "openai", Aliases: []string{"fast"}},
		{ID: "model-b", Provider: "openai", Aliases: []string{"fast"}},
	}}
	candidate, reason := resolvePluginAgentCatalogRef(NewOpenAIProfile("gpt-5.2"), catalog, "fast")
	if candidate != "" || reason != "ambiguous" {
		t.Fatalf("resolvePluginAgentCatalogRef = (%q, %q), want (empty, ambiguous)", candidate, reason)
	}
}

func TestSelectSubagentModel_AllowanceGuardPrecedesEnumeration(t *testing.T) {
	t.Parallel()

	adapter := &pluginModelListAdapter{
		fakeAdapter: fakeAdapter{name: "openai"},
		// The launch profile's own model (gpt-5.2) must also be
		// live-enumerable: NewSession validates it (Task 3).
		models: []llm.ModelInfo{{ID: "gpt-5.2"}, {ID: "gpt-5.3"}},
	}
	sess := newPluginModelSelectionSession(t, NewOpenAIProfile("gpt-5.2"), adapter, "gpt-5.3", nil)
	sess.mu.Lock()
	sess.delegationAllowance = 0
	sess.mu.Unlock()

	_, err := sess.selectSubagentModel(context.Background(), "", "reviewer")
	if err == nil || err.Error() != "delegation not permitted: your delegation_allowance is 0" {
		t.Fatalf("selectSubagentModel error = %v, want allowance guard error", err)
	}
	if adapter.listCalls() != 0 {
		t.Errorf("ListModels calls = %d, want 0", adapter.listCalls())
	}
}

// TestSelectSubagentModel_ExplicitModelRejectedWhenAbsentFromLiveList verifies
// Task 3's delegate-dispatch wiring: an explicit delegate model override that
// a successfully-enumerated live list doesn't contain fails the model
// selection (not just a warn-and-fallback), naming the requested model and a
// live alternative.
func TestSelectSubagentModel_ExplicitModelRejectedWhenAbsentFromLiveList(t *testing.T) {
	t.Parallel()

	adapter := &pluginModelListAdapter{
		fakeAdapter: fakeAdapter{name: "openai"},
		// The launch profile's own model (gpt-5.2) must be live-enumerable;
		// the requested override (gpt-9.9-does-not-exist) is deliberately
		// absent.
		models: []llm.ModelInfo{{ID: "gpt-5.2"}, {ID: "gpt-5.3"}},
	}
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.2")),
		withAdapter(adapter),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)

	_, err := sess.selectSubagentModel(context.Background(), "gpt-9.9-does-not-exist", "")
	if err == nil {
		t.Fatal("selectSubagentModel with an unavailable explicit model = nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "gpt-9.9-does-not-exist") {
		t.Fatalf("error = %q, want it to name the requested model", err.Error())
	}
	if !strings.Contains(err.Error(), "gpt-5.3") {
		t.Fatalf("error = %q, want it to name a live alternative %q", err.Error(), "gpt-5.3")
	}
}

// TestSelectSubagentModel_ExplicitModelEnumerationFailure_FailsOpen verifies
// that an explicit delegate model override still succeeds unvalidated when the
// live list can't be enumerated (fail-open, matching resolveModelSwitchTarget's
// existing SetModel behavior).
func TestSelectSubagentModel_ExplicitModelEnumerationFailure_FailsOpen(t *testing.T) {
	t.Parallel()

	adapter := &pluginModelListAdapter{
		fakeAdapter: fakeAdapter{name: "openai"},
		models:      []llm.ModelInfo{{ID: "gpt-5.2"}},
	}
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.2")),
		withAdapter(adapter),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)
	adapter.err = errors.New("models endpoint disabled")

	got, err := sess.selectSubagentModel(context.Background(), "gpt-9.9-unverifiable", "")
	if err != nil {
		t.Fatalf("selectSubagentModel with unenumerable instance should fail open, got error: %v", err)
	}
	if got.profile.Model() != "gpt-9.9-unverifiable" {
		t.Fatalf("profile model = %q, want unverified override %q", got.profile.Model(), "gpt-9.9-unverifiable")
	}
}

func newPluginModelSelectionSession(
	t *testing.T,
	base *provider.Profile,
	adapter *pluginModelListAdapter,
	pluginModel string,
	resolver func(string) (*provider.Profile, error),
) *Session {
	t.Helper()
	sess := newSession(t,
		withProfile(base),
		withAdapter(adapter),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			ResolveProfile:   resolver,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)
	adapter.resetListCalls()
	sess.pluginAgents = map[string]plugin.Agent{
		"reviewer": {
			Name:       "reviewer",
			Model:      pluginModel,
			PluginName: "test-plugin",
		},
	}
	return sess
}

func assertSelectedPluginModel(
	t *testing.T,
	got subagentModelSelection,
	wantRequested string,
	wantProfileID string,
	wantModel string,
	wantContextWindow int,
) {
	t.Helper()
	if got.requestedModel != wantRequested {
		t.Errorf("requestedModel = %q, want %q", got.requestedModel, wantRequested)
	}
	if got.profile == nil {
		t.Fatal("profile = nil")
	}
	if got.profile.ID() != wantProfileID || got.profile.Model() != wantModel {
		t.Errorf(
			"profile = %q/%q, want %q/%q",
			got.profile.ID(),
			got.profile.Model(),
			wantProfileID,
			wantModel,
		)
	}
	if got.profile.ContextWindowSize() != wantContextWindow {
		t.Errorf(
			"context window = %d, want advertised live metadata %d",
			got.profile.ContextWindowSize(),
			wantContextWindow,
		)
	}
	if got.warning != nil {
		t.Errorf("warning = %#v, want nil", got.warning)
	}
}

func assertPluginFallback(
	t *testing.T,
	got subagentModelSelection,
	wantRequested string,
	wantProfileID string,
	wantModel string,
	wantReason string,
) {
	t.Helper()
	if got.requestedModel != wantRequested {
		t.Errorf("requestedModel = %q, want %q", got.requestedModel, wantRequested)
	}
	if got.profile == nil {
		t.Fatal("profile = nil")
	}
	if got.profile.ID() != wantProfileID || got.profile.Model() != wantModel {
		t.Errorf(
			"profile = %q/%q, want %q/%q",
			got.profile.ID(),
			got.profile.Model(),
			wantProfileID,
			wantModel,
		)
	}
	if got.warning == nil {
		t.Fatal("warning = nil, want plugin fallback warning")
	}
	if !strings.Contains(got.warning.Message, wantReason) {
		t.Errorf("warning message = %q, want reason %q", got.warning.Message, wantReason)
	}
}

func subagentModelCommunicateDefinition(t *testing.T, profile *provider.Profile) llm.ToolDefinition {
	t.Helper()
	for _, def := range profile.ToolDefinitions() {
		if def.Name == "communicate" {
			return def
		}
	}
	t.Fatal("profile has no communicate definition")
	return llm.ToolDefinition{}
}

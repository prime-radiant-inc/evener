package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// pluginModelListAdapter is an override that serves a scripted live listing
// and counts the listing calls. Its rows settle membership; the model facts a
// selected profile carries come from the registry (spec §5).
type pluginModelListAdapter struct {
	fakeAdapter

	mu     sync.Mutex
	models []registry.Model
	err    error
	calls  int
}

func (a *pluginModelListAdapter) LiveModels(context.Context) ([]registry.Model, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return append([]registry.Model(nil), a.models...), a.err
}

// listedModels builds a scripted listing from bare model ids.
func listedModels(ids ...string) []registry.Model {
	models := make([]registry.Model, len(ids))
	for i, id := range ids {
		models[i] = registry.Model{ID: id}
	}
	return models
}

// modelRows declares instance rows for the hermetic test registry, so
// Registry.FindModel can find the ids a plugin agent asks for.
func modelRows(ids ...string) map[string]registry.Model {
	rows := make(map[string]registry.Model, len(ids))
	for _, id := range ids {
		rows[id] = registry.Model{}
	}
	return rows
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

func TestSelectSubagentModel_BuiltinExplorerUsesSelectedModel(t *testing.T) {
	t.Parallel()

	adapter := &pluginModelListAdapter{models: listedModels("gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.4-mini")}
	adapter.name = "openai"
	sess := newSession(t,
		withProfile(NewOpenAIProfile("gpt-5.6-luna")),
		withAdapter(adapter),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			testOnly:         testConfig{skipGitSnapshot: true},
		}),
	)
	agents, err := builtinAgents()
	if err != nil {
		t.Fatalf("builtinAgents: %v", err)
	}
	sess.pluginAgents = agents

	tests := []struct {
		name          string
		explicitModel string
		wantModel     string
	}{
		{name: "inherits parent", wantModel: "gpt-5.6-luna"},
		{name: "honors explicit model", explicitModel: "gpt-5.6-sol", wantModel: "gpt-5.6-sol"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			selected, err := sess.selectSubagentModel(context.Background(), tc.explicitModel, "explorer")
			if err != nil {
				t.Fatalf("selectSubagentModel: %v", err)
			}
			if selected.profile.Model() != tc.wantModel {
				t.Errorf("selected model = %q, want %q", selected.profile.Model(), tc.wantModel)
			}
			if selected.warning != nil {
				t.Errorf("unexpected model selection warning: %#v", selected.warning)
			}
		})
	}
}

func TestSelectSubagentModel_PluginAvailabilityPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		parentModel        string
		pluginModel        string
		explicitModel      string
		liveModels         []registry.Model
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
			liveModels:         listedModels("gpt-5.2", "gpt-5.3"),
			wantRequestedModel: "gpt-5.3",
			wantProfileID:      "openai",
			wantModel:          "gpt-5.3",
			wantWarning:        false,
			wantResolverCalls:  0,
			wantListCalls:      1,
		},
		{
			// gpt-4.1-nano is a catalog row the instance serves, so the ref
			// resolves and only the live listing can rule it out. The explicit
			// fallback model must itself pass membership, so gpt-5.3 is
			// included alongside gpt-5.2 here.
			name:               "unavailable plugin model uses explicit fallback",
			parentModel:        "gpt-5.2",
			pluginModel:        "gpt-4.1-nano",
			explicitModel:      "gpt-5.3",
			liveModels:         listedModels("gpt-5.2", "gpt-5.3"),
			wantRequestedModel: "gpt-5.3",
			wantProfileID:      "openai",
			wantModel:          "gpt-5.3",
			wantWarning:        true,
			wantReason:         "unavailable",
			wantResolverCalls:  0,
			wantListCalls:      2,
		},
		{
			// A model no instance serves is refused by the registry alone, so
			// nothing is listed for it at all.
			name:               "unservable plugin model is refused without listing",
			parentModel:        "gpt-5.2",
			pluginModel:        "gpt-9.9-does-not-exist",
			liveModels:         listedModels("gpt-5.2"),
			wantRequestedModel: "",
			wantProfileID:      "openai",
			wantModel:          "gpt-5.2",
			wantWarning:        true,
			wantReason:         "unavailable",
			wantResolverCalls:  0,
			wantListCalls:      0,
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
			// and the explicit override must pass membership here.
			name:               "inherit uses explicit model",
			parentModel:        "gpt-5.2",
			pluginModel:        "inherit",
			explicitModel:      "gpt-5.3",
			liveModels:         listedModels("gpt-5.2", "gpt-5.3"),
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
			liveModels:         listedModels("gpt-5.2"),
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
			liveModels:         listedModels("gpt-5.2"),
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

			adapter := &pluginModelListAdapter{models: tc.liveModels, err: tc.listErr}
			adapter.name = "openai"
			client := registryClient(t, map[string]registry.Provider{
				"openai": {Base: "openai", APIKey: "k", Models: map[string]registry.Model{
					"gpt-5.3": {Caps: registry.Caps{ContextWindow: new(753_001)}},
				}},
			}, adapter)
			resolverCalls := 0
			sess := newSession(t,
				withProfile(NewOpenAIProfile(tc.parentModel)),
				withClient(client),
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
				t.Errorf("listing calls = %d, want %d", adapter.listCalls(), tc.wantListCalls)
			}
			if tc.name == "available exact plugin model wins" && got.profile.ContextWindowSize() != 753_001 {
				t.Errorf("profile context window = %d, want the registry's 753001", got.profile.ContextWindowSize())
			}
		})
	}
}

// TestSelectSubagentModel_BareIDPrefersTheSessionInstance pins spec §7.5's
// ranking rule: an id the session's own instance serves stays there even when
// a higher-ranked instance serves it too, and the selected profile carries
// that instance's registry facts.
func TestSelectSubagentModel_BareIDPrefersTheSessionInstance(t *testing.T) {
	t.Parallel()

	base := namedInstanceProfile("work", "anthropic", "claude-opus-4-6")
	adapter := &pluginModelListAdapter{models: listedModels("claude-opus-4-6", "claude-sonnet-4-6")}
	adapter.name = "work"
	client := registryClient(t, map[string]registry.Provider{
		// "elsewhere" outranks "work" by name and serves the same id; the
		// session's own instance must still win.
		"elsewhere": {Base: "anthropic", APIKey: "k", Models: modelRows("claude-sonnet-4-6")},
		"work": {Base: "anthropic", APIKey: "k", Models: map[string]registry.Model{
			"claude-opus-4-6":   {},
			"claude-sonnet-4-6": {Caps: registry.Caps{ContextWindow: new(764_002), EffortValues: []string{"low", "high"}, Reasoning: new(true)}},
		}},
	}, adapter)
	sess := newPluginModelSelectionSession(t, base, client, adapter, "claude-sonnet-4-6", nil)

	got, err := sess.selectSubagentModel(context.Background(), "", "reviewer")
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	assertSelectedPluginModel(t, got, "claude-sonnet-4-6", "work", "claude-sonnet-4-6", 764_002)
	if got.profile.Surface() != registry.SurfaceAnthropic {
		t.Errorf("surface = %q, want anthropic", got.profile.Surface())
	}
	if levels := strings.Join(got.profile.ReasoningEffortLevels(), ","); levels != "low,high" {
		t.Errorf("reasoning effort levels = %q, want the registry's low,high", levels)
	}
	if adapter.listCalls() != 1 {
		t.Errorf("listing calls = %d, want 1", adapter.listCalls())
	}
}

// TestSelectSubagentModel_UnservedIDIsUnavailableWithoutListing pins that a
// model no instance serves costs no provider round trip: the registry alone
// refuses it.
func TestSelectSubagentModel_UnservedIDIsUnavailableWithoutListing(t *testing.T) {
	t.Parallel()

	adapter := &pluginModelListAdapter{models: listedModels("k3")}
	adapter.name = "kimi-anthropic"
	client := registryClient(t, map[string]registry.Provider{
		"kimi-anthropic": {Base: "moonshotai", APIKey: "k", Models: modelRows("k3")},
	}, adapter)
	base := resolveTestProfile("kimi-anthropic", map[string]registry.Provider{
		"kimi-anthropic": {Base: "moonshotai", APIKey: "k", Models: modelRows("k3")},
	}, "k3")
	sess := newPluginModelSelectionSession(t, base, client, adapter, "sonnet", nil)

	got, err := sess.selectSubagentModel(context.Background(), "k3", "reviewer")
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	assertPluginFallback(t, got, "k3", "kimi-anthropic", "k3", "unavailable")
	// The plugin model ("sonnet") is refused by the registry without any
	// listing; the one call here is the explicit override "k3"'s membership
	// validation (resolveModelSwitchTarget).
	if adapter.listCalls() != 1 {
		t.Errorf("listing calls = %d, want 1", adapter.listCalls())
	}
}

// TestSelectSubagentModel_QualifiedPluginRefUsesTheSessionResolver pins spec
// §7.5: a plugin ref naming another instance resolves through the session's
// resolver, and the cross-instance profile keeps the parent's communicate
// override.
func TestSelectSubagentModel_QualifiedPluginRefUsesTheSessionResolver(t *testing.T) {
	t.Parallel()

	resolverCalls := 0
	base := WithAllowedDecisions(newKimiAnthropicProfile("k3"), []string{"keep_config"})
	wantCommunicate := subagentModelCommunicateDefinition(t, base)
	kimi := &pluginModelListAdapter{models: listedModels("k3")}
	kimi.name = "kimi-anthropic"
	anthropic := &pluginModelListAdapter{models: listedModels("claude-sonnet-4-6")}
	anthropic.name = "anthropic"
	client := registryClient(t, map[string]registry.Provider{
		"kimi-anthropic": {Base: "moonshotai", APIKey: "k", Models: modelRows("k3")},
		"anthropic": {Base: "anthropic", APIKey: "k", Models: map[string]registry.Model{
			"claude-sonnet-4-6": {Caps: registry.Caps{ContextWindow: new(775_003)}},
		}},
	}, kimi, anthropic)
	sess := newPluginModelSelectionSession(
		t,
		base,
		client,
		kimi,
		"anthropic/claude-sonnet-4-6",
		func(string) (*provider.Profile, error) {
			resolverCalls++
			return newAnthropicProfile("claude-sonnet-4-6"), nil
		},
	)
	// The startup snapshot lists every instance; count only what the plugin
	// selection itself asks the target for.
	anthropic.resetListCalls()

	got, err := sess.selectSubagentModel(context.Background(), "", "reviewer")
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	assertSelectedPluginModel(t, got, "anthropic/claude-sonnet-4-6", "anthropic", "claude-sonnet-4-6", 775_003)
	if resolverCalls != 1 {
		t.Errorf("resolver calls = %d, want 1", resolverCalls)
	}
	if gotCommunicate := subagentModelCommunicateDefinition(t, got.profile); !reflect.DeepEqual(gotCommunicate, wantCommunicate) {
		t.Error("cross-instance selection dropped the parent's communicate override")
	}
	if anthropic.listCalls() != 1 {
		t.Errorf("target instance listing calls = %d, want 1", anthropic.listCalls())
	}
}

// TestSelectSubagentModel_UpstreamNamespaceRefKeepsItsSlash pins that a
// slashed id whose prefix is not an instance stays a model id: an
// OpenRouter-style upstream namespace reaches the provider verbatim.
func TestSelectSubagentModel_UpstreamNamespaceRefKeepsItsSlash(t *testing.T) {
	t.Parallel()

	orInstance := map[string]registry.Provider{
		"openrouter-anthropic": {Base: "openrouter", Protocol: registry.ProtocolAnthropic, Surface: registry.SurfaceAnthropic, APIKey: "k", Models: map[string]registry.Model{
			"anthropic/claude-opus-4-6":   {},
			"anthropic/claude-sonnet-4-6": {Caps: registry.Caps{ContextWindow: new(775_003)}},
		}},
	}
	base := resolveTestProfile("openrouter-anthropic", orInstance, "anthropic/claude-opus-4-6")
	adapter := &pluginModelListAdapter{models: listedModels("anthropic/claude-opus-4-6", "anthropic/claude-sonnet-4-6")}
	adapter.name = "openrouter-anthropic"
	client := registryClient(t, orInstance, adapter)
	sess := newPluginModelSelectionSession(t, base, client, adapter, "anthropic/claude-sonnet-4-6", nil)

	got, err := sess.selectSubagentModel(context.Background(), "", "reviewer")
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	assertSelectedPluginModel(
		t,
		got,
		"anthropic/claude-sonnet-4-6",
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
		rows       map[string]registry.Model
		models     []registry.Model
		wantModel  string
		wantCtx    int
		wantReason string
	}{
		{
			name:      "an instance's own custom row is accepted when advertised",
			requested: "company-special-v9",
			rows: map[string]registry.Model{
				"claude-opus-4-6":    {},
				"company-special-v9": {Caps: registry.Caps{ContextWindow: new(786_004)}},
			},
			// The launch profile's own model (claude-opus-4-6) must also pass
			// membership: NewSession validates it.
			models:    listedModels("claude-opus-4-6", "company-special-v9"),
			wantModel: "company-special-v9",
			wantCtx:   786_004,
		},
		{
			name:       "dated exact ID does not match undated family",
			requested:  "claude-sonnet-4-6-20260729",
			rows:       modelRows("claude-opus-4-6", "claude-sonnet-4-6"),
			models:     listedModels("claude-opus-4-6", "claude-sonnet-4-6"),
			wantReason: "unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			adapter := &pluginModelListAdapter{models: tc.models}
			adapter.name = "anthropic"
			client := registryClient(t, map[string]registry.Provider{
				"anthropic": {Base: "anthropic", APIKey: "k", Models: tc.rows},
			}, adapter)
			sess := newPluginModelSelectionSession(
				t,
				newAnthropicProfile("claude-opus-4-6"),
				client,
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
				t.Errorf("context window = %d, want the registry's %d", got.profile.ContextWindowSize(), tc.wantCtx)
			}
		})
	}
}

// TestResolvePluginAgentModel_CarriesRegistryFactsAndKeepsCommunicateOverride
// pins the overlay half of the plugin path: the selected profile takes the
// registry's caps for the chosen row — including a reasoning = false that
// clears the effort ladder — while the parent's communicate override survives.
func TestResolvePluginAgentModel_CarriesRegistryFactsAndKeepsCommunicateOverride(t *testing.T) {
	t.Parallel()

	base := WithAllowedDecisions(namedInstanceProfile("work", "openai", "gpt-5.2"), []string{"keep_config"})
	wantCommunicate := subagentModelCommunicateDefinition(t, base)
	adapter := &pluginModelListAdapter{models: listedModels("gpt-5.2", "gpt-5.3")}
	adapter.name = "work"
	client := registryClient(t, map[string]registry.Provider{
		"work": {Base: "openai", APIKey: "k", Models: map[string]registry.Model{
			"gpt-5.3": {Caps: registry.Caps{ContextWindow: new(654_321), Reasoning: new(false)}},
		}},
	}, adapter)
	sess := newPluginModelSelectionSession(t, base, client, adapter, "", nil)

	got := sess.resolvePluginAgentModel(context.Background(), sess.currentProfile(), "gpt-5.3")
	if got.reason != "" {
		t.Fatalf("reason = %q, want success", got.reason)
	}
	if got.profile == nil {
		t.Fatal("profile = nil, want the selected row")
	}
	if got.profile.ID() != "work" || got.profile.Model() != "gpt-5.3" {
		t.Errorf("profile = %q/%q, want work/gpt-5.3", got.profile.ID(), got.profile.Model())
	}
	if got.profile.ContextWindowSize() != 654_321 {
		t.Errorf("context window = %d, want the registry's 654321", got.profile.ContextWindowSize())
	}
	if got.profile.SupportsReasoning() {
		t.Error("SupportsReasoning = true, want the registry's reasoning = false")
	}
	if levels := got.profile.ReasoningEffortLevels(); len(levels) != 0 {
		t.Errorf("reasoning effort levels = %v, want none for reasoning = false", levels)
	}
	if gotCommunicate := subagentModelCommunicateDefinition(t, got.profile); !reflect.DeepEqual(gotCommunicate, wantCommunicate) {
		t.Error("communicate override changed after the plugin selection")
	}
	if adapter.listCalls() != 1 {
		t.Errorf("listing calls = %d, want 1", adapter.listCalls())
	}
}

// TestResolvePluginAgentRef covers spec §7.5's four outcomes for a plugin
// agent's model string, against a registry that knows two instances.
func TestResolvePluginAgentRef(t *testing.T) {
	t.Parallel()

	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"alpha": {Base: "openai", APIKey: "k", Models: modelRows("shared-model", "alpha-only")},
			"work":  {Base: "openai", APIKey: "k", Models: modelRows("shared-model", "work-only")},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	base := namedInstanceProfile("work", "openai", "shared-model")

	tests := []struct {
		name       string
		requested  string
		wantRef    string
		wantReason string
	}{
		{name: "qualified ref naming an instance", requested: "alpha/alpha-only", wantRef: "alpha/alpha-only"},
		{name: "bare id the session instance serves", requested: "shared-model", wantRef: "shared-model"},
		{name: "bare id only another instance serves", requested: "alpha-only", wantRef: "alpha/alpha-only"},
		{name: "id nobody serves", requested: "nowhere-model", wantReason: "unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, reason := resolvePluginAgentRef(r, base, tc.requested)
			if ref != tc.wantRef || reason != tc.wantReason {
				t.Fatalf("resolvePluginAgentRef(%q) = (%q, %q), want (%q, %q)", tc.requested, ref, reason, tc.wantRef, tc.wantReason)
			}
		})
	}
}

func TestSelectSubagentModel_AllowanceGuardPrecedesEnumeration(t *testing.T) {
	t.Parallel()

	// The launch profile's own model (gpt-5.2) must also pass membership.
	adapter := &pluginModelListAdapter{models: listedModels("gpt-5.2", "gpt-5.3")}
	adapter.name = "openai"
	client := registryClient(t, map[string]registry.Provider{
		"openai": {Base: "openai", APIKey: "k", Models: modelRows("gpt-5.3")},
	}, adapter)
	sess := newPluginModelSelectionSession(t, NewOpenAIProfile("gpt-5.2"), client, adapter, "gpt-5.3", nil)
	sess.mu.Lock()
	sess.delegationAllowance = 0
	sess.mu.Unlock()

	_, err := sess.selectSubagentModel(context.Background(), "", "reviewer")
	if err == nil || err.Error() != "delegation not permitted: your delegation_allowance is 0" {
		t.Fatalf("selectSubagentModel error = %v, want allowance guard error", err)
	}
	if adapter.listCalls() != 0 {
		t.Errorf("listing calls = %d, want 0", adapter.listCalls())
	}
}

// TestSelectSubagentModel_ExplicitModelRejectedWhenAbsentFromLiveList verifies
// the delegate-dispatch wiring: an explicit delegate model override that a
// successfully-fetched live listing doesn't contain fails the model selection
// (not just a warn-and-fallback), naming the requested model and a live
// alternative.
func TestSelectSubagentModel_ExplicitModelRejectedWhenAbsentFromLiveList(t *testing.T) {
	t.Parallel()

	// The launch profile's own model (gpt-5.2) must pass membership; the
	// requested override (gpt-9.9-does-not-exist) is deliberately absent.
	adapter := &pluginModelListAdapter{models: listedModels("gpt-5.2", "gpt-5.3")}
	adapter.name = "openai"
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
// instance can't be listed (fail-open, matching resolveModelSwitchTarget's
// SetModel behavior).
func TestSelectSubagentModel_ExplicitModelEnumerationFailure_FailsOpen(t *testing.T) {
	t.Parallel()

	adapter := &pluginModelListAdapter{models: listedModels("gpt-5.2")}
	adapter.name = "openai"
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
		t.Fatalf("selectSubagentModel with an unlistable instance should fail open, got error: %v", err)
	}
	if got.profile.Model() != "gpt-9.9-unverifiable" {
		t.Fatalf("profile model = %q, want unverified override %q", got.profile.Model(), "gpt-9.9-unverifiable")
	}
}

func newPluginModelSelectionSession(
	t *testing.T,
	base *provider.Profile,
	client *llm.Client,
	adapter *pluginModelListAdapter,
	pluginModel string,
	resolver func(string) (*provider.Profile, error),
) *Session {
	t.Helper()
	sess := newSession(t,
		withProfile(base),
		withClient(client),
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
			"context window = %d, want the registry's %d",
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

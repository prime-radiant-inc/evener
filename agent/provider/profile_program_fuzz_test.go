//go:build serffuzz

package provider

import (
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// FuzzProviderProfilesProgram resolves every supported provider family from
// in-memory config, then drives the real profile decorator and model-switch
// APIs. It never constructs a transport or contacts a provider.
func FuzzProviderProfilesProgram(f *testing.F) {
	f.Add("next-model", 131072, true)
	f.Add("anthropic/next", 0, false)

	f.Fuzz(func(t *testing.T, next string, window int, webSearch bool) {
		next = strings.TrimSpace(next)
		if next == "" {
			next = "next-model"
		}
		window = int(uint(window)%1_000_000) + 1

		for _, tc := range providerProfileProgramCases() {
			cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{tc.instance}}
			profile, err := ResolveProfileFromConfig(cfg, tc.instance.Name+"/"+tc.model)
			if err != nil || profile == nil {
				t.Fatalf("ResolveProfileFromConfig(%s/%s) = %#v, %v", tc.instance.Name, tc.model, profile, err)
			}
			if profile.ID() != tc.instance.Name || profile.BehaviorTag() != tc.tag || profile.Model() != tc.model {
				t.Fatalf("profile identity = %q/%q/%q, want %q/%q/%q", profile.ID(), profile.BehaviorTag(), profile.Model(), tc.instance.Name, tc.tag, tc.model)
			}
			assertProviderProfileCopies(t, profile)
			assertProviderProfileDecorators(t, profile, next, window, webSearch)
		}
		assertProviderPurePrograms(t, next)
		assertProviderResidualBranches(t)

		if got, err := ResolveProfileFromConfig(providercfg.Config{}, "missing/model"); got != nil || err == nil {
			t.Fatalf("missing provider = %#v, %v", got, err)
		}
		if got, err := ResolveProfileFromConfig(providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "bad", Type: "unsupported"}}}, "bad/model"); got != nil || err == nil {
			t.Fatalf("unsupported provider = %#v, %v", got, err)
		}
		for _, invalid := range []string{"", "no-slash", "/model", "instance/"} {
			if got, err := ResolveProfileFromConfig(providercfg.Config{}, invalid); got != nil || err == nil {
				t.Fatalf("invalid ref %q = %#v, %v", invalid, got, err)
			}
		}
	})
}

func assertProviderResidualBranches(t *testing.T) {
	t.Helper()
	p := NewOpenAIProfile("gpt-5")
	if p.CheapModelRefString() != "" || p.CrossProviderRef("bare-model") {
		t.Fatal("empty cheap model or bare model ref classified incorrectly")
	}
	if p.WithModel(" ").Model() != p.Model() {
		t.Fatal("empty model did not preserve the active model")
	}
	if restampInstanceIdentity(nil, "tag", "id") != nil {
		t.Fatal("nil identity restamp must stay nil")
	}
	if (*Profile)(nil).WithCommunicateOverridesFrom(p) != nil || p.WithCommunicateOverridesFrom(nil) != p {
		t.Fatal("nil communicate override guard failed")
	}
	withoutCommunicate := &Profile{toolDefs: []llm.ToolDefinition{{Name: "shell"}}}
	if p.WithCommunicateOverridesFrom(withoutCommunicate) != p {
		t.Fatal("missing source communicate tool changed profile")
	}
	target := &Profile{toolDefs: []llm.ToolDefinition{{Name: "shell"}}}
	target.WithCommunicateOverridesFrom(p)
	if len(target.toolDefs) != 2 || target.toolDefs[1].Name != "communicate" {
		t.Fatal("communicate override was not appended")
	}
	lookup := func(key string) *llm.ModelInfo {
		switch key {
		case "anthropic/model":
			return &llm.ModelInfo{ReasoningEffortLevels: []string{"high"}}
		case "model":
			return &llm.ModelInfo{ContextWindow: 42, ReasoningEffortLevels: []string{"low"}}
		default:
			return nil
		}
	}
	ctx, efforts := resolveOpenRouterAnthropicCtxAndEfforts(lookup, "anthropic/model", 1, nil)
	if ctx != 42 || !reflect.DeepEqual(efforts, []string{"high"}) {
		t.Fatalf("openrouter fallback = %d/%v", ctx, efforts)
	}
	ctx, efforts = resolveOpenRouterAnthropicCtxAndEfforts(func(key string) *llm.ModelInfo {
		if key == "openrouter/anthropic/model" {
			return &llm.ModelInfo{}
		}
		if key == "model" {
			return &llm.ModelInfo{ReasoningEffortLevels: []string{"low"}}
		}
		return nil
	}, "anthropic/model", 1, nil)
	if ctx != 1 || !reflect.DeepEqual(efforts, []string{"low"}) {
		t.Fatalf("openrouter stripped effort fallback = %d/%v", ctx, efforts)
	}
}

type providerProfileProgramCase struct {
	instance providercfg.InstanceConfig
	model    string
	tag      string
}

func providerProfileProgramCases() []providerProfileProgramCase {
	return []providerProfileProgramCase{
		{providercfg.InstanceConfig{Name: "open", Type: "openai", APIStyle: providercfg.StyleResponses}, "gpt-5", "openai"},
		{providercfg.InstanceConfig{Name: "chat", Type: "openai", APIStyle: providercfg.StyleChatCompletions}, "gpt-5", "openai-compatible"},
		{providercfg.InstanceConfig{Name: "anth", Type: "anthropic"}, "claude-sonnet", "anthropic"},
		{providercfg.InstanceConfig{Name: "gem", Type: "google"}, "gemini-2.5-pro", "google"},
		{providercfg.InstanceConfig{Name: "mini", Type: "minimax"}, "minimax/m2.7", "minimax"},
		{providercfg.InstanceConfig{Name: "kima", Type: "kimi-anthropic"}, "kimi-for-coding", "kimi-anthropic"},
		{providercfg.InstanceConfig{Name: "ora", Type: "openrouter-anthropic"}, "anthropic/claude-sonnet", "openrouter-anthropic"},
		{providercfg.InstanceConfig{Name: "kimi", Type: "kimi"}, "kimi-k2", "kimi"},
		{providercfg.InstanceConfig{Name: "glm", Type: "glm"}, "glm-4", "glm"},
		{providercfg.InstanceConfig{Name: "router", Type: "openrouter"}, "minimax/minimax-m2.7", "openrouter"},
		{providercfg.InstanceConfig{Name: "local", Type: "ollama"}, "llama3.1", "ollama"},
	}
}

func assertProviderProfileCopies(t *testing.T, profile *Profile) {
	t.Helper()
	_ = profile.SupportsParallelToolCalls()
	_ = profile.ProviderOptions()
	_ = profile.SupportsReasoning()
	_ = profile.SupportsStreaming()
	_ = profile.DefaultCommandTimeoutMS()
	_ = profile.KnowledgeCutoff()
	_ = profile.CheapModel()
	if profile.ConfiguredCheapModel() != "" {
		t.Fatalf("new %s profile unexpectedly has configured cheap model", profile.ID())
	}
	_ = profile.CatalogEffortFallbackEligible()
	_ = profile.EffortLevelsConfigured()
	defs := profile.ToolDefinitions()
	if len(defs) == 0 {
		t.Fatalf("%s has no tool definitions", profile.ID())
	}
	originalName := defs[0].Name
	defs[0].Name = "mutated"
	if again := profile.ToolDefinitions(); again[0].Name != originalName {
		t.Fatalf("%s ToolDefinitions exposed its slice", profile.ID())
	}

	docs := profile.ProjectDocFiles()
	if len(docs) > 0 {
		original := docs[0]
		docs[0] = "mutated"
		if again := profile.ProjectDocFiles(); again[0] != original {
			t.Fatalf("%s ProjectDocFiles exposed its slice", profile.ID())
		}
	}
	efforts := profile.ReasoningEffortLevels()
	if len(efforts) > 0 {
		original := efforts[0]
		efforts[0] = "mutated"
		if again := profile.ReasoningEffortLevels(); again[0] != original {
			t.Fatalf("%s ReasoningEffortLevels exposed its slice", profile.ID())
		}
	}
	if names := profile.ToolNameMap(); names != nil {
		for key, value := range names {
			names[key] = "mutated"
			if again := profile.ToolNameMap(); again[key] != value {
				t.Fatalf("%s ToolNameMap exposed its map", profile.ID())
			}
			break
		}
	}
}

func assertProviderProfileDecorators(t *testing.T, profile *Profile, next string, window int, webSearch bool) {
	t.Helper()
	if WithProviderID(nil, "x") != nil || WithCheapModel(nil, "x") != nil || WithContextWindow(nil, window) != nil || (*Profile)(nil).WithLiveModelInfo(llm.ModelInfo{}) != nil {
		t.Fatal("nil profile decorators must stay nil")
	}
	if WithProviderID(profile, " ") != profile || WithCheapModel(profile, " ") != profile || WithContextWindow(profile, 0) != profile {
		t.Fatal("empty/no-op decorator did not preserve its profile")
	}

	renamed := WithProviderID(profile, "  renamed  ")
	if renamed.ID() != "renamed" || renamed.BehaviorTag() != profile.BehaviorTag() || profile.ID() == "renamed" {
		t.Fatalf("WithProviderID identity = %q/%q (base %q)", renamed.ID(), renamed.BehaviorTag(), profile.ID())
	}
	bareCheap := WithCheapModel(profile, "  cheap-model  ")
	if bareCheap.CheapProvider() != profile.ID() || bareCheap.CheapModel() != "cheap-model" || bareCheap.CheapModelRefString() != "cheap-model" {
		t.Fatalf("bare WithCheapModel = %q/%q/%q", bareCheap.CheapProvider(), bareCheap.CheapModel(), bareCheap.CheapModelRefString())
	}
	qualifiedCheap := WithCheapModel(profile, "other/"+next)
	if provider, model := qualifiedCheap.CheapModelRef(); provider != "other" || model != next || qualifiedCheap.CheapModelRefString() != "other/"+next {
		t.Fatalf("qualified WithCheapModel = %q/%q/%q", provider, model, qualifiedCheap.CheapModelRefString())
	}
	baseWindow := profile.ContextWindowSize()
	baseWebSearch := profile.SupportsWebSearch()
	baseEfforts := profile.ReasoningEffortLevels()
	baseTaskListEfforts := providerProgramTaskListEfforts(profile.ToolDefinitions())
	resized := WithContextWindow(profile, window)
	if resized.ContextWindowSize() != window || profile.ContextWindowSize() != baseWindow {
		t.Fatalf("WithContextWindow = %d (base %d)", resized.ContextWindowSize(), profile.ContextWindowSize())
	}

	live := profile.WithLiveModelInfo(llm.ModelInfo{
		ContextWindow:         window,
		ReasoningEffortLevels: []string{"low", "high"},
		SupportsReasoning:     true,
		SupportsWebSearch:     &webSearch,
	})
	if live == profile || live.ContextWindowSize() != window || live.SupportsWebSearch() != webSearch {
		t.Fatalf("WithLiveModelInfo = %#v", live)
	}
	wantLiveEfforts := []string{"low", "high"}
	if got := live.ReasoningEffortLevels(); !reflect.DeepEqual(got, wantLiveEfforts) {
		t.Fatalf("WithLiveModelInfo effort levels = %v, want %v", got, wantLiveEfforts)
	}
	if got := providerProgramTaskListEfforts(live.ToolDefinitions()); !reflect.DeepEqual(got, wantLiveEfforts) {
		t.Fatalf("WithLiveModelInfo task_list effort enum = %v, want %v", got, wantLiveEfforts)
	}
	if profile.ContextWindowSize() != baseWindow || profile.SupportsWebSearch() != baseWebSearch || !reflect.DeepEqual(profile.ReasoningEffortLevels(), baseEfforts) || !reflect.DeepEqual(providerProgramTaskListEfforts(profile.ToolDefinitions()), baseTaskListEfforts) {
		t.Fatal("WithLiveModelInfo mutated its base profile")
	}

	selfRef := profile.ID() + "/" + next
	if profile.BehaviorTag() != "minimax" && profile.CrossProviderRef(selfRef) {
		t.Fatalf("self ref %q classified cross-provider", selfRef)
	}
	if !profile.CrossProviderRef("other/"+next) && profile.BehaviorTag() != "openrouter" && profile.BehaviorTag() != "openrouter-anthropic" {
		t.Fatalf("foreign ref classified same-provider for %s", profile.BehaviorTag())
	}
	changed := profile.WithModel(selfRef)
	if changed == nil || changed.ID() != profile.ID() || changed.BehaviorTag() != profile.BehaviorTag() {
		t.Fatalf("WithModel identity = %#v", changed)
	}
	wantModel := next
	if profile.BehaviorTag() == "minimax" {
		wantModel = selfRef
	}
	if changed.Model() != wantModel {
		t.Fatalf("WithModel(%q) = %q, want %q", selfRef, changed.Model(), wantModel)
	}
}

func providerProgramTaskListEfforts(defs []llm.ToolDefinition) []string {
	for _, def := range defs {
		if def.Name != "task_list" {
			continue
		}
		properties, _ := def.Parameters["properties"].(map[string]any)
		tasks, _ := properties["tasks"].(map[string]any)
		items, _ := tasks["items"].(map[string]any)
		taskProperties, _ := items["properties"].(map[string]any)
		reasoning, _ := taskProperties["reasoning_effort"].(map[string]any)
		return append([]string(nil), toStringSlice(reasoning["enum"])...)
	}
	return nil
}

func assertProviderPurePrograms(t *testing.T, next string) {
	t.Helper()
	zero := buildBaseProfile(profileSpec{})
	if zero.DefaultCommandTimeoutMS() != 120_000 {
		t.Fatalf("zero profile timeout = %d", zero.DefaultCommandTimeoutMS())
	}
	var nilProfile *Profile
	if nilProfile.ConfiguredCheapModel() != "" || nilProfile.CheapProvider() != "" || nilProfile.CheapModelRefString() != "" {
		t.Fatal("nil profile cheap-model accessors must return empty values")
	}
	input := map[string]any{
		"map":       map[string]any{"leaf": next},
		"map_slice": []map[string]any{{"leaf": next}},
		"any_slice": []any{map[string]any{"leaf": next}},
		"strings":   []string{next},
		"scalar":    next,
	}
	copy := cloneAnyMap(input)
	copy["map"].(map[string]any)["leaf"] = "mutated"
	copy["map_slice"].([]map[string]any)[0]["leaf"] = "mutated"
	copy["any_slice"].([]any)[0].(map[string]any)["leaf"] = "mutated"
	copy["strings"].([]string)[0] = "mutated"
	if input["map"].(map[string]any)["leaf"] != next || input["map_slice"].([]map[string]any)[0]["leaf"] != next || input["any_slice"].([]any)[0].(map[string]any)["leaf"] != next || input["strings"].([]string)[0] != next {
		t.Fatal("cloneAnyMap exposed nested mutable state")
	}
	if cloneStringSlice(nil) != nil || cloneStringMap(nil) != nil {
		t.Fatal("nil clone helpers must return nil")
	}

	reasoningOff := false
	configured := newOpenAICompatProfile("local", "configured", 0, map[string]providercfg.ModelConfig{
		"configured": {ContextWindow: 77_777, Reasoning: &reasoningOff},
	})
	if configured.ContextWindowSize() != 77_777 || configured.SupportsReasoning() || !configured.EffortLevelsConfigured() || configured.CatalogEffortFallbackEligible() {
		t.Fatalf("configured model precedence = %#v", configured)
	}
	live := configured.WithLiveModelInfo(llm.ModelInfo{ContextWindow: 1, ReasoningEffortLevels: []string{"high"}, SupportsReasoning: true})
	if live.ContextWindowSize() != 77_777 || live.SupportsReasoning() || len(live.ReasoningEffortLevels()) != 0 {
		t.Fatalf("live metadata overrode configured model intent = %#v", live)
	}
	configuredLevels := newOpenAICompatProfile("local", "levels", 0, map[string]providercfg.ModelConfig{
		"levels": {ThinkingLevels: map[string]string{"low": "low", "high": "high"}},
	})
	if !configuredLevels.EffortLevelsConfigured() || configuredLevels.CatalogEffortFallbackEligible() || len(configuredLevels.ReasoningEffortLevels()) != 2 {
		t.Fatalf("configured thinking levels = %#v", configuredLevels)
	}

	anthropic := newAnthropicProfile("claude" + anthropicSuffix1M)
	if anthropic.ContextWindowSize() != 1_000_000 || anthropic.WithModel("anthropic/"+next).Model() != next {
		t.Fatalf("anthropic model rebuild = %#v", anthropic)
	}
	routerAnthropic := newOpenRouterAnthropicProfile("anthropic/claude" + anthropicSuffix1M)
	if routerAnthropic.ContextWindowSize() != 1_000_000 {
		t.Fatalf("openrouter anthropic 1M profile = %#v", routerAnthropic)
	}
	router := newOpenAICompatProfile("openrouter", "anthropic/claude", 0, nil)
	if got := router.WithModel("anthropic/" + next).Model(); got != "anthropic/"+next {
		t.Fatalf("openrouter upstream model prefix was not preserved: %q", got)
	}
	if profile := NewOpenAIProfile("base"); profile.WithCommunicateOverridesFrom(nil) != profile || profile.withCheapModelFrom(nil) != profile || (*Profile)(nil).WithCommunicateOverridesFrom(profile) != nil || (*Profile)(nil).withCheapModelFrom(profile) != nil {
		t.Fatal("nil profile carry-forward helpers changed identity")
	}

	lookup := func(entries map[string]*llm.ModelInfo) func(string) *llm.ModelInfo {
		return func(key string) *llm.ModelInfo { return entries[key] }
	}
	if got := resolveOpenAICompatCatalogModel(lookup(map[string]*llm.ModelInfo{
		"openrouter/next": {ID: "tagged"}, "next": {ID: "bare"},
	}), "openrouter", "next"); got == nil || got.ID != "tagged" {
		t.Fatalf("tagged catalog lookup = %#v", got)
	}
	if got := resolveOpenAICompatCatalogModel(lookup(map[string]*llm.ModelInfo{"next": {ID: "bare"}}), "kimi", "next"); got == nil || got.ID != "bare" {
		t.Fatalf("bare catalog lookup = %#v", got)
	}
	if got := resolveOpenAICompatCatalogModel(lookup(map[string]*llm.ModelInfo{
		"claude": {ID: "bare"}, "ollama/claude": {ID: "base"},
	}), "ollama", "claude:tag"); got == nil || got.ID != "base" {
		t.Fatalf("ollama tagged fallback = %#v", got)
	}
	if got := resolveOpenAICompatCatalogModel(lookup(nil), "ollama", "missing"); got != nil {
		t.Fatalf("missing catalog lookup = %#v", got)
	}

	for _, tc := range []struct {
		tag, instance, prefix string
		want                  prefixAction
	}{
		{"openrouter", "router", "router", prefixActionStrip},
		{"openrouter", "router", "anthropic", prefixActionKeep},
		{"openrouter", "router", "ollama", prefixActionSwitch},
		{"minimax", "mini", "minimax", prefixActionKeep},
		{"minimax", "mini", "other", prefixActionSwitch},
		{"openai", "open", "open", prefixActionStrip},
		{"openai", "open", "other", prefixActionSwitch},
	} {
		if got := decidePrefixAction(tc.tag, tc.instance, tc.prefix); got != tc.want {
			t.Fatalf("decidePrefixAction(%q, %q, %q) = %v, want %v", tc.tag, tc.instance, tc.prefix, got, tc.want)
		}
	}
}

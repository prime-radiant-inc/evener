//go:build evenerfuzz

package provider

import (
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// FuzzProviderProfilesProgram resolves every surface the agent drives from a
// hermetic registry, then exercises the real profile decorator and
// model-switch APIs. It never constructs a transport or contacts a provider.
func FuzzProviderProfilesProgram(f *testing.F) {
	f.Add("next-model", 131072)
	f.Add("anthropic/next", 0)

	f.Fuzz(func(t *testing.T, next string, window int) {
		next = strings.TrimSpace(next)
		if next == "" || strings.HasPrefix(next, "/") || strings.HasSuffix(next, "/") {
			next = "next-model"
		}
		window = int(uint(window)%1_000_000) + 1

		r := fixtureRegistry(t)
		for _, ref := range providerProfileProgramRefs() {
			profile, err := Resolve(r, ref.ref)
			if err != nil || profile == nil {
				t.Fatalf("Resolve(%s) = %#v, %v", ref.ref, profile, err)
			}
			if profile.ID() != ref.instance || profile.Surface() != ref.surface || profile.Model() != ref.model {
				t.Fatalf("profile identity = %q/%q/%q, want %q/%q/%q", profile.ID(), profile.Surface(), profile.Model(), ref.instance, ref.surface, ref.model)
			}
			assertProviderProfileCopies(t, profile)
			assertProviderProfileDecorators(t, profile, next, window)
		}
		assertProviderPurePrograms(t, r, next)

		if got, err := Resolve(r, "missing/model"); got != nil || err == nil {
			t.Fatalf("missing instance = %#v, %v", got, err)
		}
		for _, invalid := range []string{"", "anthropic/", "   "} {
			if got, err := Resolve(r, invalid); got != nil || err == nil {
				t.Fatalf("invalid ref %q = %#v, %v", invalid, got, err)
			}
		}
	})
}

type providerProfileProgramRef struct {
	ref, instance, model, surface string
}

func providerProfileProgramRefs() []providerProfileProgramRef {
	return []providerProfileProgramRef{
		{"openai/gpt-5.5", "openai", "gpt-5.5", registry.SurfaceOpenAI},
		{"anthropic/claude-opus-5", "anthropic", "claude-opus-5", registry.SurfaceAnthropic},
		{"google/gemini-3-pro", "google", "gemini-3-pro", registry.SurfaceGoogle},
		{"work/glm-5", "work", "glm-5", registry.SurfaceGeneric},
		{"orclaude/minimax/minimax-m3", "orclaude", "minimax/minimax-m3", registry.SurfaceAnthropic},
		{"openrouter/openai/gpt-5.5", "openrouter", "openai/gpt-5.5", registry.SurfaceOpenAI},
		{"ollama/llama3.1", "ollama", "llama3.1", registry.SurfaceGeneric},
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
	_ = profile.Cost()
	_ = profile.MaxOutputTokens()
	_ = profile.Protocol()
	_ = profile.ProviderID()
	if profile.ConfiguredCheapModel() != "" {
		t.Fatalf("new %s profile unexpectedly has configured cheap model", profile.ID())
	}
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
	for _, accessor := range []func() []string{profile.ReasoningEffortLevels, profile.InputModalities, profile.Warnings} {
		values := accessor()
		if len(values) == 0 {
			continue
		}
		original := values[0]
		values[0] = "mutated"
		if again := accessor(); again[0] != original {
			t.Fatalf("%s exposed a caps slice", profile.ID())
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

func assertProviderProfileDecorators(t *testing.T, profile *Profile, next string, window int) {
	t.Helper()
	if WithCheapModel(nil, "x") != nil || WithContextWindow(nil, window) != nil || (*Profile)(nil).WithResolved(registry.Resolved{}) != nil {
		t.Fatal("nil profile decorators must stay nil")
	}
	if WithCheapModel(profile, " ") != profile || WithContextWindow(profile, 0) != profile {
		t.Fatal("empty/no-op decorator did not preserve its profile")
	}

	bareCheap := WithCheapModel(profile, "  cheap-model  ")
	if bareCheap.CheapProvider() != profile.ID() || bareCheap.ConfiguredCheapModel() != "cheap-model" || bareCheap.CheapModelRefString() != "cheap-model" {
		t.Fatalf("bare WithCheapModel = %q/%q/%q", bareCheap.CheapProvider(), bareCheap.ConfiguredCheapModel(), bareCheap.CheapModelRefString())
	}
	qualifiedCheap := WithCheapModel(profile, "other/"+next)
	if provider, model := qualifiedCheap.CheapModelRef(); provider != "other" || model != next || qualifiedCheap.CheapModelRefString() != "other/"+next {
		t.Fatalf("qualified WithCheapModel = %q/%q/%q", provider, model, qualifiedCheap.CheapModelRefString())
	}
	baseWindow := profile.ContextWindowSize()
	baseEfforts := profile.ReasoningEffortLevels()
	baseTaskListEfforts := providerProgramTaskListEfforts(profile.ToolDefinitions())
	resized := WithContextWindow(profile, window)
	if resized.ContextWindowSize() != window || profile.ContextWindowSize() != baseWindow {
		t.Fatalf("WithContextWindow = %d (base %d)", resized.ContextWindowSize(), profile.ContextWindowSize())
	}

	res := profile.Resolved()
	res.Caps.ContextWindow = new(window)
	res.Caps.EffortValues = []string{"low", "high"}
	live := profile.WithResolved(res)
	if live == profile || live.ContextWindowSize() != window {
		t.Fatalf("WithResolved = %#v", live)
	}
	wantLiveEfforts := []string{"low", "high"}
	if got := live.ReasoningEffortLevels(); !reflect.DeepEqual(got, wantLiveEfforts) {
		t.Fatalf("WithResolved effort levels = %v, want %v", got, wantLiveEfforts)
	}
	wantLiveTaskListEfforts := append(append([]string(nil), wantLiveEfforts...), "inherit")
	if got := providerProgramTaskListEfforts(live.ToolDefinitions()); !reflect.DeepEqual(got, wantLiveTaskListEfforts) {
		t.Fatalf("WithResolved task_list effort enum = %v, want %v", got, wantLiveTaskListEfforts)
	}
	if profile.ContextWindowSize() != baseWindow || !reflect.DeepEqual(profile.ReasoningEffortLevels(), baseEfforts) || !reflect.DeepEqual(providerProgramTaskListEfforts(profile.ToolDefinitions()), baseTaskListEfforts) {
		t.Fatal("WithResolved mutated its base profile")
	}

	selfRef := profile.ID() + "/" + next
	if profile.CrossProviderRef(selfRef) {
		t.Fatalf("self ref %q classified cross-provider", selfRef)
	}
	changed := profile.WithModel(selfRef)
	if changed == nil || changed.ID() != profile.ID() || changed.Model() != next {
		t.Fatalf("WithModel(%q) = %#v", selfRef, changed)
	}
}

func providerProgramTaskListEfforts(defs []llm.ToolDefinition) []string {
	for _, def := range defs {
		if def.Name != "task_list" {
			continue
		}
		properties, _ := def.Parameters["properties"].(map[string]any)
		// Both add and update item schemas carry the reasoning_effort enum;
		// read whichever is present (either satisfies the sync check).
		for _, arrayName := range []string{"add", "update"} {
			arraySchema, _ := properties[arrayName].(map[string]any)
			if arraySchema == nil {
				continue
			}
			items, _ := arraySchema["items"].(map[string]any)
			taskProperties, _ := items["properties"].(map[string]any)
			reasoning, _ := taskProperties["reasoning_effort"].(map[string]any)
			return append([]string(nil), toStringSlice(reasoning["enum"])...)
		}
	}
	return nil
}

func assertProviderPurePrograms(t *testing.T, r *registry.Registry, next string) {
	t.Helper()
	var nilProfile *Profile
	if nilProfile.ConfiguredCheapModel() != "" || nilProfile.CheapProvider() != "" || nilProfile.CheapModelRefString() != "" {
		t.Fatal("nil profile cheap-model accessors must return empty values")
	}
	if cloneStringSlice(nil) != nil {
		t.Fatal("nil clone helper must return nil")
	}

	// A "[1m]" alias row re-resolves its own window and beta header.
	anthropic, err := Resolve(r, "anthropic/claude-sonnet-4-5[1m]")
	if err != nil || anthropic.ContextWindowSize() != 1_000_000 {
		t.Fatalf("anthropic [1m] = %#v, %v", anthropic, err)
	}
	if got := anthropic.WithModel("anthropic/" + next).Model(); got != next {
		t.Fatalf("anthropic self-prefix strip = %q", got)
	}

	// A meta-instance keeps a namespaced upstream id it serves.
	router, err := Resolve(r, "openrouter/anthropic/claude-opus-5")
	if err != nil {
		t.Fatalf("openrouter: %v", err)
	}
	if got := router.WithModel("anthropic/claude-opus-5").Model(); got != "anthropic/claude-opus-5" {
		t.Fatalf("openrouter upstream model prefix was not preserved: %q", got)
	}

	base := NewOpenAIProfile("gpt-5.5")
	if base.WithCommunicateOverridesFrom(nil) != base || base.withCheapModelFrom(nil) != base ||
		(*Profile)(nil).WithCommunicateOverridesFrom(base) != nil || (*Profile)(nil).withCheapModelFrom(base) != nil {
		t.Fatal("nil profile carry-forward helpers changed identity")
	}
	if base.CheapModelRefString() != "" || base.CrossProviderRef("bare-model") {
		t.Fatal("empty cheap model or bare model ref classified incorrectly")
	}
	if base.WithModel(" ").Model() != base.Model() {
		t.Fatal("empty model did not preserve the active model")
	}
	withoutCommunicate := &Profile{toolDefs: []llm.ToolDefinition{{Name: "shell"}}}
	if base.WithCommunicateOverridesFrom(withoutCommunicate) != base {
		t.Fatal("missing source communicate tool changed profile")
	}
	target := &Profile{toolDefs: []llm.ToolDefinition{{Name: "shell"}}}
	target.WithCommunicateOverridesFrom(base)
	if len(target.toolDefs) != 2 || target.toolDefs[1].Name != "communicate" {
		t.Fatal("communicate override was not appended")
	}

	for _, surface := range []string{registry.SurfaceOpenAI, registry.SurfaceAnthropic, registry.SurfaceGoogle, registry.SurfaceGeneric, "unheard-of"} {
		docs, _ := surfaceConventions(surface)
		if len(docs) == 0 {
			t.Fatalf("surface %q has no project doc files", surface)
		}
	}
}

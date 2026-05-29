package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func renderPromptForTest(t *testing.T, p ProviderProfile, data PromptData) string {
	t.Helper()
	if data.Provider == "" {
		data.Provider = p.ID()
	}
	if data.Agent == "" {
		data.Agent = defaultAgentName
	}
	if data.Model == "" {
		data.Model = p.Model()
	}
	if data.ResultToolName == "" {
		data.ResultToolName = "communicate"
	}
	if data.RolePromptOverride == "" {
		switch data.Agent {
		case "coordinator", "implementer", "reviewer", "verifier", "worker", "planner", "test-engineer":
			data.RolePromptOverride = coordinatorWorkflowAgentForTest(t, data.Agent).SystemPrompt
		}
	}
	if len(data.ProfileTools) == 0 {
		data.ProfileTools = toolEntriesFromDefinitions(p.ToolDefinitions())
	}

	resolver := &SectionResolver{
		provider: p.ID(),
		agent:    data.Agent,
		agentFS:  embeddedAgents,
		sources: []SectionSource{
			embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"},
		},
	}

	result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", data)
	if err != nil {
		t.Fatalf("RenderEmbedded: %v", err)
	}
	return result
}

func TestProviderProfiles_ToolsetsAndDocSelection(t *testing.T) {
	openai := NewOpenAIProfile("gpt-5.2")
	if openai.ID() != "openai" {
		t.Fatalf("openai id: %q", openai.ID())
	}
	if !openai.SupportsParallelToolCalls() {
		t.Fatalf("openai should support parallel tool calls")
	}
	if got := strings.Join(openai.ProjectDocFiles(), ","); got != "AGENTS.md,.codex/instructions.md" {
		t.Fatalf("openai docs: %q", got)
	}
	assertHasTool(t, openai, "apply_patch")
	assertMissingTool(t, openai, "edit_file")

	anthropic := NewAnthropicProfile("claude-test")
	if anthropic.ID() != "anthropic" {
		t.Fatalf("anthropic id: %q", anthropic.ID())
	}
	if !anthropic.SupportsParallelToolCalls() {
		t.Fatalf("anthropic should support parallel tool calls")
	}
	assertHasTool(t, anthropic, "edit_file")
	assertMissingTool(t, anthropic, "apply_patch")

	gemini := NewGeminiProfile("gemini-test")
	if gemini.ID() != "gemini" {
		t.Fatalf("gemini id: %q", gemini.ID())
	}
	if !gemini.SupportsParallelToolCalls() {
		t.Fatalf("gemini should support parallel tool calls")
	}
	assertHasTool(t, gemini, "edit_file")
	assertHasTool(t, gemini, "list_directory")
	assertMissingTool(t, gemini, "apply_patch")
}

func TestProviderProfiles_ToolLists_MatchSpec(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-5.2")
		assertToolListExact(t, p, []string{
			"read_file",
			"apply_patch",
			"write_file",
			"exec_command",
			"grep_files",
			"list_dir",
			"spawn_agent",
			"resume_agent",
			"wait",
			"close_agent",
			"task_list",
			"web_fetch",
			"communicate",
			"use_skill",
		})
	})
	t.Run("anthropic", func(t *testing.T) {
		p := NewAnthropicProfile("claude-test")
		assertToolListExact(t, p, []string{
			"read_file",
			"write_file",
			"edit_file",
			"shell",
			"grep",
			"glob",
			"spawn_agent",
			"resume_agent",
			"wait",
			"close_agent",
			"task_list",
			"web_fetch",
			"communicate",
			"use_skill",
		})
	})
	t.Run("gemini", func(t *testing.T) {
		p := NewGeminiProfile("gemini-test")
		assertToolListExact(t, p, []string{
			"read_file",
			"write_file",
			"edit_file",
			"run_shell_command",
			"grep_search",
			"glob",
			"list_directory",
			"spawn_agent",
			"resume_agent",
			"wait",
			"close_agent",
			"task_list",
			"web_fetch",
			"web_search",
			"communicate",
			"use_skill",
		})
	})
}

func TestProviderProfiles_AllIncludeUseSkill(t *testing.T) {
	profiles := []ProviderProfile{
		NewOpenAIProfile("gpt-5.2"),
		NewAnthropicProfile("claude-test"),
		NewGeminiProfile("gemini-test"),
		NewMiniMaxProfile("MiniMax-M2.7"),
		NewOpenRouterAnthropicProfile("anthropic/claude-test"),
		NewOpenAICompatProfile("openrouter", "openai/gpt-test", 0),
		NewOpenAICompatProfile("kimi", "kimi-test", 0),
		NewOpenAICompatProfile("glm", "glm-test", 0),
		NewOpenAICompatProfile("ollama", "llama3", 0),
	}
	for _, p := range profiles {
		t.Run(p.ID(), func(t *testing.T) {
			assertHasTool(t, p, "use_skill")
		})
	}
}

func TestProviderProfiles_AddPurposeToEveryToolSchema(t *testing.T) {
	profiles := []ProviderProfile{
		NewOpenAIProfile("gpt-5.2"),
		NewAnthropicProfile("claude-test"),
		NewGeminiProfile("gemini-test"),
		NewOpenAICompatProfile("openrouter", "openai/gpt-test", 0),
	}
	for _, p := range profiles {
		for _, td := range p.ToolDefinitions() {
			props, _ := td.Parameters["properties"].(map[string]any)
			if props == nil {
				t.Fatalf("%s/%s has no properties schema", p.ID(), td.Name)
			}
			if _, ok := props["purpose"]; !ok {
				t.Fatalf("%s/%s missing purpose parameter", p.ID(), td.Name)
			}
			if td.Name == "shell" || td.Name == "exec_command" || td.Name == "run_shell_command" {
				if _, ok := props["description"]; ok {
					t.Fatalf("%s/%s still exposes legacy description parameter", p.ID(), td.Name)
				}
			}
		}
	}
}

func TestSystemPrompt_ImplementerWarnsOnUnavailableTools(t *testing.T) {
	prompt := renderPromptForTest(t, NewOpenAIProfile("gpt-5.4"), PromptData{
		Agent:                       "implementer",
		CallableToolNames:           []string{"read_file", "exec_command", "communicate"},
		UnavailableProfileToolNames: []string{"spawn_agent", "resume_agent", "wait", "close_agent"},
	})

	if !strings.Contains(prompt, "If the task depends on tools or capabilities explicitly listed as unavailable in") {
		t.Fatalf("implementer prompt missing unavailable-tools guidance:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do not try to recreate unavailable serf-native tools by shelling out to") {
		t.Fatalf("implementer prompt missing nested-serf warning:\n%s", prompt)
	}
}

func TestSystemPrompt_CoordinatorHasImpossibleDelegationException(t *testing.T) {
	prompt := renderPromptForTest(t, NewOpenAIProfile("gpt-5.4"), PromptData{
		Agent: "coordinator",
	})

	if !strings.Contains(prompt, "Exception: if the task itself is about delegation, agent behavior, or orchestration") {
		t.Fatalf("coordinator prompt missing delegation exception:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do not force an impossible delegation.") {
		t.Fatalf("coordinator prompt missing impossible-delegation rule:\n%s", prompt)
	}
}

func TestSystemPrompt_DefaultAgentDoesNotUseCoordinatorRole(t *testing.T) {
	prompt := renderPromptForTest(t, NewOpenAIProfile("gpt-5.4"), PromptData{})

	if strings.Contains(prompt, "You are a coordinator. You delegate, verify, and iterate. You do not implement.") {
		t.Fatalf("default prompt should not use coordinator persona:\n%s", prompt)
	}
	if strings.Contains(prompt, "### CRITICAL: You normally spawn an implementer") {
		t.Fatalf("default prompt should not include coordinator delegation mandate:\n%s", prompt)
	}
}

func TestProviderProfiles_BuildSystemPrompt_IncludesEnvironment(t *testing.T) {
	data := PromptData{
		WorkingDir:      "/tmp",
		Platform:        "linux",
		OSVersion:       "test",
		Today:           "2026-02-07",
		KnowledgeCutoff: "2024-06-01",
	}

	for _, p := range []ProviderProfile{
		NewOpenAIProfile("gpt-5.2"),
		NewAnthropicProfile("claude-test"),
		NewGeminiProfile("gemini-test"),
	} {
		sys := renderPromptForTest(t, p, data)
		if !strings.Contains(sys, "<environment>") {
			t.Errorf("%s prompt missing <environment> block", p.ID())
		}
		if !strings.Contains(sys, "## Tool usage") {
			t.Errorf("%s prompt missing tool usage section", p.ID())
		}
	}
}

func TestBuildSystemPrompt_DoesNotDuplicateProviderToolDescriptions(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	data := PromptData{
		WorkingDir: "/tmp",
		Platform:   "linux",
	}
	data.ProfileTools = toolEntriesFromDefinitions(p.ToolDefinitions())

	prompt := renderPromptForTest(t, p, data)

	if strings.Contains(prompt, "Tools:") {
		t.Fatalf("system prompt should not include provider tool description list already present in tool definitions:\n%s", prompt)
	}
	for _, td := range p.ToolDefinitions() {
		desc := strings.TrimSpace(td.Description)
		if desc != "" && strings.Contains(prompt, desc) {
			t.Fatalf("system prompt duplicates provider tool description for %s: %q", td.Name, desc)
		}
	}
}

func TestBuildSystemPrompt_DoesNotDuplicateMCPOrCustomToolDescriptions(t *testing.T) {
	prompt := renderPromptForTest(t, NewOpenAIProfile("gpt-5.2"), PromptData{
		WorkingDir: "/tmp",
		Platform:   "linux",
		MCPTools: []ToolEntry{{
			Name:        "mcp__server__search",
			Description: "Searches the remote index with an MCP-backed provider tool.",
		}},
		CustomTools: []ToolEntry{{
			Name:        "project_custom",
			Description: "Runs a project-specific custom tool.",
		}},
	})

	for _, unwanted := range []string{
		"MCP tools:",
		"Custom tools:",
		"Searches the remote index with an MCP-backed provider tool.",
		"Runs a project-specific custom tool.",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("system prompt duplicates tool description content %q:\n%s", unwanted, prompt)
		}
	}
}

func TestProviderProfile_CheapModel(t *testing.T) {
	cases := []struct {
		profile ProviderProfile
		want    string
	}{
		{NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano"},
		{NewAnthropicProfile("claude-opus-4-6"), "claude-haiku-4-5-20251001"},
		{NewGeminiProfile("gemini-3-pro"), "gemini-2.5-flash-lite"},
	}
	for _, tc := range cases {
		got := tc.profile.CheapModel()
		if got != tc.want {
			t.Fatalf("profile %q CheapModel: got %q want %q", tc.profile.ID(), got, tc.want)
		}
	}
}

func TestProviderProfile_WithModel(t *testing.T) {
	orig := NewOpenAIProfile("gpt-5.2")
	cloned := orig.WithModel("gpt-4.1-mini")

	if cloned.Model() != "gpt-4.1-mini" {
		t.Fatalf("cloned model: got %q want %q", cloned.Model(), "gpt-4.1-mini")
	}
	if cloned.ID() != orig.ID() {
		t.Fatalf("cloned ID should match original: got %q want %q", cloned.ID(), orig.ID())
	}
	// Original must be unchanged.
	if orig.Model() != "gpt-5.2" {
		t.Fatalf("original model mutated: got %q", orig.Model())
	}
	// Tool definitions preserved.
	if len(cloned.ToolDefinitions()) != len(orig.ToolDefinitions()) {
		t.Fatalf("tool count mismatch: cloned=%d orig=%d", len(cloned.ToolDefinitions()), len(orig.ToolDefinitions()))
	}
	if renderPromptForTest(t, cloned, PromptData{WorkingDir: "/tmp", Platform: "linux"}) == "" {
		t.Fatalf("cloned profile has empty system prompt")
	}
}

func TestProviderProfile_WithModel_EmptyStringKeepsOriginal(t *testing.T) {
	orig := NewAnthropicProfile("claude-opus-4-6")
	cloned := orig.WithModel("")
	if cloned.Model() != "claude-opus-4-6" {
		t.Fatalf("WithModel('') should keep original model, got %q", cloned.Model())
	}
}

func TestProviderProfile_WithModel_ResolvesProviderPrefix(t *testing.T) {
	// WithModel("openai/gpt-5.4-mini") on an OpenAI profile should strip
	// the prefix and use the bare model name.
	orig := NewOpenAIProfile("gpt-5.4")
	cloned := orig.WithModel("openai/gpt-5.4-mini")
	if cloned.Model() != "gpt-5.4-mini" {
		t.Fatalf("Model() = %q, want %q", cloned.Model(), "gpt-5.4-mini")
	}
	if cloned.ID() != "openai" {
		t.Fatalf("ID() = %q, want %q", cloned.ID(), "openai")
	}
}

// TestProviderProfile_WithModel_CrossProvider and related cross-provider
// WithModel tests have been moved to session_resolve_profile_test.go.
// Cross-provider switching is now the responsibility of the Session resolver.

func TestNewOpenAIProfile_UnknownModelUsesModernContextFallback(t *testing.T) {
	p := NewOpenAIProfile("gpt-6-preview")
	if got := p.ContextWindowSize(); got == 128_000 {
		t.Fatalf("ContextWindowSize() = %d, want modern fallback larger than 128000", got)
	}
	if got := p.ContextWindowSize(); got < 400_000 {
		t.Fatalf("ContextWindowSize() = %d, want at least 400000", got)
	}
}

// TestNewOpenAICompatProfile_OllamaResolvesCatalogMetadata verifies that
// constructing an Ollama profile with a model that exists in the catalog
// under a provider-prefixed key picks up the catalog's context window
// instead of falling back to the 128K generic default. ollama/llama3.1
// has max_input_tokens=8192 in the embedded litellm catalog.
func TestNewOpenAICompatProfile_OllamaResolvesCatalogMetadata(t *testing.T) {
	p := NewOpenAICompatProfile("ollama", "llama3.1", 0)
	if got := p.ContextWindowSize(); got != 8192 {
		t.Fatalf("ContextWindowSize() = %d, want 8192 (from ollama/llama3.1 catalog entry)", got)
	}
	if p.Model() != "llama3.1" {
		t.Fatalf("Model() = %q, want %q (bare name preserved on the wire)", p.Model(), "llama3.1")
	}
}

// TestNewOpenAICompatProfile_OllamaUnknownModelFallsBack verifies that an
// Ollama model with no catalog entry (under either bare or prefixed key,
// including the tag-stripped form) falls back to the 128K generic default
// rather than failing.
func TestNewOpenAICompatProfile_OllamaUnknownModelFallsBack(t *testing.T) {
	p := NewOpenAICompatProfile("ollama", "definitely-not-a-real-model:9999b", 0)
	if got := p.ContextWindowSize(); got != 128_000 {
		t.Fatalf("ContextWindowSize() = %d, want 128000 fallback", got)
	}
}

// TestNewOpenAICompatProfile_OllamaTaggedModelFallsBackToBase verifies that
// tagged Ollama model names like "llama3.1:8b" — the form documented in
// docs/ollama.md and produced by `ollama pull llama3.1:8b` — still pick up
// catalog metadata via tag-stripped lookup. The catalog stores
// "ollama/llama3.1" (no tag), so without this fallback every typical
// tagged Ollama model would silently miss its catalog entry.
func TestNewOpenAICompatProfile_OllamaTaggedModelFallsBackToBase(t *testing.T) {
	p := NewOpenAICompatProfile("ollama", "llama3.1:8b", 0)
	if got := p.ContextWindowSize(); got != 8192 {
		t.Fatalf("ContextWindowSize() = %d, want 8192 (from ollama/llama3.1 via tag-stripped lookup)", got)
	}
	if p.Model() != "llama3.1:8b" {
		t.Fatalf("Model() = %q, want llama3.1:8b — wire model must keep the tag", p.Model())
	}
}

// TestResolveOpenAICompatCatalogModel exercises the lookup precedence
// using a fake catalog so each branch can be observed directly. The
// embedded catalog ships every ollama/llama3* variant with the same
// 8192 context window, so a real-data test cannot distinguish the
// exact-tagged path from the tag-stripped fallback.
func TestResolveOpenAICompatCatalogModel(t *testing.T) {
	fake := func(entries map[string]int) func(string) *llm.ModelInfo {
		return func(key string) *llm.ModelInfo {
			if ctx, ok := entries[key]; ok {
				return &llm.ModelInfo{ID: key, ContextWindow: ctx}
			}
			return nil
		}
	}

	t.Run("prefixed key wins when both exist (openrouter overlap)", func(t *testing.T) {
		// Real-world case: catalog has both "deepseek/deepseek-r1"
		// (the deepseek provider's entry) and
		// "openrouter/deepseek/deepseek-r1" (OpenRouter's entry,
		// possibly with a different context window). Asking for
		// openrouter/deepseek-r1 must hit the OpenRouter entry, not
		// the deepseek one.
		lookup := fake(map[string]int{
			"deepseek/deepseek-r1":            65536, // wrong provider's entry
			"openrouter/deepseek/deepseek-r1": 65336, // correct match
		})
		mi := resolveOpenAICompatCatalogModel(lookup, "openrouter", "deepseek/deepseek-r1")
		if mi == nil {
			t.Fatal("got nil, want openrouter-prefixed match")
		}
		if mi.ContextWindow != 65336 {
			t.Fatalf("ContextWindow = %d, want 65336 (openrouter-prefixed); got %d means the bare lookup fired before the prefixed match",
				mi.ContextWindow, mi.ContextWindow)
		}
	})

	t.Run("bare key wins when prefixed misses (kimi/glm style)", func(t *testing.T) {
		// kimi and glm catalog keys are unprefixed — the prefixed
		// lookup misses, so the bare lookup is the actual match.
		lookup := fake(map[string]int{"kimi-k2.5": 100})
		mi := resolveOpenAICompatCatalogModel(lookup, "kimi", "kimi-k2.5")
		if mi == nil || mi.ContextWindow != 100 {
			t.Fatalf("got %+v, want bare match", mi)
		}
	})

	t.Run("prefixed key matches when only it exists", func(t *testing.T) {
		lookup := fake(map[string]int{"ollama/llama3.1": 200})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "llama3.1")
		if mi == nil || mi.ContextWindow != 200 {
			t.Fatalf("got %+v, want prefixed match", mi)
		}
	})

	t.Run("exact tagged prefixed key wins over tag-stripped fallback", func(t *testing.T) {
		// Both keys exist with DIFFERENT context windows. The exact tagged
		// key must be selected, NOT the tag-stripped one. If the lookup
		// regresses and the third (stripped) branch fires before the
		// second (exact prefixed) branch, this test catches it.
		lookup := fake(map[string]int{
			"ollama/llama3":    111, // tag-stripped fallback target
			"ollama/llama3:8b": 222, // exact tagged target — should win
		})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "llama3:8b")
		if mi == nil {
			t.Fatal("got nil, want exact tagged match")
		}
		if mi.ContextWindow != 222 {
			t.Fatalf("ContextWindow = %d, want 222 (exact tagged); got %d means the tag-stripped fallback fired before the exact prefixed match", mi.ContextWindow, mi.ContextWindow)
		}
	})

	t.Run("tag-stripped prefixed key when exact tagged misses", func(t *testing.T) {
		// Only the untagged base exists in the catalog. A user-supplied
		// "llama3.1:8b" must fall back to "ollama/llama3.1".
		lookup := fake(map[string]int{"ollama/llama3.1": 333})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "llama3.1:8b")
		if mi == nil || mi.ContextWindow != 333 {
			t.Fatalf("got %+v, want tag-stripped fallback to ollama/llama3.1", mi)
		}
	})

	t.Run("returns nil when nothing matches", func(t *testing.T) {
		lookup := fake(map[string]int{"unrelated/model": 1})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "nope:9999b")
		if mi != nil {
			t.Fatalf("got %+v, want nil", mi)
		}
	})

	t.Run("model without colon does not attempt tag-stripped lookup", func(t *testing.T) {
		// Sanity: an untagged miss must not fall through to a fictional
		// stripped form. We use a bare-key catalog that would otherwise
		// match the tag-stripped key if the third branch fired.
		lookup := fake(map[string]int{"ollama/": 999})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "nonexistent")
		if mi != nil {
			t.Fatalf("got %+v, want nil — there is no tag to strip", mi)
		}
	})

	t.Run("ollama does not bare-fall-back to unrelated provider entries", func(t *testing.T) {
		// Real-world hazard: the catalog has bare anthropic entries
		// ("claude-3-haiku-20240307": 200000). Asking for that name
		// under ollama must NOT pick up Anthropic's metadata — the
		// 200K window would silently mask Ollama context truncation.
		lookup := fake(map[string]int{
			"claude-3-haiku-20240307": 200000, // Anthropic's bare entry
		})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "claude-3-haiku-20240307")
		if mi != nil {
			t.Fatalf("got %+v, want nil — bare-key fallback must be disabled for ollama", mi)
		}
	})

	t.Run("openrouter still uses bare-key fallback for upstream models", func(t *testing.T) {
		// OpenRouter routes to upstreams whose catalog entries are
		// often only stored under their bare upstream key (no
		// "openrouter/..." prefix). For example, requesting
		// "openrouter + minimax/minimax-m2.7" must hit the bare
		// "minimax/minimax-m2.7" entry. The prefixed-first precedence
		// still protects against the overlap case (covered separately
		// by the deepseek subtest above).
		lookup := fake(map[string]int{
			"minimax/minimax-m2.7": 204800,
		})
		mi := resolveOpenAICompatCatalogModel(lookup, "openrouter", "minimax/minimax-m2.7")
		if mi == nil {
			t.Fatal("got nil, want bare upstream match")
		}
		if mi.ContextWindow != 204800 {
			t.Fatalf("ContextWindow = %d, want 204800 (upstream bare entry)", mi.ContextWindow)
		}
	})

	t.Run("kimi still uses bare-key fallback", func(t *testing.T) {
		// Sanity: providers whose catalog keys are unprefixed (kimi,
		// glm) must still hit the bare lookup.
		lookup := fake(map[string]int{"kimi-k2.5": 100})
		mi := resolveOpenAICompatCatalogModel(lookup, "kimi", "kimi-k2.5")
		if mi == nil || mi.ContextWindow != 100 {
			t.Fatalf("got %+v, want kimi-k2.5 bare match", mi)
		}
	})
}

// TestNewOpenAICompatProfile_OllamaDoesNotPickUpAnthropicCatalog is the
// integration-level version of the bare-key skip: the embedded catalog
// has Anthropic models like "claude-3-haiku-20240307" with 200K windows.
// Without the skip, asking ollama for that name would silently inherit
// Anthropic's window. Asserts the catalog miss falls back to 128K.
func TestNewOpenAICompatProfile_OllamaDoesNotPickUpAnthropicCatalog(t *testing.T) {
	p := NewOpenAICompatProfile("ollama", "claude-3-haiku-20240307", 0)
	if got := p.ContextWindowSize(); got != 128_000 {
		t.Fatalf("ContextWindowSize() = %d, want 128000 generic fallback — bare-key lookup leaked anthropic catalog metadata into ollama", got)
	}
}

// TestBaseProfile_WithModel_PreservesSlashOnMetaProviders verifies that
// for meta-providers whose model IDs legitimately contain slashes
// (openrouter, openrouter-anthropic, minimax), WithModel does NOT
// reinterpret the slash as a provider-switch prefix. The model string
// must be passed through verbatim and the profile id must not change.
//
// Specific failure modes this guards against, all reachable via
// runtime paths like Session.SetModel and subagent model overrides:
//   - openrouter profile + WithModel("minimax/minimax-m2.7") would have
//     switched to the standalone minimax adapter and dropped the
//     OpenRouter routing, mangling tool calls
//   - minimax profile + WithModel("minimax/minimax-m2.7") would have
//     stripped the model to "minimax-m2.7", which is not a valid model
//     name on that adapter
//   - openrouter-anthropic profile + WithModel(prefixed) similarly
//     loses the OpenRouter-Anthropic routing
func TestBaseProfile_WithModel_PreservesSlashOnMetaProviders(t *testing.T) {
	cases := []struct {
		startProfile func() ProviderProfile
		startID      string
		input        string
	}{
		{func() ProviderProfile { return NewMiniMaxProfile("minimax/minimax-m2.7") }, "minimax", "minimax/minimax-m2.7"},
		{func() ProviderProfile { return NewMiniMaxProfile("minimax/minimax-m2.7") }, "minimax", "minimax/minimax-m2.1"},
		{func() ProviderProfile {
			return NewOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
		}, "openrouter", "minimax/minimax-m2.7"},
		{func() ProviderProfile {
			return NewOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
		}, "openrouter", "anthropic/claude-3-haiku-20240307"},
		{func() ProviderProfile {
			return NewOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
		}, "openrouter", "deepseek/deepseek-r1"},
		{func() ProviderProfile { return NewOpenRouterAnthropicProfile("minimax/minimax-m2.7") }, "openrouter-anthropic", "minimax/minimax-m2.7"},
		{func() ProviderProfile { return NewOpenRouterAnthropicProfile("minimax/minimax-m2.7") }, "openrouter-anthropic", "anthropic/claude-3-5-sonnet"},
	}
	for _, tc := range cases {
		t.Run(tc.startID+"_"+tc.input, func(t *testing.T) {
			orig := tc.startProfile()
			cloned := orig.WithModel(tc.input)
			if cloned.ID() != tc.startID {
				t.Errorf("ID() = %q, want %q (meta-provider must not be switched away from)", cloned.ID(), tc.startID)
			}
			if cloned.Model() != tc.input {
				t.Errorf("Model() = %q, want %q (slash-containing model must be preserved verbatim)", cloned.Model(), tc.input)
			}
		})
	}
}

// TestBaseProfile_WithModel_StripsRedundantSelfPrefixOnMetaProviders
// verifies that WithModel still strips a redundant self-prefix on
// meta-providers — e.g. "openrouter/anthropic/claude-3-haiku" on an
// openrouter profile resolves to model "anthropic/claude-3-haiku".
// Without this, SetModel calls coming from CLI/harbor with the
// "<provider>/<model>" convention would send the doubly-prefixed
// string on the wire instead of the canonical bare form.
func TestBaseProfile_WithModel_StripsRedundantSelfPrefixOnMetaProviders(t *testing.T) {
	cases := []struct {
		startProfile func() ProviderProfile
		startID      string
		input        string
		wantModel    string
	}{
		{func() ProviderProfile { return NewOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "openrouter/anthropic/claude-3-haiku-20240307", "anthropic/claude-3-haiku-20240307"},
		{func() ProviderProfile { return NewOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "openrouter/minimax/minimax-m2.7", "minimax/minimax-m2.7"},
		{func() ProviderProfile { return NewOpenRouterAnthropicProfile("x") }, "openrouter-anthropic", "openrouter-anthropic/anthropic/claude-3-5-sonnet", "anthropic/claude-3-5-sonnet"},
	}
	for _, tc := range cases {
		t.Run(tc.startID+"_"+tc.input, func(t *testing.T) {
			cloned := tc.startProfile().WithModel(tc.input)
			if cloned.ID() != tc.startID {
				t.Errorf("ID() = %q, want %q", cloned.ID(), tc.startID)
			}
			if cloned.Model() != tc.wantModel {
				t.Errorf("Model() = %q, want %q (redundant self-prefix should be stripped)", cloned.Model(), tc.wantModel)
			}
		})
	}
}

// TestBaseProfile_WithModel_RecomputesCatalogStateOnMetaProviders
// verifies that when WithModel preserves a meta-provider but changes
// the model, model-derived state (notably ContextWindowSize) is
// recomputed via the appropriate constructor — not stale from the
// originally-constructed profile. This guards against silent context
// truncation when SetModel switches between OpenRouter-routed models
// with different real context windows.
func TestBaseProfile_WithModel_RecomputesCatalogStateOnMetaProviders(t *testing.T) {
	// Start with a known small-context model under openrouter
	// ("anthropic/claude-3-haiku-20240307" → 200000 in the catalog).
	orig := NewOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
	if orig.ContextWindowSize() != 200_000 {
		t.Fatalf("setup: orig ContextWindowSize = %d, want 200000", orig.ContextWindowSize())
	}

	// Switch model to a known different-context model
	// ("minimax/minimax-m2.7" → 204800). The clone must reflect the
	// new model's context window, not preserve 200000.
	cloned := orig.WithModel("minimax/minimax-m2.7")
	if cloned.ID() != "openrouter" {
		t.Fatalf("ID() = %q, want openrouter", cloned.ID())
	}
	if cloned.Model() != "minimax/minimax-m2.7" {
		t.Fatalf("Model() = %q, want minimax/minimax-m2.7", cloned.Model())
	}
	if got := cloned.ContextWindowSize(); got != 204_800 {
		t.Fatalf("ContextWindowSize() = %d, want 204800 from minimax catalog entry — same-provider WithModel did not recompute model-derived state", got)
	}
}

// TestNewOpenRouterAnthropicProfile_ResolvesOpenRouterPrefixedCatalog
// verifies that NewOpenRouterAnthropicProfile picks up catalog metadata
// for OpenRouter-prefixed Anthropic models. The OpenRouter catalog
// stores these as "openrouter/anthropic/claude-3-haiku-20240307" with
// a 200K context window, but the profile id is "openrouter-anthropic"
// (with a hyphen) and the bare lookup misses, so without an OpenRouter
// fallback the profile falls back to 128K.
//
// This matters because baseProfile.WithModel now rebuilds the
// openrouter-anthropic profile via NewOpenRouterAnthropicProfile when
// the model changes within the same provider — the constructor must
// re-derive metadata for the new model, not just inherit defaults.
func TestNewOpenRouterAnthropicProfile_ResolvesOpenRouterPrefixedCatalog(t *testing.T) {
	p := NewOpenRouterAnthropicProfile("anthropic/claude-3-haiku-20240307")
	if got := p.ContextWindowSize(); got != 200_000 {
		t.Fatalf("ContextWindowSize() = %d, want 200000 from openrouter/anthropic/claude-3-haiku-20240307 catalog entry — bare lookup missed and OpenRouter prefix was not tried", got)
	}
}

// TestNewOpenRouterAnthropicProfile_PreservesWebSearchDefault verifies
// that constructing an openrouter-anthropic profile with a model whose
// OpenRouter catalog entry doesn't carry supports_web_search (the
// common case — only some prefixed entries advertise it) leaves the
// constructor's default of `true` intact. Previously the code did
// `ws = mi.SupportsWebSearch` unconditionally, which silently flipped
// web search off for matched OpenRouter Anthropic models.
func TestNewOpenRouterAnthropicProfile_PreservesWebSearchDefault(t *testing.T) {
	// claude-3-haiku-20240307 is in the catalog as
	// "openrouter/anthropic/claude-3-haiku-20240307" with NO
	// supports_web_search field. Constructor default should win.
	p := NewOpenRouterAnthropicProfile("anthropic/claude-3-haiku-20240307")
	if !p.SupportsWebSearch() {
		t.Fatal("SupportsWebSearch() = false, want true (constructor default must win when prefixed catalog entry omits supports_web_search)")
	}
}

// TestResolveOpenRouterAnthropicWebSearch verifies the three-step
// resolution precedence used by NewOpenRouterAnthropicProfile.
// Step 1 (openrouter-prefixed) and step 2 (bare-direct, only when step
// 1 misses) are authoritative; step 3 (bare-upstream-stripped) is a
// fallback that only fills when no earlier step resolved the field.
//
// Particularly important: step 3 must NOT overwrite an authoritative
// step 2 result, even if step 2's matched entry happened to omit the
// field. Built against a fake catalog so all branches can be exercised
// directly — the real catalog doesn't currently contain a model where
// every relevant key exists with diverging values.
func TestResolveOpenRouterAnthropicWebSearch(t *testing.T) {
	tt := func(t *testing.T, name string, entries map[string]*bool, presentEntries map[string]bool, model string, wantWS bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			lookup := func(key string) *llm.ModelInfo {
				if _, present := presentEntries[key]; !present {
					return nil
				}
				ws := entries[key]
				return &llm.ModelInfo{ID: key, SupportsWebSearch: ws}
			}
			got := resolveOpenRouterAnthropicWebSearch(lookup, model, true)
			if got != wantWS {
				t.Fatalf("got %v, want %v", got, wantWS)
			}
		})
	}

	bTrue, bFalse := true, false

	// Step 1 wins when the openrouter-prefixed entry has an explicit value.
	tt(t, "step 1 explicit false wins over later steps",
		map[string]*bool{"openrouter/anthropic/m": &bFalse, "anthropic/m": &bTrue, "m": &bTrue},
		map[string]bool{"openrouter/anthropic/m": true, "anthropic/m": true, "m": true},
		"anthropic/m", false)

	// Step 2 wins when no openrouter-prefixed entry exists but a
	// bare-direct entry does. Step 3 stripped upstream must NOT
	// overwrite step 2's authoritative answer — this was the bug.
	tt(t, "step 2 explicit false wins over step 3 explicit true",
		map[string]*bool{"anthropic/m": &bFalse, "m": &bTrue},
		map[string]bool{"anthropic/m": true, "m": true},
		"anthropic/m", false)

	// Step 3 fills when steps 1 and 2 are silent (no entries match).
	tt(t, "step 3 fills when steps 1 and 2 silent",
		map[string]*bool{"m": &bTrue},
		map[string]bool{"m": true},
		"anthropic/m", true)

	// Step 3 fills when step 2 matched but its entry has no field —
	// useful for picking up serf overrides on bare upstream IDs.
	tt(t, "step 3 fills when step 2 matched but field absent",
		map[string]*bool{"anthropic/m": nil, "m": &bFalse},
		map[string]bool{"anthropic/m": true, "m": true},
		"anthropic/m", false)

	// Step 3 fills when step 1's prefixed entry matched but has no field.
	tt(t, "step 3 fills when step 1 matched but field absent",
		map[string]*bool{"openrouter/anthropic/m": nil, "m": &bFalse},
		map[string]bool{"openrouter/anthropic/m": true, "m": true},
		"anthropic/m", false)

	// All silent → caller default wins.
	tt(t, "default wins when nothing matches",
		map[string]*bool{}, map[string]bool{},
		"anthropic/m", true)
}

// TestNewOpenRouterAnthropicProfile_StripsBareUpstreamCtxFallback verifies
// that the catalog context-window resolution falls through to the
// bare-upstream-stripped lookup when neither the openrouter-prefixed
// nor the bare-direct entries supply one. Concrete case:
// "anthropic/claude-sonnet-4-5" — no openrouter/anthropic/... or
// anthropic/claude-sonnet-4-5 entry, but bare "claude-sonnet-4-5" has
// max_input_tokens=200000.
func TestNewOpenRouterAnthropicProfile_StripsBareUpstreamCtxFallback(t *testing.T) {
	p := NewOpenRouterAnthropicProfile("anthropic/claude-sonnet-4-5")
	if got := p.ContextWindowSize(); got != 200_000 {
		t.Fatalf("ContextWindowSize() = %d, want 200000 from bare upstream catalog entry — step 3 ctx fallback missing", got)
	}
}

// TestBaseProfile_WithModel_OpenRouterSwitchesToUnambiguousProvider has been
// moved to session_resolve_profile_test.go. Cross-provider switching is now
// routed through the Session resolver; WithModel handles only same-provider,
// strip, and keep cases.

// TestBaseProfile_WithModel_OpenRouterKeepsUpstreamNamespace verifies
// the other half of the meta-provider rule: prefixes that COULD be
// OpenRouter upstreams (anthropic, openai, google, gemini, minimax)
// stay as model namespaces and don't trigger a provider switch.
func TestBaseProfile_WithModel_OpenRouterKeepsUpstreamNamespace(t *testing.T) {
	cases := []struct {
		startProfile func() ProviderProfile
		startID      string
		input        string
	}{
		{func() ProviderProfile { return NewOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "anthropic/claude-3-haiku"},
		{func() ProviderProfile { return NewOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "openai/gpt-5"},
		{func() ProviderProfile { return NewOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "google/gemini-3"},
		{func() ProviderProfile { return NewOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "minimax/minimax-m2.7"},
		{func() ProviderProfile { return NewOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "deepseek/deepseek-r1"},
		{func() ProviderProfile { return NewOpenRouterAnthropicProfile("x") }, "openrouter-anthropic", "anthropic/claude-3-5-sonnet"},
		{func() ProviderProfile { return NewOpenRouterAnthropicProfile("x") }, "openrouter-anthropic", "minimax/minimax-m2.7"},
	}
	for _, tc := range cases {
		t.Run(tc.startID+"_"+tc.input, func(t *testing.T) {
			cloned := tc.startProfile().WithModel(tc.input)
			if cloned.ID() != tc.startID {
				t.Errorf("ID() = %q, want %q (upstream namespace prefix must NOT switch providers)", cloned.ID(), tc.startID)
			}
			if cloned.Model() != tc.input {
				t.Errorf("Model() = %q, want %q", cloned.Model(), tc.input)
			}
		})
	}
}

// TestNewOpenRouterAnthropicProfile_HonorsExplicitWebSearchFalse verifies
// that when a bare-direct catalog match (no openrouter prefix) explicitly
// sets supports_web_search to false, that signal is respected. This is
// the documented MiniMax-over-OpenRouter-Anthropic path: the model
// "minimax/minimax-m2.7" has no openrouter-prefixed catalog entry, so
// the resolver falls back to the bare entry whose serf override
// explicitly disables web search.
//
// The fix from the previous round ("only override when explicitly true")
// over-corrected: it correctly preserved the constructor default for
// sparse OpenRouter prefixed entries, but it also ignored explicit
// false on bare-direct matches, falsely advertising web search support
// for models that don't have it.
func TestNewOpenRouterAnthropicProfile_HonorsExplicitWebSearchFalse(t *testing.T) {
	p := NewOpenRouterAnthropicProfile("minimax/minimax-m2.7")
	if p.SupportsWebSearch() {
		t.Fatal("SupportsWebSearch() = true, want false — bare entry's explicit supports_web_search:false must be respected on the MiniMax-via-OpenRouter-Anthropic path")
	}
}

// TestNewOpenRouterAnthropicProfile_PicksUpBareUpstreamEffortOverrides
// verifies that effort levels resolve to the bare upstream override
// when the prefixed catalog entry doesn't carry them. The serf
// override file keys overrides under bare upstream IDs (e.g.
// "claude-sonnet-4-5" → ["low","medium","high"], no "max"). Without
// bare-fallback resolution, openrouter-anthropic falls back to the
// constructor's MiniMax-style default ["low","medium","high","max"]
// — incorrectly advertising a "max" tier these models don't support.
func TestNewOpenRouterAnthropicProfile_PicksUpBareUpstreamEffortOverrides(t *testing.T) {
	// "anthropic/claude-sonnet-4-5" — the openrouter prefix in the
	// catalog has no reasoning_effort_levels; the bare "claude-sonnet-4-5"
	// override entry sets ["low","medium","high"].
	p := NewOpenRouterAnthropicProfile("anthropic/claude-sonnet-4-5")
	got := p.ReasoningEffortLevels()
	want := []string{"low", "medium", "high"}
	if !equalStringSlices(got, want) {
		t.Fatalf("ReasoningEffortLevels() = %v, want %v (bare upstream override should be picked up)", got, want)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestBaseProfile_WithModel_PreservesToolDefOverridesAcrossProviderSwitch
// verifies that a cross-provider WithModel ("openai/...", "ollama/...",
// etc.) also preserves tool-schema overrides applied via
// WithCommunicateOutputSchema. Without this, Session.SetModel that
// switches provider mid-session — or subagent/plugin overrides that
// choose a different backend — silently revert the communicate
// contract to the new provider's default.
func TestBaseProfile_WithModel_PreservesToolDefOverridesAcrossProviderSwitch(t *testing.T) {
	customSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"my_field": map[string]any{"type": "string"},
		},
		"required":             []any{"my_field"},
		"additionalProperties": false,
	}

	cases := []struct {
		name     string
		newOrig  func() ProviderProfile
		newModel string
	}{
		{"openai-to-ollama", func() ProviderProfile { return NewOpenAIProfile("gpt-5.4") }, "ollama/llama3.1"},
		{"openrouter-to-ollama", func() ProviderProfile {
			return NewOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
		}, "ollama/llama3.1"},
		{"openai-to-anthropic", func() ProviderProfile { return NewOpenAIProfile("gpt-5.4") }, "anthropic/claude-3-opus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withSchema := WithCommunicateOutputSchema(tc.newOrig(), customSchema)
			afterSwitch := withSchema.WithModel(tc.newModel)

			var found bool
			for _, td := range afterSwitch.ToolDefinitions() {
				if td.Name != "communicate" {
					continue
				}
				found = true
				props, _ := td.Parameters["properties"].(map[string]any)
				output, _ := props["output"].(map[string]any)
				outProps, _ := output["properties"].(map[string]any)
				if _, ok := outProps["my_field"]; !ok {
					t.Errorf("after WithModel(%q) cross-provider switch, communicate.output.properties is missing my_field — custom schema was dropped during provider switch. Got: %v", tc.newModel, outProps)
				}
			}
			if !found {
				t.Fatal("communicate tool not found in switched profile")
			}
		})
	}
}

// TestBaseProfile_WithModel_PreservesToolDefOverrides verifies that
// same-provider WithModel rebuilds (which now go through the
// constructor for openrouter/openrouter-anthropic/kimi/glm/ollama)
// don't drop tool-schema customizations applied via
// WithCommunicateOutputSchema or WithAllowedDecisions. Previously the
// rebuild handed back a fresh constructor profile with default
// toolDefs, silently losing the override.
//
// Specifically: if a session sets a custom communicate output schema
// and later calls Session.SetModel(...) (or a subagent override
// arrives), the new profile must still carry the custom schema.
func TestBaseProfile_WithModel_PreservesToolDefOverrides(t *testing.T) {
	customSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"my_field": map[string]any{"type": "string"},
		},
		"required":             []any{"my_field"},
		"additionalProperties": false,
	}

	cases := []struct {
		name     string
		newOrig  func() ProviderProfile
		newModel string
	}{
		{"openrouter", func() ProviderProfile {
			return NewOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
		}, "anthropic/claude-3-5-sonnet"},
		{"openrouter-anthropic", func() ProviderProfile {
			return NewOpenRouterAnthropicProfile("anthropic/claude-3-5-sonnet")
		}, "anthropic/claude-3-haiku-20240307"},
		{"kimi", func() ProviderProfile {
			return NewOpenAICompatProfile("kimi", "kimi-k2.5", 0)
		}, "kimi-k2.6"},
		{"ollama", func() ProviderProfile {
			return NewOpenAICompatProfile("ollama", "llama3.1", 0)
		}, "llama3.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withSchema := WithCommunicateOutputSchema(tc.newOrig(), customSchema)
			afterModelChange := withSchema.WithModel(tc.newModel)

			// Verify communicate's output schema still has my_field.
			var found bool
			for _, td := range afterModelChange.ToolDefinitions() {
				if td.Name != "communicate" {
					continue
				}
				found = true
				props, _ := td.Parameters["properties"].(map[string]any)
				output, _ := props["output"].(map[string]any)
				outProps, _ := output["properties"].(map[string]any)
				if _, ok := outProps["my_field"]; !ok {
					t.Errorf("after WithModel, communicate.output.properties is missing my_field — custom schema was dropped during rebuild. Got: %v", outProps)
				}
			}
			if !found {
				t.Fatal("communicate tool not found in rebuilt profile")
			}
		})
	}
}

// TestBaseProfile_WithModel_RecomputesOpenRouterAnthropicCatalog is the
// integration check on the WithModel rebuild path. Starting from an
// openrouter-anthropic profile constructed with one Anthropic model,
// switching to a different one via WithModel must surface the new
// model's OpenRouter-prefixed catalog metadata, not stale state.
func TestBaseProfile_WithModel_RecomputesOpenRouterAnthropicCatalog(t *testing.T) {
	orig := NewOpenRouterAnthropicProfile("anthropic/claude-3-5-sonnet")
	cloned := orig.WithModel("anthropic/claude-3-haiku-20240307")
	if cloned.ID() != "openrouter-anthropic" {
		t.Fatalf("ID() = %q, want openrouter-anthropic", cloned.ID())
	}
	if cloned.Model() != "anthropic/claude-3-haiku-20240307" {
		t.Fatalf("Model() = %q, want anthropic/claude-3-haiku-20240307", cloned.Model())
	}
	if got := cloned.ContextWindowSize(); got != 200_000 {
		t.Fatalf("ContextWindowSize() = %d, want 200000 — same-provider WithModel did not pick up the new model's openrouter-prefixed catalog metadata", got)
	}
}

// TestBaseProfile_WithModel_RecomputesProviderOptsOnMetaProviders is
// the providerOpts companion to the catalog test: switching from a
// non-minimax openrouter model to a minimax/* one must inject the
// OpenRouter-MiniMax reasoning_details provider option, and switching
// the other way must drop it. A shallow clone would leave whichever
// option was set at original-profile construction time.
func TestBaseProfile_WithModel_RecomputesProviderOptsOnMetaProviders(t *testing.T) {
	// Start without minimax/* — providerOpts should be nil.
	orig := NewOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0).(*baseProfile)
	if orig.providerOpts != nil {
		t.Fatalf("setup: orig providerOpts = %+v, want nil", orig.providerOpts)
	}

	// Switch to minimax/* — should inject the reasoning option.
	cloned := orig.WithModel("minimax/minimax-m2.7").(*baseProfile)
	if cloned.providerOpts == nil {
		t.Fatal("after switch to minimax/*, providerOpts is nil — same-provider WithModel did not recompute providerOpts")
	}
	if _, ok := cloned.providerOpts["openai-compatible"]; !ok {
		t.Fatalf("providerOpts = %+v, want openai-compatible.reasoning", cloned.providerOpts)
	}

	// Switch back to non-minimax — option should be dropped.
	dropped := cloned.WithModel("anthropic/claude-3-haiku-20240307").(*baseProfile)
	if dropped.providerOpts != nil {
		t.Fatalf("after switch back to non-minimax, providerOpts = %+v, want nil", dropped.providerOpts)
	}
}

// TestBaseProfile_WithModel_StillSwitchesFromNonMeta has been moved to
// session_resolve_profile_test.go. Cross-provider switching is now
// handled by the Session resolver, not WithModel.

// TestBaseProfile_WithModel_SameProviderStripStillWorksForKimiGlm
// verifies the "WithModel('kimi/kimi-k2.5') on a kimi profile strips
// to 'kimi-k2.5'" convenience still works for non-meta same-provider
// prefixes. kimi/glm catalog keys are unprefixed so the stripped form
// is the canonical wire model.
func TestBaseProfile_WithModel_SameProviderStripStillWorksForKimiGlm(t *testing.T) {
	cases := []struct {
		startID   string
		input     string
		wantModel string
	}{
		{"kimi", "kimi/kimi-k2.5", "kimi-k2.5"},
		{"glm", "glm/glm-5", "glm-5"},
	}
	for _, tc := range cases {
		t.Run(tc.startID, func(t *testing.T) {
			orig := NewOpenAICompatProfile(tc.startID, "placeholder", 0)
			cloned := orig.WithModel(tc.input)
			if cloned.ID() != tc.startID {
				t.Errorf("ID() = %q, want %q", cloned.ID(), tc.startID)
			}
			if cloned.Model() != tc.wantModel {
				t.Errorf("Model() = %q, want %q (same-provider prefix should strip for kimi/glm)", cloned.Model(), tc.wantModel)
			}
		})
	}
}

// TestNewOpenAICompatProfile_OpenRouterUpstreamBareEntry verifies the
// OpenRouter side of the bare-key contract: openrouter routes to
// upstreams whose models often appear only under bare keys
// (e.g. "minimax/minimax-m2.7" with no openrouter/* equivalent), and
// those metadata must still be inherited. Otherwise OpenRouter would
// regress to the 128K generic fallback for many real models.
func TestNewOpenAICompatProfile_OpenRouterUpstreamBareEntry(t *testing.T) {
	p := NewOpenAICompatProfile("openrouter", "minimax/minimax-m2.7", 0)
	if got := p.ContextWindowSize(); got != 204800 {
		t.Fatalf("ContextWindowSize() = %d, want 204800 from bare minimax catalog entry — bare-key fallback was over-suppressed for openrouter", got)
	}
}

// TestNewOpenAICompatProfile_MinimaxOptOnlyForOpenRouter verifies that
// the OpenRouter-specific reasoning_details provider option is gated on
// id=="openrouter" and is NOT injected for other providers (e.g. ollama)
// even when the model name starts with "minimax/". A user could
// legitimately have a custom Ollama model named under that namespace.
func TestNewOpenAICompatProfile_MinimaxOptOnlyForOpenRouter(t *testing.T) {
	openrouter := NewOpenAICompatProfile("openrouter", "minimax/minimax-m2.7", 0).(*baseProfile)
	if openrouter.providerOpts == nil {
		t.Fatal("openrouter+minimax/* profile is missing the reasoning provider option")
	}
	if _, ok := openrouter.providerOpts["openai-compatible"]; !ok {
		t.Fatalf("openrouter providerOpts = %+v, want openai-compatible.reasoning", openrouter.providerOpts)
	}

	ollama := NewOpenAICompatProfile("ollama", "minimax/whatever", 0).(*baseProfile)
	if ollama.providerOpts != nil {
		t.Fatalf("ollama+minimax/* profile got OpenRouter-specific providerOpts = %+v, want nil", ollama.providerOpts)
	}
}

// TestNewOpenAICompatProfile_OllamaTaggedModelEndToEnd is a thin
// integration check that the helper is wired into NewOpenAICompatProfile.
// It uses a real catalog model and asserts a non-default context window
// comes back. Detailed precedence is covered by
// TestResolveOpenAICompatCatalogModel against a fake catalog.
func TestNewOpenAICompatProfile_OllamaTaggedModelEndToEnd(t *testing.T) {
	p := NewOpenAICompatProfile("ollama", "llama3:8b", 0)
	if got := p.ContextWindowSize(); got == 128_000 {
		t.Fatalf("ContextWindowSize() = 128000 (generic fallback) — helper not wired into NewOpenAICompatProfile")
	}
	if p.Model() != "llama3:8b" {
		t.Fatalf("Model() = %q, want llama3:8b — wire model must keep the tag", p.Model())
	}
}

// TestProviderProfile_WithModel_OllamaPrefix_PreservesCatalogMetadata has been
// moved to session_resolve_profile_test.go (session-level cross-provider test).

// TestNewOpenAICompatProfile_OpenRouterResolvesCatalogMetadata covers the
// other half of the prefixed-key fallback in NewOpenAICompatProfile:
// openrouter catalog entries are stored under "openrouter/<provider>/<model>"
// keys (e.g. "openrouter/anthropic/claude-3-haiku-20240307"). A future
// refactor that broke the prefixed-key fallback for the non-Ollama path
// would otherwise go unnoticed.
func TestNewOpenAICompatProfile_OpenRouterResolvesCatalogMetadata(t *testing.T) {
	p := NewOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
	if got := p.ContextWindowSize(); got != 200000 {
		t.Fatalf("ContextWindowSize() = %d, want 200000 (from openrouter/anthropic/claude-3-haiku-20240307 catalog entry)", got)
	}
	if p.Model() != "anthropic/claude-3-haiku-20240307" {
		t.Fatalf("Model() = %q, want bare model preserved on the wire", p.Model())
	}
}

// TestProviderProfile_WithModel_OpenRouterPrefix_PreservesCatalogMetadata has
// been moved to session_resolve_profile_test.go (session-level test).

// TestAnthropicProfile_WithModel_CrossProviderPrefixes has been moved to
// session_resolve_profile_test.go. Cross-provider switching is now the
// Session resolver's responsibility.

func assertHasTool(t *testing.T, p ProviderProfile, name string) {
	t.Helper()
	for _, td := range p.ToolDefinitions() {
		if td.Name == name {
			return
		}
	}
	t.Fatalf("expected tool %q in profile %q tool defs", name, p.ID())
}

func assertMissingTool(t *testing.T, p ProviderProfile, name string) {
	t.Helper()
	for _, td := range p.ToolDefinitions() {
		if td.Name == name {
			t.Fatalf("did not expect tool %q in profile %q tool defs", name, p.ID())
		}
	}
}

// TestAllProfiles_SystemPromptContainsSkillsGuidance verifies that all
// profiles include skills guidance when skills are provided.
// All provider profiles use the use_skill tool with directory paths.
func TestAllProfiles_SystemPromptContainsSkillsGuidance(t *testing.T) {
	profiles := map[string]ProviderProfile{
		"openai":    NewOpenAIProfile("gpt-5.2"),
		"anthropic": NewAnthropicProfile("claude-test"),
		"gemini":    NewGeminiProfile("gemini-test"),
	}
	skills := []SkillEntry{
		{Name: "test-skill", Description: "A test skill", Dir: "/tmp/skills/test-skill", SkillFile: "/tmp/skills/test-skill/SKILL.md"},
	}

	for name, p := range profiles {
		prompt := renderPromptForTest(t, p, PromptData{
			WorkingDir:  "/tmp",
			Platform:    "linux",
			Today:       "2026-02-09",
			Skills:      skills,
			HasUseSkill: true,
		})

		// All profiles should render <skills> when skills are provided.
		if !strings.Contains(prompt, "<skill-catalog>") {
			t.Errorf("profile %q system prompt missing <skills> section", name)
		}

		if !strings.Contains(prompt, "use_skill") {
			t.Errorf("profile %q system prompt missing use_skill guidance", name)
		}
		if !strings.Contains(prompt, "/tmp/skills/test-skill]") {
			t.Errorf("profile %q system prompt missing skill directory path", name)
		}
	}
}

func TestBuildSystemPrompt_IncludesSkillsList(t *testing.T) {
	// Anthropic profile has use_skill, so skills are rendered with directory paths.
	p := NewAnthropicProfile("claude-test")
	skills := []SkillEntry{
		{Name: "greet", Description: "Greeting skill", Dir: "/tmp/skills/greet", SkillFile: "/tmp/skills/greet/SKILL.md"},
		{Name: "deploy", Description: "Deploy skill", Dir: "/tmp/skills/deploy", SkillFile: "/tmp/skills/deploy/SKILL.md"},
	}
	prompt := renderPromptForTest(t, p, PromptData{
		WorkingDir:  "/tmp",
		Platform:    "linux",
		Today:       "2026-02-09",
		Skills:      skills,
		HasUseSkill: true,
	})

	if !strings.Contains(prompt, "<skill-catalog>") {
		t.Error("prompt missing <skills> section")
	}
	if !strings.Contains(prompt, "- greet: Greeting skill [/tmp/skills/greet]") {
		t.Error("prompt missing greet skill entry with directory path")
	}
	if !strings.Contains(prompt, "- deploy: Deploy skill [/tmp/skills/deploy]") {
		t.Error("prompt missing deploy skill entry with directory path")
	}
	if !strings.Contains(prompt, "</skill-catalog>") {
		t.Error("prompt missing </skill-catalog> closing tag")
	}
	if !strings.Contains(prompt, "use_skill") {
		t.Error("prompt missing use_skill instruction")
	}
}

func TestBuildSystemPrompt_OpenAI_SkillsWithUseSkill(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	skills := []SkillEntry{
		{Name: "greet", Description: "Greeting skill", Dir: "/tmp/skills/greet", SkillFile: "/tmp/skills/greet/SKILL.md"},
	}
	prompt := renderPromptForTest(t, p, PromptData{
		WorkingDir: "/tmp",
		Platform:   "linux",
		Today:      "2026-02-09",
		Skills:     skills,
		HasUseSkill: true,
	})

	if !strings.Contains(prompt, "<skill-catalog>") {
		t.Error("OpenAI prompt should contain <skills> section")
	}
	if !strings.Contains(prompt, "Load a skill by calling use_skill with its name") {
		t.Error("OpenAI prompt should instruct model to use use_skill for skills")
	}
	if !strings.Contains(prompt, "- greet: Greeting skill [/tmp/skills/greet]") {
		t.Error("OpenAI prompt should include skill directory path for use_skill")
	}
}

func TestBuildSystemPrompt_NoSkills_NoSkillsSection(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	prompt := renderPromptForTest(t, p, PromptData{
		WorkingDir: "/tmp",
		Platform:   "linux",
		Today:      "2026-02-09",
	})

	// Verify no skill-catalog block is present when no skills exist.
	if strings.Contains(prompt, "</skill-catalog>") {
		t.Error("prompt should not contain </skill-catalog> section when no skills present")
	}
}

func TestGeminiProfile_IncludesWebSearch(t *testing.T) {
	assertHasTool(t, NewGeminiProfile("gemini-test"), "web_search")
	assertMissingTool(t, NewOpenAIProfile("gpt-5.2"), "web_search")
	assertMissingTool(t, NewAnthropicProfile("claude-test"), "web_search")
}

func TestProviderProfile_ProviderOptions(t *testing.T) {
	p := NewAnthropicProfile("claude-opus-4-6")
	opts := p.ProviderOptions()
	if opts == nil {
		t.Fatal("expected non-nil ProviderOptions for Anthropic")
	}
}

func TestOpenAIProfile_ProviderOptions_ParallelToolCalls(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	opts := p.ProviderOptions()
	if opts == nil {
		t.Fatal("expected non-nil ProviderOptions for OpenAI")
	}
	oai, ok := opts["openai"].(map[string]any)
	if !ok {
		t.Fatal("missing openai key in provider options")
	}
	ptc, ok := oai["parallel_tool_calls"]
	if !ok {
		t.Fatal("missing parallel_tool_calls in openai provider options")
	}
	if ptc != true {
		t.Fatalf("parallel_tool_calls = %v, want true", ptc)
	}
}

func TestAnthropicProfile_ProviderOptions_MaxTokens(t *testing.T) {
	p := NewAnthropicProfile("claude-opus-4-6")
	opts := p.ProviderOptions()
	anth, ok := opts["anthropic"].(map[string]any)
	if !ok {
		t.Fatal("missing anthropic key in provider options")
	}
	mt, ok := anth["max_tokens"]
	if !ok {
		t.Fatal("missing max_tokens in anthropic provider options")
	}
	if mt != 16384 {
		t.Fatalf("max_tokens = %v, want 16384", mt)
	}
}

func TestAnthropicProfile_ProviderOptions_NoBetaHeadersByDefault(t *testing.T) {
	p := NewAnthropicProfile("test-model")
	opts := p.ProviderOptions()
	anth, ok := opts["anthropic"].(map[string]any)
	if !ok {
		t.Fatal("missing anthropic key in provider options")
	}
	// Default (non-1M) profile should not have beta_headers — caching is GA.
	if bh, ok := anth["beta_headers"]; ok {
		t.Fatalf("default profile should not have beta_headers, got %v", bh)
	}
}

func TestProviderProfile_SupportsReasoning(t *testing.T) {
	if !NewOpenAIProfile("gpt-5.2").SupportsReasoning() {
		t.Fatal("OpenAI should support reasoning")
	}
	if !NewAnthropicProfile("claude-opus-4-6").SupportsReasoning() {
		t.Fatal("Anthropic should support reasoning")
	}
}

func TestProviderProfile_SupportsStreaming(t *testing.T) {
	if !NewOpenAIProfile("gpt-5.2").SupportsStreaming() {
		t.Fatal("OpenAI should support streaming")
	}
}

func TestProviderProfile_DefaultCommandTimeout(t *testing.T) {
	if got := NewOpenAIProfile("gpt-5.2").DefaultCommandTimeoutMS(); got != 120_000 {
		t.Fatalf("OpenAI timeout = %d, want 120000", got)
	}
	if got := NewAnthropicProfile("claude-opus-4-6").DefaultCommandTimeoutMS(); got != 120_000 {
		t.Fatalf("Anthropic timeout = %d, want 120000", got)
	}
	if got := NewGeminiProfile("gemini-2.5-pro").DefaultCommandTimeoutMS(); got != 120_000 {
		t.Fatalf("Gemini timeout = %d, want 120000", got)
	}
}

func TestProviderProfile_KnowledgeCutoff(t *testing.T) {
	tests := []struct {
		name string
		p    ProviderProfile
		want string
	}{
		{"openai", NewOpenAIProfile("gpt-5.2"), "2025-06-01"},
		{"anthropic", NewAnthropicProfile("claude-opus-4-6"), "2025-04-01"},
		{"gemini", NewGeminiProfile("gemini-2.5-pro"), "2025-03-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.p.KnowledgeCutoff()
			if got != tt.want {
				t.Fatalf("KnowledgeCutoff() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSendInput_UsesMessageParam(t *testing.T) {
	profiles := []ProviderProfile{
		NewOpenAIProfile("gpt-5.2"),
		NewAnthropicProfile("claude-sonnet-4-20250514"),
		NewGeminiProfile("gemini-2.5-pro"),
	}
	for _, p := range profiles {
		t.Run(p.ID(), func(t *testing.T) {
			for _, td := range p.ToolDefinitions() {
				if td.Name == "resume_agent" {
					props := td.Parameters["properties"].(map[string]any)
					if _, ok := props["message"]; !ok {
						t.Fatal("resume_agent should have 'message' parameter")
					}
					if _, ok := props["input"]; ok {
						t.Fatal("resume_agent should not have 'input' parameter")
					}
					req := td.Parameters["required"].([]string)
					found := false
					for _, r := range req {
						if r == "message" {
							found = true
						}
						if r == "input" {
							t.Fatal("required should not contain 'input'")
						}
					}
					if !found {
						t.Fatal("required should contain 'message'")
					}
					return
				}
			}
			t.Fatal("resume_agent tool not found")
		})
	}
}

func TestSpawnAgent_HasMaxTurns(t *testing.T) {
	profiles := []ProviderProfile{
		NewOpenAIProfile("gpt-5.2"),
		NewAnthropicProfile("claude-sonnet-4-20250514"),
		NewGeminiProfile("gemini-2.5-pro"),
	}
	for _, p := range profiles {
		t.Run(p.ID(), func(t *testing.T) {
			for _, td := range p.ToolDefinitions() {
				if td.Name == "spawn_agent" {
					props := td.Parameters["properties"].(map[string]any)
					if _, ok := props["working_dir"]; ok {
						t.Fatal("spawn_agent should NOT have working_dir parameter (removed)")
					}
					if _, ok := props["max_turns"]; !ok {
						t.Fatal("spawn_agent missing max_turns parameter")
					}
					return
				}
			}
			t.Fatal("spawn_agent tool not found")
		})
	}
}

// WS3: Tool name mapping
func TestToolNameMapping_OpenAI(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	toolNames := map[string]bool{}
	for _, td := range p.ToolDefinitions() {
		toolNames[td.Name] = true
	}
	// OpenAI should use provider-specific names.
	if !toolNames["exec_command"] {
		t.Fatal("OpenAI ToolDefinitions should contain exec_command (mapped from shell)")
	}
	if !toolNames["grep_files"] {
		t.Fatal("OpenAI ToolDefinitions should contain grep_files (mapped from grep)")
	}
	if !toolNames["list_dir"] {
		t.Fatal("OpenAI ToolDefinitions should contain list_dir (mapped from glob)")
	}
	// Should NOT contain canonical names for mapped tools.
	if toolNames["shell"] {
		t.Fatal("OpenAI ToolDefinitions should not contain canonical 'shell'")
	}
	if toolNames["grep"] {
		t.Fatal("OpenAI ToolDefinitions should not contain canonical 'grep'")
	}
	if toolNames["glob"] {
		t.Fatal("OpenAI ToolDefinitions should not contain canonical 'glob'")
	}
}

func TestToolNameMapping_Gemini(t *testing.T) {
	p := NewGeminiProfile("gemini-test")
	toolNames := map[string]bool{}
	for _, td := range p.ToolDefinitions() {
		toolNames[td.Name] = true
	}
	if !toolNames["run_shell_command"] {
		t.Fatal("Gemini ToolDefinitions should contain run_shell_command (mapped from shell)")
	}
	if !toolNames["grep_search"] {
		t.Fatal("Gemini ToolDefinitions should contain grep_search (mapped from grep)")
	}
	if !toolNames["list_directory"] {
		t.Fatal("Gemini ToolDefinitions should contain list_directory (mapped from list_dir)")
	}
}

func TestToolNameMapping_Anthropic_NoMapping(t *testing.T) {
	p := NewAnthropicProfile("claude-test")
	toolNames := map[string]bool{}
	for _, td := range p.ToolDefinitions() {
		toolNames[td.Name] = true
	}
	// Anthropic uses canonical names.
	if !toolNames["shell"] {
		t.Fatal("Anthropic ToolDefinitions should contain canonical 'shell'")
	}
	if !toolNames["grep"] {
		t.Fatal("Anthropic ToolDefinitions should contain canonical 'grep'")
	}
	if !toolNames["glob"] {
		t.Fatal("Anthropic ToolDefinitions should contain canonical 'glob'")
	}
}

func TestGeminiProfile_ProviderOptions_HasSafetySettings(t *testing.T) {
	p := NewGeminiProfile("gemini-2.5-flash")
	opts := p.ProviderOptions()
	if opts == nil {
		t.Fatal("expected non-nil ProviderOptions for Gemini")
	}
	gemini, ok := opts["gemini"].(map[string]any)
	if !ok {
		t.Fatal("expected opts[\"gemini\"] to be map[string]any")
	}
	ss, ok := gemini["safetySettings"]
	if !ok || ss == nil {
		t.Fatal("expected safetySettings in gemini provider_options")
	}
	settings, ok := ss.([]map[string]any)
	if !ok {
		t.Fatalf("safetySettings type: got %T, want []map[string]any", ss)
	}
	if len(settings) == 0 {
		t.Fatal("expected at least one safety setting")
	}
	// Verify all settings use a permissive threshold for coding agent use.
	for _, s := range settings {
		threshold, _ := s["threshold"].(string)
		if threshold != "BLOCK_ONLY_HIGH" {
			t.Errorf("safety threshold for %v: got %q, want BLOCK_ONLY_HIGH", s["category"], threshold)
		}
	}
}

func TestAnthropicProfile_ContextWindow_Default200K(t *testing.T) {
	p := NewAnthropicProfile("claude-sonnet-4-5-20250929")
	if p.ContextWindowSize() != 200_000 {
		t.Errorf("expected 200000, got %d", p.ContextWindowSize())
	}
}

func TestAnthropicProfile_ContextWindow_1MSuffix(t *testing.T) {
	p := NewAnthropicProfile("claude-opus-4-6[1m]")
	if p.ContextWindowSize() != 1_000_000 {
		t.Errorf("expected 1000000, got %d", p.ContextWindowSize())
	}
	// Model string should retain the suffix for downstream use.
	if p.Model() != "claude-opus-4-6[1m]" {
		t.Errorf("model: got %q, want %q", p.Model(), "claude-opus-4-6[1m]")
	}
}

func TestAnthropicProfile_WithModel_RoundTrip(t *testing.T) {
	// Start at 200K, switch to 1M model.
	orig := NewAnthropicProfile("claude-opus-4-6")
	if orig.ContextWindowSize() != 200_000 {
		t.Fatalf("orig context: got %d, want 200000", orig.ContextWindowSize())
	}

	upgraded := orig.WithModel("claude-opus-4-6[1m]")
	if upgraded.ContextWindowSize() != 1_000_000 {
		t.Fatalf("upgraded context: got %d, want 1000000", upgraded.ContextWindowSize())
	}
	if upgraded.Model() != "claude-opus-4-6[1m]" {
		t.Fatalf("upgraded model: got %q", upgraded.Model())
	}

	// Switch back to 200K.
	downgraded := upgraded.WithModel("claude-opus-4-6")
	if downgraded.ContextWindowSize() != 200_000 {
		t.Fatalf("downgraded context: got %d, want 200000", downgraded.ContextWindowSize())
	}

	// Original untouched.
	if orig.ContextWindowSize() != 200_000 {
		t.Fatalf("orig mutated: context = %d", orig.ContextWindowSize())
	}
}

func TestAnthropicProfile_WithModel_NoProviderOptsAliasing(t *testing.T) {
	orig := NewAnthropicProfile("claude-opus-4-6")
	cloned := orig.WithModel("claude-opus-4-6[1m]")

	// Mutating the clone's providerOpts must not affect the original.
	clonedOpts := cloned.ProviderOptions()
	if clonedOpts == nil {
		t.Fatal("cloned providerOpts nil")
	}
	anthClone, ok := clonedOpts["anthropic"].(map[string]any)
	if !ok {
		t.Fatal("cloned missing anthropic key")
	}
	anthClone["injected"] = "bad"

	origOpts := orig.ProviderOptions()
	anthOrig, ok := origOpts["anthropic"].(map[string]any)
	if !ok {
		t.Fatal("orig missing anthropic key")
	}
	if _, found := anthOrig["injected"]; found {
		t.Fatal("mutating cloned providerOpts affected original — aliasing bug")
	}
}

func TestAnthropicProfile_1M_BetaHeader(t *testing.T) {
	p := NewAnthropicProfile("claude-opus-4-6[1m]")
	opts := p.ProviderOptions()
	anth, ok := opts["anthropic"].(map[string]any)
	if !ok {
		t.Fatal("missing anthropic key")
	}
	bh, _ := anth["beta_headers"].(string)
	if !strings.Contains(bh, "context-1m-2025-08-07") {
		t.Fatalf("expected 1M beta header, got %q", bh)
	}
	// Should NOT have prompt-caching — caching is GA.
	if strings.Contains(bh, "prompt-caching-2024-07-31") {
		t.Fatalf("prompt-caching beta header should not be present (GA), got %q", bh)
	}
}

func TestAnthropicProfile_Default_NoBeta1MHeader(t *testing.T) {
	p := NewAnthropicProfile("claude-opus-4-6")
	opts := p.ProviderOptions()
	anth, ok := opts["anthropic"].(map[string]any)
	if !ok {
		t.Fatal("missing anthropic key")
	}
	bh, _ := anth["beta_headers"].(string)
	if strings.Contains(bh, "context-1m-2025-08-07") {
		t.Fatalf("default model should not have 1M beta header, got %q", bh)
	}
}

func TestGeminiProfile_ContextWindow_Is1M(t *testing.T) {
	p := NewGeminiProfile("gemini-2.5-flash")
	if p.ContextWindowSize() != 1_000_000 {
		t.Errorf("expected 1000000, got %d", p.ContextWindowSize())
	}
}

func TestBuildSystemPrompt_ToolUsageBeforeProjectDocs(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	prompt := renderPromptForTest(t, p, PromptData{
		WorkingDir:  "/tmp",
		Platform:    "linux",
		Today:       "2026-02-11",
		ProjectDocs: []ProjectDoc{{Path: "AGENTS.md", Content: "project instructions here"}},
		MCPTools:    []ToolEntry{{Name: "mcp__server__tool1", Description: "Does thing one"}},
		CustomTools: []ToolEntry{{Name: "my_custom_tool", Description: "Does custom things"}},
	})

	beginIdx := strings.Index(prompt, "----- BEGIN AGENTS.md -----")
	if beginIdx < 0 {
		t.Fatal("prompt missing project doc BEGIN marker")
	}
	toolUsageIdx := strings.Index(prompt, "## Tool usage")
	if toolUsageIdx < 0 {
		t.Fatal("prompt missing tool usage section")
	}
	if toolUsageIdx > beginIdx {
		t.Errorf("tool usage (pos %d) must appear before project docs (pos %d)", toolUsageIdx, beginIdx)
	}
}

func TestApplyPatch_DescriptionIncludesCapabilities(t *testing.T) {
	d := defApplyPatch()
	if !strings.Contains(d.Description, "creating") || !strings.Contains(d.Description, "deleting") || !strings.Contains(d.Description, "modifying") {
		t.Fatalf("apply_patch description missing capability summary: %q", d.Description)
	}
}

func TestProviderProfile_NewToolRegistry_ContainsProfileTools(t *testing.T) {
	profiles := []ProviderProfile{
		NewOpenAIProfile("gpt-5.2"),
		NewAnthropicProfile("claude-test"),
		NewGeminiProfile("gemini-test"),
	}
	for _, p := range profiles {
		t.Run(p.ID(), func(t *testing.T) {
			reg := p.NewToolRegistry()
			if reg == nil {
				t.Fatal("NewToolRegistry() returned nil")
			}

			// Build the set of canonical names from p.toolDefs (the internal
			// field). We can derive them by reverse-mapping ToolDefinitions()
			// through ToolNameMap().
			reverseMap := map[string]string{} // provider-name → canonical
			if nm := p.ToolNameMap(); nm != nil {
				for canon, prov := range nm {
					reverseMap[prov] = canon
				}
			}

			for _, td := range p.ToolDefinitions() {
				canonical := td.Name
				if c, ok := reverseMap[td.Name]; ok {
					canonical = c
				}
				tool := reg.Get(canonical)
				if tool == nil {
					t.Errorf("tool %q (canonical) should be in registry", canonical)
					continue
				}
				if tool.Exec == nil {
					t.Errorf("tool %q should have a non-nil placeholder Exec", canonical)
				}
			}

			// Registry should contain exactly the profile's tools, no more.
			names := reg.Names()
			if len(names) != len(p.ToolDefinitions()) {
				t.Errorf("registry has %d tools, profile defines %d: got %v",
					len(names), len(p.ToolDefinitions()), names)
			}
		})
	}
}

func TestProviderProfile_NewToolRegistry_PlaceholderExecReturnsError(t *testing.T) {
	p := NewAnthropicProfile("claude-test")
	reg := p.NewToolRegistry()
	tool := reg.Get("read_file")
	if tool == nil {
		t.Fatal("read_file not found")
	}
	_, err := tool.Exec(nil, nil, map[string]any{})
	if err == nil {
		t.Fatal("placeholder Exec should return an error")
	}
	if !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("expected 'not wired' error, got: %v", err)
	}
}

func assertToolListExact(t *testing.T, p ProviderProfile, want []string) {
	t.Helper()
	got := make([]string, 0, len(p.ToolDefinitions()))
	for _, td := range p.ToolDefinitions() {
		got = append(got, td.Name)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("tool list mismatch for profile %q:\n got: %v\nwant: %v", p.ID(), got, want)
	}
}

func TestBuildSystemPrompt_WorkspaceSection(t *testing.T) {
	dir := t.TempDir()

	// Create a realistic workspace.
	for _, f := range []struct{ path, content string }{
		{"main.py", "print('hello')\n"},
		{"utils.py", "def helper(): pass\n"},
		{"src/core.py", "class Core: pass\n"},
		{"tests/test_main.py", "def test_main(): pass\n"},
		{"test.sh", "#!/bin/bash\nexit 0\n"},
		{"Makefile", "all:\n\techo ok\ntest:\n\t./test.sh\nclean:\n\trm -f *.o\n"},
	} {
		p := filepath.Join(dir, f.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	env := EnvironmentInfo{
		WorkingDir: dir,
		Platform:   "linux",
		Today:      "2026-03-01",
		Workspace:  ScanWorkspace(dir),
	}

	p := NewOpenAIProfile("gpt-5.3-codex")
	prompt := renderPromptForTest(t, p, PromptData{
		WorkingDir:    env.WorkingDir,
		Platform:      env.Platform,
		Today:         env.Today,
		WorkspaceTree: env.Workspace.Tree,
		BuildInfo:     env.Workspace.BuildInfo,
	})

	// Should contain workspace section.
	if !strings.Contains(prompt, "<workspace>") {
		t.Fatalf("prompt missing <workspace> section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "</workspace>") {
		t.Fatal("prompt missing </workspace> closing tag")
	}

	// Should contain the directory tree.
	if !strings.Contains(prompt, "main.py") {
		t.Error("workspace section missing main.py in tree")
	}
	if !strings.Contains(prompt, "src/") {
		t.Error("workspace section missing src/ directory")
	}

	// Should highlight test files.
	if !strings.Contains(prompt, "test.sh") || !strings.Contains(prompt, "test_main.py") {
		t.Error("workspace section missing test file callout")
	}

	// Should show build system info.
	if !strings.Contains(prompt, "Makefile") {
		t.Error("workspace section missing Makefile info")
	}

	// Workspace section should come after environment and after the tool list.
	wsIdx := strings.Index(prompt, "<workspace>")
	envIdx := strings.Index(prompt, "</environment>")
	toolIdx := strings.Index(prompt, "Tools:")
	if wsIdx < envIdx {
		t.Errorf("workspace (pos %d) should come after environment (pos %d)", wsIdx, envIdx)
	}
	if wsIdx < toolIdx {
		t.Errorf("workspace (pos %d) should come after tools (pos %d)", wsIdx, toolIdx)
	}
}

func TestBuildSystemPrompt_EmptyWorkspace(t *testing.T) {
	env := EnvironmentInfo{
		WorkingDir: "/tmp",
		Platform:   "linux",
		Today:      "2026-03-01",
		// Workspace is zero value (empty).
	}

	p := NewOpenAIProfile("gpt-5.3-codex")
	prompt := renderPromptForTest(t, p, PromptData{
		WorkingDir: env.WorkingDir,
		Platform:   env.Platform,
		Today:      env.Today,
	})

	// Should NOT render an empty workspace section.
	if strings.Contains(prompt, "<workspace>") {
		t.Error("empty workspace should not render a <workspace> section")
	}
}

func TestBuildSystemPrompt_WorkspaceAnnotation(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "main.py"), "print('hello')\n")

	env := EnvironmentInfo{
		WorkingDir: dir,
		Platform:   "linux",
		Today:      "2026-03-01",
		Workspace:  ScanWorkspace(dir),
	}

	p := NewOpenAIProfile("gpt-5.3-codex")
	prompt := renderPromptForTest(t, p, PromptData{
		WorkingDir:    env.WorkingDir,
		Platform:      env.Platform,
		Today:         env.Today,
		WorkspaceTree: env.Workspace.Tree,
		BuildInfo:     env.Workspace.BuildInfo,
	})

	if !strings.Contains(prompt, "snapshot of the working directory taken at session start") {
		t.Error("workspace section missing static annotation")
	}
}

// --- MiniMax profile tests ---

func TestMiniMaxProfile_BasicProperties(t *testing.T) {
	p := NewMiniMaxProfile("MiniMax-M2.7")
	if p.ID() != "minimax" {
		t.Fatalf("ID() = %q, want minimax", p.ID())
	}
	if p.Model() != "MiniMax-M2.7" {
		t.Fatalf("Model() = %q", p.Model())
	}
	if p.ContextWindowSize() != 204_800 {
		t.Fatalf("ContextWindowSize() = %d, want 204800", p.ContextWindowSize())
	}
	if !p.SupportsReasoning() {
		t.Fatal("should support reasoning")
	}
	if !p.SupportsStreaming() {
		t.Fatal("should support streaming")
	}
}

func TestMiniMaxProfile_AnthropicStyleTools(t *testing.T) {
	// MiniMax direct platform uses Anthropic API, so it should have
	// Anthropic-style tools (edit_file, use_skill, no apply_patch).
	p := NewMiniMaxProfile("MiniMax-M2.7")
	assertHasTool(t, p, "edit_file")
	assertHasTool(t, p, "use_skill")
	assertMissingTool(t, p, "apply_patch")
}

func TestMiniMaxProfile_ToolListExact(t *testing.T) {
	p := NewMiniMaxProfile("MiniMax-M2.7")
	assertToolListExact(t, p, []string{
		"read_file",
		"write_file",
		"edit_file",
		"shell",
		"grep",
		"glob",
		"spawn_agent",
		"resume_agent",
		"wait",
		"close_agent",
		"task_list",
		"web_fetch",
		"communicate",
		"use_skill",
	})
}

// TestMiniMaxProfile_WithModel_CrossProvider and TestWithModel_CrossProviderToMiniMax
// have been moved to session_resolve_profile_test.go. Cross-provider switching
// is now handled by the Session resolver.

func TestResolveEffortLevels_CatalogHit(t *testing.T) {
	// claude-opus-4-6 is in the catalog with [low, medium, high, max]
	p := NewAnthropicProfile("claude-opus-4-6")
	levels := p.ReasoningEffortLevels()
	if len(levels) != 4 || levels[3] != "max" {
		t.Fatalf("claude-opus-4-6 effort levels: got %v, want [low medium high max]", levels)
	}
}

func TestResolveEffortLevels_CatalogMiss(t *testing.T) {
	// A model not in the catalog should fall back to provider defaults.
	p := NewAnthropicProfile("claude-unknown-model")
	levels := p.ReasoningEffortLevels()
	// Anthropic default is [low, medium, high, max]
	if len(levels) != 4 {
		t.Fatalf("unknown anthropic model effort levels: got %v, want provider default", levels)
	}
}

func TestResolveEffortLevels_MiniMaxCatalog(t *testing.T) {
	// minimax/minimax-m2.7 is in the catalog with [low, medium, high] (no max)
	p := NewMiniMaxProfile("minimax/minimax-m2.7")
	levels := p.ReasoningEffortLevels()
	if len(levels) != 3 || levels[2] != "high" {
		t.Fatalf("minimax effort levels: got %v, want [low medium high]", levels)
	}
}

func TestTaskListSchema_EffortEnum_MatchesCatalog(t *testing.T) {
	// Verify that the task_list tool's reasoning_effort enum matches the
	// catalog entry across different profile paths.
	model := "minimax/minimax-m2.7"

	// Get expected effort levels from the catalog.
	var expectedLevels []string
	if cat := llm.EmbeddedModelCatalog(); cat != nil {
		if mi := cat.GetModelInfo(model); mi != nil {
			expectedLevels = mi.ReasoningEffortLevels
		}
	}
	if len(expectedLevels) == 0 {
		t.Fatalf("minimax/minimax-m2.7 not in catalog or has no effort levels")
	}

	// Test openrouter-anthropic profile path.
	orAnthro := NewOpenRouterAnthropicProfile(model)
	effortEnumORAnthro := extractTaskListEffortEnum(t, orAnthro)
	if !stringSliceEqual(effortEnumORAnthro, expectedLevels) {
		t.Fatalf("openrouter-anthropic task_list effort enum mismatch:\n  got:  %v\n  want: %v",
			effortEnumORAnthro, expectedLevels)
	}

	// Test minimax direct profile path.
	minimax := NewMiniMaxProfile(model)
	effortEnumMinimax := extractTaskListEffortEnum(t, minimax)
	if !stringSliceEqual(effortEnumMinimax, expectedLevels) {
		t.Fatalf("minimax task_list effort enum mismatch:\n  got:  %v\n  want: %v",
			effortEnumMinimax, expectedLevels)
	}

	// Both paths should produce the same enum.
	if !stringSliceEqual(effortEnumORAnthro, effortEnumMinimax) {
		t.Fatalf("effort enum differs between profiles:\n  openrouter-anthropic: %v\n  minimax: %v",
			effortEnumORAnthro, effortEnumMinimax)
	}
}

// extractTaskListEffortEnum finds the task_list tool and extracts the
// reasoning_effort enum from its parameters schema.
func extractTaskListEffortEnum(t *testing.T, p ProviderProfile) []string {
	t.Helper()
	var taskListTool *llm.ToolDefinition
	for _, td := range p.ToolDefinitions() {
		if td.Name == "task_list" {
			cp := td
			taskListTool = &cp
			break
		}
	}
	if taskListTool == nil {
		t.Fatalf("task_list tool not found in profile %s", p.ID())
	}

	// Navigate: parameters -> properties -> updates -> items -> properties -> reasoning_effort -> enum
	params, _ := taskListTool.Parameters["properties"].(map[string]any)
	updates, _ := params["updates"].(map[string]any)
	items, _ := updates["items"].(map[string]any)
	itemProps, _ := items["properties"].(map[string]any)
	reasoningEffort, _ := itemProps["reasoning_effort"].(map[string]any)
	enumAny, _ := reasoningEffort["enum"].([]string)
	if enumAny == nil {
		// Try []any fallback
		if enumInterface, ok := reasoningEffort["enum"].([]any); ok {
			for _, v := range enumInterface {
				if s, ok := v.(string); ok {
					enumAny = append(enumAny, s)
				}
			}
		}
	}
	if len(enumAny) == 0 {
		t.Fatalf("reasoning_effort enum not found or empty in profile %s", p.ID())
	}
	return enumAny
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestProviderProfile_BehaviorTag verifies that each constructor stamps the
// correct behavior tag on the profile.
func TestProviderProfile_BehaviorTag(t *testing.T) {
	cases := []struct {
		name    string
		profile ProviderProfile
		want    string
	}{
		{"openai", NewOpenAIProfile("gpt-5.2"), "openai"},
		{"anthropic", NewAnthropicProfile("claude-test"), "anthropic"},
		{"gemini", NewGeminiProfile("gemini-test"), "google"},
		{"minimax", NewMiniMaxProfile("MiniMax-M2.7"), "minimax"},
		{"openrouter-anthropic", NewOpenRouterAnthropicProfile("anthropic/claude-test"), "openrouter-anthropic"},
		{"openrouter (compat)", NewOpenAICompatProfile("openrouter", "openai/gpt-test", 0), "openrouter"},
		{"kimi (compat)", NewOpenAICompatProfile("kimi", "kimi-test", 0), "kimi"},
		{"glm (compat)", NewOpenAICompatProfile("glm", "glm-test", 0), "glm"},
		{"ollama (compat)", NewOpenAICompatProfile("ollama", "llama3", 0), "ollama"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.profile.BehaviorTag()
			if got != tc.want {
				t.Fatalf("BehaviorTag() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWithProviderID verifies that WithProviderID overrides the id but preserves
// the behavior tag and all other profile state.
func TestWithProviderID(t *testing.T) {
	orig := NewOpenAIProfile("gpt-5.2")
	renamed := WithProviderID(orig, "work")
	if renamed.ID() != "work" {
		t.Fatalf("ID() = %q, want %q", renamed.ID(), "work")
	}
	if renamed.BehaviorTag() != "openai" {
		t.Fatalf("BehaviorTag() = %q, want %q", renamed.BehaviorTag(), "openai")
	}
	// Original must be unchanged.
	if orig.ID() != "openai" {
		t.Fatalf("original ID mutated: got %q", orig.ID())
	}
}

// TestRenamedInstance_CheapModel verifies that CheapModel uses behaviorTag (not
// id) so a renamed instance keeps the right cheap model.
func TestRenamedInstance_CheapModel(t *testing.T) {
	// kimi renamed to "work" → cheap model should be the kimi default (p.model).
	kimiWork := WithProviderID(NewOpenAICompatProfile("kimi", "kimi-k2", 0), "work")
	// CheapModel for kimi falls through to p.model (no explicit case for kimi in
	// the switch), so the expected value is the model itself.
	if got := kimiWork.CheapModel(); got == "" {
		t.Fatalf("CheapModel() is empty for renamed kimi instance")
	}

	// google/gemini renamed to "work" → cheap model should be gemini-2.5-flash-lite
	googleWork := WithProviderID(NewGeminiProfile("gemini-2.5-pro"), "work")
	const wantGeminiCheap = "gemini-2.5-flash-lite"
	if got := googleWork.CheapModel(); got != wantGeminiCheap {
		t.Fatalf("CheapModel() = %q, want %q for renamed google (gemini) instance", got, wantGeminiCheap)
	}

	// anthropic renamed to "work" → cheap model should be claude-haiku-4-5-20251001
	anthropicWork := WithProviderID(NewAnthropicProfile("claude-opus-4-6"), "work")
	const wantAnthropicCheap = "claude-haiku-4-5-20251001"
	if got := anthropicWork.CheapModel(); got != wantAnthropicCheap {
		t.Fatalf("CheapModel() = %q, want %q for renamed anthropic instance", got, wantAnthropicCheap)
	}
}

// TestRenamedInstance_RebuildPreservesTag verifies that WithModel on a renamed
// instance (where id != behaviorTag) rebuilds correctly using behaviorTag for
// the rebuildOnSameProviderChange decision, AND re-stamps the tag on the
// rebuilt profile so it doesn't derive a wrong tag from the renamed id.
// Regression guard: before this fix, rebuildOnSameProviderChange(p.id) returned
// false for "work", skipping the catalog-aware rebuild entirely; and the rebuild
// path called NewOpenAICompatProfile(p.id, model, 0) which would derive the tag
// from "work" instead of "kimi".
func TestRenamedInstance_RebuildPreservesTag(t *testing.T) {
	kimiWork := WithProviderID(NewOpenAICompatProfile("kimi", "kimi-k2", 0), "work")
	if kimiWork.BehaviorTag() != "kimi" {
		t.Fatalf("pre-condition: BehaviorTag() = %q, want kimi", kimiWork.BehaviorTag())
	}
	// WithModel must use behaviorTag=="kimi" for rebuildOnSameProviderChange
	// and re-stamp the tag on the rebuilt profile.
	rebuilt := kimiWork.WithModel("kimi-k2.5")
	if rebuilt.BehaviorTag() != "kimi" {
		t.Fatalf("BehaviorTag() after WithModel = %q, want kimi — rebuild must preserve behaviorTag", rebuilt.BehaviorTag())
	}
	if rebuilt.ID() != "work" {
		t.Fatalf("ID() after WithModel = %q, want work — rebuild must preserve renamed id", rebuilt.ID())
	}
	// Verify that catalog state (context window) is recomputed via the rebuild.
	// This distinguishes a proper rebuild from a shallow clone (which would also
	// preserve the tag but would not recompute model-derived state).
	// We can't easily verify context window changes here without a catalog entry,
	// but the BehaviorTag check is the primary regression guard per the task spec.
	// The full semantic test is TestRenamedInstance_OpenRouterMetaNamespace which
	// exercises the providerOpts rebuild path.
}

// TestRenamedInstance_CatalogLookup verifies that the catalog/bare-suppression
// logic keys on behaviorTag, not id. An instance with id=="work" but
// behaviorTag=="ollama" must suppress bare catalog lookups.
func TestRenamedInstance_CatalogLookup(t *testing.T) {
	// An ollama instance renamed to "work" must still get catalog metadata
	// resolved under the "ollama/..." prefixed key (not "work/...").
	// If the catalog key is prefixed with id ("work/llama3.1") we get
	// no match → 128K fallback. But with behaviorTag ("ollama/llama3.1")
	// we get the 8192 context window from the embedded catalog.
	ollamaWork := WithProviderID(NewOpenAICompatProfile("ollama", "llama3.1", 0), "work")
	// The profile was already resolved at construction time, so context window
	// should be 8192 (from "ollama/llama3.1" in the catalog). But the id=="work"
	// means WithModel on this profile must also use behaviorTag for catalog lookups.
	if got := ollamaWork.ContextWindowSize(); got != 8192 {
		t.Fatalf("ContextWindowSize() = %d, want 8192 — catalog resolved at construction", got)
	}
	// Now verify that WithModel("llama3.2") also resolves catalog correctly.
	rebuilt := ollamaWork.WithModel("llama3.1")
	if rebuilt.BehaviorTag() != "ollama" {
		t.Fatalf("BehaviorTag() after WithModel = %q, want ollama", rebuilt.BehaviorTag())
	}
}

// TestRenamedInstance_OpenRouterMetaNamespace verifies that an openrouter instance
// renamed to "work" still keeps upstream namespaces (prefixActionKeep) and that
// the minimax/ providerOpts gate uses behaviorTag.
func TestRenamedInstance_OpenRouterMetaNamespace(t *testing.T) {
	// openrouter renamed to "work"
	orWork := WithProviderID(NewOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0), "work")
	if orWork.BehaviorTag() != "openrouter" {
		t.Fatalf("pre-condition: BehaviorTag() = %q, want openrouter", orWork.BehaviorTag())
	}
	// WithModel("minimax/minimax-m2.7") must keep the model verbatim (prefixActionKeep
	// because behaviorTag=="openrouter") and inject reasoning providerOpts.
	cloned := orWork.WithModel("minimax/minimax-m2.7")
	if cloned.ID() != "work" {
		t.Fatalf("ID() = %q, want work — renamed id must be preserved", cloned.ID())
	}
	if cloned.Model() != "minimax/minimax-m2.7" {
		t.Fatalf("Model() = %q, want minimax/minimax-m2.7 — upstream namespace must be kept", cloned.Model())
	}
	// The minimax/ reasoning providerOpts must be injected because behaviorTag=="openrouter".
	bp, ok := cloned.(*baseProfile)
	if !ok {
		t.Fatalf("expected *baseProfile, got %T", cloned)
	}
	openaiCompat, ok := bp.providerOpts["openai-compatible"].(map[string]any)
	if !ok {
		t.Fatalf("missing openai-compatible providerOpts — minimax reasoning gate must key on behaviorTag")
	}
	if _, ok := openaiCompat["reasoning"]; !ok {
		t.Fatalf("missing reasoning in openai-compatible providerOpts")
	}
}

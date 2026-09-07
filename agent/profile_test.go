package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/internal/bundled"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func renderPromptForTest(t *testing.T, p *provider.Profile, data promptData) string {
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

	resolver := &sectionResolver{
		surface: p.ID(),
		agent:   data.Agent,
		agentFS: bundled.Agents(),
		sources: []sectionSource{
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
	t.Parallel()
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

	anthropic := newAnthropicProfile("claude-test")
	if anthropic.ID() != "anthropic" {
		t.Fatalf("anthropic id: %q", anthropic.ID())
	}
	if !anthropic.SupportsParallelToolCalls() {
		t.Fatalf("anthropic should support parallel tool calls")
	}
	assertHasTool(t, anthropic, "edit_file")
	assertMissingTool(t, anthropic, "apply_patch")

	gemini := newGeminiProfile("gemini-test")
	if gemini.ID() != "google" {
		t.Fatalf("gemini id: %q", gemini.ID())
	}
	if !gemini.SupportsParallelToolCalls() {
		t.Fatalf("gemini should support parallel tool calls")
	}
	assertHasTool(t, gemini, "edit_file")
	assertHasTool(t, gemini, "list_dir")
	assertMissingTool(t, gemini, "apply_patch")
}

func TestProviderProfiles_ToolLists_MatchSpec(t *testing.T) {
	t.Parallel()
	t.Run("openai", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-5.2")
		// ToolDefinitions returns canonical names; provider-specific renaming
		// (shell→exec_command, grep→grep_files, glob→find_files) is applied at the
		// agent wire edge, verified by TestToolNameMapping_OpenAI.
		assertToolListExact(t, p, []string{
			"read_file",
			"apply_patch",
			"write_file",
			"shell",
			"grep",
			"glob",
			"job_status",
			"job_list",
			"job_stop",
			"delegate",
			"job_watch",
			"delegate_send",
			"task_list",
			"web_fetch",
			"communicate",
			"use_skill",
		})
	})
	t.Run("anthropic", func(t *testing.T) {
		p := newAnthropicProfile("claude-test")
		assertToolListExact(t, p, []string{
			"read_file",
			"write_file",
			"edit_file",
			"shell",
			"grep",
			"glob",
			"job_status",
			"job_list",
			"job_stop",
			"delegate",
			"job_watch",
			"delegate_send",
			"task_list",
			"web_fetch",
			"communicate",
			"use_skill",
		})
	})
	t.Run("gemini", func(t *testing.T) {
		p := newGeminiProfile("gemini-test")
		// Canonical names; Gemini renames shell→run_shell_command,
		// grep→grep_search, list_dir→list_directory at the agent wire edge
		// (see TestToolNameMapping_Gemini).
		assertToolListExact(t, p, []string{
			"read_file",
			"write_file",
			"edit_file",
			"shell",
			"grep",
			"glob",
			"list_dir",
			"job_status",
			"job_list",
			"job_stop",
			"delegate",
			"job_watch",
			"delegate_send",
			"task_list",
			"web_fetch",
			"web_search",
			"communicate",
			"use_skill",
		})
	})
}

func TestProviderProfiles_AllIncludeUseSkill(t *testing.T) {
	t.Parallel()
	profiles := []*provider.Profile{
		NewOpenAIProfile("gpt-5.2"),
		newAnthropicProfile("claude-test"),
		newGeminiProfile("gemini-test"),
		newMiniMaxProfile("MiniMax-M2.7"),
		newOpenRouterAnthropicProfile("anthropic/claude-test"),
		newOpenAICompatProfile("openrouter", "openai/gpt-test", 0),
		newOpenAICompatProfile("kimi", "kimi-test", 0),
		newOpenAICompatProfile("glm", "glm-test", 0),
		newOpenAICompatProfile("ollama", "llama3", 0),
	}
	for _, p := range profiles {
		t.Run(p.ID(), func(t *testing.T) {
			assertHasTool(t, p, "use_skill")
		})
	}
}

func TestProviderProfiles_AddIntentToWorkToolSchemas(t *testing.T) {
	t.Parallel()
	profiles := []*provider.Profile{
		NewOpenAIProfile("gpt-5.2"),
		newAnthropicProfile("claude-test"),
		newGeminiProfile("gemini-test"),
		newOpenAICompatProfile("openrouter", "openai/gpt-test", 0),
	}
	for _, p := range profiles {
		nameMap := p.ToolNameMap()
		for _, td := range p.ToolDefinitions() {
			canonicalName := td.Name
			td = wireToolDef(td, nameMap, "communicate")
			props, _ := td.Parameters["properties"].(map[string]any)
			if props == nil {
				t.Fatalf("%s/%s has no properties schema", p.ID(), td.Name)
			}
			_, hasIntent := props["intent"]
			if canonicalName == "communicate" {
				if hasIntent {
					t.Fatalf("%s/%s should not advertise intent", p.ID(), td.Name)
				}
				continue
			}
			if !hasIntent {
				t.Fatalf("%s/%s missing intent parameter", p.ID(), td.Name)
			}
		}
	}
}

func TestSystemPrompt_ImplementerWarnsOnUnavailableTools(t *testing.T) {
	t.Parallel()
	prompt := renderPromptForTest(t, NewOpenAIProfile("gpt-5.4"), promptData{
		Agent:                       "implementer",
		CallableToolNames:           []string{"read_file", "exec_command", "communicate"},
		UnavailableProfileToolNames: []string{"delegate", "job_watch"},
	})

	if !strings.Contains(prompt, "If the task depends on tools or capabilities explicitly listed as unavailable in") {
		t.Fatalf("implementer prompt missing unavailable-tools guidance:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do not try to recreate unavailable evener-native tools by shelling out to") {
		t.Fatalf("implementer prompt missing nested-evener warning:\n%s", prompt)
	}
}

func TestSystemPrompt_CoordinatorHasImpossibleDelegationException(t *testing.T) {
	t.Parallel()
	prompt := renderPromptForTest(t, NewOpenAIProfile("gpt-5.4"), promptData{
		Agent: "coordinator",
	})

	if !strings.Contains(prompt, "Exception: if the task itself is about delegation, agent behavior, or orchestration") {
		t.Fatalf("coordinator prompt missing delegation exception:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do not force an impossible delegation.") {
		t.Fatalf("coordinator prompt missing impossible-delegation rule:\n%s", prompt)
	}
}

func TestBuildSystemPrompt_IncludesBackgroundJobsSection(t *testing.T) {
	t.Parallel()
	prompt := renderPromptForTest(t, newAnthropicProfile("claude-test"), promptData{})

	if !strings.Contains(prompt, "## Background jobs") {
		t.Fatalf("system prompt missing background-jobs section heading:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Delegates are durable resources identified by") {
		t.Fatalf("system prompt missing background-jobs section body (stable delegate statement):\n%s", prompt)
	}
	if !strings.Contains(prompt, "Pick the waiting primitive by how many answers you need:") {
		t.Fatalf("system prompt missing background-jobs section body (waiting primitive sentence):\n%s", prompt)
	}
}

// TestBuildSystemPrompt_PinsAntiPollGuidance makes the scenario card
// test/scenarios/job-delegate-wait-no-poll.md's grep pin durable: the assembled
// system prompt must carry the exact anti-poll sentence.
func TestBuildSystemPrompt_PinsAntiPollGuidance(t *testing.T) {
	t.Parallel()
	prompt := renderPromptForTest(t, newAnthropicProfile("claude-test"), promptData{})

	if !strings.Contains(prompt, "Do not call `job_status` in a loop") {
		t.Fatalf("system prompt missing anti-poll pin (scenario job-delegate-wait-no-poll depends on it):\n%s", prompt)
	}
}

func TestSubagentPrompt_DoesNotIncludeBackgroundJobsSection(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		surface: "openai",
		agent:   "implementer",
		agentFS: bundled.Agents(),
		sources: []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}
	data := promptData{
		Provider:           "openai",
		Agent:              "implementer",
		RolePromptOverride: mustWorkflowAgent(t, "implementer").SystemPrompt,
		Model:              "gpt-5.4",
		ResultToolName:     "communicate",
	}
	result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "subagent", data)
	if err != nil {
		t.Fatalf("RenderEmbedded subagent: %v", err)
	}
	if strings.Contains(result, "## Background jobs") {
		t.Fatalf("subagent prompt must not contain background-jobs section (root-only):\n%s", result)
	}
}

func TestSystemPrompt_DefaultAgentDoesNotUseCoordinatorRole(t *testing.T) {
	t.Parallel()
	prompt := renderPromptForTest(t, NewOpenAIProfile("gpt-5.4"), promptData{})

	if strings.Contains(prompt, "You are a coordinator. You delegate, verify, and iterate. You do not implement.") {
		t.Fatalf("default prompt should not use coordinator persona:\n%s", prompt)
	}
	if strings.Contains(prompt, "### CRITICAL: You normally spawn an implementer") {
		t.Fatalf("default prompt should not include coordinator delegation mandate:\n%s", prompt)
	}
}

func TestProviderProfiles_BuildSystemPrompt_IncludesEnvironment(t *testing.T) {
	t.Parallel()
	data := promptData{
		WorkingDir:      "/tmp",
		Platform:        "linux",
		OSVersion:       "test",
		Today:           "2026-02-07",
		KnowledgeCutoff: "2024-06-01",
	}

	for _, p := range []*provider.Profile{
		NewOpenAIProfile("gpt-5.2"),
		newAnthropicProfile("claude-test"),
		newGeminiProfile("gemini-test"),
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
	t.Parallel()
	p := NewOpenAIProfile("gpt-5.2")
	data := promptData{
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
	t.Parallel()
	prompt := renderPromptForTest(t, NewOpenAIProfile("gpt-5.2"), promptData{
		WorkingDir: "/tmp",
		Platform:   "linux",
		MCPTools: []toolEntry{{
			Name:        "mcp__server__search",
			Description: "Searches the remote index with an MCP-backed provider tool.",
		}},
		CustomTools: []toolEntry{{
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
	t.Parallel()
	cases := []struct {
		profile *provider.Profile
		want    string
	}{
		{NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano"},
		{newAnthropicProfile("claude-opus-4-6"), "claude-haiku-4-5"},
		{newGeminiProfile("gemini-3-pro"), "gemini-2.5-flash-lite"},
	}
	for _, tc := range cases {
		got := tc.profile.CheapModel()
		if got != tc.want {
			t.Fatalf("profile %q CheapModel: got %q want %q", tc.profile.ID(), got, tc.want)
		}
	}
}

func TestProviderProfile_WithModel(t *testing.T) {
	t.Parallel()
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
	if renderPromptForTest(t, cloned, promptData{WorkingDir: "/tmp", Platform: "linux"}) == "" {
		t.Fatalf("cloned profile has empty system prompt")
	}
}

func TestProviderProfile_WithModel_EmptyStringKeepsOriginal(t *testing.T) {
	t.Parallel()
	orig := newAnthropicProfile("claude-opus-4-6")
	cloned := orig.WithModel("")
	if cloned.Model() != "claude-opus-4-6" {
		t.Fatalf("WithModel('') should keep original model, got %q", cloned.Model())
	}
}

func TestProviderProfile_WithModel_ResolvesProviderPrefix(t *testing.T) {
	t.Parallel()
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

// TestBaseProfile_WithModel_PreservesSlashOnMetaProviders verifies that a
// namespaced model id the instance itself serves ("minimax/minimax-m2.7" on
// OpenRouter) is passed through verbatim: the slash is part of the id, not an
// instance switch, so the profile keeps its instance and its routing. The
// failure mode this guards is a SetModel or subagent override silently
// switching off the OpenRouter routing and mangling tool calls.
func TestBaseProfile_WithModel_PreservesSlashOnMetaProviders(t *testing.T) {
	t.Parallel()
	cases := []struct {
		startProfile func() *provider.Profile
		startID      string
		input        string
	}{
		{func() *provider.Profile {
			return newOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
		}, "openrouter", "minimax/minimax-m2.7"},
		{func() *provider.Profile {
			return newOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
		}, "openrouter", "anthropic/claude-3-haiku-20240307"},
		{func() *provider.Profile {
			return newOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
		}, "openrouter", "deepseek/deepseek-r1"},
		{func() *provider.Profile { return newOpenRouterAnthropicProfile("minimax/minimax-m2.7") }, "orclaude", "minimax/minimax-m2.7"},
		{func() *provider.Profile { return newOpenRouterAnthropicProfile("minimax/minimax-m2.7") }, "orclaude", "anthropic/claude-3-5-sonnet"},
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
	t.Parallel()
	cases := []struct {
		startProfile func() *provider.Profile
		startID      string
		input        string
		wantModel    string
	}{
		{func() *provider.Profile { return newOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "openrouter/anthropic/claude-3-haiku-20240307", "anthropic/claude-3-haiku-20240307"},
		{func() *provider.Profile { return newOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "openrouter/minimax/minimax-m2.7", "minimax/minimax-m2.7"},
		{func() *provider.Profile { return newOpenRouterAnthropicProfile("x") }, "orclaude", "orclaude/anthropic/claude-3-5-sonnet", "anthropic/claude-3-5-sonnet"},
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

// TestBaseProfile_WithModel_RecomputesCatalogStateOnMetaProviders verifies
// that a same-instance model switch re-resolves every fact, notably the
// context window — stale state here is silent context truncation when
// SetModel moves between OpenRouter-routed models of different sizes.
func TestBaseProfile_WithModel_RecomputesCatalogStateOnMetaProviders(t *testing.T) {
	t.Parallel()
	// Start with a known small-context model under openrouter
	// ("anthropic/claude-3-haiku-20240307" → 200000 in the catalog).
	orig := newOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
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
	if got, want := cloned.ContextWindowSize(), newOpenAICompatProfile("openrouter", "minimax/minimax-m2.7", 0).ContextWindowSize(); got != want {
		t.Fatalf("ContextWindowSize() = %d, want the registry's %d — same-instance WithModel did not re-resolve", got, want)
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
	t.Parallel()
	cases := []struct {
		startProfile func() *provider.Profile
		startID      string
		input        string
	}{
		{func() *provider.Profile { return newOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "anthropic/claude-3-haiku"},
		{func() *provider.Profile { return newOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "openai/gpt-5"},
		{func() *provider.Profile { return newOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "google/gemini-3"},
		{func() *provider.Profile { return newOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "minimax/minimax-m2.7"},
		{func() *provider.Profile { return newOpenAICompatProfile("openrouter", "x", 0) }, "openrouter", "deepseek/deepseek-r1"},
		{func() *provider.Profile { return newOpenRouterAnthropicProfile("x") }, "orclaude", "anthropic/claude-3-5-sonnet"},
		{func() *provider.Profile { return newOpenRouterAnthropicProfile("x") }, "orclaude", "minimax/minimax-m2.7"},
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

// TestBaseProfile_WithModel_PreservesToolDefOverridesAcrossProviderSwitch
// verifies that a cross-provider WithModel ("openai/...", "ollama/...",
// etc.) also preserves tool-schema overrides applied via
// WithCommunicateOutputSchema. Without this, Session.SetModel that
// switches provider mid-session — or subagent/plugin overrides that
// choose a different backend — silently revert the communicate
// contract to the new provider's default.
func TestBaseProfile_WithModel_PreservesToolDefOverridesAcrossProviderSwitch(t *testing.T) {
	t.Parallel()
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
		newOrig  func() *provider.Profile
		newModel string
	}{
		{"openai-to-ollama", func() *provider.Profile { return NewOpenAIProfile("gpt-5.4") }, "ollama/llama3.1"},
		{"openrouter-to-ollama", func() *provider.Profile {
			return newOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
		}, "ollama/llama3.1"},
		{"openai-to-anthropic", func() *provider.Profile { return NewOpenAIProfile("gpt-5.4") }, "anthropic/claude-3-opus"},
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
	t.Parallel()
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
		newOrig  func() *provider.Profile
		newModel string
	}{
		{"openrouter", func() *provider.Profile {
			return newOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
		}, "anthropic/claude-3-5-sonnet"},
		{"openrouter-anthropic", func() *provider.Profile {
			return newOpenRouterAnthropicProfile("anthropic/claude-3-5-sonnet")
		}, "anthropic/claude-3-haiku-20240307"},
		{"kimi", func() *provider.Profile {
			return newOpenAICompatProfile("kimi", "kimi-k2.5", 0)
		}, "kimi-k2.6"},
		{"ollama", func() *provider.Profile {
			return newOpenAICompatProfile("ollama", "llama3.1", 0)
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
// integration check on the WithModel re-resolve for the §14.1 recipe:
// switching between two Anthropic models on the OpenRouter-over-Anthropic
// instance must surface the new model's facts, not stale state.
func TestBaseProfile_WithModel_RecomputesOpenRouterAnthropicCatalog(t *testing.T) {
	t.Parallel()
	orig := newOpenRouterAnthropicProfile("anthropic/claude-3-5-sonnet")
	cloned := orig.WithModel("anthropic/claude-3-haiku-20240307")
	if cloned.ID() != "orclaude" {
		t.Fatalf("ID() = %q, want orclaude", cloned.ID())
	}
	if cloned.Model() != "anthropic/claude-3-haiku-20240307" {
		t.Fatalf("Model() = %q, want anthropic/claude-3-haiku-20240307", cloned.Model())
	}
	if got, want := cloned.ContextWindowSize(), newOpenRouterAnthropicProfile("anthropic/claude-3-haiku-20240307").ContextWindowSize(); got != want {
		t.Fatalf("ContextWindowSize() = %d, want the registry's %d — same-instance WithModel did not re-resolve", got, want)
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
	t.Parallel()
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
			orig := newOpenAICompatProfile(tc.startID, "placeholder", 0)
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

// TestNewOpenAICompatProfile_OpenRouterUpstreamBareEntry verifies that a
// namespaced upstream id OpenRouter serves carries that row's facts rather
// than an instance-level default.
func TestNewOpenAICompatProfile_OpenRouterUpstreamBareEntry(t *testing.T) {
	t.Parallel()
	p := newOpenAICompatProfile("openrouter", "minimax/minimax-m2.7", 0)
	if got := p.ContextWindowSize(); got != 204800 {
		t.Fatalf("ContextWindowSize() = %d, want 204800 from bare minimax catalog entry — bare-key fallback was over-suppressed for openrouter", got)
	}
}

// TestProviderProfile_WithModel_OllamaPrefix_PreservesCatalogMetadata has been
// moved to session_resolve_profile_test.go (session-level cross-provider test).

// TestNewOpenAICompatProfile_OpenRouterResolvesCatalogMetadata is the same
// check for an Anthropic-namespaced OpenRouter id: the row's window is the
// upstream model's, and the wire model keeps its namespace.
func TestNewOpenAICompatProfile_OpenRouterResolvesCatalogMetadata(t *testing.T) {
	t.Parallel()
	p := newOpenAICompatProfile("openrouter", "anthropic/claude-3-haiku-20240307", 0)
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

func assertHasTool(t *testing.T, p *provider.Profile, name string) {
	t.Helper()
	for _, td := range p.ToolDefinitions() {
		if td.Name == name {
			return
		}
	}
	t.Fatalf("expected tool %q in profile %q tool defs", name, p.ID())
}

func assertMissingTool(t *testing.T, p *provider.Profile, name string) {
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
	t.Parallel()
	profiles := map[string]*provider.Profile{
		"openai":    NewOpenAIProfile("gpt-5.2"),
		"anthropic": newAnthropicProfile("claude-test"),
		"gemini":    newGeminiProfile("gemini-test"),
	}
	skills := []skillEntry{
		{Name: "test-skill", Description: "A test skill", Dir: "/tmp/skills/test-skill", SkillFile: "/tmp/skills/test-skill/SKILL.md"},
	}

	for name, p := range profiles {
		prompt := renderPromptForTest(t, p, promptData{
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
	t.Parallel()
	// Anthropic profile has use_skill, so skills are rendered with directory paths.
	p := newAnthropicProfile("claude-test")
	skills := []skillEntry{
		{Name: "greet", Description: "Greeting skill", Dir: "/tmp/skills/greet", SkillFile: "/tmp/skills/greet/SKILL.md"},
		{Name: "deploy", Description: "Deploy skill", Dir: "/tmp/skills/deploy", SkillFile: "/tmp/skills/deploy/SKILL.md"},
	}
	prompt := renderPromptForTest(t, p, promptData{
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
	t.Parallel()
	p := NewOpenAIProfile("gpt-5.2")
	skills := []skillEntry{
		{Name: "greet", Description: "Greeting skill", Dir: "/tmp/skills/greet", SkillFile: "/tmp/skills/greet/SKILL.md"},
	}
	prompt := renderPromptForTest(t, p, promptData{
		WorkingDir:  "/tmp",
		Platform:    "linux",
		Today:       "2026-02-09",
		Skills:      skills,
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
	t.Parallel()
	p := NewOpenAIProfile("gpt-5.2")
	prompt := renderPromptForTest(t, p, promptData{
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
	t.Parallel()
	assertHasTool(t, newGeminiProfile("gemini-test"), "web_search")
	assertMissingTool(t, NewOpenAIProfile("gpt-5.2"), "web_search")
	assertMissingTool(t, newAnthropicProfile("claude-test"), "web_search")
}

// The Anthropic protocol needs no request extras from the profile: max
// tokens, betas, and thinking shape are all registry capabilities now.
func TestProviderProfile_ProviderOptions(t *testing.T) {
	t.Parallel()
	if opts := newAnthropicProfile("claude-opus-4-6").ProviderOptions(); opts != nil {
		t.Fatalf("ProviderOptions() = %+v, want nil for the anthropic protocol", opts)
	}
}

func TestOpenAIProfile_ProviderOptions_ParallelToolCalls(t *testing.T) {
	t.Parallel()
	p := NewOpenAIProfile("gpt-5.2")
	opts := p.ProviderOptions()
	if opts == nil {
		t.Fatal("expected non-nil ProviderOptions for OpenAI")
	}
	oai, ok := opts[registry.ProtocolOpenAIResponses].(map[string]any)
	if !ok {
		t.Fatalf("missing %s key in provider options", registry.ProtocolOpenAIResponses)
	}
	ptc, ok := oai["parallel_tool_calls"]
	if !ok {
		t.Fatal("missing parallel_tool_calls in openai provider options")
	}
	if ptc != true {
		t.Fatalf("parallel_tool_calls = %v, want true", ptc)
	}
}

// The output cap is the row's max_output_tokens, not an injected option.
func TestAnthropicProfile_MaxOutputTokens(t *testing.T) {
	t.Parallel()
	p := newAnthropicProfile("claude-opus-4-6")
	if p.Resolved().Caps.MaxOutputTokens == nil {
		t.Fatal("the catalog carries an output cap for claude-opus-4-6")
	}
	if got, want := p.MaxOutputTokens(), *p.Resolved().Caps.MaxOutputTokens; got != want {
		t.Fatalf("MaxOutputTokens() = %d, want the row's %d", got, want)
	}
}

// A model without the 1M beta row carries no anthropic-beta header.
func TestAnthropicProfile_NoBetaHeaderByDefault(t *testing.T) {
	t.Parallel()
	p := newAnthropicProfile("claude-sonnet-4-5")
	if bh := p.Resolved().Headers["anthropic-beta"]; bh != "" {
		t.Fatalf("default profile carries anthropic-beta %q", bh)
	}
}

func TestProviderProfile_SupportsReasoning(t *testing.T) {
	t.Parallel()
	if !NewOpenAIProfile("gpt-5.2").SupportsReasoning() {
		t.Fatal("OpenAI should support reasoning")
	}
	if !newAnthropicProfile("claude-opus-4-6").SupportsReasoning() {
		t.Fatal("Anthropic should support reasoning")
	}
}

func TestProviderProfile_SupportsStreaming(t *testing.T) {
	t.Parallel()
	if !NewOpenAIProfile("gpt-5.2").SupportsStreaming() {
		t.Fatal("OpenAI should support streaming")
	}
}

func TestProviderProfile_DefaultCommandTimeout(t *testing.T) {
	t.Parallel()
	if got := NewOpenAIProfile("gpt-5.2").DefaultCommandTimeoutMS(); got != 120_000 {
		t.Fatalf("OpenAI timeout = %d, want 120000", got)
	}
	if got := newAnthropicProfile("claude-opus-4-6").DefaultCommandTimeoutMS(); got != 120_000 {
		t.Fatalf("Anthropic timeout = %d, want 120000", got)
	}
	if got := newGeminiProfile("gemini-2.5-pro").DefaultCommandTimeoutMS(); got != 120_000 {
		t.Fatalf("Gemini timeout = %d, want 120000", got)
	}
}

func TestProviderProfile_KnowledgeCutoff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		p    *provider.Profile
	}{
		{"openai", NewOpenAIProfile("gpt-5.2")},
		{"anthropic", newAnthropicProfile("claude-opus-4-6")},
		{"gemini", newGeminiProfile("gemini-2.5-pro")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.p.KnowledgeCutoff()
			want := registry.StringValue(tt.p.Resolved().Caps.KnowledgeCutoff)
			if want == "" {
				t.Fatalf("the catalog carries no knowledge cutoff for %s/%s", tt.p.ID(), tt.p.Model())
			}
			if got != want {
				t.Fatalf("KnowledgeCutoff() = %q, want the row's %q", got, want)
			}
		})
	}
}

// WS3: Tool name mapping
// wiredToolNames returns the set of tool names the profile advertises to the
// model — the agent wire form (provider-specific renaming applied).
func wiredToolNames(p *provider.Profile) map[string]bool {
	nameMap := p.ToolNameMap()
	names := map[string]bool{}
	for _, td := range p.ToolDefinitions() {
		names[wireToolDef(td, nameMap, "communicate").Name] = true
	}
	return names
}

func TestToolNameMapping_OpenAI(t *testing.T) {
	t.Parallel()
	toolNames := wiredToolNames(NewOpenAIProfile("gpt-5.2"))
	// OpenAI advertises provider-specific names to the model.
	if !toolNames["exec_command"] {
		t.Fatal("OpenAI wire defs should contain exec_command (mapped from shell)")
	}
	if !toolNames["grep_files"] {
		t.Fatal("OpenAI wire defs should contain grep_files (mapped from grep)")
	}
	if !toolNames["find_files"] {
		t.Fatal("OpenAI wire defs should contain find_files (mapped from glob)")
	}
	// Should NOT contain canonical names for mapped tools.
	if toolNames["shell"] {
		t.Fatal("OpenAI wire defs should not contain canonical 'shell'")
	}
	if toolNames["grep"] {
		t.Fatal("OpenAI wire defs should not contain canonical 'grep'")
	}
	if toolNames["glob"] {
		t.Fatal("OpenAI wire defs should not contain canonical 'glob'")
	}
}

// TestToolNameMapping_NoWireNameCollisions asserts no two canonical tools
// map to the same wire name within any profile's toolNameMap. A collision
// means one of the two tools would be shadowed at the session wire edge
// (see rebuildToolDefsCache in session_tools.go).
func TestToolNameMapping_NoWireNameCollisions(t *testing.T) {
	t.Parallel()
	profiles := []*provider.Profile{
		NewOpenAIProfile("gpt-5.2"),
		newAnthropicProfile("claude-test"),
		newGeminiProfile("gemini-test"),
		newMiniMaxProfile("MiniMax-M2.7"),
		newOpenRouterAnthropicProfile("anthropic/claude-test"),
		newOpenAICompatProfile("openrouter", "openai/gpt-test", 0),
		newOpenAICompatProfile("kimi", "kimi-test", 0),
		newOpenAICompatProfile("glm", "glm-test", 0),
		newOpenAICompatProfile("ollama", "llama3", 0),
	}
	for _, p := range profiles {
		t.Run(p.ID(), func(t *testing.T) {
			nameMap := p.ToolNameMap()
			if len(nameMap) == 0 {
				return
			}
			// Every canonical tool name not explicitly remapped keeps its own
			// name on the wire. A collision occurs when a mapped wire name equals
			// another canonical tool's effective wire name (itself unmapped, or
			// mapped to the same target).
			wireToCanonical := map[string]string{}
			for _, td := range p.ToolDefinitions() {
				wire := td.Name
				if mapped, ok := nameMap[td.Name]; ok {
					wire = mapped
				}
				if prevCanonical, exists := wireToCanonical[wire]; exists {
					t.Fatalf("wire name %q claimed by both canonical %q and %q", wire, prevCanonical, td.Name)
				}
				wireToCanonical[wire] = td.Name
			}
		})
	}
}

// TestToolNameMapping_OpenAI_ListDirNotShadowed exercises the full session
// wire-tool assembly (profile tools + registry tools), where the real
// list_dir directory-listing tool is registered separately from the
// profile's glob tool. Before the fix, OpenAI mapped glob->list_dir, and
// rebuildToolDefsCache's shadowing workaround silently dropped the real
// list_dir tool from the wire. After the fix, glob maps to find_files and
// list_dir keeps its own name unshadowed.
func TestToolNameMapping_OpenAI_ListDirNotShadowed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	names := map[string]bool{}
	for _, td := range sess.ToolDefinitions() {
		names[td.Name] = true
	}
	if !names["find_files"] {
		t.Fatal("OpenAI session tools should contain find_files (mapped from glob)")
	}
	if !names["list_dir"] {
		t.Fatal("OpenAI session tools should contain list_dir (the real directory-listing tool) unshadowed")
	}
	if !names["grep_files"] {
		t.Fatal("OpenAI session tools should contain grep_files (mapped from grep)")
	}
}

func TestToolNameMapping_Gemini(t *testing.T) {
	t.Parallel()
	toolNames := wiredToolNames(newGeminiProfile("gemini-test"))
	if !toolNames["run_shell_command"] {
		t.Fatal("Gemini wire defs should contain run_shell_command (mapped from shell)")
	}
	if !toolNames["grep_search"] {
		t.Fatal("Gemini wire defs should contain grep_search (mapped from grep)")
	}
	if !toolNames["list_directory"] {
		t.Fatal("Gemini wire defs should contain list_directory (mapped from list_dir)")
	}
}

func TestToolNameMapping_Anthropic_NoMapping(t *testing.T) {
	t.Parallel()
	p := newAnthropicProfile("claude-test")
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
	t.Parallel()
	p := newGeminiProfile("gemini-2.5-flash")
	opts := p.ProviderOptions()
	if opts == nil {
		t.Fatal("expected non-nil ProviderOptions for Gemini")
	}
	gemini, ok := opts[registry.ProtocolGoogle].(map[string]any)
	if !ok {
		t.Fatalf("expected opts[%q] to be map[string]any", registry.ProtocolGoogle)
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

// Sonnet 4.5's window is 1M on the base row itself (GA, verified live
// 2026-08-31), dated spelling included — no [1m] suffix needed to get it.
func TestAnthropicProfile_ContextWindow_Sonnet45Is1M(t *testing.T) {
	t.Parallel()
	p := newAnthropicProfile("claude-sonnet-4-5-20250929")
	if p.ContextWindowSize() != 1_000_000 {
		t.Errorf("expected 1000000, got %d", p.ContextWindowSize())
	}
}

// On Sonnet 4.5 the [1m] row is a pure alias now, so this pins what the alias
// still buys: the ref resolves (1M, reaching it through the alias fold from
// the base row) and the model string keeps the suffix for canonicalModelID.
func TestAnthropicProfile_ContextWindow_1MSuffix(t *testing.T) {
	t.Parallel()
	p := newAnthropicProfile("claude-sonnet-4-5[1m]")
	if p.ContextWindowSize() != 1_000_000 {
		t.Errorf("expected 1000000, got %d", p.ContextWindowSize())
	}
	// Model string should retain the suffix for downstream use.
	if p.Model() != "claude-sonnet-4-5[1m]" {
		t.Errorf("model: got %q, want %q", p.Model(), "claude-sonnet-4-5[1m]")
	}
}

func TestAnthropicProfile_WithModel_RoundTrip(t *testing.T) {
	t.Parallel()
	// Start at 200K, switch to 1M model. Opus 4.5 is the pair whose [1m] alias
	// still moves the window; Sonnet 4.5 is 1M on its base row, so it no longer
	// exercises the switch.
	orig := newAnthropicProfile("claude-opus-4-5")
	if orig.ContextWindowSize() != 200_000 {
		t.Fatalf("orig context: got %d, want 200000", orig.ContextWindowSize())
	}

	upgraded := orig.WithModel("claude-opus-4-5[1m]")
	if upgraded.ContextWindowSize() != 1_000_000 {
		t.Fatalf("upgraded context: got %d, want 1000000", upgraded.ContextWindowSize())
	}
	if upgraded.Model() != "claude-opus-4-5[1m]" {
		t.Fatalf("upgraded model: got %q", upgraded.Model())
	}

	// Switch back to 200K.
	downgraded := upgraded.WithModel("claude-opus-4-5")
	if downgraded.ContextWindowSize() != 200_000 {
		t.Fatalf("downgraded context: got %d, want 200000", downgraded.ContextWindowSize())
	}

	// Original untouched.
	if orig.ContextWindowSize() != 200_000 {
		t.Fatalf("orig mutated: context = %d", orig.ContextWindowSize())
	}
}

// ProviderOptions builds a fresh map on every call, so a caller that layers
// its own keys onto the result cannot leak them into the profile.
func TestProfile_ProviderOptionsAreNotAliased(t *testing.T) {
	t.Parallel()
	p := newGeminiProfile("gemini-2.5-pro")
	opts := p.ProviderOptions()
	google, ok := opts[registry.ProtocolGoogle].(map[string]any)
	if !ok {
		t.Fatalf("missing %s key", registry.ProtocolGoogle)
	}
	google["injected"] = "bad"

	fresh, _ := p.ProviderOptions()[registry.ProtocolGoogle].(map[string]any)
	if _, found := fresh["injected"]; found {
		t.Fatal("mutating a returned provider-options map affected the profile — aliasing bug")
	}
}

// Sonnet 4.5's 1M window is GA (verified live 2026-08-31), and so is prompt
// caching, so the [1m] profile opts into neither: it sends no anthropic-beta
// header at all. Any value here means a retired beta crept back onto every
// [1m] request.
func TestAnthropicProfile_1M_NoBetaHeader(t *testing.T) {
	t.Parallel()
	p := newAnthropicProfile("claude-sonnet-4-5[1m]")
	if bh := p.Resolved().Headers["anthropic-beta"]; bh != "" {
		t.Fatalf("anthropic-beta = %q, want no header: the 1M window and prompt caching are both GA", bh)
	}
}

func TestGeminiProfile_ContextWindow_IsAtLeast1M(t *testing.T) {
	t.Parallel()
	p := newGeminiProfile("gemini-2.5-flash")
	if got := p.ContextWindowSize(); got < 1_000_000 {
		t.Errorf("expected at least 1000000, got %d", got)
	}
}

func TestBuildSystemPrompt_ToolUsageBeforeProjectDocs(t *testing.T) {
	t.Parallel()
	p := NewOpenAIProfile("gpt-5.2")
	prompt := renderPromptForTest(t, p, promptData{
		WorkingDir:  "/tmp",
		Platform:    "linux",
		Today:       "2026-02-11",
		ProjectDocs: []ProjectDoc{{Path: "AGENTS.md", Content: "project instructions here"}},
		MCPTools:    []toolEntry{{Name: "mcp__server__tool1", Description: "Does thing one"}},
		CustomTools: []toolEntry{{Name: "my_custom_tool", Description: "Does custom things"}},
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
	t.Parallel()
	d := tool.DefApplyPatch()
	if !strings.Contains(d.Description, "creating") || !strings.Contains(d.Description, "deleting") || !strings.Contains(d.Description, "modifying") {
		t.Fatalf("apply_patch description missing capability summary: %q", d.Description)
	}
}

func TestProviderProfile_NewToolRegistry_ContainsProfileTools(t *testing.T) {
	t.Parallel()
	profiles := []*provider.Profile{
		NewOpenAIProfile("gpt-5.2"),
		newAnthropicProfile("claude-test"),
		newGeminiProfile("gemini-test"),
	}
	for _, p := range profiles {
		t.Run(p.ID(), func(t *testing.T) {
			reg := newProfileToolRegistry(p)
			if reg == nil {
				t.Fatal("tool.NewRegistry() returned nil")
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
	t.Parallel()
	p := newAnthropicProfile("claude-test")
	reg := newProfileToolRegistry(p)
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

func TestProviderProfile_NewToolRegistry_CacheReturnsIndependentRegistry(t *testing.T) {
	t.Parallel()
	p := newAnthropicProfile("claude-test")
	first := newProfileToolRegistry(p)
	first.Remove("read_file")
	if first.Get("read_file") != nil {
		t.Fatal("read_file survived removal from first registry")
	}

	second := newProfileToolRegistry(p)
	if second.Get("read_file") == nil {
		t.Fatal("cached profile registry shared mutable tool map with caller")
	}
}

func assertToolListExact(t *testing.T, p *provider.Profile, want []string) {
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
	t.Parallel()
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

	env := schema.EnvironmentInfo{
		WorkingDir: dir,
		Platform:   "linux",
		Today:      "2026-03-01",
		Workspace:  ScanWorkspace(dir),
	}

	p := NewOpenAIProfile("gpt-5.3-codex")
	prompt := renderPromptForTest(t, p, promptData{
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
	toolIdx := strings.Index(prompt, "## Tool usage")
	if wsIdx < envIdx {
		t.Errorf("workspace (pos %d) should come after environment (pos %d)", wsIdx, envIdx)
	}
	if wsIdx < toolIdx {
		t.Errorf("workspace (pos %d) should come after tools (pos %d)", wsIdx, toolIdx)
	}
}

func TestBuildSystemPrompt_EmptyWorkspace(t *testing.T) {
	t.Parallel()
	env := schema.EnvironmentInfo{
		WorkingDir: "/tmp",
		Platform:   "linux",
		Today:      "2026-03-01",
		// Workspace is zero value (empty).
	}

	p := NewOpenAIProfile("gpt-5.3-codex")
	prompt := renderPromptForTest(t, p, promptData{
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
	t.Parallel()
	dir := t.TempDir()
	touchFile(t, filepath.Join(dir, "main.py"), "print('hello')\n")

	env := schema.EnvironmentInfo{
		WorkingDir: dir,
		Platform:   "linux",
		Today:      "2026-03-01",
		Workspace:  ScanWorkspace(dir),
	}

	p := NewOpenAIProfile("gpt-5.3-codex")
	prompt := renderPromptForTest(t, p, promptData{
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
	t.Parallel()
	p := newMiniMaxProfile("MiniMax-M2.7")
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
	t.Parallel()
	// MiniMax direct platform uses Anthropic API, so it should have
	// Anthropic-style tools (edit_file, use_skill, no apply_patch).
	p := newMiniMaxProfile("MiniMax-M2.7")
	assertHasTool(t, p, "edit_file")
	assertHasTool(t, p, "use_skill")
	assertMissingTool(t, p, "apply_patch")
}

func TestMiniMaxProfile_ToolListExact(t *testing.T) {
	t.Parallel()
	p := newMiniMaxProfile("MiniMax-M2.7")
	assertToolListExact(t, p, []string{
		"read_file",
		"write_file",
		"edit_file",
		"shell",
		"grep",
		"glob",
		"job_status",
		"job_list",
		"job_stop",
		"delegate",
		"job_watch",
		"delegate_send",
		"task_list",
		"web_fetch",
		"communicate",
		"use_skill",
	})
}

// TestMiniMaxProfile_WithModel_CrossProvider and TestWithModel_CrossProviderToMiniMax
// have been moved to session_resolve_profile_test.go. Cross-provider switching
// is now handled by the Session resolver.

// The effort ladder is the row's, and only the row's (spec §7.4).
func TestEffortLevels_ComeFromTheRow(t *testing.T) {
	t.Parallel()
	p := newAnthropicProfile("claude-opus-4-6")
	levels := p.ReasoningEffortLevels()
	if len(levels) == 0 || !stringSliceEqual(levels, p.Resolved().Caps.EffortValues) {
		t.Fatalf("claude-opus-4-6 effort levels: got %v, want the row's %v", levels, p.Resolved().Caps.EffortValues)
	}
}

// A model the catalog does not carry advertises no ladder at all: the request
// builder passes any requested effort through unchanged rather than clamping
// against a made-up default (spec §7.3, §7.4).
func TestEffortLevels_UncataloguedModelHasNoLadder(t *testing.T) {
	t.Parallel()
	p := newAnthropicProfile("claude-unknown-model")
	if levels := p.ReasoningEffortLevels(); len(levels) != 0 {
		t.Fatalf("unknown anthropic model effort levels: got %v, want none", levels)
	}
	if !p.SupportsReasoning() {
		t.Fatal("unknown is not disabled: an uncatalogued model still supports reasoning")
	}
}

// The task_list reasoning_effort enum is the row's ladder plus the "inherit"
// sentinel, whichever instance serves the model.
func TestTaskListSchema_EffortEnum_MatchesTheRowsLadder(t *testing.T) {
	t.Parallel()
	// Google models steer thinking by budget rather than an effort ladder, so
	// the enum-bearing surfaces are the two that advertise one.
	for _, p := range []*provider.Profile{
		newAnthropicProfile("claude-opus-4-6"),
		NewOpenAIProfile("gpt-5.2"),
	} {
		t.Run(p.ID(), func(t *testing.T) {
			levels := p.ReasoningEffortLevels()
			if len(levels) == 0 {
				t.Fatalf("%s/%s advertises no effort ladder; pick a model the catalog covers", p.ID(), p.Model())
			}
			// The enum additionally carries the "inherit" sentinel so
			// strict-mode providers (which force-require the property) let the
			// model decline to override the session's effort.
			want := append(append([]string(nil), levels...), "inherit")
			if got := extractTaskListEffortEnum(t, p); !stringSliceEqual(got, want) {
				t.Fatalf("task_list effort enum = %v, want %v", got, want)
			}
		})
	}
}

// extractTaskListEffortEnum finds the task_list tool and extracts the
// reasoning_effort enum from its parameters schema.
func extractTaskListEffortEnum(t *testing.T, p *provider.Profile) []string {
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

	// Navigate: parameters -> properties -> update -> items -> properties -> reasoning_effort -> enum
	// (check both add and update; both item schemas carry the enum)
	params, _ := taskListTool.Parameters["properties"].(map[string]any)
	var itemProps map[string]any
	for _, arrayName := range []string{"update", "add"} {
		arraySchema, _ := params[arrayName].(map[string]any)
		if arraySchema == nil {
			continue
		}
		items, _ := arraySchema["items"].(map[string]any)
		if items == nil {
			continue
		}
		itemProps, _ = items["properties"].(map[string]any)
		if itemProps != nil {
			if _, has := itemProps["reasoning_effort"]; has {
				break
			}
		}
	}
	if itemProps == nil {
		t.Fatalf("no task_list item schema with reasoning_effort in profile %s", p.ID())
	}
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

// TestProviderProfileRegistryKeys verifies each fixture resolves to the
// surface/protocol/provider-id triple the agent branches on (spec §7.5).
func TestProviderProfileRegistryKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                                string
		profile                             *provider.Profile
		surface, protocol, providerID, inst string
	}{
		{"openai", NewOpenAIProfile("gpt-5.2"), registry.SurfaceOpenAI, registry.ProtocolOpenAIResponses, "openai", "openai"},
		{"anthropic", newAnthropicProfile("claude-test"), registry.SurfaceAnthropic, registry.ProtocolAnthropic, "anthropic", "anthropic"},
		{"gemini", newGeminiProfile("gemini-test"), registry.SurfaceGoogle, registry.ProtocolGoogle, "google", "google"},
		{"minimax", newMiniMaxProfile("MiniMax-M2.7"), registry.SurfaceAnthropic, registry.ProtocolAnthropic, "minimax", "minimax"},
		{"openrouter over anthropic", newOpenRouterAnthropicProfile("anthropic/claude-test"), registry.SurfaceAnthropic, registry.ProtocolAnthropic, "openrouter", "orclaude"},
		{"kimi for coding", newKimiAnthropicProfile("k3"), registry.SurfaceAnthropic, registry.ProtocolAnthropic, "kimi-for-coding", "kimi-for-coding"},
		{"openrouter", newOpenAICompatProfile("openrouter", "openai/gpt-test", 0), registry.SurfaceGeneric, registry.ProtocolOpenAIChat, "openrouter", "openrouter"},
		{"kimi", newOpenAICompatProfile("kimi", "kimi-test", 0), registry.SurfaceGeneric, registry.ProtocolOpenAIChat, "moonshotai", "kimi"},
		{"glm", newOpenAICompatProfile("glm", "glm-test", 0), registry.SurfaceGeneric, registry.ProtocolOpenAIChat, "zai", "glm"},
		{"ollama", newOpenAICompatProfile("ollama", "llama3", 0), registry.SurfaceGeneric, registry.ProtocolOpenAIChat, "ollama", "ollama"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.profile
			if p.Surface() != tc.surface || p.Protocol() != tc.protocol || p.ProviderID() != tc.providerID || p.ID() != tc.inst {
				t.Fatalf("keys = %s/%s/%s on %s, want %s/%s/%s on %s",
					p.Surface(), p.Protocol(), p.ProviderID(), p.ID(), tc.surface, tc.protocol, tc.providerID, tc.inst)
			}
		})
	}
}

// TestNamedInstanceKeepsItsIdentity pins what the deleted WithProviderID used
// to fake: an instance carries a user-assigned name and inherits its base's
// facts, and both survive a model switch.
func TestNamedInstanceKeepsItsIdentity(t *testing.T) {
	t.Parallel()
	work := namedInstanceProfile("work-kimi", "moonshotai", "kimi-k2.5")
	if work.ID() != "work-kimi" || work.ProviderID() != "moonshotai" {
		t.Fatalf("named instance = %s/%s, want work-kimi/moonshotai", work.ID(), work.ProviderID())
	}
	rebuilt := work.WithModel("kimi-k3")
	if rebuilt.ID() != "work-kimi" || rebuilt.ProviderID() != "moonshotai" || rebuilt.Model() != "kimi-k3" {
		t.Fatalf("after WithModel = %s/%s/%s", rebuilt.ID(), rebuilt.ProviderID(), rebuilt.Model())
	}
	// A named instance inherits its base's auxiliary route as the catalog evolves.
	for _, tc := range []struct {
		name        string
		named, base *provider.Profile
	}{
		{"moonshotai", work, newOpenAICompatProfile("kimi", "kimi-k2.5", 0)},
		{"google", namedInstanceProfile("work-google", "google", "gemini-2.5-pro"), newGeminiProfile("gemini-2.5-pro")},
		{"anthropic", namedInstanceProfile("work-anthropic", "anthropic", "claude-opus-4-6"), newAnthropicProfile("claude-opus-4-6")},
	} {
		want := tc.base.Resolved().CheapModel
		if want == "" {
			t.Fatalf("%s fixture has no curated cheap model", tc.name)
		}
		if got := tc.named.CheapModel(); got != want {
			t.Errorf("%s named CheapModel() = %q, base = %q", tc.name, got, want)
		}
	}
}

// TestNamedInstanceInheritsBaseFacts verifies a renamed instance resolves its
// base's rows: the window comes from the ollama record, not from the name.
func TestNamedInstanceInheritsBaseFacts(t *testing.T) {
	t.Parallel()
	direct := newOpenAICompatProfile("ollama", "llama3.1", 0)
	renamed := namedInstanceProfile("work-ollama", "ollama", "llama3.1")
	if direct.ContextWindowSize() == 0 || renamed.ContextWindowSize() != direct.ContextWindowSize() {
		t.Fatalf("window = %d, want ollama's %d", renamed.ContextWindowSize(), direct.ContextWindowSize())
	}
	if rebuilt := renamed.WithModel("llama3.2"); rebuilt.ID() != "work-ollama" || rebuilt.ProviderID() != "ollama" {
		t.Fatalf("after WithModel = %s/%s", rebuilt.ID(), rebuilt.ProviderID())
	}
}

// TestNamedOpenRouterInstanceKeepsUpstreamNamespace verifies a renamed
// OpenRouter instance keeps a namespaced upstream id verbatim: the slash is
// part of the model id, not an instance switch.
func TestNamedOpenRouterInstanceKeepsUpstreamNamespace(t *testing.T) {
	t.Parallel()
	orWork := namedInstanceProfile("work-router", "openrouter", "anthropic/claude-3-haiku-20240307")
	cloned := orWork.WithModel("minimax/minimax-m2.7")
	if cloned.ID() != "work-router" {
		t.Fatalf("ID() = %q, want work-router", cloned.ID())
	}
	if cloned.Model() != "minimax/minimax-m2.7" {
		t.Fatalf("Model() = %q, want minimax/minimax-m2.7 — the upstream namespace must be kept", cloned.Model())
	}
	// The MiniMax-over-OpenRouter reasoning arrangement is a registry cap now,
	// not an injected provider option.
	if cloned.ProviderOptions() != nil {
		t.Fatalf("chat-completions sends no extras: %+v", cloned.ProviderOptions())
	}
	if got := registry.StringValue(cloned.Resolved().Caps.ReasoningField); got != "reasoning_details" {
		t.Fatalf("reasoning_field = %q, want reasoning_details from the openrouter minimax glob", got)
	}
}

// TestProviderProfile_CheapModelRefDefaultsToPrimary ports main's
// aux-route rule (6b5af0acb): an instance with no cheap_model anywhere —
// configured, instance row, or curated base — routes auxiliary work to the
// active instance and model rather than to nothing.
func TestProviderProfile_CheapModelRefDefaultsToPrimary(t *testing.T) {
	t.Parallel()
	p := resolveTestProfile("plaingw", openAICompatInstance("plaingw"), "some-model")
	if got := p.ConfiguredCheapModel(); got != "" {
		t.Fatalf("fixture has a configured cheap model %q; the test needs none", got)
	}
	providerName, model := p.CheapModelRef()
	if providerName != p.ID() || model != p.Model() {
		t.Fatalf("CheapModelRef = (%q, %q), want the primary (%q, %q)",
			providerName, model, p.ID(), p.Model())
	}
}

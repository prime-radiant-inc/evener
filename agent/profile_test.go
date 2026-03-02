package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	assertHasTool(t, gemini, "read_many_files")
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
			"read_many_files",
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

func TestProviderProfiles_BuildSystemPrompt_IncludesProviderSpecificBaseInstructions(t *testing.T) {
	env := EnvironmentInfo{
		WorkingDir:      "/tmp",
		Platform:        "linux",
		OSVersion:       "test",
		Today:           "2026-02-07",
		KnowledgeCutoff: "2024-06-01",
	}

	openai := NewOpenAIProfile("gpt-5.2")
	sysO := openai.BuildSystemPrompt(env, nil, nil, "")
	if !strings.Contains(sysO, "OpenAI profile") || !strings.Contains(sysO, "apply_patch") {
		t.Fatalf("openai system prompt missing expected base instructions:\n%s", sysO)
	}
	if strings.Contains(sysO, "edit_file") {
		t.Fatalf("openai system prompt should not focus on edit_file:\n%s", sysO)
	}

	anthropic := NewAnthropicProfile("claude-test")
	sysA := anthropic.BuildSystemPrompt(env, nil, nil, "")
	if !strings.Contains(sysA, "Anthropic profile") || !strings.Contains(sysA, "edit_file") {
		t.Fatalf("anthropic system prompt missing expected base instructions:\n%s", sysA)
	}
	if strings.Contains(sysA, "apply_patch") {
		t.Fatalf("anthropic system prompt should not focus on apply_patch:\n%s", sysA)
	}

	gemini := NewGeminiProfile("gemini-test")
	sysG := gemini.BuildSystemPrompt(env, nil, nil, "")
	if !strings.Contains(sysG, "Gemini profile") || !strings.Contains(sysG, "edit_file") {
		t.Fatalf("gemini system prompt missing expected base instructions:\n%s", sysG)
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
	// System prompt preserved.
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux"}
	if cloned.BuildSystemPrompt(env, nil, nil, "") == "" {
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

// TestOpenAIProfile_SystemPromptContainsApplyPatchFormat verifies that the
// OpenAI profile's system prompt includes the full v4a patch format specification
// so the model knows how to emit correctly-formatted patches.
func TestOpenAIProfile_SystemPromptContainsApplyPatchFormat(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}
	prompt := p.BuildSystemPrompt(env, nil, nil, "")

	// Must contain the patch envelope syntax.
	mustContain := []string{
		"*** Begin Patch",
		"*** End Patch",
		"*** Add File:",
		"*** Delete File:",
		"*** Update File:",
	}
	for _, s := range mustContain {
		if !strings.Contains(prompt, s) {
			t.Errorf("OpenAI system prompt missing %q", s)
		}
	}

	// Must contain the grammar definition so the model understands the format structurally.
	grammarKeywords := []string{
		"Patch :=",
		"AddFile :=",
		"UpdateFile :=",
		"HunkLine :=",
	}
	for _, s := range grammarKeywords {
		if !strings.Contains(prompt, s) {
			t.Errorf("OpenAI system prompt missing grammar rule %q", s)
		}
	}

	// Must contain a complete example showing how to create a file with + prefix lines.
	if !strings.Contains(prompt, "+Hello") {
		t.Errorf("OpenAI system prompt missing example with + prefixed lines")
	}

	// Must contain hunk line syntax explanation.
	if !strings.Contains(prompt, "@@") {
		t.Errorf("OpenAI system prompt missing @@ hunk header syntax")
	}
}

// TestAllProfiles_SystemPromptContainsTaskListGuidance verifies that all
// profiles include behavioral guidance for when and how to use the task_list tool.
func TestAllProfiles_SystemPromptContainsTaskListGuidance(t *testing.T) {
	profiles := map[string]ProviderProfile{
		"openai":    NewOpenAIProfile("gpt-5.2"),
		"anthropic": NewAnthropicProfile("claude-test"),
		"gemini":    NewGeminiProfile("gemini-test"),
	}
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	for name, p := range profiles {
		prompt := p.BuildSystemPrompt(env, nil, nil, "")

		// Must mention task_list tool by name in behavioral guidance (not just the tool list).
		if !strings.Contains(prompt, "task_list") {
			t.Errorf("profile %q system prompt missing task_list guidance", name)
		}

		// Must include guidance about when to use task_list.
		if !strings.Contains(prompt, "in_progress") {
			t.Errorf("profile %q system prompt missing 'in_progress' status guidance", name)
		}

		// Must include guidance about task statuses.
		if !strings.Contains(prompt, "done") || !strings.Contains(prompt, "undone") {
			t.Errorf("profile %q system prompt missing task status guidance (done/undone)", name)
		}
	}
}

// TestAllProfiles_SystemPromptContainsSubmitResultGuidance verifies that all
// profiles include behavioral guidance for the submit_result tool.
func TestAllProfiles_SystemPromptContainsSubmitResultGuidance(t *testing.T) {
	profiles := map[string]ProviderProfile{
		"openai":    NewOpenAIProfile("gpt-5.2"),
		"anthropic": NewAnthropicProfile("claude-test"),
		"gemini":    NewGeminiProfile("gemini-test"),
	}
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	for name, p := range profiles {
		prompt := p.BuildSystemPrompt(env, nil, nil, "")

		if !strings.Contains(prompt, "communicate") {
			t.Errorf("profile %q system prompt missing submit_result guidance", name)
		}
		if !strings.Contains(prompt, "inbox") {
			t.Errorf("profile %q system prompt missing inbox guidance", name)
		}
	}
}

// TestAllProfiles_SystemPromptContainsSkillsGuidance verifies that
// Anthropic/Gemini include use_skill guidance and OpenAI does not
// include skills guidance (skills are not rendered for OpenAI).
func TestAllProfiles_SystemPromptContainsSkillsGuidance(t *testing.T) {
	profiles := map[string]ProviderProfile{
		"openai":    NewOpenAIProfile("gpt-5.2"),
		"anthropic": NewAnthropicProfile("claude-test"),
		"gemini":    NewGeminiProfile("gemini-test"),
	}
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	for name, p := range profiles {
		prompt := p.BuildSystemPrompt(env, nil, nil, "")

		// Anthropic and Gemini use the use_skill tool.
		if name != "openai" && !strings.Contains(prompt, "use_skill") {
			t.Errorf("profile %q system prompt missing use_skill guidance", name)
		}

		// OpenAI should NOT have skills-related guidance.
		if name == "openai" && strings.Contains(prompt, "<skills>") {
			t.Errorf("profile %q system prompt should NOT contain <skills> section", name)
		}
	}
}

func TestBuildSystemPrompt_IncludesSkillsList(t *testing.T) {
	// Anthropic profile has use_skill, so skills are rendered in system prompt.
	p := NewAnthropicProfile("claude-test")
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	skills := []SkillMeta{
		{Name: "greet", Description: "Greeting skill", SkillFile: "/tmp/skills/greet/SKILL.md"},
		{Name: "deploy", Description: "Deploy skill", SkillFile: "/tmp/skills/deploy/SKILL.md"},
	}
	prompt := p.BuildSystemPrompt(env, nil, skills, "")

	if !strings.Contains(prompt, "<skills>") {
		t.Error("prompt missing <skills> section")
	}
	if !strings.Contains(prompt, "- greet: Greeting skill") {
		t.Error("prompt missing greet skill entry")
	}
	if !strings.Contains(prompt, "- deploy: Deploy skill") {
		t.Error("prompt missing deploy skill entry")
	}
	if !strings.Contains(prompt, "</skills>") {
		t.Error("prompt missing </skills> closing tag")
	}
}

func TestBuildSystemPrompt_OpenAI_NoSkillsList(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	skills := []SkillMeta{
		{Name: "greet", Description: "Greeting skill", SkillFile: "/tmp/skills/greet/SKILL.md"},
	}
	prompt := p.BuildSystemPrompt(env, nil, skills, "")

	if strings.Contains(prompt, "<skills>") {
		t.Error("OpenAI prompt should NOT contain <skills> section")
	}
}

func TestBuildSystemPrompt_NoSkills_NoSkillsSection(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	prompt := p.BuildSystemPrompt(env, nil, nil, "")

	// The guidance text mentions <skills> as a reference, but the actual
	// structured block starts with "<skills>\n- " (a skill entry). Verify
	// no structured block is present by checking for "</skills>".
	if strings.Contains(prompt, "</skills>") {
		t.Error("prompt should not contain </skills> section when no skills present")
	}
}

// TestAllProfiles_SystemPromptContainsSubagentGuidance verifies that all
// profiles include behavioral guidance for subagent delegation.
func TestAllProfiles_SystemPromptContainsSubagentGuidance(t *testing.T) {
	profiles := map[string]ProviderProfile{
		"openai":    NewOpenAIProfile("gpt-5.2"),
		"anthropic": NewAnthropicProfile("claude-test"),
		"gemini":    NewGeminiProfile("gemini-test"),
	}
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	for name, p := range profiles {
		prompt := p.BuildSystemPrompt(env, nil, nil, "")

		if !strings.Contains(prompt, "spawn_agent") {
			t.Errorf("profile %q system prompt missing spawn_agent guidance", name)
		}
		if !strings.Contains(prompt, "Subagent delegation") {
			t.Errorf("profile %q system prompt missing subagent delegation section", name)
		}
	}
}

func TestBuildSystemPrompt_SubagentGuidanceContent(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}
	prompt := p.BuildSystemPrompt(env, nil, nil, "")

	// Should mention exploration, implementation, and verification use cases.
	for _, keyword := range []string{"Explore", "implementation", "verification"} {
		if !strings.Contains(prompt, keyword) {
			t.Errorf("subagent guidance missing %q keyword", keyword)
		}
	}
	// Should mention task_list for parent coordination.
	if !strings.Contains(prompt, "task_list") {
		t.Error("subagent guidance should mention task_list for coordination")
	}
	// Should clarify that task_list is for the parent agent, not shared across subagents.
	if !strings.Contains(prompt, "communicate") {
		t.Error("subagent guidance should mention submit_result for subagent results")
	}
	// Should NOT imply shared task_list across subagents.
	if strings.Contains(prompt, "coordinate work across subagents") {
		t.Error("subagent guidance should not imply shared task_list across subagents")
	}
}

func TestGeminiProfile_IncludesWebSearch(t *testing.T) {
	assertHasTool(t, NewGeminiProfile("gemini-test"), "web_search")
	assertMissingTool(t, NewOpenAIProfile("gpt-5.2"), "web_search")
	assertMissingTool(t, NewAnthropicProfile("claude-test"), "web_search")
}

func TestProviderProfile_WithBasePrompt(t *testing.T) {
	orig := NewOpenAIProfile("gpt-5.2")
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux"}

	custom := orig.WithBasePrompt("Custom base prompt for testing.")

	// Custom prompt should appear in the system prompt output.
	prompt := custom.BuildSystemPrompt(env, nil, nil, "")
	if !strings.Contains(prompt, "Custom base prompt for testing.") {
		t.Error("custom base prompt not found in system prompt")
	}

	// Original should be unmodified.
	origPrompt := orig.BuildSystemPrompt(env, nil, nil, "")
	if strings.Contains(origPrompt, "Custom base prompt for testing.") {
		t.Error("original profile was mutated")
	}
	if !strings.Contains(origPrompt, "OpenAI profile") {
		t.Error("original profile lost its embedded prompt")
	}
}

func TestProviderProfile_WithBasePrompt_PreservesOtherFields(t *testing.T) {
	orig := NewOpenAIProfile("gpt-5.2")
	custom := orig.WithBasePrompt("different prompt")

	if custom.ID() != orig.ID() {
		t.Errorf("ID changed: got %q want %q", custom.ID(), orig.ID())
	}
	if custom.Model() != orig.Model() {
		t.Errorf("Model changed: got %q want %q", custom.Model(), orig.Model())
	}
	if len(custom.ToolDefinitions()) != len(orig.ToolDefinitions()) {
		t.Errorf("tool count changed: got %d want %d", len(custom.ToolDefinitions()), len(orig.ToolDefinitions()))
	}
	if custom.ContextWindowSize() != orig.ContextWindowSize() {
		t.Errorf("context window changed: got %d want %d", custom.ContextWindowSize(), orig.ContextWindowSize())
	}
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

func TestSpawnAgent_HasWorkingDirAndMaxTurns(t *testing.T) {
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
					if _, ok := props["working_dir"]; !ok {
						t.Fatal("spawn_agent missing working_dir parameter")
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

func TestAnthropicProfile_SystemPromptCoversSpecTopics(t *testing.T) {
	p := NewAnthropicProfile("claude-opus-4-6")
	prompt := p.BuildSystemPrompt(EnvironmentInfo{}, nil, nil, "")

	required := []string{
		"old_string must be unique",
		"edit_file",
		"edit existing files over creating",
	}
	for _, substr := range required {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(substr)) {
			t.Fatalf("Anthropic prompt missing required topic: %q", substr)
		}
	}
}

func TestGeminiProfile_SystemPromptCoversSpecTopics(t *testing.T) {
	p := NewGeminiProfile("gemini-2.5-pro")
	prompt := p.BuildSystemPrompt(EnvironmentInfo{}, nil, nil, "")

	required := []string{
		"GEMINI.md",
		"read_many_files",
		"list_directory",
	}
	for _, substr := range required {
		if !strings.Contains(prompt, substr) {
			t.Fatalf("Gemini prompt missing required topic: %q", substr)
		}
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

func TestGeminiProfile_ProviderPromptUsesMappedToolNames(t *testing.T) {
	// Read the embedded Gemini provider prompt directly to verify it uses
	// mapped tool names. The Gemini profile maps:
	//   list_dir→list_directory, grep→grep_search, shell→run_shell_command
	// The provider prompt must use mapped names so the model sees consistent
	// names between the tool list and the behavioral guidance.
	b, err := embeddedPrompts.ReadFile("prompts/system.gemini.md")
	if err != nil {
		t.Fatalf("reading embedded gemini prompt: %v", err)
	}
	prompt := string(b)

	checks := []struct {
		canonical string
		mapped    string
	}{
		{"list_dir", "list_directory"},
		{"grep", "grep_search"},
		{"shell", "run_shell_command"},
	}
	for _, c := range checks {
		stripped := strings.ReplaceAll(prompt, c.mapped, "")
		if strings.Contains(stripped, c.canonical) {
			t.Errorf("Gemini provider prompt contains bare %q — should use %q to match the mapped tool name", c.canonical, c.mapped)
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

func TestAnthropicProfile_WithSubmitResultRequiredDataKeys(t *testing.T) {
	p := NewAnthropicProfile("claude-opus-4-6")
	p2 := WithSubmitResultRequiredDataKeys(p, []string{"tasks"})
	if p2 == nil {
		t.Fatal("WithSubmitResultRequiredDataKeys returned nil")
	}
	// Should still be a valid profile with correct ID.
	if p2.ID() != "anthropic" {
		t.Fatalf("got ID %q", p2.ID())
	}
	if p2.Model() != "claude-opus-4-6" {
		t.Fatalf("got model %q", p2.Model())
	}
}

func TestAnthropicProfile_WithBasePrompt(t *testing.T) {
	orig := NewAnthropicProfile("claude-opus-4-6")
	custom := orig.WithBasePrompt("Custom anthropic prompt.")
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux"}

	prompt := custom.BuildSystemPrompt(env, nil, nil, "")
	if !strings.Contains(prompt, "Custom anthropic prompt.") {
		t.Error("custom base prompt not found")
	}
	// Should preserve anthropic-specific context window.
	if custom.ContextWindowSize() != 200_000 {
		t.Errorf("context window changed: got %d", custom.ContextWindowSize())
	}
}

func TestGeminiProfile_ContextWindow_Is1M(t *testing.T) {
	p := NewGeminiProfile("gemini-2.5-flash")
	if p.ContextWindowSize() != 1_000_000 {
		t.Errorf("expected 1000000, got %d", p.ContextWindowSize())
	}
}

func TestBuildSystemPrompt_ExtraToolsBeforeProjectDocs(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-11"}
	docs := []ProjectDoc{{Path: "AGENTS.md", Content: "project instructions here"}}
	extra := "- mcp__server__tool1: Does thing one\n- my_custom_tool: Does custom things\n"

	prompt := p.BuildSystemPrompt(env, docs, nil, extra)

	beginIdx := strings.Index(prompt, "----- BEGIN AGENTS.md -----")
	if beginIdx < 0 {
		t.Fatal("prompt missing project doc BEGIN marker")
	}
	mcpIdx := strings.Index(prompt, "mcp__server__tool1")
	if mcpIdx < 0 {
		t.Fatal("prompt missing extra tool description for mcp__server__tool1")
	}
	customIdx := strings.Index(prompt, "my_custom_tool")
	if customIdx < 0 {
		t.Fatal("prompt missing extra tool description for my_custom_tool")
	}
	if mcpIdx > beginIdx {
		t.Errorf("extra tools (pos %d) must appear before project docs (pos %d)", mcpIdx, beginIdx)
	}
	if customIdx > beginIdx {
		t.Errorf("custom tool (pos %d) must appear before project docs (pos %d)", customIdx, beginIdx)
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
	prompt := p.BuildSystemPrompt(env, nil, nil, "")

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

	// Workspace section should come after environment but before tool list.
	wsIdx := strings.Index(prompt, "<workspace>")
	envIdx := strings.Index(prompt, "</environment>")
	toolIdx := strings.Index(prompt, "Tools:")
	if wsIdx < envIdx {
		t.Errorf("workspace (pos %d) should come after environment (pos %d)", wsIdx, envIdx)
	}
	if wsIdx > toolIdx {
		t.Errorf("workspace (pos %d) should come before tools (pos %d)", wsIdx, toolIdx)
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
	prompt := p.BuildSystemPrompt(env, nil, nil, "")

	// Should NOT render an empty workspace section.
	if strings.Contains(prompt, "<workspace>") {
		t.Error("empty workspace should not render a <workspace> section")
	}
}

func TestBuildSystemPrompt_WorkspaceTestFilesCallout(t *testing.T) {
	dir := t.TempDir()

	// Create test files only.
	for _, f := range []struct{ path, content string }{
		{"app.py", "print('app')\n"},
		{"tests/test_app.py", "def test_app(): pass\n"},
		{"tests/test_integration.py", "def test_int(): pass\n"},
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
	prompt := p.BuildSystemPrompt(env, nil, nil, "")

	// The test files callout should list test files (without "run these to verify").
	if !strings.Contains(prompt, "Test files:") {
		t.Error("workspace section missing 'Test files:' callout")
	}
	if strings.Contains(prompt, "run these to verify") {
		t.Error("workspace section should NOT say 'run these to verify'")
	}
	// Test file paths should be absolute.
	if !strings.Contains(prompt, filepath.Join(dir, "tests/test_app.py")) {
		t.Errorf("workspace section missing absolute path for tests/test_app.py in:\n%s", prompt)
	}
	if !strings.Contains(prompt, filepath.Join(dir, "tests/test_integration.py")) {
		t.Errorf("workspace section missing absolute path for tests/test_integration.py in:\n%s", prompt)
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
	prompt := p.BuildSystemPrompt(env, nil, nil, "")

	if !strings.Contains(prompt, "snapshot of the working directory taken at session start") {
		t.Error("workspace section missing static annotation")
	}
}

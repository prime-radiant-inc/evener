package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestProviderProfiles_ToolsetsAndDocSelection(t *testing.T) {
	openai := NewOpenAIProfile("gpt-5.2")
	if openai.ID() != "openai" {
		t.Fatalf("openai id: %q", openai.ID())
	}
	if openai.SupportsParallelToolCalls() {
		t.Fatalf("openai should not support parallel tool calls by default")
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
			"send_input",
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
			"send_input",
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
			"send_input",
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
	sysO := openai.BuildSystemPrompt(env, nil, nil)
	if !strings.Contains(sysO, "OpenAI profile") || !strings.Contains(sysO, "apply_patch") {
		t.Fatalf("openai system prompt missing expected base instructions:\n%s", sysO)
	}
	if strings.Contains(sysO, "edit_file") {
		t.Fatalf("openai system prompt should not focus on edit_file:\n%s", sysO)
	}

	anthropic := NewAnthropicProfile("claude-test")
	sysA := anthropic.BuildSystemPrompt(env, nil, nil)
	if !strings.Contains(sysA, "Anthropic profile") || !strings.Contains(sysA, "edit_file") {
		t.Fatalf("anthropic system prompt missing expected base instructions:\n%s", sysA)
	}
	if strings.Contains(sysA, "apply_patch") {
		t.Fatalf("anthropic system prompt should not focus on apply_patch:\n%s", sysA)
	}

	gemini := NewGeminiProfile("gemini-test")
	sysG := gemini.BuildSystemPrompt(env, nil, nil)
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
	if cloned.BuildSystemPrompt(env, nil, nil) == "" {
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
	prompt := p.BuildSystemPrompt(env, nil, nil)

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
		prompt := p.BuildSystemPrompt(env, nil, nil)

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

// TestAllProfiles_SystemPromptContainsCommunicateGuidance verifies that all
// profiles include behavioral guidance for the communicate tool.
func TestAllProfiles_SystemPromptContainsCommunicateGuidance(t *testing.T) {
	profiles := map[string]ProviderProfile{
		"openai":    NewOpenAIProfile("gpt-5.2"),
		"anthropic": NewAnthropicProfile("claude-test"),
		"gemini":    NewGeminiProfile("gemini-test"),
	}
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	for name, p := range profiles {
		prompt := p.BuildSystemPrompt(env, nil, nil)

		if !strings.Contains(prompt, "communicate") {
			t.Errorf("profile %q system prompt missing communicate guidance", name)
		}
		if !strings.Contains(prompt, "communicate(status)") {
			t.Errorf("profile %q system prompt missing communicate(status) guidance", name)
		}
		if !strings.Contains(prompt, "communicate(result)") {
			t.Errorf("profile %q system prompt missing communicate(result) guidance", name)
		}
		if !strings.Contains(prompt, "inbox") {
			t.Errorf("profile %q system prompt missing inbox guidance", name)
		}
	}
}

// TestAllProfiles_SystemPromptContainsSkillsGuidance verifies that all
// profiles include behavioral guidance for the use_skill tool.
func TestAllProfiles_SystemPromptContainsSkillsGuidance(t *testing.T) {
	profiles := map[string]ProviderProfile{
		"openai":    NewOpenAIProfile("gpt-5.2"),
		"anthropic": NewAnthropicProfile("claude-test"),
		"gemini":    NewGeminiProfile("gemini-test"),
	}
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	for name, p := range profiles {
		prompt := p.BuildSystemPrompt(env, nil, nil)

		if !strings.Contains(prompt, "use_skill") {
			t.Errorf("profile %q system prompt missing use_skill guidance", name)
		}
		if !strings.Contains(prompt, "<skills>") || !strings.Contains(prompt, "Skills extend") {
			t.Errorf("profile %q system prompt missing skills guidance text", name)
		}
	}
}

func TestBuildSystemPrompt_IncludesSkillsList(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	skills := []SkillMeta{
		{Name: "greet", Description: "Greeting skill"},
		{Name: "deploy", Description: "Deploy skill"},
	}
	prompt := p.BuildSystemPrompt(env, nil, skills)

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

func TestBuildSystemPrompt_NoSkills_NoSkillsSection(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	env := EnvironmentInfo{WorkingDir: "/tmp", Platform: "linux", Today: "2026-02-09"}

	prompt := p.BuildSystemPrompt(env, nil, nil)

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
		prompt := p.BuildSystemPrompt(env, nil, nil)

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
	prompt := p.BuildSystemPrompt(env, nil, nil)

	// Should mention research, implementation, and verification use cases.
	for _, keyword := range []string{"Research", "Implementation", "Verification"} {
		if !strings.Contains(prompt, keyword) {
			t.Errorf("subagent guidance missing %q keyword", keyword)
		}
	}
	// Should mention task_list for parent coordination.
	if !strings.Contains(prompt, "task_list") {
		t.Error("subagent guidance should mention task_list for coordination")
	}
	// Should clarify that task_list is for the parent agent, not shared across subagents.
	if !strings.Contains(prompt, "communicate(result)") {
		t.Error("subagent guidance should mention communicate(result) for subagent results")
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
	prompt := custom.BuildSystemPrompt(env, nil, nil)
	if !strings.Contains(prompt, "Custom base prompt for testing.") {
		t.Error("custom base prompt not found in system prompt")
	}

	// Original should be unmodified.
	origPrompt := orig.BuildSystemPrompt(env, nil, nil)
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
	if got := NewOpenAIProfile("gpt-5.2").DefaultCommandTimeoutMS(); got != 10_000 {
		t.Fatalf("OpenAI timeout = %d, want 10000", got)
	}
	if got := NewAnthropicProfile("claude-opus-4-6").DefaultCommandTimeoutMS(); got != 120_000 {
		t.Fatalf("Anthropic timeout = %d, want 120000", got)
	}
	if got := NewGeminiProfile("gemini-2.5-pro").DefaultCommandTimeoutMS(); got != 10_000 {
		t.Fatalf("Gemini timeout = %d, want 10000", got)
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
				if td.Name == "send_input" {
					props := td.Parameters["properties"].(map[string]any)
					if _, ok := props["message"]; !ok {
						t.Fatal("send_input should have 'message' parameter")
					}
					if _, ok := props["input"]; ok {
						t.Fatal("send_input should not have 'input' parameter")
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
			t.Fatal("send_input tool not found")
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
	prompt := p.BuildSystemPrompt(EnvironmentInfo{}, nil, nil)

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
	prompt := p.BuildSystemPrompt(EnvironmentInfo{}, nil, nil)

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

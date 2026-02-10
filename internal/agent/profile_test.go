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
	if gemini.ID() != "google" {
		t.Fatalf("gemini id: %q", gemini.ID())
	}
	if !gemini.SupportsParallelToolCalls() {
		t.Fatalf("gemini should support parallel tool calls")
	}
	assertHasTool(t, gemini, "edit_file")
	assertHasTool(t, gemini, "read_many_files")
	assertHasTool(t, gemini, "list_dir")
	assertMissingTool(t, gemini, "apply_patch")
}

func TestProviderProfiles_ToolLists_MatchSpec(t *testing.T) {
	t.Run("openai", func(t *testing.T) {
		p := NewOpenAIProfile("gpt-5.2")
		assertToolListExact(t, p, []string{
			"read_file",
			"apply_patch",
			"write_file",
			"shell",
			"grep",
			"glob",
			"spawn_agent",
			"send_input",
			"wait",
			"close_agent",
			"task_list",
			"web_fetch",
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
		})
	})
	t.Run("gemini", func(t *testing.T) {
		p := NewGeminiProfile("gemini-test")
		// coding-agent-loop-spec.md 3.6: "Profile tool list for Gemini".
		assertToolListExact(t, p, []string{
			"read_file",
			"read_many_files",
			"write_file",
			"edit_file",
			"shell",
			"grep",
			"glob",
			"list_dir",
			"spawn_agent",
			"send_input",
			"wait",
			"close_agent",
			"task_list",
			"web_fetch",
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
	sysO := openai.BuildSystemPrompt(env, nil)
	if !strings.Contains(sysO, "OpenAI profile") || !strings.Contains(sysO, "apply_patch") {
		t.Fatalf("openai system prompt missing expected base instructions:\n%s", sysO)
	}
	if strings.Contains(sysO, "edit_file") {
		t.Fatalf("openai system prompt should not focus on edit_file:\n%s", sysO)
	}

	anthropic := NewAnthropicProfile("claude-test")
	sysA := anthropic.BuildSystemPrompt(env, nil)
	if !strings.Contains(sysA, "Anthropic profile") || !strings.Contains(sysA, "edit_file") {
		t.Fatalf("anthropic system prompt missing expected base instructions:\n%s", sysA)
	}
	if strings.Contains(sysA, "apply_patch") {
		t.Fatalf("anthropic system prompt should not focus on apply_patch:\n%s", sysA)
	}

	gemini := NewGeminiProfile("gemini-test")
	sysG := gemini.BuildSystemPrompt(env, nil)
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
	if cloned.BuildSystemPrompt(env, nil) == "" {
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

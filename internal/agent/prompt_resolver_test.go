package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSystemPrompt_ComposesBaseAndProvider(t *testing.T) {
	// Embedded result must contain both base.md content and provider-specific content.
	tests := []struct {
		provider        string
		providerSnippet string // from provider file
		baseSnippet     string // from base.md
	}{
		{"openai", "apply_patch", "task_list"},
		{"anthropic", "edit_file", "communicate"},
		{"gemini", "edit_file", "Subagent delegation"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			prompt, err := ResolveSystemPrompt(tt.provider, "some-model", "", "", "", nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(prompt, tt.providerSnippet) {
				t.Errorf("prompt missing provider content %q", tt.providerSnippet)
			}
			if !strings.Contains(prompt, tt.baseSnippet) {
				t.Errorf("prompt missing base content %q", tt.baseSnippet)
			}
		})
	}
}

func TestResolveSystemPrompt_ProviderBeforeBase(t *testing.T) {
	// Provider identity and tool docs should come before base guidance.
	prompt, err := ResolveSystemPrompt("openai", "some-model", "", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	providerIdx := strings.Index(prompt, "apply_patch")
	baseIdx := strings.Index(prompt, "## task_list")
	if providerIdx < 0 || baseIdx < 0 {
		t.Fatal("prompt missing expected content")
	}
	if providerIdx >= baseIdx {
		t.Error("provider content should appear before base content")
	}
}

func TestEmbeddedPrompts_ContainCoreGuidance(t *testing.T) {
	// All embedded prompts must include security, code quality, and
	// change discipline guidance regardless of provider.
	required := []struct {
		label   string
		snippet string
	}{
		{"security", "security"},
		{"minimal changes", "existing file"},
		{"understand before modifying", "before editing"},
		{"verification before completion", "verify"},
		{"root cause", "root cause"},
		{"decisive action", "decisive"},
	}

	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		prompt, err := ResolveSystemPrompt(provider, "some-model", "", "", "", nil)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		for _, r := range required {
			if !strings.Contains(prompt, r.snippet) {
				t.Errorf("%s prompt missing %s guidance (looked for %q)", provider, r.label, r.snippet)
			}
		}
	}
}

func TestResolveSystemPrompt_CLIOverride(t *testing.T) {
	// CLI flag replaces the entire embedded base.
	tmp := t.TempDir()
	cliFile := filepath.Join(tmp, "custom.md")
	os.WriteFile(cliFile, []byte("custom CLI prompt"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", cliFile, "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "custom CLI prompt" {
		t.Errorf("got %q, want %q", prompt, "custom CLI prompt")
	}
	// Should NOT contain embedded content.
	if strings.Contains(prompt, "task_list") {
		t.Error("CLI override should replace embedded content, not append to it")
	}
}

func TestResolveSystemPrompt_CLIOverrideMissing(t *testing.T) {
	_, err := ResolveSystemPrompt("openai", "gpt-4o", "/nonexistent/path.md", "", "", nil)
	if err == nil {
		t.Fatal("expected error for missing CLI prompt file")
	}
}

func TestResolveSystemPrompt_ProjectAddsToEmbedded(t *testing.T) {
	// Project-level file is appended to the embedded prompt, not replacing it.
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, ".serf", "prompts")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "system.openai.md"), []byte("project openai rules"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", "", projDir, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must contain both embedded and project content.
	if !strings.Contains(prompt, "apply_patch") {
		t.Error("missing embedded provider content")
	}
	if !strings.Contains(prompt, "task_list") {
		t.Error("missing embedded base content")
	}
	if !strings.Contains(prompt, "project openai rules") {
		t.Error("missing project addition")
	}
}

func TestResolveSystemPrompt_ProjectGenericAndProviderBothAppended(t *testing.T) {
	// Both system.md and system.<provider>.md in a project dir should be appended.
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "prompts")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "system.md"), []byte("generic project rules"), 0644)
	os.WriteFile(filepath.Join(projDir, "system.openai.md"), []byte("openai project rules"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", "", projDir, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "generic project rules") {
		t.Error("missing generic project addition")
	}
	if !strings.Contains(prompt, "openai project rules") {
		t.Error("missing provider-specific project addition")
	}
	// Generic should come before provider-specific.
	genIdx := strings.Index(prompt, "generic project rules")
	provIdx := strings.Index(prompt, "openai project rules")
	if genIdx >= provIdx {
		t.Error("generic addition should come before provider-specific addition")
	}
}

func TestResolveSystemPrompt_GlobalAndProjectBothAppended(t *testing.T) {
	// Both global and project additions should be present, global first.
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "project", "prompts")
	globalDir := filepath.Join(tmp, "global", "prompts")
	os.MkdirAll(projDir, 0755)
	os.MkdirAll(globalDir, 0755)
	os.WriteFile(filepath.Join(globalDir, "system.openai.md"), []byte("global rules"), 0644)
	os.WriteFile(filepath.Join(projDir, "system.openai.md"), []byte("project rules"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", "", projDir, globalDir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "global rules") {
		t.Error("missing global addition")
	}
	if !strings.Contains(prompt, "project rules") {
		t.Error("missing project addition")
	}
	// Global before project.
	globalIdx := strings.Index(prompt, "global rules")
	projIdx := strings.Index(prompt, "project rules")
	if globalIdx >= projIdx {
		t.Error("global addition should come before project addition")
	}
}

func TestResolveSystemPrompt_GlobalOverride(t *testing.T) {
	// Global addition appended to embedded prompt.
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "prompts")
	os.MkdirAll(globalDir, 0755)
	os.WriteFile(filepath.Join(globalDir, "system.anthropic.md"), []byte("global anthropic rules"), 0644)

	prompt, err := ResolveSystemPrompt("anthropic", "claude-sonnet", "", "", globalDir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "edit_file") {
		t.Error("missing embedded provider content")
	}
	if !strings.Contains(prompt, "global anthropic rules") {
		t.Error("missing global addition")
	}
}

func TestResolveSystemPrompt_AppendPaths(t *testing.T) {
	// CLI --system-prompt-append files are always appended.
	tmp := t.TempDir()
	f1 := filepath.Join(tmp, "extra1.md")
	f2 := filepath.Join(tmp, "extra2.md")
	os.WriteFile(f1, []byte("append one"), 0644)
	os.WriteFile(f2, []byte("append two"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", "", "", "", []string{f1, f2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "apply_patch") {
		t.Error("missing embedded content")
	}
	if !strings.Contains(prompt, "append one") {
		t.Error("missing first append")
	}
	if !strings.Contains(prompt, "append two") {
		t.Error("missing second append")
	}
	// Appends should come after embedded content.
	embIdx := strings.Index(prompt, "task_list")
	app1Idx := strings.Index(prompt, "append one")
	app2Idx := strings.Index(prompt, "append two")
	if embIdx >= app1Idx {
		t.Error("embedded content should come before appends")
	}
	if app1Idx >= app2Idx {
		t.Error("appends should be in order")
	}
}

func TestResolveSystemPrompt_AppendWithCLIOverride(t *testing.T) {
	// CLI override + append should both work together.
	tmp := t.TempDir()
	cliFile := filepath.Join(tmp, "base.md")
	appendFile := filepath.Join(tmp, "extra.md")
	os.WriteFile(cliFile, []byte("custom base"), 0644)
	os.WriteFile(appendFile, []byte("extra guidance"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", cliFile, "", "", []string{appendFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "custom base") {
		t.Error("missing CLI override content")
	}
	if !strings.Contains(prompt, "extra guidance") {
		t.Error("missing append content")
	}
	// Should NOT contain embedded content.
	if strings.Contains(prompt, "task_list") {
		t.Error("CLI override should replace embedded, not include it")
	}
}

func TestResolveSystemPrompt_AppendMissingFile(t *testing.T) {
	_, err := ResolveSystemPrompt("openai", "gpt-4o", "", "", "", []string{"/nonexistent/path.md"})
	if err == nil {
		t.Fatal("expected error for missing append file")
	}
}

func TestResolveSystemPrompt_UnknownProviderGetsBase(t *testing.T) {
	// Unknown provider should still get the base prompt.
	prompt, err := ResolveSystemPrompt("unknown-provider", "some-model", "", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt == "" {
		t.Fatal("expected non-empty prompt for unknown provider")
	}
	// Should have base content.
	if !strings.Contains(prompt, "task_list") {
		t.Error("unknown provider should still get base prompt")
	}
}

func TestEmbeddedProviderCandidates(t *testing.T) {
	names := embeddedProviderCandidates("openai", "gpt-4o")
	want := []string{"system.openai.gpt-4o.md", "system.openai.md"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, n, want[i])
		}
	}
}

func TestAdditionCandidateNames(t *testing.T) {
	names := additionCandidateNames("openai")
	want := []string{"system.md", "system.openai.md"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, n, want[i])
		}
	}
}

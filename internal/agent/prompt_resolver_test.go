package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSystemPrompt_EmbeddedDefaults(t *testing.T) {
	// With no overrides, should return the embedded default for each provider.
	tests := []struct {
		provider string
		contains string // substring that must appear in the result
	}{
		{"openai", "apply_patch"},
		{"anthropic", "edit_file"},
		{"google", "edit_file"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			prompt, err := ResolveSystemPrompt(tt.provider, "some-model", "", "", "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if prompt == "" {
				t.Fatal("expected non-empty prompt")
			}
			if !contains(prompt, tt.contains) {
				t.Errorf("prompt for %s missing %q", tt.provider, tt.contains)
			}
		})
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
	}

	for _, provider := range []string{"openai", "anthropic", "google"} {
		prompt, err := ResolveSystemPrompt(provider, "some-model", "", "", "")
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		for _, r := range required {
			if !contains(prompt, r.snippet) {
				t.Errorf("%s prompt missing %s guidance (looked for %q)", provider, r.label, r.snippet)
			}
		}
	}
}

func TestResolveSystemPrompt_CLIOverride(t *testing.T) {
	// CLI flag should take highest priority.
	tmp := t.TempDir()
	cliFile := filepath.Join(tmp, "custom.md")
	os.WriteFile(cliFile, []byte("custom CLI prompt"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", cliFile, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "custom CLI prompt" {
		t.Errorf("got %q, want %q", prompt, "custom CLI prompt")
	}
}

func TestResolveSystemPrompt_CLIOverrideMissing(t *testing.T) {
	_, err := ResolveSystemPrompt("openai", "gpt-4o", "/nonexistent/path.md", "", "")
	if err == nil {
		t.Fatal("expected error for missing CLI prompt file")
	}
}

func TestResolveSystemPrompt_ProjectOverride(t *testing.T) {
	// Project-level override with provider-specific naming.
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, ".serf", "prompts")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "system.openai.md"), []byte("project openai prompt"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", "", projDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "project openai prompt" {
		t.Errorf("got %q, want %q", prompt, "project openai prompt")
	}
}

func TestResolveSystemPrompt_ProjectModelSpecific(t *testing.T) {
	// Model-specific override should win over provider-only.
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, ".serf", "prompts")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "system.openai.md"), []byte("provider prompt"), 0644)
	os.WriteFile(filepath.Join(projDir, "system.openai.gpt-4o.md"), []byte("model prompt"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", "", projDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "model prompt" {
		t.Errorf("got %q, want %q", prompt, "model prompt")
	}
}

func TestResolveSystemPrompt_GlobalOverride(t *testing.T) {
	// Global override should be used when no project override exists.
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "prompts")
	os.MkdirAll(globalDir, 0755)
	os.WriteFile(filepath.Join(globalDir, "system.anthropic.md"), []byte("global anthropic prompt"), 0644)

	prompt, err := ResolveSystemPrompt("anthropic", "claude-sonnet", "", "", globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "global anthropic prompt" {
		t.Errorf("got %q, want %q", prompt, "global anthropic prompt")
	}
}

func TestResolveSystemPrompt_ProjectShadowsGlobal(t *testing.T) {
	// Project override should shadow global.
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "project", "prompts")
	globalDir := filepath.Join(tmp, "global", "prompts")
	os.MkdirAll(projDir, 0755)
	os.MkdirAll(globalDir, 0755)
	os.WriteFile(filepath.Join(projDir, "system.openai.md"), []byte("project wins"), 0644)
	os.WriteFile(filepath.Join(globalDir, "system.openai.md"), []byte("global loses"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", "", projDir, globalDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "project wins" {
		t.Errorf("got %q, want %q", prompt, "project wins")
	}
}

func TestResolveSystemPrompt_GenericFallback(t *testing.T) {
	// A system.md (no provider) should be used as ultimate override fallback.
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "prompts")
	os.MkdirAll(projDir, 0755)
	os.WriteFile(filepath.Join(projDir, "system.md"), []byte("generic prompt"), 0644)

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", "", projDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "generic prompt" {
		t.Errorf("got %q, want %q", prompt, "generic prompt")
	}
}

func TestResolveSystemPrompt_UnknownProviderFallsToGenericEmbed(t *testing.T) {
	// Unknown provider with no overrides should still get a reasonable default.
	prompt, err := ResolveSystemPrompt("unknown-provider", "some-model", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should fall back to the generic embedded prompt.
	if prompt == "" {
		t.Fatal("expected non-empty prompt for unknown provider")
	}
}

func TestPromptCandidateNames(t *testing.T) {
	names := promptCandidateNames("openai", "gpt-4o")
	want := []string{"system.openai.gpt-4o.md", "system.openai.md", "system.md"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, n, want[i])
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

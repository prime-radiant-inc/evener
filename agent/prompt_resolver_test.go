package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSystemPrompt_CLIOverride(t *testing.T) {
	// CLI flag replaces the entire embedded base.
	tmp := t.TempDir()
	cliFile := filepath.Join(tmp, "custom.md")
	if err := os.WriteFile(cliFile, []byte("custom CLI prompt"), 0644); err != nil {
		t.Fatal(err)
	}

	prompt, err := ResolveSystemPrompt("openai", "gpt-4o", cliFile, "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prompt != "custom CLI prompt" {
		t.Errorf("got %q, want %q", prompt, "custom CLI prompt")
	}
}

func TestResolveSystemPrompt_CLIOverrideMissing(t *testing.T) {
	_, err := ResolveSystemPrompt("openai", "gpt-4o", "/nonexistent/path.md", "", "", nil)
	if err == nil {
		t.Fatal("expected error for missing CLI prompt file")
	}
}

func TestResolveSystemPrompt_ProjectGenericAndProviderBothAppended(t *testing.T) {
	// Both system.md and system.<provider>.md in a project dir should be appended.
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "prompts")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "system.md"), []byte("generic project rules"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "system.openai.md"), []byte("openai project rules"), 0644); err != nil {
		t.Fatal(err)
	}

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
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "system.openai.md"), []byte("global rules"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "system.openai.md"), []byte("project rules"), 0644); err != nil {
		t.Fatal(err)
	}

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

func TestResolveSystemPrompt_GlobalAddition(t *testing.T) {
	// Global addition should be included in the prompt.
	tmp := t.TempDir()
	globalDir := filepath.Join(tmp, "prompts")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "system.anthropic.md"), []byte("global anthropic rules"), 0644); err != nil {
		t.Fatal(err)
	}

	prompt, err := ResolveSystemPrompt("anthropic", "claude-sonnet", "", "", globalDir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(prompt, "global anthropic rules") {
		t.Error("missing global addition")
	}
}

func TestResolveSystemPrompt_AppendWithCLIOverride(t *testing.T) {
	// CLI override + append should both work together.
	tmp := t.TempDir()
	cliFile := filepath.Join(tmp, "base.md")
	appendFile := filepath.Join(tmp, "extra.md")
	if err := os.WriteFile(cliFile, []byte("custom base"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appendFile, []byte("extra guidance"), 0644); err != nil {
		t.Fatal(err)
	}

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
}

func TestResolveSystemPrompt_AppendMissingFile(t *testing.T) {
	_, err := ResolveSystemPrompt("openai", "gpt-4o", "", "", "", []string{"/nonexistent/path.md"})
	if err == nil {
		t.Fatal("expected error for missing append file")
	}
}

func TestResolveSystemPromptWithSources_ProjectAndGlobalLabels(t *testing.T) {
	// Project and global additions should appear with correct label prefixes.
	tmp := t.TempDir()
	projDir := filepath.Join(tmp, "project")
	globalDir := filepath.Join(tmp, "global")
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "system.md"), []byte("global generic"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "system.openai.md"), []byte("project openai"), 0644); err != nil {
		t.Fatal(err)
	}

	_, sources, err := ResolveSystemPromptWithSources("openai", "gpt-4o", "", projDir, globalDir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Collect labels for validation.
	labels := make([]string, len(sources))
	for i, s := range sources {
		labels[i] = s.Label
	}

	// Must include global and project labels.
	found := map[string]bool{}
	for _, s := range sources {
		found[s.Label] = true
	}
	if !found["global:system.md"] {
		t.Errorf("missing global:system.md in sources: %v", labels)
	}
	if !found["project:system.openai.md"] {
		t.Errorf("missing project:system.openai.md in sources: %v", labels)
	}
}

func TestResolveSystemPromptWithSources_CLIOverrideLabel(t *testing.T) {
	tmp := t.TempDir()
	cliFile := filepath.Join(tmp, "custom.md")
	if err := os.WriteFile(cliFile, []byte("custom"), 0644); err != nil {
		t.Fatal(err)
	}

	_, sources, err := ResolveSystemPromptWithSources("openai", "gpt-4o", cliFile, "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d: %+v", len(sources), sources)
	}
	wantLabel := "cli:" + cliFile
	if sources[0].Label != wantLabel {
		t.Errorf("source label = %q, want %q", sources[0].Label, wantLabel)
	}
	if sources[0].Size != 6 { // len("custom")
		t.Errorf("source size = %d, want 6", sources[0].Size)
	}
}

func TestResolveSystemPromptWithSources_AppendLabels(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "extra.md")
	if err := os.WriteFile(f, []byte("extra stuff"), 0644); err != nil {
		t.Fatal(err)
	}

	_, sources, err := ResolveSystemPromptWithSources("openai", "gpt-4o", "", "", "", []string{f})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	last := sources[len(sources)-1]
	wantLabel := "append:" + f
	if last.Label != wantLabel {
		t.Errorf("last source label = %q, want %q", last.Label, wantLabel)
	}
	if last.Size != len("extra stuff") {
		t.Errorf("last source size = %d, want %d", last.Size, len("extra stuff"))
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

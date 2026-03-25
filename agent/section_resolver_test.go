package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskSource_ReadFile(t *testing.T) {
	dir := t.TempDir()
	content := "I am serf"
	if err := os.WriteFile(filepath.Join(dir, "identity.md"), []byte(content), 0644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	src := diskSource{dir: dir}

	data, ok := src.ReadFile("identity.md")
	if !ok {
		t.Fatal("expected ok=true for existing file")
	}
	if string(data) != content {
		t.Errorf("got %q, want %q", string(data), content)
	}

	// Missing file returns (nil, false).
	data, ok = src.ReadFile("nonexistent.md")
	if ok {
		t.Error("expected ok=false for missing file")
	}
	if data != nil {
		t.Error("expected nil data for missing file")
	}
}

func TestDiskSource_EmptyDir(t *testing.T) {
	src := diskSource{dir: ""}
	data, ok := src.ReadFile("anything.md")
	if ok {
		t.Error("expected ok=false for empty dir")
	}
	if data != nil {
		t.Error("expected nil data for empty dir")
	}
}

func TestEmbedSource_ReadFile(t *testing.T) {
	src := embedSource{fs: embeddedPrompts, prefix: "prompts/"}

	data, ok := src.ReadFile("core.md")
	if !ok {
		t.Fatal("expected ok=true for embedded core.md")
	}
	if len(data) == 0 {
		t.Error("expected non-empty content for core.md")
	}

	// Missing file returns (nil, false).
	data, ok = src.ReadFile("nonexistent.md")
	if ok {
		t.Error("expected ok=false for missing embedded file")
	}
	if data != nil {
		t.Error("expected nil data for missing embedded file")
	}
}

// helper: write a file into dir.
func writeSection(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// helper: create a SectionResolver backed by a single temp directory.
func newTestResolver(t *testing.T, dir, provider, agent string) *SectionResolver {
	t.Helper()
	return &SectionResolver{
		provider: provider,
		agent:    agent,
		sources:  []SectionSource{diskSource{dir: dir}},
	}
}

func TestSectionResolver_BaseOnly(t *testing.T) {
	dir := t.TempDir()
	writeSection(t, dir, "identity.md", "I am serf")

	r := newTestResolver(t, dir, "openai", "coordinator")
	got := r.Section("identity", PromptData{})
	if got != "I am serf" {
		t.Errorf("got %q, want %q", got, "I am serf")
	}
}

func TestSectionResolver_ProviderOverride(t *testing.T) {
	dir := t.TempDir()
	writeSection(t, dir, "tools.md", "generic tools")
	writeSection(t, dir, "tools.provider-openai.md", "openai tools")

	r := newTestResolver(t, dir, "openai", "")
	got := r.Section("tools", PromptData{})
	if got != "openai tools" {
		t.Errorf("got %q, want %q", got, "openai tools")
	}
}

func TestSectionResolver_ProviderFallsBackToBase(t *testing.T) {
	dir := t.TempDir()
	writeSection(t, dir, "tools.md", "generic tools")

	r := newTestResolver(t, dir, "anthropic", "")
	got := r.Section("tools", PromptData{})
	if got != "generic tools" {
		t.Errorf("got %q, want %q", got, "generic tools")
	}
}

func TestSectionResolver_PrependAppend(t *testing.T) {
	dir := t.TempDir()
	writeSection(t, dir, "tools.provider-openai_prepend.md", "before")
	writeSection(t, dir, "tools.md", "base")
	writeSection(t, dir, "tools.provider-openai_append.md", "after")

	r := newTestResolver(t, dir, "openai", "")
	got := r.Section("tools", PromptData{})
	want := "before\n\nbase\n\nafter"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSectionResolver_AgentBodyReplaces(t *testing.T) {
	dir := t.TempDir()
	writeSection(t, dir, "communicate.md", "call communicate")
	writeSection(t, dir, "communicate.agent-reviewer.md", "call approve or reject")

	r := newTestResolver(t, dir, "openai", "reviewer")
	got := r.Section("communicate", PromptData{})
	if got != "call approve or reject" {
		t.Errorf("got %q, want %q", got, "call approve or reject")
	}
}

func TestSectionResolver_AgentAppendIsAdditive(t *testing.T) {
	dir := t.TempDir()
	writeSection(t, dir, "tools.md", "base tools")
	writeSection(t, dir, "tools.agent-implementer_append.md", "impl tips")

	r := newTestResolver(t, dir, "openai", "implementer")
	got := r.Section("tools", PromptData{})
	want := "base tools\n\nimpl tips"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSectionResolver_MissingSectionReturnsEmpty(t *testing.T) {
	dir := t.TempDir()

	r := newTestResolver(t, dir, "openai", "coordinator")
	got := r.Section("nonexistent", PromptData{})
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestSectionResolver_SourcePriority(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	writeSection(t, dir1, "identity.md", "project identity")
	writeSection(t, dir2, "identity.md", "global identity")

	r := &SectionResolver{
		provider: "openai",
		agent:    "",
		sources:  []SectionSource{diskSource{dir: dir1}, diskSource{dir: dir2}},
	}
	got := r.Section("identity", PromptData{})
	if got != "project identity" {
		t.Errorf("got %q, want %q", got, "project identity")
	}
}

func TestSectionResolver_TmplRendering(t *testing.T) {
	dir := t.TempDir()
	writeSection(t, dir, "identity.md.tmpl", "Hello {{ .Provider }}")

	r := newTestResolver(t, dir, "openai", "")
	got := r.Section("identity", PromptData{Provider: "openai"})
	if got != "Hello openai" {
		t.Errorf("got %q, want %q", got, "Hello openai")
	}
}

func TestSectionResolver_TmplPriorityOverMd(t *testing.T) {
	dir := t.TempDir()
	writeSection(t, dir, "identity.md.tmpl", "Template {{ .Provider }}")
	writeSection(t, dir, "identity.md", "Static")

	r := newTestResolver(t, dir, "openai", "")
	got := r.Section("identity", PromptData{Provider: "openai"})
	if got != "Template openai" {
		t.Errorf("got %q, want %q", got, "Template openai")
	}
}

func TestSectionResolver_SourceTracking(t *testing.T) {
	dir := t.TempDir()
	writeSection(t, dir, "identity.md", "I am serf")

	r := newTestResolver(t, dir, "openai", "coordinator")
	r.Section("identity", PromptData{})

	sources := r.Sources()
	if len(sources) == 0 {
		t.Fatal("expected non-empty Sources()")
	}
	// Should contain a label referencing the disk path.
	found := false
	for _, s := range sources {
		if strings.Contains(s.Label, "identity.md") {
			found = true
			if s.Size != len("I am serf") {
				t.Errorf("Size=%d, want %d", s.Size, len("I am serf"))
			}
		}
	}
	if !found {
		t.Errorf("no source label mentions identity.md; got %v", sources)
	}
}

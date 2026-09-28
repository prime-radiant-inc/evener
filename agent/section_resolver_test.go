package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/internal/bundled"
)

func TestDiskSource_ReadFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	content := "I am evener"
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
	t.Parallel()
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
	t.Parallel()
	src := embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}

	data, ok := src.ReadFile("identity.md")
	if !ok {
		t.Fatal("expected ok=true for embedded identity.md")
	}
	if len(data) == 0 {
		t.Error("expected non-empty content for identity.md")
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

// helper: create a sectionResolver backed by a single temp directory.
func newTestResolver(t *testing.T, dir, provider, agent string) *sectionResolver {
	t.Helper()
	return &sectionResolver{
		surface: provider,
		agent:   agent,
		sources: []sectionSource{diskSource{dir: dir}},
	}
}

func mustWorkflowAgent(t *testing.T, name string) plugin.Agent {
	t.Helper()
	return coordinatorWorkflowAgentForTest(t, name)
}

func TestSectionResolver_ResolvesToBase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		section  string
		content  string
		provider string
		agent    string
		query    string
		want     string
	}{
		{
			name:     "BaseOnly",
			section:  "identity.md",
			content:  "I am evener",
			provider: "openai",
			agent:    "coordinator",
			query:    "identity",
			want:     "I am evener",
		},
		{
			name:     "ProviderFallsBackToBase",
			section:  "tools.md",
			content:  "generic tools",
			provider: "anthropic",
			agent:    "",
			query:    "tools",
			want:     "generic tools",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeSection(t, dir, c.section, c.content)

			r := newTestResolver(t, dir, c.provider, c.agent)
			got := r.Section(c.query, promptData{})
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSectionResolver_VariantOverridesBase(t *testing.T) {
	t.Parallel()
	type section struct {
		name, content string
	}
	cases := []struct {
		name     string
		sections []section
		provider string
		agent    string
		query    string
		want     string
	}{
		{
			name: "ProviderOverride",
			sections: []section{
				{"tools.md", "generic tools"},
				{"tools.provider-openai.md", "openai tools"},
			},
			provider: "openai",
			agent:    "",
			query:    "tools",
			want:     "openai tools",
		},
		{
			name: "AgentBodyReplaces",
			sections: []section{
				{"communicate.md", "call communicate"},
				{"communicate.agent-reviewer.md", "call approve or reject"},
			},
			provider: "openai",
			agent:    "reviewer",
			query:    "communicate",
			want:     "call approve or reject",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, s := range c.sections {
				writeSection(t, dir, s.name, s.content)
			}

			r := newTestResolver(t, dir, c.provider, c.agent)
			got := r.Section(c.query, promptData{})
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSectionResolver_PrependAppend(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSection(t, dir, "tools.provider-openai_prepend.md", "before")
	writeSection(t, dir, "tools.md", "base")
	writeSection(t, dir, "tools.provider-openai_append.md", "after")

	r := newTestResolver(t, dir, "openai", "")
	got := r.Section("tools", promptData{})
	want := "before\n\nbase\n\nafter"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSectionResolver_AgentAppendIsAdditive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSection(t, dir, "tools.md", "base tools")
	writeSection(t, dir, "tools.agent-implementer_append.md", "impl tips")

	r := newTestResolver(t, dir, "openai", "implementer")
	got := r.Section("tools", promptData{})
	want := "base tools\n\nimpl tips"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSectionResolver_MissingSectionReturnsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	r := newTestResolver(t, dir, "openai", "coordinator")
	got := r.Section("nonexistent", promptData{})
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestSectionResolver_SourcePriority(t *testing.T) {
	t.Parallel()
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	writeSection(t, dir1, "identity.md", "project identity")
	writeSection(t, dir2, "identity.md", "global identity")

	r := &sectionResolver{
		surface: "openai",
		agent:   "",
		sources: []sectionSource{diskSource{dir: dir1}, diskSource{dir: dir2}},
	}
	got := r.Section("identity", promptData{})
	if got != "project identity" {
		t.Errorf("got %q, want %q", got, "project identity")
	}
}

func TestSectionResolver_TmplRendering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSection(t, dir, "identity.md.tmpl", "Hello {{ .Provider }}")

	r := newTestResolver(t, dir, "openai", "")
	got := r.Section("identity", promptData{Provider: "openai"})
	if got != "Hello openai" {
		t.Errorf("got %q, want %q", got, "Hello openai")
	}
}

func TestSectionResolver_TmplPriorityOverMd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSection(t, dir, "identity.md.tmpl", "Template {{ .Provider }}")
	writeSection(t, dir, "identity.md", "Static")

	r := newTestResolver(t, dir, "openai", "")
	got := r.Section("identity", promptData{Provider: "openai"})
	if got != "Template openai" {
		t.Errorf("got %q, want %q", got, "Template openai")
	}
}

func TestSectionResolver_Render(t *testing.T) {
	t.Parallel()
	// Section files.
	sectionDir := t.TempDir()
	writeSection(t, sectionDir, "identity.md", "I am evener")
	writeSection(t, sectionDir, "values.md", "Be honest")

	// Template file.
	tmplDir := t.TempDir()
	writeSection(t, tmplDir, "test.md.tmpl", "{{ section \"identity\" }}\n\n{{ section \"values\" }}")

	r := newTestResolver(t, sectionDir, "openai", "coordinator")
	got, sources, err := r.Render(tmplDir, "test", promptData{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "I am evener\n\nBe honest"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(sources) < 2 {
		t.Errorf("expected at least 2 sources, got %d: %v", len(sources), sources)
	}
}

func TestSectionResolver_RenderConditional(t *testing.T) {
	t.Parallel()
	sectionDir := t.TempDir()
	writeSection(t, sectionDir, "identity.md", "I am evener")
	writeSection(t, sectionDir, "non-interactive.md", "headless mode")

	tmplDir := t.TempDir()
	tmpl := `{{ section "identity" }}
{{ if .NonInteractive }}
{{ section "non-interactive" }}
{{ end }}`
	writeSection(t, tmplDir, "cond.md.tmpl", tmpl)

	// NonInteractive false: "headless" should NOT appear.
	r := newTestResolver(t, sectionDir, "openai", "coordinator")
	got, _, err := r.Render(tmplDir, "cond", promptData{NonInteractive: false})
	if err != nil {
		t.Fatalf("Render (false): %v", err)
	}
	if strings.Contains(got, "headless") {
		t.Errorf("NonInteractive=false: should not contain 'headless', got %q", got)
	}

	// NonInteractive true: "headless" should appear.
	r2 := newTestResolver(t, sectionDir, "openai", "coordinator")
	got2, _, err := r2.Render(tmplDir, "cond", promptData{NonInteractive: true})
	if err != nil {
		t.Fatalf("Render (true): %v", err)
	}
	if !strings.Contains(got2, "headless") {
		t.Errorf("NonInteractive=true: should contain 'headless', got %q", got2)
	}
}

func TestSectionResolver_RoleSection(t *testing.T) {
	t.Parallel()
	r := &sectionResolver{
		surface: "openai",
		agent:   "coordinator",
		sources: nil,
		agentFS: bundled.Agents(),
	}
	got := r.Section("role", promptData{
		RolePromptOverride: mustWorkflowAgent(t, "coordinator").SystemPrompt,
	})

	if !strings.Contains(got, "You are a coordinator") {
		t.Errorf("expected role to contain 'You are a coordinator', got %q", got)
	}
	if strings.Contains(got, "---") {
		t.Errorf("expected frontmatter stripped (no '---'), got %q", got)
	}
	if len(r.Sources()) == 0 {
		t.Error("expected non-empty Sources()")
	}
}

func TestSectionResolver_RoleDiskOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSection(t, dir, "role.agent-coordinator.md", "Custom coordinator role")

	r := &sectionResolver{
		surface: "openai",
		agent:   "coordinator",
		sources: []sectionSource{diskSource{dir: dir}},
		agentFS: bundled.Agents(),
	}
	got := r.Section("role", promptData{})

	if got != "Custom coordinator role" {
		t.Errorf("got %q, want %q", got, "Custom coordinator role")
	}
}

func TestSectionResolver_SourceTracking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSection(t, dir, "identity.md", "I am evener")

	r := newTestResolver(t, dir, "openai", "coordinator")
	r.Section("identity", promptData{})

	sources := r.Sources()
	if len(sources) == 0 {
		t.Fatal("expected non-empty Sources()")
	}
	// Should contain a label referencing the disk path.
	found := false
	for _, s := range sources {
		if strings.Contains(s.Label, "identity.md") {
			found = true
			if s.Size != len("I am evener") {
				t.Errorf("Size=%d, want %d", s.Size, len("I am evener"))
			}
		}
	}
	if !found {
		t.Errorf("no source label mentions identity.md; got %v", sources)
	}
}

func TestMasterTemplates_Parse(t *testing.T) {
	t.Parallel()
	funcMap := template.FuncMap{"section": func(string) string { return "" }}
	for _, name := range []string{"system", "subagent"} {
		content, err := embeddedPrompts.ReadFile("prompts/templates/" + name + ".md.tmpl")
		if err != nil {
			t.Fatalf("reading %s template: %v", name, err)
		}
		_, err = template.New(name).Funcs(funcMap).Parse(string(content))
		if err != nil {
			t.Fatalf("parsing %s template: %v", name, err)
		}
	}
}

func TestSystemTemplate_StructuralRegression(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		surface: "openai",
		agent:   "coordinator",
		agentFS: bundled.Agents(),
		sources: []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	data := promptData{
		Provider:           "openai",
		Agent:              "coordinator",
		RolePromptOverride: mustWorkflowAgent(t, "coordinator").SystemPrompt,
		WorkingDir:         "/tmp/test",
		IsGitRepo:          true,
		GitBranch:          "main",
		Platform:           "linux",
		OSVersion:          "Linux 6.1",
		Today:              "2026-03-25",
		Model:              "gpt-5.4",
		KnowledgeCutoff:    "2025-05",
		ResultToolName:     "communicate",
		ProfileTools: []toolEntry{
			{Name: "shell", Description: "Run commands"},
			{Name: "apply_patch", Description: "Edit files"},
		},
		AvailableAgents: []agentEntry{
			{
				Name:         "implementer",
				Description:  "Code implementation agent.",
				DefaultTools: "`read_file`, `apply_patch`",
				TaskList: []agentTaskEntry{
					{Title: "Do the work", Description: "Implement the solution.", ReplacedByParentTasks: true},
				},
			},
		},
	}

	_, sources, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}

	foundGitSafety := false
	for _, source := range sources {
		if source.Label == "embedded:prompts/sections/git-safety.md" {
			foundGitSafety = true
			if source.Size == 0 {
				t.Error("embedded git-safety section was tracked with no content")
			}
		}
	}
	if !foundGitSafety {
		t.Errorf("system prompt did not resolve embedded git-safety section; sources = %v", sources)
	}

	// Verify sources were tracked.
	if len(sources) < 5 {
		t.Errorf("expected at least 5 tracked sources, got %d", len(sources))
	}
}

func TestAnthropicProvider_UsesEditFile(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		surface: "anthropic",
		agent:   "coordinator",
		agentFS: bundled.Agents(),
		sources: []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	data := promptData{
		Provider:           "anthropic",
		Agent:              "coordinator",
		RolePromptOverride: mustWorkflowAgent(t, "coordinator").SystemPrompt,
		ResultToolName:     "communicate",
		ProfileTools:       toolEntriesFromDefinitions(newAnthropicProfile("claude-test").ToolDefinitions()),
	}

	result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}

	// Anthropic should not get OpenAI-specific apply_patch prompt text.
	if strings.Contains(result, "apply_patch") {
		t.Error("anthropic prompt should NOT contain apply_patch")
	}
	// Anthropic must provide edit_file as its native editing tool.
	// This ensures the provider separation is bidirectional: apply_patch absent
	// AND edit_file present in the profile that drives the rendered prompt.
	assertHasTool(t, newAnthropicProfile("claude-test"), "edit_file")
}

// TestSectionResolver_DiskOverrideBeatsEmbeddedTemplate pins the source-order
// rule: a disk override replaces the embedded section whichever extension each
// side uses. Without it, turning an embedded section into a .md.tmpl — which is
// how a section gates a tool mention — would silently disable every project or
// global .md override of that section.
func TestSectionResolver_DiskOverrideBeatsEmbeddedTemplate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSection(t, dir, "transcripts.md", "project override")

	r := &sectionResolver{
		surface: "openai",
		agent:   "explorer",
		agentFS: bundled.Agents(),
		sources: []sectionSource{
			diskSource{dir: dir},
			embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"},
		},
	}
	got := r.Section("transcripts", promptData{
		Provider: "openai", Agent: "explorer",
		CallableTools: map[string]bool{"read_transcript": true},
	})
	if got != "project override" {
		t.Fatalf("section = %q, want the disk override to win over the embedded .md.tmpl", got)
	}
}

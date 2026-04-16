package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
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

func TestSectionResolver_Render(t *testing.T) {
	// Section files.
	sectionDir := t.TempDir()
	writeSection(t, sectionDir, "identity.md", "I am serf")
	writeSection(t, sectionDir, "values.md", "Be honest")

	// Template file.
	tmplDir := t.TempDir()
	writeSection(t, tmplDir, "test.md.tmpl", "{{ section \"identity\" }}\n\n{{ section \"values\" }}")

	r := newTestResolver(t, sectionDir, "openai", "coordinator")
	got, sources, err := r.Render(tmplDir, "test", PromptData{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "I am serf\n\nBe honest"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(sources) < 2 {
		t.Errorf("expected at least 2 sources, got %d: %v", len(sources), sources)
	}
}

func TestSectionResolver_RenderConditional(t *testing.T) {
	sectionDir := t.TempDir()
	writeSection(t, sectionDir, "identity.md", "I am serf")
	writeSection(t, sectionDir, "non-interactive.md", "headless mode")

	tmplDir := t.TempDir()
	tmpl := `{{ section "identity" }}
{{ if .NonInteractive }}
{{ section "non-interactive" }}
{{ end }}`
	writeSection(t, tmplDir, "cond.md.tmpl", tmpl)

	// NonInteractive false: "headless" should NOT appear.
	r := newTestResolver(t, sectionDir, "openai", "coordinator")
	got, _, err := r.Render(tmplDir, "cond", PromptData{NonInteractive: false})
	if err != nil {
		t.Fatalf("Render (false): %v", err)
	}
	if strings.Contains(got, "headless") {
		t.Errorf("NonInteractive=false: should not contain 'headless', got %q", got)
	}

	// NonInteractive true: "headless" should appear.
	r2 := newTestResolver(t, sectionDir, "openai", "coordinator")
	got2, _, err := r2.Render(tmplDir, "cond", PromptData{NonInteractive: true})
	if err != nil {
		t.Fatalf("Render (true): %v", err)
	}
	if !strings.Contains(got2, "headless") {
		t.Errorf("NonInteractive=true: should contain 'headless', got %q", got2)
	}
}

func TestCollapseBlankLines(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"\n\n\n", "\n\n"},
		{"\n\n\n\n", "\n\n"},
		{"\n\n", "\n\n"},
		{"a\n\n\nb", "a\n\nb"},
		{"a\n\nb", "a\n\nb"},
		{"no newlines", "no newlines"},
	}
	for _, tt := range tests {
		got := collapseBlankLines(tt.in)
		if got != tt.want {
			t.Errorf("collapseBlankLines(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSectionResolver_RoleSection(t *testing.T) {
	r := &SectionResolver{
		provider: "openai",
		agent:    "coordinator",
		sources:  nil,
		agentFS:  embeddedAgents,
	}
	got := r.Section("role", PromptData{})

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
	dir := t.TempDir()
	writeSection(t, dir, "role.agent-coordinator.md", "Custom coordinator role")

	r := &SectionResolver{
		provider: "openai",
		agent:    "coordinator",
		sources:  []SectionSource{diskSource{dir: dir}},
		agentFS:  embeddedAgents,
	}
	got := r.Section("role", PromptData{})

	if got != "Custom coordinator role" {
		t.Errorf("got %q, want %q", got, "Custom coordinator role")
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

func TestMasterTemplates_Parse(t *testing.T) {
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
	resolver := &SectionResolver{
		provider: "openai",
		agent:    "coordinator",
		agentFS:  embeddedAgents,
		sources:  []SectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	data := PromptData{
		Provider:        "openai",
		Agent:           "coordinator",
		WorkingDir:      "/tmp/test",
		IsGitRepo:       true,
		GitBranch:       "main",
		Platform:        "linux",
		OSVersion:       "Linux 6.1",
		Today:           "2026-03-25",
		Model:           "gpt-5.4",
		KnowledgeCutoff: "2025-05",
		ResultToolName:  "communicate",
		ProfileTools: []ToolEntry{
			{Name: "shell", Description: "Run commands"},
			{Name: "apply_patch", Description: "Edit files"},
		},
		AvailableAgents: []AgentEntry{
			{Name: "implementer", Description: "Code implementation agent."},
		},
	}

	result, sources, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}

	// Verify key structural markers appear in order.
	markers := []string{
		"## Identity",
		"## Values",
		"## Capabilities",
		"## Tool usage",
		"## Git safety",
		"## Security",
		"## Task tracking",
		"## Submitting your work",
		"<environment>",
		"<git>",
		"## Role",
		"You are a coordinator",
	}
	lastIdx := -1
	for _, marker := range markers {
		idx := strings.Index(result, marker)
		if idx < 0 {
			t.Errorf("missing marker: %q", marker)
			continue
		}
		if idx <= lastIdx {
			t.Errorf("marker %q (pos %d) appears before or at previous marker (pos %d) — out of order", marker, idx, lastIdx)
		}
		lastIdx = idx
	}

	// Verify sources were tracked.
	if len(sources) < 5 {
		t.Errorf("expected at least 5 tracked sources, got %d", len(sources))
	}
}

func TestSubagentTemplate_StructuralRegression(t *testing.T) {
	resolver := &SectionResolver{
		provider: "openai",
		agent:    "implementer",
		agentFS:  embeddedAgents,
		sources:  []SectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	data := PromptData{
		Provider:       "openai",
		Agent:          "implementer",
		WorkingDir:     "/tmp/test",
		Model:          "gpt-5.4",
		ResultToolName: "communicate",
	}

	result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "subagent", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}

	// Subagent should have identity, values, tools, workflow, communicate, role.
	for _, marker := range []string{"## Identity", "## Values", "## Workflow", "## Submitting your work", "You implement code"} {
		if !strings.Contains(result, marker) {
			t.Errorf("subagent prompt missing: %q", marker)
		}
	}

	// Subagent should NOT have git-safety, task-tracking, skills, available-agents.
	for _, absent := range []string{"## Git safety", "## Task tracking", "<skill-catalog>", "<available_agents>"} {
		if strings.Contains(result, absent) {
			t.Errorf("subagent prompt should not contain: %q", absent)
		}
	}
}

func TestReviewerTemplate_UsesRoleNarrowing(t *testing.T) {
	resolver := &SectionResolver{
		provider: "openai",
		agent:    "reviewer",
		agentFS:  embeddedAgents,
		sources:  []SectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	data := PromptData{
		Provider:       "openai",
		Agent:          "reviewer",
		ResultToolName: "communicate",
	}

	result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "subagent", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}

	// Reviewer should get approve/reject, not the base communicate section.
	if !strings.Contains(result, "approve") {
		t.Error("reviewer prompt should contain 'approve'")
	}
	if !strings.Contains(result, "reject") {
		t.Error("reviewer prompt should contain 'reject'")
	}
	if !strings.Contains(result, "Do not call `communicate` in this role.") {
		t.Error("reviewer prompt should narrow the result channel in the role prompt")
	}
}

func TestAnthropicProvider_UsesEditFile(t *testing.T) {
	resolver := &SectionResolver{
		provider: "anthropic",
		agent:    "coordinator",
		agentFS:  embeddedAgents,
		sources:  []SectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	data := PromptData{
		Provider:       "anthropic",
		Agent:          "coordinator",
		ResultToolName: "communicate",
		ProfileTools:   toolEntriesFromDefinitions(NewAnthropicProfile("claude-test").ToolDefinitions()),
	}

	result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}

	// Anthropic should get edit_file docs, NOT apply_patch.
	if !strings.Contains(result, "edit_file") {
		t.Error("anthropic prompt should contain edit_file")
	}
	if strings.Contains(result, "apply_patch") {
		t.Error("anthropic prompt should NOT contain apply_patch")
	}
}

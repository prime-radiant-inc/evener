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
		provider: provider,
		agent:    agent,
		sources:  []sectionSource{diskSource{dir: dir}},
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
		provider: "openai",
		agent:    "",
		sources:  []sectionSource{diskSource{dir: dir1}, diskSource{dir: dir2}},
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

func TestCollapseBlankLines(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	r := &sectionResolver{
		provider: "openai",
		agent:    "coordinator",
		sources:  nil,
		agentFS:  bundled.Agents(),
	}
	got := r.Section("role", promptData{
		RolePromptOverride: mustWorkflowAgent(t, "coordinator").SystemPrompt,
	})

	if strings.TrimSpace(got) == "" {
		t.Errorf("expected role override to render a non-empty body")
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
		provider: "openai",
		agent:    "coordinator",
		sources:  []sectionSource{diskSource{dir: dir}},
		agentFS:  bundled.Agents(),
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
		provider: "openai",
		agent:    "coordinator",
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
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
		CallableTools: map[string]bool{
			"read_transcript":          true,
			"find_session_transcripts": true,
			"job_watch":                true,
		},
		WorkspaceTree:        "workspace/",
		BuildInfo:            "make test",
		ProjectDocs:          []ProjectDoc{{Path: "AGENTS.md", Content: "PROMPT_PROJECT_DOC"}},
		ActivatedSkillBodies: []string{"PROMPT_ACTIVATED_SKILL"},
		Skills: []skillEntry{{
			Name:        "PROMPT_SKILL",
			CatalogName: "PROMPT_SKILL",
			Description: "configured skill",
			Dir:         "/tmp/prompt-skill",
		}},
		HasUseSkill:             true,
		UserInstructionOverride: "PROMPT_USER_OVERRIDE",
		CLIAppends:              []string{"PROMPT_CLI_APPEND"},
		ProfileTools: []toolEntry{
			{Name: "shell", Description: "Run commands"},
			{Name: "apply_patch", Description: "Edit files"},
		},
		AvailableAgents: []agentEntry{
			{
				Name:         "PROMPT_AGENT",
				Description:  "Code implementation agent.",
				DefaultTools: "`read_file`, `apply_patch`",
				TaskList: []agentTaskEntry{
					{Title: "Do the work", Description: "Implement the solution.", ReplacedByParentTasks: true},
				},
			},
		},
	}

	result, sources, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}

	if strings.TrimSpace(result) == "" {
		t.Fatal("rendered system prompt is empty")
	}

	// Verify template composition through the resolver's source ledger rather
	// than pinning the prose used inside each section.
	sourceIndex := func(marker string) int {
		for i, source := range sources {
			if source.Label == marker || strings.HasSuffix(source.Label, marker) {
				return i
			}
		}
		return -1
	}
	sourceOrder := []string{
		"identity.md",
		"capabilities.md",
		"workflow.md.tmpl",
		"verification.md",
		"communicate.md.tmpl",
		"delegation.md",
		"background-jobs.md",
		"transcripts.md.tmpl",
		"git-safety.md",
		"security.md",
		"task-tracking.md",
		"context-management.md.tmpl",
		"config:role_prompt_override",
		"tools.md.tmpl",
		"tools.provider-openai_append.md.tmpl",
		"environment.md.tmpl",
		"git.md.tmpl",
		"workspace.md.tmpl",
		"prompts/sections/project-docs.md.tmpl",
		"prompts/sections/activated-skills.md.tmpl",
		"prompts/sections/skills.md.tmpl",
		"prompts/sections/available-agents.md.tmpl",
	}
	lastIdx := -1
	for _, marker := range sourceOrder {
		idx := sourceIndex(marker)
		if idx < 0 {
			t.Errorf("missing prompt source: %q", marker)
			continue
		}
		if idx <= lastIdx {
			t.Errorf("prompt source %q (index %d) appears before or at previous source (index %d)", marker, idx, lastIdx)
		}
		lastIdx = idx
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

	payloadOrder := []string{
		"PROMPT_PROJECT_DOC",
		"PROMPT_ACTIVATED_SKILL",
		"PROMPT_SKILL",
		"PROMPT_AGENT",
		"PROMPT_USER_OVERRIDE",
		"PROMPT_CLI_APPEND",
	}
	lastPayload := -1
	for _, marker := range payloadOrder {
		idx := strings.Index(result, marker)
		if idx < 0 {
			t.Errorf("rendered prompt missing dynamic payload %q", marker)
			continue
		}
		if idx <= lastPayload {
			t.Errorf("dynamic payload %q appears before the previous payload", marker)
		}
		lastPayload = idx
	}

	// Verify sources were tracked.
	if len(sources) < 5 {
		t.Errorf("expected at least 5 tracked sources, got %d", len(sources))
	}
}

// TestGitSection_SingleSourceAndLabeled verifies git state lives only in the
// dedicated git section (never duplicated in the environment section), is labeled
// as a session-start snapshot that may be stale, and reports when the working
// directory is not a repository. The system prompt is cached, so an unlabeled
// snapshot would be read as live.
// TestEnvironmentSection_SandboxLine: a sandboxed session's environment section
// carries a one-line sandbox notice; an unsandboxed session omits it entirely.
func TestEnvironmentSection_SandboxLine(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		provider: "openai",
		agent:    "coordinator",
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	sandboxed := promptData{Provider: "openai", Agent: "coordinator", WorkingDir: "/tmp/test", Sandbox: "restricted (network off) — fixed for this session"}
	env := resolver.Section("environment", sandboxed)
	if !strings.Contains(env, "Sandbox: restricted (network off) — fixed for this session") {
		t.Errorf("sandboxed environment section must carry the sandbox line, got:\n%s", env)
	}

	off := promptData{Provider: "openai", Agent: "coordinator", WorkingDir: "/tmp/test"}
	if env := resolver.Section("environment", off); strings.Contains(env, "Sandbox:") {
		t.Errorf("unsandboxed environment section must omit the sandbox line, got:\n%s", env)
	}
}

func TestGitSection_SingleSourceAndLabeled(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		provider: "openai",
		agent:    "coordinator",
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	inRepo := promptData{Provider: "openai", Agent: "coordinator", WorkingDir: "/tmp/test", IsGitRepo: true, GitBranch: "main"}
	git := resolver.Section("git", inRepo)
	if !strings.Contains(git, "<git>") || !strings.Contains(git, "Branch: main") {
		t.Errorf("git section missing repository snapshot data, got:\n%s", git)
	}

	notRepo := promptData{Provider: "openai", Agent: "coordinator", WorkingDir: "/tmp/test", IsGitRepo: false}
	if git := resolver.Section("git", notRepo); !strings.Contains(git, "<git>") || strings.Contains(git, "Branch:") {
		t.Errorf("git section should expose the non-repository state without branch data, got:\n%s", git)
	}

	// Git state must not be duplicated in the environment section.
	env := resolver.Section("environment", inRepo)
	for _, absent := range []string{"Git branch", "Is git repository"} {
		if strings.Contains(env, absent) {
			t.Errorf("environment section should not carry git state (%q), got:\n%s", absent, env)
		}
	}
}

// TestSubagentTemplate_IncludesGitSection verifies subagents receive the git
// section — it is their only source of git state, so it must be present and
// labeled rather than confined to the root system prompt.
func TestSubagentTemplate_IncludesGitSection(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		provider: "openai",
		agent:    "implementer",
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}
	data := promptData{
		Provider:           "openai",
		Agent:              "implementer",
		RolePromptOverride: mustWorkflowAgent(t, "implementer").SystemPrompt,
		WorkingDir:         "/tmp/test",
		Model:              "gpt-5.4",
		ResultToolName:     "communicate",
		IsGitRepo:          true,
		GitBranch:          "main",
	}
	result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "subagent", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if !strings.Contains(result, "<git>") || !strings.Contains(result, "Branch: main") {
		t.Errorf("subagent prompt missing Git snapshot data:\n%s", result)
	}
}

func TestSubagentTemplate_StructuralRegression(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		provider: "openai",
		agent:    "implementer",
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	data := promptData{
		Provider:           "openai",
		Agent:              "implementer",
		RolePromptOverride: mustWorkflowAgent(t, "implementer").SystemPrompt,
		WorkingDir:         "/tmp/test",
		Model:              "gpt-5.4",
		ResultToolName:     "communicate",
	}

	result, sources, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "subagent", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}

	if strings.TrimSpace(result) == "" {
		t.Fatal("subagent prompt is empty")
	}

	// Verify the selected layers through the resolver ledger. This keeps the
	// test independent of editorial wording inside those sections.
	sourceIndex := func(marker string) int {
		for i, source := range sources {
			if source.Label == marker || strings.HasSuffix(source.Label, marker) {
				return i
			}
		}
		return -1
	}
	sourceOrder := []string{
		"identity.md",
		"capabilities.md",
		"workflow.md.tmpl",
		"verification.md",
		"communicate.md.tmpl",
		"context-management.md.tmpl",
		"config:role_prompt_override",
		"tools.md.tmpl",
		"environment.md.tmpl",
		"git.md.tmpl",
	}
	lastIdx := -1
	for _, marker := range sourceOrder {
		idx := sourceIndex(marker)
		if idx < 0 {
			t.Errorf("missing subagent prompt source: %q", marker)
			continue
		}
		if idx <= lastIdx {
			t.Errorf("subagent prompt source %q (index %d) is out of order after %d", marker, idx, lastIdx)
		}
		lastIdx = idx
	}

	// A leaf subagent has no root-only source layers.
	for _, absent := range []string{"delegation.md", "background-jobs.md", "git-safety.md", "task-tracking.md", "<skill-catalog>", "<available_agents>"} {
		if sourceIndex(absent) >= 0 || strings.Contains(result, absent) {
			t.Errorf("leaf subagent prompt should not include root-only layer %q", absent)
		}
	}
}

// TestTranscriptsSection_TeachesToolsNotRawRead verifies that the transcripts
// guidance section instructs agents to use the transcript tools and does not
// tell them to read raw transcript files with read_file.
func TestTranscriptsSection_TeachesToolsNotRawRead(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		provider: "openai",
		agent:    "coordinator",
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}
	data := promptData{
		Provider: "openai", Agent: "coordinator",
		CallableTools: map[string]bool{"read_transcript": true, "find_session_transcripts": true},
	}
	section := resolver.Section("transcripts", data)

	// Must name both tools.
	for _, want := range []string{"read_transcript", "find_session_transcripts"} {
		if !strings.Contains(section, want) {
			t.Errorf("transcripts section missing tool name %q", want)
		}
	}

	// The transcript section routes access through the transcript API rather than
	// advertising the ordinary file reader for transcript contents.
	if strings.Contains(section, "read_file") {
		t.Errorf("transcripts section advertises read_file for transcript access: %s", section)
	}
}

// TestTranscriptsSection_SilentWithoutTheTools is the other half of the rule
// ruled 2026-08-06: the section is eight lines of instructions for two tools, so
// a session that has neither — ten of the eleven shipped typed agents — gets
// none of it rather than a page it cannot act on.
func TestTranscriptsSection_SilentWithoutTheTools(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		provider: "openai",
		agent:    "explorer",
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	none := resolver.Section("transcripts", promptData{Provider: "openai", Agent: "explorer"})
	if strings.TrimSpace(none) != "" {
		t.Fatalf("transcripts section = %q, want nothing for a session with no transcript tool", none)
	}

	readOnly := resolver.Section("transcripts", promptData{
		Provider: "openai", Agent: "explorer",
		CallableTools: map[string]bool{"read_transcript": true},
	})
	if !strings.Contains(readOnly, "read_transcript") {
		t.Fatalf("read-only section = %q, want the read_transcript guidance", readOnly)
	}
	if strings.Contains(readOnly, "find_session_transcripts") {
		t.Fatalf("read-only section names a tool this session cannot call: %q", readOnly)
	}
}

// TestWorkflowSection_GatesItsToolMentions covers the other base section that
// named tools unconditionally: the job-output pointer (read_transcript) and the
// readiness-condition advice (job_watch).
func TestWorkflowSection_GatesItsToolMentions(t *testing.T) {
	t.Parallel()
	resolver := func() *sectionResolver {
		return &sectionResolver{
			provider: "openai",
			agent:    "explorer",
			agentFS:  bundled.Agents(),
			sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
		}
	}

	with := resolver().Section("workflow", promptData{
		Provider: "openai", Agent: "explorer",
		CallableTools: map[string]bool{"read_transcript": true, "job_watch": true},
	})
	for _, want := range []string{"read_transcript", "job_watch"} {
		if !strings.Contains(with, want) {
			t.Errorf("workflow section = %q, want it to name %q when the session has it", with, want)
		}
	}

	without := resolver().Section("workflow", promptData{Provider: "openai", Agent: "explorer"})
	for _, bad := range []string{"read_transcript", "job_watch"} {
		if strings.Contains(without, bad) {
			t.Errorf("workflow section names %q, which this session cannot call: %q", bad, without)
		}
	}
	if !strings.Contains(without, "pipefail") {
		t.Errorf("tool-free workflow section lost its tool-independent guidance: %q", without)
	}
}

// TestWorkflowSectionHasOrderedSubsections checks the workflow's information
// architecture without coupling the test to editorial wording in its prose.
func TestWorkflowSectionHasOrderedSubsections(t *testing.T) {
	t.Parallel()
	section := postureSectionResolver("coordinator").Section("workflow", promptData{Provider: "openai", Agent: "coordinator"})
	last := -1
	for _, heading := range []string{
		"## How to work",
		"### Default loop",
		"### Evidence and computation",
		"### Failure boundaries",
		"### Worktrees and commands",
		"### Missing capabilities",
	} {
		at := strings.Index(section, heading)
		if at < 0 {
			t.Fatalf("workflow section missing structural heading %q", heading)
		}
		if at <= last {
			t.Fatalf("workflow heading %q is out of order", heading)
		}
		last = at
	}
}

func TestReviewerTemplate_UsesCommunicateDecisionContract(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		provider: "openai",
		agent:    "reviewer",
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}

	data := promptData{
		Provider:                    "openai",
		Agent:                       "reviewer",
		RolePromptOverride:          mustWorkflowAgent(t, "reviewer").SystemPrompt,
		ResultToolName:              "communicate",
		ProfileTools:                toolEntriesFromDefinitions(NewOpenAIProfile("gpt-5.2").ToolDefinitions()),
		CallableToolNames:           []string{"read_file", "grep", "glob", "shell", "communicate"},
		UnavailableProfileToolNames: []string{"apply_patch", "write_file", "delegate", "job_watch", "task_list", "web_fetch"},
	}

	result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "subagent", data)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}

	for _, marker := range []string{"message", "end_turn", "output.data", "output.artifacts"} {
		if !strings.Contains(result, marker) {
			t.Errorf("reviewer prompt missing communicate field %q", marker)
		}
	}
	if !strings.Contains(result, "output.decision") {
		t.Error("reviewer prompt should mention output.decision")
	}
	if !strings.Contains(result, "approve") || !strings.Contains(result, "reject") {
		t.Error("reviewer prompt should mention the allowed decision values")
	}
	if !strings.Contains(result, "Currently callable tools:") {
		t.Error("reviewer prompt should show the callable tool list for this role")
	}
	if !strings.Contains(result, "Provider tools currently unavailable here:") {
		t.Error("reviewer prompt should show the unavailable provider tools for this role")
	}
	if !strings.Contains(result, "`delegate`") {
		t.Error("reviewer prompt should identify unavailable delegated tools")
	}
}

func TestAnthropicProvider_UsesEditFile(t *testing.T) {
	t.Parallel()
	resolver := &sectionResolver{
		provider: "anthropic",
		agent:    "coordinator",
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
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
		provider: "openai",
		agent:    "explorer",
		agentFS:  bundled.Agents(),
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

// postureSectionResolver builds an embedded-only resolver for the orchestration
// posture sections, which carry no provider or agent variants.
func postureSectionResolver(agent string) *sectionResolver {
	return &sectionResolver{
		provider: "openai",
		agent:    agent,
		agentFS:  bundled.Agents(),
		sources:  []sectionSource{embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"}},
	}
}

// TestVerificationSectionHasVerificationLayers checks the verification
// architecture without coupling it to the wording of individual rules.
func TestVerificationSectionDefinesIncompleteGates(t *testing.T) {
	t.Parallel()
	section := postureSectionResolver("coordinator").Section("verification", promptData{Provider: "openai", Agent: "coordinator"})
	for _, heading := range []string{
		"## Verification",
		"### Gate status",
		"### Protecting assertions",
		"### Attributing failures",
	} {
		if !strings.Contains(section, heading) {
			t.Fatalf("verification section missing structural heading %q", heading)
		}
	}
}

// TestVerificationSectionRequiresSmokeCaseBeforeMatrix pins kata nbcf's
// harness-vs-product rule: before a cross-model or cross-configuration
// comparison is read as a product/model-behavior finding, one known-good
// smoke case must be proven on each participant first.
func TestVerificationSectionRequiresSmokeCaseBeforeMatrix(t *testing.T) {
	t.Parallel()
	section := postureSectionResolver("coordinator").Section("verification", promptData{Provider: "openai", Agent: "coordinator"})
	if !strings.Contains(section, "### Attributing failures") || !strings.Contains(section, "#### Smoke before comparison") {
		t.Fatalf("verification section missing failure-attribution layers: %s", section)
	}
}

// TestContextManagementSectionIsAdvisoryAndBounded pins the context-management
// posture: compaction is a suggestion at a task boundary, and a stalled
// implement/review/fix loop stops after two cycles instead of repeating.
func TestContextManagementSectionIsAdvisoryAndBounded(t *testing.T) {
	t.Parallel()
	section := postureSectionResolver("coordinator").Section("context-management", promptData{
		Provider: "openai", Agent: "coordinator",
		CallableTools: map[string]bool{"compact_context": true},
	})
	for _, marker := range []string{
		"## Context management", "compact_context", "implement/review/fix", "reslice",
	} {
		if !strings.Contains(section, marker) {
			t.Fatalf("context-management section missing structural marker %q", marker)
		}
	}
}

// TestContextManagementSection_SilentAboutAnUncallableCompactTool applies the
// tool-mention rule (ruled 2026-08-06) to this section: a session without
// compact_context keeps the stop rule and loses only the tool suggestion.
func TestContextManagementSection_SilentAboutAnUncallableCompactTool(t *testing.T) {
	t.Parallel()
	section := postureSectionResolver("explorer").Section("context-management", promptData{Provider: "openai", Agent: "explorer"})
	if strings.Contains(section, "compact_context") {
		t.Fatalf("context-management section names a tool this session cannot call: %s", section)
	}
	if strings.TrimSpace(section) == "" {
		t.Fatal("tool-free context-management section is empty")
	}
}

// TestCommunicateSectionReportsPhaseChangeCheckpoint pins kata nbcf's
// checkpoint-reporting rule: a long or delegated task that changes phase
// (e.g. investigation to implementation) sends a non-terminal checkpoint
// naming completed work, the current hypothesis/plan, the next concrete
// action, and blockers, and it lands after the "Report real milestones"
// paragraph rather than replacing it.
func TestCommunicateSectionReportsPhaseChangeCheckpoint(t *testing.T) {
	t.Parallel()
	section := postureSectionResolver("coordinator").Section("communicate", promptData{
		Provider: "openai", Agent: "coordinator", ResultToolName: "communicate",
	})
	for _, marker := range []string{
		"## Communication",
		"### Message shape",
		"output.message",
		"output.data",
		"output.artifacts",
		"### Phase changes",
		"end_turn=false",
		"end_turn=true",
	} {
		if !strings.Contains(section, marker) {
			t.Fatalf("communicate section missing structural marker %q", marker)
		}
	}

	messageShape := strings.Index(section, "### Message shape")
	phaseChanges := strings.Index(section, "### Phase changes")
	if messageShape < 0 || phaseChanges < 0 || messageShape >= phaseChanges {
		t.Fatalf("communication layers out of order: message shape=%d phase changes=%d", messageShape, phaseChanges)
	}
}

// TestOrchestrationPostureTemplateInclusion checks where the posture lands: both
// master templates carry verification and context management, and a leaf child
// gets them without the delegation guidance it cannot act on.
func TestOrchestrationPostureTemplateInclusion(t *testing.T) {
	t.Parallel()
	data := func(canDelegate bool) promptData {
		return promptData{
			Provider:       "openai",
			Agent:          "coordinator",
			WorkingDir:     "/tmp/test",
			Model:          "gpt-5.4",
			ResultToolName: "communicate",
			CallableTools:  map[string]bool{"compact_context": true},
			CanDelegate:    canDelegate,
		}
	}
	posture := []string{"## Verification", "## Context management"}

	root, _, err := postureSectionResolver("coordinator").RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", data(true))
	if err != nil {
		t.Fatalf("render system: %v", err)
	}
	for _, marker := range append([]string{"## Delegation"}, posture...) {
		if !strings.Contains(root, marker) {
			t.Errorf("root prompt missing %q", marker)
		}
	}

	leaf, _, err := postureSectionResolver("coordinator").RenderEmbedded(embeddedPrompts, "prompts/templates/", "subagent", data(false))
	if err != nil {
		t.Fatalf("render subagent: %v", err)
	}
	for _, marker := range posture {
		if !strings.Contains(leaf, marker) {
			t.Errorf("leaf child prompt missing %q", marker)
		}
	}
	if strings.Contains(leaf, "## Delegation") {
		t.Error("leaf child prompt should not contain the delegation section")
	}

	delegating, _, err := postureSectionResolver("coordinator").RenderEmbedded(embeddedPrompts, "prompts/templates/", "subagent", data(true))
	if err != nil {
		t.Fatalf("render delegating subagent: %v", err)
	}
	for _, marker := range append([]string{"## Delegation"}, posture...) {
		if !strings.Contains(delegating, marker) {
			t.Errorf("delegating child prompt missing %q", marker)
		}
	}
}

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// s5covWriteSection writes a section file into dir.
func s5covWriteSection(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// s5covResolver builds a sectionResolver reading from a single disk directory.
func s5covResolver(dir, provider, agent string, agentFS fstest.MapFS) *sectionResolver {
	return &sectionResolver{
		provider: provider,
		agent:    agent,
		sources:  []sectionSource{diskSource{dir: dir}},
		agentFS:  agentFS,
	}
}

// Section layers provider body + agent prepend/append when no agent body exists.
func TestS5Cov_Section_ProviderAndAgentLayering(t *testing.T) {
	dir := t.TempDir()
	s5covWriteSection(t, dir, "intro.md", "BASE BODY")
	s5covWriteSection(t, dir, "intro.agent-explorer_prepend.md", "AGENT PRE")
	s5covWriteSection(t, dir, "intro.agent-explorer_append.md", "AGENT POST")

	r := s5covResolver(dir, "openai", "explorer", nil)
	out := r.Section("intro", promptData{})
	if !strings.Contains(out, "BASE BODY") || !strings.Contains(out, "AGENT PRE") || !strings.Contains(out, "AGENT POST") {
		t.Errorf("layered section missing parts:\n%s", out)
	}
	// Provider-specific body overrides the base body.
	s5covWriteSection(t, dir, "intro.provider-openai.md", "OPENAI BODY")
	out = r.Section("intro", promptData{})
	if !strings.Contains(out, "OPENAI BODY") || strings.Contains(out, "BASE BODY") {
		t.Errorf("provider-specific body should override base:\n%s", out)
	}
}

// An agent body replaces the provider result entirely.
func TestS5Cov_Section_AgentBodyReplaces(t *testing.T) {
	dir := t.TempDir()
	s5covWriteSection(t, dir, "intro.md", "BASE BODY")
	s5covWriteSection(t, dir, "intro.agent-explorer.md", "AGENT BODY")
	r := s5covResolver(dir, "openai", "explorer", nil)
	out := r.Section("intro", promptData{})
	if out != "AGENT BODY" {
		t.Errorf("agent body should replace provider result, got %q", out)
	}
}

// resolveRole: explicit override wins; else disk override; else embedded agent
// definition with frontmatter stripped.
func TestS5Cov_ResolveRole_Precedence(t *testing.T) {
	dir := t.TempDir()
	agentFS := fstest.MapFS{
		"explorer.md": &fstest.MapFile{Data: []byte("---\nname: explorer\n---\nEMBEDDED ROLE\n")},
	}
	r := s5covResolver(dir, "openai", "explorer", agentFS)

	// Explicit override.
	if got := r.Section("role", promptData{RolePromptOverride: "OVERRIDE"}); got != "OVERRIDE" {
		t.Errorf("role override should win, got %q", got)
	}
	// Embedded fallback (no disk override present).
	if got := r.Section("role", promptData{}); !strings.Contains(got, "EMBEDDED ROLE") {
		t.Errorf("embedded role expected, got %q", got)
	}
	// Disk override beats embedded.
	s5covWriteSection(t, dir, "role.agent-explorer.md", "DISK ROLE")
	if got := r.Section("role", promptData{}); got != "DISK ROLE" {
		t.Errorf("disk role override should win over embedded, got %q", got)
	}
}

// resolveRole returns empty when there is no agent.
func TestS5Cov_ResolveRole_NoAgent(t *testing.T) {
	r := s5covResolver(t.TempDir(), "openai", "", nil)
	if got := r.Section("role", promptData{}); got != "" {
		t.Errorf("no agent should yield empty role, got %q", got)
	}
}

// readAndRender: a .md.tmpl is rendered; a template execution error tracks an
// ERROR source and yields empty.
func TestS5Cov_ReadAndRender_TemplateAndError(t *testing.T) {
	dir := t.TempDir()
	s5covWriteSection(t, dir, "greet.md.tmpl", "Hi {{.Model}}")
	r := s5covResolver(dir, "openai", "", nil)
	if got := r.readAndRender("greet", promptData{Model: "gpt-x"}); got != "Hi gpt-x" {
		t.Errorf("template render = %q, want Hi gpt-x", got)
	}

	// A template referencing a missing field errors on execute.
	s5covWriteSection(t, dir, "bad.md.tmpl", "{{.DoesNotExist}}")
	r2 := s5covResolver(dir, "openai", "", nil)
	if got := r2.readAndRender("bad", promptData{}); got != "" {
		t.Errorf("bad template should render empty, got %q", got)
	}
	var sawErr bool
	for _, src := range r2.Sources() {
		if strings.HasPrefix(src.Label, "ERROR:") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("a template execution error should be tracked as ERROR:")
	}
}

// Render executes a top-level template whose {{section}} calls resolve through
// the resolver.
func TestS5Cov_Render_TopLevelTemplate(t *testing.T) {
	dir := t.TempDir()
	s5covWriteSection(t, dir, "intro.md", "INTRO BODY")
	s5covWriteSection(t, dir, "main.md.tmpl", "START\n{{section \"intro\"}}\nEND")
	r := s5covResolver(dir, "openai", "", nil)

	out, sources, err := r.Render(dir, "main", promptData{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "INTRO BODY") || !strings.Contains(out, "START") {
		t.Errorf("rendered top-level template missing parts:\n%s", out)
	}
	if len(sources) == 0 {
		t.Error("expected tracked sources from the resolved section")
	}
	// A missing template errors.
	if _, _, err := r.Render(dir, "nope", promptData{}); err == nil {
		t.Error("missing top-level template should error")
	}
}

// RenderEmbedded renders a top-level template from an embedded FS mirror. Since
// embed.FS cannot be built in a test, exercise the error path (missing file).
func TestS5Cov_RenderEmbedded_MissingErrors(t *testing.T) {
	r := s5covResolver(t.TempDir(), "openai", "", nil)
	if _, _, err := r.RenderEmbedded(embeddedPrompts, "prompts/nonexistent/", "missing", promptData{}); err == nil {
		t.Error("missing embedded template should error")
	}
}

// sourceLabel distinguishes disk, embedded, and unknown sources.
func TestS5Cov_SourceLabel(t *testing.T) {
	r := &sectionResolver{}
	if got := r.sourceLabel(diskSource{dir: "/d"}, "x.md"); got != "disk:"+filepath.Join("/d", "x.md") {
		t.Errorf("disk label = %q", got)
	}
	if got := r.sourceLabel(embedSource{prefix: "p/"}, "x.md"); got != "embedded:p/x.md" {
		t.Errorf("embed label = %q", got)
	}
	if got := r.sourceLabel(nil, "x.md"); got != "unknown:x.md" {
		t.Errorf("unknown label = %q", got)
	}
}

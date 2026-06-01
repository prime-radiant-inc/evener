# Prompt Template Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace serf's flat-concatenation prompt assembly with a template engine that transcludes named sections, resolved by provider and agent qualifiers.

**Architecture:** A `SectionResolver` scans layered file sources (project disk, global disk, embedded FS) for section files matching `{name}[.{qualifier}][_modifier].md[.tmpl]`. Master templates (`system.md.tmpl`, `subagent.md.tmpl`) call `{{ section "name" }}` to transclude resolved content. `text/template` renders `.tmpl` files with a `PromptData` struct.

**Tech Stack:** Go `text/template`, `embed.FS`, existing `frontmatter` package

**Spec:** `docs/plans/2026-03-25-prompt-template-engine.md`

---

## File Map

### New files

| File | Responsibility |
|------|---------------|
| `agent/section_resolver.go` | `SectionResolver` struct, `SectionSource` interface, `diskSource`, `embedSource`, resolution algorithm, `Render()` |
| `agent/section_resolver_test.go` | Unit tests for resolver: resolution order, provider/agent variants, prepend/append, .tmpl rendering, missing sections, role special case |
| `agent/prompt_data.go` | `PromptData` struct, `SkillEntry`, `ToolEntry`, `AgentEntry` types |
| `agent/prompt_data_test.go` | Tests for `buildPromptData()` helper |
| `agent/prompts/templates/system.md.tmpl` | Master template for top-level sessions |
| `agent/prompts/templates/subagent.md.tmpl` | Master template for subagent sessions |
| `agent/prompts/sections/identity.md` | Identity section |
| `agent/prompts/sections/values.md` | Values section |
| `agent/prompts/sections/capabilities.md` | Vision/native capabilities |
| `agent/prompts/sections/tools.md` | Shared tool guidance |
| `agent/prompts/sections/tools.provider-openai.md` | apply_patch docs |
| `agent/prompts/sections/tools.provider-openai_append.md` | rg, multi_tool_use.parallel |
| `agent/prompts/sections/tools.provider-anthropic.md` | edit_file docs |
| `agent/prompts/sections/tools.provider-anthropic_append.md` | Tool selection guide |
| `agent/prompts/sections/tools.provider-gemini.md` | edit_file docs |
| `agent/prompts/sections/tools.provider-gemini_append.md` | Tool selection guide |
| `agent/prompts/sections/workflow.md` | Workflow guidance |
| `agent/prompts/sections/git-safety.md` | Git safety rules |
| `agent/prompts/sections/security.md` | Security guidance |
| `agent/prompts/sections/task-tracking.md` | Task tracking guidance |
| `agent/prompts/sections/communicate.md.tmpl` | communicate tool (uses `{{ .ResultToolName }}`) |
| `agent/prompts/sections/communicate.agent-reviewer.md` | Reviewer approve/reject |
| `agent/prompts/sections/environment.md.tmpl` | Dynamic environment block |
| `agent/prompts/sections/git.md.tmpl` | Dynamic git status block |
| `agent/prompts/sections/workspace.md.tmpl` | Dynamic workspace block |
| `agent/prompts/sections/skills.md.tmpl` | Dynamic skills listing |
| `agent/prompts/sections/tool-list.md.tmpl` | Dynamic tool listing |
| `agent/prompts/sections/available-agents.md.tmpl` | Dynamic available agents |
| `agent/prompts/sections/project-docs.md.tmpl` | Dynamic project docs |
| `agent/prompts/sections/non-interactive.md.tmpl` | Non-interactive rules (uses `{{ .ResultToolName }}`) |
| `agent/prompts/sections/non-interactive.agent-coordinator.md` | Coordinator-specific non-interactive |

### Modified files

| File | Change |
|------|--------|
| `agent/prompt_resolver.go` | Update `//go:embed` directive to cover new directories. Keep `ResolveSystemPromptWithSources` for `--system-prompt` CLI override path. |
| `agent/profile.go` | `BuildSystemPrompt` delegates to resolver. Add `NewResolver()` method to `ProviderProfile` interface and `baseProfile`. |
| `agent/session.go` | Replace per-round prompt rebuild with cached prompt. New `renderSystemPrompt()` and `buildPromptData()`. Simplify `initSessionState()` and `rebuildPromptCache()`. |
| `agent/subagents.go` | Use resolver with `subagent` template instead of manual `core + persona` concatenation. |
| `agent/plugin_prompt.go` | Delete `FormatPluginAgentsPrompt` (moved to template). |

### Deleted files (after migration verified)

| File | Replaced by |
|------|-------------|
| `agent/prompts/system.openai.md` | Section files |
| `agent/prompts/system.anthropic.md` | Section files |
| `agent/prompts/system.gemini.md` | Section files |
| `agent/prompts/core.md` | Section files |

---

## Task 1: SectionSource Interface and Implementations

**Files:**
- Create: `agent/section_resolver.go`
- Test: `agent/section_resolver_test.go`

- [ ] **Step 1: Write failing tests for diskSource**

```go
// agent/section_resolver_test.go
package agent

import (
    "os"
    "path/filepath"
    "testing"
)

func TestDiskSource_ReadFile(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "identity.md"), []byte("I am serf"), 0o644)

    src := diskSource{dir: dir}

    data, ok := src.ReadFile("identity.md")
    if !ok {
        t.Fatal("expected identity.md to exist")
    }
    if string(data) != "I am serf" {
        t.Errorf("got %q, want %q", string(data), "I am serf")
    }

    _, ok = src.ReadFile("missing.md")
    if ok {
        t.Fatal("expected missing.md to not exist")
    }
}

func TestEmbedSource_ReadFile(t *testing.T) {
    // Uses the real embeddedPrompts FS from prompt_resolver.go.
    // After migration, sections/ will exist. For now, test with
    // the existing prompts/ directory.
    src := embedSource{fs: embeddedPrompts, prefix: "prompts/"}

    data, ok := src.ReadFile("core.md")
    if !ok {
        t.Fatal("expected core.md to exist in embedded FS")
    }
    if len(data) == 0 {
        t.Fatal("expected non-empty content")
    }

    _, ok = src.ReadFile("nonexistent.md")
    if ok {
        t.Fatal("expected nonexistent.md to not exist")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run TestDiskSource_ReadFile -v`
Expected: FAIL — `diskSource` undefined

- [ ] **Step 3: Implement SectionSource, diskSource, embedSource**

```go
// agent/section_resolver.go
package agent

import (
    "embed"
    "os"
    "path/filepath"
)

// SectionSource provides read access to a directory of section files.
type SectionSource interface {
    ReadFile(name string) ([]byte, bool)
}

// diskSource reads section files from a filesystem directory.
type diskSource struct {
    dir string
}

func (d diskSource) ReadFile(name string) ([]byte, bool) {
    if d.dir == "" {
        return nil, false
    }
    data, err := os.ReadFile(filepath.Join(d.dir, name))
    if err != nil {
        return nil, false
    }
    return data, true
}

// embedSource reads section files from an embedded filesystem.
type embedSource struct {
    fs     embed.FS
    prefix string // e.g. "prompts/sections/"
}

func (e embedSource) ReadFile(name string) ([]byte, bool) {
    data, err := e.fs.ReadFile(e.prefix + name)
    if err != nil {
        return nil, false
    }
    return data, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/ -run "TestDiskSource|TestEmbedSource" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/section_resolver.go agent/section_resolver_test.go
git commit -m "feat: add SectionSource interface with disk and embed implementations"
```

---

## Task 2: PromptData Struct

**Files:**
- Create: `agent/prompt_data.go`

- [ ] **Step 1: Write the PromptData type and supporting types**

```go
// agent/prompt_data.go
package agent

// PromptData is the template context for system prompt rendering.
// Assembled from session state; not a source of truth.
type PromptData struct {
    // Resolution context
    NonInteractive bool
    Provider       string // "openai", "anthropic", "gemini"
    Agent          string // "coordinator", "implementer", "reviewer", etc.

    // Environment
    WorkingDir      string
    IsGitRepo       bool
    GitBranch       string
    Platform        string
    OSVersion       string
    Today           string
    Model           string // from profile, not EnvironmentInfo
    KnowledgeCutoff string

    // Git
    GitModifiedFiles      int
    GitUntrackedFiles     int
    GitRecentCommitTitles []string

    // Workspace
    WorkspaceTree string
    TestFiles     []string
    BuildInfo     string
    WorkingDirFull string // absolute path for test file paths

    // Skills
    Skills      []SkillEntry
    HasUseSkill bool

    // Tools (three tiers)
    ProfileTools []ToolEntry
    MCPTools     []ToolEntry
    CustomTools  []ToolEntry

    // Available agents (for coordinator spawn_agent)
    AvailableAgents []AgentEntry

    // Project docs
    ProjectDocs []ProjectDoc

    // Result tool
    ResultToolName string

    // User instruction override (highest priority, appended last)
    UserInstructionOverride string

    // CLI appends (--system-prompt-append, applied after everything)
    CLIAppends []string
}

// SkillEntry is a skill for template rendering.
type SkillEntry struct {
    Name        string
    Description string
    Dir         string // directory path (for use_skill profiles)
    SkillFile   string // SKILL.md path (for read_file profiles)
}

// ToolEntry is a tool for template rendering.
type ToolEntry struct {
    Name        string
    Description string
}

// AgentEntry is a spawnable agent for template rendering.
type AgentEntry struct {
    Name        string
    Description string
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./agent/...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add agent/prompt_data.go
git commit -m "feat: add PromptData struct for template rendering context"
```

---

## Task 3: Section Resolution Algorithm

**Files:**
- Modify: `agent/section_resolver.go`
- Test: `agent/section_resolver_test.go`

- [ ] **Step 1: Write failing tests for section resolution**

```go
func TestSectionResolver_BaseOnly(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "identity.md"), []byte("I am serf"), 0o644)

    r := &SectionResolver{
        provider: "openai",
        agent:    "coordinator",
        sources:  []SectionSource{diskSource{dir: dir}},
    }

    got := r.Section("identity", PromptData{})
    if got != "I am serf" {
        t.Errorf("got %q, want %q", got, "I am serf")
    }
}

func TestSectionResolver_ProviderOverride(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "tools.md"), []byte("base tools"), 0o644)
    os.WriteFile(filepath.Join(dir, "tools.provider-openai.md"), []byte("openai tools"), 0o644)

    r := &SectionResolver{
        provider: "openai",
        agent:    "coordinator",
        sources:  []SectionSource{diskSource{dir: dir}},
    }

    got := r.Section("tools", PromptData{})
    if got != "openai tools" {
        t.Errorf("got %q, want %q", got, "openai tools")
    }
}

func TestSectionResolver_ProviderFallsBackToBase(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "tools.md"), []byte("base tools"), 0o644)

    r := &SectionResolver{
        provider: "anthropic",
        agent:    "coordinator",
        sources:  []SectionSource{diskSource{dir: dir}},
    }

    got := r.Section("tools", PromptData{})
    if got != "base tools" {
        t.Errorf("got %q, want %q", got, "base tools")
    }
}

func TestSectionResolver_PrependAppend(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "tools.md"), []byte("base"), 0o644)
    os.WriteFile(filepath.Join(dir, "tools.provider-openai_prepend.md"), []byte("before"), 0o644)
    os.WriteFile(filepath.Join(dir, "tools.provider-openai_append.md"), []byte("after"), 0o644)

    r := &SectionResolver{
        provider: "openai",
        agent:    "coordinator",
        sources:  []SectionSource{diskSource{dir: dir}},
    }

    got := r.Section("tools", PromptData{})
    want := "before\n\nbase\n\nafter"
    if got != want {
        t.Errorf("got %q, want %q", got, want)
    }
}

func TestSectionResolver_AgentBodyReplaces(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "communicate.md"), []byte("call communicate"), 0o644)
    os.WriteFile(filepath.Join(dir, "communicate.agent-reviewer.md"), []byte("call approve or reject"), 0o644)

    r := &SectionResolver{
        provider: "openai",
        agent:    "reviewer",
        sources:  []SectionSource{diskSource{dir: dir}},
    }

    got := r.Section("communicate", PromptData{})
    if got != "call approve or reject" {
        t.Errorf("got %q, want %q", got, "call approve or reject")
    }
}

func TestSectionResolver_AgentAppendIsAdditive(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "tools.md"), []byte("base tools"), 0o644)
    os.WriteFile(filepath.Join(dir, "tools.agent-implementer_append.md"), []byte("impl tips"), 0o644)

    r := &SectionResolver{
        provider: "openai",
        agent:    "implementer",
        sources:  []SectionSource{diskSource{dir: dir}},
    }

    got := r.Section("tools", PromptData{})
    want := "base tools\n\nimpl tips"
    if got != want {
        t.Errorf("got %q, want %q", got, want)
    }
}

func TestSectionResolver_MissingSectionReturnsEmpty(t *testing.T) {
    dir := t.TempDir()

    r := &SectionResolver{
        provider: "openai",
        agent:    "coordinator",
        sources:  []SectionSource{diskSource{dir: dir}},
    }

    got := r.Section("nonexistent", PromptData{})
    if got != "" {
        t.Errorf("got %q, want empty string", got)
    }
}

func TestSectionResolver_SourcePriority(t *testing.T) {
    // Project dir overrides embedded.
    projDir := t.TempDir()
    globalDir := t.TempDir()
    os.WriteFile(filepath.Join(projDir, "identity.md"), []byte("project identity"), 0o644)
    os.WriteFile(filepath.Join(globalDir, "identity.md"), []byte("global identity"), 0o644)

    r := &SectionResolver{
        provider: "openai",
        agent:    "coordinator",
        sources:  []SectionSource{
            diskSource{dir: projDir},
            diskSource{dir: globalDir},
        },
    }

    got := r.Section("identity", PromptData{})
    if got != "project identity" {
        t.Errorf("got %q, want %q", got, "project identity")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run TestSectionResolver -v`
Expected: FAIL — `SectionResolver` has no `Section` method

- [ ] **Step 3: Implement SectionResolver.Section**

Add to `agent/section_resolver.go`:

```go
import (
    "strings"
    "text/template"
)

// SectionResolver resolves named sections from layered file sources.
type SectionResolver struct {
    provider string
    agent    string
    sources  []SectionSource
    tracked  []PromptSource // files that contributed to the output
}

// Section resolves a named section to its rendered content.
// Returns empty string if no files match. Never errors.
func (r *SectionResolver) Section(name string, data PromptData) string {
    // Provider layer
    providerPrepend := r.readAndRender(name+".provider-"+r.provider+"_prepend", data)
    providerBody := r.readAndRender(name+".provider-"+r.provider, data)
    if providerBody == "" {
        providerBody = r.readAndRender(name, data)
    }
    providerAppend := r.readAndRender(name+".provider-"+r.provider+"_append", data)

    // Agent layer
    agentPrepend := r.readAndRender(name+".agent-"+r.agent+"_prepend", data)
    agentBody := r.readAndRender(name+".agent-"+r.agent, data)
    agentAppend := r.readAndRender(name+".agent-"+r.agent+"_append", data)

    // If agent body exists, it replaces the provider result.
    var parts []string
    if agentBody != "" {
        // Agent replaces: agent-prepend + agent-body + agent-append
        for _, s := range []string{agentPrepend, agentBody, agentAppend} {
            if s != "" {
                parts = append(parts, s)
            }
        }
    } else {
        // No agent override: provider-prepend + provider-body + provider-append + agent-prepend + agent-append
        for _, s := range []string{providerPrepend, providerBody, providerAppend, agentPrepend, agentAppend} {
            if s != "" {
                parts = append(parts, s)
            }
        }
    }

    return strings.Join(parts, "\n\n")
}

// readAndRender looks up a file stem across sources, renders .tmpl files.
// Tries {stem}.md.tmpl first, then {stem}.md. Returns "" if not found.
func (r *SectionResolver) readAndRender(stem string, data PromptData) string {
    // Try .md.tmpl first
    if content, label := r.readFirst(stem + ".md.tmpl"); content != nil {
        rendered, err := renderTemplate(stem, string(content), data)
        if err != nil {
            // Log but don't crash — surface via tracked sources.
            r.tracked = append(r.tracked, PromptSource{Label: "ERROR:" + label, Size: 0})
            return ""
        }
        r.tracked = append(r.tracked, PromptSource{Label: label, Size: len(rendered)})
        return rendered
    }
    // Then plain .md
    if content, label := r.readFirst(stem + ".md"); content != nil {
        s := strings.TrimRight(string(content), "\n")
        r.tracked = append(r.tracked, PromptSource{Label: label, Size: len(s)})
        return s
    }
    return ""
}

// readFirst checks each source in priority order, returns first match.
func (r *SectionResolver) readFirst(name string) ([]byte, string) {
    for _, src := range r.sources {
        if data, ok := src.ReadFile(name); ok {
            return data, sourceLabel(src, name)
        }
    }
    return nil, ""
}

func sourceLabel(src SectionSource, name string) string {
    switch s := src.(type) {
    case diskSource:
        return "disk:" + filepath.Join(s.dir, name)
    case embedSource:
        return "embedded:" + s.prefix + name
    default:
        return "unknown:" + name
    }
}

func renderTemplate(name, content string, data PromptData) (string, error) {
    tmpl, err := template.New(name).Parse(content)
    if err != nil {
        return "", err
    }
    var buf strings.Builder
    if err := tmpl.Execute(&buf, data); err != nil {
        return "", err
    }
    return strings.TrimRight(buf.String(), "\n"), nil
}

// Sources returns the tracked prompt sources for debugging.
func (r *SectionResolver) Sources() []PromptSource {
    return r.tracked
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/ -run TestSectionResolver -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/section_resolver.go agent/section_resolver_test.go
git commit -m "feat: implement section resolution algorithm with provider/agent layering"
```

---

## Task 4: Template Rendering with section FuncMap

**Files:**
- Modify: `agent/section_resolver.go`
- Test: `agent/section_resolver_test.go`

- [ ] **Step 1: Write failing test for Render**

```go
func TestSectionResolver_Render(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "identity.md"), []byte("I am serf"), 0o644)
    os.WriteFile(filepath.Join(dir, "values.md"), []byte("Be honest"), 0o644)

    // Create a minimal template
    tmplDir := t.TempDir()
    os.WriteFile(filepath.Join(tmplDir, "test.md.tmpl"),
        []byte("{{ section \"identity\" }}\n\n{{ section \"values\" }}"), 0o644)

    r := &SectionResolver{
        provider: "openai",
        agent:    "coordinator",
        sources:  []SectionSource{diskSource{dir: dir}},
    }

    data := PromptData{Provider: "openai", Agent: "coordinator"}
    result, sources, err := r.Render(tmplDir, "test", data)
    if err != nil {
        t.Fatalf("Render error: %v", err)
    }
    want := "I am serf\n\nBe honest"
    if result != want {
        t.Errorf("got %q, want %q", result, want)
    }
    if len(sources) < 2 {
        t.Errorf("expected at least 2 sources, got %d", len(sources))
    }
}

func TestSectionResolver_RenderConditional(t *testing.T) {
    dir := t.TempDir()
    os.WriteFile(filepath.Join(dir, "identity.md"), []byte("I am serf"), 0o644)
    os.WriteFile(filepath.Join(dir, "non-interactive.md"), []byte("headless mode"), 0o644)

    tmplDir := t.TempDir()
    tmpl := `{{ section "identity" }}
{{ if .NonInteractive }}

{{ section "non-interactive" }}
{{ end }}`
    os.WriteFile(filepath.Join(tmplDir, "test.md.tmpl"), []byte(tmpl), 0o644)

    r := &SectionResolver{
        provider: "openai",
        agent:    "coordinator",
        sources:  []SectionSource{diskSource{dir: dir}},
    }

    // NonInteractive = false → section omitted
    result, _, _ := r.Render(tmplDir, "test", PromptData{})
    if strings.Contains(result, "headless") {
        t.Error("non-interactive section should not appear when NonInteractive=false")
    }

    // NonInteractive = true → section included
    r2 := &SectionResolver{
        provider: "openai",
        agent:    "coordinator",
        sources:  []SectionSource{diskSource{dir: dir}},
    }
    result2, _, _ := r2.Render(tmplDir, "test", PromptData{NonInteractive: true})
    if !strings.Contains(result2, "headless") {
        t.Error("non-interactive section should appear when NonInteractive=true")
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./agent/ -run "TestSectionResolver_Render" -v`
Expected: FAIL — `Render` method undefined

- [ ] **Step 3: Implement Render**

Add to `agent/section_resolver.go`:

```go
// Render parses a master template from tmplDir/{name}.md.tmpl and executes it
// with a FuncMap containing `section` as a closure over the resolver.
// Returns the rendered prompt, tracked sources, and any error.
func (r *SectionResolver) Render(tmplDir string, name string, data PromptData) (string, []PromptSource, error) {
    r.tracked = nil // reset source tracking

    tmplPath := filepath.Join(tmplDir, name+".md.tmpl")
    tmplContent, err := os.ReadFile(tmplPath)
    if err != nil {
        return "", nil, fmt.Errorf("reading template %s: %w", tmplPath, err)
    }

    funcMap := template.FuncMap{
        "section": func(sectionName string) string {
            return r.Section(sectionName, data)
        },
    }

    tmpl, err := template.New(name).Funcs(funcMap).Parse(string(tmplContent))
    if err != nil {
        return "", nil, fmt.Errorf("parsing template %s: %w", name, err)
    }

    var buf strings.Builder
    if err := tmpl.Execute(&buf, data); err != nil {
        return "", nil, fmt.Errorf("executing template %s: %w", name, err)
    }

    // Clean up excessive blank lines from optional sections.
    result := collapseBlankLines(buf.String())
    return strings.TrimSpace(result), r.tracked, nil
}

// collapseBlankLines reduces runs of 3+ newlines to 2 (one blank line).
func collapseBlankLines(s string) string {
    for strings.Contains(s, "\n\n\n") {
        s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
    }
    return s
}
```

Also add `"fmt"` to imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/ -run "TestSectionResolver_Render" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/section_resolver.go agent/section_resolver_test.go
git commit -m "feat: add Render method with section FuncMap closure"
```

---

## Task 5: Role Section Special Case

**Files:**
- Modify: `agent/section_resolver.go`
- Test: `agent/section_resolver_test.go`

The `role` section reads from `agents/{agent}.md` and strips YAML frontmatter.

- [ ] **Step 1: Write failing test**

```go
func TestSectionResolver_RoleSection(t *testing.T) {
    // The "role" section should read from the builtinAgents embed FS,
    // strip frontmatter, and return the body.
    r := &SectionResolver{
        provider: "openai",
        agent:    "coordinator",
        sources:  []SectionSource{},
        agentFS:  embeddedAgents, // new field
    }

    got := r.Section("role", PromptData{})
    if !strings.Contains(got, "You are a dispatcher") {
        t.Errorf("expected coordinator role text, got %q", got[:min(len(got), 100)])
    }
    // Should NOT contain frontmatter
    if strings.Contains(got, "---") {
        t.Error("role section should not contain frontmatter delimiters")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestSectionResolver_RoleSection -v`
Expected: FAIL — `agentFS` field doesn't exist

- [ ] **Step 3: Add agentFS field and role handling to Section**

Add `agentFS embed.FS` field to `SectionResolver`. In `Section()`, before returning, check if `name == "role"`:

```go
// In SectionResolver struct:
    agentFS embed.FS // for role section: reads agents/{agent}.md

// At the top of Section(), handle "role" specially:
func (r *SectionResolver) Section(name string, data PromptData) string {
    if name == "role" {
        return r.resolveRole(data)
    }
    // ... existing resolution algorithm
}

func (r *SectionResolver) resolveRole(data PromptData) string {
    if r.agent == "" {
        return ""
    }
    // Check disk sources first for override
    for _, src := range r.sources {
        stem := "role.agent-" + r.agent
        if content, _ := src.ReadFile(stem + ".md"); content != nil {
            s := strings.TrimRight(string(content), "\n")
            r.tracked = append(r.tracked, PromptSource{Label: sourceLabel(src, stem+".md"), Size: len(s)})
            return s
        }
    }
    // Fall back to embedded agents dir
    path := "agents/" + r.agent + ".md"
    data2, err := r.agentFS.ReadFile(path)
    if err != nil {
        return ""
    }
    doc, err := frontmatter.Parse(string(data2))
    if err != nil {
        return ""
    }
    body := strings.TrimRight(strings.TrimSpace(doc.Body), "\n")
    r.tracked = append(r.tracked, PromptSource{Label: "agent:" + r.agent, Size: len(body)})
    return body
}
```

Import `"primeradiant.com/serf/frontmatter"`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./agent/ -run TestSectionResolver_RoleSection -v`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./agent/... -count=1 -short`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add agent/section_resolver.go agent/section_resolver_test.go
git commit -m "feat: role section reads agents/*.md with frontmatter stripping"
```

---

## Task 6: Embedded Template and Section Support

**Files:**
- Modify: `agent/prompt_resolver.go` (embed directive)
- Modify: `agent/section_resolver.go` (add `RenderEmbedded` method)
- Test: `agent/section_resolver_test.go`

- [ ] **Step 1: Update the embed directive**

In `agent/prompt_resolver.go`, change:

```go
//go:embed prompts/*.md
var embeddedPrompts embed.FS
```

to:

```go
//go:embed prompts/templates/* prompts/sections/*
var embeddedPrompts embed.FS
```

This covers `prompts/templates/*.md.tmpl` and `prompts/sections/*.md` and `prompts/sections/*.md.tmpl`. Avoids `all:` which would include `.DS_Store` and editor temp files.

**Note:** This step must come AFTER Task 7 and 8 (section files exist), otherwise the embed directive fails on empty directories.

- [ ] **Step 2: Add RenderEmbedded method**

The `Render` method reads master templates from disk. Add `RenderEmbedded` that reads from the embedded FS (the normal path for production):

```go
// RenderEmbedded is like Render but reads the master template from the embedded FS.
func (r *SectionResolver) RenderEmbedded(fs embed.FS, prefix, name string, data PromptData) (string, []PromptSource, error) {
    r.tracked = nil

    tmplContent, err := fs.ReadFile(prefix + name + ".md.tmpl")
    if err != nil {
        return "", nil, fmt.Errorf("reading embedded template %s: %w", name, err)
    }

    funcMap := template.FuncMap{
        "section": func(sectionName string) string {
            return r.Section(sectionName, data)
        },
    }

    tmpl, err := template.New(name).Funcs(funcMap).Parse(string(tmplContent))
    if err != nil {
        return "", nil, fmt.Errorf("parsing template %s: %w", name, err)
    }

    var buf strings.Builder
    if err := tmpl.Execute(&buf, data); err != nil {
        return "", nil, fmt.Errorf("executing template %s: %w", name, err)
    }

    result := collapseBlankLines(buf.String())
    return strings.TrimSpace(result), r.tracked, nil
}
```

- [ ] **Step 3: Verify build passes**

Run: `go build ./agent/...`
Expected: success (section files from Tasks 7-8 must exist first)

- [ ] **Step 4: Commit**

```bash
git add agent/prompt_resolver.go agent/section_resolver.go
git commit -m "feat: embed directive covers subdirectories, add RenderEmbedded"
```

---

## Task 7: Create Section Files (Static Content)

**Files:**
- Create: all static `.md` section files listed in the file layout
- Delete: nothing yet (old files kept for comparison)

This is the content migration. Extract content from the four source files (`system.openai.md`, `system.anthropic.md`, `system.gemini.md`, `core.md`) into individual section files per the migration table in the spec.

- [ ] **Step 1: Create `prompts/sections/` and `prompts/templates/` directories**

```bash
mkdir -p agent/prompts/sections agent/prompts/templates
```

- [ ] **Step 2: Create identity.md**

From `core.md` lines 1-4 + `system.openai.md` personality section (deduplicated, coherent voice):

```markdown
## Identity

You are serf. You persist until the task is completely solved. You do not stop at partial
solutions or analysis. You do not end your turn until the deliverables are done and verified.

You are a deeply pragmatic, effective software engineer. You communicate efficiently,
keeping focus on the task without unnecessary detail.

- Honesty is non-negotiable. NEVER invent technical details, fabricate results, or claim
  you did something you did not do. If you do not know something, say so.
- ALL test failures are YOUR responsibility, even pre-existing ones. Never dismiss a
  failing test — it is a clue. Investigate it.
- NEVER ignore system or test output. Logs, warnings, error messages, and non-zero exit
  codes contain critical information. Read them carefully.
- Your job is not just to write code. It is to accomplish what the user asked. If the user
  asks for a running server, there must be a running server when you are done — not just
  config files that could start one.
- Correctness over speed. But do not waste time — be decisive when the path is clear.

Persist until the task is fully handled end-to-end within the current turn whenever feasible:
do not stop at analysis or partial fixes; carry changes through implementation, verification,
and a clear explanation of outcomes.

Assume the task requires you to make code changes or run tools to solve the problem. Go ahead
and actually implement the change. If you encounter challenges or blockers, attempt to resolve
them yourself.
```

- [ ] **Step 3: Create values.md**

From `core.md` lines 26-41 + `system.openai.md` values (merged, deduplicated):

```markdown
## Values

- Clarity: Communicate reasoning explicitly and concretely, so decisions and tradeoffs
  are easy to evaluate upfront.
- Pragmatism: Keep the end goal and momentum in mind, focusing on what will actually work
  and move things forward.
- Rigor: Expect technical arguments to be coherent and defensible. Surface gaps or weak
  assumptions with emphasis on creating clarity and moving the task forward.

- Never substitute a simpler workaround for the real implementation. No hardcoded values,
  stub functions, or shortcuts. When a specialized library exists for the hard part (game
  analysis, crypto, numerical methods), install and use it instead of reasoning manually.
- Prefer standard defaults over custom configuration. When a tool has default parameters,
  use them unless you have a specific reason to change them.
- Never weaken or delete a test to make it pass. Fix the implementation.
- Keep changes minimal and focused. Do not add unrelated features or abstractions.
- When delegating to subagents, break work into investigate → implement → verify stages.
  Investigate means both inspecting the workspace AND researching the problem — when you
  are uncertain about the right approach, search for knowledge or skills that would help
  you solve the problem before attempting implementation.
  Never trust a subagent's completion report — check the result yourself.
- Before finishing: clean up the working directory so it contains only the files you were
  asked to create. Verify services survive session exit, and run the project's actual test
  suite (look in /tests/ too, not just the working directory).

Avoid cheerleading, motivational language, or artificial reassurance. Stay concise and
communicate what is necessary — not more, not less.
```

- [ ] **Step 4: Create capabilities.md**

From `core.md` lines 17-23:

```markdown
## Capabilities

You can see images. Your visual perception is accurate — trust what you see. When an
image appears in your context, describe what you see in detail. Do not write code to
extract information you can already see. Use code for actions (computation, file I/O),
not for perception. If you need more detail on a specific area, crop or zoom that
region, then look at the result and describe it.
```

- [ ] **Step 5: Create tools.md (shared guidance)**

From `system.openai.md` lines 95-104 (general editing constraints only):

```markdown
## Editing

- Default to ASCII when editing or creating files. Only introduce non-ASCII or other Unicode
  characters when there is a clear justification and the file already uses them.
- Do not use Python to read/write files when a simple shell command or the edit tool would
  suffice.
```

- [ ] **Step 6: Create tools.provider-openai.md**

From `system.openai.md` lines 1-63 + lines 99-101 (apply_patch-specific):

Copy the full `## apply_patch` section (lines 1-63) verbatim, then append:

```markdown
- Try to use apply_patch for single file edits, but it is fine to explore other options to
  make the edit if it does not work well. Do not use apply_patch for changes that are
  auto-generated or when scripting is more efficient (such as search and replacing a string
  across a codebase).
```

- [ ] **Step 7: Create tools.provider-openai_append.md**

From `system.openai.md` lines 86-93:

```markdown
- When searching for text or files, prefer using `rg` or `rg --files` respectively because
  `rg` is much faster than alternatives like `grep`. (If the `rg` command is not found, then
  use alternatives.)
- Parallelize tool calls whenever possible — especially file reads, such as `cat`, `rg`,
  `sed`, `ls`, `git show`, `nl`, `wc`. Use `multi_tool_use.parallel` to parallelize tool
  calls and only this.
```

- [ ] **Step 8: Create tools.provider-anthropic.md**

From `system.anthropic.md` lines 8-16:

```markdown
## edit_file

Use the edit_file tool to make precise changes to existing files. It replaces an exact
occurrence of old_string with new_string. Rules:

- The old_string must be unique within the file. If it matches multiple locations, the
  edit will fail. Include enough surrounding context to make old_string unique.
- Always read the file first so you know the exact content to match.
- Keep edits small and focused. Make one logical change per edit_file call.
- Set replace_all to true only when renaming a symbol across the entire file.
```

- [ ] **Step 9: Create tools.provider-anthropic_append.md**

From `system.anthropic.md` lines 19-27:

```markdown
## Tool selection

- **grep**: Search file contents by regex. Use to find definitions, references, and patterns.
- **glob**: Find files by name pattern (e.g., `**/*_test.go`). Use to discover file layout.
- **read_file**: Read a specific file. Always read before editing.
- **write_file**: Create a new file or overwrite entirely. Use only for new files.
- **shell**: Run commands (build, test, git). Check output for errors.
- **edit_file**: Modify existing files. Prefer edit existing files over creating new ones.
```

- [ ] **Step 10: Create tools.provider-gemini.md**

From `system.gemini.md` lines 13-17:

```markdown
## edit_file

Use the edit_file tool to make precise changes to existing files. It replaces an exact
occurrence of old_string with new_string. The old_string must be unique within the file —
include enough surrounding context to disambiguate. Always read a file before editing it.
```

- [ ] **Step 11: Create tools.provider-gemini_append.md**

From `system.gemini.md` lines 19-31:

```markdown
## Tool selection

- **read_many_files**: Read several files in a single call. Use for batch exploration
  instead of multiple read_file calls.
- **read_file**: Read a single file.
- **list_directory**: List directory contents with optional depth. Use to explore project
  structure before diving into files.
- **grep_search**: Search file contents by regex pattern.
- **glob**: Find files by name pattern (e.g., `**/*.go`).
- **write_file**: Create a new file or overwrite entirely. Use only for new files.
- **run_shell_command**: Run commands (build, test, git). Check output for errors.
- **edit_file**: Modify existing files. Prefer editing existing files over creating new ones.
```

- [ ] **Step 12: Create remaining static sections**

Create `workflow.md`, `git-safety.md`, `security.md`, `task-tracking.md`, `communicate.agent-reviewer.md`, `non-interactive.agent-coordinator.md` from the source content identified in the spec migration tables. Copy the text faithfully from the source files.

**workflow.md** — from `core.md` lines 43-47
**git-safety.md** — from `system.openai.md` lines 106-116
**security.md** — from `core.md` lines 59-63
**task-tracking.md** — from `core.md` lines 65-76
**communicate.agent-reviewer.md** — from `agents/reviewer.md` lines 62-75 (the Decision/approve/reject section)
**non-interactive.agent-coordinator.md** — new content:

```markdown
## Non-interactive mode — CRITICAL

You are running in a non-interactive, headless environment. There is no human available.

RULES (these override ANY skill instructions that conflict):
- The task prompt IS the complete specification. Read it carefully, then DELEGATE.
- Decompose the task and spawn an implementer within your first 3 tool calls.
- Do NOT ask questions or request confirmation. Make judgment calls yourself.
- If a skill says "ask your human partner" or "confirm with user": make those
  judgment calls yourself. You are both the coordinator and the decision-maker.
- Focus on: read spec → scout → delegate → verify → deliver.
```

- [ ] **Step 13: Commit all section files**

```bash
git add agent/prompts/sections/
git commit -m "feat: create static section files from existing prompts"
```

---

## Task 8: Create Dynamic Section Templates (.tmpl files)

**Files:**
- Create: all `.md.tmpl` section files

- [ ] **Step 1: Create environment.md.tmpl**

From `profile.go:183-195`:

```
<environment>
Working directory: {{ .WorkingDir }}
Is git repository: {{ .IsGitRepo }}
Git branch: {{ .GitBranch }}
Platform: {{ .Platform }}
OS version: {{ .OSVersion }}
Today's date: {{ .Today }}
Model: {{ .Model }}
Knowledge cutoff: {{ .KnowledgeCutoff }}
{{ if .WorkspaceTree }}Workspace:
{{ .WorkspaceTree }}
{{ end }}</environment>
```

- [ ] **Step 2: Create git.md.tmpl**

From `profile.go:197-208`:

```
{{ if .IsGitRepo }}<git>
Branch: {{ .GitBranch }}
Modified files: {{ .GitModifiedFiles }}
Untracked files: {{ .GitUntrackedFiles }}
{{ if .GitRecentCommitTitles }}Recent commits:
{{ range .GitRecentCommitTitles }}- {{ . }}
{{ end }}{{ end }}</git>{{ end }}
```

- [ ] **Step 3: Create workspace.md.tmpl**

From `profile.go:211-233`:

```
{{ if or .WorkspaceTree .TestFiles .BuildInfo }}<workspace>
This is a snapshot of the working directory taken at session start. It does not update.

{{ if .WorkspaceTree }}Directory structure:
{{ .WorkspaceTree }}

{{ end }}{{ if .TestFiles }}Test files:
{{ range .TestFiles }}- {{ $.WorkingDirFull }}/{{ . }}
{{ end }}
{{ end }}{{ if .BuildInfo }}Build system:
{{ .BuildInfo }}
{{ end }}</workspace>{{ end }}
```

- [ ] **Step 4: Create skills.md.tmpl**

From `profile.go:236-260`:

```
{{ if .Skills }}<skills>
{{ if .HasUseSkill }}Load a skill by calling use_skill with its name. The response includes the skill directory path for accessing scripts and other collateral.
{{ else }}Load a skill by reading its SKILL.md file path with read_file. The skill directory may contain scripts and other collateral.
{{ end }}{{ range .Skills }}{{ if $.HasUseSkill }}- {{ .Name }}: {{ .Description }} [{{ .Dir }}]
{{ else }}- {{ .Name }}: {{ .Description }} [{{ .SkillFile }}]
{{ end }}{{ end }}</skills>{{ end }}
```

- [ ] **Step 5: Create tool-list.md.tmpl**

From `profile.go:263-282`:

```
Tools:
{{ range .ProfileTools }}- {{ .Name }}: {{ .Description }}
{{ end }}
Tool usage:
- Use tools to inspect the codebase before editing.
- When editing code, prefer the provider-aligned edit tool for this profile.
- After running commands, read errors carefully and fix them.
{{ if .MCPTools }}
MCP Tools (from external servers):
{{ range .MCPTools }}- {{ .Name }}: {{ .Description }}
{{ end }}{{ end }}{{ if .CustomTools }}
Additional tools:
{{ range .CustomTools }}- {{ .Name }}: {{ .Description }}
{{ end }}{{ end }}
```

- [ ] **Step 6: Create available-agents.md.tmpl**

From `plugin_prompt.go`:

```
{{ if .AvailableAgents }}<available_agents>
The following agent types are available for spawn_agent with agent_type parameter:
{{ range .AvailableAgents }}- {{ .Name }}: {{ .Description }}
{{ end }}</available_agents>{{ end }}
```

- [ ] **Step 7: Create project-docs.md.tmpl**

From `profile.go:284-294`:

```
{{ range .ProjectDocs }}{{ if .Path }}
----- BEGIN {{ .Path }} -----
{{ .Content }}
----- END {{ .Path }} -----
{{ end }}{{ end }}
```

- [ ] **Step 8: Create communicate.md.tmpl**

From `core.md` lines 49-57:

```
## {{ .ResultToolName }}

Call {{ .ResultToolName }} when the task is complete and verified. This exits the session.

- For automation workflows, prefer {{ .ResultToolName }} with an `output` object:
  `{message, data, artifacts}`.
- If the prompt defines a required output schema, {{ .ResultToolName }} MUST include `output`.
- Every response includes an inbox with pending user messages. Read them and adjust.
- If the inbox contains a message, acknowledge it in your next action.
```

- [ ] **Step 9: Create non-interactive.md.tmpl**

From `session.go:37-57`:

```
## Non-interactive mode — CRITICAL

You are running in a non-interactive, headless environment. There is no human available to
answer questions, provide clarification, or confirm your approach. Nobody will ever respond
to you. Any attempt to ask a question or wait for confirmation wastes your limited rounds.

RULES (these override ANY skill instructions that conflict):
- NEVER use {{ .ResultToolName }} to ask a question or request confirmation.
- The ONLY valid use of {{ .ResultToolName }} is to deliver FINAL work output.
- The task prompt IS the complete specification. Read it carefully, then BUILD.
- If a skill says "ask your human partner", "confirm with user", or "explore user intent":
  make those judgment calls yourself. You are both the implementer and the decision-maker.
- The brainstorming skill's "explore user intent" step means carefully re-reading the spec
  and extracting every requirement — NOT asking questions.
- Start coding within your first 3 tool calls. Read the spec, read relevant files, then write code.
- Focus on: read spec → plan internally → test → implement → verify → deliver.
```

- [ ] **Step 10: Commit**

```bash
git add agent/prompts/sections/
git commit -m "feat: create dynamic .tmpl section files for environment, git, workspace, skills, tools, etc."
```

---

## Task 9: Create Master Templates

**Files:**
- Create: `agent/prompts/templates/system.md.tmpl`
- Create: `agent/prompts/templates/subagent.md.tmpl`

- [ ] **Step 1: Create system.md.tmpl**

```
{{ section "identity" }}

{{ section "values" }}

{{ section "capabilities" }}

{{ section "tools" }}

{{ section "tool-list" }}

{{ section "workflow" }}

{{ section "git-safety" }}

{{ section "security" }}

{{ section "task-tracking" }}

{{ section "communicate" }}

{{ section "environment" }}

{{ section "git" }}

{{ section "workspace" }}

{{ section "skills" }}

{{ section "available-agents" }}

{{ section "role" }}

{{ section "project-docs" }}

{{ if .NonInteractive }}
{{ section "non-interactive" }}
{{ end }}
{{ if .UserInstructionOverride }}

{{ .UserInstructionOverride }}
{{ end }}
{{ range .CLIAppends }}

{{ . }}
{{ end }}
```

- [ ] **Step 2: Create subagent.md.tmpl**

```
{{ section "identity" }}

{{ section "values" }}

{{ section "capabilities" }}

{{ section "tools" }}

{{ section "tool-list" }}

{{ section "communicate" }}

{{ section "environment" }}

{{ section "workspace" }}

{{ section "role" }}
```

- [ ] **Step 3: Verify templates parse**

Write a quick test:

```go
func TestMasterTemplates_Parse(t *testing.T) {
    for _, name := range []string{"system", "subagent"} {
        content, err := embeddedPrompts.ReadFile("prompts/templates/" + name + ".md.tmpl")
        if err != nil {
            t.Fatalf("reading %s template: %v", name, err)
        }
        funcMap := template.FuncMap{"section": func(string) string { return "" }}
        _, err = template.New(name).Funcs(funcMap).Parse(string(content))
        if err != nil {
            t.Fatalf("parsing %s template: %v", name, err)
        }
    }
}
```

- [ ] **Step 4: Run test**

Run: `go test ./agent/ -run TestMasterTemplates_Parse -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/prompts/templates/
git commit -m "feat: create system and subagent master templates"
```

---

## Task 10: Integration — Wire Resolver into Session

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/profile.go`
- Test: existing session tests must still pass

This is the critical integration step. Replace the per-round prompt building with a single cached render.

- [ ] **Step 1: Add buildPromptData to session.go**

```go
func (s *Session) buildPromptData() PromptData {
    agentName := s.cfg.AgentName
    if agentName == "" {
        agentName = "coordinator"
    }

    data := PromptData{
        NonInteractive:      s.cfg.NonInteractive,
        Provider:            s.profile.ID(),
        Agent:               agentName,
        WorkingDir:          s.envInfo.WorkingDir,
        IsGitRepo:           s.envInfo.IsGitRepo,
        GitBranch:           s.envInfo.GitBranch,
        Platform:            s.envInfo.Platform,
        OSVersion:           s.envInfo.OSVersion,
        Today:               s.envInfo.Today,
        Model:               s.profile.Model(),
        KnowledgeCutoff:     s.envInfo.KnowledgeCutoff,
        GitModifiedFiles:    s.envInfo.GitModifiedFiles,
        GitUntrackedFiles:   s.envInfo.GitUntrackedFiles,
        GitRecentCommitTitles: s.envInfo.GitRecentCommitTitles,
        WorkspaceTree:       s.envInfo.Workspace.Tree,
        TestFiles:           s.envInfo.Workspace.TestFiles,
        BuildInfo:           s.envInfo.Workspace.BuildInfo,
        WorkingDirFull:      s.envInfo.WorkingDir,
        ResultToolName:      s.resultToolName(),
        UserInstructionOverride: strings.TrimSpace(s.cfg.UserInstructionOverride),
        CLIAppends:          readCLIAppends(s.cfg.SystemPromptAppend), // read file paths → contents
        ProjectDocs:         s.projectDocs,
    }

    // Skills
    hasUseSkill := false
    for _, td := range s.profile.ToolDefinitions() {
        if td.Name == "use_skill" {
            hasUseSkill = true
            break
        }
    }
    data.HasUseSkill = hasUseSkill
    for _, sm := range s.skills {
        data.Skills = append(data.Skills, SkillEntry{
            Name: sm.Name, Description: sm.Description,
            Dir: sm.Dir, SkillFile: sm.SkillFile,
        })
    }

    // Profile tools
    for _, td := range s.profile.ToolDefinitions() {
        desc := strings.TrimSpace(td.Description)
        if desc == "" { desc = "(no description)" }
        data.ProfileTools = append(data.ProfileTools, ToolEntry{Name: td.Name, Description: desc})
    }

    // MCP tools
    for _, td := range s.mcpTools {
        desc := strings.TrimSpace(td.Description)
        if desc == "" { desc = "(no description)" }
        data.MCPTools = append(data.MCPTools, ToolEntry{Name: td.Name, Description: desc})
    }

    // Custom tools (not core, not MCP)
    mcpNames := make(map[string]bool, len(s.mcpTools))
    for _, td := range s.mcpTools { mcpNames[td.Name] = true }
    for _, td := range s.reg.Definitions() {
        if s.coreToolNames[td.Name] || mcpNames[td.Name] { continue }
        desc := strings.TrimSpace(td.Description)
        if desc == "" { desc = "(no description)" }
        data.CustomTools = append(data.CustomTools, ToolEntry{Name: td.Name, Description: desc})
    }

    // Available agents
    names := make([]string, 0, len(s.pluginAgents))
    for n := range s.pluginAgents { names = append(names, n) }
    sort.Strings(names)
    for _, n := range names {
        a := s.pluginAgents[n]
        data.AvailableAgents = append(data.AvailableAgents, AgentEntry{Name: n, Description: a.Description})
    }

    return data
}
```

- [ ] **Step 2: Add renderSystemPrompt and helpers to session.go**

```go
// readCLIAppends reads --system-prompt-append file paths into their contents.
func readCLIAppends(paths []string) []string {
    var contents []string
    for _, p := range paths {
        b, err := os.ReadFile(p)
        if err != nil {
            continue
        }
        contents = append(contents, string(b))
    }
    return contents
}

func (s *Session) renderSystemPrompt() string {
    // Legacy: --system-prompt overrides everything. Fall back to current
    // resolution code which reads the file and appends dynamic blocks.
    if s.cfg.SystemPromptFile != "" {
        return s.buildInitialSystemPrompt() // existing method, kept for this path
    }
    // Legacy: subagents with BasePromptOverride.
    if s.cfg.BasePromptOverride != "" {
        return s.cfg.BasePromptOverride
    }

    gitRoot := gitRootOrEmpty(s.env, s.envInfo.WorkingDir)
    projDir := ProjectPromptsDir(gitRoot)
    if s.cfg.NoProjectPrompts { projDir = "" }
    projSections := ""
    globalSections := ""
    if projDir != "" { projSections = filepath.Join(projDir, "sections") }
    if gd := GlobalPromptsDir(); gd != "" { globalSections = filepath.Join(gd, "sections") }

    resolver := &SectionResolver{
        provider: s.profile.ID(),
        agent:    s.cfg.AgentName,
        agentFS:  embeddedAgents,
        sources: []SectionSource{
            diskSource{dir: projSections},
            diskSource{dir: globalSections},
            embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"},
        },
    }
    if resolver.agent == "" { resolver.agent = "coordinator" }

    data := s.buildPromptData()

    templateName := "system"
    if s.depth > 0 { templateName = "subagent" }

    result, sources, err := resolver.RenderEmbedded(
        embeddedPrompts, "prompts/templates/", templateName, data,
    )
    if err != nil {
        // Hard fail — template errors are bugs, not runtime conditions.
        panic(fmt.Sprintf("prompt template render failed: %v", err))
    }
    s.promptSourceLog = sources
    return result
}
```

- [ ] **Step 3: Replace per-round rebuild with cached prompt**

In `initSessionState()`, after `s.rebuildPromptCache()`, add:

```go
s.cachedSystemPrompt = s.renderSystemPrompt()
```

Add `cachedSystemPrompt string` and `promptSourceLog []PromptSource` fields to Session struct.

In the agentic loop (`processOneInput`, around line 1584), replace:

```go
sys := s.profile.BuildSystemPrompt(s.envInfo, s.projectDocs, s.cachedSkillList, s.cachedExtraTools)
if s.cachedAgentSection != "" {
    sys += s.cachedAgentSection
}
sys += s.cachedNonInteractiveGuidance
if strings.TrimSpace(s.cfg.UserInstructionOverride) != "" {
    sys = sys + "\n\n" + strings.TrimSpace(s.cfg.UserInstructionOverride) + "\n"
}
```

with:

```go
sys := s.cachedSystemPrompt
```

- [ ] **Step 4: Update buildInitialSystemPrompt to use cached prompt**

Replace the body of `buildInitialSystemPrompt()` with:

```go
func (s *Session) buildInitialSystemPrompt() string {
    return s.cachedSystemPrompt
}
```

- [ ] **Step 5: Update RegisterTool to invalidate cached prompt**

In `session.go`, find `RegisterTool` (which currently calls `s.rebuildPromptCache()`). Replace that call with:

```go
s.cachedSystemPrompt = s.renderSystemPrompt()
```

This ensures tools registered after init (e.g., reviewer's approve/reject tools) are reflected in the cached prompt.

- [ ] **Step 6: Run full test suite**

Run: `go test ./agent/... -count=1 -short`
Expected: PASS. If tests fail, debug — the most likely issue is whitespace/formatting differences in the assembled prompt. Compare old vs new output for specific test cases.

- [ ] **Step 7: Commit**

```bash
git add agent/session.go agent/prompt_data.go
git commit -m "feat: wire template resolver into session, cache system prompt"
```

---

## Task 11: Wire Subagent Prompt Through Templates

**Files:**
- Modify: `agent/subagents.go`
- Modify: `agent/session.go` (spawnReviewer)

- [ ] **Step 1: Update spawnSubagent to use resolver**

In `agent/subagents.go`, replace the manual `core + rolePrompt + skills` concatenation (lines 105-132) with template rendering:

```go
// Instead of manual concatenation, build PromptData for the subagent
// and render through the subagent template.
subData := PromptData{
    Provider:       s.profile.ID(),
    Agent:          agentName,
    ResultToolName: s.resultToolName(),
    WorkingDir:     s.envInfo.WorkingDir,
    // ... populate env fields from s.envInfo
}

subResolver := &SectionResolver{
    provider: s.profile.ID(),
    agent:    agentName,
    agentFS:  embeddedAgents,
    sources: []SectionSource{
        embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"},
    },
}

composed, _, err := subResolver.RenderEmbedded(
    embeddedPrompts, "prompts/templates/", "subagent", subData,
)
if err != nil {
    // fallback to legacy path
    composed = CorePrompt() + "\n\n" + rolePrompt
}
```

Remove the `BasePromptOverride` assignment for the new path.

- [ ] **Step 2: Update spawnReviewer similarly**

In `session.go:1108-1113`, replace the `stripPromptSection` + manual composition with template rendering using `agent: "reviewer"`. The reviewer's `communicate.agent-reviewer.md` section replaces the base communicate section automatically.

- [ ] **Step 3: Run full test suite**

Run: `go test ./agent/... -count=1 -short`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add agent/subagents.go agent/session.go
git commit -m "feat: subagent prompts use template resolver instead of manual concatenation"
```

---

## Task 12: Delete Old Prompt Files and Clean Up Dead Code

**Files:**
- Delete: `agent/prompts/system.openai.md`
- Delete: `agent/prompts/system.anthropic.md`
- Delete: `agent/prompts/system.gemini.md`
- Delete: `agent/prompts/core.md`
- Modify: `agent/session.go` (remove dead code)
- Modify: `agent/prompt_resolver.go` (remove dead code)
- Modify: `agent/plugin_prompt.go` (remove `FormatPluginAgentsPrompt`)
- Modify: `agent/profile.go` (simplify `BuildSystemPrompt`)

- [ ] **Step 1: Verify old files are no longer referenced**

Run: `grep -r "core\.md\|system\.openai\.md\|system\.anthropic\.md\|system\.gemini\.md" agent/ --include='*.go'`

Fix any remaining references. The `embeddedBasePrompt()` function in `profile.go` and `CorePrompt()` in `prompt_resolver.go` are the main ones.

- [ ] **Step 2: Delete old prompt files**

```bash
git rm agent/prompts/system.openai.md agent/prompts/system.anthropic.md agent/prompts/system.gemini.md agent/prompts/core.md
```

- [ ] **Step 3: Remove dead code**

- Delete `nonInteractiveGuidance()` from `session.go`
- Delete `FormatPluginAgentsPrompt()` from `plugin_prompt.go` (or the entire file if it's the only function)
- Delete `CorePrompt()` from `prompt_resolver.go`
- Delete `embeddedBasePrompt()` from `profile.go` (and `embeddedProviderCandidates`, `firstEmbedMatch`, etc. if no longer used)
- Remove `cachedAgentSection`, `cachedNonInteractiveGuidance`, `cachedSkillList`, `cachedExtraTools` from Session struct and `rebuildPromptCache()` (they're no longer needed — `buildPromptData()` assembles everything fresh for the single render)
- Simplify `BuildSystemPrompt` on `baseProfile` to just return `p.basePrompt` (it's still used by `WithBasePrompt` for the legacy path)
- Delete `stripPromptSection()` if no longer used
- Delete `customToolDescriptions()` from `session.go` if no longer used

- [ ] **Step 4: Run full test suite**

Run: `go test ./agent/... -count=1 -short`
Expected: PASS. Some tests may reference `CorePrompt()` or the old file structure — update them.

- [ ] **Step 5: Run full project tests**

Run: `go test ./... -count=1 -short`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor: delete old prompt files, remove dead prompt assembly code"
```

---

## Task 13: Regression Test — Compare Old vs New Prompt Output

**Files:**
- Create: `agent/section_resolver_regression_test.go`

- [ ] **Step 1: Write regression test**

Build the prompt using the old code path (if still available as a helper) and the new template path, then compare. Or snapshot the current prompt output before migration and compare against the template output.

```go
func TestPromptOutput_MatchesExpectedStructure(t *testing.T) {
    // Render the system template with representative data and verify
    // key sections appear in the right order.
    // This is a structural test, not an exact string match (whitespace will differ).

    data := PromptData{
        Provider: "openai",
        Agent: "coordinator",
        WorkingDir: "/tmp/test",
        IsGitRepo: true,
        GitBranch: "main",
        Platform: "linux",
        Model: "gpt-5.4",
        ResultToolName: "communicate",
        // ... other fields
    }

    resolver := &SectionResolver{
        provider: "openai",
        agent: "coordinator",
        agentFS: embeddedAgents,
        sources: []SectionSource{
            embedSource{fs: embeddedPrompts, prefix: "prompts/sections/"},
        },
    }

    result, _, err := resolver.RenderEmbedded(embeddedPrompts, "prompts/templates/", "system", data)
    if err != nil {
        t.Fatalf("render error: %v", err)
    }

    // Verify structural ordering
    sections := []string{
        "## Identity", "## Values", "## Capabilities",
        "apply_patch",  // OpenAI tools
        "Tools:", "<environment>", "<workspace>",
    }
    lastIdx := -1
    for _, marker := range sections {
        idx := strings.Index(result, marker)
        if idx < 0 {
            t.Errorf("missing section marker: %q", marker)
            continue
        }
        if idx < lastIdx {
            t.Errorf("section %q appears before previous section (out of order)", marker)
        }
        lastIdx = idx
    }
}
```

- [ ] **Step 2: Run regression test**

Run: `go test ./agent/ -run TestPromptOutput_MatchesExpectedStructure -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add agent/section_resolver_regression_test.go
git commit -m "test: add regression test for prompt template output structure"
```

---

## Task 14: Final Verification

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -count=1 -short`
Expected: all pass

- [ ] **Step 2: Build the binary**

Run: `make build` (or `go build ./cmd/serf/`)
Expected: success

- [ ] **Step 3: Run serf with a trivial task and inspect the transcript**

Run serf against a simple task, then extract the `system_prompt` from the transcript header JSON. Verify it looks correct — sections in the right order, no duplicated content, provider-specific tools present.

- [ ] **Step 4: Commit any final fixes**

```bash
git add -A
git commit -m "chore: final prompt template engine cleanup"
```

# Prompt Template Engine

**Date:** 2026-03-25
**Status:** Design

## Problem

Serf's system prompts are chaotic. Content that should be organized by concern
(identity, values, tools, workflow) is instead organized by provider file. The
OpenAI prompt file is 127 lines mixing apply_patch docs with personality, git
safety, and autonomy guidance. `core.md` duplicates identity and values with
slightly different wording. There is no structural way to say "the OpenAI tool
guidance goes in the Tools section" — the system just concatenates files.

Subagent prompts bypass the resolution chain entirely via `BasePromptOverride`,
creating a second code path that's hard to reason about.

The system prompt is also rebuilt every round of the agentic loop. All inputs
are cached at init time and never change, so the per-round string assembly is
wasted work.

## Solution

Replace the flat file concatenation with a template engine that transcludes
named sections. A readable master template defines the document structure.
Section files provide the content, with provider- and agent-specific variants
resolved by filename convention.

## File Layout

```
agent/prompts/
  templates/
    system.md.tmpl            # top-level (coordinator, interactive)
    subagent.md.tmpl          # all subagents
  sections/
    identity.md
    values.md
    capabilities.md           # native model capabilities (vision, etc.)
    tools.md                  # shared tool guidance
    tools.provider-openai.md  # apply_patch docs (replaces tools.md)
    tools.provider-openai_append.md   # rg, multi_tool_use.parallel tips
    tools.provider-anthropic.md       # edit_file docs
    tools.provider-anthropic_append.md
    tools.provider-gemini.md
    tools.provider-gemini_append.md
    workflow.md
    git-safety.md
    security.md
    task-tracking.md
    communicate.md.tmpl              # uses {{ .ResultToolName }}
    communicate.agent-reviewer.md     # approve/reject instead of communicate
    environment.md.tmpl               # dynamic: working dir, platform, etc.
    git.md.tmpl                       # dynamic: branch, modified files
    workspace.md.tmpl                 # dynamic: directory tree, test files
    skills.md.tmpl                    # dynamic: skill listing
    tool-list.md.tmpl                 # dynamic: registered tool names
    available-agents.md.tmpl          # dynamic: spawnable agent types
    project-docs.md.tmpl              # dynamic: CLAUDE.md etc.
    non-interactive.md                # headless/benchmark mode rules
    non-interactive.agent-coordinator.md  # "delegate" not "start coding"
```

Role sections (`role.agent-coordinator.md`, etc.) are not separate files.
The resolver reads existing `agents/*.md` files and strips YAML frontmatter.
This avoids duplicating agent prompt text.

## Master Templates

### system.md.tmpl (top-level)

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
```

### subagent.md.tmpl

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

Subagents omit workflow, git-safety, security, task-tracking, git status,
skills, available-agents, and project-docs. Subagents are not non-interactive
in the headless sense — they communicate with their parent via the communicate
tool. Autonomous execution guidance is baked into each role file.

## Filename Convention

`{section}[.{qualifier}][_modifier].md[.tmpl]`

- **qualifier:** `provider-openai`, `provider-anthropic`, `provider-gemini`,
  `agent-coordinator`, `agent-implementer`, etc. New providers (e.g.,
  `openai-compatible`) can be added by creating section files with the
  matching `provider-{id}` qualifier.
- **modifier:** `_prepend` or `_append`
- **`.tmpl` suffix:** render through `text/template` with PromptData context

Qualifiers are explicit and unambiguous — `provider-openai` cannot be confused
with `agent-openai`.

## Section Resolution

### Algorithm

For `{{ section "tools" }}` with provider=openai, agent=implementer:

```
1. Provider layer:
   prepend = tools.provider-openai_prepend.md     (if exists)
   body    = tools.provider-openai.md             (if exists, else tools.md)
   append  = tools.provider-openai_append.md      (if exists)

2. Agent layer:
   prepend = tools.agent-implementer_prepend.md   (if exists)
   body    = tools.agent-implementer.md           (if exists, REPLACES provider result)
   append  = tools.agent-implementer_append.md    (if exists)

3. Concatenate non-empty parts with double newline separator.
```

Agent-qualified bodies **replace** the provider-layer result (same semantics as
provider bodies replacing the base). Only `_prepend` and `_append` are additive.
This is necessary for cases like the reviewer's communicate section, where
`communicate.agent-reviewer.md` must replace the base communicate guidance with
approve/reject instructions rather than appending to it.

If no agent-qualified body exists, the provider result passes through unchanged.
Completely different prompt structures belong in different master templates.

### File Lookup Priority

For each filename, check sources in order (first match wins):

1. Project disk: `.serf/prompts/sections/`
2. Global disk: `~/.config/serf/prompts/sections/`
3. Embedded: `agent/prompts/sections/` (compiled into binary)

This preserves the self-contained binary for eval/benchmark deployment while
allowing disk overrides for development and customization.

### .tmpl Rendering

Files with `.tmpl` suffix are rendered through `text/template` with the
PromptData context before inclusion. The resolver tries `{stem}.md.tmpl` first,
then `{stem}.md`. This means a `.tmpl` file on disk can override an embedded
static `.md` file and vice versa.

### Missing Sections

If a section name resolves to zero files (no base, no provider variant, no
agent variant), the `section` function returns empty string. No error. This
allows templates to reference optional sections.

### Role Section (Special Case)

The `role` section reads from existing `agents/{agent}.md` files and strips
YAML frontmatter. The agent frontmatter (name, description, model, color,
tools, skills) is still needed by `spawnSubagent` for config — only the prompt
text portion is used as section content. This avoids maintaining duplicate files.

## Template Data Model

```go
type PromptData struct {
    // Resolution context
    NonInteractive bool
    Provider       string // "openai", "anthropic", "gemini", "openai-compatible"
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

    // Skills
    Skills      []SkillEntry // {Name, Description, Dir, SkillFile}
    HasUseSkill bool

    // Tools (three tiers)
    ProfileTools []ToolEntry // core tools from provider profile
    MCPTools     []ToolEntry // from MCP servers
    CustomTools  []ToolEntry // registered but not core or MCP

    // Available agents (for coordinator spawn_agent)
    AvailableAgents []AgentEntry // {Name, Description}

    // Project docs
    ProjectDocs []ProjectDoc // {Path, Content}

    // Result tool
    ResultToolName string // "communicate" or override

    // User instruction override (highest priority, appended last)
    UserInstructionOverride string
}
```

Derived from existing `EnvironmentInfo`, `Workspace`, `SkillMeta`, and
`ToolDefinition` types. Not a new source of truth — a view assembled from
existing session state.

## Resolver Implementation

```go
type SectionResolver struct {
    provider string
    agent    string
    sources  []SectionSource // checked in priority order
}

type SectionSource interface {
    ReadFile(name string) ([]byte, bool)
}
```

Two `SectionSource` implementations:
- `diskSource{dir string}` — reads from a filesystem directory
- `embedSource{fs embed.FS, prefix string}` — reads from embedded FS

The role section is a special case: it reads from the existing `builtinAgents`
embedded FS (`agent/agents/*.md`) and strips YAML frontmatter. This is handled
inside the resolver's `Section` method, not as a separate source.

The `Section(name string) string` method implements the resolution algorithm.
It is a **closure** that captures the `PromptData` and resolution context
(`provider`, `agent`) when the FuncMap is built — one FuncMap per render call.
This keeps the template syntax clean: `{{ section "tools" }}` with no extra
arguments.

Source tracking: every file read appends to a `[]PromptSource` slice recording
which file contributed and its byte size. This feeds into the transcript header
for prompt debugging.

## CLI Overrides

### --system-prompt

When `--system-prompt <path>` is set, the file at that path replaces the
entire template resolution — no master template, no sections. This matches
current behavior. The file content is used verbatim as the base prompt, with
the `<environment>`, `<workspace>`, and other dynamic blocks still appended
by `BuildSystemPrompt` (or under the new system, the session falls back to
legacy string assembly for this path).

### --system-prompt-append

When `--system-prompt-append <path>` is set, the file content is appended
after all template rendering, after even `UserInstructionOverride`. This
matches current behavior where append paths are always applied last.

In `PromptData`, add:

```go
    // CLI appends (--system-prompt-append, applied after everything)
    CLIAppends []string
```

The master template renders these at the very end:

```
{{ range .CLIAppends }}
{{ . }}
{{ end }}
```

## ResultToolName

The current code uses `strings.ReplaceAll(basePrompt, "communicate", name)`
to rename the result tool throughout the prompt. This is fragile — it matches
"communicate" in prose, not just tool references.

Under the template system, sections that reference the result tool use
`{{ .ResultToolName }}` in `.tmpl` files (e.g., `communicate.md.tmpl`,
`non-interactive.md.tmpl`). Static `.md` sections that don't reference the
tool name need no change.

## Integration

### BuildSystemPrompt Replacement

The current `BuildSystemPrompt` method (~125 lines of string building) and the
session-layer post-append code (agent section, non-interactive, user override,
plugin agents) are replaced by a single call:

```go
func (s *Session) renderSystemPrompt() string {
    if s.cfg.BasePromptOverride != "" {
        return s.cfg.BasePromptOverride  // legacy subagent path
    }

    resolver := s.profile.NewResolver(s.promptSources())
    data := s.buildPromptData()

    templateName := "system"
    if s.depth > 0 {
        templateName = "subagent"
    }

    result, sources, _ := resolver.Render(templateName, data)
    s.promptSourceLog = sources
    return result
}
```

### Build Once, Cache Forever

The system prompt is built once before the first turn and cached for the
session lifetime. The current code rebuilds it every round with identical
data. All inputs (`envInfo`, `projectDocs`, `cachedSkillList`, etc.) are
set during session init and never change.

```go
// During session init:
s.cachedSystemPrompt = s.renderSystemPrompt()

// In the agentic loop (replaces per-round rebuild):
sys := s.cachedSystemPrompt
```

### Subagent Migration

Subagents currently use `BasePromptOverride = core.md + persona` set by
the parent. Under the new system, the parent sets `templateName = "subagent"`
and `data.Agent = "implementer"` instead of manually concatenating strings.
Migration is incremental — the `BasePromptOverride` escape hatch remains
until all subagent paths use templates.

## Content Migration

### From system.openai.md (deleted)

| Lines | Content | Destination |
|-------|---------|-------------|
| 1-63 | apply_patch docs | `tools.provider-openai.md` |
| 65-84 | Personality, Values | `identity.md`, `values.md` (shared) |
| 86-93 | rg, multi_tool_use.parallel | `tools.provider-openai_append.md` |
| 95-104 | Editing constraints | general → `tools.md`, apply_patch → `tools.provider-openai.md` |
| 106-116 | Git safety | `git-safety.md` |
| 118-127 | Autonomy | fold into `identity.md` |

### From system.anthropic.md (deleted)

| Lines | Content | Destination |
|-------|---------|-------------|
| 1 | Identity | covered by shared `identity.md` |
| 4-6 | Skill loading | handled by `skills.md.tmpl` (`HasUseSkill` logic) |
| 8-16 | edit_file docs | `tools.provider-anthropic.md` |
| 18-27 | Tool selection | `tools.provider-anthropic_append.md` |

### From system.gemini.md (deleted)

| Lines | Content | Destination |
|-------|---------|-------------|
| 1 | Identity | covered by shared `identity.md` |
| 4-7 | GEMINI.md reference | fold into project-docs section |
| 9-11 | Skill loading | handled by `skills.md.tmpl` |
| 13-17 | edit_file docs | `tools.provider-gemini.md` |
| 19-31 | Tool selection | `tools.provider-gemini_append.md` |

### From core.md (deleted)

| Lines | Content | Destination |
|-------|---------|-------------|
| 1-4 | Identity | `identity.md` |
| 17-23 | Vision | `capabilities.md` |
| 25-41 | Values | `values.md` |
| 43-47 | Workflow | `workflow.md` |
| 49-57 | communicate | `communicate.md` |
| 59-63 | Security | `security.md` |
| 65-76 | Task tracking | `task-tracking.md` |

### From Go code (deleted)

| Source | Destination |
|--------|-------------|
| `nonInteractiveGuidance()` | `non-interactive.md.tmpl` + `non-interactive.agent-coordinator.md` (introduces coordinator/subagent split; current code has no depth awareness) |
| `FormatPluginAgentsPrompt()` | `available-agents.md.tmpl` |
| `BuildSystemPrompt()` env block | `environment.md.tmpl` |
| `BuildSystemPrompt()` git block | `git.md.tmpl` |
| `BuildSystemPrompt()` workspace block | `workspace.md.tmpl` |
| `BuildSystemPrompt()` tool listing | `tool-list.md.tmpl` |
| `BuildSystemPrompt()` project docs | `project-docs.md.tmpl` |

### End State

`system.openai.md`, `system.anthropic.md`, `system.gemini.md`, and `core.md`
are deleted. `BuildSystemPrompt()` shrinks to ~10 lines.
`nonInteractiveGuidance()` is deleted. All prompt content lives in section
files and master templates.

## Risks

- **Prompt regression:** The assembled prompt will differ from the current one
  (section ordering, whitespace, deduplication of overlapping content). Must
  diff old vs new output and verify on benchmarks.
- **Template errors at runtime:** Syntax errors in `.tmpl` files surface at
  render time. Mitigated by startup validation and tests that render all
  templates with representative data.
- **embed.FS directive:** The current `//go:embed prompts/*.md` only matches
  `.md` files in the top-level `prompts/` directory. The new layout needs
  `//go:embed prompts/templates/* prompts/sections/*` or the broader
  `all:prompts` directive to capture subdirectories and `.tmpl` files.
- **Reviewer tool registration timing:** The reviewer subagent gets
  `approve`/`reject` tools registered after `NewSession`, which is after
  the prompt is built and cached. The reviewer's tool-list section must
  either be populated from the agent definition's tool list (known at
  template time) or the reviewer must use a subagent template that omits
  the tool-list section.

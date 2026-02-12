# Audit: Section 6 (System Prompts and Environment Context)

Auditor: Claude Opus 4.6
Date: 2026-02-11
Spec: `/Users/jesse/prime-radiant/serf/coding-agent-loop-spec.md` lines 976-1046

## Files Reviewed

- `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (ProviderProfile interface, BuildSystemPrompt, env block, profile constructors)
- `/Users/jesse/prime-radiant/serf/internal/agent/prompt_resolver.go` (ResolveSystemPrompt, layered prompt resolution)
- `/Users/jesse/prime-radiant/serf/internal/agent/project_docs.go` (LoadProjectDocs, 32KB budget, truncation)
- `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (NewSession, processOneInput, final prompt assembly)
- `/Users/jesse/prime-radiant/serf/internal/agent/git_snapshot.go` (snapshotGit, gitOriginURL)
- `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go` (LocalExecutionEnvironment, Platform, OSVersion)
- `/Users/jesse/prime-radiant/serf/internal/agent/env.go` (ExecutionEnvironment interface)
- `/Users/jesse/prime-radiant/serf/internal/agent/prompts/system.openai.md`
- `/Users/jesse/prime-radiant/serf/internal/agent/prompts/system.anthropic.md`
- `/Users/jesse/prime-radiant/serf/internal/agent/prompts/system.gemini.md`
- `/Users/jesse/prime-radiant/serf/internal/agent/prompts/base.md`
- `/Users/jesse/prime-radiant/serf/internal/agent/profile_test.go`
- `/Users/jesse/prime-radiant/serf/internal/agent/prompt_resolver_test.go`
- `/Users/jesse/prime-radiant/serf/internal/agent/project_docs_test.go`
- `/Users/jesse/prime-radiant/serf/internal/agent/session_test.go`

---

## Summary

Section 6 is broadly well-implemented. All five layers exist with the correct priority ordering. The environment context block matches the spec format exactly. Git context captures branch, modified/untracked counts, and recent commits. Project document discovery walks from git root to cwd with the correct 32KB budget and truncation marker. Provider-specific doc file lists (AGENTS.md/CLAUDE.md/GEMINI.md/.codex/instructions.md) are correctly partitioned per profile.

There are two gaps and two minor observations below.

---

## Gaps

### GAP-6.01: MCP and custom tool descriptions placed after project docs, violating layer ordering

**Status:** GAP (minor, cosmetic)

**Spec requirement (6.1):** The 5-layer system prompt ordering puts tool descriptions (layer 3) BEFORE project-specific instructions (layer 4):
```
1. Provider-specific base instructions
2. Environment context
3. Tool descriptions         <-- before project docs
4. Project-specific instructions
5. User instructions override
```

**Evidence:** In `session.go` `processOneInput` (lines 754-781), `BuildSystemPrompt` produces the base prompt with core tool descriptions and project docs in the correct order (layers 1-4). However, MCP tool descriptions and custom tool descriptions are appended AFTER project docs:

```go
sys := s.profile.BuildSystemPrompt(s.envInfo, docs, skillList) // layers 1-4 (tools then docs)

if len(s.mcpTools) > 0 {
    sys += "\nMCP Tools (from external servers):\n"  // after project docs
    ...
}
if extra := s.customToolDescriptions(); len(extra) > 0 {
    sys += "\nAdditional tools:\n"                   // after MCP tools, after project docs
    ...
}
if s.cfg.UserInstructionOverride != "" {
    sys = sys + "\n\n" + override + "\n"             // layer 5, correct
}
```

**Description:** Core tool descriptions (from the profile) appear in the correct position (layer 3, before project docs). But MCP server tool descriptions and custom-registered tool descriptions are spliced between project docs (layer 4) and the user instruction override (layer 5). This creates a split where tool documentation appears in two different places in the prompt, with project instructions sandwiched between them. The spec intends all tool descriptions to be in a single layer before project instructions.

**Impact:** Low. The model still sees all tool descriptions and project docs. The layer violation is unlikely to cause behavioral issues since tool descriptions are clearly labeled sections. User instruction override (layer 5) still has highest priority, which is the most important ordering constraint.

---

### GAP-6.02: OSVersion() returns platform/arch instead of actual OS version string

**Status:** GAP (minor, cosmetic)

**Spec requirement (6.3):**
```
OS version: {os_version_string}
```

**Evidence:** `env_local.go` line 93:
```go
func (e *LocalExecutionEnvironment) OSVersion() string { return runtime.GOOS + "/" + runtime.GOARCH }
```

This produces values like `darwin/arm64` or `linux/amd64`.

**Description:** The spec field is labeled "OS version" which conventionally means the operating system version (e.g., "Darwin 25.2.0", "Ubuntu 22.04", "macOS 15.3"). The current implementation returns the Go platform/architecture pair instead. The `Platform()` field already covers `darwin/linux/windows`, so OSVersion providing the same info with an architecture suffix is partially redundant. Comparable agents (Claude Code, codex-rs) typically shell out to `uname -r` or read `/etc/os-release` to produce an actual version string.

**Impact:** Low. The model receives a reasonable string that conveys useful information. The `GOOS/GOARCH` format is simply less informative than a true OS version string.

---

## Conformant Areas

### 6.1 Layered System Prompt Construction -- PASS (with GAP-6.01 caveat)

The five layers exist and are assembled in the correct priority order:

1. **Layer 1 (Provider base):** `ResolveSystemPrompt` loads the embedded provider-specific prompt (`prompts/system.{openai,anthropic,gemini}.md`) followed by the common base (`prompts/base.md`), plus global and project `.serf/prompts/` additions. This becomes `p.basePrompt` via `WithBasePrompt`. Written first in `BuildSystemPrompt`.

2. **Layer 2 (Environment context):** The `<environment>` block is emitted immediately after the base prompt in `BuildSystemPrompt` (profile.go lines 143-152). The `<git>` block follows when `env.IsGitRepo` is true (lines 154-166).

3. **Layer 3 (Tool descriptions):** Core tool definitions are listed in `BuildSystemPrompt` (lines 176-187). MCP and custom tools are appended later in `processOneInput` -- see GAP-6.01.

4. **Layer 4 (Project instructions):** Project docs (AGENTS.md, CLAUDE.md, etc.) are loaded by `LoadProjectDocs` and appended by `BuildSystemPrompt` (lines 189-199) with `BEGIN`/`END` markers.

5. **Layer 5 (User override):** `UserInstructionOverride` is appended last in `processOneInput` (line 780-781), verified by `TestSession_UserInstructionOverride_AppendedLastToSystemPrompt` which checks `HasSuffix`.

### 6.2 Provider-Specific Base Instructions -- PASS

Each profile has an embedded provider prompt covering the required topics:

- **OpenAI (`system.openai.md`):** Identity ("You are serf, a non-interactive coding agent (OpenAI profile)"), full apply_patch format specification with grammar and examples, coding best practices (in base.md), error handling (in base.md).

- **Anthropic (`system.anthropic.md`):** Identity ("Anthropic profile"), edit_file format (old_string uniqueness, read-before-edit), tool selection guidance (grep, glob, read_file, write_file, shell, edit_file), file operation preferences ("Prefer edit existing files over creating new ones").

- **Gemini (`system.gemini.md`):** Identity ("Gemini profile"), GEMINI.md conventions ("Look for a GEMINI.md file"), edit_file format, tool selection guidance using mapped names (read_many_files, list_directory, grep_search, run_shell_command).

Test coverage: `TestProviderProfiles_BuildSystemPrompt_IncludesProviderSpecificBaseInstructions`, `TestAnthropicProfile_SystemPromptCoversSpecTopics`, `TestGeminiProfile_SystemPromptCoversSpecTopics`, `TestOpenAIProfile_SystemPromptContainsApplyPatchFormat`.

### 6.3 Environment Context Block -- PASS

The `<environment>` block format matches the spec exactly (profile.go lines 143-152):

```
<environment>
Working directory: {env.WorkingDir}
Is git repository: {true/false}
Git branch: {env.GitBranch}
Platform: {env.Platform}
OS version: {env.OSVersion}
Today's date: {env.Today}
Model: {p.model}
Knowledge cutoff: {env.KnowledgeCutoff}
</environment>
```

All eight fields present. Format matches spec verbatim. Block generated once at session start (`NewSession` lines 193-203) via `envInfoFromEnv` + `snapshotGit`, stored in `s.envInfo`, reused in every `BuildSystemPrompt` call.

Test coverage: `TestSession_NaturalCompletion_LoadsOnlyProfileDocs_OpenAI` checks for `<environment>`, `Working directory:`, `Is git repository:`, `Platform:`, `Today's date:`, `Knowledge cutoff:`, `Tools:`.

Note: See GAP-6.02 regarding the OSVersion format.

### 6.4 Git Context -- PASS

Git snapshot captured at session start via `snapshotGit` (git_snapshot.go):

- **Current branch:** `git rev-parse --abbrev-ref HEAD` (line 49)
- **Short status:** `git status --porcelain` parsed for modified vs untracked file counts (lines 53-65). Produces integer counts, not full diffs, per spec.
- **Recent commit messages:** `git log -n 10 --pretty=format:%h%x20%s` (line 68). Captures last 10 commits (spec says "5-10"), within range.

The `<git>` block is emitted in `BuildSystemPrompt` (lines 154-166) only when `env.IsGitRepo` is true, containing branch, modified/untracked counts, and recent commit titles.

Test coverage: `TestSession_SystemPrompt_IncludesGitSnapshot_WhenInGitRepo` verifies `Is git repository: true`, `Git branch:`, `<git>`, `Modified files:`, `Untracked files:`, `Recent commits:`.

### 6.5 Project Document Discovery -- PASS

`LoadProjectDocs` (project_docs.go) implements all loading rules:

- **Walk from git root to cwd:** `gitRootOrEmpty` resolves the git root; `dirsFromRootToCwd` builds the directory chain; each directory is scanned for matching files (lines 45-88).
- **Recognized files per profile:**
  - OpenAI: `["AGENTS.md", ".codex/instructions.md"]`
  - Anthropic: `["CLAUDE.md", "AGENTS.md"]`
  - Gemini: `["GEMINI.md", "AGENTS.md"]`
- **AGENTS.md always loaded:** Present in all three profiles' `docFiles` lists.
- **Root-level first, deeper higher precedence:** `dirsFromRootToCwd` returns directories in root-to-leaf order. Deeper files are appended later, naturally giving them higher precedence in the prompt.
- **32KB byte budget:** `projectDocByteBudget = 32 * 1024` (line 19). Content truncated when budget exceeded (lines 70-83).
- **Truncation marker:** `projectDocTruncMark = "[Project instructions truncated at 32KB]"` (line 20). Appended when truncated (line 81).
- **Only profile-matching files loaded:** Achieved by each profile's `ProjectDocFiles()` returning only relevant filenames. The test `TestSession_NaturalCompletion_LoadsOnlyProfileDocs_OpenAI` verifies OpenAI loads AGENTS.md and .codex/instructions.md but NOT CLAUDE.md or GEMINI.md.

Test coverage: `TestLoadProjectDocs_WalksFromGitRootToWorkingDir_InDepthOrder`, `TestLoadProjectDocs_TruncatesTo32KBAndAddsMarker`, plus integration tests in session_test.go.

### Additional system prompt features beyond spec (not gaps)

The following features exist in the implementation but are not specified in section 6. They represent extensions, not deviations:

- **Skills section (`<skills>`):** When skills are discovered, a `<skills>` XML block is included between the environment context and tool descriptions. This is an extension not mentioned in the spec's 5-layer model but does not violate it.
- **System prompt per-round rebuild:** `processOneInput` rebuilds the system prompt (including fresh project docs) every tool round, not just once per input. This is more aggressive than the spec requires ("generated at session start") but ensures newly-created AGENTS.md files are reflected.
- **`.serf/prompts/` addition mechanism:** The `ResolveSystemPrompt` function supports global and project-level prompt additions from `.serf/prompts/` directories. This is an implementation detail for the base prompt layer, not a separate layer.

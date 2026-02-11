# Section 6: System Prompts and Environment Context - Audit Findings

## Summary
7 gaps found (1 Important, 5 Minor, 1 Info)

## Findings

### GAP-6.01: Model display name vs. raw model ID in environment block
- **Spec requirement:** Section 6.3 specifies `Model: {model_display_name}` in the environment context block.
- **Current state:** `BuildSystemPrompt` in `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` line 150 uses `p.model` (the raw model ID string, e.g., "gpt-5.2") rather than a display name. The LLM layer has a `ModelInfo.DisplayName` field in `/Users/jesse/prime-radiant/serf/internal/llm/model_catalog.go` line 17 but it is never consulted during system prompt construction. In practice the raw model ID and the display name are often identical, so this is a minor semantic gap.
- **Severity:** Minor
- **Files checked:** `internal/agent/profile.go` (line 150), `internal/llm/model_catalog.go` (lines 14-26)

### GAP-6.02: Layer 3 (Tool descriptions) ordering relative to Layer 2 (Environment context) includes extra sections
- **Spec requirement:** Section 6.1 specifies the layered order as: (1) Provider-specific base instructions, (2) Environment context, (3) Tool descriptions, (4) Project-specific instructions, (5) User instructions override. The spec describes tool descriptions as layer 3, coming immediately after environment context.
- **Current state:** In `BuildSystemPrompt` (`/Users/jesse/prime-radiant/serf/internal/agent/profile.go` lines 131-201), the ordering is: base prompt, environment block, git block, skills block, tool descriptions, project docs. The `<git>` section (lines 154-166) and `<skills>` section (lines 168-174) are inserted between environment context (layer 2) and tool descriptions (layer 3). These sections are not mentioned as separate layers in the spec. This is benign -- git context is arguably part of environment context, and skills are arguably part of tool descriptions -- but the spec does not define these as separate blocks.
- **Severity:** Info
- **Files checked:** `internal/agent/profile.go` (lines 131-201), `internal/agent/session.go` (lines 694-715)

### GAP-6.03: OpenAI base prompt does not explicitly cover "identity" topic
- **Spec requirement:** Section 6.2 says the OpenAI profile should "Cover identity, tool usage (especially apply_patch conventions), coding best practices, error handling guidance."
- **Current state:** The OpenAI prompt at `/Users/jesse/prime-radiant/serf/internal/agent/prompts/system.openai.md` line 1 says "You are serf, a non-interactive coding agent (OpenAI profile)." This covers identity minimally ("serf"). The codex-rs system prompt has a much more detailed identity section. However, the spec explicitly says "The spec does NOT prescribe full system prompt text -- those are implementation details that change frequently. It specifies what topics the prompt must cover." The current prompt does cover identity (line 1), apply_patch conventions (lines 4-66), and coding best practices (via base.md). Error handling guidance is present in base.md ("Fix errors yourself rather than reporting them and stopping"). All four topics are covered.
- **Severity:** Minor
- **Files checked:** `internal/agent/prompts/system.openai.md`, `internal/agent/prompts/base.md`

### GAP-6.04: Anthropic base prompt does not explicitly cover "identity" in the native Claude Code style
- **Spec requirement:** Section 6.2 says the Anthropic profile should "Mirror Claude Code system prompt. Cover identity, tool selection guidance (read before edit, edit over write), the edit_file format (old_string must be unique), file operation preferences."
- **Current state:** The Anthropic prompt at `/Users/jesse/prime-radiant/serf/internal/agent/prompts/system.anthropic.md` starts with "You are serf, a non-interactive coding agent (Anthropic profile)." It covers: edit_file format (old_string uniqueness, lines 9-13), tool selection guidance (lines 17-22 with "read before editing", "Prefer editing existing files"), and file operation preferences. All required topics are present. However, the "read before edit" guidance is "Always read the file first" in edit_file section and "Read files before editing" in base.md, but the phrase "edit over write" or "edit_file over write_file" for existing files could be slightly more explicit. The prompt says "Use only for new files" for write_file which implies the preference.
- **Severity:** Minor
- **Files checked:** `internal/agent/prompts/system.anthropic.md`, `internal/agent/prompts/base.md`

### GAP-6.05: Gemini base prompt does not explicitly mention "coding best practices"
- **Spec requirement:** Section 6.2 says the Gemini profile should "Cover identity, tool usage, GEMINI.md conventions, coding best practices."
- **Current state:** The Gemini prompt at `/Users/jesse/prime-radiant/serf/internal/agent/prompts/system.gemini.md` covers: identity (line 1), GEMINI.md conventions (lines 5-7), tool usage (lines 9-26 with read_many_files, list_dir, etc.). Coding best practices are covered by `base.md` which is always loaded alongside the provider-specific prompt. All required topics are addressed when considering both files together.
- **Severity:** Minor
- **Files checked:** `internal/agent/prompts/system.gemini.md`, `internal/agent/prompts/base.md`

### GAP-6.06: No dedicated unit test for `snapshotGit()` function
- **Spec requirement:** Section 6.4 specifies git context snapshot at session start must include: current branch, short status (modified/untracked file count), recent commit messages (last 5-10).
- **Current state:** The `snapshotGit()` function in `/Users/jesse/prime-radiant/serf/internal/agent/git_snapshot.go` correctly implements all three requirements: branch (line 49-51), modified/untracked counts via `git status --porcelain` (lines 53-65), and recent commits via `git log -n 10` (lines 68-76). However, there is no dedicated unit test for `snapshotGit()` itself. The function is tested indirectly through `TestSession_SystemPrompt_IncludesGitSnapshot_WhenInGitRepo` in `session_test.go` (line 507), which verifies the system prompt contains the git snapshot fields. The `git_snapshot_test.go` file only tests `gitOriginURL()`, not `snapshotGit()`. A dedicated test would improve confidence in edge cases (e.g., fresh repo with no commits, detached HEAD).
- **Severity:** Minor
- **Files checked:** `internal/agent/git_snapshot.go`, `internal/agent/git_snapshot_test.go`, `internal/agent/session_test.go`

### GAP-6.07: Project doc loading does not filter `.codex/instructions.md` path for non-OpenAI providers at the LoadProjectDocs level
- **Spec requirement:** Section 6.5 says "Only load files matching the active provider profile (e.g., Anthropic profile loads AGENTS.md and CLAUDE.md, not GEMINI.md)" and "AGENTS.md is always loaded regardless of provider."
- **Current state:** The filtering is implemented correctly via `ProjectDocFiles()` on each profile. Each profile returns only its own recognized files: OpenAI returns `["AGENTS.md", ".codex/instructions.md"]` (profile.go line 210), Anthropic returns `["CLAUDE.md", "AGENTS.md"]` (line 246), Gemini returns `["GEMINI.md", "AGENTS.md"]` (line 278). These lists are passed to `LoadProjectDocs()` as the `filenames` parameter, so the function only looks for those specific files. The filtering happens at the call site rather than inside `LoadProjectDocs`, which is a reasonable design. The test `TestSession_NaturalCompletion_LoadsOnlyProfileDocs` (session_test.go line 58) verifies that OpenAI profile loads AGENTS.md and .codex/instructions.md but NOT CLAUDE.md or GEMINI.md. This is correctly implemented, but there are no equivalent integration tests verifying that Anthropic skips GEMINI.md/.codex/instructions.md or that Gemini skips CLAUDE.md/.codex/instructions.md.
- **Severity:** Important
- **Files checked:** `internal/agent/profile.go` (lines 210, 246, 278), `internal/agent/project_docs.go`, `internal/agent/session_test.go` (line 58)

## Fully Implemented (Verified)

### 6.1 Layered System Prompt Construction
- **Layer 1 (Provider-specific base instructions):** Correctly loaded from embedded provider-specific files (`prompts/system.openai.md`, `prompts/system.anthropic.md`, `prompts/system.gemini.md`) plus `prompts/base.md` via `ResolveSystemPrompt()` in `/Users/jesse/prime-radiant/serf/internal/agent/prompt_resolver.go`. Provider prompt comes before base content (tested in `TestResolveSystemPrompt_ProviderBeforeBase`).
- **Layer 2 (Environment context):** Generated in `BuildSystemPrompt()` at `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` lines 143-152. Contains all 8 required fields. Verified by `TestSession_NaturalCompletion_LoadsOnlyProfileDocs` which checks for `<environment>`, `Working directory:`, `Is git repository:`, `Platform:`, `Today's date:`, `Knowledge cutoff:`, and `Tools:`.
- **Layer 3 (Tool descriptions):** Generated in `BuildSystemPrompt()` at lines 176-187. Lists all profile tools with descriptions. MCP tools appended in `processOneInput()` at session.go lines 702-711.
- **Layer 4 (Project-specific instructions):** Loaded via `LoadProjectDocs()` and rendered at lines 189-199. Files loaded from git root to cwd in depth order. Provider-specific file filtering via `ProjectDocFiles()`.
- **Layer 5 (User instructions override):** `UserInstructionOverride` appended last in `processOneInput()` at session.go lines 713-715. Verified by `TestSession_UserInstructionOverride_AppendedLastToSystemPrompt` which checks it appears after project docs.

### 6.2 Provider-Specific Base Instructions
- **OpenAI profile:** Covers identity ("You are serf, a non-interactive coding agent (OpenAI profile)"), apply_patch conventions (full v4a patch format specification with grammar, examples), coding best practices (via base.md), error handling (via base.md). Tested in `TestOpenAIProfile_SystemPromptContainsApplyPatchFormat` and `TestProviderProfiles_BuildSystemPrompt_IncludesProviderSpecificBaseInstructions`.
- **Anthropic profile:** Covers identity, edit_file format (old_string uniqueness), tool selection guidance (read before edit, prefer edit over write), file operation preferences. Tested in `TestAnthropicProfile_SystemPromptCoversSpecTopics`.
- **Gemini profile:** Covers identity, tool usage (read_many_files, list_directory), GEMINI.md conventions. Tested in `TestGeminiProfile_SystemPromptCoversSpecTopics`.

### 6.3 Environment Context Block
- All 8 fields present: Working directory, Is git repository (bool), Git branch, Platform, OS version, Today's date (YYYY-MM-DD format), Model, Knowledge cutoff.
- Uses `<environment>` / `</environment>` tags as specified.
- Generated at session start and stored in `s.envInfo` (session.go line 190).
- Knowledge cutoff correctly sourced from `profile.KnowledgeCutoff()` (session.go line 181).
- Today's date uses UTC and `2006-01-02` format (profile.go line 333).

### 6.4 Git Context Snapshot
- Snapshotted at session start via `snapshotGit()` (session.go line 182).
- Current branch: Retrieved via `git rev-parse --abbrev-ref HEAD` (git_snapshot.go line 49).
- Modified/untracked counts: Parsed from `git status --porcelain` (git_snapshot.go lines 53-65). Correctly counts `??` prefix as untracked and everything else as modified.
- Recent commits: Retrieved via `git log -n 10 --pretty=format:%h%x20%s` (git_snapshot.go line 68). Returns last 10 commits (within spec range of 5-10).
- Rendered in a `<git>` block in the system prompt with Branch, Modified files, Untracked files, and Recent commits (profile.go lines 154-166).
- Tested by `TestSession_SystemPrompt_IncludesGitSnapshot_WhenInGitRepo`.

### 6.5 Project Document Discovery
- **Walk from git root to working directory:** Implemented in `dirsFromRootToCwd()` (project_docs.go lines 92-118). Uses `gitRootOrEmpty()` to find git root, falls back to cwd for non-git repos.
- **Recognized files:** OpenAI: `AGENTS.md`, `.codex/instructions.md`. Anthropic: `CLAUDE.md`, `AGENTS.md`. Gemini: `GEMINI.md`, `AGENTS.md`. All four spec-listed file names are covered.
- **AGENTS.md always loaded:** Present in all three profile `docFiles` lists.
- **Root-level files loaded first:** `dirsFromRootToCwd` returns directories root-first (tested in `TestLoadProjectDocs_WalksFromGitRootToWorkingDir_InDepthOrder`).
- **Subdirectory files appended (deeper = higher precedence):** Depth ordering verified by test.
- **32KB budget with truncation marker:** Implemented with `projectDocByteBudget = 32 * 1024` and `projectDocTruncMark = "[Project instructions truncated at 32KB]"` (project_docs.go lines 18-20). Tested in `TestLoadProjectDocs_TruncatesTo32KBAndAddsMarker`.
- **Provider-specific filtering:** Filtering achieved by each profile returning only its recognized files in `ProjectDocFiles()`. OpenAI profile tested end-to-end in `TestSession_NaturalCompletion_LoadsOnlyProfileDocs` confirming CLAUDE.md and GEMINI.md are excluded.

### Additional System Prompt Features (beyond spec Section 6)
- CLI override via `--system-prompt` replaces embedded base entirely (tested in `TestResolveSystemPrompt_CLIOverride`, `TestSession_SystemPromptFile_OverridesBasePrompt`).
- CLI append via `--system-prompt-append` always applied (tested in `TestResolveSystemPrompt_AppendPaths`, `TestResolveSystemPrompt_AppendWithCLIOverride`).
- Global additions from `~/.config/serf/prompts/` (tested in `TestResolveSystemPrompt_GlobalOverride`, `TestResolveSystemPrompt_GlobalAndProjectBothAppended`).
- Project additions from `.serf/prompts/` (tested in `TestResolveSystemPrompt_ProjectAddsToEmbedded`).
- Skills section dynamically included when skills are discovered (tested in `TestBuildSystemPrompt_IncludesSkillsList`).
- MCP tool descriptions appended to system prompt when MCP servers are connected (session.go lines 702-711).

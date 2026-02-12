# Section 3: Provider-Aligned Toolsets - Audit Report

Spec: `coding-agent-loop-spec.md`, lines 454-706
Codebase: `internal/agent/` (profile.go, tool_registry.go, session.go, env_local.go, subagents.go, prompt_resolver.go, prompts/)

## Summary

Section 3 is largely well-implemented. The three-profile architecture (OpenAI, Anthropic, Gemini) exists with correct tool selection, name mapping, and provider-specific system prompts. Tool registry supports registration, unregistration, name collision via latest-wins, and JSON Schema validation. Core tool implementations cover read_file, write_file, edit_file, shell, grep, glob with the expected behaviors (fuzzy matching, process groups, SIGTERM/SIGKILL, mtime sorting, env filtering, etc.).

Key gaps are minor: a structural deviation where the ProviderProfile does not own a ToolRegistry instance (the Session owns it instead), Anthropic context_window_size set to 200K instead of 1M, and the grep `output_mode` parameter being globally defined rather than Anthropic-specific as stated in the spec.

---

## Conforming Items

### PASS: 3.1 Provider Alignment Principle
Three distinct profiles exist: OpenAI (apply_patch-based), Anthropic (edit_file-based), Gemini (edit_file + read_many_files + list_dir). Each profile has its own embedded system prompt (prompts/system.openai.md, system.anthropic.md, system.gemini.md) that mirrors the reference agent's conventions. Tool name mappings correctly alias canonical names to provider-specific names.

### PASS: 3.2 ProviderProfile Interface (partial)
The `ProviderProfile` Go interface at profile.go:35-53 includes: `ID()`, `Model()`, `ToolDefinitions()`, `BuildSystemPrompt()`, `ProviderOptions()`, `SupportsReasoning()`, `SupportsStreaming()`, `SupportsParallelToolCalls()`, `ContextWindowSize()`. All capability flags are present.

### PASS: 3.3 Shared Core Tools - read_file
Correctly implements: file_path (required), offset (optional, 1-based), limit (optional, default 2000). Returns "NNN | content" format with 4-digit padded line numbers. Image support returns base64-encoded data. Binary file detection via NUL byte.

### PASS: 3.3 Shared Core Tools - write_file
Correctly implements: file_path (required), content (required). Creates parent directories via `os.MkdirAll`. Returns bytes written confirmation.

### PASS: 3.3 Shared Core Tools - edit_file
Correctly implements: file_path, old_string, new_string, replace_all (default false). Exact match with fuzzy fallback (whitespace normalization). Error on non-unique match when replace_all=false. Returns replacement count.

### PASS: 3.3 Shared Core Tools - shell
Correctly implements: command (required), timeout_ms (optional), description (optional). New process group via `Setpgid: true`. SIGTERM then wait 2s then SIGKILL on timeout. Environment variable filtering. Returns stdout, stderr, exit code, duration, timed_out.

### PASS: 3.3 Shared Core Tools - grep
Correctly implements: pattern (required), path (optional, default working dir), glob_filter (optional), case_insensitive (optional), max_results (default 100). Returns matching lines with paths and line numbers. Ripgrep-backed with native Go fallback.

### PASS: 3.3 Shared Core Tools - glob
Correctly implements: pattern (required), path (optional, default working dir). Returns sorted by mtime (newest first) via `sort.SliceStable` comparing `ModTime`.

### PASS: 3.4 OpenAI Profile - Tool List
Tools: read_file, apply_patch, write_file, exec_command (shell), grep_files (grep), list_dir (glob), spawn_agent, send_input, wait, close_agent, task_list, web_fetch, communicate, use_skill. Matches spec plus extras (task_list, web_fetch, communicate, use_skill are beyond-spec extensions).

### PASS: 3.4 OpenAI Profile - apply_patch
Correctly has the v4a patch format tool. System prompt includes full grammar, examples, and format specification (verified by TestOpenAIProfile_SystemPromptContainsApplyPatchFormat).

### PASS: 3.4 OpenAI Profile - Name Mappings
shell -> exec_command, grep -> grep_files, glob -> list_dir. Correct.

### PASS: 3.4 OpenAI Profile - Default Timeout
10s (10_000ms) as specified. Verified in profile.go:213.

### PASS: 3.4 OpenAI Profile - Provider Options / Reasoning Effort
`reasoning.effort` is passed through via `req.ReasoningEffort` on the Responses API. The OpenAI adapter sets `body["reasoning"] = map[string]any{"effort": *req.ReasoningEffort}`. Tested in session_dod_test.go and adapter_test.go.

### PASS: 3.4 OpenAI Profile - apply_patch does NOT appear in Anthropic/Gemini
OpenAI has apply_patch; Anthropic and Gemini do not. Verified by tests.

### PASS: 3.5 Anthropic Profile - edit_file is native
Anthropic uses edit_file (not apply_patch). System prompt covers old_string uniqueness, read-before-edit, and prefer editing over creating.

### PASS: 3.5 Anthropic Profile - Shell 120s Default
`defaultTimeout: 120_000` at profile.go:249. Correct per spec.

### PASS: 3.5 Anthropic Profile - Provider Options / Beta Headers
`providerOpts` includes `anthropic.beta_headers` with extended thinking and prompt caching headers. Verified at profile.go:251-255 and by test.

### PASS: 3.5 Anthropic Profile - Canonical Tool Names
Anthropic uses canonical names (shell, grep, glob) with no name mapping. Verified by TestToolNameMapping_Anthropic_NoMapping.

### PASS: 3.6 Gemini Profile - read_many_files
Present in Gemini profile (profile.go:305). Executor handles batch reading with per-file error handling.

### PASS: 3.6 Gemini Profile - list_dir / list_directory
Present in Gemini profile. Mapped from list_dir to list_directory. Supports depth parameter.

### PASS: 3.6 Gemini Profile - web_search / web_fetch
Both present in Gemini profile. web_search uses a separate grounding call to avoid the google_search + functionDeclarations conflict.

### PASS: 3.6 Gemini Profile - System Prompt
References GEMINI.md, uses mapped tool names (run_shell_command, grep_search, list_directory).

### PASS: 3.6 Gemini Profile - Safety Settings
`provider_options.gemini.safetySettings` configured with 4 harm categories, all using BLOCK_ONLY_HIGH threshold.

### PASS: 3.6 Gemini Profile - Name Mappings
shell -> run_shell_command, grep -> grep_search, list_dir -> list_directory. Verified by tests.

### PASS: 3.7 Custom Tool Registration - Latest Wins
TestToolRegistry_Register_LatestWinsOnNameCollision confirms latest-wins behavior.

### PASS: 3.8 Tool Registry - Structure
`ToolRegistry` has: `_tools` map, `Register`, `Unregister`, `Get`, `Definitions`, `Names`. RegisteredTool has definition + executor. ToolDefinition has name, description, parameters (JSON Schema, root must be "object").

### PASS: 3.8 Tool Execution Pipeline
ExecuteCall implements: LOOKUP (tool map lookup), VALIDATE (JSON Schema validation), EXECUTE (executor call), TRUNCATE (char/line truncation), EMIT (via execTool emitting TOOL_CALL_START/END events), RETURN (truncated output as ToolExecResult).

### PASS: 3.8 Tool Registry - Root Schema Must Be Object
compileSchema rejects non-"object" root types. Verified by TestToolRegistry_Register_RejectsNonObjectRootSchema.

---

## Gaps

### GAP-3.01: ProviderProfile Does Not Own a ToolRegistry

**Status**: STRUCTURAL DEVIATION
**Spec**: Section 3.2 says `tool_registry: ToolRegistry` is a field on ProviderProfile.
**Code**: The ProviderProfile interface (profile.go:35-53) has no `ToolRegistry()` method. Instead, the Session owns the `*ToolRegistry` (session.go:115). Profiles only carry `[]llm.ToolDefinition` (tool definitions), not executable registered tools.
**Impact**: The architecture works but differs structurally from the spec. Custom tools registered on the Session's registry are correctly reflected in tool execution but not directly associated with the profile. Tool registration happens at session init via `registerCoreTools(reg, s)` rather than on the profile itself.
**Evidence**: profile.go:35-53 has `ToolDefinitions() []llm.ToolDefinition` but no `ToolRegistry()`. session.go:115 has `reg *ToolRegistry`. The spec pseudocode shows `profile.tool_registry.register(...)` for extending profiles.

### GAP-3.02: Anthropic context_window_size Is 200K, Not 1M

**Status**: POTENTIAL DEVIATION
**Spec**: Section 3.5 references "1M context" in the context of beta headers. The `context_window_size` field is specified in Section 3.2 as an Integer on the profile.
**Code**: `contextWindow: 200_000` at profile.go:244.
**Impact**: The standard Anthropic API window is 200K tokens. The 1M context requires specific beta headers and is model-dependent (only certain models support it). The code does pass the beta headers. However, if the beta headers enable 1M context, the `context_window_size` should arguably reflect the actual window being used (1M = 1_000_000), since the context management code (Section 2) uses this value for pressure calculations and warnings.
**Evidence**: profile.go:244 sets `contextWindow: 200_000`. The beta header "prompt-caching-2024-07-31" is present but the extended-context beta that unlocks 1M is not explicitly configured beyond the thinking header.

### GAP-3.03: grep output_mode Is Global, Not Anthropic-Specific

**Status**: MINOR DEVIATION
**Spec**: Section 3.3 (shared core grep) does NOT list `output_mode` as a parameter. Section 3.5 (Anthropic profile) specifies "ripgrep-backed with output modes: content, files_with_matches, count" as an Anthropic-specific feature.
**Code**: `defGrep()` (profile.go:441-463) includes `output_mode` with enum `["content", "files_with_matches", "count"]` globally for ALL profiles. All three profiles share the same grep definition.
**Impact**: Low. Having output_mode on all profiles is arguably better than restricting it to Anthropic only. This is an additive deviation that provides more functionality than specified.
**Evidence**: profile.go:454-458 defines output_mode in defGrep() used by all profiles. The spec Section 3.3 grep spec does not include output_mode; Section 3.5 says it's an Anthropic feature.

### GAP-3.04: Gemini Profile Shell Default Timeout Should Be 10s Per Spec

**Status**: PASS (confirming compliance)
**Spec**: Section 3.6 says "shell (command execution, 10s default timeout)".
**Code**: `defaultTimeout: 10_000` at profile.go:285. Correct.
**Evidence**: This is correctly implemented; noted here for completeness of the Gemini profile audit.

### GAP-3.05: OpenAI Profile Missing edit_file in Tool Definitions

**Status**: CORRECT BY DESIGN
**Spec**: Section 3.4 says apply_patch replaces edit_file for modifications. write_file kept for new files.
**Code**: OpenAI profile includes read_file, apply_patch, write_file, shell, grep, glob. Does NOT include edit_file. However, `registerCoreTools` (session.go:1216-1232) registers edit_file in the ToolRegistry for ALL sessions regardless of profile.
**Impact**: The edit_file tool is registered in the ToolRegistry but NOT in the OpenAI profile's ToolDefinitions sent to the model. This means the model won't see edit_file, which is correct - but the tool IS still executable if called by name. This is harmless since the model won't generate calls to tools it doesn't know about.
**Evidence**: profile.go:220-236 (OpenAI toolDefs list); session.go:1216 (`_ = reg.Register(... defEditFile() ...)`).

### GAP-3.06: ProviderProfile Missing tools() Method Name

**Status**: NAMING DEVIATION
**Spec**: Section 3.2 specifies `FUNCTION tools() -> List<ToolDefinition>`.
**Code**: The method is named `ToolDefinitions()` not `Tools()`.
**Impact**: Cosmetic naming difference. Functionally equivalent.
**Evidence**: profile.go:38: `ToolDefinitions() []llm.ToolDefinition`.

### GAP-3.07: Extra Tools Beyond Spec Tool Lists

**Status**: ADDITIVE - NOT A GAP
**Spec**: Section 3.4/3.5/3.6 list specific tools per profile.
**Code**: All profiles include additional tools not mentioned in the spec profile tool lists: `task_list`, `web_fetch`, `communicate`, `use_skill`. OpenAI and Anthropic also get `read_many_files` and `list_dir` registered in the ToolRegistry (though not in their ToolDefinitions sent to the model).
**Impact**: These are legitimate extensions covered by Section 3.7 (extending profiles with custom tools). The communicate and task_list tools are described in other spec sections (Section 6/9). Not a gap.

### GAP-3.08: Gemini Profile context_window_size Is 128K

**Status**: INFORMATIONAL
**Spec**: Section 3.2 defines context_window_size as Integer. Section 3.6 does not specify a value.
**Code**: `contextWindow: 128_000` at profile.go:280.
**Impact**: Gemini 2.5 Pro supports up to 1M context, Gemini 2.5 Flash supports 1M, Gemini 3 Flash is unclear. The 128K default is conservative. This may cause premature context pressure warnings for models that actually have larger context windows. A per-model override mechanism would be more accurate.
**Evidence**: profile.go:280.

### GAP-3.09: ToolDefinition description Field Is Optional in Go, Required in Spec

**Status**: MINOR DEVIATION
**Spec**: Section 3.8 shows `description: String -- for the LLM` as a field on ToolDefinition.
**Code**: `Description string json:"description,omitempty"` in llm/types.go:171. The `omitempty` tag means empty descriptions are silently accepted without validation.
**Impact**: Low. All built-in tools have descriptions. Only custom or MCP tools could lack descriptions, which would degrade model understanding but not break anything.
**Evidence**: llm/types.go:171. No validation of non-empty description in Register() or ValidateToolName().

### GAP-3.10: OpenAI Profile parallel Tool Calls Set to False

**Status**: INFORMATIONAL
**Spec**: Section 3.2 defines `supports_parallel_tool_calls: Boolean`. Section 3.4 does not specify a value for OpenAI.
**Code**: `parallel: false` at profile.go:207.
**Impact**: OpenAI models (codex-rs) historically execute tools sequentially. This is a deliberate design choice matching the codex-rs reference agent behavior. Not a gap per se, but worth noting since it means OpenAI tool calls are always sequential while Anthropic and Gemini can be parallel.
**Evidence**: profile.go:207; confirmed by TestProviderProfiles_ToolsetsAndDocSelection.

### GAP-3.11: Shell Tool Does Not Return Duration in Tool Output

**Status**: COMPLIANT
**Spec**: Section 3.3 shell says "returns: Command output (stdout + stderr), exit code, duration".
**Code**: The shell executor (session.go:1265) includes `exit_code=N duration_ms=N timed_out=T` in the output string.
**Evidence**: session.go:1265: `fmt.Sprintf("exit_code=%d duration_ms=%d timed_out=%t\n", ...)`.

### GAP-3.12: OpenAI Profile System Prompt Says "serf" Not "codex-rs"

**Status**: INTENTIONAL DEVIATION
**Spec**: Section 3.4 says "System prompt should mirror the codex-rs system prompt structure."
**Code**: system.openai.md line 1: "You are serf, a non-interactive coding agent (OpenAI profile)."
**Impact**: The spec says "should mirror" the structure, not copy it verbatim. The system prompt covers apply_patch format, tool usage, and coding practices as specified. Using the "serf" identity is intentional branding.
**Evidence**: prompts/system.openai.md:1.

### GAP-3.13: OpenAI write_file Still Available (Not Just for New Files)

**Status**: MINOR DEVIATION
**Spec**: Section 3.4 says "write_file (kept for creating new files without patch overhead)."
**Code**: write_file is available for both OpenAI and Anthropic. The write_file executor does not enforce new-file-only semantics for OpenAI. It has a `readBeforeWriteWarning` that warns if the file exists and hasn't been read, but this applies to all profiles and doesn't prevent overwriting.
**Impact**: Low. The model's system prompt says "Use only for new files" which provides behavioral guidance. Enforcement is soft (warning) rather than hard (error).
**Evidence**: session.go:1199-1213 (write_file executor); prompts/system.anthropic.md:20 ("Use only for new files"); no per-profile enforcement logic.

### GAP-3.14: Gemini Profile Identifies as "gemini" but LLM Client Uses "google"

**Status**: INFORMATIONAL (HANDLED)
**Spec**: Section 3.2 says profile id should be "gemini".
**Code**: Profile ID is "gemini" (profile.go:277). The LLM client has `normalizeProviderName` that maps "gemini" to "google" (client.go:177-179).
**Impact**: None - the normalization layer handles this correctly. The profile correctly identifies as "gemini" while the adapter uses "google" internally. All tool calls route correctly.
**Evidence**: profile.go:277; client.go:175-183.

### GAP-3.15: Gemini Profile Grounding Not Configured via provider_options

**Status**: MINOR DEVIATION
**Spec**: Section 3.6 says "Gemini profile should configure safety settings and grounding via `provider_options.gemini`."
**Code**: Safety settings ARE configured in provider_options.gemini (profile.go:287-294). However, grounding is not configured via provider_options. Instead, web search/grounding is handled via the session-level `WebSearch: true` flag on LLM requests (session.go:830) and via the explicit `web_search` tool (tool_web_search.go).
**Impact**: Low. The grounding functionality works, just through a different mechanism than provider_options. The spec's reference to "grounding via provider_options.gemini" is ambiguous about exactly what should go there.
**Evidence**: profile.go:287-294 has safety but no grounding config. tool_web_search.go and session.go:830 handle grounding differently.

---

## Summary Table

| ID | Title | Severity | Status |
|----|-------|----------|--------|
| GAP-3.01 | ProviderProfile does not own a ToolRegistry | Structural | Medium |
| GAP-3.02 | Anthropic context_window_size is 200K not 1M | Potential | Medium |
| GAP-3.03 | grep output_mode is global not Anthropic-specific | Minor | Low |
| GAP-3.05 | edit_file registered in ToolRegistry for all profiles but not in OpenAI ToolDefinitions | By Design | None |
| GAP-3.06 | Method named ToolDefinitions() not tools() | Naming | None |
| GAP-3.08 | Gemini context_window_size conservative at 128K | Informational | Low |
| GAP-3.09 | ToolDefinition description not validated as non-empty | Minor | Low |
| GAP-3.10 | OpenAI parallel tool calls disabled | Informational | None |
| GAP-3.12 | System prompt says "serf" not "codex-rs" | Intentional | None |
| GAP-3.13 | write_file not restricted to new files only for OpenAI | Minor | Low |
| GAP-3.14 | Profile "gemini" vs adapter "google" naming | Handled | None |
| GAP-3.15 | Grounding not in provider_options.gemini | Minor | Low |

# Section 3: Provider-Aligned Toolsets - Audit Findings

## Summary
14 gaps found (2 Critical, 4 Important, 6 Minor, 2 Info)

## Findings

### GAP-3.01: ProviderProfile missing `tool_registry` field
- **Spec requirement:** (Section 3.2) `tool_registry : ToolRegistry -- all tools available to this profile`
- **Current state:** The `ProviderProfile` interface has no `tool_registry` field or `ToolRegistry` reference. The ToolRegistry lives on the `Session` struct and is populated by `registerCoreTools()` during session construction. The profile only carries `ToolDefinitions()` (schema definitions) but not executors.
- **Severity:** Important
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (lines 35-53), `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 99-144, 209-235)
- **Notes:** The spec envisions the profile *owning* its registry so custom tools can be registered directly on the profile (see Section 3.7 example: `profile.tool_registry.register(...)`). The current architecture decouples definitions (profile) from execution (session registry). While functionally equivalent, this means the Section 3.7 pattern of extending a profile's registry before session creation is not possible.

### GAP-3.02: System prompts are not 1:1 copies of provider reference agents
- **Spec requirement:** (Section 3.1) "The initial base for each provider should be a 1:1 copy of the provider's reference agent -- the exact same system prompt, the exact same tool definitions, byte for byte."
- **Current state:** System prompts are short, custom summaries. The OpenAI prompt (`system.openai.md`) is 67 lines focused on apply_patch format. The Anthropic prompt (`system.anthropic.md`) is 23 lines. The Gemini prompt (`system.gemini.md`) is 27 lines. None are byte-for-byte copies of their respective reference agents (codex-rs, Claude Code, gemini-cli). They are original prose that covers similar topics but with substantially different wording, structure, and length.
- **Severity:** Critical
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/prompts/system.openai.md`, `/Users/jesse/prime-radiant/serf/internal/agent/prompts/system.anthropic.md`, `/Users/jesse/prime-radiant/serf/internal/agent/prompts/system.gemini.md`, `/Users/jesse/prime-radiant/serf/internal/agent/prompts/base.md`
- **Notes:** The spec is emphatic: "Not a similar prompt. Not similar tools. The actual prompt and harness that the model was evaluated and optimized against." The current prompts cover the right topics but are substantially shorter and differently structured than the reference agents' system prompts.

### GAP-3.03: Anthropic profile does not pre-configure beta headers
- **Spec requirement:** (Section 3.5) "The Anthropic profile should pass beta headers (e.g., for extended thinking, 1M context) via `provider_options.anthropic.beta_headers`."
- **Current state:** The Anthropic profile's `providerOpts` is initialized as an empty map (`map[string]any{}`). The Anthropic LLM adapter does support reading `beta_headers` from `provider_options.anthropic`, but no default beta headers are set. Extended thinking and 1M context beta features are not enabled by default.
- **Severity:** Important
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (line 251), `/Users/jesse/prime-radiant/serf/internal/llm/providers/anthropic/adapter.go` (lines 144-186, 760-780)
- **Notes:** The mechanism exists in the adapter layer. The gap is that the profile does not pre-populate any beta headers. Users would need to pass them through SessionConfig or some other mechanism.

### GAP-3.04: Anthropic grep tool missing output mode parameter
- **Spec requirement:** (Section 3.5) "grep (ripgrep-backed with output modes: content, files_with_matches, count)"
- **Current state:** The `defGrep()` function defines parameters: `pattern`, `path`, `glob_filter`, `case_insensitive`, `max_results`. There is no `output_mode` parameter. The grep implementation always returns matching lines with file paths and line numbers (content mode only).
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (lines 437-454), `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go` (lines 297-345, 347-409)
- **Notes:** The spec mentions output modes specifically for the Anthropic profile's grep, mirroring Claude Code's ripgrep tool behavior. The current implementation only supports the equivalent of "content" mode.

### GAP-3.05: apply_patch description differs from spec
- **Spec requirement:** (Section 3.4) `description: "Apply code changes using the patch format. Supports creating, deleting, and modifying files in a single operation."`
- **Current state:** Code has `description: "Apply code changes using the v4a patch format."` -- missing the capability summary ("Supports creating, deleting, and modifying files in a single operation.").
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (lines 472-485)

### GAP-3.06: edit_file fuzzy matching missing Unicode equivalence
- **Spec requirement:** (Section 3.3, edit_file) "the implementation may attempt fuzzy matching (whitespace normalization, Unicode equivalence)"
- **Current state:** `findFuzzyMatch()` only performs whitespace normalization via `normalizeWS()` (which uses `strings.Fields` to collapse whitespace). No Unicode equivalence normalization (e.g., NFC/NFD normalization, combining character handling) is implemented.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go` (lines 169-193), `/Users/jesse/prime-radiant/serf/internal/agent/apply_patch.go` (lines 301-303)
- **Notes:** The spec uses "may attempt" so this is technically optional, but it's listed as an expected behavior.

### GAP-3.07: ToolDefinition schema root type not validated as "object"
- **Spec requirement:** (Section 3.8) `parameters : Dict -- JSON Schema (root must be "object")`
- **Current state:** The `compileSchema()` function in `tool_registry.go` compiles whatever JSON schema is provided without checking that the root `type` is `"object"`. If a tool registers with a non-object root type (e.g., `"string"`), it would be accepted.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go` (lines 253-270)

### GAP-3.08: Gemini system prompt uses wrong tool name for list_dir
- **Spec requirement:** (Section 3.6) The Gemini profile's tool list includes `list_dir` which gets mapped to `list_directory`.
- **Current state:** The Gemini system prompt (`system.gemini.md` line 20) refers to the tool as `list_dir`, but after name mapping the tool is exposed to the model as `list_directory`. The model will see `list_directory` in the tool definitions but `list_dir` in the system prompt instructions. Also, the `BuildSystemPrompt()` method generates a "Tools:" section that lists the mapped names (e.g., `list_directory`), so there's a contradiction within the prompt itself.
- **Severity:** Important
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/prompts/system.gemini.md` (line 20), `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (lines 176-183, 293-297)
- **Notes:** The base prompt text says `list_dir` but the auto-generated tool listing at the end of `BuildSystemPrompt()` will say `list_directory`. The model sees conflicting names.

### GAP-3.09: OpenAI profile missing reasoning.effort in provider_options
- **Spec requirement:** (Section 3.4) "The OpenAI profile should set `reasoning.effort` on the Responses API request when `reasoning_effort` is configured."
- **Current state:** The reasoning effort is passed through `req.ReasoningEffort` field on the LLM request, not through `provider_options`. The OpenAI adapter reads `req.ReasoningEffort` and translates it to `{"reasoning": {"effort": ...}}` in the API body. The OpenAI profile has no `providerOpts` configured (nil).
- **Severity:** Info
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 779-782), `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (lines 203-237), `/Users/jesse/prime-radiant/serf/internal/llm/providers/openai/adapter.go` (lines 126-127, 233-234)
- **Notes:** The functionality works correctly via the `ReasoningEffort` field on `llm.Request` rather than through `provider_options`. This is an architectural difference from the spec's suggestion but achieves the same result. The spec says "should set `reasoning.effort`" which the implementation does, just via a different mechanism.

### GAP-3.10: Tool execution pipeline missing explicit VALIDATE step for JSON Schema
- **Spec requirement:** (Section 3.8) The execution pipeline step 2 is "VALIDATE -- parse and validate arguments against JSON Schema"
- **Current state:** The implementation does perform JSON Schema validation (`t.Schema.Validate(args)` in `ExecuteCall` at line 150), so this is implemented. However, there is no explicit enforcement that the schema root type must be `"object"` during tool registration (see GAP-3.07).
- **Severity:** Info
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go` (lines 124-169)
- **Notes:** This is really a sub-aspect of GAP-3.07. The validation step itself works correctly.

### GAP-3.11: OpenAI tool list has extra tools beyond spec
- **Spec requirement:** (Section 3.4) OpenAI profile tool list: `read_file, apply_patch, write_file, shell, grep, glob, spawn_agent, send_input, wait, close_agent`
- **Current state:** The OpenAI profile includes 4 additional tools not listed in the spec: `task_list`, `web_fetch`, `communicate`, `use_skill`. Similarly, the Anthropic and Gemini profiles include these extra tools.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (lines 220-236, 252-268, 298-317)
- **Notes:** The spec does not mention `task_list`, `communicate`, `use_skill`, or `web_fetch` (for non-Gemini profiles) anywhere. These are implementation additions that extend the tool set. Section 3.7 allows custom tool registration, and these tools are all reasonable additions. The `communicate` tool appears to be a core non-interactive-mode tool. Not a compliance issue, but worth documenting that the tool lists diverge from the spec's enumeration.

### GAP-3.12: Custom tool registration uses Session, not Profile
- **Spec requirement:** (Section 3.7) `profile.tool_registry.register(RegisteredTool(...))` -- tools registered directly on the profile
- **Current state:** Custom tools (including MCP tools) are registered on the Session's ToolRegistry (`s.reg`), not on the profile. The profile interface has no `Register()` method. MCP tools are discovered and registered during `initMCP()` on the session. The `ProviderProfile` interface only exposes `ToolDefinitions()` (read-only list of schemas) and has no mutable registry.
- **Severity:** Important
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (lines 35-53), `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 1004-1031, 996-1002)
- **Notes:** This is architecturally different from the spec. The spec envisions adding tools to a profile before creating a session; the implementation only supports adding tools after session creation via the session's registry. This means the profile's `ToolDefinitions()` list is immutable after profile creation, and custom tools only appear via the session's `allToolDefinitions()` merge.

### GAP-3.13: Name collision resolution not explicitly "latest-wins"
- **Spec requirement:** (Section 3.7) "Name collisions are resolved by latest-wins: a custom tool with the same name as a profile tool overrides it."
- **Current state:** The `ToolRegistry.Register()` method does use `r.tools[t.Definition.Name] = t` (map assignment) which is effectively "latest-wins" -- registering a tool with the same name as an existing one replaces it. This behavior is correct but implicit rather than explicit. There is no documentation or test verifying this collision resolution behavior.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go` (lines 61-85), `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry_test.go`

### GAP-3.14: Gemini profile web_fetch is not optional
- **Spec requirement:** (Section 3.6) `web_fetch (optional -- fetch and extract content from URLs)` and `web_search (optional -- Gemini models have native grounding capabilities)`
- **Current state:** Both `web_fetch` and `web_search` are unconditionally included in the Gemini profile's tool list. The spec marks them as "optional" which implies they should be toggleable. Additionally, `web_fetch` is also included in the OpenAI and Anthropic profiles, but the spec only lists it for Gemini.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (lines 298-317, 220-236, 252-268)
- **Notes:** Making these optional would require a profile configuration mechanism. The current unconditional inclusion is pragmatic but doesn't match the spec's "optional" annotation.

## Fully Implemented (Verified)

### Section 3.2: ProviderProfile Interface
- `id` field: Implemented via `ID() string` -- returns "openai", "anthropic", "gemini"
- `model` field: Implemented via `Model() string`
- `build_system_prompt(environment, project_docs)`: Implemented via `BuildSystemPrompt(env EnvironmentInfo, docs []ProjectDoc, skills []SkillMeta) string`
- `tools()`: Implemented via `ToolDefinitions() []llm.ToolDefinition`
- `provider_options()`: Implemented via `ProviderOptions() map[string]any`
- `supports_reasoning`: Implemented via `SupportsReasoning() bool` -- true for all profiles
- `supports_streaming`: Implemented via `SupportsStreaming() bool` -- true for all profiles
- `supports_parallel_tool_calls`: Implemented via `SupportsParallelToolCalls() bool` -- false for OpenAI, true for Anthropic/Gemini
- `context_window_size`: Implemented via `ContextWindowSize() int` -- 128k OpenAI, 200k Anthropic, 128k Gemini

### Section 3.3: Shared Core Tools

**read_file:**
- `file_path` (required): Implemented
- `offset` (optional, 1-based): Implemented (1-based in `env_local.go` line 101)
- `limit` (optional, default 2000): Implemented (default 2000 in `env_local.go` line 104)
- Line-numbered output format "NNN | content": Implemented (`"%4d | %s\n"` in `env_local.go` line 117)
- Image support: Implemented (`detectImageFormat` + base64 encoding in `env_local.go` lines 89-91)
- Binary file error: Implemented (`env_local.go` lines 94-96)

**write_file:**
- `file_path` (required), `content` (required): Implemented
- Creates parent directories: Implemented (`os.MkdirAll` in `env_local.go` line 124)
- Returns bytes written: Implemented (`"wrote %d bytes to %s"` in `env_local.go` line 130)

**edit_file:**
- `file_path` (required), `old_string` (required), `new_string` (required): Implemented
- `replace_all` (optional, default false): Implemented
- Fuzzy matching fallback (whitespace normalization): Implemented in `findFuzzyMatch()`
- Uniqueness check when `replace_all=false`: Implemented (`env_local.go` lines 150-151)
- Returns replacement count: Implemented (`"edited %s: %d replacement(s)"`)
- File not found error: Implemented
- old_string not found error: Implemented
- old_string not unique error: Implemented

**shell:**
- `command` (required), `timeout_ms` (optional), `description` (optional): Implemented
- Process group: Implemented (`Setpgid: true` in `env_local.go` line 426)
- SIGTERM on timeout: Implemented (`terminateProcessGroup` in `env_local.go` line 457)
- 2-second wait then SIGKILL: Implemented (`env_local.go` lines 458-468)
- Returns stdout + stderr, exit code, duration: Implemented
- Environment variable filtering: Implemented (`filteredEnvWithPolicy` in `env_local.go` line 427)

**grep:**
- `pattern` (required): Implemented
- `path` (optional, default working dir): Implemented
- `glob_filter` (optional): Implemented
- `case_insensitive` (optional): Implemented
- `max_results` (optional, default 100): Implemented (default 100 in `env_local.go` lines 328-330, 357-358, and `session.go` line 1219)

**glob:**
- `pattern` (required): Implemented
- `path` (optional, default working dir): Implemented
- Sorted by mtime newest first: Implemented (`env_local.go` lines 283-293)

### Section 3.4: OpenAI Profile
- `apply_patch` replaces `edit_file`: Implemented -- OpenAI profile has `apply_patch`, no `edit_file`
- Tool name mappings: `shell` -> `exec_command`, `grep` -> `grep_files`, `glob` -> `list_dir`: Implemented
- 10s default timeout: Implemented (`defaultTimeout: 10_000`)
- System prompt covers apply_patch format: Implemented -- v4a grammar, envelope syntax, examples all present
- `reasoning.effort` setting: Implemented via `req.ReasoningEffort` (functional, different mechanism than spec suggests)

### Section 3.5: Anthropic Profile
- `edit_file` as native format: Implemented -- Anthropic profile has `edit_file`, no `apply_patch`
- 120s default timeout: Implemented (`defaultTimeout: 120_000`)
- System prompt covers edit_file guidance: Implemented -- uniqueness, read-first, edit-over-create guidance present
- No tool name mapping (uses canonical names): Implemented

### Section 3.6: Gemini Profile
- `read_many_files`: Implemented
- `list_dir`: Implemented (`defListDir()` with depth parameter)
- `web_search` / `web_fetch`: Implemented
- Tool name mappings: `shell` -> `run_shell_command`, `grep` -> `grep_search`, `list_dir` -> `list_directory`: Implemented
- 10s default timeout: Implemented (`defaultTimeout: 10_000`)
- Safety settings via provider_options: Implemented (`providerOpts` has `gemini.safetySettings`)
- System prompt mentions GEMINI.md: Implemented

### Section 3.8: Tool Registry
- `ToolDefinition` with name, description, parameters: Implemented (`llm.ToolDefinition`)
- `RegisteredTool` with definition and executor: Implemented
- `ToolRegistry` with `register()`, `unregister()`, `get()`, `definitions()`, `names()`: All implemented
- Execution pipeline LOOKUP: Implemented (map lookup in `ExecuteCall`)
- Execution pipeline VALIDATE: Implemented (JSON parse + schema validation)
- Execution pipeline EXECUTE: Implemented (executor call)
- Execution pipeline TRUNCATE: Implemented (`truncateResult` with char/line limits)
- Execution pipeline EMIT: Implemented (TOOL_CALL_END event with full output in `execTool`)
- Execution pipeline RETURN: Implemented (truncated output as ToolExecResult)

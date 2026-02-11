# Section 8: Out of Scope (Nice-to-Haves) - Audit Findings

## Summary

6 features checked, 4 fully implemented, 1 partially implemented, 1 not implemented.

The spec explicitly lists these as "nice-to-haves" that are "intentionally excluded from this core spec" but have "natural extension points." Despite that framing, the codebase has implemented 4 of the 6 features to a high degree of completeness, and partially implemented a 5th.

## Findings

### FEATURE-8.01: MCP (Model Context Protocol)

- **Spec description:** "An MCP client can extend the agent with tools from external servers (GitHub, databases, Slack, etc.). The tool registry supports registering MCP-discovered tools with namespaced names (e.g., `github__create_pr`). This is a natural extension but not a core requirement for a functional coding agent."
- **Implementation status:** Fully Implemented
- **What exists:**
  - **MCP Client:** Full implementation in `internal/agent/mcp_manager.go` using the official `github.com/modelcontextprotocol/go-sdk/mcp` v1.3.0 SDK. The `MCPManager` struct manages connections to external MCP servers.
  - **Tool Discovery:** `NewMCPManager()` connects to each configured server, calls `session.ListTools()` to discover available tools, and converts their input schemas to `llm.ToolDefinition` format.
  - **Namespaced Names:** Tools are namespaced as `servername__toolname` (double underscore separator). The `sanitizeToolName()` function replaces hyphens with underscores for LLM compatibility. Original MCP tool names are stored in `origNames` map for `CallTool` dispatch.
  - **Name Validation:** Namespaced names are validated via `llm.ValidateToolName()` with a 64-character length limit enforced. Collision detection prevents MCP tools from shadowing existing registered tools.
  - **Transports:** All three transport types are supported:
    - `stdio`: Uses `mcp.CommandTransport` wrapping `exec.Command`. Supports env var merging.
    - `sse`: Uses `mcp.SSEClientTransport` with `Endpoint` field. Supports custom headers via `headerRoundTripper`.
    - `http`: Uses `mcp.StreamableClientTransport` with `Endpoint` field. Supports custom headers.
  - **Config Sources:** 4-layer config discovery in `DiscoverMCPConfigs()`:
    1. Global: `~/.config/serf/mcp.json` (XDG_CONFIG_HOME respected)
    2. Per-project: `.serf/mcp.json` at git root
    3. CLI config files: `--mcp-config` flag
    4. CLI inline specs: `--mcp` flag (format: `name:command args...`)
    Later layers shadow earlier layers by server name.
  - **Env Var Expansion:** `${VAR}` and `${VAR:-default}` syntax in command, args, env, url, and headers fields. Missing vars without defaults produce errors.
  - **Tool Execution:** `RegisterTools()` creates execution closures that call `sess.CallTool()` on the appropriate MCP server session. Results are converted from MCP `CallToolResult` to strings via `mcpResultToString()`, which handles text content, non-text content (JSON marshaled), and error results.
  - **System Prompt Integration:** MCP tool descriptions are appended to the system prompt in `processOneInput()` under an "MCP Tools (from external servers):" header.
  - **Lifecycle:** `MCPManager.Close()` shuts down all server connections. Called from `Session.Close()`.
  - **Testing:** Comprehensive test coverage:
    - Unit tests for config parsing, env expansion, inline spec parsing, merge semantics, transport creation, tool name sanitization
    - In-memory transport tests for tool discovery and invocation (using `mcp.NewInMemoryTransports()`)
    - Multi-server tests verifying namespace isolation
    - Collision detection tests
    - Name length validation tests
    - Full integration test through `Session.ProcessInput()` verifying tools appear in both tool list and system prompt
    - Real MCP server tests using `@modelcontextprotocol/server-everything` (echo, get_sum, get_tiny_image, env passing, annotated messages)
- **What's missing:**
  - The spec uses `github__create_pr` as an example pattern, while the implementation uses double underscore consistently -- this matches perfectly.
  - No pagination support for `ListTools` (the SDK call doesn't paginate; servers with many tools return all at once). This is a minor edge case.
  - No reconnection logic if an MCP server connection drops mid-session. The connection is established once at session init and never re-established.
- **Severity:** Info (feature is fully implemented as described in the spec)
- **Files checked:**
  - `/Users/jesse/prime-radiant/serf/internal/agent/mcp_config.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/mcp_manager.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/mcp_config_test.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/mcp_manager_test.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/mcp_integration_test.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/mcp_real_test.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (initMCP, allToolDefinitions, processOneInput MCP prompt section)

---

### FEATURE-8.02: Skills / Custom Commands

- **Spec description:** "Reusable prompt templates stored as markdown files with YAML frontmatter. Skills standardize common workflows (e.g., `/commit`, `/review-pr`) and can be loaded from project directories or user home. The system prompt layer has a natural insertion point for skill descriptions."
- **Implementation status:** Fully Implemented
- **What exists:**
  - **Skill Format:** Skills are markdown files with YAML frontmatter, stored as `SKILL.md` inside subdirectories of a `skills/` directory. The `internal/frontmatter` package parses `---` delimited YAML frontmatter using `gopkg.in/yaml.v3`.
  - **YAML Frontmatter Fields:**
    - `name` (required): Skill name used for lookup
    - `description` (required): One-line description shown in system prompt
    - `allowed-tools` (optional): List of tool names the skill is allowed to use
  - **Discovery:** `DiscoverSkills()` walks from git root to cwd, scanning `skills/` subdirectories at each level. Skills in deeper directories shadow those in parent directories.
  - **Extra Directories:** `SkillsDirs` in `SessionConfig` allows CLI-specified extra skill directories (`--skills-dir` flag). Extra dirs shadow project skills with the same name.
  - **System Prompt Insertion:** `BuildSystemPrompt()` in `profile.go` includes a `<skills>` section listing all discovered skills with their names and descriptions, formatted as `- name: description`. This is the "natural insertion point" the spec references.
  - **Progressive Disclosure:** Skills are not loaded into context until explicitly activated. The `use_skill` tool loads the full markdown body on demand, emitting an `EventSkillActivated` event.
  - **Tool Definition:** `defUseSkill()` registers a `use_skill` tool with a `skill_name` parameter. It is included in all provider profiles (OpenAI, Anthropic, Gemini).
  - **Body Loading:** `LoadSkillBody()` re-parses the SKILL.md file and returns the body after frontmatter.
  - **Testing:** Thorough test coverage:
    - Discovery from project skills/ directories
    - Shadowing semantics (deeper overrides shallower, extra dirs override project)
    - Missing name/description validation
    - AllowedTools parsing
    - Body loading and return
    - Session integration (system prompt contains skill list, use_skill returns body)
    - Event emission on skill activation
    - Non-git directory support
- **What's missing:**
  - **User Home Directory:** The spec says skills "can be loaded from project directories or user home." The implementation loads from project directories (git root to cwd walk) and extra CLI directories, but does NOT automatically scan a user home skills directory (e.g., `~/.config/serf/skills/`). Loading from user home requires explicit `--skills-dir` flag. This is a minor gap since the spec says "can be loaded" (capability exists via `--skills-dir`) rather than "automatically loaded."
  - **Allowed-tools enforcement:** The `AllowedTools` field is parsed from frontmatter and stored in `SkillMeta`, but there is no code that restricts which tools can be called when a skill is active. The field is metadata only -- it is not enforced at tool execution time.
  - **Compaction integration:** The `summarizeToolResult` function handles `use_skill` results for observation masking, showing `[use_skill: name -> N chars]`.
- **Severity:** Minor (user home auto-discovery gap and allowed-tools non-enforcement)
- **Files checked:**
  - `/Users/jesse/prime-radiant/serf/internal/agent/skills.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/skills_test.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/session_skills_test.go`
  - `/Users/jesse/prime-radiant/serf/internal/frontmatter/frontmatter.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/profile.go` (BuildSystemPrompt skills section)
  - `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (defUseSkill, registerCoreTools)
  - `/Users/jesse/prime-radiant/serf/cmd/serf/main.go` (--skills-dir flag)

---

### FEATURE-8.03: Sandbox / Security Policies

- **Spec description:** "OS-level sandboxing (macOS Seatbelt, Linux Landlock/Seccomp, Windows restricted tokens) constrains file and network access. The `ExecutionEnvironment` abstraction provides a natural hook -- a `SandboxedLocalExecutionEnvironment` could wrap the default environment. For stronger isolation, use `DockerExecutionEnvironment`."
- **Implementation status:** Not Implemented
- **What exists:**
  - The `ExecutionEnvironment` interface in `env.go` provides the abstraction layer the spec references. Any sandbox implementation could implement this interface.
  - `LocalExecutionEnvironment` in `env_local.go` has an `EnvVarPolicy` mechanism that filters sensitive environment variables from child processes (API keys, secrets, tokens, passwords, credentials). Four policies exist: Default (filter sensitive), All, None, CoreOnly.
  - Process group isolation: commands run with `Setpgid: true` and are terminated via process group signals (SIGTERM then SIGKILL).
  - The system prompts (in `.serf/prompts/`) mention "Sandbox and approvals" sections, but these are aspirational prompt text, not actual runtime sandbox enforcement.
- **What's missing:**
  - No macOS Seatbelt (sandbox-exec) integration
  - No Linux Landlock or Seccomp integration
  - No Windows restricted token support
  - No `SandboxedLocalExecutionEnvironment` wrapper
  - No `DockerExecutionEnvironment` implementation
  - No file system access restrictions beyond the working directory convention
  - No network access restrictions
  - No read-only file system boundaries
- **Severity:** Info (spec explicitly lists this as out of scope; the extension point exists as designed)
- **Files checked:**
  - `/Users/jesse/prime-radiant/serf/internal/agent/env.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go`
  - Grep for "sandbox", "Seatbelt", "Landlock", "Seccomp", "Docker" across entire codebase

---

### FEATURE-8.04: Compaction / Context Summarization

- **Spec description:** "Automatic conversation history summarization when approaching context limits. This is a complex feature with significant tradeoffs (information loss, summarization cost, pinned turns). The context window awareness signal (Section 5.5) gives host applications the information they need to implement their own strategy."
- **Implementation status:** Fully Implemented
- **What exists:**
  - **4-Layer Progressive Compaction** in `internal/agent/context_manager.go`:
    1. **Layer 1 - Observation Masking** (threshold: 60%): Replaces old tool result content with one-line summaries. Preserves error results, communicate results, already-masked results, and results where the summary would be longer. Has tool-specific summary formats for read_file, shell, grep, glob, edit_file, write_file, apply_patch, web_fetch, spawn_agent, task_list, use_skill.
    2. **Layer 2 - Thinking Clearing** (threshold: 70%): Replaces old thinking text with `[thinking: N chars]` placeholders. Preserves redacted thinking blocks and already-cleared thinking.
    3. **Layer 3 - Deterministic Checkpoint** (threshold: 80%): Replaces old history with a structured `[CONTEXT CHECKPOINT]` message containing original task, modified files, action counts, and last shell results. Preserves recent turns.
    4. **Layer 4 - LLM Summarization** (threshold: 90%): Calls the cheap model to generate a narrative summary. History text is truncated to ~50k chars for cheap model compatibility. Results in a `[CONTEXT SUMMARY]` message.
  - **Pinned Turns:** `PreserveRecentTurns` (default: 6) protects the most recent turns from all compaction layers.
  - **Safe Cutoff:** `safeCutoff()` adjusts the compaction boundary to avoid orphaned TurnTool turns or consecutive user-role messages after checkpoint/summary.
  - **Pressure Estimation:** Uses actual API-reported `InputTokens` when available (from `RecordInputTokens()`), falling back to char/4 heuristic. Stale measurements are invalidated before running compaction layers to prevent cascade bugs.
  - **Cascade Prevention:** The `lastInputTokens` measurement is reset before any layer runs when above the threshold, ensuring between-layer pressure checks use char/4 (which reflects in-place mutations).
  - **Events:** Each layer emits `EventContextCompaction` with layer name, turn counts, and estimated token counts before/after.
  - **Wired into Session:** `MaybeCompact()` is called in `processOneInput()` before each LLM request. History is copied out of the mutex before compaction to avoid holding the lock during potential Layer 4 LLM calls.
  - **Context Window Awareness:** `maybeWarnContextUsage()` emits `EventWarning` when context usage exceeds ~80%, matching the Section 5.5 signal the spec references.
  - **Testing:** Extensive test coverage (87+ tests in context_manager_test.go):
    - Token estimation for empty history, user turns, tool results, thinking blocks
    - Usage accumulation and thread safety
    - Observation masking: preserves recent turns, skips already-masked, skips errors, preserves communicate, handles short results, preserves assistant turns
    - Thinking clearing: removes old thinking, preserves recent, preserves redacted, handles mixed content
    - Checkpoint: creates valid messages, includes original task, tracks modified files (edit_file, write_file, apply_patch), summarizes actions, preserves recent turns, handles no history, adjusts cutoff for orphaned tool turns, handles repeated checkpoints, deterministic output, includes web_search counts
    - LLM summarization: calls cheap model, replaces old history, preserves recent turns, error graceful fallback, truncates prompt for cheap model
    - MaybeCompact orchestrator: below threshold no action, layer thresholds (observation mask, checkpoint, summarize), emits events, respects sys prompt size, L1 stops cascade, L1+L3 integration, uses API token count for pressure, falls back to char/4 heuristic, resets after compaction
- **What's missing:**
  - The spec says this is "out of scope" and that "the context window awareness signal gives host applications the information they need to implement their own strategy." The codebase goes well beyond this by implementing a full 4-layer compaction system internally rather than leaving it to host applications. This is a superset of what the spec suggests.
  - No external API for host applications to implement their own compaction strategy as an alternative to the built-in one. The built-in compaction always runs. A host application cannot disable or replace it (though the thresholds can be adjusted by modifying the ContextManager).
- **Severity:** Info (implemented beyond what spec called for; no compliance issue)
- **Files checked:**
  - `/Users/jesse/prime-radiant/serf/internal/agent/context_manager.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/context_manager_test.go`
  - `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (MaybeCompact wiring, maybeWarnContextUsage)

---

### FEATURE-8.05: Approval / Permission System

- **Spec description:** "User approval gates for sensitive operations (file writes, shell commands, destructive actions). The tool execution pipeline (Section 3.8) has a natural extension point between VALIDATE and EXECUTE where an approval step can be inserted."
- **Implementation status:** Partially Implemented (prompt-level only, no runtime enforcement)
- **What exists:**
  - **System Prompt Mentions:** Multiple provider system prompts reference approval concepts:
    - OpenAI prompts mention "approval modes" (never, on-failure, untrusted, on-request) and "escalated to the user for approval"
    - Google prompt mentions "User Approval: Obtain user approval for the proposed plan"
    - These are prompt-level guidance to the model, not runtime enforcement mechanisms
  - **Extension Point:** The tool execution pipeline in `session.go` (`execTool()`) calls `s.reg.ExecuteCall()` directly. There is no hook between validation and execution where an approval step could be injected without modifying session.go. However, the `ToolRegistry.ExecuteCall()` function provides a natural interception point.
  - **Event System:** The event system emits `EventToolCallStart` before execution and `EventToolCallEnd` after, which a host application could use to implement external approval (pause between start and end events). But the current design is fire-and-forget -- there is no mechanism to pause execution and wait for host approval.
  - **Communicate Tool:** The `communicate` tool provides a mechanism for the agent to request user input, but this is agent-initiated (the model decides to ask), not a mandatory gate on sensitive operations.
- **What's missing:**
  - No approval callback or middleware in the tool execution pipeline
  - No permission policy configuration (which tools require approval, which are auto-approved)
  - No blocking mechanism between tool call start and execution
  - No concept of "sensitive operations" in the tool registry
  - No approval state machine or approval modes (the prompt text references modes like "never", "untrusted", "on-request" but these are not implemented as runtime concepts)
  - The extension point described in the spec (between VALIDATE and EXECUTE) exists structurally but has no hook for approval injection without code changes
- **Severity:** Info (spec explicitly marks as out of scope; the prompt-level guidance exists but runtime enforcement does not)
- **Files checked:**
  - `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (execTool, processOneInput)
  - `/Users/jesse/prime-radiant/serf/internal/agent/env.go` (ExecutionEnvironment interface)
  - `.serf/prompts/system.openai.md`, `.serf/prompts/system.openai.gpt-5-1.md`, `.serf/prompts/system.openai.gpt-5-2.md`, `.serf/prompts/system.google.md`

---

### FEATURE-8.06: Read-Before-Write Guardrail

- **Spec description:** "Tracking which files have been read and blocking writes to unread files. A heuristic safety net that can be implemented as a tool execution middleware wrapping the execution environment."
- **Implementation status:** Fully Implemented
- **What exists:**
  - **File Tracking:** `Session.readFiles` map tracks all files read during the session (absolute paths). `trackReadFile()` is called from both `read_file` and `read_many_files` tool handlers.
  - **Write Warning:** `readBeforeWriteWarning()` checks if a file has been read before allowing a write. It prepends a `[WARNING: Writing to file that has not been read in this session. Consider reading first.]` message to the tool result.
  - **Coverage:** Applied to both `write_file` and `edit_file` tools. These are the two write tools that take explicit file paths.
  - **New File Exemption:** New files (where `FileExists()` returns false) are exempt from the warning. This correctly handles the case where the agent creates a new file that obviously cannot be read first.
  - **Path Resolution:** `resolveFilePath()` resolves relative paths against the working directory for consistent tracking.
  - **Implementation Location:** Implemented directly in the session tool registration closures, not as middleware wrapping the execution environment as the spec suggests. The result is the same (writes to unread files trigger a warning) but the architecture differs from the spec's suggestion.
  - **Testing:** Three dedicated tests in `session_dod_test.go`:
    - `TestSession_ReadBeforeWrite_WarnsOnUnreadFile`: Verifies warning appears when writing to existing file without reading first
    - `TestSession_ReadBeforeWrite_NoWarningAfterRead`: Verifies no warning after file has been read
    - `TestSession_ReadBeforeWrite_NewFileNoWarning`: Verifies no warning for newly created files
- **What's missing:**
  - **Soft warning, not blocking:** The spec says "blocking writes to unread files." The implementation issues a warning prepended to the tool result but does NOT block the write -- the file is still written. This is arguably a deliberate design choice (a hard block might cause the agent to get stuck), but it deviates from the spec's "blocking" language.
  - **apply_patch not covered:** The `apply_patch` tool writes to files but does NOT have read-before-write checking. Since apply_patch takes a patch string (not a file path directly), tracking would require parsing the patch to extract file paths. The checkpoint code already does this extraction (`*** Update File:` parsing), so the capability exists but is not wired into the guardrail.
  - **Not middleware:** The spec suggests implementation "as a tool execution middleware wrapping the execution environment." The actual implementation is inline in the session tool closures, which achieves the same effect but is less composable.
  - **Compaction handling:** The `summarizeToolResult` function does not preserve read-tracking state across compaction. If a file was read in turns that get compacted away, the `readFiles` map still correctly tracks it since tracking is session-level, not history-level. This is correct behavior.
- **Severity:** Minor (warning instead of blocking; apply_patch not covered)
- **Files checked:**
  - `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (readFiles, trackReadFile, readBeforeWriteWarning, write_file closure, edit_file closure)
  - `/Users/jesse/prime-radiant/serf/internal/agent/session_dod_test.go` (ReadBeforeWrite tests)

---

## Cross-Cutting Observations

1. **Implementation exceeds spec expectations.** The spec frames Section 8 features as "intentionally excluded from this core spec" and describes them as "natural extensions." Despite this, the codebase implements 4 of 6 features to production quality with comprehensive test coverage. This suggests the spec is outdated relative to the implementation state.

2. **Compaction is the most over-delivered feature.** The spec suggests compaction is complex with "significant tradeoffs" and recommends the host application implement its own strategy. Instead, the codebase has a sophisticated 4-layer progressive compaction system with ~90 tests, API token integration, cascade prevention, and safe cutoff handling.

3. **Sandbox is the only truly unimplemented feature.** This is appropriate since OS-level sandboxing is platform-specific and complex. The ExecutionEnvironment interface provides the correct extension point.

4. **Approval is the most underserved feature.** While prompts reference approval modes, there is no runtime mechanism. The event system provides observability but not control. A host application cannot currently pause tool execution to request approval.

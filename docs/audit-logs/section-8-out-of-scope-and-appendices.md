# Audit: Section 8 - Out of Scope & Appendices A/B

**Spec**: `/Users/jesse/prime-radiant/serf/coding-agent-loop-spec.md` (lines 1117-1433)
**Auditor**: Bot
**Date**: 2026-02-11

---

## Summary

Section 8 lists six features intentionally excluded from the core spec but asserts that "the spec's design has natural extension points for each." Appendix A defines the `apply_patch` v4a format grammar and semantics. Appendix B defines tool-level errors, session-level errors, and the graceful shutdown sequence.

| Status   | Count |
|----------|-------|
| PASS     | 19    |
| PARTIAL  | 4     |
| FAIL     | 1     |

---

## Section 8: Out of Scope Extension Points

### GAP-8.01: MCP Extension Point -- Tool registry supports namespaced MCP tools

**Status**: PASS

**Spec text** (8):
> The tool registry supports registering MCP-discovered tools with namespaced names (e.g., `github__create_pr`).

**Evidence**:
- `internal/agent/mcp_manager.go` implements `NewMCPManager` which connects to MCP servers, discovers tools, and namespaces them as `servername__toolname` (line 74).
- `RegisterTools()` registers execution closures into the standard `ToolRegistry` (line 110-153).
- `sanitizeToolName()` replaces hyphens with underscores for LLM compatibility (line 170-172).
- Tool name validation enforces the 64-char limit via `llm.ValidateToolName()` (line 117).
- MCP config discovery supports global, project, CLI, and inline sources (`mcp_config.go` line 230-273).
- The spec's example format `github__create_pr` matches the double-underscore convention used.

This is not just an extension point -- it is a fully implemented feature.

---

### GAP-8.02: Skills / Custom Commands Extension Point -- System prompt has natural insertion point

**Status**: PASS

**Spec text** (8):
> Reusable prompt templates stored as markdown files with YAML frontmatter. [...] The system prompt layer has a natural insertion point for skill descriptions.

**Evidence**:
- `internal/agent/skills.go` implements `SkillMeta` struct with Name, Description, AllowedTools, Dir, and SkillFile fields (lines 13-19).
- `DiscoverSkills()` walks from git root to cwd scanning for `skills/` directories (lines 25-54).
- `parseSkillFile()` reads YAML frontmatter from SKILL.md files (lines 94-126).
- `LoadSkillBody()` returns the markdown body after frontmatter (lines 79-89).
- System prompt includes a `<skills>` section listing discovered skills (`profile.go` lines 168-174).
- A `use_skill` tool is registered that loads full skill instructions on demand (`session.go` lines 1493-1510).
- `ResolveSystemPrompt()` in `prompt_resolver.go` provides layered composition with global + project + CLI append paths.

This is fully implemented, not just an extension point.

---

### GAP-8.03: Sandbox / Security Policies Extension Point -- ExecutionEnvironment provides hook

**Status**: PASS

**Spec text** (8):
> The `ExecutionEnvironment` abstraction provides a natural hook -- a `SandboxedLocalExecutionEnvironment` could wrap the default environment.

**Evidence**:
- `internal/agent/env.go` defines the `ExecutionEnvironment` interface (lines 20-40) with all operations abstracted.
- `internal/agent/env_local.go` provides `LocalExecutionEnvironment` as the default implementation.
- The interface is implementable by external consumers (Section 4.3 of the spec describes Docker, K8s, WASM, SSH variants).
- `EnvVarPolicy` enum already provides sandboxing controls: `EnvPolicyDefault`, `EnvPolicyAll`, `EnvPolicyNone`, `EnvPolicyCoreOnly` (lines 26-31).
- Section 4.4 of the spec describes composable wrappers (logging, read-only) which the Go interface supports directly.

The extension point exists and is well-designed for sandboxing wrappers.

---

### GAP-8.04: Compaction / Context Summarization Extension Point -- Context window awareness signal exists

**Status**: PASS

**Spec text** (8):
> The context window awareness signal (Section 5.5) gives host applications the information they need to implement their own strategy.

**Evidence**:
- `session.go` line 599-627: `maybeWarnContextUsage()` calculates approximate tokens (chars/4), compares to 80% of `profile.ContextWindowSize()`, and emits `EventWarning` with `approx_tokens`, `context_window_size`, and `percent` fields.
- `events.go` line 23: `EventWarning` event kind is defined.
- Events are delivered via the `Events()` channel for host applications to consume.
- Additionally, a full `ContextManager` is implemented in `context_manager.go` with 4-layer progressive compaction (observation masking at 60%, thinking clearing at 70%, checkpoint at 80%, LLM summarize at 90%), which goes beyond the spec's "out of scope" designation.

The extension point exists, and the implementation already includes a full compaction strategy beyond what the spec requires.

---

### GAP-8.05: Approval / Permission System Extension Point -- Pipeline has insertion point between VALIDATE and EXECUTE

**Status**: PARTIAL

**Spec text** (8):
> The tool execution pipeline (Section 3.8) has a natural extension point between VALIDATE and EXECUTE where an approval step can be inserted.

**Evidence**:
- The tool execution pipeline in `ToolRegistry.ExecuteCall()` (`tool_registry.go` lines 135-180) follows the steps: LOOKUP -> VALIDATE (JSON parse + schema validation) -> EXECUTE -> TRUNCATE -> RETURN.
- There is no explicit hook, callback, or middleware mechanism between VALIDATE and EXECUTE within the tool execution pipeline itself.
- The LLM `Client` has middleware support (`middleware.go`) for wrapping `Complete` and `Stream` calls, but this operates at the LLM layer, not the tool execution layer.
- To add an approval step today, a consumer would need to either: (a) wrap the `ToolRegistry` entirely, (b) modify `ExecuteCall`, or (c) wrap individual tool executors in closures that check approval before delegating.

**What's missing**:
- No `ToolMiddleware` or `ToolInterceptor` interface exists that could be inserted between VALIDATE and EXECUTE.
- The `RegisteredTool.Exec` function is the only customization point, but it runs after schema validation, so a consumer could wrap individual tool executors -- this is a workable but inelegant extension point.

**Recommendation**: Add a `ToolMiddleware` interface or a pre-execution hook to `ToolRegistry` that runs after validation but before execution, allowing approval gates to be injected without modifying tool registrations.

---

### GAP-8.06: Read-Before-Write Guardrail Extension Point -- Implementable as tool execution middleware

**Status**: PASS

**Spec text** (8):
> A heuristic safety net that can be implemented as a tool execution middleware wrapping the execution environment.

**Evidence**:
- This is already implemented directly in the session. `session.go` line 142: `readFiles map[string]bool` tracks read files.
- `trackReadFile()` (line 1516-1518) records files read via `read_file` tool.
- `readBeforeWriteWarning()` (line 1522-1531) returns a warning if writing to an existing file that hasn't been read. New file creation is exempt.
- The warning is prepended to `write_file` and `edit_file` tool results (lines 1204-1212, 1225-1231).
- This is implemented as inline logic in tool executors rather than as middleware wrapping the execution environment, but the effect is the same.

The guardrail is fully implemented.

---

## Appendix A: apply_patch v4a Format

### GAP-8.07: Grammar -- Patch envelope markers

**Status**: PASS

**Spec grammar**:
> `patch = "*** Begin Patch\n" operations "*** End Patch\n"`

**Evidence**:
- `apply_patch.go` line 187-188: Checks for `"*** Begin Patch"` at the start (using `TrimSpace` for tolerance).
- Line 196: Checks for `"*** End Patch"` to terminate the patch.
- Line 255: Returns error if `"*** End Patch"` is missing.

---

### GAP-8.08: Grammar -- Add File operation

**Status**: PASS

**Spec grammar**:
> `add_file = "*** Add File: " path "\n" added_lines`
> `added_lines = ("+" line "\n")*`

**Evidence**:
- `apply_patch.go` lines 205-221: Parses `"*** Add File: "` prefix, extracts path, reads lines with `"+"` prefix.
- Lines 216-217: Enforces that all content lines start with `"+"`, returns error otherwise.
- `addFileOp.apply()` (lines 40-56): Creates parent directories, writes content, appends trailing newline.

---

### GAP-8.09: Grammar -- Delete File operation

**Status**: PASS

**Spec grammar**:
> `delete_file = "*** Delete File: " path "\n"`

**Evidence**:
- `apply_patch.go` lines 222-224: Parses `"*** Delete File: "` prefix, extracts path.
- `deleteFileOp.apply()` (lines 62-69): Removes the file. Uses `os.Remove` (ignores errors, which is reasonable for delete-if-exists semantics).

---

### GAP-8.10: Grammar -- Update File with hunks

**Status**: PASS

**Spec grammar**:
> `update_file = "*** Update File: " path "\n" [move_line] hunks`
> `hunks = hunk+`
> `hunk = "@@ " [context_hint] "\n" hunk_lines`
> `hunk_lines = (context_line | delete_line | add_line)+`

**Evidence**:
- `apply_patch.go` lines 225-250: Parses `"*** Update File: "` prefix, optional `"*** Move to: "`, then collects hunks separated by `"@@"` lines.
- `updateFileOp.apply()` (lines 77-178): Processes each hunk with context/delete/add line logic.
- Context lines (space prefix) are verified against original file content (lines 131-138).
- Delete lines (minus prefix) are verified and consumed (lines 139-146).
- Add lines (plus prefix) are appended to output (lines 147-148).

---

### GAP-8.11: Grammar -- Move to (Update + Rename)

**Status**: PASS

**Spec grammar**:
> `move_line = "*** Move to: " new_path "\n"`

**Evidence**:
- `apply_patch.go` lines 228-230: Parses `"*** Move to: "` line if present after `"*** Update File: "`.
- `updateFileOp.apply()` lines 164-176: After applying hunks, renames the file if `moveTo` is set and differs from original path. Creates destination parent directories.
- Test `TestApplyPatch_AddUpdateMoveDelete` in `apply_patch_test.go` verifies update+rename.

---

### GAP-8.12: Grammar -- End of File marker

**Status**: PASS

**Spec grammar**:
> `eof_marker = "*** End of File\n" -- optional, marks end of last hunk`

**Evidence**:
- `apply_patch.go` line 203: `"*** End of File"` is recognized and treated as a no-op (`continue`).
- Test `TestApplyPatch_EndOfFileMarker` verifies patches with the marker are applied correctly.

---

### GAP-8.13: Hunk Matching -- Context hint and fuzzy matching

**Status**: PARTIAL

**Spec text**:
> The `@@` line provides a context hint (typically a function signature or recognizable line near the change). The implementation uses this hint plus the context lines (space-prefixed) to locate the correct position in the file.
> When exact matching fails, the implementation should attempt fuzzy matching (whitespace normalization, Unicode punctuation equivalence) before reporting an error.

**Evidence**:
- `apply_patch.go` line 275-283: `hintFromHunk()` extracts the hint text after `@@`.
- Lines 285-298: `firstAnchor()` finds the first context or delete line in a hunk.
- Lines 94-114: Hunk application uses the hint to narrow the search, then finds the first anchor line.
- Lines 300-303: `normalizeWS()` collapses whitespace runs for fuzzy matching.
- Lines 305-323: `indexOfLine()` tries exact match first, then whitespace-normalized fuzzy match.
- Lines 134, 144: Context and delete line verification also falls back to whitespace-normalized comparison.

**What's missing**:
- **Unicode punctuation equivalence** is not implemented. The spec says "whitespace normalization, Unicode punctuation equivalence" but only whitespace normalization is done. For example, Unicode smart quotes (`\u201c`, `\u201d`) vs ASCII quotes (`"`) would not match. LLMs sometimes produce Unicode punctuation variants that differ from what's in the file.

**Recommendation**: Add a Unicode punctuation normalization pass (e.g., normalize curly quotes to straight quotes, em-dashes to hyphens, etc.) as a third-level fuzzy match fallback.

---

### GAP-8.14: Multi-hunk updates

**Status**: PASS

**Spec text**:
> A single Update File block can contain multiple `@@` hunks.

**Evidence**:
- `apply_patch.go` lines 233-249: Multiple hunks are collected by splitting on `"@@"` lines.
- `updateFileOp.apply()` processes hunks sequentially, advancing `pos` through the original file.
- Test `TestApplyPatch_MultiHunkSingleFile` verifies two hunks in a single update block.

---

### GAP-8.15: Path safety -- No traversal or absolute paths

**Status**: PASS

**Evidence**:
- `safeJoin()` (lines 258-271) rejects absolute paths and `..` path traversal.
- Test `TestApplyPatch_RejectsPathTraversalAndAbsolutePaths` verifies both cases.

---

## Appendix B: Error Handling

### GAP-8.16: Tool-level errors -- All seven types caught and returned as error results

**Status**: PARTIAL

**Spec table**: FileNotFound, EditConflict, ShellExitError, ShellTimeout, PermissionDenied, ValidationError, UnknownTool

**Evidence**:

| Error Type | Implemented | How |
|---|---|---|
| FileNotFound | Yes | `env.ReadFile`/`os.ReadFile` returns `os.ErrNotExist`; error is returned from tool executor, caught by `ExecuteCall` (line 167-175), sent as `is_error=true` result |
| EditConflict | Yes | `env.EditFile` returns "old_string not found" or "old_string not unique" errors; caught by `ExecuteCall` |
| ShellExitError | Yes | `ExecCommand` returns `*exec.ExitError` as the Go `error` for non-zero exit codes. The shell tool returns `(output, err)` to `ExecuteCall`, which detects `err != nil` and marks the result as `is_error=true`. The model receives full stdout+stderr+exit_code in the error result text. |
| ShellTimeout | Yes | Shell tool appends `[ERROR: Command timed out after Xms...]` message (line 1263); `ExecCommand` returns `context.DeadlineExceeded` error, so result is marked `is_error=true` |
| PermissionDenied | Partial | OS-level permission errors (e.g., `os.ErrPermission`) propagate through `ReadFile`/`WriteFile` as generic errors. There is no explicit `PermissionDenied` error type in the agent layer. The error message from the OS will contain "permission denied" text, which gives the model enough information to recover. |
| ValidationError | Yes | `tool_registry.go` lines 150-163: JSON parse errors and schema validation failures return error results with `is_error=true` |
| UnknownTool | Yes | `tool_registry.go` lines 145-148: Unknown tool names return "unknown tool: X" with `is_error=true` |

**What's partially missing**:
- PermissionDenied: No dedicated error type in the agent layer. OS permission errors propagate as generic errors with the OS error message. The spec lists this as a distinct error type, but the implementation treats it as any other tool execution error. The model does receive the "permission denied" text and can react accordingly.

---

### GAP-8.17: Session-level errors -- ProviderError (429) retried with backoff

**Status**: PASS

**Spec text**:
> ProviderError (429) - Yes - Retry with backoff (handled by Unified LLM SDK)

**Evidence**:
- `llm/errors.go` lines 128-129: Status 429 creates `RateLimitError` with `retryable: true`.
- `llm/retry_util.go` line 27-40: `retryableError()` checks `Error.Retryable()`.
- `llm/retry_util.go` lines 50-87: `Retry()` implements exponential backoff with jitter.
- `session.go` lines 840-846: Session uses `llm.Retry()` to wrap `client.Complete()` calls.
- `llm/retry.go` line 26: Default policy has 2 retries, 1s base delay, 60s max delay, 2x backoff, jitter enabled.

---

### GAP-8.18: Session-level errors -- ProviderError (500-503) retried with backoff

**Status**: PASS

**Evidence**:
- `llm/errors.go` lines 130-132: Status 500, 502, 503, 504 create `ServerError` with `retryable: true`.
- Same retry mechanism as GAP-8.17.

---

### GAP-8.19: Session-level errors -- AuthenticationError no retry, session -> CLOSED

**Status**: PASS

**Evidence**:
- `llm/errors.go` lines 113-114: Status 401 creates `AuthenticationError` with `retryable: false`.
- `session.go` lines 855-858: After LLM error, checks `le.Retryable()` and calls `s.Close()` for non-retryable errors.
- `Close()` transitions to `SessionClosed` state.

---

### GAP-8.20: Session-level errors -- ContextLengthError, no retry, session -> CLOSED

**Status**: PASS

**Evidence**:
- `llm/errors.go` lines 125-126: Status 413 creates `ContextLengthError` with `retryable: false`.
- `llm/errors.go` lines 147-148: Message-based classification catches "context length" and "too many tokens" from 400/422 responses.
- `session.go` lines 850-853: Emits `EventWarning` with "Context length exceeded" message.
- `session.go` lines 855-858: Non-retryable error handling calls `s.Close()`.

---

### GAP-8.21: Session-level errors -- NetworkError retried

**Status**: PASS

**Evidence**:
- `llm/sdk_errors.go` lines 36, 49-51: `NetworkError` is defined with `retryable: true`.
- `llm/retry_util.go` lines 37-39: Unknown errors (without `Error` interface) also default to retryable.
- Retry mechanism handles these via the standard `Retry()` function.

---

### GAP-8.22: Session-level errors -- TurnLimitExceeded -> IDLE

**Status**: PASS

**Evidence**:
- `session.go` lines 729-734: MaxTurns exceeded emits `EventTurnLimit`, sets state to `SessionIdle`.
- `session.go` lines 996-1000: MaxToolRoundsPerInput exceeded emits `EventTurnLimit`, sets state to `SessionIdle`.
- Both return an error string but do NOT transition to CLOSED, matching the spec's "IDLE" behavior.

---

### GAP-8.23: Graceful Shutdown Sequence -- Ordering matches spec

**Status**: FAIL

**Spec ordering** (Appendix B):
> 1. Cancel any in-flight LLM stream
> 2. Send SIGTERM to all running command process groups
> 3. Wait 2 seconds
> 4. Send SIGKILL to any remaining processes
> 5. Flush pending events
> 6. Emit SESSION_END event with final state
> 7. Clean up subagents (close_agent on all active subagents)
> 8. Transition session to CLOSED

**Actual ordering** in `session.go` `Close()` (lines 437-481):
1. `s.state = SessionClosed` -- **Step 8 happens FIRST** (line 443)
2. Collect subagents from map
3. `s.cancelFunc()` -- Cancel LLM stream (step 1) (line 459)
4. Emit SESSION_END (step 6) (lines 463-469)
5. Close subagents -- `sub.sess.Close()` for each (step 7) (lines 471-473)
6. Close MCP manager (not in spec)
7. `s.env.Cleanup()` -- SIGTERM + wait 2s + SIGKILL (steps 2-4) (line 479)
8. `close(s.events)` -- close events channel (line 480)

**Deviations**:
1. **State transition happens first** instead of last. The session is marked CLOSED before any cleanup occurs. While this prevents re-entry, it means the session reports CLOSED while processes are still running.
2. **Process killing happens after SESSION_END and subagent cleanup** instead of before. The spec says SIGTERM/SIGKILL (steps 2-4) should happen before flushing events (step 5) and SESSION_END (step 6). In the implementation, `env.Cleanup()` (which does SIGTERM/SIGKILL) runs after SESSION_END emission and subagent closure.
3. **No explicit "flush pending events" step**. The spec says "Flush pending events" (step 5) should happen before SESSION_END. The implementation closes the events channel at the very end, but does not flush buffered events before SESSION_END.

**Recommendation**: Reorder `Close()` to: (1) cancel LLM, (2) env.Cleanup() for SIGTERM/SIGKILL, (3) flush event buffer, (4) emit SESSION_END, (5) close subagents, (6) transition to CLOSED, (7) close events channel. The early state transition to CLOSED can be kept as a guard against re-entry, but the semantic CLOSED transition (as reported in SESSION_END data) should reflect the final step.

---

### GAP-8.24: Graceful Shutdown -- Abort signal triggers Close which kills processes

**Status**: PARTIAL

**Spec text**:
> Abort signal: cancellation stops the loop, kills running processes, and transitions to CLOSED.

**Evidence**:
- `session.go` lines 493-496: When `ProcessInput` detects context cancellation, it calls `s.Close()`.
- `session.go` line 459: `Close()` calls `s.cancelFunc()` to cancel in-flight LLM calls.
- `env_local.go` lines 60-78: `Cleanup()` collects running PIDs, sends SIGTERM, waits 2s, then SIGKILL.
- `env_local.go` lines 486-487: Running PIDs are tracked via `runningPIDs` sync.Map.

**What's partially missing**:
- The abort path through `ProcessInput` calls `s.Close()` which eventually calls `env.Cleanup()`. This does kill processes, but the ordering issue from GAP-8.23 means SESSION_END is emitted before processes are killed.
- The `processOneInput` loop checks for context cancellation at the top of each round (lines 747-752), which means an abort during a long-running tool execution will not interrupt the tool until the current tool call completes. However, `ExecCommand` respects context cancellation (line 494-496 of env_local.go), so shell commands will be interrupted.

---

## Summary Table

| GAP ID | Title | Status |
|--------|-------|--------|
| GAP-8.01 | MCP Extension Point | PASS |
| GAP-8.02 | Skills Extension Point | PASS |
| GAP-8.03 | Sandbox Extension Point | PASS |
| GAP-8.04 | Compaction Extension Point | PASS |
| GAP-8.05 | Approval Extension Point | PARTIAL |
| GAP-8.06 | Read-Before-Write Extension Point | PASS |
| GAP-8.07 | Patch Envelope Markers | PASS |
| GAP-8.08 | Add File Operation | PASS |
| GAP-8.09 | Delete File Operation | PASS |
| GAP-8.10 | Update File with Hunks | PASS |
| GAP-8.11 | Move to (Rename) | PASS |
| GAP-8.12 | End of File Marker | PASS |
| GAP-8.13 | Hunk Matching - Fuzzy | PARTIAL |
| GAP-8.14 | Multi-Hunk Updates | PASS |
| GAP-8.15 | Path Safety | PASS |
| GAP-8.16 | Tool-Level Errors | PARTIAL |
| GAP-8.17 | 429 Retry | PASS |
| GAP-8.18 | 500-503 Retry | PASS |
| GAP-8.19 | AuthenticationError -> CLOSED | PASS |
| GAP-8.20 | ContextLengthError -> CLOSED | PASS |
| GAP-8.21 | NetworkError Retry | PASS |
| GAP-8.22 | TurnLimitExceeded -> IDLE | PASS |
| GAP-8.23 | Graceful Shutdown Ordering | FAIL |
| GAP-8.24 | Abort Signal Kills Processes | PARTIAL |

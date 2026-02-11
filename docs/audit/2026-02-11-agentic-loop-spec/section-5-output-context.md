# Section 5: Tool Output and Context Management - Audit Findings

## Summary
4 gaps found (0 Critical, 1 Important, 2 Minor, 1 Info)

## Findings

### GAP-5.01: Spec says no automatic compaction but implementation has full 4-layer context compaction
- **Spec requirement:** "This is informational only. The agent does NOT perform automatic compaction or summarization (that is out of scope for this spec). The host application can use this signal to implement its own context management strategy." (Section 5.5, lines 962-963)
- **Current state:** The implementation has a full `ContextManager` in `context_manager.go` with 4 progressive compaction layers (observation masking at 60%, thinking clearing at 70%, checkpoint at 80%, LLM summarization at 90%). This is called before every LLM request via `MaybeCompact` in `session.go` line 734. The spec's informational warning at 80% IS also implemented via `maybeWarnContextUsage` (session.go lines 561-589), so the informational behavior is present. The compaction is *additional* functionality beyond the spec.
- **Severity:** Important
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/context_manager.go`, `/Users/jesse/prime-radiant/serf/internal/agent/session.go`
- **Note:** The MEMORY.md states "The implementation IS the spec now", suggesting the spec has been superseded. If so, the spec document should be updated to reflect the 4-layer compaction architecture rather than stating the agent does NOT perform compaction.

### GAP-5.02: SessionConfig.tool_output_limits declared as Map<String, Integer> in spec but implemented as Map<String, ToolOutputLimit>
- **Spec requirement:** `tool_output_limits : Map<String, Integer>  -- per-tool char limits (see Section 5)` (Section 2.2, line 154). Spec pseudocode also references a separate `config.tool_line_limits` field (Section 5.3, line 901) but this field is NOT declared in the SessionConfig record.
- **Current state:** The implementation uses `ToolOutputLimits map[string]ToolOutputLimit` where `ToolOutputLimit` bundles `MaxChars`, `MaxLines`, and `Strategy` into a single struct (session.go line 37, tool_registry.go lines 25-29). This is strictly more capable than the spec. It effectively covers both the spec's `tool_output_limits` (char limits) and the undeclared `tool_line_limits` (line limits), and additionally allows overriding the truncation strategy. The spec only intends char limits to be overridable; the implementation also allows overriding line limits and truncation modes.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go`, `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go`, `/Users/jesse/prime-radiant/serf/coding-agent-loop-spec.md` (lines 148-158, 893-906)
- **Note:** This is a spec inconsistency more than an implementation gap. The spec references `tool_line_limits` in pseudocode (line 901) but never declares it in SessionConfig. The implementation's unified `ToolOutputLimit` struct is arguably a cleaner design.

### GAP-5.03: Extra tool limits defined beyond the spec table
- **Spec requirement:** Section 5.2 (lines 874-883) defines default output size limits for exactly 8 tools: read_file, shell, grep, glob, edit_file, apply_patch, write_file, spawn_agent.
- **Current state:** The implementation's `defaultToolLimit` function (tool_registry.go lines 222-251) defines limits for 5 additional tools not listed in the spec table: `task_list` (20,000 chars, tail), `web_fetch` (20,000 chars, head_tail), `communicate` (5,000 chars, tail), `use_skill` (32,000 chars, tail), and a `default` fallback (20,000 chars, head_tail). All 8 spec-defined tools have correct limits matching the spec exactly.
- **Severity:** Info
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go`
- **Note:** These extra limits are for tools that were added after the spec was written. The fallback default of 20,000 chars is reasonable. No spec-defined limits are wrong.

### GAP-5.04: Spec line 898 implies truncation mode comes from DEFAULT_TRUNCATION_MODES but implementation allows override
- **Spec requirement:** `result = truncate_output(output, max_chars, DEFAULT_TRUNCATION_MODES[tool_name])` (line 898). The mode is always read from the default map, never from config.
- **Current state:** The implementation allows `SessionConfig.ToolOutputLimits` to override the `Strategy` (truncation mode) per tool, in addition to `MaxChars` and `MaxLines`. See session.go lines 228-230 where a non-empty strategy override is applied.
- **Severity:** Minor
- **Files checked:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go` (lines 214-234), `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go`
- **Note:** This is extra flexibility, not missing functionality. The spec-defined default modes are all correctly set.

## Fully Implemented (Verified)

### 5.1 Truncation Algorithm
- **head_tail mode**: First `max_chars/2` characters + warning marker + last `max_chars/2` characters. Implemented correctly in `truncateChars` (tool_registry.go lines 186-203). `headCount = max / 2`, `tailCount = max - headCount`.
- **tail mode**: Warning prepended + last `max_chars` characters. Implemented correctly.
- **head_tail warning text**: `[WARNING: Tool output was truncated. {N} characters were removed from the middle. The full output is available in the event stream. If you need to see specific parts, re-run the tool with more targeted parameters.]` -- matches spec exactly (tool_registry.go line 200).
- **tail warning text**: `[WARNING: Tool output was truncated. First {N} characters were removed. The full output is available in the event stream.]` -- matches spec exactly (tool_registry.go line 194).
- **Full output available in event stream**: `TOOL_CALL_END` event carries `full_output` (untruncated) per spec (session.go lines 518-523). `ToolExecResult` stores both `Output` (truncated) and `FullOutput` (untruncated).
- **Test coverage**: `TestToolRegistry_TruncationMarkers` validates truncation markers are present and output is small.

### 5.2 Default Output Size Limits
All 8 spec-defined tool limits verified to be exact matches:
- read_file: 50,000 chars, head_tail (tool_registry.go line 225)
- shell: 30,000 chars, head_tail (line 227)
- grep: 20,000 chars, tail (line 229)
- glob: 20,000 chars, tail (line 231)
- edit_file: 10,000 chars, tail (line 233)
- apply_patch: 10,000 chars, tail (line 235)
- write_file: 1,000 chars, tail (line 237)
- spawn_agent: 20,000 chars, head_tail (line 239)
- **Overridable via SessionConfig**: Yes, `ToolOutputLimits` in `SessionConfig` is applied in `NewSession` (session.go lines 214-234) with positive-override merge semantics.
- **Test coverage**: `TestDefaultToolLimit_MatchesSpecTable` verifies all 8 defaults (tool_registry_test.go lines 272-295). `TestSession_ToolOutputTruncation_OverridesLimitsAndKeepsFullOutputInEvents` tests config overrides.

### 5.3 Truncation Order
- **Character-based MUST run first**: Yes. In `truncateResult` (tool_registry.go lines 171-184), `truncateChars` is called first, then `truncateLines` is called only if `MaxLines > 0`.
- **Line-based truncation uses head/tail split**: Yes. `truncateLines` (tool_registry.go lines 205-219) always uses head/tail split with `headCount = max / 2`, matching the spec's algorithm.
- **Line-based truncation omitted marker**: `[... {N} lines omitted ...]` matches spec exactly (line 216).
- **Default line limits**: shell=256, grep=200, glob=500, read_file=None (0), edit_file=None (0) -- all correct (tool_registry.go lines 225-237).
- **Test coverage**: `TestToolRegistry_TruncationOrder_CharsFirstThenLines` verifies chars-first-then-lines order. `TestToolRegistry_TruncationLines_UsesHeadTailAndOmittedMarker` verifies head/tail split with correct marker text.

### 5.4 Default Command Timeouts
- **default_command_timeout_ms: 10,000**: Yes, set in `applyDefaults` (session.go line 82) and also as a fallback in `ExecCommand` (env_local.go line 413).
- **max_command_timeout_ms: 600,000**: Yes, set in `applyDefaults` (session.go line 85).
- **Timeout capped to max**: Yes, in the shell tool executor (session.go lines 1164-1166).
- **SIGTERM -> wait 2s -> SIGKILL**: Yes. `ExecCommand` (env_local.go lines 456-469):
  1. `terminateProcessGroup` sends `SIGTERM` to process group (`syscall.Kill(-pid, syscall.SIGTERM)`)
  2. Waits 2 seconds via `time.After(2 * time.Second)`
  3. `killProcessGroup` sends `SIGKILL` to process group (`syscall.Kill(-pid, syscall.SIGKILL)`)
- **Process group creation**: `Setpgid: true` (env_local.go line 426) ensures new process group.
- **Timeout error message**: `[ERROR: Command timed out after {X}ms. Partial output is shown above.\nYou can retry with a longer timeout by setting the timeout_ms parameter.]` matches spec exactly (session.go line 1184).
- **Test coverage**: `TestLocalExecutionEnvironment_ExecCommand_TimesOutAndKillsProcessGroup` tests timeout and exit code 124. `TestExecCommand_SIGTERM_ThenSIGKILL_Escalation` specifically tests that a SIGTERM-trapping process is killed via SIGKILL within ~5 seconds.

### 5.5 Context Window Awareness
- **Track approximate token usage (1 token ~ 4 chars)**: Yes, `maybeWarnContextUsage` computes `approxTokens := float64(totalChars) / 4.0` (session.go line 574).
- **Warning at 80% of context_window_size**: Yes, `threshold := float64(cw) * 0.8` (session.go line 575).
- **Warning message format**: `"Context usage at ~{N}% of context window"` matches spec (session.go line 581).
- **Informational only**: The `maybeWarnContextUsage` function emits a `WARNING` event and returns a boolean; it does NOT trigger any compaction. (Note: the separate `ContextManager.MaybeCompact` does trigger compaction, but that is a separate mechanism -- see GAP-5.01.)
- **Test coverage**: `TestSession_ContextWindowAwareness_EmitsWarningOver80Percent` and `TestSession_ContextWindowAwareness_DoesNotWarnUnderThreshold` (session_dod_test.go lines 560-630).

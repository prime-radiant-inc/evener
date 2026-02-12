# Audit: Section 5 - Tool Output and Context Management

**Auditor:** Bot (Claude Opus 4.6)
**Date:** 2026-02-11
**Spec:** coding-agent-loop-spec.md, lines 839-972
**Codebase:** internal/agent/ (tool_registry.go, session.go, env_local.go, profile.go, events.go)

## Summary

Section 5 is one of the best-implemented sections of the spec. The truncation algorithm (head_tail and tail modes), default output size limits, truncation ordering (chars-first, then lines), timeout handling (SIGTERM -> 2s -> SIGKILL), and context window awareness are all implemented correctly and tested thoroughly. The truncation message wording matches the spec verbatim. There are only minor deviations and a few informational notes.

**Gaps found: 3** (1 low, 2 informational)
**Compliant items: 14+**

---

## Findings

### GAP-5.01: Truncation uses byte length, not character count

**Status:** Low
**Spec requirement (5.1):** The truncation algorithm references `LENGTH(output)` and uses "characters" throughout (e.g., "N characters were removed from the middle").

**Implementation:** `truncateChars()` in `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:197-213` uses Go's `len(s)`, which returns the byte count of the string, not the Unicode character (rune) count.

```go
func truncateChars(s string, max int, strat TruncationStrategy) string {
    if max <= 0 || len(s) <= max {
        return s
    }
    removed := len(s) - max
```

For ASCII-only content (the vast majority of tool output), bytes and characters are identical. For multi-byte UTF-8 content:
1. The "N characters were removed" message would report byte count, not character count.
2. The split point (`s[:headCount]`, `s[len(s)-max:]`) could slice in the middle of a multi-byte rune, producing invalid UTF-8.

**Evidence:** No `utf8.RuneCount` or rune-aware slicing anywhere in `tool_registry.go`.

**Severity:** Low. Tool output is overwhelmingly ASCII (file content, shell output, search results). Multi-byte edge cases are extremely rare in practice.

---

### GAP-5.02: Head/tail split asymmetry for odd max_chars values

**Status:** Informational
**Spec requirement (5.1):** The pseudocode uses `half = max_chars / 2` for both head and tail:
```
half = max_chars / 2
RETURN output[0..half] + marker + output[-half..]
```

**Implementation:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:209-212`:
```go
headCount := max / 2
tailCount := max - headCount
```

When `max` is even, this is identical to the spec. When `max` is odd (e.g., 201), the implementation gives `headCount=100`, `tailCount=101` -- one extra byte in the tail. The spec's pseudocode would give `half=100` for both head and tail, keeping only 200 bytes and losing one byte.

**Severity:** Informational. The implementation is arguably more correct than the spec pseudocode for odd values, since it preserves exactly `max` content bytes. No action needed.

---

### GAP-5.03: No separate `tool_line_limits` config field

**Status:** Informational
**Spec requirement (5.3):** The truncation pipeline pseudocode references two separate config namespaces:
```
max_chars = config.tool_output_limits.get(tool_name, ...)
max_lines = config.tool_line_limits.get(tool_name, ...)
```

**Implementation:** The codebase uses a single `ToolOutputLimits` struct that combines `MaxChars`, `MaxLines`, and `Strategy` into one entry per tool:

```go
// session.go:37
ToolOutputLimits map[string]ToolOutputLimit `json:"tool_output_limits,omitempty"`

// tool_registry.go:25-29
type ToolOutputLimit struct {
    MaxChars int                `json:"max_chars,omitempty"`
    MaxLines int                `json:"max_lines,omitempty"`
    Strategy TruncationStrategy `json:"strategy,omitempty"`
}
```

This achieves the same functionality as separate `tool_output_limits` and `tool_line_limits` maps but with a simpler API. All override capabilities are preserved: a consumer can override chars, lines, and strategy independently per tool.

**Severity:** Informational. The unified struct is a reasonable simplification that preserves full spec functionality. No action needed.

---

## Verified Compliant Items

### 5.1 - Truncation Algorithm (head_tail mode)

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:207-212`

The `truncateChars` function implements the head_tail split correctly:
- Returns output as-is when `len(s) <= max`
- Splits into head (first half) and tail (last half)
- Inserts the WARNING marker between them
- Message wording matches spec verbatim: `"[WARNING: Tool output was truncated. %d characters were removed from the middle. The full output is available in the event stream. If you need to see specific parts, re-run the tool with more targeted parameters.]"`

**Test:** `TestToolRegistry_TruncationMarkers` in tool_registry_test.go

### 5.1 - Truncation Algorithm (tail mode)

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:203-206`

The tail mode correctly keeps the last `max_chars` bytes and prepends the warning:
```go
marker := fmt.Sprintf("[WARNING: Tool output was truncated. First %d characters were removed. The full output is available in the event stream.]\n\n", removed)
return marker + s[len(s)-max:]
```

Message wording matches spec verbatim.

### 5.1 - Full output available via event stream

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go:556-561`

The `TOOL_CALL_END` event carries `full_output` (untruncated), while the truncated output goes to the model via the tool result message.

```go
s.emit(EventToolCallEnd, map[string]any{
    "tool_name":   res.ToolName,
    "call_id":     res.CallID,
    "is_error":    res.IsError,
    "full_output": res.FullOutput,
})
```

**Test:** `TestSession_ToolOutputTruncation_OverridesLimitsAndKeepsFullOutputInEvents` in session_test.go

### 5.2 - Default Output Size Limits (all 8 tools)

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:233-261`

| Tool         | Spec Max (chars) | Impl Max (chars) | Spec Mode | Impl Mode | Match |
|--------------|----------------:|----------------:|-----------|-----------|-------|
| read_file    | 50,000          | 50,000          | head_tail | head_tail | YES   |
| shell        | 30,000          | 30,000          | head_tail | head_tail | YES   |
| grep         | 20,000          | 20,000          | tail      | tail      | YES   |
| glob         | 20,000          | 20,000          | tail      | tail      | YES   |
| edit_file    | 10,000          | 10,000          | tail      | tail      | YES   |
| apply_patch  | 10,000          | 10,000          | tail      | tail      | YES   |
| write_file   | 1,000           | 1,000           | tail      | tail      | YES   |
| spawn_agent  | 20,000          | 20,000          | head_tail | head_tail | YES   |

All 8 spec-required tools match exactly in both limit value and truncation mode.

**Test:** `TestDefaultToolLimit_MatchesSpecTable` in tool_registry_test.go explicitly asserts every value.

### 5.2 - Overridable via SessionConfig

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go:37,227-247`

`SessionConfig.ToolOutputLimits` (JSON: `tool_output_limits`) allows per-tool overrides of `MaxChars`, `MaxLines`, and `Strategy`. Only positive values take effect (partial override).

**Test:** `TestSession_ToolOutputTruncation_OverridesLimitsAndKeepsFullOutputInEvents` and `TestSession_ToolOutputTruncation_CanOverrideLineLimitViaSessionConfig` in session_test.go

### 5.3 - Truncation Order (chars first, then lines)

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:182-195`

```go
func truncateResult(toolName, callID, full string, isErr bool, lim ToolOutputLimit) ToolExecResult {
    out := full
    out = truncateChars(out, lim.MaxChars, lim.Strategy)  // Step 1: chars first
    if lim.MaxLines > 0 {
        out = truncateLines(out, lim.MaxLines)             // Step 2: lines second
    }
```

Character truncation runs before line truncation, exactly as spec requires. This ensures pathological cases (e.g., 10MB single-line CSV) are handled by the char truncation before line truncation sees them.

**Test:** `TestToolRegistry_TruncationOrder_CharsFirstThenLines` in tool_registry_test.go asserts both markers appear.

### 5.3 - Default Line Limits

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:237-244`

| Tool      | Spec Lines | Impl Lines | Match |
|-----------|----------:|----------:|-------|
| shell     | 256       | 256       | YES   |
| grep      | 200       | 200       | YES   |
| glob      | 500       | 500       | YES   |
| read_file | None      | 0 (none)  | YES   |
| edit_file | None      | 0 (none)  | YES   |

### 5.3 - Line Truncation Algorithm (head/tail split)

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/tool_registry.go:216-231`

Line truncation splits lines, computes `headCount = max / 2`, `tailCount = max - headCount`, keeps first and last lines, inserts `"[... N lines omitted ...]"` marker. Matches the spec pseudocode.

**Test:** `TestToolRegistry_TruncationLines_UsesHeadTailAndOmittedMarker` in tool_registry_test.go

### 5.4 - Default Command Timeouts

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go:77-86`

```go
func (c *SessionConfig) applyDefaults() {
    // ...
    if c.DefaultCommandTimeoutMS <= 0 {
        c.DefaultCommandTimeoutMS = 10_000       // Spec: 10,000
    }
    if c.MaxCommandTimeoutMS <= 0 {
        c.MaxCommandTimeoutMS = 600_000           // Spec: 600,000
    }
```

Both values match the spec exactly.

**Test:** `TestSession_ShellTool_UsesDefaultTimeoutAndAllowsOverride` and `TestSession_ShellTool_CapsTimeoutToMaxCommandTimeoutMS` in session_dod_test.go

### 5.4 - Timeout Escalation (SIGTERM -> 2s wait -> SIGKILL)

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/env_local.go:504-518`

```go
if timedOut {
    terminateProcessGroup(cmd.Process.Pid)       // 1. Send SIGTERM
    select {
    case <-done:                                  // Exited gracefully
    case <-time.After(2 * time.Second):           // 2. Wait 2 seconds
        killProcessGroup(cmd.Process.Pid)         // 3. Send SIGKILL
    }
}
```

The `terminateProcessGroup` function sends `syscall.SIGTERM` to the process group (negative PID). The `killProcessGroup` function sends `syscall.SIGKILL`. Both use `Setpgid: true` on the command to isolate the process group.

**Tests:**
- `TestLocalExecutionEnvironment_ExecCommand_TimesOutAndKillsProcessGroup`
- `TestExecCommand_SIGTERM_ThenSIGKILL_Escalation` (traps SIGTERM to verify SIGKILL escalation)
- `TestLocalExecutionEnvironment_ExecCommand_ContextCancel_KillsProcessGroup`

### 5.4 - Timeout Message Format

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go:1263`

```go
b.WriteString(fmt.Sprintf("[ERROR: Command timed out after %dms. Partial output is shown above.\nYou can retry with a longer timeout by setting the timeout_ms parameter.]\n", timeout))
```

Matches the spec format:
```
[ERROR: Command timed out after {X}ms. Partial output is shown above.
You can retry with a longer timeout by setting the timeout_ms parameter.]
```

**Test:** `TestSession_ShellTool_TimeoutAppendsMessageToToolResult` in session_dod_test.go asserts both "Command timed out after 10000ms" and "You can retry with a longer timeout".

### 5.4 - Model Can Override Timeout Per-Call

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go:1240-1245`

```go
timeout := s.cfg.DefaultCommandTimeoutMS
if v, ok := args["timeout_ms"].(float64); ok && int(v) > 0 {
    timeout = int(v)
}
if s.cfg.MaxCommandTimeoutMS > 0 && timeout > s.cfg.MaxCommandTimeoutMS {
    timeout = s.cfg.MaxCommandTimeoutMS
}
```

The model can set `timeout_ms` per-call, capped at `MaxCommandTimeoutMS`.

**Test:** `TestSession_ShellTool_CapsTimeoutToMaxCommandTimeoutMS` asserts that 999999 is capped to 5000.

### 5.5 - Context Window Awareness (80% warning)

**Status:** COMPLIANT
**Evidence:** `/Users/jesse/prime-radiant/serf/internal/agent/session.go:599-627`

```go
func (s *Session) maybeWarnContextUsage(msgs []llm.Message) bool {
    cw := s.profile.ContextWindowSize()
    totalChars := 0
    for _, m := range msgs {
        totalChars += messageCharCount(m)
    }
    approxTokens := float64(totalChars) / 4.0          // 1 token ~ 4 chars
    threshold := float64(cw) * 0.8                      // 80% threshold
    if approxTokens <= threshold {
        return false
    }
    pct := int(math.Round((approxTokens / float64(cw)) * 100.0))
    msg := fmt.Sprintf("Context usage at ~%d%% of context window", pct)
    s.emit(EventWarning, map[string]any{...})
    return true
}
```

- Uses 1 token ~ 4 chars heuristic (spec compliant)
- Emits WARNING event at 80% threshold (spec compliant)
- Informational only, no automatic compaction triggered (spec compliant)

**Tests:**
- `TestSession_ContextWindowAwareness_EmitsWarningOver80Percent`
- `TestSession_ContextWindowAwareness_DoesNotWarnUnderThreshold`

---

## Additional Tools (not in spec table but implemented)

The `defaultToolLimit` function includes limits for tools beyond the 8 listed in the spec table:

| Tool         | Max Chars | Max Lines | Strategy   |
|--------------|----------:|----------:|------------|
| task_list    | 20,000    | 0         | tail       |
| web_fetch    | 20,000    | 0         | head_tail  |
| communicate  | 5,000     | 0         | tail       |
| use_skill    | 32,000    | 0         | tail       |
| (default)    | 20,000    | 0         | head_tail  |

These are additive to the spec and do not conflict with any requirement. The default fallback (20,000 chars, head_tail) applies to MCP tools and any other tools not explicitly listed.

---

## Test Coverage Assessment

The truncation and output management code has excellent test coverage:

| Feature | Test | File |
|---------|------|------|
| Head/tail char truncation | `TestToolRegistry_TruncationMarkers` | tool_registry_test.go |
| Chars-first ordering | `TestToolRegistry_TruncationOrder_CharsFirstThenLines` | tool_registry_test.go |
| Line truncation with marker | `TestToolRegistry_TruncationLines_UsesHeadTailAndOmittedMarker` | tool_registry_test.go |
| Default limits match spec | `TestDefaultToolLimit_MatchesSpecTable` | tool_registry_test.go |
| Override via SessionConfig | `TestSession_ToolOutputTruncation_OverridesLimitsAndKeepsFullOutputInEvents` | session_test.go |
| Line limit override | `TestSession_ToolOutputTruncation_CanOverrideLineLimitViaSessionConfig` | session_test.go |
| Full output in events | `TestSession_ToolOutputTruncation_OverridesLimitsAndKeepsFullOutputInEvents` | session_test.go |
| Cross-provider truncation | `TestParity_ToolOutputTruncation` | session_parity_test.go |
| Timeout handling (SIGTERM) | `TestLocalExecutionEnvironment_ExecCommand_TimesOutAndKillsProcessGroup` | env_local_test.go |
| SIGKILL escalation | `TestExecCommand_SIGTERM_ThenSIGKILL_Escalation` | env_local_test.go |
| Timeout message format | `TestSession_ShellTool_TimeoutAppendsMessageToToolResult` | session_dod_test.go |
| Default timeout values | `TestSession_ShellTool_UsesDefaultTimeoutAndAllowsOverride` | session_dod_test.go |
| Max timeout cap | `TestSession_ShellTool_CapsTimeoutToMaxCommandTimeoutMS` | session_dod_test.go |
| Context window warning | `TestSession_ContextWindowAwareness_EmitsWarningOver80Percent` | session_dod_test.go |
| No warning under threshold | `TestSession_ContextWindowAwareness_DoesNotWarnUnderThreshold` | session_dod_test.go |

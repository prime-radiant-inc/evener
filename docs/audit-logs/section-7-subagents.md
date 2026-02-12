# Section 7: Subagents - Spec Compliance Audit

**Spec file:** `coding-agent-loop-spec.md`, lines 1049-1115
**Codebase scope:** `internal/agent/subagents.go`, `internal/agent/session.go`, `internal/agent/profile.go`, `internal/agent/env_local.go`
**Auditor:** Claude Opus 4.6
**Date:** 2026-02-11

---

## Summary

Section 7 is well-implemented overall. All four spec-mandated tools exist with correct parameter schemas. The core lifecycle (spawn, send, wait, close) works correctly with proper depth limiting. Seven findings identified: two minor deviations in tool descriptions, one spec-extending parameter (`timeout_ms` on `wait`), one naming discrepancy in internal types, one concern about non-LocalExecutionEnvironment fallback, one concern about subagent inheriting MCP config, and one note about `close_agent` not waiting for running goroutines.

---

## Findings

### GAP-7.01: Tool description for `spawn_agent` differs from spec

**Status:** INFORMATIONAL (cosmetic)
**Spec:** `"Spawn a subagent to handle a scoped task autonomously."`
**Actual:** `"Spawn a sub-agent to work on a scoped task."` (profile.go:499)

**Evidence:**
```go
// profile.go:498-499
func defSpawnAgent() llm.ToolDefinition {
    return llm.ToolDefinition{
        Name:        "spawn_agent",
        Description: "Spawn a sub-agent to work on a scoped task.",
```

**Description:** The word "autonomously" is missing from the description and "subagent" is hyphenated as "sub-agent". This is cosmetic and does not affect functionality. The model receives slightly different guidance about the tool's purpose.

---

### GAP-7.02: Tool descriptions for `send_input`, `wait`, `close_agent` differ from spec

**Status:** INFORMATIONAL (cosmetic)
**Spec descriptions:**
- `send_input`: "Send a message to a running subagent."
- `wait`: "Wait for a subagent to complete and return its result."
- `close_agent`: "Terminate a subagent."

**Actual descriptions:**
- `send_input`: "Send input to a sub-agent." (profile.go:516)
- `wait`: "Wait for a sub-agent to finish and return its result." (profile.go:532)
- `close_agent`: "Close a sub-agent session." (profile.go:548)

**Evidence:**
```go
// profile.go:516
Description: "Send input to a sub-agent.",
// profile.go:532
Description: "Wait for a sub-agent to finish and return its result.",
// profile.go:548
Description: "Close a sub-agent session.",
```

**Description:** Minor wording differences. Most notably, `close_agent` says "Close" instead of "Terminate", which could slightly alter how models reason about the tool's behavior. The `send_input` description drops "running" which is semantically relevant since the spec implies the tool is for running agents specifically.

---

### GAP-7.03: `wait` tool has extra `timeout_ms` parameter not in spec

**Status:** ACCEPTABLE EXTENSION
**Spec:** `wait` has only `agent_id : String (required)`
**Actual:** `wait` has `agent_id` (required) AND `timeout_ms` (optional Integer)

**Evidence:**
```go
// profile.go:536-539
"properties": map[string]any{
    "agent_id":   map[string]any{"type": "string"},
    "timeout_ms": map[string]any{"type": "integer"},
},
```

```go
// subagents.go:128-143
if timeoutMS <= 0 {
    select {
    case <-ctx.Done():
        return "", ctx.Err()
    case <-done:
    }
} else {
    t := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
    ...
}
```

**Description:** The `timeout_ms` parameter is a useful extension that lets the parent agent poll subagents without blocking indefinitely. The spec does not define this parameter, but it is optional and does not break spec-compliant callers. Not a compliance gap, but worth noting as a deviation.

---

### GAP-7.04: Internal type named `subagent` instead of spec's `SubAgentHandle`

**Status:** INFORMATIONAL (internal naming)
**Spec:** Defines `RECORD SubAgentHandle` with fields `id`, `session`, `status`
**Actual:** Uses unexported `type subagent struct` with equivalent fields

**Evidence:**
```go
// subagents.go:28-39
type subagent struct {
    id   string
    sess *Session
    mu        sync.Mutex
    running   bool
    status    SubAgentStatus
    turnsUsed int
    done      chan struct{}
    result    string
    err       error
}
```

**Description:** The internal struct has the same semantic fields (`id`, `sess`/`session`, `status`) plus additional implementation-specific fields. The type is unexported (`subagent` not `SubAgentHandle`), which is appropriate for Go since it's not part of the public API. The spec's record is a conceptual data model, and the implementation correctly captures all required fields. `SubAgentResult` and `SubAgentStatus` are properly exported and match the spec. No action needed.

---

### GAP-7.05: `working_dir` on non-LocalExecutionEnvironment creates isolated environment

**Status:** MINOR CONCERN
**Spec:** Subagent "Shares the parent's ExecutionEnvironment (same filesystem)"

**Evidence:**
```go
// subagents.go:63-69
if workingDir = strings.TrimSpace(workingDir); workingDir != "" {
    if le, ok := s.env.(*LocalExecutionEnvironment); ok {
        subEnv = le.WithWorkingDirectory(workingDir)
    } else {
        subEnv = NewLocalExecutionEnvironment(workingDir)
    }
}
```

**Description:** When the parent's `ExecutionEnvironment` is not a `*LocalExecutionEnvironment` (e.g., a custom test double or future remote env), and `working_dir` is specified, a brand-new `LocalExecutionEnvironment` is created. This new environment does NOT share PID tracking with the parent, violating the spec's requirement that parent and child share the same execution environment. In practice, the `LocalExecutionEnvironment` type assertion succeeds in all production code paths, so this is only a concern for custom implementations of the `ExecutionEnvironment` interface. The `WithWorkingDirectory` pattern itself correctly shares PID tracking (as tested in `TestSubagent_WorkingDir_SharesParentPIDTracking`).

---

### GAP-7.06: Subagent inherits full `SessionConfig` including MCP configuration

**Status:** MINOR CONCERN
**Spec:** Subagent "Gets its own Session with independent conversation history" and "Shares the parent's ExecutionEnvironment"

**Evidence:**
```go
// subagents.go:55
subCfg := s.cfg
```

Then `NewSession(s.client, subProfile, subEnv, subCfg)` is called, which triggers `s.initMCP()` (session.go:252) for the child session.

**Description:** The subagent inherits the full `SessionConfig` struct including `MCPConfigFiles`, `MCPInline`, `SkillsDirs`, and `SystemPromptAppend` (slice fields shared by reference). The `NewSession` constructor calls `initMCP()`, which will attempt to discover and connect to MCP servers again for the subagent. This means each subagent creates duplicate MCP server connections. The spec does not explicitly address whether subagents should have MCP access, but creating duplicate connections to external servers is wasteful and could cause issues with servers that maintain state. The shared slice references are also a theoretical mutation risk, though no code path currently modifies them after construction.

---

### GAP-7.07: `close_agent` does not wait for running subagent goroutine to complete

**Status:** INFORMATIONAL
**Spec:** `close_agent` "Returns: Final status"

**Evidence:**
```go
// subagents.go:160-183
func (s *Session) closeAgent(agentID string) (any, error) {
    s.mu.Lock()
    sub := s.subagents[agentID]
    delete(s.subagents, agentID)
    s.mu.Unlock()
    ...
    sub.sess.Close()    // cancels context but doesn't wait
    ...
    return string(b), nil
}
```

**Description:** When `close_agent` is called on a still-running subagent, it calls `sub.sess.Close()` which cancels the session context (triggering cancellation of in-flight LLM calls). However, it does not wait for the `run` goroutine to actually finish. The status returned may be "running" instead of a truly final status. The `sub.sess.Close()` will eventually cause the goroutine to exit (the `ProcessInput` call will return with a context cancellation error), but there's a race window where the returned status is stale. For v1 this is acceptable since `close_agent` is typically called after `wait` returns, but calling `close_agent` on a running agent without first waiting could return misleading status.

---

## Compliant Items (No Gaps Found)

### 7.1 Concept - COMPLIANT
- Child session spawned by parent: `NewSession` is called in `spawnAgent` (subagents.go:71)
- Own agentic loop with own conversation history: Verified by `TestSession_Subagent_IndependentHistory` (session_dod_test.go:2976)
- Shares parent's execution environment: `subEnv := s.env` (subagents.go:62), PID sharing verified by test
- Enables parallel work: `go sub.run(ctx, task)` (subagents.go:88)

### 7.2 Spawn Interface - COMPLIANT
All four tools exist with correct names and parameter schemas:
- `spawn_agent`: task (required String), working_dir (optional String), model (optional String), max_turns (optional Integer, default 50) -- all present and correct
- `send_input`: agent_id (required String), message (required String) -- correct (parameter is `message` not `input`)
- `wait`: agent_id (required String) -- correct (plus optional `timeout_ms` extension)
- `close_agent`: agent_id (required String) -- correct
- Return values match spec: spawn returns agent_id + status, wait returns SubAgentResult, close returns final status

### 7.2 Tool Registration Across All Profiles - COMPLIANT
All three profiles (OpenAI, Anthropic, Gemini) include all four subagent tools:
```go
// profile.go - verified in OpenAI (line 227-230), Anthropic (line 257-260), Gemini (line 309-312)
defSpawnAgent(), defSendInput(), defWait(), defCloseAgent(),
```
Verified by `TestProfileToolset` (profile_test.go:56-99).

### 7.3 SubAgentHandle Fields - COMPLIANT
- `id`: `subagent.id` (string)
- `session`: `subagent.sess` (*Session, independent history)
- `status`: `subagent.status` (SubAgentStatus: "running"/"completed"/"failed")
- Verified by `TestSubAgentStatus_Values` (session_dod_test.go:1271-1280)

### 7.3 SubAgentResult Fields - COMPLIANT
```go
type SubAgentResult struct {
    Output    string `json:"output"`
    Success   bool   `json:"success"`
    TurnsUsed int    `json:"turns_used"`
}
```
Matches spec: output (String), success (Boolean), turns_used (Integer).
Verified by `TestSession_WaitAgent_ReturnsSubAgentResult`.

### 7.3 Independent Conversation History - COMPLIANT
Verified by test `TestSession_Subagent_IndependentHistory` (session_dod_test.go:2976-3042).

### 7.3 Shares Parent's ExecutionEnvironment - COMPLIANT
- Without working_dir: `subEnv := s.env` (subagents.go:62) -- direct sharing
- With working_dir: `le.WithWorkingDirectory(workingDir)` shares PID tracking
- Verified by `TestSubagent_WorkingDir_SharesParentPIDTracking` and `TestSession_Subagent_SharedFilesystem`

### 7.3 Uses Parent's ProviderProfile (or overridden model) - COMPLIANT
```go
subProfile := s.profile
if model = strings.TrimSpace(model); model != "" {
    subProfile = s.profile.WithModel(model)
}
```
Verified by `TestSession_SpawnAgent_ModelOverride` (session_dod_test.go:1283-1337).

### 7.3 Turn Limits (default 50) - COMPLIANT
```go
if maxTurns > 0 {
    subCfg.MaxTurns = maxTurns
} else {
    subCfg.MaxTurns = 50
}
```
Default is 50, not inherited from parent. Verified by:
- `TestSession_SpawnAgent_MaxTurns` (custom value works)
- `TestSubagent_MaxTurns_DefaultsTo50_NotInheritedFromParent` (default 50, not parent's value)

### 7.3 Depth Limiting (default max depth: 1) - COMPLIANT
```go
// session.go:87-89
if c.MaxSubagentDepth <= 0 {
    c.MaxSubagentDepth = 1
}

// subagents.go:43-47
depth := s.depth
maxDepth := s.cfg.MaxSubagentDepth
if depth >= maxDepth {
    return "", fmt.Errorf("subagent depth limit reached")
}
```
Default max depth is 1, configurable via `SessionConfig.MaxSubagentDepth`.
Verified by `TestSession_Subagents_SpawnWaitClose_AndDepthLimit` (session_dod_test.go:1039-1046).

### 7.3 Configurable via `max_subagent_depth` - COMPLIANT
`SessionConfig.MaxSubagentDepth` is a public field with JSON tag `"max_subagent_depth,omitempty"`.
Not exposed via CLI flag, but configurable programmatically. The spec says "configurable" without specifying the mechanism.

### send_input Steers Running Agents - COMPLIANT
```go
if running {
    sub.sess.Steer(input)
    return "ok", nil
}
```
Verified by `TestSendInput_SteersRunningAgent` (session_dod_test.go:3260-3306).

---

## Test Coverage Summary

| Requirement | Test |
|---|---|
| Spawn/Wait/Close lifecycle | `TestSession_Subagents_SpawnWaitClose_AndDepthLimit` |
| Spawn returns agent_id + status | `TestSession_SpawnAgent_ReturnsStatus` |
| Wait returns SubAgentResult | `TestSession_WaitAgent_ReturnsSubAgentResult` |
| send_input uses `message` param | `TestSession_SendInput_UsesMessageParam` |
| send_input steers running agents | `TestSendInput_SteersRunningAgent` |
| max_turns parameter | `TestSession_SpawnAgent_MaxTurns` |
| max_turns defaults to 50 | `TestSubagent_MaxTurns_DefaultsTo50_NotInheritedFromParent` |
| Model override | `TestSession_SpawnAgent_ModelOverride` |
| Status values | `TestSubAgentStatus_Values` |
| Depth limiting | `TestSession_Subagents_SpawnWaitClose_AndDepthLimit` |
| Independent history | `TestSession_Subagent_IndependentHistory` |
| Shared filesystem | `TestSession_Subagent_SharedFilesystem` |
| PID tracking shared | `TestSubagent_WorkingDir_SharesParentPIDTracking` |
| close_agent structured result | `TestCloseAgent_ReturnsStructuredStatus` |
| Integration (end-to-end) | `TestSmoke_SubagentFlow` |
| All profiles have tools | `TestProfileToolset` |

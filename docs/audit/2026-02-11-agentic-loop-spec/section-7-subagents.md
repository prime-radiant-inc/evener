# Section 7: Subagents - Audit Findings

## Summary
7 gaps found

## Findings

### GAP-7.01: No named `SubAgentHandle` type
- **Spec requirement:** "RECORD SubAgentHandle: id: String, session: Session, status: 'running' | 'completed' | 'failed'" (spec line 1090-1093)
- **Current state:** The implementation uses an unexported `subagent` struct (subagents.go:28) with fields `id`, `sess`, `status`, `running`, `turnsUsed`, `done`, `result`, `err`. There is no exported `SubAgentHandle` type. The internal struct has all the required fields (id, session reference, status enum), but the naming does not match the spec. The status field uses a properly-typed `SubAgentStatus` enum with the three correct values.
- **Severity:** Minor
- **Files checked:** `internal/agent/subagents.go`

### GAP-7.02: `close_agent` returns bare string instead of final status
- **Spec requirement:** "TOOL close_agent: ... returns: Final status" (spec line 1080-1084)
- **Current state:** `closeAgent` (subagents.go:149-159) returns the string `"closed"` on success. It does not return structured information about the agent's final status (running/completed/failed), output, or turns used. The agent's status, result, and error are discarded when `sub.sess.Close()` is called. For comparison, `waitAgent` returns a proper `SubAgentResult` JSON.
- **Severity:** Important
- **Files checked:** `internal/agent/subagents.go`

### GAP-7.03: `max_turns` default not always 50 -- inherits parent's value
- **Spec requirement:** "max_turns: Integer (optional) -- turn limit (default: 50)" (spec line 1064) and "Has its own turn limits (configurable, default: 50)" (spec line 1105)
- **Current state:** In `spawnAgent` (subagents.go:55-60), when `maxTurns` is 0 (not specified by the caller), the code only sets `subCfg.MaxTurns = 50` if `subCfg.MaxTurns <= 0`. If the parent session has a non-zero `MaxTurns` (e.g., 100), the subagent inherits it rather than defaulting to 50. The spec states the default is unconditionally 50 when not specified.
- **Severity:** Important
- **Files checked:** `internal/agent/subagents.go`

### GAP-7.04: `wait` tool has undocumented `timeout_ms` parameter
- **Spec requirement:** "TOOL wait: parameters: agent_id: String (required)" (spec line 1074-1078) -- only `agent_id` is specified
- **Current state:** The `defWait()` definition (profile.go:521-535) and `waitAgent` implementation (subagents.go:108-147) include an optional `timeout_ms` parameter not present in the spec. This is a useful extension but is not spec-compliant. The implementation handles timeout=0 as "wait indefinitely."
- **Severity:** Info
- **Files checked:** `internal/agent/profile.go`, `internal/agent/subagents.go`

### GAP-7.05: Tool descriptions differ from spec
- **Spec requirement:** Exact descriptions specified in spec lines 1058-1084:
  - `spawn_agent`: "Spawn a subagent to handle a scoped task autonomously."
  - `send_input`: "Send a message to a running subagent."
  - `wait`: "Wait for a subagent to complete and return its result."
  - `close_agent`: "Terminate a subagent."
- **Current state:** Implementation descriptions in `profile.go`:
  - `spawn_agent`: "Spawn a sub-agent to work on a scoped task." (missing "autonomously", uses "sub-agent" vs "subagent")
  - `send_input`: "Send input to a sub-agent." (says "input" not "message", says "sub-agent" not "running subagent")
  - `wait`: "Wait for a sub-agent to finish and return its result." (uses "finish" vs "complete", "sub-agent" vs "subagent")
  - `close_agent`: "Close a sub-agent session." (says "Close" not "Terminate", says "session" not just agent)
- **Severity:** Minor
- **Files checked:** `internal/agent/profile.go`

### GAP-7.06: `working_dir` creates new ExecutionEnvironment instead of sharing parent's
- **Spec requirement:** "Shares the parent's `ExecutionEnvironment` (same filesystem)" (spec line 1103)
- **Current state:** When `working_dir` is provided, `spawnAgent` (subagents.go:62-65) calls `NewLocalExecutionEnvironment(workingDir)` creating an entirely new environment instance. This new environment has its own PID tracker (`runningPIDs sync.Map`), which means processes spawned by the subagent are not tracked by the parent's cleanup logic. When `working_dir` is omitted, the parent's `ExecutionEnvironment` is correctly shared (`subEnv := s.env`). The spec's intent of "same filesystem" is satisfied in either case, but full sharing of the `ExecutionEnvironment` object (and its process tracking) is lost when `working_dir` is specified.
- **Severity:** Minor
- **Files checked:** `internal/agent/subagents.go`, `internal/agent/env_local.go`

### GAP-7.07: `send_input` semantics inverted from spec description
- **Spec requirement:** "TOOL send_input: description: 'Send a message to a running subagent.'" (spec line 1067-1072)
- **Current state:** The `sendInput` implementation (subagents.go:90-106) explicitly rejects calls when the agent IS running (`if sub.running { return "", fmt.Errorf("agent is already running") }`). It only works when the agent has finished its current task and is idle. The tool effectively means "give the idle subagent a new task", not "send a message to a running subagent." This is functionally useful (it allows multi-round interaction with a subagent) but the semantics are opposite to the spec description. The spec implies injecting a message into an active agent, while the implementation starts a new run on an idle agent.
- **Severity:** Important
- **Files checked:** `internal/agent/subagents.go`

## Fully Implemented (Verified)

- **7.1 Concept:** Subagent is a child session with its own agentic loop and independent conversation history. Shares the parent's filesystem. Verified by `TestSession_Subagent_IndependentHistory` and `TestSession_Subagent_SharedFilesystem` in `session_dod_test.go`.

- **7.2 Spawn interface - 4 tools registered:** All four tools (`spawn_agent`, `send_input`, `wait`, `close_agent`) are defined in `profile.go` and registered in `session.go:registerCoreTools`. They are included in all three provider profiles (OpenAI, Anthropic, Gemini). Verified in `profile_test.go`.

- **7.2 spawn_agent parameters:** All four parameters present: `task` (required), `working_dir` (optional), `model` (optional), `max_turns` (optional). Types correct (string, string, string, integer). Verified in `profile.go:defSpawnAgent()`.

- **7.2 spawn_agent returns agent ID and initial status:** Returns JSON `{"agent_id":"...","status":"running"}`. Verified by `TestSession_SpawnAgent_ReturnsStatus` in `session_dod_test.go`.

- **7.2 send_input parameters:** Both `agent_id` (required) and `message` (required) present. Verified in `profile.go:defSendInput()`.

- **7.2 send_input returns acknowledgement:** Returns `"ok"` on success. Verified in `subagents.go:105`.

- **7.2 wait returns SubAgentResult:** Returns JSON with `output`, `success`, and `turns_used` fields. Verified by `TestSession_WaitAgent_ReturnsSubAgentResult` in `session_dod_test.go`.

- **7.3 SubAgentResult record:** `SubAgentResult` struct (subagents.go:22-26) has all three fields: `Output string`, `Success bool`, `TurnsUsed int` with correct JSON tags.

- **7.3 SubAgentStatus enum:** Three values present: `SubAgentRunning = "running"`, `SubAgentCompleted = "completed"`, `SubAgentFailed = "failed"`. Verified by `TestSubAgentStatus_Values` in `session_dod_test.go`.

- **7.3 Independent Session:** Subagent gets its own `Session` via `NewSession()` with independent history. Parent history is unaffected. Verified by `TestSession_Subagent_IndependentHistory`.

- **7.3 Shares parent's ExecutionEnvironment (when working_dir omitted):** `subEnv := s.env` directly assigns parent's env. Verified in `subagents.go:62`.

- **7.3 Uses parent's ProviderProfile (or overridden model):** `subProfile := s.profile` with optional `WithModel(model)` override. Verified by `TestSession_SpawnAgent_ModelOverride` in `session_dod_test.go`.

- **7.3 Depth limiting:** `depth >= maxDepth` check in `spawnAgent` (subagents.go:46-48). Child gets `depth = parent.depth + 1`. Default `MaxSubagentDepth = 1`. Configurable via `SessionConfig.MaxSubagentDepth`. Verified by `TestSession_Subagents_SpawnWaitClose_AndDepthLimit` in `session_dod_test.go`.

- **7.3 max_subagent_depth configurable:** `SessionConfig.MaxSubagentDepth` field exists with JSON tag `max_subagent_depth`. Default set in `applyDefaults()` (session.go:87-89).

- **7.3 Default max depth = 1:** Verified in `session.go:88`: `c.MaxSubagentDepth = 1`.

- **7.4 Use cases - architecture supports them:** Parallel exploration (spawn multiple agents concurrently, each runs in its own goroutine), focused refactoring (working_dir scoping), test execution (independent session), alternative approaches (spawn multiple, wait for results). All architecturally supported.

- **Session.Close() cleans up subagents:** `Close()` (session.go:419-453) iterates all subagents and calls `sub.sess.Close()` on each.

- **send_input actually works:** Verified by `TestSession_SendInput_UsesMessageParam` in `session_dod_test.go` -- sends a second message after first task completes, agent processes it successfully.

# Replace `communicate` with `submit_result` + Reviewer Gate

## Context

Serf's `communicate(success)` tool lets agents claim task completion without structural
verification. Benchmark analysis (val6: largest-eigenval) showed an agent calling
`communicate(success)` despite eval.py showing subpar results and never running pytest.
The `communicate(status)` action is also being removed -- status updates cause agents to
lose momentum and stop prematurely.

**Design**: Replace `communicate` with a single `submit_result` tool. At depth 0 (top-level
sessions), calling `submit_result` spawns a reviewer subagent that validates the work before
accepting. At depth > 0 (subagents), it passes through directly (no review, prevents recursion).
If the reviewer rejects, feedback is returned as the tool result and the agent continues working.

---

## Phase 1: Mechanical rename (`communicate` -> `submit_result`)

No behavior change. Every test stays green after each step.

### Task 1.1: Rename tool definition (`agent/profile.go`)

- Rename `defCommunicate()` -> `defSubmitResult()`
- Change tool name from `"communicate"` to `"submit_result"`
- Remove `action` parameter (no more status/success enum)
- Keep `message` (string, required) and `output` (optional structured object)
- Update `Description` for submit_result semantics

### Task 1.2: Rename tool handler (`agent/session.go:2177-2248`)

- Use `defSubmitResult()` in registration
- Remove `action` switch -- handler always behaves like old `success` path
- Remove `"status"` case and `"result"` backward-compat alias
- Validation: require `message` or `output` (same as old success)

### Task 1.3: Rename event (`agent/events.go`)

- `EventCommunicate` -> `EventSubmitResult`
- `CommunicateData` -> `SubmitResultData` (drop `Action` field, keep `Message`)

### Task 1.4: Rename in tool registry (`agent/tool_registry.go`)

- `Restrict()`: `allowed["communicate"]` -> `allowed["submit_result"]`
- `defaultToolOutputLimit`: `case "communicate"` -> `case "submit_result"`

### Task 1.5: Rename in context manager (`agent/context_manager.go`)

- Line 337: `tr.Name == "communicate"` -> `"submit_result"`
- Line 458: `case "communicate"` -> `case "submit_result"`

### Task 1.6: Rename tool gating (`agent/session.go:1557-1582`)

- `hideCommunicate` -> `hideSubmitResult`
- `canonical == "communicate"` -> `canonical == "submit_result"`
- Comment on `MinResultRound` field: update reference

### Task 1.7: Rename hook reason (`agent/session.go:1447`)

- `hi.Reason = "communicate_result"` -> `"submit_result"`

### Task 1.8: Rename subagent nudge (`agent/subagents.go`)

- `communicateNudge` -> `submitResultNudge`
- Update nudge text to reference `submit_result`

### Task 1.9: Rename profile overrides (`agent/profile_overrides.go`)

- `WithCommunicateRequiredDataKeys` -> `WithSubmitResultRequiredDataKeys`
- `defCommunicateWithRequiredDataKeys` -> `defSubmitResultWithRequiredDataKeys`
- Internal `"communicate"` refs -> `"submit_result"`

### Task 1.10: Rename in prompts

Files:
- `agent/prompts/base.md` -- replace `## communicate` section with `## submit_result`,
  remove all status guidance, keep verification hard gate
- `agent/prompts/subagent_base.md` -- `communicate(success)` -> `submit_result`
- `agent/agents/reviewer.md` -- line 57
- `agent/agents/explorer.md`, `implementer.md`, `planner.md`, `test-writer.md` -- all refs

### Task 1.11: Rename in CLI (`cmd/serf/run.go`)

- `EventCommunicate` -> `EventSubmitResult`
- Remove status-action check (no more status)

### Task 1.12: Rename in TUI (`cmd/serf-tui/`)

- `tool_summary.go`, `model.go`, `message.go`, `styles.go` -- all `"communicate"` refs
- Rename Go identifiers: `extractCommunicateMessage` -> `extractSubmitResultMessage`, etc.
- Update tests in `message_test.go`, `tool_summary_test.go`, `theme_test.go`

### Task 1.13: Rename in all test files

Key files:
- `agent/session_communicate_test.go` -> rename file, update all helpers/tests
- `agent/session_dod_test.go`, `session_parity_test.go`, `session_empty_response_test.go`
- `agent/profile_test.go`, `profile_overrides_test.go`, `builtin_agents_test.go`
- `agent/context_manager_test.go`, `prompt_resolver_test.go`
- `agent/plugin_agents_integration_test.go`, `plugin_integration_live_test.go`

### Task 1.14: Verify + commit

- `go test ./... -count=1` -- all green
- `go build ./cmd/serf/ && go build ./cmd/serf-tui/`
- Commit: "rename communicate to submit_result (mechanical, no behavior change)"

---

## Phase 2: Implement reviewer gate (new behavior)

### Task 2.1: Write failing tests (`agent/session_submit_result_test.go`)

Tests to write (all RED initially):

1. **TestSubmitResult_Depth0_ReviewerPass**: At depth 0, submit_result spawns reviewer.
   Reviewer returns PASS. Tool returns `{"accepted": true}`. Session exits.

2. **TestSubmitResult_Depth0_ReviewerFail**: Reviewer returns FAIL with feedback.
   Tool returns `{"accepted": false, "feedback": "..."}`. `resultDelivered` NOT set.
   Agent loop continues. Second submit_result → reviewer PASS → session exits.

3. **TestSubmitResult_DepthGt0_Passthrough**: At depth > 0 (subagent), submit_result
   sets `resultDelivered` immediately. No reviewer spawned.

4. **TestSubmitResult_ReviewerError_FailOpen**: Reviewer errors/crashes → accept result
   (fail-open). Return `{"accepted": true, "reviewer_error": "..."}`.

5. **TestSubmitResult_ReviewerGetsOriginalTask**: Reviewer prompt contains the original
   task text from session history.

### Task 2.2: Implement helper `extractOriginalTask()` (`agent/session.go`)

```go
func (s *Session) extractOriginalTask() string
```

Walk `s.history` for first `TurnUserInput`, extract text. Fall back to
`s.cfg.SubagentTask` if compaction removed it.

### Task 2.3: Implement `spawnReviewer()` (`agent/session.go`)

```go
type reviewVerdict struct {
    Pass     bool
    Feedback string
}
func (s *Session) spawnReviewer(ctx context.Context, prompt string) (reviewVerdict, error)
```

- Create a reviewer session at `s.depth + 1` (so its submit_result passes through)
- System prompt: `SubagentBasePrompt() + "\n\n" + embeddedReviewerPrompt`
- Tools: glob, grep, read_file, shell (read-only) + submit_result (auto-kept)
- Max turns: 20
- Run `ProcessInput(ctx, prompt)` synchronously
- Parse PASS/FAIL from result text

The reviewer prompt (`agents/reviewer.md`) is already embedded in the binary. Access via
the `builtinAgents` map or embed it directly as a constant.

### Task 2.5: Wire reviewer gate into submit_result handler

In the submit_result tool handler:

```
if s.depth > 0:
    → passthrough (set resultDelivered, return accepted: true)
if s.depth == 0:
    → compose reviewer prompt with: original task + claimed result
    → call spawnReviewer() synchronously (reviewer has shell access for git diff if needed)
    → if error: fail-open (accept, include reviewer_error)
    → if PASS: set resultDelivered, return accepted: true
    → if FAIL: return accepted: false + feedback (do NOT set resultDelivered)
```

### Task 2.6: Verify all tests pass + commit

- `go test ./agent/... -count=1`
- Commit: "implement reviewer gate for submit_result at depth 0"

---

## Phase 3: Edge cases + cleanup

### Task 3.1: Grep audit

- `grep -r "communicate" agent/ cmd/` -> zero hits (except comments explaining the rename)
- Fix any stragglers

### Task 3.2: Build + full test verification

- `go test ./... -count=1` -- all green
- `go build ./cmd/serf/ && go build ./cmd/serf-tui/` -- clean
- Commit any fixes

---

## Files Modified

| File | Change |
|------|--------|
| `agent/profile.go` | `defCommunicate()` -> `defSubmitResult()`, remove action param |
| `agent/session.go` | Replace handler, add reviewer gate + helpers |
| `agent/events.go` | Rename event + data struct |
| `agent/context_manager.go` | Update string refs |
| `agent/tool_registry.go` | Update always-allowed name + output limit |
| `agent/subagents.go` | Update nudge constant + text |
| `agent/profile_overrides.go` | Rename functions + internal refs |
| `agent/prompts/base.md` | Replace communicate section with submit_result |
| `agent/prompts/subagent_base.md` | Update tool references |
| `agent/agents/*.md` | Update communicate refs in all 5 agent prompts |
| `cmd/serf/run.go` | Update event handler |
| `cmd/serf-tui/*.go` | Update all communicate refs |
| `agent/*_test.go` | Update ~10 test files |

## Design Decisions

1. **Fail-open on reviewer error**: Broken reviewer shouldn't block work completion.
   Agent already did the work. Log the error, accept the result.

2. **Depth > 0 passthrough**: Prevents infinite recursion. Reviewer calls submit_result
   at depth 1+, passes through directly.

3. **PASS/FAIL text parsing**: Match existing `reviewer.md` contract. String matching
   is robust enough -- reviewer always outputs `**PASS**` or `**FAIL**`.

4. **No git diff in prompt**: Reviewer has shell access and can run `git diff` itself
   if it needs to see changes. No need to front-load potentially large diffs.

5. **No separate reviewer event**: Reviewer is an implementation detail of submit_result.
   Observability can be added later if needed.

## Verification

1. `go test ./... -count=1` -- all tests pass
2. `go build ./cmd/serf/ && go build ./cmd/serf-tui/` -- clean builds
3. Local smoke test: run serf on a simple task, verify reviewer spawns and approves
4. Deploy to flower-garden, run benchmark tasks to compare pass rate
